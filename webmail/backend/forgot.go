// forgot.go — anonymous "I lost my password" request handler.
//
// Two paths depending on whether the requester is a *role* mailbox (the
// NCN operator team's shared role accounts: postmaster/noc/hostmaster/
// abuse/security):
//
//   - Operator (role mailbox) → SELF-RECOVERY. Backend mints a one-shot
//     /admin-recover URL using the same HMAC machinery as the admin-driven
//     role-recover bridge in role_recover.go and emails it directly to
//     the requester. No admin involvement; operator clicks → resets.
//     The assumption is operators can read mail for these mailboxes from
//     another session/device even when "forgot" the web UI password.
//
//   - Non-operator → QUEUE. Same as before: persists to a JSON queue and
//     admins see pending requests in the invites panel where they can
//     one-click reset. ALSO sends a heads-up email to postmaster@ so the
//     admin team gets notified out-of-band (dual track: queue + email).
//
// Threat model: anonymous endpoint, so we rate-limit per mailbox (3/day)
// and per IP (10/day) to avoid being used as a notification spam vector
// against the admin's UI. The request stores the IP + UA for the admin's
// situational awareness — bots get flagged on the dashboard.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	forgotPath       = stateDir + "/forgot-requests.json"
	forgotRequestTTL = 7 * 24 * time.Hour
	forgotMaxPerMbox = 3  // per 24h
	forgotMaxPerIP   = 10 // per 24h
)

type forgotRequest struct {
	ID          string    `json:"id"` // 12-hex
	Mailbox     string    `json:"mailbox"`
	RequestedAt time.Time `json:"requested_at"`
	IP          string    `json:"ip"`
	UserAgent   string    `json:"ua,omitempty"`
	// SelfRecover=true means the operator-mailbox self-recovery URL was
	// auto-dispatched; admins don't need to act. Kept in the file for
	// rate-limit accounting; filtered out of the admin list views.
	SelfRecover bool `json:"self_recover,omitempty"`
}

type forgotFile struct {
	Version  int             `json:"version"`
	Requests []forgotRequest `json:"requests"`
}

type forgotStore struct {
	mu       sync.RWMutex
	requests []forgotRequest
	admins   *inviteStore // re-use admin list
}

func newForgotStore(admins *inviteStore) (*forgotStore, error) {
	s := &forgotStore{admins: admins}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *forgotStore) load() error {
	data, err := os.ReadFile(forgotPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var f forgotFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse %s: %w", forgotPath, err)
	}
	s.requests = f.Requests
	return nil
}

func (s *forgotStore) persistLocked() error {
	f := forgotFile{Version: 1, Requests: s.requests}
	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := forgotPath + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, forgotPath)
}

func (s *forgotStore) gcLocked(now time.Time) {
	live := s.requests[:0]
	for _, r := range s.requests {
		if now.Sub(r.RequestedAt) < forgotRequestTTL {
			live = append(live, r)
		}
	}
	s.requests = live
}

func clientIP(r *http.Request) string {
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		// take left-most
		if i := strings.Index(xf, ","); i > 0 {
			return strings.TrimSpace(xf[:i])
		}
		return strings.TrimSpace(xf)
	}
	if ri := r.Header.Get("X-Real-IP"); ri != "" {
		return ri
	}
	return r.RemoteAddr
}

