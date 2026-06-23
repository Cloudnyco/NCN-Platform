// auth_ssh.go — SSH public-key based authentication for ncn-api.
//
// Two surfaces:
//
//   1. Key management (per-operator)
//        POST   /api/v1/auth/ssh-keys        register a new public key
//        GET    /api/v1/auth/ssh-keys        list this operator's keys
//        DELETE /api/v1/auth/ssh-keys/<id>   revoke one
//      All operator-only (any role); same self-service shape as passkeys.
//
//   2. SSH-signed login (challenge/response)
//        POST /api/v1/auth/ssh-login/begin   {operator} →
//                                             {challenge_id, challenge_b64, fingerprints[]}
//        POST /api/v1/auth/ssh-login/finish  {challenge_id, fingerprint, signature_b64,
//                                             signature_format} →
//                                             {redeem_url}
//        GET  /api/v1/auth/ssh-login/redeem?t=…
//                                            sets session cookie + 302 /admin
//
// Why this flow:
//   The browser can't directly talk to ssh-agent, so the user runs the
//   `ncn-login` CLI which:
//     1. Posts to /begin with their username.
//     2. Server returns the challenge + the list of pubkey fingerprints
//        registered to that user.
//     3. CLI asks ssh-agent to sign the challenge with one of those keys.
//     4. CLI posts to /finish; server verifies the signature against the
//        stored pubkey, mints a one-shot redemption ticket.
//     5. CLI opens the redeem URL in the user's browser; the GET sets the
//        session cookie and the user lands inside the admin console.
//   This keeps the browser session as the only authoritative session
//   (everything else in the system already understands the cookie) while
//   letting the user authenticate via an identity the browser can't see.
//
// Security notes:
//   * Challenges live for 5 minutes in a memory map; on server restart
//     in-flight logins fail safely (user re-runs `ncn-login`).
//   * Signed payload is `"ncn-ssh-login\x00" + challenge_bytes` — the
//     domain-separation prefix prevents a signature minted for, say, a
//     git commit ever being valid here.
//   * Verify uses ssh.PublicKey.Verify with the format the agent emitted,
//     no shelling out to ssh-keygen.
//   * Each successful login bumps LastUsedAt on the matching key.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Domain-separation tag mixed into every challenge before signing.
// Changing this invalidates all in-flight challenges — never change it
// in a way that doesn't also bump a config version, lest old CLI
// versions silently fail with cryptic errors.
const sshLoginContext = "ncn-ssh-login\x00"

const (
	sshChallengeTTL    = 5 * time.Minute
	sshRedeemTTL       = 60 * time.Second // one-shot ticket; user opens within a minute
	sshMaxKeysPerUser  = 16
	sshMaxLabelLen     = 64
	sshMaxKeyBytes     = 16 << 10 // 16 KB; well above any real authorized_keys line
)

// ============================================================================
// Key management
// ============================================================================

// handleSSHKeysList — GET /api/v1/auth/ssh-keys
// Lists the calling operator's registered keys (no secret material).
func (s *authStore) handleSSHKeysList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET only"})
		return
	}
	c, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	s.mu.RLock()
	op, ok := s.operators[c.Sub]
	s.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "operator gone"})
		return
	}
	// Strip the public_key body from the list view — UI displays
	// fingerprint + type + label. Whoever wants the raw line can
	// re-export it from their own ~/.ssh/.
	out := make([]map[string]any, 0, len(op.SSHKeys))
	for _, k := range op.SSHKeys {
		out = append(out, map[string]any{
			"id":           k.ID,
			"label":        k.Label,
			"fingerprint":  k.Fingerprint,
			"type":         k.Type,
			"created_at":   k.CreatedAt,
			"last_used_at": k.LastUsedAt,
		})
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
}

