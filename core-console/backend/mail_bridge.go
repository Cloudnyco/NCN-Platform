// mail_bridge.go — mints HMAC-signed operator-self-invite tokens that the
// webmail (ncn-mail on pop-03) verifies offline.
//
// Trust model:
//   * One symmetric HMAC-SHA256 key, shared with pop-03 at
//     /etc/ncn-core-console/operator-mail-bridge.key (this host) ↔
//     /etc/ncn-mail/operator-bridge.key (pop-03). Generated once, copied
//     out-of-band, never rotated by automation.
//   * Tokens live 15 minutes max. Format:
//        op-<base64url(JSON payload)>.<base64url(HMAC-SHA256(key, payload))>
//   * Operator session on this host is the upstream authn — we only get to
//     this code path through `protected()`, so claims.Sub is already
//     a valid operator username.
package main

import (
	"bytes"
	"crypto/hmac"
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
	"os"
	"time"
)

const (
	mailBridgeKeyPath = authConfigDir + "/operator-mail-bridge.key"
	mailBridgeTTL     = 10 * time.Minute
	webmailHost       = "mail.example.com"

	// HMAC-signed system-mail dispatch shared with webmail (pop-03). Same
	// /admin/send-bridge endpoint that peering decisions and applicant
	// acknowledgements use — webmail wraps the {to, subject, headline,
	// paragraphs} payload in the noreply chrome (logo + Terms|Privacy
	// footer) before SMTP.
	systemMailBridgeURL    = "https://" + webmailHost + "/api/v1/mail/admin/send-bridge"
	systemMailBridgeIntent = "send-system-mail"
)

// loadMailBridgeKey reads the shared HMAC key. Returns nil + nil if absent
// (the bridge endpoint will then return 503 — feature is opt-in).
func loadMailBridgeKey() ([]byte, error) {
	data, err := os.ReadFile(mailBridgeKeyPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("mail-bridge: %s missing — operator self-register disabled", mailBridgeKeyPath)
			return nil, nil
		}
		return nil, err
	}
	if len(data) < 16 {
		return nil, fmt.Errorf("%s too short (%d bytes; expected >=16)", mailBridgeKeyPath, len(data))
	}
	return data, nil
}

type mailBridgeService struct {
	key  []byte
	auth *authStore
}

func newMailBridgeService(auth *authStore) (*mailBridgeService, error) {
	k, err := loadMailBridgeKey()
	if err != nil {
		return nil, err
	}
	return &mailBridgeService{key: k, auth: auth}, nil
}

// POST /api/v1/auth/mail-self-invite
//
// Caller must already hold an operator session (route is `protected()`).
// Returns { url, token, expires_at }.
func (m *mailBridgeService) handleSelfInvite(w http.ResponseWriter, r *http.Request) {
	if len(m.key) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false,
			Error: "operator-bridge key not installed; ask an admin to provision " + mailBridgeKeyPath})
		return
	}
	c, ok := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if !ok || c == nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}

	// Look up role for richer claims (informational only — pop-03 doesn't
	// actually enforce role currently).
	m.auth.mu.RLock()
	op, exists := m.auth.operators[c.Sub]
	m.auth.mu.RUnlock()
	if !exists {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "operator record missing"})
		return
	}
	role := op.Role
	if role == "" {
		role = "operator"
	}

	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	exp := time.Now().Add(mailBridgeTTL).Unix()

	payload, err := json.Marshal(struct {
		Op    string `json:"op"`
		Role  string `json:"role"`
		Exp   int64  `json:"exp"`
		Nonce string `json:"n"`
	}{
		Op:    c.Sub,
		Role:  role,
		Exp:   exp,
		Nonce: hex.EncodeToString(nonce),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)

	mac := hmac.New(sha256.New, m.key)
	mac.Write([]byte(payloadB64))
	sigB64 := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	token := "op-" + payloadB64 + "." + sigB64
	url := "https://" + webmailHost + "/invite/" + token

	log.Printf("mail-bridge: minted self-invite for operator=%s role=%s exp=%s", c.Sub, role, time.Unix(exp, 0).Format(time.RFC3339))
	auditRecord(r, AuditEvent{
		Event: "mail.self-invite.mint", Severity: auditSevWarn, Actor: c.Sub, Target: c.Sub,
		Details: map[string]any{"role": role},
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"token":      token,
		"url":        url,
		"expires_at": time.Unix(exp, 0).UTC().Format(time.RFC3339),
		"operator":   c.Sub,
		"role":       role,
	}})
}

// SendSystemMail dispatches one noreply-styled email through the webmail
// send-bridge. Any caller that needs to notify a user (invite, peering
// decision, system alert) uses this — it's the only place in ncn-api
// that knows the bridge URL + signing protocol.
//
// `kind` is a free-form tag included in logs and audit ("invite",
// "peering.approved", etc.) — webmail doesn't read it, it's just for our
// own breadcrumbing. Returns nil on HTTP 200 from webmail; otherwise the
// error carries enough detail (status + truncated body) to triage.
//
// Bridge key absent → returns a typed error so callers can decide
// whether to surface "we couldn't send the email, here's the URL to copy"
// vs. fail hard.
var errBridgeKeyMissing = errors.New("operator-mail-bridge.key not installed")

func (m *mailBridgeService) SendSystemMail(to, subject, headline string, paragraphs []string, kind string) error {
	if m == nil || len(m.key) == 0 {
		return errBridgeKeyMissing
	}
	nonceB := make([]byte, 8)
	_, _ = rand.Read(nonceB)
	payload := map[string]any{
		"intent":     systemMailBridgeIntent,
		"by":         "ncn-api/" + kind,
		"ts":         time.Now().Unix(),
		"nonce":      hex.EncodeToString(nonceB),
		"to":         to,
		"subject":    subject,
		"headline":   headline,
		"paragraphs": paragraphs,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, m.key)
	mac.Write(raw)
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	req, err := http.NewRequest(http.MethodPost, systemMailBridgeURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bridge-Sig", sig)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("send-bridge HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	return nil
}

