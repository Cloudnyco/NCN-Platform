// recover_bootstrap.go — the verifier half of `ncn-api admin mint-recover`.
//
// Trust model:
//   * One symmetric HMAC-SHA256 key at /etc/ncn-core-console/recovery-bootstrap.key
//     (auto-generated on first mint; mode 0600 root). The CLI signs tokens
//     with this key; the HTTP handler below verifies them.
//   * Tokens live 15 minutes max. Format:
//        rcv-<base64url(JSON payload)>.<base64url(HMAC-SHA256(key, payload))>
//   * Each token's nonce is recorded in recovery-used.json on first
//     successful redemption — no replay, even within the TTL window.
//
// This endpoint is intentionally NOT protected by `protected()` (the user
// has lost their MFA factors, that's the whole point). Trust comes entirely
// from possession of a valid HMAC signature, which in turn requires SSH
// root on tyo to mint. That's the same trust boundary as `systemctl
// restart` — fine for break-glass.
package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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

	"golang.org/x/crypto/bcrypt"
)

const (
	recoveryBootstrapKeyPath  = authConfigDir + "/recovery-bootstrap.key"
	recoveryBootstrapUsedPath = authConfigDir + "/recovery-bootstrap-used.json"
	recoveryBootstrapPrefix   = "rcv-"
	recoveryBootstrapMaxTTL   = 15 * time.Minute
)

// loadOrCreateRecoveryBootstrapKey returns the shared HMAC key. Generates
// 32 random bytes on first call. Called by both the CLI (mint side) and
// the HTTP handler (verify side).
func loadOrCreateRecoveryBootstrapKey() ([]byte, error) {
	if data, err := os.ReadFile(recoveryBootstrapKeyPath); err == nil {
		if len(data) < 16 {
			return nil, fmt.Errorf("%s too short (%d bytes)", recoveryBootstrapKeyPath, len(data))
		}
		return data, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(recoveryBootstrapKeyPath, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// usedNonceStore tracks redeemed nonces so a single token can't be used
// twice. Persisted as a flat JSON array of {nonce,user,used_at} on disk.
type usedNonceStore struct {
	mu      sync.Mutex
	entries []usedNonceEntry
}

type usedNonceEntry struct {
	Nonce  string `json:"nonce"`
	User   string `json:"user"`
	UsedAt int64  `json:"used_at"`
}

func (s *usedNonceStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if globalDB != nil {
		if doc, err := loadConfigDoc("recovery_used"); err != nil {
			log.Printf("recovery-used: db load failed (%v) — falling back to file", err)
		} else if doc != nil {
			return json.Unmarshal(doc, &s.entries)
		}
	}
	data, err := os.ReadFile(recoveryBootstrapUsedPath)
	if err != nil {
		if os.IsNotExist(err) {
			s.entries = nil
			return nil
		}
		return err
	}
	if len(data) == 0 {
		s.entries = nil
		return nil
	}
	if err := json.Unmarshal(data, &s.entries); err != nil {
		return err
	}
	// Migrate file→DB on the first DB-enabled boot.
	if globalDB != nil {
		_ = s.persistLocked() // best-effort
	}
	return nil
}

// persistLocked writes the entries to the JSON file and mirrors them into
// Postgres when available. Caller holds s.mu.
func (s *usedNonceStore) persistLocked() error {
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := recoveryBootstrapUsedPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, recoveryBootstrapUsedPath); err != nil {
		return err
	}
	if globalDB != nil {
		if err := saveConfigDoc("recovery_used", data); err != nil {
			log.Printf("recovery-used: db persist failed (%v) — file is current", err)
		}
	}
	return nil
}

func (s *usedNonceStore) seen(nonce string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.Nonce == nonce {
			return true
		}
	}
	return false
}

func (s *usedNonceStore) record(nonce, user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, usedNonceEntry{
		Nonce: nonce, User: user, UsedAt: time.Now().Unix(),
	})
	// gc entries older than 24h — anything past the 15m TTL is irrelevant
	// for replay protection, keep a day's window for forensic readability.
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	kept := s.entries[:0]
	for _, e := range s.entries {
		if e.UsedAt >= cutoff {
			kept = append(kept, e)
		}
	}
	s.entries = kept

	return s.persistLocked()
}

// recoverBootstrapService wraps the verifier with its key + nonce store.
type recoverBootstrapService struct {
	key  []byte
	used *usedNonceStore
	auth *authStore
}

