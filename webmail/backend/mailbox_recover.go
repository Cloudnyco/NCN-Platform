// mailbox_recover.go — verifier half of `ncn-mail admin mint-recover`.
//
// Trust model:
//   * One symmetric HMAC-SHA256 key at /etc/ncn-mail/recovery-bootstrap.key
//     (auto-generated on first mint; mode 0600 root). The CLI signs
//     tokens with this key; the HTTP handler below verifies them.
//   * Tokens live 15 minutes max. Format:
//        mb-<base64url(JSON payload)>.<base64url(HMAC-SHA256(key, payload))>
//   * Each token's nonce is recorded in recovery-bootstrap-used.json on
//     first successful redemption — no replay, even within the TTL window.
//
// This endpoint is intentionally NOT protected (the user has lost the
// mailbox password, that's the whole point). Trust comes entirely from
// possession of a valid HMAC signature, which in turn requires SSH root
// on pop-03 to mint.
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
)

const (
	mailboxRecoveryKeyPath  = stateDir + "/recovery-bootstrap.key"
	mailboxRecoveryUsedPath = stateDir + "/recovery-bootstrap-used.json"
	mailboxRecoveryPrefix   = "mb-"
	mailboxRecoveryMaxTTL   = 15 * time.Minute
)

// loadOrCreateMailboxRecoveryKey returns the shared HMAC key. Generates
// 32 random bytes on first call. Called by both the CLI (mint side) and
// the HTTP verifier (loaded lazily so a fresh mint goes live without a
// service restart).
func loadOrCreateMailboxRecoveryKey() ([]byte, error) {
	if data, err := os.ReadFile(mailboxRecoveryKeyPath); err == nil {
		if len(data) < 16 {
			return nil, fmt.Errorf("%s too short (%d bytes)", mailboxRecoveryKeyPath, len(data))
		}
		return data, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(mailboxRecoveryKeyPath, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// mailboxUsedNonceStore tracks redeemed nonces (single-use enforcement).
type mailboxUsedNonceStore struct {
	mu      sync.Mutex
	entries []mailboxUsedNonceEntry
}

type mailboxUsedNonceEntry struct {
	Nonce   string `json:"nonce"`
	Mailbox string `json:"mailbox"`
	UsedAt  int64  `json:"used_at"`
}

func (s *mailboxUsedNonceStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(mailboxRecoveryUsedPath)
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
	return json.Unmarshal(data, &s.entries)
}

func (s *mailboxUsedNonceStore) seen(nonce string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.Nonce == nonce {
			return true
		}
	}
	return false
}

func (s *mailboxUsedNonceStore) record(nonce, mailbox string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, mailboxUsedNonceEntry{
		Nonce: nonce, Mailbox: mailbox, UsedAt: time.Now().Unix(),
	})
	// 24h retention — anything older than 15-min TTL is meaningless for
	// replay protection but useful for forensic audit.
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	kept := s.entries[:0]
	for _, e := range s.entries {
		if e.UsedAt >= cutoff {
			kept = append(kept, e)
		}
	}
	s.entries = kept

	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := mailboxRecoveryUsedPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, mailboxRecoveryUsedPath)
}

// mailboxRecoverService wraps the verifier with its key + nonce store.
type mailboxRecoverService struct {
	key  []byte
	used *mailboxUsedNonceStore
}

func newMailboxRecoverService() (*mailboxRecoverService, error) {
	used := &mailboxUsedNonceStore{}
	if err := used.load(); err != nil {
		return nil, err
	}
	// Key is created lazily on first mint by the CLI. If absent here, we
	// stay nil-key and the handler tries to read it again on each request.
	data, err := os.ReadFile(mailboxRecoveryKeyPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("mailbox-recover: %s missing — endpoint disabled until first `ncn-mail admin mint-recover` call",
				mailboxRecoveryKeyPath)
			return &mailboxRecoverService{used: used}, nil
		}
		return nil, err
	}
	if len(data) < 16 {
		return nil, fmt.Errorf("%s too short (%d bytes)", mailboxRecoveryKeyPath, len(data))
	}
	log.Printf("mailbox-recover: key loaded (%d bytes)", len(data))
	return &mailboxRecoverService{key: data, used: used}, nil
}

