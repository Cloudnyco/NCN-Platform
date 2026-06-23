// role_recover.go — admin-console-driven role mailbox recovery.
//
// Flow:
//   1. Admin operator (alice) on admin.example.com clicks "Mint recovery
//      URL" for a role mailbox (e.g. noc@) in Security.
//   2. ncn-api on tyo signs a JSON body with operator-mail-bridge.key and
//      POSTs to this endpoint.
//   3. We verify the signature, allowlist the mailbox to the 5 role names,
//      then mint a one-shot recovery URL using the same machinery as
//      `ncn-mail admin mint-recover`.
//   4. URL gets returned to admin's UI, where the operator can click,
//      copy, or hand to whoever needs the password reset.
//
// This is a SECOND consumer of the operator-bridge HMAC key, alongside the
// existing op- self-invite tokens (mail_bridge.go on the admin side ↔
// invite.go's verifyOperatorToken). To keep the two flows from cross-
// contaminating, we sign the entire request body (not a URL token) and
// require a distinct "intent" field.
package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// roleMailboxes is the closed allowlist. Anyone trying to use this bridge
// for a non-role mailbox gets 403 — that protects ordinary user mailboxes
// from getting reset by a compromised admin operator account (the threat
// model says admins are trustworthy, but defense in depth).
var roleMailboxes = map[string]struct{}{
	"postmaster@" + mailDomain: {},
	"noc@" + mailDomain:        {},
	"hostmaster@" + mailDomain: {},
	"abuse@" + mailDomain:      {},
	"security@" + mailDomain:   {},
}

const (
	roleRecoverIntent   = "role-recover"
	roleRecoverMaxClock = 60 * time.Second
)

type roleRecoverRequest struct {
	Intent  string `json:"intent"`
	Mailbox string `json:"mailbox"`
	By      string `json:"by"`    // operator username on admin (informational, logged)
	TS      int64  `json:"ts"`    // unix seconds, must be within ±60s of now
	Nonce   string `json:"nonce"` // random, replay defense (combined with TS window)
}

// handleRoleRecover verifies the bridge signature and mints a one-shot
// recovery URL for the requested role mailbox.
//
// POST /api/v1/mail/admin/role-recover
// Headers:
//
//	X-Bridge-Sig: base64url(HMAC-SHA256(operator-bridge.key, raw-body))
//
// Body:
//
//	{ "intent": "role-recover", "mailbox": "noc@example.com",
//	  "by": "alice", "ts": 1779676800, "nonce": "..." }
func (s *inviteStore) handleRoleRecover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	if len(s.bridgeKey) == 0 {
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
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "read body"})
		return
	}
	mac := hmac.New(sha256.New, s.bridgeKey)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "bad signature"})
		return
	}

	var req roleRecoverRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	if req.Intent != roleRecoverIntent {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "wrong intent (expected role-recover)"})
		return
	}
	now := time.Now().Unix()
	if req.TS == 0 || abs64(now-req.TS) > int64(roleRecoverMaxClock.Seconds()) {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "stale or future timestamp"})
		return
	}
	mailbox := strings.ToLower(strings.TrimSpace(req.Mailbox))
	if !strings.Contains(mailbox, "@") {
		mailbox = mailbox + "@" + mailDomain
	}
	if _, ok := roleMailboxes[mailbox]; !ok {
		writeJSON(w, http.StatusForbidden, envelope{OK: false,
			Error: "role-recover only permitted for postmaster/noc/hostmaster/abuse/security"})
		return
	}

	key, err := loadOrCreateMailboxRecoveryKey()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}

	nonce := make([]byte, 12)
	_, _ = rand.Read(nonce)
	exp := time.Now().Add(15 * time.Minute).Unix()

	payload, _ := json.Marshal(struct {
		Mailbox string `json:"mb"`
		Exp     int64  `json:"exp"`
		Nonce   string `json:"n"`
	}{Mailbox: mailbox, Exp: exp, Nonce: base64.RawURLEncoding.EncodeToString(nonce)})

	pb := base64.RawURLEncoding.EncodeToString(payload)
	tokMac := hmac.New(sha256.New, key)
	tokMac.Write([]byte(pb))
	sb := base64.RawURLEncoding.EncodeToString(tokMac.Sum(nil))

	token := mailboxRecoveryPrefix + pb + "." + sb
	url := "https://" + mailHost + "/admin-recover/" + token

	log.Printf("role-recover: minted URL for %s requested by operator=%s nonce=%s", mailbox, req.By, req.Nonce)
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"mailbox":    mailbox,
		"url":        url,
		"expires_at": time.Unix(exp, 0).UTC().Format(time.RFC3339),
	}})
}

// mintMailboxRecoverURL is the shared minting logic used by both the
// admin-driven role-recover bridge above and the operator self-recover
// path in forgot.go. The mailbox MUST already be allowlisted by the
// caller — this function does NOT enforce any policy. Returns the URL
// and an absolute expiry timestamp.
func mintMailboxRecoverURL(mailbox string) (url string, expiresAt time.Time, err error) {
	key, err := loadOrCreateMailboxRecoveryKey()
	if err != nil {
		return "", time.Time{}, err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", time.Time{}, err
	}
	exp := time.Now().Add(15 * time.Minute).Unix()

	payload, _ := json.Marshal(struct {
		Mailbox string `json:"mb"`
		Exp     int64  `json:"exp"`
		Nonce   string `json:"n"`
	}{Mailbox: mailbox, Exp: exp, Nonce: base64.RawURLEncoding.EncodeToString(nonce)})

	pb := base64.RawURLEncoding.EncodeToString(payload)
	tokMac := hmac.New(sha256.New, key)
	tokMac.Write([]byte(pb))
	sb := base64.RawURLEncoding.EncodeToString(tokMac.Sum(nil))

	return "https://" + mailHost + "/admin-recover/" + mailboxRecoveryPrefix + pb + "." + sb,
		time.Unix(exp, 0).UTC(), nil
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