// handleSSHKeyAdd — POST /api/v1/auth/ssh-keys
// Body: { "label": "...", "public_key": "ssh-ed25519 AAAA… comment" }
func (s *authStore) handleSSHKeyAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	c, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)

	var req struct {
		Label     string `json:"label"`
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, sshMaxKeyBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	req.Label = strings.TrimSpace(req.Label)
	req.PublicKey = strings.TrimSpace(req.PublicKey)
	if req.Label == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "label is required"})
		return
	}
	if len(req.Label) > sshMaxLabelLen {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: fmt.Sprintf("label too long (max %d)", sshMaxLabelLen)})
		return
	}

	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(req.PublicKey))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: "not a valid SSH public key: " + err.Error()})
		return
	}
	fp := ssh.FingerprintSHA256(pub) // "SHA256:abc…"
	canonical := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))

	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.operators[c.Sub]
	if !ok {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "operator gone"})
		return
	}
	if len(op.SSHKeys) >= sshMaxKeysPerUser {
		writeJSON(w, http.StatusTooManyRequests, envelope{OK: false,
			Error: fmt.Sprintf("max %d keys per operator — revoke an old one first", sshMaxKeysPerUser)})
		return
	}
	// Reject duplicate fingerprints (re-registering the same key is
	// almost certainly user error, and would confuse the UI).
	for _, k := range op.SSHKeys {
		if k.Fingerprint == fp {
			writeJSON(w, http.StatusConflict, envelope{OK: false,
				Error: "this public key is already registered as \"" + k.Label + "\""})
			return
		}
	}
	idB := make([]byte, 4)
	_, _ = rand.Read(idB)
	now := time.Now().Unix()
	rec := sshKeyRecord{
		ID:          hex.EncodeToString(idB),
		Label:       req.Label,
		PublicKey:   canonical,
		Fingerprint: fp,
		Type:        pub.Type(),
		CreatedAt:   now,
	}
	op.SSHKeys = append(op.SSHKeys, rec)
	s.operators[c.Sub] = op
	if err := s.persistLocked(); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "persist: " + err.Error()})
		return
	}
	log.Printf("auth: ssh-key REGISTERED operator=%s label=%q fp=%s type=%s",
		c.Sub, rec.Label, rec.Fingerprint, rec.Type)
	auditRecord(r, AuditEvent{
		Event: "ssh-key.add", Severity: auditSevWarn, Actor: c.Sub, Target: rec.Fingerprint,
		Details: map[string]any{"label": rec.Label, "type": rec.Type},
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"id":          rec.ID,
		"label":       rec.Label,
		"fingerprint": rec.Fingerprint,
		"type":        rec.Type,
		"created_at":  rec.CreatedAt,
	}})
}

// handleSSHKeyDelete — DELETE /api/v1/auth/ssh-keys/<id>
func (s *authStore) handleSSHKeyDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "DELETE only"})
		return
	}
	c, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/ssh-keys/")
	id = strings.TrimSuffix(id, "/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "id required"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.operators[c.Sub]
	if !ok {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "operator gone"})
		return
	}
	kept := op.SSHKeys[:0]
	var removed *sshKeyRecord
	for _, k := range op.SSHKeys {
		if k.ID == id {
			r := k
			removed = &r
			continue
		}
		kept = append(kept, k)
	}
	if removed == nil {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "no such key"})
		return
	}
	op.SSHKeys = kept
	s.operators[c.Sub] = op
	if err := s.persistLocked(); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "persist: " + err.Error()})
		return
	}
	log.Printf("auth: ssh-key REVOKED operator=%s label=%q fp=%s",
		c.Sub, removed.Label, removed.Fingerprint)
	auditRecord(r, AuditEvent{
		Event: "ssh-key.remove", Severity: auditSevWarn, Actor: c.Sub, Target: removed.Fingerprint,
		Details: map[string]any{"label": removed.Label},
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]string{"removed": removed.ID}})
}

// ============================================================================
// SSH-signed login flow
// ============================================================================

// sshChallenge is a single in-flight login attempt.
type sshChallenge struct {
	Operator  string
	Bytes     []byte    // 32 random bytes; what the CLI signs
	IssuedAt  time.Time
	ExpiresAt time.Time
}

type sshLoginStore struct {
	mu         sync.Mutex
	challenges map[string]*sshChallenge // challenge_id (hex) → record
	tickets    map[string]*sshTicket    // redeem_token (hex) → operator + expiry
}

type sshTicket struct {
	Operator  string
	ExpiresAt time.Time
}

func newSSHLoginStore() *sshLoginStore {
	return &sshLoginStore{
		challenges: make(map[string]*sshChallenge),
		tickets:    make(map[string]*sshTicket),
	}
}

// gc drops anything that's expired. Cheap; called inline at the top
// of each handler so the map size tracks active load.
func (sl *sshLoginStore) gc(now time.Time) {
	for k, v := range sl.challenges {
		if now.After(v.ExpiresAt) {
			delete(sl.challenges, k)
		}
	}
	for k, v := range sl.tickets {
		if now.After(v.ExpiresAt) {
			delete(sl.tickets, k)
		}
	}
}

