// auth_sso.go — bidirectional SSO between the admin console and webmail.
// Mirror of webmail/backend/sso.go; same operator-mail-bridge.key, same
// HMAC ticket format, same 60s TTL + nonce-replay defense.
//
// Identity convention: operator.username on this host ↔ mailbox
// `<username-lowercased>@example.com` on pop-03. If the operator doesn't
// exist (or isn't approved), ingest fails 404 / 403.
//
// Two endpoints:
//   - GET  /api/v1/auth/sso/ingest?t=<ticket>  — accept a webmail-minted
//     ticket → issue an admin session → 302 to /admin
//   - POST /api/v1/auth/sso/mail-ticket        — for a logged-in operator,
//     mint a ticket the webmail can ingest → return { url, exp }
package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	ssoIntentToMail  = "sso-mail"  // admin → webmail
	ssoIntentToAdmin = "sso-admin" // webmail → admin
	ssoTicketTTL     = 60 * time.Second
	ssoTicketSkew    = 5 * time.Second
	ssoMailDomain    = "example.com"
	ssoMailHost      = "mail.example.com"
)

type ssoTicket struct {
	Intent   string `json:"intent"`
	Operator string `json:"operator"`
	Mailbox  string `json:"mailbox"`
	Iat      int64  `json:"iat"`
	Exp      int64  `json:"exp"`
	Nonce    string `json:"nonce"`
}

type ssoNonceCache struct {
	mu   sync.Mutex
	seen map[string]int64
}

func newSSONonceCache() *ssoNonceCache {
	return &ssoNonceCache{seen: make(map[string]int64)}
}

func (c *ssoNonceCache) claim(nonce string, exp int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().Unix()
	for n, e := range c.seen {
		if e <= now {
			delete(c.seen, n)
		}
	}
	if _, dup := c.seen[nonce]; dup {
		return false
	}
	c.seen[nonce] = exp
	return true
}

type ssoBridge struct {
	auth    *authStore
	bridge  *mailBridgeService
	nonces  *ssoNonceCache
}

func newSSOBridge(auth *authStore, bridge *mailBridgeService) *ssoBridge {
	return &ssoBridge{
		auth:   auth,
		bridge: bridge,
		nonces: newSSONonceCache(),
	}
}

// GET /api/v1/auth/sso/ingest?t=<ticket>
// Verifies a webmail-minted ticket and issues an admin session for the
// mapped operator. 302 → /admin on success.
func (s *ssoBridge) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET only"})
		return
	}
	if s.bridge == nil || len(s.bridge.key) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false,
			Error: "operator-bridge key not installed on this host"})
		return
	}
	tok := r.URL.Query().Get("t")
	if tok == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing ticket"})
		return
	}
	t, err := verifySSOTicket(tok, s.bridge.key, ssoIntentToAdmin, s.nonces)
	if err != nil {
		log.Printf("sso-ingest: rejected: %v", err)
		// Don't dump raw JSON to the browser — the user landed here from
		// a click in webmail and expects a page, not an API response.
		// Bounce to /login with a hint flag so the SPA can render a
		// non-scary explanation.
		http.Redirect(w, r, "/login?sso_err=invalid", http.StatusFound)
		return
	}
	// Operator with this username must exist + be approved on this host.
	s.auth.mu.RLock()
	op, exists := s.auth.operators[t.Operator]
	s.auth.mu.RUnlock()
	if !exists {
		log.Printf("sso-ingest: no operator record for username=%q", t.Operator)
		http.Redirect(w, r, "/login?sso_err=no-operator", http.StatusFound)
		return
	}
	if !op.Approved {
		http.Redirect(w, r, "/login?sso_err=pending", http.StatusFound)
		return
	}
	// Issue session via the shared helper so cookie/token details
	// (TTL, HttpOnly, SameSite, HMAC) match the password + passkey paths.
	if _, err := s.auth.setSessionCookie(w, r, op.Username); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "session issue: " + err.Error()})
		return
	}
	log.Printf("sso-ingest: operator=%s (from mailbox=%s) signed in via SSO", t.Operator, t.Mailbox)
	auditRecord(r, AuditEvent{
		Event: "login.ok", Actor: t.Operator,
		Details: map[string]any{"path": "sso-from-webmail", "mailbox": t.Mailbox},
	})
	http.Redirect(w, r, "/admin", http.StatusFound)
}

