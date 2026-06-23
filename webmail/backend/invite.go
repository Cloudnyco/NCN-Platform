// invite.go — webmail invite-based mailbox registration.
//
// Mirrors the admin console's invite flow:
//  1. An admin (a mailbox listed in /etc/ncn-mail/admins.json) logs into
//     webmail and POSTs to /api/v1/mail/invites to mint a one-shot token.
//  2. They share https://mail.example.com/invite/<token> out-of-band.
//  3. Invitee opens the link, picks a local-part + password, submits.
//  4. We provision the mailbox: write the dovecot passwd-file, postfix
//     vmailbox + sender-login maps, create the maildir, reload services.
//
// State lives entirely under /etc/ncn-mail/. We never touch /etc/dovecot or
// /etc/postfix directly; instead, those config files reference *our* files
// as a second passdb/userdb / lookup map, so this service can manage users
// without write access to the upstream config tree.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	mailDomain  = "example.com"
	invitesPath = stateDir + "/invites.json"
	adminsPath  = stateDir + "/admins.json"
	// Dovecot 2.4 collapses two passdb passwd-file blocks into one even when
	// declared from separate include files, so instead of running a parallel
	// users file we append directly to the canonical /etc/dovecot/users.
	managedUsersPath = "/etc/dovecot/users"
	managedVmailbox  = stateDir + "/vmailbox"
	managedSenderLog = stateDir + "/sender-login"
	maildirRoot      = "/var/mail/vhosts/" + mailDomain
	defaultInviteTTL = 7 * 24 * time.Hour
	maildirUIDStr    = "5000" // vmail
	maildirGIDStr    = "5000"

	// Operator-bridge: HMAC secret shared with ncn-api on tyo. When the
	// admin console issues an "op-<payload>.<sig>" token, this service
	// verifies the signature offline — no cross-host runtime call.
	operatorBridgePath  = stateDir + "/operator-bridge.key"
	operatorTokenPrefix = "op-"
	operatorTokenMaxAge = 15 * time.Minute
)

