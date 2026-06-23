// passkey.go — WebAuthn (FIDO2) login for webmail.
//
// Threat model & invariant:
//
//	Passkey authenticates "the mailbox owner is at the keyboard". It does
//	NOT replace the IMAP password — dovecot/postfix still require SASL
//	PLAIN. So the FIRST sign-in for any mailbox must be password + remember,
//	which AES-GCM-stashes the password in /etc/ncn-mail/mail-creds.json.
//	Subsequent passkey logins: assert → server looks up stashed pw → issues
//	mail session cookie. The user never types the IMAP password again.
//
// Storage:
//
//	/etc/ncn-mail/passkeys.json  ── map[mailbox][]passkeyRecord  (mode 0600)
//
// Challenge store: in-memory map keyed by a random session ID returned to
// the client; 5 min TTL. A server restart invalidates in-flight rounds.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	passkeysPath   = stateDir + "/passkeys.json"
	pkChallengeTTL = 5 * time.Minute
)

// passkeyRecord is one credential registered to a mailbox.
type passkeyRecord struct {
	ID         []byte    `json:"id"` // credential ID (raw bytes, JSON base64 by stdlib)
	PublicKey  []byte    `json:"public_key"`
	AAGUID     []byte    `json:"aaguid"`
	SignCount  uint32    `json:"sign_count"`
	Name       string    `json:"name"` // user-friendly nickname
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
	Transports []string  `json:"transports,omitempty"`
}

type passkeysFile struct {
	Version int                        `json:"version"`
	Records map[string][]passkeyRecord `json:"records"` // mailbox → records
}

type pendingChallenge struct {
	mailbox   string
	session   *webauthn.SessionData
	op        string // "register" | "login"
	createdAt time.Time
}

type passkeyService struct {
	mu         sync.RWMutex
	creds      map[string][]passkeyRecord
	wa         *webauthn.WebAuthn
	challenges map[string]pendingChallenge // key: challenge_id we return to client
	mailSvc    *mailService
}

func newPasskeyService(mailSvc *mailService) (*passkeyService, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          "mail.example.com",
		RPDisplayName: "NCN Webmail",
		RPOrigins:     []string{"https://mail.example.com"},
	})
	if err != nil {
		return nil, fmt.Errorf("webauthn init: %w", err)
	}
	s := &passkeyService{
		creds:      map[string][]passkeyRecord{},
		wa:         wa,
		challenges: map[string]pendingChallenge{},
		mailSvc:    mailSvc,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	// Background GC for stale challenges
	go func() {
		t := time.NewTicker(pkChallengeTTL)
		defer t.Stop()
		for range t.C {
			s.gcChallenges()
		}
	}()
	return s, nil
}

func (s *passkeyService) load() error {
	data, err := os.ReadFile(passkeysPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var f passkeysFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse %s: %w", passkeysPath, err)
	}
	if f.Records != nil {
		s.creds = f.Records
	}
	return nil
}

func (s *passkeyService) persistLocked() error {
	f := passkeysFile{Version: 1, Records: s.creds}
	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := passkeysPath + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, passkeysPath)
}

func (s *passkeyService) gcChallenges() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, ch := range s.challenges {
		if now.Sub(ch.createdAt) > pkChallengeTTL {
			delete(s.challenges, id)
		}
	}
}

// passkeyUser implements webauthn.User.
type passkeyUser struct {
	mailbox string
	creds   []webauthn.Credential
}

func (u *passkeyUser) WebAuthnID() []byte                         { return []byte(u.mailbox) }
func (u *passkeyUser) WebAuthnName() string                       { return u.mailbox }
func (u *passkeyUser) WebAuthnDisplayName() string                { return u.mailbox }
func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }
func (u *passkeyUser) WebAuthnIcon() string                       { return "" }

// userFor returns a passkeyUser populated with the mailbox's existing
// credentials (in webauthn.Credential form).
func (s *passkeyService) userFor(mailbox string) *passkeyUser {
	mailbox = strings.ToLower(strings.TrimSpace(mailbox))
	s.mu.RLock()
	recs := s.creds[mailbox]
	s.mu.RUnlock()
	creds := make([]webauthn.Credential, 0, len(recs))
	for _, r := range recs {
		creds = append(creds, webauthn.Credential{
			ID:        r.ID,
			PublicKey: r.PublicKey,
			Authenticator: webauthn.Authenticator{
				AAGUID:    r.AAGUID,
				SignCount: r.SignCount,
			},
		})
	}
	return &passkeyUser{mailbox: mailbox, creds: creds}
}

func (s *passkeyService) stashChallenge(op, mailbox string, sd *webauthn.SessionData) string {
	id := make([]byte, 18)
	_, _ = rand.Read(id)
	idHex := hex.EncodeToString(id)
	s.mu.Lock()
	s.challenges[idHex] = pendingChallenge{
		mailbox:   strings.ToLower(strings.TrimSpace(mailbox)),
		session:   sd,
		op:        op,
		createdAt: time.Now(),
	}
	s.mu.Unlock()
	return idHex
}

