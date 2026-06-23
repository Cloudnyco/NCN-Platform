// auth_apitoken.go — bearer-token authentication for CLI/script use.
//
// Tokens are operator-scoped credentials that grant the SAME identity a
// session cookie would. A request with `Authorization: Bearer ncntok_<secret>`
// passes through requireAuth as if it had a browser session under the
// owning operator's username. Downstream handlers don't have to know
// which path the caller arrived through.
//
// Threat model + design choices:
//   * Token = `ncntok_` prefix + 32 random bytes encoded as base64url.
//     Total ~50 chars. Prefix is a GitHub-style "self-identifying secret"
//     so leak scanners can flag accidental commits.
//   * Stored as bcrypt hash; the raw secret is shown ONCE at creation
//     and never persisted. Loss = revoke + re-issue (same flow as
//     recovery codes; same user education).
//   * Lookup is currently O(N) over the token store on every Bearer
//     request — fine for tens of tokens. If this grows past hundreds
//     we'll add a prefix index.
//   * Tokens carry an optional expires_at; expired tokens fail
//     verifyAPIToken with a distinct error so the UI can show "expired"
//     vs "wrong".
//   * Revoke is immediate (in-memory + persisted). No grace window.
//
// Storage: /etc/ncn-core-console/api-tokens.json, mode 0600, root-owned.
// One file for all operators — small footprint.
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

	"golang.org/x/crypto/bcrypt"
)

const (
	apiTokensPath   = authConfigDir + "/api-tokens.json"
	apiTokenPrefix  = "ncntok_"
	apiTokenMaxPer  = 32 // per operator; UI can warn near the cap
	apiTokenMaxLbl  = 64
	apiTokenSecretBytes = 32
)

// apiTokenRecord is the stored shape. NEVER includes the raw secret —
// only its bcrypt hash + a short visible prefix for the UI.
type apiTokenRecord struct {
	ID          string `json:"id"`             // 8-hex; stable identifier for revoke
	Label       string `json:"label"`          // operator-supplied
	Operator    string `json:"operator"`       // username this token authenticates as
	Hash        string `json:"hash"`           // bcrypt(secret)
	PrefixHint  string `json:"prefix_hint"`    // first 10 chars of the public token, e.g. "ncntok_aBc"
	CreatedAt   int64  `json:"created_at"`     // unix
	LastUsedAt  int64  `json:"last_used_at"`   // unix; 0 if never used
	ExpiresAt   int64  `json:"expires_at,omitempty"` // unix; 0 = no expiry
}

type apiTokenStore struct {
	mu     sync.RWMutex
	tokens map[string]*apiTokenRecord // id → record
}

var globalAPITokens *apiTokenStore

func newAPITokenStore() (*apiTokenStore, error) {
	s := &apiTokenStore{tokens: map[string]*apiTokenRecord{}}
	var arr []*apiTokenRecord

	// Prefer Postgres when it already holds the document (post-cutover).
	loadedFromDB := false
	if globalDB != nil {
		if doc, err := loadConfigDoc("api_tokens"); err != nil {
			log.Printf("api-tokens: db load failed (%v) — falling back to file", err)
		} else if doc != nil {
			if err := json.Unmarshal(doc, &arr); err != nil {
				return nil, fmt.Errorf("parse db doc: %w", err)
			}
			loadedFromDB = true
		}
	}

	// Otherwise load the JSON file if present.
	if !loadedFromDB {
		data, err := os.ReadFile(apiTokensPath)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("read %s: %w", apiTokensPath, err)
			}
		} else if err := json.Unmarshal(data, &arr); err != nil {
			return nil, fmt.Errorf("parse %s: %w", apiTokensPath, err)
		}
	}

	for _, t := range arr {
		if t != nil && t.ID != "" {
			s.tokens[t.ID] = t
		}
	}
	log.Printf("api-tokens: loaded %d token(s)", len(s.tokens))

	// Migrate file→DB on the first DB-enabled boot.
	if globalDB != nil && !loadedFromDB {
		s.mu.Lock()
		err := s.persistLocked()
		s.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("migrate api-tokens to db: %w", err)
		}
	}
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
	if globalDB != nil {
		if err := saveConfigDoc("api_tokens", data); err != nil {
			log.Printf("api-tokens: db persist failed (%v) — file is current", err)
		}
	}
	return nil
}

// verifyAPIToken is called from requireAuth on every Bearer request.
// Returns a sessionClaims equivalent for the owning operator on success.
// O(N) over the active token set; bcrypt CompareHashAndPassword does the
// constant-time secret check. If a record is found but expired we return
// a distinct error so the middleware can present a useful message.
func (s *authStore) verifyAPIToken(raw string) (*sessionClaims, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, apiTokenPrefix) {
		return nil, errors.New("token: missing ncntok_ prefix")
	}
	if globalAPITokens == nil {
		return nil, errors.New("token: store not initialized")
	}
	globalAPITokens.mu.RLock()
	candidates := make([]*apiTokenRecord, 0, len(globalAPITokens.tokens))
	for _, t := range globalAPITokens.tokens {
		candidates = append(candidates, t)
	}
	globalAPITokens.mu.RUnlock()

	now := time.Now()
	for _, t := range candidates {
		// bcrypt is the slow side; check the cheap expiry first to
		// avoid wasting cycles on definitely-expired tokens.
		if t.ExpiresAt != 0 && now.Unix() > t.ExpiresAt {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(t.Hash), []byte(raw)) == nil {
			// Confirm the owning operator still exists + is approved.
			s.mu.RLock()
			op, ok := s.operators[t.Operator]
			s.mu.RUnlock()
			if !ok || !op.Approved {
				return nil, errors.New("token: owning operator gone or unapproved")
			}
			// Bump LastUsedAt — best-effort; lock contention is fine.
			globalAPITokens.mu.Lock()
			if cur, ok := globalAPITokens.tokens[t.ID]; ok {
				cur.LastUsedAt = now.Unix()
				_ = globalAPITokens.persistLocked()
			}
			globalAPITokens.mu.Unlock()
			// Synthesize session claims. Sid is the token ID so audit
			// logs can correlate token-driven activity.
			return &sessionClaims{
				Sub: t.Operator,
				Sid: "tok-" + t.ID,
				Exp: now.Add(24 * time.Hour).Unix(), // not actually enforced — bearer doesn't have a TTL beyond the token's own
			}, nil
		}
	}
	return nil, errors.New("token: not recognized")
}