// localPartRE is the alias name rule: 1–32 chars, lowercase ASCII letters,
// digits, dots, hyphens; cannot start or end with a separator. Mirrors the
// safer subset of RFC 5321/6531 that we feel like dealing with.
var localPartRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{0,30}[a-z0-9]$`)

// ----------------------------------------------------------------------------

type webmailInvite struct {
	Token     string    `json:"token"`
	Prefix    string    `json:"prefix"` // first 8 chars (display)
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedBy string    `json:"created_by"`
	Note      string    `json:"note,omitempty"`
	UsedBy    string    `json:"used_by,omitempty"`
	UsedAt    time.Time `json:"used_at,omitempty"`
}

type invitesFile struct {
	Version int             `json:"version"`
	Invites []webmailInvite `json:"invites"`
}

type adminsFile struct {
	Admins []string `json:"admins"`
}

type inviteStore struct {
	mu        sync.RWMutex
	invites   map[string]webmailInvite // key = full token
	admins    map[string]struct{}      // lowercase mailbox set
	bridgeKey []byte                   // HMAC key shared with ncn-api on tyo
}

// operatorClaims is the payload inside an op-<base64>.<sig> token issued by
// ncn-api on the operator console.
type operatorClaims struct {
	Op    string `json:"op"`   // operator username (already authenticated upstream)
	Role  string `json:"role"` // "admin" | "operator"
	Exp   int64  `json:"exp"`  // unix seconds
	Nonce string `json:"n"`    // random, just to scatter signatures
}

func newInviteStore() (*inviteStore, error) {
	s := &inviteStore{
		invites: map[string]webmailInvite{},
		admins:  map[string]struct{}{},
	}
	if err := s.loadAdmins(); err != nil {
		return nil, err
	}
	if err := s.loadInvites(); err != nil {
		return nil, err
	}
	if err := s.loadBridgeKey(); err != nil {
		// Non-fatal: operator-self-register flow is degraded but the
		// regular invite token flow keeps working.
		log.Printf("invite: operator bridge disabled: %v", err)
	}
	return s, nil
}

func (s *inviteStore) loadBridgeKey() error {
	data, err := os.ReadFile(operatorBridgePath)
	if err != nil {
		return err
	}
	if len(data) < 16 {
		return fmt.Errorf("%s too short (%d bytes)", operatorBridgePath, len(data))
	}
	s.bridgeKey = data
	log.Printf("invite: operator bridge key loaded (%d bytes)", len(data))
	return nil
}

// verifyOperatorToken parses and validates "op-<payload>.<sig>". On success
// it returns the claim and a synthetic webmailInvite suitable for downstream
// rendering. On failure: error explains why.
func (s *inviteStore) verifyOperatorToken(token string) (*operatorClaims, error) {
	if len(s.bridgeKey) == 0 {
		return nil, errors.New("operator bridge not configured on this host")
	}
	if !strings.HasPrefix(token, operatorTokenPrefix) {
		return nil, errors.New("not an operator token")
	}
	body := strings.TrimPrefix(token, operatorTokenPrefix)
	parts := strings.Split(body, ".")
	if len(parts) != 2 {
		return nil, errors.New("malformed operator token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode sig: %w", err)
	}
	mac := hmac.New(sha256.New, s.bridgeKey)
	mac.Write([]byte(parts[0]))
	if subtle.ConstantTimeCompare(sig, mac.Sum(nil)) != 1 {
		return nil, errors.New("bad signature")
	}
	var c operatorClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}
	now := time.Now().Unix()
	if c.Exp == 0 || now > c.Exp {
		return nil, errors.New("operator token expired")
	}
	// Reject tokens with an implausibly long TTL — a rogue ncn-api or a
	// stolen key shouldn't be able to mint long-lived tokens.
	if c.Exp-now > int64(operatorTokenMaxAge.Seconds())+60 {
		return nil, errors.New("operator token TTL too long")
	}
	if strings.TrimSpace(c.Op) == "" {
		return nil, errors.New("empty operator")
	}
	return &c, nil
}

func (s *inviteStore) loadAdmins() error {
	data, err := os.ReadFile(adminsPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Bootstrap: postmaster@ is admin by default.
			s.admins["postmaster@"+mailDomain] = struct{}{}
			af := adminsFile{Admins: []string{"postmaster@" + mailDomain}}
			body, _ := json.MarshalIndent(af, "", "  ")
			if err := os.WriteFile(adminsPath, body, 0o600); err != nil {
				log.Printf("invite: cannot write %s: %v", adminsPath, err)
			} else {
				log.Printf("invite: bootstrapped %s with postmaster@", adminsPath)
			}
			return nil
		}
		return err
	}
	var af adminsFile
	if err := json.Unmarshal(data, &af); err != nil {
		return fmt.Errorf("parse %s: %w", adminsPath, err)
	}
	for _, m := range af.Admins {
		s.admins[strings.ToLower(strings.TrimSpace(m))] = struct{}{}
	}
	return nil
}

func (s *inviteStore) loadInvites() error {
	data, err := os.ReadFile(invitesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var f invitesFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse %s: %w", invitesPath, err)
	}
	for _, inv := range f.Invites {
		s.invites[inv.Token] = inv
	}
	return nil
}

func (s *inviteStore) persistLocked() error {
	f := invitesFile{Version: 1, Invites: make([]webmailInvite, 0, len(s.invites))}
	for _, inv := range s.invites {
		f.Invites = append(f.Invites, inv)
	}
	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := invitesPath + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, invitesPath)
}

func (s *inviteStore) isAdmin(mailbox string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.admins[strings.ToLower(strings.TrimSpace(mailbox))]
	return ok
}

func (s *inviteStore) issue(by, note string) (webmailInvite, error) {
	tok := make([]byte, 24)
	if _, err := rand.Read(tok); err != nil {
		return webmailInvite{}, err
	}
	tokHex := hex.EncodeToString(tok)
	now := time.Now().UTC()
	inv := webmailInvite{
		Token:     tokHex,
		Prefix:    tokHex[:8],
		CreatedAt: now,
		ExpiresAt: now.Add(defaultInviteTTL),
		CreatedBy: by,
		Note:      strings.TrimSpace(note),
	}
	s.mu.Lock()
	s.invites[tokHex] = inv
	s.gcLocked(now)
	if err := s.persistLocked(); err != nil {
		s.mu.Unlock()
		return webmailInvite{}, err
	}
	s.mu.Unlock()
	return inv, nil
}

func (s *inviteStore) gcLocked(now time.Time) {
	for k, inv := range s.invites {
		// Drop entries that are >30 days past expiry or >30 days past use.
		ref := inv.ExpiresAt
		if !inv.UsedAt.IsZero() && inv.UsedAt.After(ref) {
			ref = inv.UsedAt
		}
		if now.Sub(ref) > 30*24*time.Hour {
			delete(s.invites, k)
		}
	}
}

func (s *inviteStore) listForDisplay() []webmailInvite {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]webmailInvite, 0, len(s.invites))
	for _, inv := range s.invites {
		// Redact full token in listings; UI uses Prefix to revoke.
		inv.Token = ""
		out = append(out, inv)
	}
	return out
}

func (s *inviteStore) revokeByPrefix(prefix string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, inv := range s.invites {
		if inv.Prefix == prefix {
			delete(s.invites, k)
			_ = s.persistLocked()
			return true
		}
	}
	return false
}

func (s *inviteStore) lookup(token string) (webmailInvite, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inv, ok := s.invites[token]
	return inv, ok
}

func (s *inviteStore) markUsedLocked(token, localPart string) error {
	inv, ok := s.invites[token]
	if !ok {
		return errors.New("invite vanished mid-use")
	}
	inv.UsedBy = localPart
	inv.UsedAt = time.Now().UTC()
	s.invites[token] = inv
	return s.persistLocked()
}

// ----------------------------------------------------------------------------
// HTTP handlers
// ----------------------------------------------------------------------------

// POST /api/v1/mail/invites      body: { "note": "..." }
//
// Requires a mail session whose mailbox is on the admin list.
func (s *inviteStore) handleCreate(w http.ResponseWriter, r *http.Request) {
	c, ok := mailClaimsFromRequest(r)
	if !ok || !s.isAdmin(c.Mailbox) {
		writeJSON(w, http.StatusForbidden, envelope{OK: false, Error: "admin only"})
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)

	inv, err := s.issue(c.Mailbox, req.Note)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"token":      inv.Token, // returned ONCE (caller must copy now)
		"prefix":     inv.Prefix,
		"url":        "https://mail.example.com/invite/" + inv.Token,
		"expires_at": inv.ExpiresAt,
		"note":       inv.Note,
	}})
}

// GET /api/v1/mail/invites
func (s *inviteStore) handleList(w http.ResponseWriter, r *http.Request) {
	c, ok := mailClaimsFromRequest(r)
	if !ok || !s.isAdmin(c.Mailbox) {
		writeJSON(w, http.StatusForbidden, envelope{OK: false, Error: "admin only"})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: s.listForDisplay()})
}

// POST /api/v1/mail/admin/reset-password
//
//	{ "mailbox": "alice@example.com", "new_password": "..." }
//
// Admin-only. Replaces the password hash for an existing mailbox in
// /etc/dovecot/users. No effect on stashed cred (the user will be forced to
// re-enter on next login since the cached pw will fail IMAP LOGIN).
func (s *inviteStore) handleAdminResetPassword(w http.ResponseWriter, r *http.Request) {
	c, ok := mailClaimsFromRequest(r)
	if !ok || !s.isAdmin(c.Mailbox) {
		writeJSON(w, http.StatusForbidden, envelope{OK: false, Error: "admin only"})
		return
	}
	var req struct {
		Mailbox     string `json:"mailbox"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	req.Mailbox = strings.ToLower(strings.TrimSpace(req.Mailbox))
	if req.Mailbox == "" || len(req.NewPassword) < 8 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: "mailbox + new_password (≥8) required"})
		return
	}
	if err := replaceMailboxPassword(req.Mailbox, req.NewPassword); err != nil {
		log.Printf("admin reset (%s by %s): %v", req.Mailbox, c.Mailbox, err)
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	log.Printf("admin reset: %s reset password for %s", c.Mailbox, req.Mailbox)
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"mailbox":  req.Mailbox,
		"reset_by": c.Mailbox,
	}})
}

