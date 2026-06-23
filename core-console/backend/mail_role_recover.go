// mail_role_recover.go — admin-side proxy for role mailbox recovery.
//
// Admin UI (Security.vue) calls this; this proxies to ncn-mail on pop-03
// signed with the operator-mail-bridge.key. ncn-mail verifies the
// signature, allowlists postmaster/noc/hostmaster/abuse/security, and
// returns a one-shot recovery URL. We pass that URL back to the UI.
//
// Authorization: admin operator session (handled by the route guard in
// main.go). The operator-bridge.key is the host-to-host trust anchor.
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
	"strings"
	"time"
)

const (
	roleRecoverIntent      = "role-recover"
	roleRecoverEndpoint    = "https://" + webmailHost + "/api/v1/mail/admin/role-recover"
	roleRecoverHTTPTimeout = 10 * time.Second
)

// roleMailboxAllowlist is the same list as on the webmail side; we check
// it here too so we 400 on bad input without crossing the wire.
var roleMailboxAllowlist = map[string]struct{}{
	"postmaster": {}, "noc": {}, "hostmaster": {}, "abuse": {}, "security": {},
}

// POST /api/v1/auth/mail-role-recover
//
// Body: { "mailbox": "noc" | "noc@example.com" }
// Auth: admin operator session.
//
// On success: returns { mailbox, url, expires_at } from the webmail side.
func (m *mailBridgeService) handleRoleRecover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	if len(m.key) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false,
			Error: "operator-bridge key not installed on this host"})
		return
	}
	c, ok := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if !ok || c == nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}

	var req struct {
		Mailbox string `json:"mailbox"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	mb := strings.ToLower(strings.TrimSpace(req.Mailbox))
	// accept both "noc" and "noc@example.com" forms; check the local-part
	// against the allowlist.
	local := mb
	if i := strings.Index(mb, "@"); i >= 0 {
		local = mb[:i]
	}
	if _, ok := roleMailboxAllowlist[local]; !ok {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: "mailbox not in role allowlist (postmaster/noc/hostmaster/abuse/security)"})
		return
	}

	url, expiresAt, err := m.mintRoleRecover(local, c.Sub)
	if err != nil {
		log.Printf("role-recover: proxy to webmail failed: %v", err)
		writeJSON(w, http.StatusBadGateway, envelope{OK: false, Error: err.Error()})
		return
	}
	log.Printf("role-recover: minted URL for %s@ by operator=%s", local, c.Sub)
	auditRecord(r, AuditEvent{
		Event: "mail.role-recover.mint", Severity: auditSevCritical, Actor: c.Sub,
		Target: local + "@example.com",
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"mailbox":    local + "@example.com",
		"url":        url,
		"expires_at": expiresAt,
	}})
}

// mintRoleRecover signs a request body with the bridge key, POSTs to
// ncn-mail on pop-03, and unmarshals the URL out of the response.
func (m *mailBridgeService) mintRoleRecover(local, by string) (url, expiresAt string, err error) {
	nonceBytes := make([]byte, 8)
	_, _ = rand.Read(nonceBytes)

	body := map[string]any{
		"intent":  roleRecoverIntent,
		"mailbox": local + "@example.com",
		"by":      by,
		"ts":      time.Now().Unix(),
		"nonce":   hex.EncodeToString(nonceBytes),
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", "", err
	}
	mac := hmac.New(sha256.New, m.key)
	mac.Write(raw)
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	httpReq, err := http.NewRequest(http.MethodPost, roleRecoverEndpoint, bytes.NewReader(raw))
	if err != nil {
		return "", "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Bridge-Sig", sig)

	client := &http.Client{Timeout: roleRecoverHTTPTimeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", "", fmt.Errorf("contact webmail: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	var env struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
		Data  struct {
			Mailbox   string `json:"mailbox"`
			URL       string `json:"url"`
			ExpiresAt string `json:"expires_at"`
		} `json:"data"`
	}
	if jerr := json.Unmarshal(respBody, &env); jerr != nil {
		return "", "", fmt.Errorf("webmail returned non-JSON (HTTP %d): %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	if !env.OK || env.Data.URL == "" {
		msg := env.Error
		if msg == "" {
			msg = fmt.Sprintf("webmail rejected (HTTP %d)", resp.StatusCode)
		}
		return "", "", errors.New(msg)
	}
	return env.Data.URL, env.Data.ExpiresAt, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
