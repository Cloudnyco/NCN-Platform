// mail.go — webmail logic for ncn-mail.
//
// Mounts the user's IMAP mailbox on the local dovecot instance (loopback to
// :993, implicit TLS — even on loopback, because dovecot wants its full
// TLS handshake) and forwards SMTP submission to local postfix on :587 with
// SASL PLAIN. Mailbox passwords are encrypted at rest with AES-256-GCM
// keyed off this service's session secret (/etc/ncn-mail/session.key).
//
// Auth model: a webmail session cookie (ncn_mail_session) is the sole proof
// of identity. The user demonstrates ownership of a mailbox by completing
// an IMAP LOGIN against the local dovecot at login time; we re-use the
// stashed credential for subsequent IMAP/SMTP operations.
package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"golang.org/x/crypto/hkdf"
)

const (
	// mailHost is the public hostname used for TLS SNI when talking to our
	// own dovecot/postfix on loopback — must match the LE cert subject.
	mailHost           = "mail.example.com"
	mailIMAPSPort      = "993"
	mailSubmissionPort = "587"
	mailCredsPath      = "/etc/ncn-mail/mail-creds.json"
	mailSessionCookie  = "ncn_mail_session"
	// Default session TTL — used when the user did NOT tick "remember
	// password" on the login form. Cookie lives ~8h, then they have to
	// re-auth. Matches a typical "work day" window so an idle session on
	// a shared browser doesn't persist overnight.
	mailSessionTTL = 8 * time.Hour
	// "Remember password" session TTL — kicks in when the form's
	// `remember` checkbox is ticked, OR when the user signs in via
	// passkey (passkey itself is unphishable proof, so we treat it as
	// implicit "trust this browser"). 30 days strikes the balance
	// between convenience and revocation-window safety; the cookie is
	// HttpOnly + Secure + SameSite=Lax + signed with the rotating
	// macKey, so a stolen cookie still can't be exfiltrated by JS.
	mailSessionRememberTTL = 30 * 24 * time.Hour
	mailMaxBody        = 5 * 1024 * 1024 // 5 MB compose ceiling
)

// ---------------------------------------------------------------------------
// session
// ---------------------------------------------------------------------------

type mailClaims struct {
	Mailbox string `json:"m"`
	Iat     int64  `json:"iat"`
	Exp     int64  `json:"exp"`
	Sid     string `json:"sid"`
}

// mailCtxKey is a distinct context key from ctxKeyAuth (operator session).
type mailCtxKey int

const ctxKeyMail mailCtxKey = 1

// ---------------------------------------------------------------------------
// encrypted credential store
// ---------------------------------------------------------------------------

type mailCredsFile struct {
	Version int                      `json:"version"`
	Entries map[string]mailCredEntry `json:"entries"`
}

type mailCredEntry struct {
	Nonce      string `json:"n"`   // base64 IV
	Ciphertext string `json:"c"`   // base64 AES-GCM(password)
	UpdatedAt  string `json:"upd"` // RFC3339
}

type mailService struct {
	mu     sync.RWMutex
	secret []byte // master session secret (shared with authStore)
	encKey []byte // derived AES-256 key
	creds  map[string]mailCredEntry
}