// verify parses + validates a token. Returns the mailbox + nonce on
// success. Does NOT check the nonce store — that's caller's job, post-
// verify.
func (s *mailboxRecoverService) verify(token string) (mailbox, nonce string, err error) {
	if len(s.key) == 0 {
		k, kerr := os.ReadFile(mailboxRecoveryKeyPath)
		if kerr != nil {
			return "", "", errors.New("mailbox-recover key not provisioned")
		}
		s.key = k
	}
	if !strings.HasPrefix(token, mailboxRecoveryPrefix) {
		return "", "", errors.New("not a mailbox-recover token")
	}
	body := strings.TrimPrefix(token, mailboxRecoveryPrefix)
	parts := strings.Split(body, ".")
	if len(parts) != 2 {
		return "", "", errors.New("malformed token")
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
		Mailbox string `json:"mb"`
		Exp     int64  `json:"exp"`
		Nonce   string `json:"n"`
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return "", "", fmt.Errorf("parse claims: %w", err)
	}
	if c.Mailbox == "" || c.Nonce == "" {
		return "", "", errors.New("missing mailbox or nonce")
	}
	now := time.Now().Unix()
	if c.Exp == 0 || now > c.Exp {
		return "", "", errors.New("token expired")
	}
	if c.Exp-now > int64(mailboxRecoveryMaxTTL.Seconds())+60 {
		return "", "", errors.New("token TTL too long")
	}
	return strings.ToLower(c.Mailbox), c.Nonce, nil
}

// GET /api/v1/mail/admin/bootstrap-recover/preview?token=...
//
// Surfaces "valid / expired / used" without requiring the user to type a
// new password first.
func (s *mailboxRecoverService) handlePreview(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	mailbox, nonce, err := s.verify(token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: err.Error()})
		return
	}
	if s.used.seen(nonce) {
		writeJSON(w, http.StatusConflict, envelope{OK: false, Error: "this recovery link has already been used"})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"mailbox": mailbox,
	}})
}

// POST /api/v1/mail/admin/bootstrap-recover
//
//	{ "token": "mb-...", "new_password": "..." }
//
// On success: rewrites the bcrypt hash in /etc/dovecot/users via
// replaceMailboxPassword and burns the nonce. Does NOT log the user in.
func (s *mailboxRecoverService) handleSubmit(w http.ResponseWriter, r *http.Request) {
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
	mailbox, nonce, err := s.verify(req.Token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: err.Error()})
		return
	}
	if s.used.seen(nonce) {
		writeJSON(w, http.StatusConflict, envelope{OK: false, Error: "this recovery link has already been used"})
		return
	}
	if err := replaceMailboxPassword(mailbox, req.NewPassword); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "reset failed: " + err.Error()})
		return
	}
	if err := s.used.record(nonce, mailbox); err != nil {
		log.Printf("mailbox-recover: persist used nonce: %v", err)
	}
	log.Printf("mailbox-recover: password reset for %s via mint-recover URL", mailbox)

	// Best-effort heads-up to the mailbox owner that their password just
	// changed. Notification-only (no link); if the user didn't do this,
	// they should contact postmaster. Failure is logged + ignored.
	if err := sendSystemMail(
		mailbox,
		"Your mailbox password was reset",
		"Mailbox Password Reset",
		[]string{
			"The password for " + mailbox + " was just reset via the break-glass recovery URL.",
			"If this was you, no action needed.",
			"If this was NOT you, contact postmaster@example.com immediately — the recovery URL can only be minted by an operator with SSH root on the pop-03 host.",
		},
	); err != nil {
		log.Printf("mailbox-recover: notify failed (continuing): %v", err)
	}

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"mailbox": mailbox,
	}})
}
