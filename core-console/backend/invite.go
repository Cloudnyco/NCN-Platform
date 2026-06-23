// Invite-based operator onboarding.
//
// Flow:
//
//   1. Admin clicks "generate invite" in /admin/security.
//      → POST /api/v1/auth/invites
//      → server mints a 24h single-use token, returns
//        { token, url: https://admin.example.com/invite/<token>, expires_at }
//      → admin sends the URL to the prospective operator over a secure
//        channel (Signal, in-person, etc.).
//
//   2. Operator opens the invite URL.
//      → GET /api/v1/auth/invite/preview?token=<...>
//      → public; just validates the token is still good.
//
//   3. Operator picks a username + password, binds passkey OR TOTP, submits.
//      → POST /api/v1/auth/invite/complete { token, username, password,
//                                            passkey?:{...} | totp?:{...} }
//      → server creates the operator record with Approved=false,
//        InvitedBy=<admin>, MFA already bound. Marks token used.
//
//   4. Admin reviews the pending registration in /admin/security and clicks
//      Approve or Reject.
//      → POST /api/v1/auth/operators/approve { username }     → flips Approved=true
//      → DELETE /api/v1/auth/operators?username=<...>          → deletes pending account
//
// Tokens persist to /etc/ncn-core-console/invites.json (mode 0600) so they
// survive ncn-api restarts. Earlier the map was in-memory only — but with
// regular deploy-triggered restarts, that meant any invite link the admin
// already shared would silently die. Persisting solves it; the file is
// root-only and listed in the GC sweep so dead tokens don't accumulate.

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"golang.org/x/crypto/bcrypt"
)

const (
	inviteTTL    = 24 * time.Hour
	inviteMaxLen = 64 // soft cap on outstanding tokens
)

type inviteToken struct {
	Token     string    `json:"token"`
	Role      string    `json:"role"`
	InvitedBy string    `json:"invited_by"`
	CreatedAt time.Time `json:"created_at"`
	Expires   time.Time `json:"expires_at"`
	Used      bool      `json:"used"`
	UsedBy    string    `json:"used_by,omitempty"`
	UsedAt    time.Time `json:"used_at,omitempty"`

	// Invitee delivery — set when the admin issues an invite by email.
	// Tokens predating this field (or hand-minted ones with no email)
	// have empty values; the admin must distribute the URL out-of-band
	// for those. Newer flows always set InviteeEmail.
	InviteeEmail string `json:"invitee_email,omitempty"`
	InviteeName  string `json:"invitee_name,omitempty"`

	// Tracks whether the invite mail has been dispatched, and if not,
	// why. This is what the admin UI surfaces under the row:
	//   "" / "sent"      → green, no action needed
	//   "queued"         → in flight (transient — we don't actually use
	//                      this today; the send is synchronous)
	//   "failed: <err>"  → red; admin should copy the URL manually or retry
	MailStatus string `json:"mail_status,omitempty"`
}

var (
	inviteTokens   = map[string]*inviteToken{}
	inviteTokensMu sync.Mutex
)

// loadInvitesFromDisk hydrates inviteTokens from invitesPath at startup.
// Missing file is fine (fresh install). Corrupt file logs a warning and
// proceeds with an empty map rather than crashing — losing the invite
// list is preferable to refusing to boot.
func loadInvitesFromDisk() {
	// Prefer Postgres when it already holds the document (post-cutover).
	if globalDB != nil {
		if doc, err := loadConfigDoc("invites"); err != nil {
			log.Printf("invite: db load failed (%v) — falling back to file", err)
		} else if doc != nil {
			var arr []*inviteToken
			if err := json.Unmarshal(doc, &arr); err != nil {
				log.Printf("invite: parse db doc: %v (continuing with empty store)", err)
				return
			}
			inviteTokensMu.Lock()
			defer inviteTokensMu.Unlock()
			for _, t := range arr {
				if t == nil || t.Token == "" {
					continue
				}
				inviteTokens[t.Token] = t
			}
			if gcInvitesLocked(time.Now()) {
				persistInvitesLocked()
			}
			log.Printf("invite: loaded %d token(s) from db", len(inviteTokens))
			return
		}
	}

	data, err := os.ReadFile(invitesPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("invite: load %s: %v (continuing with empty store)", invitesPath, err)
		}
		return
	}
	var arr []*inviteToken
	if err := json.Unmarshal(data, &arr); err != nil {
		log.Printf("invite: parse %s: %v (continuing with empty store)", invitesPath, err)
		return
	}
	inviteTokensMu.Lock()
	defer inviteTokensMu.Unlock()
	for _, t := range arr {
		if t == nil || t.Token == "" {
			continue
		}
		inviteTokens[t.Token] = t
	}
	// gc and/or migrate file→DB on first DB boot (persistInvitesLocked dual-writes).
	if gcInvitesLocked(time.Now()) || globalDB != nil {
		persistInvitesLocked()
	}
	log.Printf("invite: loaded %d token(s) from %s", len(inviteTokens), invitesPath)
}