// replaceMailboxPassword rewrites the bcrypt hash for the given mailbox in
// /etc/dovecot/users. Returns error if not found. Holds provisionMu since it
// touches the same file as provisioning. Owner/group/mode are inherited from
// the existing file — dovecot's auth-worker (running as vmail) needs group
// read access, and the existing /etc/dovecot/users on this host is already
// set up correctly (root:dovecot mode 0640).
func replaceMailboxPassword(mailbox, newPassword string) error {
	provisionMu.Lock()
	defer provisionMu.Unlock()

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 10)
	if err != nil {
		return err
	}

	// Inherit perms from the existing file so we don't accidentally lock
	// out dovecot (a previous bug hardcoded gid=113 which is postdrop).
	st, err := os.Stat(managedUsersPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", managedUsersPath, err)
	}
	sysStat, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot read owner of dovecot users file")
	}
	origUID, origGID, origMode := int(sysStat.Uid), int(sysStat.Gid), st.Mode().Perm()

	data, err := os.ReadFile(managedUsersPath)
	if err != nil {
		return err
	}
	prefix := strings.ToLower(mailbox) + ":"
	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if !strings.HasPrefix(strings.ToLower(line), prefix) {
			continue
		}
		fields := strings.SplitN(line, ":", 8)
		if len(fields) < 8 {
			return fmt.Errorf("malformed passwd-file row for %s", mailbox)
		}
		fields[1] = "{BLF-CRYPT}" + string(hash)
		lines[i] = strings.Join(fields, ":")
		found = true
		break
	}
	if !found {
		return fmt.Errorf("mailbox not found: %s", mailbox)
	}
	tmp := managedUsersPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")), origMode); err != nil {
		return err
	}
	if err := os.Chown(tmp, origUID, origGID); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("chown %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, managedUsersPath); err != nil {
		return err
	}

	// Best-effort: poke dovecot so the change is picked up on the next auth
	// (passwd-file driver checks mtime, but reload makes it instant).
	if out, err := exec.Command("/usr/bin/doveadm", "reload").CombinedOutput(); err != nil {
		log.Printf("admin reset: doveadm reload failed (continuing): %v · %s", err, strings.TrimSpace(string(out)))
	}

	// Wipe any cached stash for this mailbox — withIMAP would otherwise
	// keep auth'ing with the previous (now-revoked) password and silently
	// fail every IMAP operation until the user re-logged in. See the
	// forgetStashEverywhere docstring.
	forgetStashEverywhere(mailbox)
	return nil
}