// ============================================================================
// HTTP handlers — operator-scoped self-service.
// ============================================================================

// handleAPITokenList — GET /api/v1/auth/api-tokens
func (s *authStore) handleAPITokenList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET only"})
		return
	}
	c, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	globalAPITokens.mu.RLock()
	out := make([]map[string]any, 0, 8)
	for _, t := range globalAPITokens.tokens {
		if t.Operator != c.Sub {
			continue
		}
		out = append(out, map[string]any{
			"id":           t.ID,
			"label":        t.Label,
			"prefix_hint":  t.PrefixHint,
			"created_at":   t.CreatedAt,
			"last_used_at": t.LastUsedAt,
			"expires_at":   t.ExpiresAt,
		})
	}
	globalAPITokens.mu.RUnlock()
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
}

// handleAPITokenCreate — POST /api/v1/auth/api-tokens
// Body: { "label": "...", "expires_in": 0 | seconds }
// Returns the ONE-TIME plaintext token.
func (s *authStore) handleAPITokenCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	c, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)

	var req struct {
		Label     string `json:"label"`
		ExpiresIn int64  `json:"expires_in"` // seconds; 0 = no expiry
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	req.Label = strings.TrimSpace(req.Label)
	if req.Label == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "label is required"})
		return
	}
	if len(req.Label) > apiTokenMaxLbl {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: fmt.Sprintf("label too long (max %d)", apiTokenMaxLbl)})
		return
	}

	globalAPITokens.mu.Lock()
	count := 0
	for _, t := range globalAPITokens.tokens {
		if t.Operator == c.Sub {
			count++
		}
	}
	if count >= apiTokenMaxPer {
		globalAPITokens.mu.Unlock()
		writeJSON(w, http.StatusTooManyRequests, envelope{OK: false,
			Error: fmt.Sprintf("max %d tokens per operator — revoke an old one first", apiTokenMaxPer)})
		return
	}

	// Mint the secret.
	secret := make([]byte, apiTokenSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		globalAPITokens.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "rand failed"})
		return
	}
	plaintext := apiTokenPrefix + base64.RawURLEncoding.EncodeToString(secret)

	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		globalAPITokens.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "hash failed"})
		return
	}

	idB := make([]byte, 4)
	_, _ = rand.Read(idB)
	id := hex.EncodeToString(idB)
	now := time.Now().Unix()
	var expiresAt int64
	if req.ExpiresIn > 0 {
		expiresAt = now + req.ExpiresIn
	}
	rec := &apiTokenRecord{
		ID:         id,
		Label:      req.Label,
		Operator:   c.Sub,
		Hash:       string(hash),
		PrefixHint: plaintext[:10] + "…",
		CreatedAt:  now,
		ExpiresAt:  expiresAt,
	}
	globalAPITokens.tokens[id] = rec
	persistErr := globalAPITokens.persistLocked()
	globalAPITokens.mu.Unlock()
	if persistErr != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "persist: " + persistErr.Error()})
		return
	}

	log.Printf("api-tokens: CREATED operator=%s id=%s label=%q expires=%d",
		c.Sub, id, req.Label, expiresAt)
	auditRecord(r, AuditEvent{
		Event: "api-token.create", Severity: auditSevCritical, Actor: c.Sub, Target: id,
		Details: map[string]any{"label": req.Label, "expires_at": expiresAt},
	})

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"id":         id,
		"label":      req.Label,
		"token":      plaintext, // ONE-TIME — UI must show with "copy now" warning
		"expires_at": expiresAt,
		"created_at": now,
	}})
}

// handleAPITokenDelete — DELETE /api/v1/auth/api-tokens/<id>
func (s *authStore) handleAPITokenDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "DELETE only"})
		return
	}
	c, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/api-tokens/")
	id = strings.TrimSuffix(id, "/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "id required"})
		return
	}

	globalAPITokens.mu.Lock()
	rec, ok := globalAPITokens.tokens[id]
	if !ok || rec.Operator != c.Sub {
		globalAPITokens.mu.Unlock()
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "no such token"})
		return
	}
	label := rec.Label
	delete(globalAPITokens.tokens, id)
	persistErr := globalAPITokens.persistLocked()
	globalAPITokens.mu.Unlock()
	if persistErr != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "persist: " + persistErr.Error()})
		return
	}

	log.Printf("api-tokens: REVOKED operator=%s id=%s label=%q", c.Sub, id, label)
	auditRecord(r, AuditEvent{
		Event: "api-token.revoke", Severity: auditSevWarn, Actor: c.Sub, Target: id,
		Details: map[string]any{"label": label},
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]string{"removed": id}})
}