// handleSSHLoginBegin — POST /api/v1/auth/ssh-login/begin
//
//	Body: { "operator": "alice" }
//	Returns: { "challenge_id": "...", "challenge_b64": "...", "fingerprints": [...] }
//
// Public route — no session yet. We return WHICH keys are accepted so
// the CLI can pick the right one from ssh-agent without bothering the
// user. Username enumeration via the fingerprint list is an accepted
// trade-off: ops accounts aren't secret, and the CLI is unusable
// without it.
func (s *authStore) handleSSHLoginBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var req struct {
		Operator string `json:"operator"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	req.Operator = strings.TrimSpace(req.Operator)
	if req.Operator == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "operator is required"})
		return
	}

	s.mu.RLock()
	op, ok := s.operators[req.Operator]
	s.mu.RUnlock()
	// To avoid leaking which usernames exist we still mint a challenge
	// and return an empty fingerprint list — the CLI will fail on the
	// /finish side with "no matching key" the same way as a real
	// unknown user, just with a brief auth delay.
	var fingerprints []map[string]string
	if ok {
		fingerprints = make([]map[string]string, 0, len(op.SSHKeys))
		for _, k := range op.SSHKeys {
			fingerprints = append(fingerprints, map[string]string{
				"fingerprint": k.Fingerprint,
				"type":        k.Type,
				"label":       k.Label,
			})
		}
	}

	chBytes := make([]byte, 32)
	if _, err := rand.Read(chBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "rand failed"})
		return
	}
	idB := make([]byte, 8)
	_, _ = rand.Read(idB)
	chID := hex.EncodeToString(idB)

	now := time.Now()
	s.sshLogin.mu.Lock()
	s.sshLogin.gc(now)
	s.sshLogin.challenges[chID] = &sshChallenge{
		Operator:  req.Operator,
		Bytes:     chBytes,
		IssuedAt:  now,
		ExpiresAt: now.Add(sshChallengeTTL),
	}
	s.sshLogin.mu.Unlock()

	log.Printf("auth: ssh-login BEGIN operator=%s peer=%s keys=%d",
		req.Operator, clientAddr(r), len(fingerprints))
	auditRecord(r, AuditEvent{
		Event: "ssh-login.begin", Actor: req.Operator,
		Details: map[string]any{"key_count": len(fingerprints)},
	})

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"challenge_id":  chID,
		"challenge_b64": base64.StdEncoding.EncodeToString(chBytes),
		"context":       sshLoginContext, // CLI prepends this before signing
		"fingerprints":  fingerprints,
		"expires_at":    now.Add(sshChallengeTTL).Unix(),
	}})
}

// handleSSHLoginFinish — POST /api/v1/auth/ssh-login/finish
//
//	Body: {
//	   "challenge_id":     "...",
//	   "fingerprint":      "SHA256:...",
//	   "signature_format": "ssh-ed25519" | "ecdsa-sha2-nistp256" | …,
//	   "signature_b64":    "...",   // raw ssh.Signature.Blob, base64
//	}
//	Returns: { "redeem_url": "/api/v1/auth/ssh-login/redeem?t=…" }
//
// On success we mint a one-shot redeem token rather than setting the
// session cookie directly — the CLI sees the response in its own
// process, not the browser, and needs to bounce the user into the
// browser flow. The redeem URL is single-use and 60-second-TTL.
func (s *authStore) handleSSHLoginFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var req struct {
		ChallengeID     string `json:"challenge_id"`
		Fingerprint     string `json:"fingerprint"`
		SignatureFormat string `json:"signature_format"`
		SignatureB64    string `json:"signature_b64"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}

	now := time.Now()
	s.sshLogin.mu.Lock()
	s.sshLogin.gc(now)
	ch, ok := s.sshLogin.challenges[req.ChallengeID]
	if ok {
		delete(s.sshLogin.challenges, req.ChallengeID) // single-use
	}
	s.sshLogin.mu.Unlock()
	if !ok {
		s.sshLoginReject(w, r, "", errors.New("challenge not found or expired"))
		return
	}

	// Look up the operator + key by fingerprint.
	s.mu.RLock()
	op, exists := s.operators[ch.Operator]
	s.mu.RUnlock()
	if !exists || !op.Approved {
		s.sshLoginReject(w, r, ch.Operator, errors.New("unknown or pending operator"))
		return
	}
	var matchedKey *sshKeyRecord
	for i := range op.SSHKeys {
		if op.SSHKeys[i].Fingerprint == req.Fingerprint {
			matchedKey = &op.SSHKeys[i]
			break
		}
	}
	if matchedKey == nil {
		s.sshLoginReject(w, r, ch.Operator, errors.New("no matching key for operator"))
		return
	}

	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(matchedKey.PublicKey))
	if err != nil {
		s.sshLoginReject(w, r, ch.Operator, fmt.Errorf("stored key unparseable: %w", err))
		return
	}
	sigBlob, err := base64.StdEncoding.DecodeString(req.SignatureB64)
	if err != nil {
		s.sshLoginReject(w, r, ch.Operator, fmt.Errorf("signature_b64 decode: %w", err))
		return
	}
	sig := &ssh.Signature{Format: req.SignatureFormat, Blob: sigBlob}

	// Verify against context || challenge_bytes — domain-separated so
	// the same key signing a git tag can never be replayed as a login.
	signed := append([]byte(sshLoginContext), ch.Bytes...)
	if err := pub.Verify(signed, sig); err != nil {
		// Hash the signed payload for the audit record so we can
		// triage replay attempts without storing raw key material.
		h := sha256.Sum256(signed)
		s.sshLoginReject(w, r, ch.Operator,
			fmt.Errorf("signature verify (payload sha256=%s): %w",
				hex.EncodeToString(h[:8]), err))
		return
	}

	// Bump LastUsedAt on the key.
	s.mu.Lock()
	op2, ok := s.operators[ch.Operator]
	if ok {
		for i := range op2.SSHKeys {
			if op2.SSHKeys[i].ID == matchedKey.ID {
				op2.SSHKeys[i].LastUsedAt = now.Unix()
				break
			}
		}
		s.operators[ch.Operator] = op2
		_ = s.persistLocked()
	}
	s.mu.Unlock()

	// Mint redeem ticket.
	tB := make([]byte, 16)
	_, _ = rand.Read(tB)
	ticket := hex.EncodeToString(tB)
	s.sshLogin.mu.Lock()
	s.sshLogin.tickets[ticket] = &sshTicket{
		Operator:  ch.Operator,
		ExpiresAt: now.Add(sshRedeemTTL),
	}
	s.sshLogin.mu.Unlock()

	log.Printf("auth: ssh-login OK operator=%s peer=%s fp=%s",
		ch.Operator, clientAddr(r), matchedKey.Fingerprint)
	auditRecord(r, AuditEvent{
		Event: "login.ok", Actor: ch.Operator,
		Details: map[string]any{
			"path":        "ssh",
			"fingerprint": matchedKey.Fingerprint,
			"key_label":   matchedKey.Label,
		},
	})

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"redeem_url":   "/api/v1/auth/ssh-login/redeem?t=" + ticket,
		"redeem_token": ticket, // for headless flows that don't want to bounce through GET
		"expires_at":   now.Add(sshRedeemTTL).Unix(),
		"operator":     ch.Operator,
	}})
}

