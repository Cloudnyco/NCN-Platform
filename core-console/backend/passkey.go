// Passkey / WebAuthn support.
//
// Uses github.com/go-webauthn/webauthn for the protocol-level work. Each
// operator can register multiple credentials; on login we use discoverable
// credentials (Conditional UI) so the operator doesn't need to type a
// username — the browser/password-manager picks the right account.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
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

// passkeyCredential is the persisted form of a registered WebAuthn credential.
//
// The Flags field is critical: WebAuthn requires that BackupEligible cannot
// change across the life of a credential (per W3C §6.5.1). go-webauthn
// enforces this by comparing the assertion's BE flag against the stored one
// at every login. We must therefore capture BE (and BS) at registration and
// hand them back to the library inside `WebAuthnCredentials()`.
type passkeyCredential struct {
	ID         []byte                    `json:"id"`
	PublicKey  []byte                    `json:"public_key"`
	SignCount  uint32                    `json:"sign_count"`
	AAGUID     []byte                    `json:"aaguid"`
	Transport  []string                  `json:"transport,omitempty"`
	Name       string                    `json:"name"`
	CreatedAt  time.Time                 `json:"created_at"`
	Flags      webauthn.CredentialFlags  `json:"flags"`
}

// webauthnUser wraps an operatorRecord to satisfy the library's User interface.
type webauthnUser struct{ op operatorRecord }

func (u webauthnUser) WebAuthnID() []byte          { return []byte(u.op.Username) }
func (u webauthnUser) WebAuthnName() string        { return u.op.Username }
func (u webauthnUser) WebAuthnDisplayName() string { return u.op.Username }
func (u webauthnUser) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(u.op.Passkeys))
	for _, p := range u.op.Passkeys {
		transports := make([]protocol.AuthenticatorTransport, 0, len(p.Transport))
		for _, t := range p.Transport {
			transports = append(transports, protocol.AuthenticatorTransport(t))
		}
		out = append(out, webauthn.Credential{
			ID:        p.ID,
			PublicKey: p.PublicKey,
			Transport: transports,
			Flags:     p.Flags, // BE/BS/UP/UV — must persist or BE inconsistency fires
			Authenticator: webauthn.Authenticator{
				AAGUID:    p.AAGUID,
				SignCount: p.SignCount,
			},
		})
	}
	return out
}

// webauthnSubsystem encapsulates the WebAuthn config + pending-challenge map.
type webauthnSubsystem struct {
	wa *webauthn.WebAuthn

	mu      sync.Mutex
	pending map[string]*pendingChallenge
}

type pendingChallenge struct {
	Kind    string                 // "register" | "login"
	Session *webauthn.SessionData
	User    string                 // username for register; "" for discoverable login
	Expires time.Time
}

func newWebAuthn(rpID, displayName string, origins []string) (*webauthnSubsystem, error) {
	cfg := &webauthn.Config{
		RPDisplayName: displayName,
		RPID:          rpID,
		RPOrigins:     origins,
	}
	w, err := webauthn.New(cfg)
	if err != nil {
		return nil, err
	}
	return &webauthnSubsystem{
		wa:      w,
		pending: make(map[string]*pendingChallenge),
	}, nil
}

func (s *webauthnSubsystem) put(kind, user string, sess *webauthn.SessionData) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	// expire stale entries (lazy GC)
	now := time.Now()
	for k, v := range s.pending {
		if v.Expires.Before(now) {
			delete(s.pending, k)
		}
	}
	idBytes := make([]byte, 16)
	rand.Read(idBytes)
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	s.pending[id] = &pendingChallenge{
		Kind: kind, Session: sess, User: user,
		Expires: now.Add(5 * time.Minute),
	}
	return id
}

func (s *webauthnSubsystem) take(id string) *pendingChallenge {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[id]
	if !ok || p.Expires.Before(time.Now()) {
		delete(s.pending, id)
		return nil
	}
	delete(s.pending, id)
	return p
}

// ----------------------------------------------------------------------------
// authStore wiring
// ----------------------------------------------------------------------------

func (s *authStore) initWebAuthn(rpID, displayName string, origins []string) error {
	w, err := newWebAuthn(rpID, displayName, origins)
	if err != nil {
		return err
	}
	s.wa = w
	return nil
}

