// sso.go — bidirectional SSO between the NCN admin console (on tyo) and
// webmail (this host).
//
// Trust model: same operator-mail-bridge.key (HMAC-SHA256, 32 bytes,
// /etc/ncn-mail/operator-bridge.key on pop-03 ↔ /etc/ncn-core-console/
// operator-mail-bridge.key on tyo) that already gates self-invite,
// role-recover, and forgot-bridge. SSO tickets are end-to-end HMAC-
// signed JSON payloads with a 60s TTL window + random nonce, kept
// briefly in an in-memory replay-defense cache.
//
// Identity convention: an operator on the admin console (key `username`
// in operator.json) maps to a mailbox `<username-lowercased>@<domain>`
// on this host. If the target mailbox doesn't exist in dovecot, SSO
// fails with a clear 404 so the operator self-registers first.
//
// Two endpoints:
//   - GET  /api/v1/mail/sso/ingest?t=<ticket>   — accept an admin-minted
//     ticket → issue a webmail session cookie → 302 to /
//   - POST /api/v1/mail/sso/admin-ticket        — for a logged-in mailbox,
//     mint a ticket the admin console can ingest → return { url, exp }
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
	"strings"
	"sync"
	"time"
)

const (
	ssoIntentToMail  = "sso-mail"  // admin → webmail
	ssoIntentToAdmin = "sso-admin" // webmail → admin
	ssoTicketTTL     = 60 * time.Second
	ssoTicketSkew    = 5 * time.Second
	ssoAdminHost     = "admin.example.com"
)

type ssoTicket struct {
	Intent   string `json:"intent"`
	Operator string `json:"operator"` // admin-side username (lowercased)
	Mailbox  string `json:"mailbox"`  // full <local>@<domain> form
	Iat      int64  `json:"iat"`
	Exp      int64  `json:"exp"`
	Nonce    string `json:"nonce"`
}

// ssoNonceCache tracks recently-consumed nonces so a captured ticket
// can't be reused within its 60s window. Sweeps stale entries lazily.
type ssoNonceCache struct {
	mu   sync.Mutex
	seen map[string]int64 // nonce → expiry-unix
}

func newSSONonceCache() *ssoNonceCache {
	return &ssoNonceCache{seen: make(map[string]int64)}
}

// claim returns true if the nonce was fresh (and registers it). Returns
// false if it was already used within the TTL window.
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

type ssoService struct {
	mailSvc    *mailService
	invites    *inviteStore // for bridgeKey access
	nonceCache *ssoNonceCache
}

func newSSOService(mailSvc *mailService, invites *inviteStore) *ssoService {
	return &ssoService{
		mailSvc:    mailSvc,
		invites:    invites,
		nonceCache: newSSONonceCache(),
	}
}

// GET /api/v1/mail/sso/ingest?t=<ticket>
// Verifies an admin-minted ticket and issues a webmail session for the
// mapped mailbox. Returns a 302 on success.
func (s *ssoService) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET only"})
		return
	}
	if len(s.invites.bridgeKey) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false,
			Error: "operator-bridge key not installed on this host"})
		return
	}
	tok := r.URL.Query().Get("t")
	if tok == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing ticket"})
		return
	}
	t, err := verifySSOTicket(tok, s.invites.bridgeKey, ssoIntentToMail, s.nonceCache)
	if err != nil {
		log.Printf("sso-ingest: rejected: %v", err)
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "invalid sso ticket"})
		return
	}
	if !mailboxExistsInDovecot(t.Mailbox) {
		writeJSON(w, http.StatusNotFound, envelope{OK: false,
			Error: "no webmail mailbox for operator " + t.Operator + " — self-register first"})
		return
	}
	// SSO is implicit "remember" — the operator already proved identity
	// on the admin side, so we don't ask for the ✓ checkbox again.
	if err := s.mailSvc.issueSession(w, r, t.Mailbox, true); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "session issue: " + err.Error()})
		return
	}
	log.Printf("sso-ingest: %s (from operator=%s) signed in via SSO", t.Mailbox, t.Operator)
	http.Redirect(w, r, "/", http.StatusFound)
}

// POST /api/v1/mail/sso/admin-ticket
// For a logged-in mailbox, mints a ticket the admin console can verify
// to start a parallel admin session. The mapped operator username is
// `<mailbox-local-part-lowercased>`; the admin side decides whether an
// operator record with that username exists and is approved.
func (s *ssoService) handleMintAdminTicket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	if len(s.invites.bridgeKey) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false,
			Error: "operator-bridge key not installed on this host"})
		return
	}
	c, ok := mailClaimsFromRequest(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	mailbox := strings.ToLower(strings.TrimSpace(c.Mailbox))
	local := mailbox
	if i := strings.Index(mailbox, "@"); i >= 0 {
		local = mailbox[:i]
	}

	// Role mailboxes (postmaster/noc/hostmaster/abuse/security) are
	// SHARED — there's no 1:1 operator who "owns" them, so we can't
	// silently map noc@ → operator "noc". Instead, hand the user the
	// plain admin /login URL; they sign in with their personal operator
	// credentials. Returning a `not_sso` flag lets the frontend tweak
	// the UX copy if needed.
	if _, isRole := roleMailboxes[mailbox]; isRole {
		log.Printf("sso-ticket: %s is a role mailbox; redirecting to admin /login instead of SSO",
			mailbox)
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
			"url":     "https://" + ssoAdminHost + "/login",
			"not_sso": true,
			"reason":  "role-mailbox",
		}})
		return
	}

	tok, exp, err := mintSSOTicket(s.invites.bridgeKey, ssoIntentToAdmin, local, mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	url := "https://" + ssoAdminHost + "/api/v1/auth/sso/ingest?t=" + tok
	log.Printf("sso-ticket: minted admin-ingest ticket for mailbox=%s operator=%s exp=%s",
		mailbox, local, time.Unix(exp, 0).UTC().Format(time.RFC3339))
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"url":        url,
		"operator":   local,
		"expires_at": time.Unix(exp, 0).UTC().Format(time.RFC3339),
	}})
}

// mintSSOTicket builds an HMAC-signed ticket payload. Used by webmail
// when sending the user to admin; the admin side has its own minter for
// the other direction.
func mintSSOTicket(key []byte, intent, operator, mailbox string) (string, int64, error) {
	nonceB := make([]byte, 8)
	if _, err := rand.Read(nonceB); err != nil {
		return "", 0, err
	}
	now := time.Now().Unix()
	exp := now + int64(ssoTicketTTL.Seconds())
	payload := ssoTicket{
		Intent:   intent,
		Operator: operator,
		Mailbox:  mailbox,
		Iat:      now,
		Exp:      exp,
		Nonce:    hex.EncodeToString(nonceB),
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

// verifySSOTicket checks signature + intent + clock window + nonce
// freshness. Returns the parsed payload on success.
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
	if t.Mailbox == "" || t.Operator == "" || t.Nonce == "" {
		return nil, errors.New("incomplete payload")
	}
	if !nonces.claim(t.Nonce, t.Exp) {
		return nil, errors.New("nonce replay")
	}
	return &t, nil
}