// persistInvitesLocked writes the current map to invitesPath. MUST be called
// with inviteTokensMu held. atomic write via tmp + rename. Best-effort:
// errors are logged but not propagated — losing persistence is bad but not
// fatal; the in-memory copy still serves until the next restart.
func persistInvitesLocked() {
	arr := make([]*inviteToken, 0, len(inviteTokens))
	for _, t := range inviteTokens {
		arr = append(arr, t)
	}
	data, err := json.MarshalIndent(arr, "", "  ")
	if err != nil {
		log.Printf("invite: marshal failed: %v", err)
		return
	}
	tmp := invitesPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("invite: write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, invitesPath); err != nil {
		log.Printf("invite: rename %s -> %s: %v", tmp, invitesPath, err)
		return
	}
	if globalDB != nil {
		if err := saveConfigDoc("invites", data); err != nil {
			log.Printf("invite: db persist failed (%v) — file is current", err)
		}
	}
}

// gcInvitesLocked sweeps expired/used-+-grace tokens. Called under lock.
// Returns true if anything was deleted, so the caller knows whether to
// persist. We intentionally don't persist inside this function to keep
// the lock window short and let the caller batch persistence after other
// changes in the same critical section.
func gcInvitesLocked(now time.Time) bool {
	changed := false
	for k, t := range inviteTokens {
		// Used tokens stick around for 7d so the admin's UI can still show
		// "this invite was used by alice on T". Then they evaporate.
		if t.Used && now.Sub(t.UsedAt) > 7*24*time.Hour {
			delete(inviteTokens, k)
			changed = true
			continue
		}
		// Unused tokens expire at TTL.
		if !t.Used && now.After(t.Expires) {
			delete(inviteTokens, k)
			changed = true
		}
	}
	return changed
}

