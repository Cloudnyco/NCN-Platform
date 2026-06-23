// api_token.go — bearer-token auth for the transactional send API.
//
// This mirrors the admin-console `ncntok_` scheme (auth_apitoken.go over on
// core-console) but lives in the webmail keystore and binds a token to a
// SENDER IDENTITY rather than an operator account. A request carrying
// `Authorization: Bearer ncntok_<secret>` to the send API is authorised to
// emit mail AS the token's bound `from` mailbox, over the transports the
// token permits.
//
// Design (kept deliberately identical to the admin side for operator muscle
// memory):
//   * Token = `ncntok_` prefix + 32 random bytes, base64url. The prefix is a
//     self-identifying secret so leak scanners can flag accidental commits.
//   * Stored as a bcrypt hash; the raw secret is shown ONCE at creation and
//     never persisted. Loss = revoke + re-issue.
//   * Lookup is O(N) over the token set per request; bcrypt does the
//     constant-time compare. Fine for tens of keys.
//   * Optional expiry; expired tokens fail with a distinct error.
//
// Storage: /etc/ncn-mail/api-tokens.json, mode 0600, root-owned.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	apiTokensPath       = stateDir + "/api-tokens.json"
	apiTokenPrefix      = "ncntok_"
	apiTokenMaxLbl      = 64
	apiTokenSecretBytes = 32
)

// apiTokenRecord is the stored shape. NEVER includes the raw secret — only
// its bcrypt hash plus a short visible prefix hint for the UI/CLI listing.
type apiTokenRecord struct {
	ID         string   `json:"id"`                     // 8-hex; stable identifier for revoke
	Label      string   `json:"label"`                  // operator-supplied
	Mailbox    string   `json:"mailbox"`                // the From identity this token may send as
	Transports []string `json:"transports"`             // allowed: "local" and/or "gmail"
	Hash       string   `json:"hash"`                   // bcrypt(secret)
	PrefixHint string   `json:"prefix_hint"`            // first 14 chars of the public token
	CreatedAt  int64    `json:"created_at"`             // unix
	LastUsedAt int64    `json:"last_used_at"`           // unix; 0 if never used
	ExpiresAt  int64    `json:"expires_at,omitempty"`   // unix; 0 = no expiry
}

// allowsTransport reports whether this token may use transport t. An empty
// Transports list means "all" (back-compat / convenience).
func (t *apiTokenRecord) allowsTransport(transport string) bool {
	if len(t.Transports) == 0 {
		return true
	}
	for _, a := range t.Transports {
		if strings.EqualFold(a, transport) {
			return true
		}
	}
	return false
}

type apiTokenStore struct {
	mu      sync.RWMutex
	tokens  map[string]*apiTokenRecord // id → record
	modTime time.Time                  // mtime of apiTokensPath as last loaded
}

var globalAPITokens *apiTokenStore

// loadFromDiskLocked replaces the in-memory map from the on-disk file and
// records its mtime. Caller holds s.mu. Missing file = empty store (not an
// error — the file is created on first `api-key create`).
func (s *apiTokenStore) loadFromDiskLocked() error {
	fi, statErr := os.Stat(apiTokensPath)
	data, err := os.ReadFile(apiTokensPath)
	if err != nil {
		if os.IsNotExist(err) {
			s.tokens = map[string]*apiTokenRecord{}
			s.modTime = time.Time{}
			return nil
		}
		return fmt.Errorf("read %s: %w", apiTokensPath, err)
	}
	var arr []*apiTokenRecord
	if err := json.Unmarshal(data, &arr); err != nil {
		return fmt.Errorf("parse %s: %w", apiTokensPath, err)
	}
	m := make(map[string]*apiTokenRecord, len(arr))
	for _, t := range arr {
		if t != nil && t.ID != "" {
			m[t.ID] = t
		}
	}
	s.tokens = m
	if statErr == nil {
		s.modTime = fi.ModTime()
	}
	return nil
}

// reloadIfChanged re-reads the token file when its mtime advanced since the
// last load. This is what lets a key minted by the CLI (a separate process)
// authenticate against the long-running server without a restart. Cheap: one
// stat per call, a re-parse only when the file actually changed.
func (s *apiTokenStore) reloadIfChanged() {
	fi, err := os.Stat(apiTokensPath)
	if err != nil {
		return // missing file: nothing to pick up
	}
	s.mu.RLock()
	cur := s.modTime
	s.mu.RUnlock()
	if !fi.ModTime().After(cur) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-check under the write lock (another goroutine may have reloaded).
	if fi2, err := os.Stat(apiTokensPath); err == nil && fi2.ModTime().After(s.modTime) {
		if err := s.loadFromDiskLocked(); err != nil {
			log.Printf("api-tokens: reload failed: %v", err)
		}
	}
}