func newRecoverBootstrapService(auth *authStore) (*recoverBootstrapService, error) {
	used := &usedNonceStore{}
	if err := used.load(); err != nil {
		return nil, err
	}
	// Key is created lazily on first mint by the CLI. If absent here, we
	// stay nil and the handler returns 503.
	data, err := os.ReadFile(recoveryBootstrapKeyPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("recover-bootstrap: %s missing — endpoint disabled until first `ncn-api admin mint-recover` call",
				recoveryBootstrapKeyPath)
			return &recoverBootstrapService{used: used, auth: auth}, nil
		}
		return nil, err
	}
	if len(data) < 16 {
		return nil, fmt.Errorf("%s too short (%d bytes)", recoveryBootstrapKeyPath, len(data))
	}
	log.Printf("recover-bootstrap: key loaded (%d bytes)", len(data))
	return &recoverBootstrapService{key: data, used: used, auth: auth}, nil
}

// verify parses + validates a token. Returns the operator name on success.
// Does NOT check the nonce store — that's caller's job, post-verify.
func (s *recoverBootstrapService) verify(token string) (user, nonce string, err error) {
	// Re-load the key on every call: it might have been created by the CLI
	// AFTER the service started. Cheap (32 bytes) and means we never need
	// to restart for the URL flow to come online.
	if len(s.key) == 0 {
		k, kerr := os.ReadFile(recoveryBootstrapKeyPath)
		if kerr != nil {
			return "", "", errors.New("recovery-bootstrap key not provisioned")
		}
		s.key = k
	}
	if !strings.HasPrefix(token, recoveryBootstrapPrefix) {
		return "", "", errors.New("not a recovery token")
	}
	body := strings.TrimPrefix(token, recoveryBootstrapPrefix)
	parts := strings.Split(body, ".")
	if len(parts) != 2 {
		return "", "", errors.New("malformed recovery token")
	}
	payload, perr := base64.RawURLEncoding.DecodeString(parts[0])
	if perr != nil {
		return "", "", fmt.Errorf("decode payload: %w", perr)
	}
	sig, serr := base64.RawURLEncoding.DecodeString(parts[1])
	if serr != nil {
		return "", "", fmt.Errorf("decode sig: %w", serr)
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(parts[0]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", "", errors.New("bad signature")
	}
	var c struct {
		User  string `json:"user"`
		Exp   int64  `json:"exp"`
		Nonce string `json:"n"`
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return "", "", fmt.Errorf("parse claims: %w", err)
	}
	if c.User == "" || c.Nonce == "" {
		return "", "", errors.New("missing user or nonce")
	}
	now := time.Now().Unix()
	if c.Exp == 0 || now > c.Exp {
		return "", "", errors.New("token expired")
	}
	if c.Exp-now > int64(recoveryBootstrapMaxTTL.Seconds())+60 {
		return "", "", errors.New("token TTL too long (refusing implausible exp)")
	}
	return c.User, c.Nonce, nil
}

// GET /api/v1/auth/bootstrap-recover/preview?token=...
//
// Frontend uses this before showing the password form so we can surface
// "token invalid/expired/used" before the user types anything.
func (s *recoverBootstrapService) handlePreview(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	user, nonce, err := s.verify(token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: err.Error()})
		return
	}
	if s.used.seen(nonce) {
		writeJSON(w, http.StatusConflict, envelope{OK: false, Error: "this recovery link has already been used"})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"user": user,
	}})
}

// POST /api/v1/auth/bootstrap-recover
//
//	{ "token": "rcv-...", "new_password": "..." }
//
// On success: replaces the password hash and burns the nonce. Does NOT
// log the user in — they then go to /login and authenticate normally.
func (s *recoverBootstrapService) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var req struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	if len(req.NewPassword) < 8 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "password must be at least 8 characters"})
		return
	}
	user, nonce, err := s.verify(req.Token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: err.Error()})
		return
	}
	if s.used.seen(nonce) {
		writeJSON(w, http.StatusConflict, envelope{OK: false, Error: "this recovery link has already been used"})
		return
	}

	// Update in-memory + persist via authStore's locked path.
	s.auth.mu.Lock()
	op, ok := s.auth.operators[user]
	if !ok {
		s.auth.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "user no longer exists"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		s.auth.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	op.PasswordHash = string(hash)
	s.auth.operators[user] = op
	s.auth.mu.Unlock()
	if err := s.auth.persist(); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "persist: " + err.Error()})
		return
	}
	if err := s.used.record(nonce, user); err != nil {
		log.Printf("recover-bootstrap: persist used nonce: %v", err)
		// Not fatal — we already changed the password, but the same token
		// could now be replayed until restart. Worth a log line.
	}
	log.Printf("recover-bootstrap: password reset for %s via mint-recover URL", user)
	auditRecord(r, AuditEvent{
		Event: "break-glass.recover.use", Severity: auditSevCritical, Actor: user, Target: user,
		Details: map[string]any{"via": "mint-recover-url"},
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"user":      user,
		"login_url": "/login",
	}})
}