// ----------------------------------------------------------------------------
// Endpoints
// ----------------------------------------------------------------------------

// POST /api/v1/auth/passkey/register/begin  (requires auth)
// Returns { challenge_id, options } where options is the
// PublicKeyCredentialCreationOptions for navigator.credentials.create.
func (s *authStore) handlePasskeyRegBegin(w http.ResponseWriter, r *http.Request) {
	if s.wa == nil {
		writeJSON(w, 500, envelope{OK: false, Error: "webauthn not configured"})
		return
	}
	c, ok := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if !ok || c == nil {
		writeJSON(w, 401, envelope{OK: false, Error: "not authenticated"})
		return
	}
	s.mu.RLock()
	op, exists := s.operators[c.Sub]
	s.mu.RUnlock()
	if !exists {
		writeJSON(w, 404, envelope{OK: false, Error: "operator not found"})
		return
	}

	// IMPORTANT: ResidentKey=required (and the legacy RequireResidentKey=true)
	// forces the authenticator to create a discoverable credential — without
	// this, BeginDiscoverableLogin on the sign-in page can't find anything to
	// offer the user and the "Sign in with passkey" flow fails.
	options, sess, err := s.wa.wa.BeginRegistration(
		webauthnUser{op: op},
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			UserVerification:   protocol.VerificationPreferred,
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
		}),
		webauthn.WithExclusions(existingCredentialDescriptors(op)),
	)
	if err != nil {
		writeJSON(w, 500, envelope{OK: false, Error: err.Error()})
		return
	}
	challengeID := s.wa.put("register", c.Sub, sess)
	writeJSON(w, 200, envelope{OK: true, Data: map[string]any{
		"challenge_id": challengeID,
		"options":      options.Response,
	}})
}

func existingCredentialDescriptors(op operatorRecord) []protocol.CredentialDescriptor {
	out := make([]protocol.CredentialDescriptor, 0, len(op.Passkeys))
	for _, p := range op.Passkeys {
		out = append(out, protocol.CredentialDescriptor{
			Type:         protocol.PublicKeyCredentialType,
			CredentialID: p.ID,
		})
	}
	return out
}