// POST /api/v1/auth/invites — admin only.
//   body  : { role: "operator" }    (currently only "operator" allowed via invite)
//   reply : { token, url, expires_at, role }
func (s *authStore) handleInvitesCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	caller, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	var req struct {
		Role         string `json:"role"`
		InviteeEmail string `json:"invitee_email"`
		InviteeName  string `json:"invitee_name"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req)
	role := strings.TrimSpace(req.Role)
	if role == "" {
		role = roleOperator
	}
	// Invite-self-register can ONLY produce operator-role accounts. Promoting
	// to admin is a separate, explicit admin action after approval.
	if role != roleOperator {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invite role must be 'operator'"})
		return
	}

	// Invitee email is now required for the "send me a link to fill the
	// form" UX. The admin no longer just gets a URL to copy — they enter
	// who the invite is for and the backend dispatches the email.
	inviteeEmail := strings.TrimSpace(req.InviteeEmail)
	inviteeName := strings.TrimSpace(req.InviteeName)
	if inviteeEmail == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: "invitee_email is required — the invite link is sent there for self-registration"})
		return
	}
	if _, perr := mail.ParseAddress(inviteeEmail); perr != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: "invitee_email is not a valid address: " + perr.Error()})
		return
	}

	b := make([]byte, 24) // 192 bits, plenty of entropy
	if _, err := rand.Read(b); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "rand failed"})
		return
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	now := time.Now()

	inviteTokensMu.Lock()
	gcInvitesLocked(now)
	if len(inviteTokens) >= inviteMaxLen {
		inviteTokensMu.Unlock()
		writeJSON(w, http.StatusTooManyRequests, envelope{OK: false,
			Error: "too many outstanding invites — revoke stale ones first"})
		return
	}
	tk := &inviteToken{
		Token:        token,
		Role:         role,
		InvitedBy:    caller.Sub,
		CreatedAt:    now,
		Expires:      now.Add(inviteTTL),
		InviteeEmail: inviteeEmail,
		InviteeName:  inviteeName,
	}
	inviteTokens[token] = tk
	persistInvitesLocked()
	inviteTokensMu.Unlock()

	log.Printf("auth: invite ISSUED token=%s... role=%s by=%s peer=%s ttl=%.0fh invitee=%s",
		token[:8], role, caller.Sub, clientAddr(r), inviteTTL.Hours(), inviteeEmail)
	auditRecord(r, AuditEvent{
		Event: "invite.issue", Severity: auditSevWarn, Actor: caller.Sub,
		Target: inviteeEmail,
		Details: map[string]any{
			"role":          role,
			"ttl_hours":     inviteTTL.Hours(),
			"token_prefix":  token[:8],
			"invitee_name":  inviteeName,
		},
	})

	// Construct the absolute URL from the request — works whether reached
	// via admin.example.com or a fallback. Frontend resolves `/invite/<t>`
	// to its public SPA route.
	scheme := "https"
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		scheme = fwd
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	url := fmt.Sprintf("%s://%s/invite/%s", scheme, host, token)

	// Dispatch the invite mail. Failure here is non-fatal — the token is
	// already persisted, the admin sees the URL in the response and a
	// mail_status of "failed: ..." in the row so they can copy manually
	// or hit a (future) "resend" button. Most common cause of failure:
	// operator-mail-bridge.key not installed yet on tyo.
	mailStatus := s.sendInviteMail(tk, url, caller.Sub)

	// Persist updated MailStatus so the list view shows it. Re-lock briefly.
	inviteTokensMu.Lock()
	if cur, ok := inviteTokens[token]; ok {
		cur.MailStatus = mailStatus
		persistInvitesLocked()
	}
	inviteTokensMu.Unlock()

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"token":        token,
		"role":         role,
		"url":          url,
		"expires_at":   tk.Expires.UTC().Format(time.RFC3339),
		"expires_in":   int(inviteTTL.Seconds()),
		"invitee_email": inviteeEmail,
		"invitee_name":  inviteeName,
		"mail_status":   mailStatus,
	}})
}

// GET /api/v1/auth/invites — admin only. Lists outstanding + recently-used.
func (s *authStore) handleInvitesList(w http.ResponseWriter, r *http.Request) {
	inviteTokensMu.Lock()
	if gcInvitesLocked(time.Now()) {
		persistInvitesLocked()
	}
	out := make([]map[string]any, 0, len(inviteTokens))
	for _, t := range inviteTokens {
		out = append(out, map[string]any{
			"token":         t.Token[:12] + "…", // short prefix; full token never returned after issuance
			"role":          t.Role,
			"invited_by":    t.InvitedBy,
			"created_at":    t.CreatedAt.UTC().Format(time.RFC3339),
			"expires_at":    t.Expires.UTC().Format(time.RFC3339),
			"used":          t.Used,
			"used_by":       t.UsedBy,
			"used_at":       timeOrEmpty(t.UsedAt),
			"invitee_email": t.InviteeEmail,
			"invitee_name":  t.InviteeName,
			"mail_status":   t.MailStatus,
		})
	}
	inviteTokensMu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i]["created_at"].(string) > out[j]["created_at"].(string)
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
}

func timeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// DELETE /api/v1/auth/invites?token=<full or short prefix>
func (s *authStore) handleInvitesRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "DELETE only"})
		return
	}
	caller, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	q := strings.TrimSpace(r.URL.Query().Get("token"))
	if q == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing ?token="})
		return
	}
	q = strings.TrimSuffix(q, "…")

	inviteTokensMu.Lock()
	defer inviteTokensMu.Unlock()
	for k := range inviteTokens {
		if k == q || strings.HasPrefix(k, q) {
			delete(inviteTokens, k)
			persistInvitesLocked()
			log.Printf("auth: invite REVOKED token=%s... by=%s peer=%s",
				k[:8], caller.Sub, clientAddr(r))
			auditRecord(r, AuditEvent{
				Event: "invite.revoke", Severity: auditSevWarn, Actor: caller.Sub, Target: k[:8] + "...",
			})
			writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]string{"revoked": k[:8] + "…"}})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "token not found"})
}

// GET /api/v1/auth/invite/preview?token=<...>   (public)
//   Returns just enough info to render the invite landing page:
//   the role being granted and how much time is left.
func (s *authStore) handleInvitePreview(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing token"})
		return
	}
	inviteTokensMu.Lock()
	defer inviteTokensMu.Unlock()
	if gcInvitesLocked(time.Now()) {
		persistInvitesLocked()
	}
	t, ok := inviteTokens[token]
	if !ok {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "invite link invalid, expired, or already used"})
		return
	}
	if t.Used {
		writeJSON(w, http.StatusGone, envelope{OK: false, Error: "invite already used"})
		return
	}
	if time.Now().After(t.Expires) {
		writeJSON(w, http.StatusGone, envelope{OK: false, Error: "invite expired"})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"role":       t.Role,
		"invited_by": t.InvitedBy,
		"expires_at": t.Expires.UTC().Format(time.RFC3339),
		"expires_in": int(time.Until(t.Expires).Seconds()),
	}})
}

// POST /api/v1/auth/invite/complete   (public)
//   { token, username, password, totp?:{secret,code} | passkey?:{...response...} }
//
// Creates an operator record with Approved=false. The caller (an admin)
// must then click "approve" in /admin/security to activate it.
func (s *authStore) handleInviteComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var req struct {
		Token    string `json:"token"`
		Username string `json:"username"`
		Password string `json:"password"`
		TOTP     *struct {
			Secret string `json:"secret"`
			Code   string `json:"code"`
		} `json:"totp,omitempty"`
		Passkey *struct {
			Name        string          `json:"name"`
			ChallengeID string          `json:"challenge_id"`
			Response    json.RawMessage `json:"response"`
		} `json:"passkey,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	req.Username = strings.TrimSpace(req.Username)

	// Validate token (under lock) but don't mark used yet — we mark only
	// after a successful record write so failures don't burn the invite.
	inviteTokensMu.Lock()
	tkn, ok := inviteTokens[req.Token]
	if !ok || tkn.Used || time.Now().After(tkn.Expires) {
		inviteTokensMu.Unlock()
		writeJSON(w, http.StatusGone, envelope{OK: false, Error: "invite link invalid, expired, or already used"})
		return
	}
	inviteTokensMu.Unlock()

	if !isValidUsername(req.Username) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: "username must be 2-32 chars, [A-Za-z0-9._-]"})
		return
	}
	if len(req.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "password must be ≥ 8 chars"})
		return
	}
	if req.TOTP == nil && req.Passkey == nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: "must bind a passkey OR set up TOTP before completing registration"})
		return
	}

	// Username uniqueness check (under auth lock).
	s.mu.Lock()
	if _, exists := s.operators[req.Username]; exists {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, envelope{OK: false, Error: "username already taken"})
		return
	}
	s.mu.Unlock()

	// Hash password.
	pwHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "hash failed"})
		return
	}

	// Verify the chosen MFA factor.
	var (
		totpSecret string
		passkeys   []passkeyCredential
	)
	if req.TOTP != nil {
		secret := strings.TrimSpace(req.TOTP.Secret)
		code := strings.TrimSpace(req.TOTP.Code)
		if secret == "" || code == "" {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "totp secret + code required"})
			return
		}
		if !verifyTOTP(secret, code, time.Now()) {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false,
				Error: "TOTP code doesn't match — check your authenticator's clock"})
			return
		}
		totpSecret = secret
	}
	if req.Passkey != nil {
		// We need to register the passkey. The challenge was issued via
		// /api/v1/auth/passkey/register/begin during the invite flow.
		if s.wa == nil {
			writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "webauthn not configured"})
			return
		}
		p := s.wa.take(req.Passkey.ChallengeID)
		if p == nil || p.Kind != "register" || p.User != req.Username {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false,
				Error: "passkey challenge invalid (re-open the page and retry)"})
			return
		}
		parsed, perr := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(req.Passkey.Response))
		if perr != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "passkey parse: " + perr.Error()})
			return
		}
		// Synthetic user — we don't yet have an operatorRecord but we know
		// the chosen username.
		tempUser := &inviteRegistrant{username: req.Username}
		cred, verr := s.wa.wa.CreateCredential(tempUser, *p.Session, parsed)
		if verr != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "passkey verify failed: " + verr.Error()})
			return
		}
		transports := make([]string, 0, len(cred.Transport))
		for _, t := range cred.Transport {
			transports = append(transports, string(t))
		}
		passkeys = []passkeyCredential{{
			ID:        cred.ID,
			PublicKey: cred.PublicKey,
			SignCount: cred.Authenticator.SignCount,
			AAGUID:    cred.Authenticator.AAGUID,
			Transport: transports,
			Flags:     cred.Flags,
			Name:      strings.TrimSpace(req.Passkey.Name),
			CreatedAt: time.Now(),
		}}
		if passkeys[0].Name == "" {
			passkeys[0].Name = "passkey · invite-bound"
		}
	}

	// Generate recovery codes for the new operator.
	plainCodes, hashedCodes, codeErr := generateRecoveryCodes(10)
	if codeErr != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "recovery codes: " + codeErr.Error()})
		return
	}

	// Persist.
	now := time.Now()
	createdAt := now.UTC().Format(time.RFC3339)
	s.mu.Lock()
	// Re-check uniqueness in case of race.
	if _, exists := s.operators[req.Username]; exists {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, envelope{OK: false, Error: "username already taken"})
		return
	}
	s.operators[req.Username] = operatorRecord{
		Username:      req.Username,
		PasswordHash:  string(pwHash),
		Role:          tkn.Role,
		CreatedAt:     createdAt,
		RecoveryCodes: hashedCodes,
		TOTPSecret:    totpSecret,
		Passkeys:      passkeys,
		Approved:      false,
		InvitedBy:     tkn.InvitedBy,
		InvitedAt:     createdAt,
	}
	if err := s.persistLocked(); err != nil {
		delete(s.operators, req.Username)
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "persist: " + err.Error()})
		return
	}
	s.mu.Unlock()

	// Burn the invite token.
	inviteTokensMu.Lock()
	if t, ok := inviteTokens[req.Token]; ok {
		t.Used = true
		t.UsedBy = req.Username
		t.UsedAt = now
	}
	persistInvitesLocked()
	inviteTokensMu.Unlock()

	log.Printf("auth: invite COMPLETED user=%q role=%s invited_by=%s peer=%s mfa=%s · awaiting admin approval",
		req.Username, tkn.Role, tkn.InvitedBy, clientAddr(r),
		mfaSummary(totpSecret != "", len(passkeys) > 0))
	auditRecord(r, AuditEvent{
		Event: "invite.accept", Severity: auditSevCritical, Actor: req.Username, Target: req.Username,
		Details: map[string]any{
			"role":       tkn.Role,
			"invited_by": tkn.InvitedBy,
			"has_totp":   totpSecret != "",
			"passkeys":   len(passkeys),
		},
	})

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"username":        req.Username,
		"role":            tkn.Role,
		"approved":        false,
		"invited_by":      tkn.InvitedBy,
		"recovery_codes":  plainCodes,
		"status":          "pending admin approval",
	}})
}