// DELETE /api/v1/mail/invites/<prefix>
func (s *inviteStore) handleRevoke(w http.ResponseWriter, r *http.Request) {
	c, ok := mailClaimsFromRequest(r)
	if !ok || !s.isAdmin(c.Mailbox) {
		writeJSON(w, http.StatusForbidden, envelope{OK: false, Error: "admin only"})
		return
	}
	prefix := strings.TrimPrefix(r.URL.Path, "/api/v1/mail/invites/")
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "prefix required"})
		return
	}
	if s.revokeByPrefix(prefix) {
		writeJSON(w, http.StatusOK, envelope{OK: true})
		return
	}
	writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "no matching invite"})
}

// GET /api/v1/mail/invite/preview?token=<...>
//
// Public — but token IS the auth, so anyone who knows it can preview.
// Accepts both random-hex admin-issued invites AND op-<...> operator-bridge
// tokens minted by ncn-api on the admin console.
func (s *inviteStore) handlePreview(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")

	if strings.HasPrefix(token, operatorTokenPrefix) {
		c, err := s.verifyOperatorToken(token)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
			"operator":     true,
			"operator_op":  c.Op,
			"operator_rol": c.Role,
			"created_by":   "ncn-operator-console",
			"expires_at":   time.Unix(c.Exp, 0).UTC(),
			"domain":       mailDomain,
			"suggested":    c.Op, // pre-fill the alias with the operator username
		}})
		return
	}

	inv, ok := s.lookup(token)
	if !ok {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "invite not found"})
		return
	}
	now := time.Now().UTC()
	if now.After(inv.ExpiresAt) {
		writeJSON(w, http.StatusGone, envelope{OK: false, Error: "invite expired"})
		return
	}
	if !inv.UsedAt.IsZero() {
		writeJSON(w, http.StatusConflict, envelope{OK: false, Error: "invite already used"})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"prefix":     inv.Prefix,
		"created_by": inv.CreatedBy,
		"expires_at": inv.ExpiresAt,
		"note":       inv.Note,
		"domain":     mailDomain,
	}})
}

