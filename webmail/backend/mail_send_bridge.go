// mail_send_bridge.go — admin-side operators on tyo can dispatch
// arbitrary noreply-styled system mail via this HMAC-gated endpoint.
//
// Why it exists:
//   The peering-application flow, future "incident notification" flows,
//   and other admin-initiated emails live in ncn-api on tyo, not in
//   ncn-mail on pop-03. To send those they'd otherwise need their own
//   stash of noreply@'s password — which means key duplication, drift,
//   and a fresh attack surface. Instead they sign the payload with the
//   already-deployed operator-mail-bridge.key and hand off through this
//   endpoint; ncn-mail does the SMTP submission itself.
//
// Same trust model as forgot-bridge / role-recover / sso bridge:
//   * X-Bridge-Sig: base64url(HMAC-SHA256(bridge_key, raw_body))
//   * intent must match the expected verb (defense against cross-flow replay)
//   * 60s timestamp window + nonce
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

const (
	sendBridgeIntent    = "send-system-mail"
	sendBridgeMaxClock  = 60 * time.Second
	sendBridgeBodyLimit = 32 << 10 // 32 KB cap on inbound payload
)

type sendBridgeRequest struct {
	Intent     string   `json:"intent"`
	By         string   `json:"by"`    // operator username on admin (informational)
	TS         int64    `json:"ts"`
	Nonce      string   `json:"nonce"`
	To         string   `json:"to"`
	Subject    string   `json:"subject"`
	Headline   string   `json:"headline"`
	Paragraphs []string `json:"paragraphs"`
}

type sendBridgeService struct {
	mailSvc *mailService
	invites *inviteStore // for bridgeKey access
}

func newSendBridgeService(mailSvc *mailService, invites *inviteStore) *sendBridgeService {
	return &sendBridgeService{mailSvc: mailSvc, invites: invites}
}

// POST /api/v1/mail/admin/send-bridge
func (s *sendBridgeService) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	if len(s.invites.bridgeKey) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false,
			Error: "operator-bridge key not installed on this host"})
		return
	}
	rawSig := r.Header.Get("X-Bridge-Sig")
	if rawSig == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "missing X-Bridge-Sig"})
		return
	}
	sig, err := base64.RawURLEncoding.DecodeString(rawSig)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "decode sig: " + err.Error()})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, sendBridgeBodyLimit))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "read body"})
		return
	}
	mac := hmac.New(sha256.New, s.invites.bridgeKey)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "bad signature"})
		return
	}

	var req sendBridgeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	if req.Intent != sendBridgeIntent {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: "wrong intent (expected " + sendBridgeIntent + ")"})
		return
	}
	now := time.Now().Unix()
	if req.TS == 0 || abs64(now-req.TS) > int64(sendBridgeMaxClock.Seconds()) {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "stale or future timestamp"})
		return
	}

	to := strings.TrimSpace(req.To)
	if _, perr := mail.ParseAddress(to); perr != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid recipient"})
		return
	}
	subj := strings.TrimSpace(req.Subject)
	if subj == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "subject required"})
		return
	}
	headline := strings.TrimSpace(req.Headline)
	if headline == "" {
		headline = subj
	}
	if len(req.Paragraphs) == 0 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "at least one paragraph required"})
		return
	}

	if err := sendSystemMail(to, subj, headline, req.Paragraphs); err != nil {
		log.Printf("send-bridge: sendSystemMail to=%s subj=%q failed: %v", to, subj, err)
		writeJSON(w, http.StatusBadGateway, envelope{OK: false, Error: "smtp dispatch failed"})
		return
	}
	log.Printf("send-bridge: %q → %s (by operator=%s nonce=%s)", subj, to, req.By, req.Nonce)
	writeJSON(w, http.StatusOK, envelope{OK: true})
}