func (s *passkeyService) consumeChallenge(id, op string) (*pendingChallenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.challenges[id]
	if !ok {
		return nil, errors.New("no such challenge")
	}
	delete(s.challenges, id)
	if ch.op != op {
		return nil, fmt.Errorf("challenge op mismatch: wanted %s got %s", op, ch.op)
	}
	if time.Since(ch.createdAt) > pkChallengeTTL {
		return nil, errors.New("challenge expired")
	}
	return &ch, nil
}

// ----------------------------------------------------------------------------
// Handlers
// ----------------------------------------------------------------------------

// POST /api/v1/mail/passkey/register/begin
//
// Requires a mail session. Returns CredentialCreationOptions (per WebAuthn
// spec) + a challenge_id to echo back on /finish.
func (s *passkeyService) handleRegBegin(w http.ResponseWriter, r *http.Request) {
	c, ok := mailClaimsFromRequest(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	user := s.userFor(c.Mailbox)
	excludeList := make([]protocol.CredentialDescriptor, 0, len(user.creds))
	for _, cr := range user.creds {
		excludeList = append(excludeList, protocol.CredentialDescriptor{
			Type:         protocol.PublicKeyCredentialType,
			CredentialID: cr.ID,
		})
	}
	opts, sd, err := s.wa.BeginRegistration(user,
		webauthn.WithExclusions(excludeList),
		// IMPORTANT: ResidentKey=required makes the credential DISCOVERABLE.
		// Cross-device passkey stores — Google Password Manager, iCloud
		// Keychain, 1Password, Bitwarden — only offer to save a passkey
		// when the RP asks for a discoverable credential. Without this
		// flag GPM silently passes the registration through to the local
		// platform authenticator (Touch ID / Windows Hello) and never
		// shows the "Save passkey to Google Password Manager" prompt.
		// Same setting the admin console uses (core-console/passkey.go).
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			UserVerification:   protocol.VerificationPreferred,
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
		}),
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	chID := s.stashChallenge("register", c.Mailbox, sd)
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"challenge_id": chID,
		"options":      opts,
	}})
}

// POST /api/v1/mail/passkey/register/finish
//
//	{ "challenge_id": "...", "credential": <PublicKeyCredential>, "name": "MacBook TouchID" }
func (s *passkeyService) handleRegFinish(w http.ResponseWriter, r *http.Request) {
	c, ok := mailClaimsFromRequest(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	var req struct {
		ChallengeID string          `json:"challenge_id"`
		Credential  json.RawMessage `json:"credential"`
		Name        string          `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	ch, err := s.consumeChallenge(req.ChallengeID, "register")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: err.Error()})
		return
	}
	if ch.mailbox != strings.ToLower(c.Mailbox) {
		writeJSON(w, http.StatusForbidden, envelope{OK: false, Error: "session mailbox doesn't match challenge"})
		return
	}

	pcc, err := protocol.ParseCredentialCreationResponseBody(strings.NewReader(string(req.Credential)))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "parse cred: " + err.Error()})
		return
	}
	user := s.userFor(c.Mailbox)
	cred, err := s.wa.CreateCredential(user, *ch.session, pcc)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "create cred: " + err.Error()})
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "passkey"
	}
	if len(name) > 64 {
		name = name[:64]
	}

	rec := passkeyRecord{
		ID:        cred.ID,
		PublicKey: cred.PublicKey,
		AAGUID:    cred.Authenticator.AAGUID,
		SignCount: cred.Authenticator.SignCount,
		Name:      name,
		CreatedAt: time.Now().UTC(),
	}
	for _, t := range pcc.Response.Transports {
		rec.Transports = append(rec.Transports, string(t))
	}

	mailbox := strings.ToLower(c.Mailbox)
	s.mu.Lock()
	s.creds[mailbox] = append(s.creds[mailbox], rec)
	if err := s.persistLocked(); err != nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	s.mu.Unlock()

	log.Printf("passkey: registered for %s (name=%q, id=%s)", mailbox, name, base64.RawURLEncoding.EncodeToString(rec.ID))

	// Notification-only: tell the mailbox owner a new passkey was added.
	// If THIS wasn't them, the account is likely compromised — they can
	// revoke from Settings → Passkeys.
	if err := sendSystemMail(
		mailbox,
		"New passkey added to your mailbox",
		"New Passkey Added",
		[]string{
			"A new passkey was just registered on " + mailbox + ":",
			"    name: " + name + "\n    time: " + time.Now().UTC().Format(time.RFC1123Z),
			"If you did this, no action needed.",
			"If this was NOT you, sign in immediately and revoke the passkey from Settings → Passkeys, then rotate your password.",
		},
	); err != nil {
		log.Printf("passkey-reg: notify failed (continuing): %v", err)
	}

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"id":         base64.RawURLEncoding.EncodeToString(rec.ID),
		"name":       rec.Name,
		"created_at": rec.CreatedAt,
	}})
}

// POST /api/v1/mail/passkey/login/begin
//
//	{ "mailbox": "alice@example.com" }
//
// Anonymous. Returns assertion options + challenge_id. Requires the mailbox
// to have at least one registered passkey AND a stashed IMAP password
// (registration of passkey implies a prior password+remember login).
func (s *passkeyService) handleLoginBegin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mailbox string `json:"mailbox"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	mailbox := strings.ToLower(strings.TrimSpace(req.Mailbox))
	if mailbox == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "mailbox required"})
		return
	}

	user := s.userFor(mailbox)
	if len(user.creds) == 0 {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "no passkey registered for this mailbox"})
		return
	}
	if _, has := s.mailSvc.lookup(mailbox); !has {
		writeJSON(w, http.StatusPreconditionRequired, envelope{OK: false,
			Error: "stashed password gone — log in once with password to re-prime passkey"})
		return
	}

	opts, sd, err := s.wa.BeginLogin(user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	chID := s.stashChallenge("login", mailbox, sd)
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"challenge_id": chID,
		"options":      opts,
	}})
}