func newMailService(secret []byte) (*mailService, error) {
	if len(secret) < 32 {
		return nil, errors.New("mail: session secret too short")
	}
	// HKDF-SHA256 derive a dedicated 32-byte AES key. Domain-separating the
	// key means a compromise of one subsystem can't trivially decrypt the
	// other's stored material.
	hk := hkdf.New(sha256.New, secret, nil, []byte("ncn.mail.creds.v1"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(hk, key); err != nil {
		return nil, fmt.Errorf("derive enc key: %w", err)
	}
	m := &mailService{
		secret: secret,
		encKey: key,
		creds:  map[string]mailCredEntry{},
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *mailService) load() error {
	data, err := os.ReadFile(mailCredsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", mailCredsPath, err)
	}
	var f mailCredsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse mail-creds.json: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if f.Entries != nil {
		m.creds = f.Entries
	}
	return nil
}

func (m *mailService) persist() error {
	m.mu.RLock()
	f := mailCredsFile{Version: 1, Entries: make(map[string]mailCredEntry, len(m.creds))}
	for k, v := range m.creds {
		f.Entries[k] = v
	}
	m.mu.RUnlock()

	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := mailCredsPath + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, mailCredsPath)
}

func (m *mailService) encrypt(plaintext string) (mailCredEntry, error) {
	block, err := aes.NewCipher(m.encKey)
	if err != nil {
		return mailCredEntry{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return mailCredEntry{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return mailCredEntry{}, err
	}
	ct := aead.Seal(nil, nonce, []byte(plaintext), nil)
	return mailCredEntry{
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ct),
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func (m *mailService) decrypt(e mailCredEntry) (string, error) {
	block, err := aes.NewCipher(m.encKey)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce, err := base64.StdEncoding.DecodeString(e.Nonce)
	if err != nil {
		return "", err
	}
	ct, err := base64.StdEncoding.DecodeString(e.Ciphertext)
	if err != nil {
		return "", err
	}
	pt, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// stash stores the password under the mailbox key (lowercase, trimmed).
func (m *mailService) stash(mailbox, password string) error {
	mailbox = strings.ToLower(strings.TrimSpace(mailbox))
	e, err := m.encrypt(password)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.creds[mailbox] = e
	m.mu.Unlock()
	return m.persist()
}

func (m *mailService) lookup(mailbox string) (string, bool) {
	mailbox = strings.ToLower(strings.TrimSpace(mailbox))
	m.mu.RLock()
	e, ok := m.creds[mailbox]
	m.mu.RUnlock()
	if !ok {
		return "", false
	}
	pw, err := m.decrypt(e)
	if err != nil {
		log.Printf("mail: decrypt %s: %v", mailbox, err)
		return "", false
	}
	return pw, true
}

func (m *mailService) forget(mailbox string) error {
	mailbox = strings.ToLower(strings.TrimSpace(mailbox))
	m.mu.Lock()
	delete(m.creds, mailbox)
	m.mu.Unlock()
	return m.persist()
}

// mailSvcGlobal is a package-level pointer set by main() so other handlers
// (handleAdminResetPassword, mailboxRecoverService.handleSubmit) and the
// `ncn-mail admin reset-password` CLI subcommand can wipe a mailbox's
// stashed credential whenever its password changes. Otherwise the running
// service keeps decrypting the OLD password, every subsequent IMAP call
// reauths with stale creds, and the user is locked out of the webmail UI
// until they re-login.
var mailSvcGlobal *mailService

// forgetStashEverywhere drops the cached credential for a mailbox AND
// persists the change to disk. Safe to call from contexts that don't have
// a *mailService receiver — when mailSvcGlobal is nil (e.g. CLI mode
// short-circuited before main()), it falls back to a direct JSON edit.
func forgetStashEverywhere(mailbox string) {
	mailbox = strings.ToLower(strings.TrimSpace(mailbox))
	if mailSvcGlobal != nil {
		_ = mailSvcGlobal.forget(mailbox)
		return
	}
	// CLI fallback: read mail-creds.json directly, delete the entry,
	// atomic rename. mailService not initialised here so no in-memory
	// copy to update.
	data, err := os.ReadFile(mailCredsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("forget-stash: read %s: %v", mailCredsPath, err)
		}
		return
	}
	var f mailCredsFile
	if err := json.Unmarshal(data, &f); err != nil {
		log.Printf("forget-stash: parse %s: %v", mailCredsPath, err)
		return
	}
	if f.Entries == nil {
		return
	}
	if _, ok := f.Entries[mailbox]; !ok {
		return
	}
	delete(f.Entries, mailbox)
	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		log.Printf("forget-stash: marshal: %v", err)
		return
	}
	tmp := mailCredsPath + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		log.Printf("forget-stash: write tmp: %v", err)
		return
	}
	if err := os.Rename(tmp, mailCredsPath); err != nil {
		log.Printf("forget-stash: rename: %v", err)
	}
}

// ---------------------------------------------------------------------------
// session tokens — separate signing namespace from operator sessions
// ---------------------------------------------------------------------------

func (m *mailService) issueSession(w http.ResponseWriter, r *http.Request, mailbox string, remember bool) error {
	sid := make([]byte, 9)
	if _, err := rand.Read(sid); err != nil {
		return err
	}
	ttl := mailSessionTTL
	if remember {
		ttl = mailSessionRememberTTL
	}
	claims := mailClaims{
		Mailbox: strings.ToLower(strings.TrimSpace(mailbox)),
		Iat:     time.Now().Unix(),
		Exp:     time.Now().Add(ttl).Unix(),
		Sid:     hex.EncodeToString(sid),
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, m.macKey())
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	tok := payload + "." + sig

	secure := strings.HasPrefix(strings.ToLower(r.Header.Get("X-Forwarded-Proto")), "https") || r.TLS != nil
	http.SetCookie(w, &http.Cookie{
		Name:     mailSessionCookie,
		Value:    tok,
		Path:     "/",
		Expires:  time.Unix(claims.Exp, 0),
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (m *mailService) verifySession(r *http.Request) (*mailClaims, error) {
	ck, err := r.Cookie(mailSessionCookie)
	if err != nil {
		return nil, errors.New("no mail session")
	}
	parts := strings.Split(ck.Value, ".")
	if len(parts) != 2 {
		return nil, errors.New("malformed mail session")
	}
	mac := hmac.New(sha256.New, m.macKey())
	mac.Write([]byte(parts[0]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtleConstEq(want, parts[1]) == false {
		return nil, errors.New("bad mail session signature")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	var c mailClaims
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, err
	}
	if time.Now().Unix() >= c.Exp {
		return nil, errors.New("mail session expired")
	}
	return &c, nil
}

func (m *mailService) macKey() []byte {
	// Domain-separate the mac key from the encryption key + operator session
	// key so a leak in any layer doesn't compromise the others.
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte("ncn.mail.session.v1"))
	return mac.Sum(nil)
}

func subtleConstEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func clearMailSession(w http.ResponseWriter, r *http.Request) {
	secure := strings.HasPrefix(strings.ToLower(r.Header.Get("X-Forwarded-Proto")), "https") || r.TLS != nil
	http.SetCookie(w, &http.Cookie{
		Name:     mailSessionCookie,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// requireMailAuth is the protected() equivalent for webmail endpoints.
func (m *mailService) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := m.verifySession(r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyMail, c)
		next(w, r.WithContext(ctx))
	}
}

func mailClaimsFromRequest(r *http.Request) (*mailClaims, bool) {
	c, ok := r.Context().Value(ctxKeyMail).(*mailClaims)
	return c, ok && c != nil
}

// ---------------------------------------------------------------------------
// IMAP helpers
// ---------------------------------------------------------------------------

// dialIMAP opens an authenticated, TLS-implicit IMAP connection. Caller owns
// Close().
func dialIMAP(mailbox, password string) (*imapclient.Client, error) {
	addr := mailHost + ":" + mailIMAPSPort
	tc := &tls.Config{ServerName: mailHost, MinVersion: tls.VersionTLS12}
	c, err := imapclient.DialTLS(addr, &imapclient.Options{TLSConfig: tc})
	if err != nil {
		return nil, fmt.Errorf("imap dial %s: %w", addr, err)
	}
	if err := c.Login(mailbox, password).Wait(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("imap login %s: %w", mailbox, err)
	}
	return c, nil
}

// ---------------------------------------------------------------------------
// endpoints
// ---------------------------------------------------------------------------

// POST /api/v1/mail/auth
//
//	{ "mailbox": "noc@example.com", "password": "...", "remember": true }
func (m *mailService) handleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var req struct {
		Mailbox        string `json:"mailbox"`
		Password       string `json:"password"`
		Remember       bool   `json:"remember"`
		TurnstileToken string `json:"turnstile_token"`
	}
	// 16 KB cap — Turnstile tokens are 2-3 KB on their own; 4 KB was
	// dangerously close to truncating a real submission and silently
	// emitting "bad json" instead.
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	req.Mailbox = strings.ToLower(strings.TrimSpace(req.Mailbox))
	if req.Mailbox == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "mailbox + password required"})
		return
	}
	if _, err := mail.ParseAddress(req.Mailbox); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid mailbox address"})
		return
	}

	// Human-presence check BEFORE we attempt IMAP auth — keeps bots from
	// probing whether a mailbox/password combo exists.
	if err := verifyTurnstileToken(r.Context(), req.TurnstileToken, clientIP(r)); err != nil {
		log.Printf("mail: turnstile rejected for %s: %v", req.Mailbox, err)
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false,
			Error: "human verification failed — please retry the check"})
		return
	}

	c, err := dialIMAP(req.Mailbox, req.Password)
	if err != nil {
		log.Printf("mail: auth %s: %v", req.Mailbox, err)
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "login failed"})
		return
	}
	_ = c.Logout().Wait()
	_ = c.Close()

	if req.Remember {
		if err := m.stash(req.Mailbox, req.Password); err != nil {
			log.Printf("mail: stash %s: %v", req.Mailbox, err)
			// non-fatal — session still issues
		}
	} else {
		// If the user previously stored a password but now declined to
		// remember, we don't proactively wipe — they may still want it for
		// other sessions. Explicit forget endpoint exists.
	}

	if err := m.issueSession(w, r, req.Mailbox, req.Remember); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "session issue failed"})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"mailbox":  req.Mailbox,
		"remember": req.Remember,
	}})
}