func newAPITokenStore() (*apiTokenStore, error) {
	s := &apiTokenStore{tokens: map[string]*apiTokenRecord{}}
	s.mu.Lock()
	err := s.loadFromDiskLocked()
	n := len(s.tokens)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	log.Printf("api-tokens: loaded %d token(s) from %s", n, apiTokensPath)
	return s, nil
}

func (s *apiTokenStore) persistLocked() error {
	arr := make([]*apiTokenRecord, 0, len(s.tokens))
	for _, t := range s.tokens {
		arr = append(arr, t)
	}
	data, err := json.MarshalIndent(arr, "", "  ")
	if err != nil {
		return err
	}
	tmp := apiTokensPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, apiTokensPath); err != nil {
		return err
	}
	// Track our own write's mtime so reloadIfChanged doesn't re-read what we
	// just wrote (and, more importantly, doesn't treat our write as a
	// foreign change to roll back).
	if fi, err := os.Stat(apiTokensPath); err == nil {
		s.modTime = fi.ModTime()
	}
	return nil
}

// mint creates a new token bound to `mailbox` over `transports`, persists the
// bcrypt hash, and returns (record, rawSecret). The raw secret is the ONLY
// time the caller can see it.
func (s *apiTokenStore) mint(label, mailbox string, transports []string, expiresAt int64) (*apiTokenRecord, string, error) {
	label = strings.TrimSpace(label)
	if len(label) > apiTokenMaxLbl {
		label = label[:apiTokenMaxLbl]
	}
	mailbox = strings.ToLower(strings.TrimSpace(mailbox))
	if mailbox == "" {
		return nil, "", errors.New("mint: mailbox (sender identity) required")
	}
	for _, tr := range transports {
		if tr != "local" && tr != "gmail" {
			return nil, "", fmt.Errorf("mint: unknown transport %q (want local|gmail)", tr)
		}
	}

	secretRaw := make([]byte, apiTokenSecretBytes)
	if _, err := rand.Read(secretRaw); err != nil {
		return nil, "", fmt.Errorf("mint: rand: %w", err)
	}
	raw := apiTokenPrefix + base64.RawURLEncoding.EncodeToString(secretRaw)

	hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.DefaultCost)
	if err != nil {
		return nil, "", fmt.Errorf("mint: bcrypt: %w", err)
	}
	idRaw := make([]byte, 4)
	_, _ = rand.Read(idRaw)
	hint := raw
	if len(hint) > 14 {
		hint = hint[:14]
	}
	rec := &apiTokenRecord{
		ID:         hex.EncodeToString(idRaw),
		Label:      label,
		Mailbox:    mailbox,
		Transports: transports,
		Hash:       string(hash),
		PrefixHint: hint,
		CreatedAt:  time.Now().Unix(),
		ExpiresAt:  expiresAt,
	}
	s.mu.Lock()
	s.tokens[rec.ID] = rec
	err = s.persistLocked()
	s.mu.Unlock()
	if err != nil {
		return nil, "", fmt.Errorf("mint: persist: %w", err)
	}
	return rec, raw, nil
}

func (s *apiTokenStore) revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tokens[id]; !ok {
		return fmt.Errorf("revoke: no token with id %q", id)
	}
	delete(s.tokens, id)
	return s.persistLocked()
}

func (s *apiTokenStore) list() []*apiTokenRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*apiTokenRecord, 0, len(s.tokens))
	for _, t := range s.tokens {
		out = append(out, t)
	}
	return out
}

// verify resolves a raw bearer secret to its record, or an error. On success
// it bumps LastUsedAt (best-effort persist). Distinct error for expiry so the
// caller can message "expired" vs "unknown".
func (s *apiTokenStore) verify(raw string) (*apiTokenRecord, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, apiTokenPrefix) {
		return nil, errors.New("token: missing ncntok_ prefix")
	}
	// Pick up keys minted by the CLI (separate process) before we read or
	// mutate the map — so a fresh key authenticates without a server restart,
	// and a later LastUsedAt persist can't clobber it.
	s.reloadIfChanged()
	s.mu.RLock()
	candidates := make([]*apiTokenRecord, 0, len(s.tokens))
	for _, t := range s.tokens {
		candidates = append(candidates, t)
	}
	s.mu.RUnlock()

	now := time.Now()
	for _, t := range candidates {
		// Cheap expiry check before the expensive bcrypt compare.
		if t.ExpiresAt != 0 && now.Unix() > t.ExpiresAt {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(t.Hash), []byte(raw)) == nil {
			s.mu.Lock()
			t.LastUsedAt = now.Unix()
			_ = s.persistLocked()
			s.mu.Unlock()
			return t, nil
		}
	}
	// Distinguish "exists but expired" for a better message: re-scan ignoring
	// expiry and report expired if a hash matches.
	for _, t := range candidates {
		if t.ExpiresAt != 0 && now.Unix() > t.ExpiresAt &&
			bcrypt.CompareHashAndPassword([]byte(t.Hash), []byte(raw)) == nil {
			return nil, errors.New("token: expired")
		}
	}
	return nil, errors.New("token: unknown or revoked")
}