// POST /api/v1/mail/passkey/login/finish
//
//	{ "challenge_id": "...", "credential": <PublicKeyCredential> }
//
// On success: issues ncn_mail_session cookie (same as password login).
func (s *passkeyService) handleLoginFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChallengeID string          `json:"challenge_id"`
		Credential  json.RawMessage `json:"credential"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	ch, err := s.consumeChallenge(req.ChallengeID, "login")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: err.Error()})
		return
	}

	pca, err := protocol.ParseCredentialRequestResponseBody(strings.NewReader(string(req.Credential)))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "parse cred: " + err.Error()})
		return
	}
	user := s.userFor(ch.mailbox)
	cred, err := s.wa.ValidateLogin(user, *ch.session, pca)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "assertion invalid: " + err.Error()})
		return
	}

	// Update sign count to defend against credential cloning.
	s.mu.Lock()
	for i := range s.creds[ch.mailbox] {
		rec := &s.creds[ch.mailbox][i]
		if string(rec.ID) == string(cred.ID) {
			if cred.Authenticator.SignCount > 0 && cred.Authenticator.SignCount <= rec.SignCount {
				s.mu.Unlock()
				writeJSON(w, http.StatusUnauthorized, envelope{OK: false,
					Error: "sign count regression — possible cloned authenticator"})
				return
			}
			rec.SignCount = cred.Authenticator.SignCount
			rec.LastUsedAt = time.Now().UTC()
			break
		}
	}
	_ = s.persistLocked()
	s.mu.Unlock()

	// All checks pass — issue the same mail session cookie the password
	// login flow would. Passkey is unphishable hardware-bound proof, so
	// we treat it as implicit "remember" (30d cookie). The user can
	// still sign out explicitly, and the underlying credential record
	// is revocable from Settings → Passkeys.
	if err := s.mailSvc.issueSession(w, r, ch.mailbox, true); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "session issue: " + err.Error()})
		return
	}
	log.Printf("passkey: %s logged in via WebAuthn (cred id=%s)", ch.mailbox, base64.RawURLEncoding.EncodeToString(cred.ID))
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"mailbox": ch.mailbox,
	}})
}

// GET /api/v1/mail/passkey
func (s *passkeyService) handleList(w http.ResponseWriter, r *http.Request) {
	c, ok := mailClaimsFromRequest(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	mailbox := strings.ToLower(c.Mailbox)
	s.mu.RLock()
	recs := s.creds[mailbox]
	s.mu.RUnlock()
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		row := map[string]any{
			"id":         base64.RawURLEncoding.EncodeToString(r.ID),
			"name":       r.Name,
			"created_at": r.CreatedAt,
			"transports": r.Transports,
		}
		if !r.LastUsedAt.IsZero() {
			row["last_used_at"] = r.LastUsedAt
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
}

// DELETE /api/v1/mail/passkey/<id-b64url>
func (s *passkeyService) handleDelete(w http.ResponseWriter, r *http.Request) {
	c, ok := mailClaimsFromRequest(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	idB64 := strings.TrimPrefix(r.URL.Path, "/api/v1/mail/passkey/")
	idB64 = strings.TrimSuffix(idB64, "/")
	if idB64 == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "id required"})
		return
	}
	id, err := base64.RawURLEncoding.DecodeString(idB64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad id"})
		return
	}

	mailbox := strings.ToLower(c.Mailbox)
	s.mu.Lock()
	defer s.mu.Unlock()
	recs := s.creds[mailbox]
	for i, rec := range recs {
		if string(rec.ID) == string(id) {
			s.creds[mailbox] = append(recs[:i], recs[i+1:]...)
			if len(s.creds[mailbox]) == 0 {
				delete(s.creds, mailbox)
			}
			if err := s.persistLocked(); err != nil {
				writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
				return
			}
			log.Printf("passkey: %s revoked credential %s", mailbox, idB64)
			writeJSON(w, http.StatusOK, envelope{OK: true})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "no such passkey"})
}
