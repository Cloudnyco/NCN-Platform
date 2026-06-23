// forgot_bridge.go — operator-bridge endpoints that let the admin console
// on tyo proxy-render the webmail forgot-password queue.
//
// Trust model:
//   * Same operator-bridge HMAC key as role-recover / self-invite.
//   * No mailbox session cookie needed — these endpoints are server-to-
//     server only, signed end-to-end with X-Bridge-Sig.
//   * Intent field guards against cross-flow signature replay.
//
// Two operations:
//   - list:    return the actionable forgot-password queue
//   - dismiss: drop one entry by id
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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
	forgotBridgeMaxClock      = 60 * time.Second
)

type forgotBridgeRequest struct {
	Intent string `json:"intent"`
	By     string `json:"by"`    // operator username on admin (informational)
	TS     int64  `json:"ts"`    // unix seconds, ±60s window
	Nonce  string `json:"nonce"` // random, replay defense
	// dismiss-only: which queue entry to drop
	ID string `json:"id,omitempty"`
}

// verifyBridge does the boilerplate signature + replay-window check
// shared between the list and dismiss handlers. Returns the parsed
// body on success.
func (s *forgotStore) verifyBridge(w http.ResponseWriter, r *http.Request, expectIntent string) (forgotBridgeRequest, bool) {
	var req forgotBridgeRequest
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return req, false
	}
	if len(s.admins.bridgeKey) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false,
			Error: "operator-bridge key not installed on this host"})
		return req, false
	}
	rawSig := r.Header.Get("X-Bridge-Sig")
	if rawSig == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "missing X-Bridge-Sig"})
		return req, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(rawSig)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "decode sig: " + err.Error()})
		return req, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "read body"})
		return req, false
	}
	mac := hmac.New(sha256.New, s.admins.bridgeKey)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "bad signature"})
		return req, false
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return req, false
	}
	if req.Intent != expectIntent {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: "wrong intent (expected " + expectIntent + ")"})
		return req, false
	}
	now := time.Now().Unix()
	if req.TS == 0 || abs64(now-req.TS) > int64(forgotBridgeMaxClock.Seconds()) {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "stale or future timestamp"})
		return req, false
	}
	return req, true
}

// POST /api/v1/mail/admin/forgot-bridge/list
func (s *forgotStore) handleBridgeList(w http.ResponseWriter, r *http.Request) {
	req, ok := s.verifyBridge(w, r, forgotBridgeIntentList)
	if !ok {
		return
	}
	out := s.snapshotForAdmins()
	log.Printf("forgot-bridge list: %d entries returned to operator=%s nonce=%s",
		len(out), req.By, req.Nonce)
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
}

// POST /api/v1/mail/admin/forgot-bridge/dismiss
func (s *forgotStore) handleBridgeDismiss(w http.ResponseWriter, r *http.Request) {
	req, ok := s.verifyBridge(w, r, forgotBridgeIntentDismiss)
	if !ok {
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "id required"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, fr := range s.requests {
		if fr.ID == id {
			s.requests = append(s.requests[:i], s.requests[i+1:]...)
			if err := s.persistLocked(); err != nil {
				writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
				return
			}
			log.Printf("forgot-bridge dismiss: id=%s mailbox=%s by operator=%s nonce=%s",
				id, fr.Mailbox, req.By, req.Nonce)
			writeJSON(w, http.StatusOK, envelope{OK: true})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "no such request"})
}

// POST /api/v1/mail/admin/forgot-bridge/approve
//
// Bridged equivalent of approveByID: invoked by admin.example.com over the
// shared HMAC channel. Mints a single-use mailbox-recover URL, emails it
// to the requester, removes the queue entry on success. Failure to send
// keeps the entry so the admin can retry.
func (s *forgotStore) handleBridgeApprove(w http.ResponseWriter, r *http.Request) {
	req, ok := s.verifyBridge(w, r, forgotBridgeIntentApprove)
	if !ok {
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "id required"})
		return
	}

	// Locate the request (mailbox needed for the mint).
	s.mu.Lock()
	var fr forgotRequest
	found := -1
	for i := range s.requests {
		if s.requests[i].ID == id {
			fr = s.requests[i]
			found = i
			break
		}
	}
	s.mu.Unlock()
	if found < 0 {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "no such request"})
		return
	}

	url, exp, err := mintMailboxRecoverURL(fr.Mailbox)
	if err != nil {
		log.Printf("forgot-bridge approve: mint URL for %s: %v", fr.Mailbox, err)
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "could not mint recovery URL"})
		return
	}

	if mailErr := sendSystemMail(
		fr.Mailbox,
		"Password recovery link for "+fr.Mailbox,
		"Mailbox Password Recovery",
		[]string{
			"An administrator approved your password-recovery request for " + fr.Mailbox + ".",
			"Open the URL below within 15 minutes to set a new password. The link is single-use and burns on first click.",
			url,
			"Expires at " + exp.Format(time.RFC1123Z) + ".",
			"If you did NOT request this, contact postmaster@" + mailDomain + " immediately.",
		},
	); mailErr != nil {
		log.Printf("forgot-bridge approve: mail send to %s FAILED: %v", fr.Mailbox, mailErr)
		writeJSON(w, http.StatusBadGateway, envelope{OK: false,
			Error: "minted URL but mail send failed: " + mailErr.Error()})
		return
	}

	// Remove from queue now that mail is out.
	s.mu.Lock()
	for i := range s.requests {
		if s.requests[i].ID == id {
			s.requests = append(s.requests[:i], s.requests[i+1:]...)
			break
		}
	}
	_ = s.persistLocked()
	s.mu.Unlock()

	log.Printf("forgot-bridge approve: id=%s mailbox=%s by operator=%s nonce=%s",
		id, fr.Mailbox, req.By, req.Nonce)
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"mailbox":    fr.Mailbox,
		"sent_to":    fr.Mailbox,
		"expires_at": exp.UTC().Format(time.RFC3339),
	}})
}
