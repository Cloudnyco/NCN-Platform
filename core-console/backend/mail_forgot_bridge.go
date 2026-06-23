// mail_forgot_bridge.go — admin-side proxy to the webmail forgot-password
// queue, so the NCN admin console can render the same pending requests
// the webmail admin panel shows. Dual-track display: admins can act from
// either UI.
//
// Authorization: admin operator session (handled by the route guard in
// main.go). The operator-bridge key is the host-to-host trust anchor —
// every outbound request is HMAC-SHA256-signed end-to-end and includes
// an "intent" + timestamp + nonce to make replay across flows impossible.
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
	forgotBridgeIntentList    = "forgot-list"
	forgotBridgeIntentDismiss = "forgot-dismiss"
	forgotBridgeIntentApprove = "forgot-approve"
	forgotBridgeListURL       = "https://" + webmailHost + "/api/v1/mail/admin/forgot-bridge/list"
	forgotBridgeDismissURL    = "https://" + webmailHost + "/api/v1/mail/admin/forgot-bridge/dismiss"
	forgotBridgeApproveURL    = "https://" + webmailHost + "/api/v1/mail/admin/forgot-bridge/approve"
	forgotBridgeHTTPTimeout   = 10 * time.Second
)

// forgotEntry mirrors the JSON shape ncn-mail emits in its admin list.
type forgotEntry struct {
	ID          string `json:"id"`
	Mailbox     string `json:"mailbox"`
	RequestedAt string `json:"requested_at"`
	IP          string `json:"ip"`
	UserAgent   string `json:"ua,omitempty"`
}

// GET /api/v1/auth/mail-forgot
// Admin operator session required. Proxies the webmail queue snapshot.
func (m *mailBridgeService) handleForgotList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET only"})
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
	entries, err := m.fetchForgotList(c.Sub)
	if err != nil {
		log.Printf("forgot-bridge list: proxy to webmail failed: %v", err)
		writeJSON(w, http.StatusBadGateway, envelope{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: entries})
}

// DELETE /api/v1/auth/mail-forgot/<id>
// Admin operator session required. Proxies the dismiss to webmail.
func (m *mailBridgeService) handleForgotDismiss(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "DELETE only"})
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
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/mail-forgot/")
	id = strings.TrimSuffix(id, "/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "id required"})
		return
	}
	if err := m.dismissForgot(id, c.Sub); err != nil {
		log.Printf("forgot-bridge dismiss id=%s: proxy to webmail failed: %v", id, err)
		writeJSON(w, http.StatusBadGateway, envelope{OK: false, Error: err.Error()})
		return
	}
	log.Printf("forgot-bridge dismiss: id=%s by operator=%s", id, c.Sub)
	auditRecord(r, AuditEvent{
		Event: "mail.forgot-queue.dismiss", Severity: auditSevWarn, Actor: c.Sub, Target: id,
	})
	writeJSON(w, http.StatusOK, envelope{OK: true})
}

// fetchForgotList signs an empty list-intent body, POSTs to webmail, and
// unmarshals the queue.
func (m *mailBridgeService) fetchForgotList(by string) ([]forgotEntry, error) {
	raw, err := m.signedBridgeBody(forgotBridgeIntentList, by, "")
	if err != nil {
		return nil, err
	}
	respBody, err := m.bridgePost(forgotBridgeListURL, raw)
	if err != nil {
		return nil, err
	}
	var env struct {
		OK    bool          `json:"ok"`
		Error string        `json:"error,omitempty"`
		Data  []forgotEntry `json:"data"`
	}
	if err := json.Unmarshal(respBody, &env); err != nil {
		return nil, fmt.Errorf("webmail non-JSON: %s", truncate(string(respBody), 200))
	}
	if !env.OK {
		if env.Error == "" {
			env.Error = "webmail rejected"
		}
		return nil, errors.New(env.Error)
	}
	return env.Data, nil
}