// POST /api/v1/mail/invite/complete
//
//	{ "token": "...", "local_part": "alice", "password": "..." }
func (s *inviteStore) handleComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token     string `json:"token"`
		LocalPart string `json:"local_part"`
		Password  string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	req.LocalPart = strings.ToLower(strings.TrimSpace(req.LocalPart))
	if !localPartRE.MatchString(req.LocalPart) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: "alias must be 2–32 chars, lowercase a-z, 0-9, dot or hyphen; cannot start/end with separator"})
		return
	}
	if len(req.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "password must be at least 8 characters"})
		return
	}

	mailbox := req.LocalPart + "@" + mailDomain

	// Operator-bridge path: token is signed by ncn-api on tyo. No invite
	// store entry, no markUsed bookkeeping — possession of a valid HMAC
	// signature within TTL is the authorization.
	//
	// Anti-replay: the alias is REQUIRED to equal the operator username.
	// Without this, a single valid op-token (within its 15min TTL) could
	// be used to mint multiple mailboxes under arbitrary aliases. Binding
	// alias=op turns "I am operator X" into "I get mailbox X@", and a
	// second use trivially hits userExists() → 409.
	if strings.HasPrefix(req.Token, operatorTokenPrefix) {
		c, err := s.verifyOperatorToken(req.Token)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: err.Error()})
			return
		}
		opAlias := strings.ToLower(strings.TrimSpace(c.Op))
		if req.LocalPart != opAlias {
			writeJSON(w, http.StatusForbidden, envelope{OK: false,
				Error: "operator self-register can only claim alias '" + opAlias + "' (matching your operator username)"})
			return
		}
		if userExists(mailbox) {
			writeJSON(w, http.StatusConflict, envelope{OK: false, Error: "alias already taken"})
			return
		}
		if err := provisionMailbox(mailbox, req.Password); err != nil {
			log.Printf("invite: provision (operator %s) %s: %v", c.Op, mailbox, err)
			writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "provision failed: " + err.Error()})
			return
		}
		log.Printf("invite: operator %s self-registered mailbox %s", c.Op, mailbox)
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
			"mailbox":  mailbox,
			"operator": c.Op,
		}})
		return
	}

	// Reserve the invite under lock — also guards against the same alias
	// being claimed twice concurrently.
	s.mu.Lock()
	inv, ok := s.invites[req.Token]
	if !ok {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "invite not found"})
		return
	}
	now := time.Now().UTC()
	if now.After(inv.ExpiresAt) {
		s.mu.Unlock()
		writeJSON(w, http.StatusGone, envelope{OK: false, Error: "invite expired"})
		return
	}
	if !inv.UsedAt.IsZero() {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, envelope{OK: false, Error: "invite already used"})
		return
	}

	// Collision check against both the managed file AND the role mailboxes
	// in the upstream /etc/dovecot/users.
	if userExists(mailbox) {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, envelope{OK: false, Error: "alias already taken"})
		return
	}

	if err := provisionMailbox(mailbox, req.Password); err != nil {
		s.mu.Unlock()
		log.Printf("invite: provision %s: %v", mailbox, err)
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "provision failed: " + err.Error()})
		return
	}

	if err := s.markUsedLocked(req.Token, req.LocalPart); err != nil {
		log.Printf("invite: mark used %s: %v", req.Token, err)
	}
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"mailbox": mailbox,
	}})
}

// ----------------------------------------------------------------------------
// Mailbox provisioning — writes the three managed files + creates the maildir
// + reloads postfix/dovecot.
// ----------------------------------------------------------------------------