// sshLoginReject is the unified error responder + audit hook.
func (s *authStore) sshLoginReject(w http.ResponseWriter, r *http.Request, op string, err error) {
	log.Printf("auth: ssh-login FAIL operator=%q peer=%s: %v", op, clientAddr(r), err)
	auditRecord(r, AuditEvent{
		Event: "login.fail", Severity: auditSevWarn, Actor: op, Outcome: "fail",
		Details: map[string]any{"path": "ssh", "reason": err.Error()},
	})
	time.Sleep(300 * time.Millisecond) // mild brute-force slowdown
	writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "ssh login failed"})
}

// handleSSHLoginRedeem — GET /api/v1/auth/ssh-login/redeem?t=…
// Consumes a one-shot ticket from /finish, sets the session cookie,
// 302's to /admin. Single-use, 60s TTL.
func (s *authStore) handleSSHLoginRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET only"})
		return
	}
	t := strings.TrimSpace(r.URL.Query().Get("t"))
	if t == "" {
		http.Redirect(w, r, "/login?ssh_err=missing", http.StatusFound)
		return
	}
	now := time.Now()
	s.sshLogin.mu.Lock()
	s.sshLogin.gc(now)
	ticket, ok := s.sshLogin.tickets[t]
	if ok {
		delete(s.sshLogin.tickets, t) // single-use
	}
	s.sshLogin.mu.Unlock()
	if !ok {
		http.Redirect(w, r, "/login?ssh_err=expired", http.StatusFound)
		return
	}

	if _, err := s.setSessionCookie(w, r, ticket.Operator); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "session issue: " + err.Error()})
		return
	}
	log.Printf("auth: ssh-login REDEEM operator=%s peer=%s", ticket.Operator, clientAddr(r))
	http.Redirect(w, r, "/admin", http.StatusFound)
}