// POST /api/v1/auth/sso/mail-ticket
// For a logged-in operator, mints a ticket the webmail can ingest.
// Mapped mailbox is `<operator-username-lowercased>@example.com`. The
// webmail side checks dovecot for the mailbox's existence.
func (s *ssoBridge) handleMintMailTicket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	if s.bridge == nil || len(s.bridge.key) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false,
			Error: "operator-bridge key not installed on this host"})
		return
	}
	c, ok := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if !ok || c == nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	operator := strings.ToLower(strings.TrimSpace(c.Sub))
	mailbox := operator + "@" + ssoMailDomain
	tok, exp, err := mintSSOTicket(s.bridge.key, ssoIntentToMail, operator, mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	url := "https://" + ssoMailHost + "/api/v1/mail/sso/ingest?t=" + tok
	log.Printf("sso-ticket: minted mail-ingest ticket for operator=%s mailbox=%s exp=%s",
		operator, mailbox, time.Unix(exp, 0).UTC().Format(time.RFC3339))
	auditRecord(r, AuditEvent{
		Event: "sso.mail-ticket.mint", Actor: operator, Target: mailbox,
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"url":        url,
		"mailbox":    mailbox,
		"expires_at": time.Unix(exp, 0).UTC().Format(time.RFC3339),
	}})
}

// GET /api/v1/auth/sso-out?target=mail
// Convenience entry for the "Sign in with NCN Admin" button on the
// webmail login page. If the visitor already has an admin session
// cookie, we mint a mail ticket server-side and 302 straight into
// webmail. Otherwise we bounce through /login with a ?return= so the
// SPA can replay this URL after the operator authenticates.
func (s *ssoBridge) handleSSOOut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET only"})
		return
	}
	target := r.URL.Query().Get("target")
	if target != "mail" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "unsupported sso target"})
		return
	}
	if s.bridge == nil || len(s.bridge.key) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false,
			Error: "operator-bridge key not installed on this host"})
		return
	}

	selfURL := "/api/v1/auth/sso-out?target=mail"

	// Probe the session cookie. If absent or invalid, bounce to the
	// login UI with ?next= so the SPA replays this URL after a
	// successful sign-in. We use `next` because that's the SPA's
	// existing convention (see Login.vue's route.query.next lookup).
	ck, err := r.Cookie(cookieName)
	if err != nil || ck.Value == "" {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(selfURL), http.StatusFound)
		return
	}
	claims, err := s.auth.verifyToken(ck.Value)
	if err != nil {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(selfURL), http.StatusFound)
		return
	}

	// Confirmed authenticated. Mint the ticket and redirect.
	operator := strings.ToLower(strings.TrimSpace(claims.Sub))
	mailbox := operator + "@" + ssoMailDomain
	tok, _, err := mintSSOTicket(s.bridge.key, ssoIntentToMail, operator, mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	log.Printf("sso-out: operator=%s → webmail SSO (mailbox=%s)", operator, mailbox)
	auditRecord(r, AuditEvent{
		Event: "sso.out-to-mail", Actor: operator, Target: mailbox,
	})
	http.Redirect(w, r, "https://"+ssoMailHost+"/api/v1/mail/sso/ingest?t="+tok, http.StatusFound)
}

func mintSSOTicket(key []byte, intent, operator, mailbox string) (string, int64, error) {
	nonceB := make([]byte, 8)
	if _, err := rand.Read(nonceB); err != nil {
		return "", 0, err
	}
	now := time.Now().Unix()
	exp := now + int64(ssoTicketTTL.Seconds())
	payload := ssoTicket{
		Intent: intent, Operator: operator, Mailbox: mailbox,
		Iat: now, Exp: exp, Nonce: hex.EncodeToString(nonceB),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", 0, err
	}
	pb := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(pb))
	sb := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return pb + "." + sb, exp, nil
}

func verifySSOTicket(tok string, key []byte, expectIntent string, nonces *ssoNonceCache) (*ssoTicket, error) {
	dot := strings.IndexByte(tok, '.')
	if dot < 1 || dot == len(tok)-1 {
		return nil, errors.New("malformed ticket")
	}
	pb, sb := tok[:dot], tok[dot+1:]
	raw, err := base64.RawURLEncoding.DecodeString(pb)
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	want, err := base64.RawURLEncoding.DecodeString(sb)
	if err != nil {
		return nil, fmt.Errorf("decode sig: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(pb))
	if !hmac.Equal(want, mac.Sum(nil)) {
		return nil, errors.New("bad signature")
	}
	var t ssoTicket
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("parse payload: %w", err)
	}
	if t.Intent != expectIntent {
		return nil, fmt.Errorf("wrong intent (want %s got %s)", expectIntent, t.Intent)
	}
	now := time.Now().Unix()
	skew := int64(ssoTicketSkew.Seconds())
	if t.Iat > now+skew {
		return nil, errors.New("ticket from the future")
	}
	if t.Exp < now-skew {
		return nil, errors.New("ticket expired")
	}
	if t.Operator == "" || t.Mailbox == "" || t.Nonce == "" {
		return nil, errors.New("incomplete payload")
	}
	if !nonces.claim(t.Nonce, t.Exp) {
		return nil, errors.New("nonce replay")
	}
	return &t, nil
}