// POST /api/v1/auth/operators/approve   (admin)
//   body: { username }
func (s *authStore) handleOperatorsApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	caller, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	var req struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing username"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	op, exists := s.operators[req.Username]
	if !exists {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "unknown operator"})
		return
	}
	if op.Approved {
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
			"operator":  req.Username,
			"already":   true,
			"approved":  true,
		}})
		return
	}
	op.Approved = true
	op.ApprovedAt = time.Now().UTC().Format(time.RFC3339)
	s.operators[req.Username] = op
	if err := s.persistLocked(); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "persist: " + err.Error()})
		return
	}
	log.Printf("auth: operator APPROVED user=%q by=%s peer=%s",
		req.Username, caller.Sub, clientAddr(r))
	auditRecord(r, AuditEvent{
		Event: "operator.approve", Severity: auditSevCritical, Actor: caller.Sub, Target: req.Username,
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"operator": req.Username,
		"approved": true,
	}})
}

// POST /api/v1/auth/invite/passkey/begin   (public)
//   body  : { token, username }
//   reply : { challenge_id, options }
//
// Public counterpart of /api/v1/auth/passkey/register/begin. The auth-gated
// version assumes the operator record already exists; on the invite flow it
// doesn't. We validate the invite + username, then mint a creation challenge
// against a synthetic `inviteRegistrant` user. The username supplied here
// MUST match the one later sent to /invite/complete — that linkage is what
// /complete's `p.User != req.Username` check enforces, so we can't be tricked
// into binding a passkey for one username and creating the record under
// another.
func (s *authStore) handleInvitePasskeyBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	if s.wa == nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "webauthn not configured"})
		return
	}
	var req struct {
		Token    string `json:"token"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	req.Username = strings.TrimSpace(req.Username)
	if !isValidUsername(req.Username) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: "username must be 2-32 chars, [A-Za-z0-9._-]"})
		return
	}

	// Verify the invite is still good. Doing it here instead of only at
	// /complete means we don't waste a passkey-create round trip on a dead
	// invite — and we get an audit log line if someone is replaying.
	inviteTokensMu.Lock()
	tkn, ok := inviteTokens[req.Token]
	if !ok || tkn.Used || time.Now().After(tkn.Expires) {
		inviteTokensMu.Unlock()
		writeJSON(w, http.StatusGone, envelope{OK: false,
			Error: "invite link invalid, expired, or already used"})
		return
	}
	inviteTokensMu.Unlock()

	// If the username is already taken, fail early — otherwise the user
	// would burn through a passkey creation only to be told no.
	s.mu.RLock()
	_, taken := s.operators[req.Username]
	s.mu.RUnlock()
	if taken {
		writeJSON(w, http.StatusConflict, envelope{OK: false, Error: "username already taken"})
		return
	}

	// ResidentKey=required so the credential is discoverable — same as the
	// auth-gated /passkey/register/begin path. Without this the resulting
	// credential can't be used for "Sign in with passkey" later.
	options, sess, err := s.wa.wa.BeginRegistration(
		&inviteRegistrant{username: req.Username},
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			UserVerification:   protocol.VerificationPreferred,
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
		}),
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	challengeID := s.wa.put("register", req.Username, sess)
	log.Printf("auth: invite PASSKEY-BEGIN token=%s... user=%q peer=%s",
		req.Token[:8], req.Username, clientAddr(r))
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"challenge_id": challengeID,
		"options":      options.Response,
	}})
}

// inviteRegistrant is a webauthn.User stand-in used during passkey
// registration BEFORE the operator record exists. The username is the only
// identifier we have at this point.
type inviteRegistrant struct {
	username string
}

func (u *inviteRegistrant) WebAuthnID() []byte                         { return []byte(u.username) }
func (u *inviteRegistrant) WebAuthnName() string                       { return u.username }
func (u *inviteRegistrant) WebAuthnDisplayName() string                { return u.username }
func (u *inviteRegistrant) WebAuthnCredentials() []webauthn.Credential { return nil }

func mfaSummary(totp, passkey bool) string {
	switch {
	case totp && passkey:
		return "totp+passkey"
	case totp:
		return "totp"
	case passkey:
		return "passkey"
	default:
		return "none"
	}
}

// sendInviteMail dispatches the invitation email through the webmail
// send-bridge. Returns a short status string that gets stored on the
// inviteToken: "sent" on success, "failed: <reason>" otherwise. Never
// errors out the caller — the token is already persisted; if mail
// fails the admin sees the URL in the response and can copy it manually.
func (s *authStore) sendInviteMail(tk *inviteToken, url, inviter string) string {
	if s.mailBridge == nil || len(s.mailBridge.key) == 0 {
		return "failed: " + errBridgeKeyMissing.Error()
	}

	displayName := tk.InviteeName
	if displayName == "" {
		displayName = tk.InviteeEmail
	}
	ttlH := int(time.Until(tk.Expires).Hours())
	if ttlH < 1 {
		ttlH = 1
	}

	subj := "You're invited to join Acme Net"
	headline := "Welcome to NCN"
	paragraphs := []string{
		fmt.Sprintf("Hi %s,", displayName),
		fmt.Sprintf("%s has invited you to register as an operator on the Acme Net admin console. Open the link below and fill in your username, password, and a second factor (Passkey or TOTP).", inviter),
		"Your registration link:\n\n    " + url,
		fmt.Sprintf("The link expires in %d hours. After you submit, an administrator will review and approve your account — you'll be able to sign in once approval lands.", ttlH),
		"If you weren't expecting this email, you can safely ignore it — the link is single-use and time-limited.",
	}

	if err := s.mailBridge.SendSystemMail(tk.InviteeEmail, subj, headline, paragraphs, "invite"); err != nil {
		log.Printf("invite: mail send FAIL to=%s: %v", tk.InviteeEmail, err)
		return "failed: " + truncate(err.Error(), 120)
	}
	log.Printf("invite: mail sent to=%s token=%s...", tk.InviteeEmail, tk.Token[:8])
	return "sent"
}

// resendInviteMail re-dispatches an invite mail for an existing token.
// Useful when the first attempt failed (e.g. bridge key was missing) or
// the invitee deleted the original email. POST /api/v1/auth/invites/<token>/resend.
func (s *authStore) handleInviteResend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	caller, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)

	// Token comes from the URL path after /invites/.
	path := r.URL.Path
	const prefix = "/api/v1/auth/invites/"
	if !strings.HasPrefix(path, prefix) {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "unknown route"})
		return
	}
	rest := strings.TrimPrefix(path, prefix)
	rest = strings.TrimSuffix(rest, "/resend")
	rest = strings.TrimSuffix(rest, "/")
	rest = strings.TrimSuffix(rest, "…") // tolerate the short-prefix render the UI shows
	if rest == "" || strings.Contains(rest, "/") {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing token"})
		return
	}

	// Look up by full match OR prefix — admin UI only knows the prefix,
	// not the full token (full one was only shown once at issuance).
	inviteTokensMu.Lock()
	var tk *inviteToken
	var fullKey string
	for k, v := range inviteTokens {
		if k == rest || strings.HasPrefix(k, rest) {
			tk = v
			fullKey = k
			break
		}
	}
	if tk == nil {
		inviteTokensMu.Unlock()
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "invite not found"})
		return
	}
	if tk.Used {
		inviteTokensMu.Unlock()
		writeJSON(w, http.StatusConflict, envelope{OK: false, Error: "invite already used"})
		return
	}
	if tk.InviteeEmail == "" {
		inviteTokensMu.Unlock()
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: "this invite has no associated email (predates invite-by-email) — share the URL manually"})
		return
	}
	// Snapshot the data we need while we hold the lock; release before
	// the (potentially slow) outbound HTTP call.
	tkSnap := *tk
	inviteTokensMu.Unlock()

	scheme := "https"
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		scheme = fwd
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	url := fmt.Sprintf("%s://%s/invite/%s", scheme, host, tkSnap.Token)

	status := s.sendInviteMail(&tkSnap, url, caller.Sub)

	// Persist updated status under the full key (not the prefix we matched on).
	inviteTokensMu.Lock()
	if cur, ok := inviteTokens[fullKey]; ok {
		cur.MailStatus = status
		persistInvitesLocked()
	}
	inviteTokensMu.Unlock()

	auditRecord(r, AuditEvent{
		Event: "invite.resend", Severity: auditSevInfo, Actor: caller.Sub,
		Target:  tkSnap.InviteeEmail,
		Details: map[string]any{"token_prefix": tkSnap.Token[:8], "result": status},
	})

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"token":       tkSnap.Token,
		"mail_status": status,
	}})
}