var provisionMu sync.Mutex

func userExists(mailbox string) bool {
	mailbox = strings.ToLower(mailbox)
	// 1. Managed file
	if data, err := os.ReadFile(managedUsersPath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.ToLower(line), mailbox+":") {
				return true
			}
		}
	}
	// 2. Upstream dovecot users (role mailboxes)
	if data, err := os.ReadFile("/etc/dovecot/users"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.ToLower(line), mailbox+":") {
				return true
			}
		}
	}
	return false
}

func provisionMailbox(mailbox, password string) error {
	provisionMu.Lock()
	defer provisionMu.Unlock()

	local := strings.SplitN(mailbox, "@", 2)[0]

	// BLF-CRYPT (dovecot's name for bcrypt). Cost 10 — same as our admin
	// console operators.json.
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 10)
	if err != nil {
		return fmt.Errorf("hash: %w", err)
	}

	// dovecot passwd-file row: 8 fields, colon-separated.
	//   user:hash:uid:gid:gecos:home:shell:extra
	dovecotLine := fmt.Sprintf(
		"%s:{BLF-CRYPT}%s:%s:%s::/var/mail/vhosts/%s/%s::\n",
		mailbox, string(hash), maildirUIDStr, maildirGIDStr, mailDomain, local,
	)
	if err := appendLine(managedUsersPath, dovecotLine, 0o640, "root:dovecot"); err != nil {
		return fmt.Errorf("dovecot users: %w", err)
	}
	// Postfix vmailbox map: "<addr>  example.com/<user>/"
	if err := appendLine(managedVmailbox,
		fmt.Sprintf("%s\t%s/%s/\n", mailbox, mailDomain, local),
		0o644, ""); err != nil {
		return fmt.Errorf("vmailbox: %w", err)
	}
	// sender-login: user can send as themselves
	if err := appendLine(managedSenderLog,
		fmt.Sprintf("%s\t%s\n", mailbox, mailbox),
		0o644, ""); err != nil {
		return fmt.Errorf("sender-login: %w", err)
	}

	// Rebuild .db files. Use absolute paths since the systemd unit's PATH
	// may not include /usr/sbin. Capture stderr so we surface the real
	// reason on failure (postmap is laconic on its own).
	if out, err := exec.Command("/usr/sbin/postmap", "hash:"+managedVmailbox).CombinedOutput(); err != nil {
		return fmt.Errorf("postmap vmailbox: %w · %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("/usr/sbin/postmap", "hash:"+managedSenderLog).CombinedOutput(); err != nil {
		return fmt.Errorf("postmap sender-login: %w · %s", err, strings.TrimSpace(string(out)))
	}

	// Create the maildir (postfix LMTP delivery autocreates, but dovecot
	// occasionally trips over missing parent on first SELECT — we eagerly
	// make it).
	dir := filepath.Join(maildirRoot, local)
	for _, sub := range []string{"cur", "new", "tmp"} {
		p := filepath.Join(dir, sub)
		if err := os.MkdirAll(p, 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", p, err)
		}
	}
	if err := chownRecursive(dir, 5000, 5000); err != nil {
		return fmt.Errorf("chown %s: %w", dir, err)
	}

	// Reload services. Best-effort: provisioning is already committed to
	// disk, the worst case is a few-second delay before postfix re-reads
	// the new map.
	if out, err := exec.Command("/usr/bin/systemctl", "reload", "postfix").CombinedOutput(); err != nil {
		log.Printf("invite: postfix reload failed (continuing): %v · %s", err, strings.TrimSpace(string(out)))
	}
	// Dovecot watches passwd-file mtime; reload is not strictly required.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "/usr/bin/doveadm", "reload").CombinedOutput(); err != nil {
		log.Printf("invite: doveadm reload failed (continuing): %v · %s", err, strings.TrimSpace(string(out)))
	}

	log.Printf("invite: provisioned mailbox %s", mailbox)
	return nil
}

func appendLine(path, line string, mode os.FileMode, _ string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}

func chownRecursive(path string, uid, gid int) error {
	return filepath.Walk(path, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chown(p, uid, gid)
	})
}