// POST /api/v1/mail/forgot/request
//
//	{ "mailbox": "alice@example.com" }
//
// Anonymous. We DON'T leak whether the mailbox exists (always 200 on valid
// input) to avoid mailbox enumeration.
func (s *forgotStore) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var req struct {
		Mailbox string `json:"mailbox"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	req.Mailbox = strings.ToLower(strings.TrimSpace(req.Mailbox))
	if req.Mailbox == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "mailbox required"})
		return
	}
	if _, err := mail.ParseAddress(req.Mailbox); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid email"})
		return
	}

	now := time.Now().UTC()
	ip := clientIP(r)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked(now)

	// Rate limits (best-effort, in-memory + persisted). Apply BEFORE the
	// operator-branch so role-mailbox spam can't bypass them.
	mboxIn24h, ipIn24h := 0, 0
	for _, x := range s.requests {
		if now.Sub(x.RequestedAt) > 24*time.Hour {
			continue
		}
		if x.Mailbox == req.Mailbox {
			mboxIn24h++
		}
		if x.IP == ip {
			ipIn24h++
		}
	}
	if mboxIn24h >= forgotMaxPerMbox {
		// Pretend success to avoid leaking that the mailbox exists.
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"queued": true}})
		log.Printf("forgot: rate-limit hit (mbox=%s, ip=%s, count=%d)", req.Mailbox, ip, mboxIn24h)
		return
	}
	if ipIn24h >= forgotMaxPerIP {
		writeJSON(w, http.StatusTooManyRequests, envelope{OK: false, Error: "too many requests from your address; try again tomorrow"})
		return
	}

	// ---- Branch A: OPERATOR (role mailbox) self-recovery ----------------
	// Same allowlist as the admin-driven role-recover bridge. Self-mint
	// the URL + email it; do NOT enqueue (no admin action needed) and
	// still also record an "operator self-recovery dispatched" event in
	// the per-IP rate counter so a bot can't abuse this branch.
	if _, ok := roleMailboxes[req.Mailbox]; ok {
		url, exp, err := mintMailboxRecoverURL(req.Mailbox)
		if err != nil {
			log.Printf("forgot: mint self-recover for %s: %v", req.Mailbox, err)
			writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "could not mint recovery URL"})
			return
		}
		// Persist a "consumed" marker only for rate-limit purposes; the
		// admin queue handlers filter these out so they don't clutter
		// the UI (admins didn't need to act).
		id := make([]byte, 6)
		_, _ = rand.Read(id)
		s.requests = append(s.requests, forgotRequest{
			ID:          hex.EncodeToString(id),
			Mailbox:     req.Mailbox,
			RequestedAt: now,
			IP:          ip,
			UserAgent:   strings.TrimSpace(r.Header.Get("User-Agent")),
			SelfRecover: true,
		})
		_ = s.persistLocked()

		if err := sendSystemMail(
			req.Mailbox,
			"Self-recovery URL for "+req.Mailbox,
			"Mailbox Self-Recovery",
			[]string{
				"You requested a password reset for the operator mailbox " + req.Mailbox + ".",
				"Open the URL below within 15 minutes to set a new password. The link is single-use; opening it consumes the token whether or not you complete the reset.",
				url,
				"Expires at " + exp.Format(time.RFC1123Z) + ".",
				"If you did NOT request this, ignore this message and rotate your mailbox password as soon as you can sign in again — the link expires on its own, but a stray request usually means a bot is poking at the operator role accounts.",
			},
		); err != nil {
			log.Printf("forgot: self-recover email to %s failed: %v", req.Mailbox, err)
			writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "could not deliver recovery email"})
			return
		}
		log.Printf("forgot: self-recover URL dispatched to operator mailbox %s from %s", req.Mailbox, ip)
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"queued": true, "self_recover": true}})
		return
	}

	// ---- Branch B: regular user — enqueue + admin-notification ---------
	id := make([]byte, 6)
	_, _ = rand.Read(id)
	fr := forgotRequest{
		ID:          hex.EncodeToString(id),
		Mailbox:     req.Mailbox,
		RequestedAt: now,
		IP:          ip,
		UserAgent:   strings.TrimSpace(r.Header.Get("User-Agent")),
	}
	s.requests = append(s.requests, fr)
	if err := s.persistLocked(); err != nil {
		log.Printf("forgot: persist %s: %v", forgotPath, err)
	}
	log.Printf("forgot: queued request for %s from %s", req.Mailbox, ip)

	// Ack to the requester. Doesn't leak whether the mailbox exists — we
	// always send this if the address is well-formed.
	if err := sendSystemMail(
		req.Mailbox,
		"We received your password recovery request",
		"Password Recovery Requested",
		[]string{
			"We received a password recovery request for " + req.Mailbox + ".",
			"What happens next: an operator will contact you out-of-band (usually within a day) to verify your identity and walk you through the reset. We deliberately do NOT send reset links by email to avoid phishing patterns.",
			"If you didn't make this request, ignore this message — nothing will change without an operator's manual action.",
		},
	); err != nil {
		log.Printf("forgot: ack notify failed (continuing): %v", err)
	}

	// Heads-up to the admin team via the postmaster@ role mailbox so they
	// see it without having to refresh the admin UI. Includes the same
	// situational-awareness fields the UI shows: IP + UA + timestamp.
	if err := sendSystemMail(
		"postmaster@"+mailDomain,
		"[forgot-password] new request: "+req.Mailbox,
		"Forgot-password request",
		[]string{
			"A non-operator mailbox just filed a password-recovery request:",
			"    mailbox:   " + req.Mailbox + "\n" +
				"    request:   " + fr.ID + "\n" +
				"    submitted: " + now.Format(time.RFC1123Z) + "\n" +
				"    ip:        " + ip + "\n" +
				"    user-agent: " + fr.UserAgent,
			"Open the webmail admin panel, or the NCN admin console (Security → Forgot Requests), and dismiss / reset / ignore as appropriate.",
		},
	); err != nil {
		log.Printf("forgot: admin notify failed (continuing): %v", err)
	}

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"queued": true}})
}

// GET /api/v1/mail/forgot/requests — admin only
func (s *forgotStore) handleList(w http.ResponseWriter, r *http.Request) {
	c, ok := mailClaimsFromRequest(r)
	if !ok || !s.admins.isAdmin(c.Mailbox) {
		writeJSON(w, http.StatusForbidden, envelope{OK: false, Error: "admin only"})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: s.snapshotForAdmins()})
}

// snapshotForAdmins returns the actionable subset of the queue — i.e.
// non-self-recover entries that an admin still needs to consider.
// Used by both handleList (webmail-side panel) and the bridge handler
// (admin-console panel on tyo).
func (s *forgotStore) snapshotForAdmins() []forgotRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked(time.Now().UTC())
	out := make([]forgotRequest, 0, len(s.requests))
	for _, r := range s.requests {
		if r.SelfRecover {
			continue
		}
		out = append(out, r)
	}
	return out
}

// handleByID dispatches on /api/v1/mail/forgot/requests/<id>[/approve] based
// on method + suffix:
//
//	DELETE …/requests/<id>            → dismiss
//	POST   …/requests/<id>/approve    → mint recovery URL + email + remove
//
// Both are admin-only; the gate is identical between the two so we check
// it once at the top instead of duplicating in each branch.
func (s *forgotStore) handleByID(w http.ResponseWriter, r *http.Request) {
	c, ok := mailClaimsFromRequest(r)
	if !ok || !s.admins.isAdmin(c.Mailbox) {
		writeJSON(w, http.StatusForbidden, envelope{OK: false, Error: "admin only"})
		return
	}
	tail := strings.TrimPrefix(r.URL.Path, "/api/v1/mail/forgot/requests/")
	tail = strings.TrimSuffix(tail, "/")
	if tail == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "id required"})
		return
	}

	// approve sub-route: id/approve
	if strings.HasSuffix(tail, "/approve") && r.Method == http.MethodPost {
		id := strings.TrimSuffix(tail, "/approve")
		s.approveByID(w, r, id, c.Mailbox)
		return
	}

	// otherwise treat as the dismiss endpoint
	if r.Method == http.MethodDelete || r.Method == http.MethodPost {
		s.dismissByID(w, tail)
		return
	}
	writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "DELETE for dismiss, POST .../approve to approve"})
}

func (s *forgotStore) dismissByID(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, req := range s.requests {
		if req.ID == id {
			s.requests = append(s.requests[:i], s.requests[i+1:]...)
			if err := s.persistLocked(); err != nil {
				writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, envelope{OK: true})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "no such request"})
}

// approveByID: closes the loop the original forgot-flow left open.
// Regular users used to only get an ack email saying "we'll contact you" —
// this turns the admin's "approve" click into the same self-service URL
// that role mailboxes get on the auto-recovery branch.
//
// Steps:
//  1. Find the queued request (404 if gone — possibly already approved
//     from another tab).
//  2. Mint a single-use mailbox-recover URL (same helper role-recover uses).
//  3. Email it to the requesting mailbox via sendSystemMail.
//  4. Remove from the queue (job done; if email fails we still keep the
//     URL minted but log loudly — at that point the admin can adopt+manual
//     reset as before).
func (s *forgotStore) approveByID(w http.ResponseWriter, r *http.Request, id, adminMailbox string) {
	s.mu.Lock()
	idx := -1
	for i := range s.requests {
		if s.requests[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "no such request"})
		return
	}
	req := s.requests[idx]
	s.mu.Unlock()

	// Mint the URL OUTSIDE the lock — it touches keystore IO.
	url, exp, err := mintMailboxRecoverURL(req.Mailbox)
	if err != nil {
		log.Printf("forgot-approve: mint URL for %s by admin=%s: %v", req.Mailbox, adminMailbox, err)
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "could not mint recovery URL"})
		return
	}

	// Dispatch email. A send failure does NOT remove the entry from the
	// queue — the admin can retry and we don't want to silently lose the
	// only record of this request.
	if mailErr := sendSystemMail(
		req.Mailbox,
		"Password recovery link for "+req.Mailbox,
		"Mailbox Password Recovery",
		[]string{
			"An administrator approved your password-recovery request for " + req.Mailbox + ".",
			"Open the URL below within 15 minutes to set a new password. The link is single-use and burns on first click.",
			url,
			"Expires at " + exp.Format(time.RFC1123Z) + ".",
			"If you did NOT request this, contact postmaster@" + mailDomain + " immediately. The link will expire on its own, but a stray approval is worth flagging.",
		},
	); mailErr != nil {
		log.Printf("forgot-approve: mail send to %s FAILED (keeping queue entry): %v", req.Mailbox, mailErr)
		writeJSON(w, http.StatusBadGateway, envelope{OK: false,
			Error: "minted URL but mail send failed: " + mailErr.Error()})
		return
	}

	// Mail OK → safe to remove from queue.
	s.mu.Lock()
	for i := range s.requests {
		if s.requests[i].ID == id {
			s.requests = append(s.requests[:i], s.requests[i+1:]...)
			break
		}
	}
	if perr := s.persistLocked(); perr != nil {
		s.mu.Unlock()
		log.Printf("forgot-approve: persist after %s: %v", req.Mailbox, perr)
		// The mail already went out; surface success to the admin so the
		// UI doesn't double-fire. The stale queue entry will be re-GC'd
		// on next request anyway.
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
			"mailbox":   req.Mailbox,
			"sent_to":   req.Mailbox,
			"expires_at": exp.UTC().Format(time.RFC3339),
		}})
		return
	}
	s.mu.Unlock()
	log.Printf("forgot-approve: %s by admin=%s, URL emailed, expires %s",
		req.Mailbox, adminMailbox, exp.Format(time.RFC3339))
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"mailbox":    req.Mailbox,
		"sent_to":    req.Mailbox,
		"expires_at": exp.UTC().Format(time.RFC3339),
	}})
}

// handleDismiss kept for backwards-compat (the bridge handler still calls
// it). New requests go through handleByID instead.
func (s *forgotStore) handleDismiss(w http.ResponseWriter, r *http.Request) {
	s.handleByID(w, r)
}