// POST /api/v1/auth/passkey/register/finish  (requires auth)
func (s *authStore) handlePasskeyRegFinish(w http.ResponseWriter, r *http.Request) {
	if s.wa == nil {
		writeJSON(w, 500, envelope{OK: false, Error: "webauthn not configured"})
		return
	}
	var req struct {
		ChallengeID string          `json:"challenge_id"`
		Name        string          `json:"name"`
		Response    json.RawMessage `json:"response"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<15)).Decode(&req); err != nil {
		writeJSON(w, 400, envelope{OK: false, Error: "bad json"})
		return
	}
	p := s.wa.take(req.ChallengeID)
	if p == nil || p.Kind != "register" {
		writeJSON(w, 400, envelope{OK: false, Error: "challenge expired or unknown"})
		return
	}

	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(req.Response))
	if err != nil {
		writeJSON(w, 400, envelope{OK: false, Error: "parse: " + err.Error()})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	op, exists := s.operators[p.User]
	if !exists {
		writeJSON(w, 404, envelope{OK: false, Error: "operator vanished"})
		return
	}
	cred, err := s.wa.wa.CreateCredential(webauthnUser{op: op}, *p.Session, parsed)
	if err != nil {
		writeJSON(w, 400, envelope{OK: false, Error: "verify: " + err.Error()})
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "passkey-" + time.Now().Format("20060102-150405")
	}
	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}
	pk := passkeyCredential{
		ID:        cred.ID,
		PublicKey: cred.PublicKey,
		SignCount: cred.Authenticator.SignCount,
		AAGUID:    cred.Authenticator.AAGUID,
		Transport: transports,
		Name:      name,
		CreatedAt: time.Now(),
		Flags:     cred.Flags,
	}
	op.Passkeys = append(op.Passkeys, pk)
	s.operators[p.User] = op
	if err := s.persistLocked(); err != nil {
		writeJSON(w, 500, envelope{OK: false, Error: "persist: " + err.Error()})
		return
	}
	log.Printf("passkey: registered user=%q name=%q id=%s", p.User, name,
		base64.RawURLEncoding.EncodeToString(cred.ID))
	auditRecord(r, AuditEvent{
		Event: "passkey.register", Severity: auditSevWarn, Actor: p.User, Target: name,
		Details: map[string]any{"credential_id": base64.RawURLEncoding.EncodeToString(cred.ID)},
	})

	writeJSON(w, 200, envelope{OK: true, Data: map[string]any{
		"name":          name,
		"credential_id": base64.RawURLEncoding.EncodeToString(cred.ID),
		"created_at":    pk.CreatedAt.Format(time.RFC3339),
	}})
}

// persistLocked writes the operators file while caller already holds the write lock.
func (s *authStore) persistLocked() error {
	ops := make([]operatorRecord, 0, len(s.operators))
	for _, op := range s.operators {
		ops = append(ops, op)
	}
	data, err := json.MarshalIndent(ops, "", "  ")
	if err != nil {
		return err
	}
	tmp := operatorsPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, operatorsPath)
}

// POST /api/v1/auth/passkey/login/begin  (public)
// Returns discoverable assertion options for navigator.credentials.get.
func (s *authStore) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if s.wa == nil {
		writeJSON(w, 500, envelope{OK: false, Error: "webauthn not configured"})
		return
	}
	options, sess, err := s.wa.wa.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationPreferred),
	)
	if err != nil {
		writeJSON(w, 500, envelope{OK: false, Error: err.Error()})
		return
	}
	challengeID := s.wa.put("login", "", sess)
	writeJSON(w, 200, envelope{OK: true, Data: map[string]any{
		"challenge_id": challengeID,
		"options":      options.Response,
	}})
}

// POST /api/v1/auth/passkey/login/finish  (public)
// Verifies the assertion, looks up which operator owns the credential
// (via UserHandle returned by the authenticator), issues a session cookie.
func (s *authStore) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if s.wa == nil {
		writeJSON(w, 500, envelope{OK: false, Error: "webauthn not configured"})
		return
	}
	var req struct {
		ChallengeID string          `json:"challenge_id"`
		Response    json.RawMessage `json:"response"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<15)).Decode(&req); err != nil {
		writeJSON(w, 400, envelope{OK: false, Error: "bad json"})
		return
	}
	p := s.wa.take(req.ChallengeID)
	if p == nil || p.Kind != "login" {
		writeJSON(w, 400, envelope{OK: false, Error: "challenge expired"})
		return
	}

	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(req.Response))
	if err != nil {
		writeJSON(w, 400, envelope{OK: false, Error: "parse: " + err.Error()})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// userHandle finder — given the WebAuthnID (which we set to the username),
	// return the matching operator.
	cred, err := s.wa.wa.ValidateDiscoverableLogin(
		func(rawID, userHandle []byte) (webauthn.User, error) {
			username := string(userHandle)
			log.Printf("passkey: assertion lookup userHandle=%q (len=%d) rawID-len=%d", username, len(userHandle), len(rawID))
			if username == "" {
				return nil, errors.New("authenticator returned empty userHandle — likely a non-discoverable credential; delete it from your password manager and re-register on /admin/security")
			}
			op, ok := s.operators[username]
			if !ok {
				return nil, fmt.Errorf("no operator named %q", username)
			}
			if len(op.Passkeys) == 0 {
				return nil, fmt.Errorf("operator %q has no registered passkeys", username)
			}
			return webauthnUser{op: op}, nil
		},
		*p.Session, parsed,
	)
	if err != nil {
		log.Printf("passkey: login FAIL peer=%s: %v", clientAddr(r), err)
		auditRecord(r, AuditEvent{
			Event: "login.fail", Severity: auditSevWarn, Actor: "anonymous", Outcome: "fail",
			Details: map[string]any{"path": "passkey", "reason": err.Error()},
		})
		time.Sleep(300 * time.Millisecond)
		writeJSON(w, 401, envelope{OK: false, Error: "passkey verification failed: " + err.Error()})
		return
	}

	// Update sign count + BackupState (BS can change per spec; BE cannot)
	// for the matching credential.
	var matchedUser string
	for username, op := range s.operators {
		for i, pk := range op.Passkeys {
			if bytes.Equal(pk.ID, cred.ID) {
				op.Passkeys[i].SignCount = cred.Authenticator.SignCount
				op.Passkeys[i].Flags.BackupState = cred.Flags.BackupState
				s.operators[username] = op
				matchedUser = username
				break
			}
		}
		if matchedUser != "" {
			break
		}
	}
	if matchedUser == "" {
		writeJSON(w, 500, envelope{OK: false, Error: "credential matched but no operator owns it"})
		return
	}
	// Self-registered invites cannot bypass the approval gate via passkey.
	if matchedOp, ok := s.operators[matchedUser]; ok && !matchedOp.Approved {
		writeJSON(w, http.StatusForbidden, envelope{OK: false,
			Error: "account pending admin approval"})
		return
	}
	_ = s.persistLocked() // best-effort; ignore failure

	// Issue session cookie (same as password login)
	token, exp, err := s.issueToken(matchedUser)
	if err != nil {
		writeJSON(w, 500, envelope{OK: false, Error: "session issuance failed"})
		return
	}
	secureCookie := r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secureCookie,
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
	})
	log.Printf("passkey: login OK user=%q peer=%s exp=%s", matchedUser, clientAddr(r), exp.Format(time.RFC3339))
	auditRecord(r, AuditEvent{
		Event: "login.ok", Actor: matchedUser,
		Details: map[string]any{"path": "passkey"},
	})
	writeJSON(w, 200, envelope{OK: true, Data: map[string]any{
		"operator":   matchedUser,
		"issued_at":  time.Now().Unix(),
		"expires_at": exp.Unix(),
	}})
}