// POST /api/v1/mail/logout
func (m *mailService) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearMailSession(w, r)
	writeJSON(w, http.StatusOK, envelope{OK: true})
}

// GET /api/v1/mail/me — return the session mailbox + whether password is stashed
func (m *mailService) handleMe(w http.ResponseWriter, r *http.Request) {
	c, ok := mailClaimsFromRequest(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	_, stashed := m.lookup(c.Mailbox)
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"mailbox": c.Mailbox,
		"stashed": stashed,
		"exp":     c.Exp,
	}})
}

// withIMAP runs fn with an authenticated IMAP client. The mailbox comes from
// the session; the password from the cred store. If no stashed password,
// returns 401 so the SPA can re-prompt.
func (m *mailService) withIMAP(w http.ResponseWriter, r *http.Request, fn func(*imapclient.Client) error) {
	c, ok := mailClaimsFromRequest(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	pw, ok := m.lookup(c.Mailbox)
	if !ok {
		writeJSON(w, http.StatusPreconditionRequired, envelope{OK: false, Error: "password not stashed — re-login with remember"})
		return
	}
	ic, err := dialIMAP(c.Mailbox, pw)
	if err != nil {
		log.Printf("mail: IMAP dial for %s: %v", c.Mailbox, err)
		writeJSON(w, http.StatusBadGateway, envelope{OK: false, Error: "IMAP unreachable"})
		return
	}
	defer func() {
		_ = ic.Logout().Wait()
		_ = ic.Close()
	}()
	if err := fn(ic); err != nil {
		log.Printf("mail: IMAP op for %s: %v", c.Mailbox, err)
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
	}
}