func (m *mailBridgeService) dismissForgot(id, by string) error {
	raw, err := m.signedBridgeBody(forgotBridgeIntentDismiss, by, id)
	if err != nil {
		return err
	}
	respBody, err := m.bridgePost(forgotBridgeDismissURL, raw)
	if err != nil {
		return err
	}
	var env struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(respBody, &env); err != nil {
		return fmt.Errorf("webmail non-JSON: %s", truncate(string(respBody), 200))
	}
	if !env.OK {
		if env.Error == "" {
			env.Error = "webmail rejected"
		}
		return errors.New(env.Error)
	}
	return nil
}

// forgotApproveResult is the data field of a successful approve response.
type forgotApproveResult struct {
	Mailbox   string `json:"mailbox"`
	SentTo    string `json:"sent_to"`
	ExpiresAt string `json:"expires_at"`
}

func (m *mailBridgeService) approveForgot(id, by string) (forgotApproveResult, error) {
	var out forgotApproveResult
	raw, err := m.signedBridgeBody(forgotBridgeIntentApprove, by, id)
	if err != nil {
		return out, err
	}
	respBody, err := m.bridgePost(forgotBridgeApproveURL, raw)
	if err != nil {
		return out, err
	}
	var env struct {
		OK    bool                `json:"ok"`
		Error string              `json:"error,omitempty"`
		Data  forgotApproveResult `json:"data"`
	}
	if err := json.Unmarshal(respBody, &env); err != nil {
		return out, fmt.Errorf("webmail non-JSON: %s", truncate(string(respBody), 200))
	}
	if !env.OK {
		if env.Error == "" {
			env.Error = "webmail rejected"
		}
		return out, errors.New(env.Error)
	}
	return env.Data, nil
}

// POST /api/v1/auth/mail-forgot/<id>/approve
// Admin operator session required. Asks webmail to mint a mailbox-recover
// URL for the requesting user and email it to them, then removes the entry
// from the queue. Audited as critical (privilege-equivalent of an admin
// reset, but the user proves they're at their email).
func (m *mailBridgeService) handleForgotApprove(w http.ResponseWriter, r *http.Request) {
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
	// Path: /api/v1/auth/mail-forgot/<id>/approve
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/mail-forgot/")
	rest = strings.TrimSuffix(rest, "/approve")
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "id required"})
		return
	}
	res, err := m.approveForgot(rest, c.Sub)
	if err != nil {
		log.Printf("forgot-bridge approve id=%s: proxy to webmail failed: %v", rest, err)
		writeJSON(w, http.StatusBadGateway, envelope{OK: false, Error: err.Error()})
		return
	}
	log.Printf("forgot-bridge approve: id=%s mailbox=%s by operator=%s", rest, res.Mailbox, c.Sub)
	auditRecord(r, AuditEvent{
		Event: "mail.forgot-queue.approve", Severity: auditSevCritical, Actor: c.Sub,
		Target:  res.Mailbox,
		Details: map[string]any{"id": rest, "expires_at": res.ExpiresAt},
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: res})
}

// signedBridgeBody assembles the standard intent/by/ts/nonce envelope
// (plus an optional id for dismiss). The body is HMAC-signed by the
// caller via the X-Bridge-Sig header; this returns the raw JSON to feed
// to bridgePost.
func (m *mailBridgeService) signedBridgeBody(intent, by, id string) ([]byte, error) {
	nonceBytes := make([]byte, 8)
	_, _ = rand.Read(nonceBytes)
	body := map[string]any{
		"intent": intent,
		"by":     by,
		"ts":     time.Now().Unix(),
		"nonce":  hex.EncodeToString(nonceBytes),
	}
	if id != "" {
		body["id"] = id
	}
	return json.Marshal(body)
}

// bridgePost wraps the shared "POST + HMAC + read body" pattern.
func (m *mailBridgeService) bridgePost(url string, raw []byte) ([]byte, error) {
	mac := hmac.New(sha256.New, m.key)
	mac.Write(raw)
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bridge-Sig", sig)

	client := &http.Client{Timeout: forgotBridgeHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contact webmail: %w", err)
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 65536))
}