// ----------------------------------------------------------------------------
// Listing / deletion (requires auth)
// ----------------------------------------------------------------------------

type passkeyOut struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	SignCount  uint32    `json:"sign_count"`
	Transport  []string  `json:"transport,omitempty"`
}

// GET /api/v1/auth/passkey · list registered passkeys for the current operator.
func (s *authStore) handlePasskeyList(w http.ResponseWriter, r *http.Request) {
	c, ok := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if !ok || c == nil {
		writeJSON(w, 401, envelope{OK: false, Error: "not authenticated"})
		return
	}
	s.mu.RLock()
	op := s.operators[c.Sub]
	s.mu.RUnlock()
	out := make([]passkeyOut, 0, len(op.Passkeys))
	for _, p := range op.Passkeys {
		out = append(out, passkeyOut{
			ID:        base64.RawURLEncoding.EncodeToString(p.ID),
			Name:      p.Name,
			CreatedAt: p.CreatedAt,
			SignCount: p.SignCount,
			Transport: p.Transport,
		})
	}
	writeJSON(w, 200, envelope{OK: true, Data: out})
}

// DELETE /api/v1/auth/passkey?id=...  (requires auth)
func (s *authStore) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, 405, envelope{OK: false, Error: "DELETE only"})
		return
	}
	c, ok := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if !ok || c == nil {
		writeJSON(w, 401, envelope{OK: false, Error: "not authenticated"})
		return
	}
	idStr := strings.TrimSpace(r.URL.Query().Get("id"))
	if idStr == "" {
		writeJSON(w, 400, envelope{OK: false, Error: "missing ?id="})
		return
	}
	id, err := base64.RawURLEncoding.DecodeString(idStr)
	if err != nil {
		writeJSON(w, 400, envelope{OK: false, Error: "id not base64url"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	op := s.operators[c.Sub]
	found := -1
	for i, p := range op.Passkeys {
		if bytes.Equal(p.ID, id) {
			found = i
			break
		}
	}
	if found < 0 {
		writeJSON(w, 404, envelope{OK: false, Error: "passkey not found"})
		return
	}
	removed := op.Passkeys[found].Name
	op.Passkeys = append(op.Passkeys[:found], op.Passkeys[found+1:]...)
	s.operators[c.Sub] = op
	_ = s.persistLocked()
	log.Printf("passkey: removed user=%q name=%q", c.Sub, removed)
	auditRecord(r, AuditEvent{
		Event: "passkey.remove", Severity: auditSevWarn, Actor: c.Sub, Target: removed,
	})
	writeJSON(w, 200, envelope{OK: true, Data: map[string]string{"removed": removed}})
}

