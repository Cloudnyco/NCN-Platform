// Acme Net — Core Console · operator authentication.
//
// Two-factor: bcrypt-hashed password + RFC 6238 TOTP. Sessions are issued
// as HMAC-SHA256 signed cookies (compact JWT-shaped tokens with no library
// dependency beyond crypto/* and x/crypto/bcrypt).
package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	cookieName        = "ncn_session"
	sessionTTL        = 8 * time.Hour
	authConfigDir     = "/etc/ncn-core-console"
	operatorsPath     = authConfigDir + "/operators.json"
	invitesPath       = authConfigDir + "/invites.json"
	sessionSecretPath = authConfigDir + "/session.key"

	// Two-step login: between password-verify (step 1) and TOTP-verify
	// (step 2) the server hands the client a short-lived HMAC-signed
	// "login intent" token in a cookie. 5 minutes is long enough for a
	// user to fish out their authenticator; not long enough for an
	// attacker who steals the cookie to do anything (they'd still need
	// the TOTP code).
	loginIntentCookie = "ncn_login_intent"
	loginIntentTTL    = 5 * time.Minute

	// Long-lived device-trust cookie. When set, future logins from this
	// browser skip step 2 entirely. The cookie value is an opaque random
	// token; we store only its bcrypt hash per operator, so a leaked
	// operators.json never reveals trust tokens.
	deviceTrustCookie = "ncn_device_trust"
	deviceTrustTTL    = 90 * 24 * time.Hour // 90 days

	// Cap per-operator trusted-device count. After this many,
	// the oldest entry is pruned at trust time so the slice stays bounded.
	maxTrustedDevices = 16
)

// ----------------------------------------------------------------------------
// Operator store
// ----------------------------------------------------------------------------

type operatorRecord struct {
	Username       string              `json:"username"`
	PasswordHash   string              `json:"password_hash"`   // bcrypt
	TOTPSecret     string              `json:"totp_secret"`     // base32 (no padding)
	Role           string              `json:"role"`
	CreatedAt      string              `json:"created_at"`
	RecoveryCodes  []string            `json:"recovery_codes,omitempty"`  // bcrypt hashes; each consumed on use
	Passkeys       []passkeyCredential `json:"passkeys,omitempty"`        // registered WebAuthn credentials

	// Trusted-device list — browsers that have completed a TOTP challenge
	// with the "trust this device" checkbox. Subsequent password logins
	// from a device whose stored bcrypt hash matches the device-trust
	// cookie skip the TOTP step.
	TrustedDevices []trustedDevice `json:"trusted_devices,omitempty"`

	// Invite/approval workflow. Direct admin-created accounts are auto-
	// approved on creation. Invite-self-registered accounts land here with
	// Approved=false until the inviting admin clicks "approve".
	Approved   bool   `json:"approved"`
	InvitedBy  string `json:"invited_by,omitempty"`
	InvitedAt  string `json:"invited_at,omitempty"`
	ApprovedAt string `json:"approved_at,omitempty"`

	// SSH public keys this operator can sign a login challenge with.
	// See auth_ssh.go for the challenge/verify flow. Adding a key is
	// always operator-self-service; admins can't add SSH keys to other
	// operators (same self-service model as passkeys).
	SSHKeys []sshKeyRecord `json:"ssh_keys,omitempty"`

	// External OAuth/OIDC/Telegram identities bound to this operator (see
	// oauth.go). Binding is operator-self-service while authenticated; an
	// unbound external identity can NEVER sign in. (provider, subject) is
	// globally unique across all operators.
	ExternalIdentities []externalIdentity `json:"external_identities,omitempty"`

	// BotNick is an optional self-chosen display name (称呼) the Telegram bot
	// addresses this operator by. Empty → bot falls back to the Telegram
	// @username / group tag. Set in-chat via /callme (see bot_tg.go).
	BotNick string `json:"bot_nick,omitempty"`

	// AvatarURL is the operator's profile picture, auto-captured from their
	// OAuth provider (GitHub avatar_url / Telegram photo_url) at login or bind.
	// Empty → the UI shows an initials avatar. https-only; rendered as <img>.
	AvatarURL string `json:"avatar_url,omitempty"`
}

// externalIdentity is one bound provider login. Subject is the provider's
// STABLE per-user id (OIDC `sub` / GitHub numeric id / Telegram user id) — the
// binding key. Email is for display only (never the binding key — emails change
// / are reassignable).
type externalIdentity struct {
	Provider   string `json:"provider"` // google | microsoft | github | telegram
	Subject    string `json:"subject"`
	Email      string `json:"email,omitempty"`
	BoundAt    string `json:"bound_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
}

// sshKeyRecord is one registered SSH public key the operator can use
// to sign login challenges via the ncn-login CLI. The pubkey is stored
// in OpenSSH authorized_keys format (`ssh-ed25519 AAAA… optional-comment`)
// — same shape `ssh-keygen -y` emits, what fits in ~/.ssh/authorized_keys,
// and what GitHub's "Settings → SSH Keys" accepts. We parse + re-emit on
// add to canonicalize and reject malformed strings.
type sshKeyRecord struct {
	ID          string `json:"id"`           // 8-hex; stable identifier for revoke API
	Label       string `json:"label"`        // operator-supplied, e.g. "MacBook YubiKey"
	PublicKey   string `json:"public_key"`   // canonicalized authorized_keys line
	Fingerprint string `json:"fingerprint"`  // SHA256:… for UI display + lookup at login
	Type        string `json:"type"`         // ssh-ed25519 / ssh-rsa / ecdsa-sha2-… / sk-ssh-ed25519@openssh.com
	CreatedAt   int64  `json:"created_at"`   // unix
	LastUsedAt  int64  `json:"last_used_at"` // unix; bumped on successful login
}

// trustedDevice records one browser/agent that the operator has marked
// "remember this device" on. Hash is a bcrypt of the cookie token; the
// raw token never reaches disk. ID is a stable opaque identifier the
// operator can revoke in /admin/security.
type trustedDevice struct {
	ID         string `json:"id"`              // 8-char random, for revoke API
	Hash       string `json:"hash"`            // bcrypt(cookie token)
	Label      string `json:"label"`           // e.g. "Chrome on macOS"
	UserAgent  string `json:"user_agent"`      // full UA for audit
	CreatedAt  int64  `json:"created_at"`      // unix
	LastSeenAt int64  `json:"last_seen_at"`    // unix; bumped each successful match
	LastSeenIP string `json:"last_seen_ip"`
}

type authStore struct {
	mu        sync.RWMutex
	operators map[string]operatorRecord
	secret    []byte

	// WebAuthn state (lazily initialized via initWebAuthn).
	wa *webauthnSubsystem

	// Mail bridge — wired up by main() after newMailBridgeService. May be
	// nil if /etc/ncn-core-console/operator-mail-bridge.key isn't installed
	// yet; invite-by-email checks this and falls back to "URL only" mode.
	mailBridge *mailBridgeService

	// In-memory challenge + redeem ticket maps for SSH-key login.
	// Lifecycle: 5-min challenge TTL, 60-sec redeem TTL; both single-use.
	// See auth_ssh.go for the handlers that read/write this.
	sshLogin *sshLoginStore

	// tgBindTickets are one-time capability tokens minted by the Telegram
	// bot's /bind command. Each carries the requesting Telegram user's
	// (id, username); an authenticated operator consumes it on /admin/bind to
	// bind that Telegram identity to their own account. Single process, short
	// TTL → kept in memory. See mint/peek/consumeTGBindTicket + bot_tg.go.
	tgBindMu      sync.Mutex
	tgBindTickets map[string]tgBindTicket
}

// tgBindTicket is one pending Telegram-bind capability (see authStore field).
type tgBindTicket struct {
	TGID       string
	TGUsername string
	Exp        time.Time
}

func loadAuthStore() (*authStore, error) {
	if err := os.MkdirAll(authConfigDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", authConfigDir, err)
	}

	s := &authStore{
		operators:     map[string]operatorRecord{},
		sshLogin:      newSSHLoginStore(),
		tgBindTickets: map[string]tgBindTicket{},
	}

	// --- session secret ---
	if data, err := os.ReadFile(sessionSecretPath); err == nil && len(data) >= 32 {
		s.secret = data
	} else {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
		if err := os.WriteFile(sessionSecretPath, secret, 0o600); err != nil {
			return nil, err
		}
		s.secret = secret
		log.Printf("auth: generated new session secret → %s", sessionSecretPath)
	}

	// --- operators --- prefer Postgres when populated (post-cutover), else the
	// JSON file. The first DB-enabled boot migrates the file into PG (below).
	loadedOpsFromDB := false
	if globalDB != nil {
		if doc, err := loadConfigDoc("operators"); err != nil {
			log.Printf("operators: db load failed (%v) — falling back to file", err)
		} else if doc != nil {
			var ops []operatorRecord
			if err := json.Unmarshal(doc, &ops); err != nil {
				return nil, fmt.Errorf("parse operators db doc: %w", err)
			}
			for _, op := range ops {
				s.operators[op.Username] = op
			}
			loadedOpsFromDB = true
		}
	}
	if !loadedOpsFromDB {
		if data, err := os.ReadFile(operatorsPath); err == nil {
			var ops []operatorRecord
			if err := json.Unmarshal(data, &ops); err != nil {
				return nil, fmt.Errorf("parse operators.json: %w", err)
			}
			for _, op := range ops {
				s.operators[op.Username] = op
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}

	if len(s.operators) == 0 {
		if err := s.bootstrap(); err != nil {
			return nil, fmt.Errorf("bootstrap: %w", err)
		}
	} else {
		// Migration: existing operators that pre-date recovery codes get a
		// fresh set generated on first launch with the new build.
		if err := s.migrateRecoveryCodes(); err != nil {
			return nil, fmt.Errorf("migrate recovery codes: %w", err)
		}
		// Approval-flag migration. Accounts created BEFORE the invite system
		// have InvitedBy="" — those are admin-direct-created and auto-trusted.
		// New invite-created accounts will set InvitedBy=<admin> so this loop
		// won't touch them.
		dirty := false
		for u, op := range s.operators {
			if op.InvitedBy == "" && !op.Approved {
				op.Approved = true
				if op.ApprovedAt == "" {
					op.ApprovedAt = time.Now().UTC().Format(time.RFC3339)
				}
				s.operators[u] = op
				dirty = true
				log.Printf("auth: migrated user=%q to approved=true (legacy direct-create)", u)
			}
		}
		if dirty {
			_ = s.persist()
		}
	}

	// Migrate operators file→Postgres on the first DB-enabled boot (persist
	// dual-writes; the file stays the backup). No-op once the DB holds them.
	if globalDB != nil && !loadedOpsFromDB {
		if err := s.persist(); err != nil {
			return nil, fmt.Errorf("migrate operators to db: %w", err)
		}
	}

	return s, nil
}

// generateRecoveryCodes returns N plain-text codes (for one-time display)
// and their bcrypt hashes (for persistent storage). Format: 4×4 base32
// groups joined by dashes, e.g. "K3MN-7Q2X-A4BR-V9YH".
func generateRecoveryCodes(n int) (plain []string, hashes []string, err error) {
	plain = make([]string, n)
	hashes = make([]string, n)
	for i := 0; i < n; i++ {
		buf := make([]byte, 10)
		if _, err = rand.Read(buf); err != nil {
			return nil, nil, err
		}
		s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)[:16]
		plain[i] = s[:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
		h, hErr := bcrypt.GenerateFromPassword([]byte(plain[i]), bcrypt.DefaultCost)
		if hErr != nil {
			return nil, nil, hErr
		}
		hashes[i] = string(h)
	}
	return
}

// migrateRecoveryCodes generates fresh codes for any operator missing them.
// Plain codes go to the journal exactly once so the operator can capture them.
func (s *authStore) migrateRecoveryCodes() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	needPersist := false
	for u, op := range s.operators {
		if len(op.RecoveryCodes) > 0 {
			continue
		}
		plain, hashes, err := generateRecoveryCodes(10)
		if err != nil {
			return err
		}
		op.RecoveryCodes = hashes
		s.operators[u] = op
		needPersist = true

		bar := strings.Repeat("=", 72)
		log.Println(bar)
		log.Printf("  NCN AUTH · MIGRATION · RECOVERY CODES for %q — capture now", u)
		log.Println(bar)
		for i, c := range plain {
			log.Printf("  [%2d/10]  %s", i+1, c)
		}
		log.Println(bar)
		log.Println("  ↑ each code single-use; use via login page 'Forgot password' flow")
		log.Println(bar)
	}
	if needPersist {
		// release lock to call persist (which reacquires read lock)
		// Actually persist also locks, but we hold write lock. Inline persist:
		ops := make([]operatorRecord, 0, len(s.operators))
		for _, op := range s.operators {
			ops = append(ops, op)
		}
		data, err := json.MarshalIndent(ops, "", "  ")
		if err != nil {
			return err
		}
		if err := writeOperatorsBlob(data); err != nil {
			return err
		}
	}
	return nil
}

func (s *authStore) persist() error {
	s.mu.RLock()
	ops := make([]operatorRecord, 0, len(s.operators))
	for _, op := range s.operators {
		ops = append(ops, op)
	}
	s.mu.RUnlock()
	data, err := json.MarshalIndent(ops, "", "  ")
	if err != nil {
		return err
	}
	return writeOperatorsBlob(data)
}

// writeOperatorsBlob atomically writes the operators JSON to disk and mirrors
// it into Postgres when available (dual-write; the file stays the durable
// backup + the globalDB==nil path). Pure I/O — it does NOT touch s.mu, so it's
// safe to call from the many handlers that already hold the auth write lock
// (the operators store predates a single persist chokepoint; this funnels them
// all without changing their locking). A DB error is non-fatal.
func writeOperatorsBlob(data []byte) error {
	tmp := operatorsPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, operatorsPath); err != nil {
		return err
	}
	if globalDB != nil {
		if err := saveConfigDoc("operators", data); err != nil {
			log.Printf("operators: db persist failed (%v) — file is current", err)
		}
	}
	return nil
}

// findOperatorByIdentity returns the operator username bound to (provider,
// subject), or ("", false). This is the ONLY lookup path for OAuth login — an
// unbound external identity matches nobody and is rejected.
func (s *authStore) findOperatorByIdentity(provider, subject string) (string, bool) {
	if subject == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for name, op := range s.operators {
		for _, ei := range op.ExternalIdentities {
			if ei.Provider == provider && ei.Subject == subject {
				return name, true
			}
		}
	}
	return "", false
}

// bindIdentity binds (provider, subject) to an operator. Rejects if that
// identity is already bound to a DIFFERENT operator (global uniqueness). A new
// subject for a provider the operator already linked replaces the old one (the
// operator switched their upstream account).
func (s *authStore) bindIdentity(username, provider, subject, email string) error {
	if subject == "" {
		return errors.New("empty subject")
	}
	s.mu.Lock()
	for name, op := range s.operators {
		for _, ei := range op.ExternalIdentities {
			if ei.Provider == provider && ei.Subject == subject && name != username {
				s.mu.Unlock()
				return fmt.Errorf("this %s account is already linked to another operator", provider)
			}
		}
	}
	op, ok := s.operators[username]
	if !ok {
		s.mu.Unlock()
		return errors.New("operator not found")
	}
	kept := op.ExternalIdentities[:0:0]
	for _, ei := range op.ExternalIdentities {
		if ei.Provider != provider { // drop any prior binding for this provider
			kept = append(kept, ei)
		}
	}
	kept = append(kept, externalIdentity{Provider: provider, Subject: subject, Email: email, BoundAt: time.Now().UTC().Format(time.RFC3339)})
	op.ExternalIdentities = kept
	s.operators[username] = op
	s.mu.Unlock()
	return s.persist()
}

// setAvatar stores the operator's profile-picture URL, auto-captured from the
// OAuth provider at login/bind. Best-effort + non-critical: only a sane,
// length-capped https URL is accepted; anything else is silently ignored (an
// avatar must never block or fail a login). No-op when unchanged.
func (s *authStore) setAvatar(username, url string) {
	url = strings.TrimSpace(url)
	if url == "" || !strings.HasPrefix(url, "https://") || len(url) > 512 || strings.ContainsAny(url, " \t\n\r\"'<>") {
		return
	}
	s.mu.Lock()
	op, ok := s.operators[username]
	if !ok || op.AvatarURL == url {
		s.mu.Unlock()
		return
	}
	op.AvatarURL = url
	s.operators[username] = op
	s.mu.Unlock()
	_ = s.persist()
}

// unbindIdentity removes the operator's binding for a provider.
func (s *authStore) unbindIdentity(username, provider string) error {
	s.mu.Lock()
	op, ok := s.operators[username]
	if !ok {
		s.mu.Unlock()
		return errors.New("operator not found")
	}
	kept := op.ExternalIdentities[:0:0]
	for _, ei := range op.ExternalIdentities {
		if ei.Provider != provider {
			kept = append(kept, ei)
		}
	}
	op.ExternalIdentities = kept
	s.operators[username] = op
	s.mu.Unlock()
	return s.persist()
}

// touchIdentity stamps LastUsedAt after a successful OAuth login (best-effort).
func (s *authStore) touchIdentity(username, provider, subject string) {
	s.mu.Lock()
	if op, ok := s.operators[username]; ok {
		for i := range op.ExternalIdentities {
			if op.ExternalIdentities[i].Provider == provider && op.ExternalIdentities[i].Subject == subject {
				op.ExternalIdentities[i].LastUsedAt = time.Now().UTC().Format(time.RFC3339)
			}
		}
		s.operators[username] = op
	}
	s.mu.Unlock()
	_ = s.persist()
}

// operatorApproved reports whether the operator exists and is approved — the
// gate every OAuth login must pass before a session is minted.
func (s *authStore) operatorApproved(username string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	op, ok := s.operators[username]
	return ok && op.Approved
}

// adminTelegramIDs returns the Telegram user ids (subject strings) of every
// admin operator who has bound their Telegram — used to DM agent write-approval
// cards to admins.
func (s *authStore) adminTelegramIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for _, op := range s.operators {
		if op.Role != roleAdmin {
			continue
		}
		for _, ei := range op.ExternalIdentities {
			if ei.Provider == "telegram" && ei.Subject != "" {
				out = append(out, ei.Subject)
			}
		}
	}
	return out
}

// botNick returns the operator's self-chosen Telegram display name (称呼), or
// "" if none set / operator unknown. Read-only.
func (s *authStore) botNick(username string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if op, ok := s.operators[username]; ok {
		return op.BotNick
	}
	return ""
}

// normalizeBotNick trims, strips control chars, and caps the 称呼 at 32 runes.
// Pure — separated from setBotNick so it can be unit-tested without persisting.
func normalizeBotNick(nick string) string {
	nick = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(nick))
	if rs := []rune(nick); len(rs) > 32 {
		nick = string(rs[:32])
	}
	return nick
}

// setBotNick sets (or, with an empty nick, clears) the operator's Telegram
// display name. The stored value lands verbatim in chat HTML, so callers must
// still escape it on display.
func (s *authStore) setBotNick(username, nick string) error {
	nick = normalizeBotNick(nick)
	s.mu.Lock()
	op, ok := s.operators[username]
	if !ok {
		s.mu.Unlock()
		return errors.New("operator not found")
	}
	op.BotNick = nick
	s.operators[username] = op
	s.mu.Unlock()
	return s.persist()
}

// 10 min — generous enough to cover a fresh console login + TOTP between the
// bot minting the link and the operator clicking confirm.
const tgBindTicketTTL = 10 * time.Minute

// mintTGBindTicket creates a single-use, short-TTL token carrying the Telegram
// (id, username) that an authenticated operator redeems on /admin/bind. Expired
// tickets are swept opportunistically on each mint.
func (s *authStore) mintTGBindTicket(tgID, tgUsername string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	now := time.Now()
	s.tgBindMu.Lock()
	for k, v := range s.tgBindTickets {
		if now.After(v.Exp) {
			delete(s.tgBindTickets, k)
		}
	}
	s.tgBindTickets[tok] = tgBindTicket{TGID: tgID, TGUsername: tgUsername, Exp: now.Add(tgBindTicketTTL)}
	s.tgBindMu.Unlock()
	return tok
}

// peekTGBindTicket returns the ticket WITHOUT consuming it (so the bind page can
// show which Telegram account is about to be linked). ("", false) if missing/expired.
func (s *authStore) peekTGBindTicket(tok string) (tgBindTicket, bool) {
	s.tgBindMu.Lock()
	defer s.tgBindMu.Unlock()
	t, ok := s.tgBindTickets[tok]
	if !ok || time.Now().After(t.Exp) {
		return tgBindTicket{}, false
	}
	return t, true
}

// consumeTGBindTicket atomically reads and removes a ticket (single-use).
func (s *authStore) consumeTGBindTicket(tok string) (tgBindTicket, bool) {
	s.tgBindMu.Lock()
	defer s.tgBindMu.Unlock()
	t, ok := s.tgBindTickets[tok]
	if ok {
		delete(s.tgBindTickets, tok)
	}
	if !ok || time.Now().After(t.Exp) {
		return tgBindTicket{}, false
	}
	return t, true
}

// bootstrap creates the initial NOC operator with a random password and
// TOTP secret, then logs both prominently to stderr/journald so an operator
// can capture them on first boot.
func (s *authStore) bootstrap() error {
	pwBytes := make([]byte, 12)
	if _, err := rand.Read(pwBytes); err != nil {
		return err
	}
	pw := base64.RawURLEncoding.EncodeToString(pwBytes)

	pwHash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	totpBytes := make([]byte, 20)
	if _, err := rand.Read(totpBytes); err != nil {
		return err
	}
	b32 := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(totpBytes)

	plainCodes, codeHashes, err := generateRecoveryCodes(10)
	if err != nil {
		return err
	}

	op := operatorRecord{
		Username:      "NOC",
		PasswordHash:  string(pwHash),
		TOTPSecret:    b32,
		Role:          "operator",
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		RecoveryCodes: codeHashes,
	}

	s.mu.Lock()
	s.operators[op.Username] = op
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		return err
	}

	otpauth := fmt.Sprintf(
		"otpauth://totp/AcmeNet:%s?secret=%s&issuer=Acme%%20Cloud%%20Network&algorithm=SHA1&digits=6&period=30",
		op.Username, b32,
	)
	bar := strings.Repeat("=", 72)
	log.Println(bar)
	log.Println("  ACME NET · CORE CONSOLE · BOOTSTRAP CREDENTIALS — CAPTURE NOW")
	log.Println(bar)
	log.Printf("  OPERATOR : %s", op.Username)
	log.Printf("  PASSWORD : %s", pw)
	log.Printf("  TOTP_KEY : %s   (Base32, scan into Authenticator)", b32)
	log.Printf("  OTPAUTH  : %s", otpauth)
	log.Println(bar)
	log.Println("  RECOVERY CODES (each single-use; for 'Forgot password' on login page):")
	for i, c := range plainCodes {
		log.Printf("  [%2d/10]  %s", i+1, c)
	}
	log.Println(bar)
	log.Println("  ↑ secrets stored at " + operatorsPath + " — rotate by editing the file.")
	log.Println(bar)
	return nil
}

// ----------------------------------------------------------------------------
// Credential verification
// ----------------------------------------------------------------------------

// dummyHash is a real bcrypt output used to keep the unknown-user code path
// from completing noticeably faster than the known-user one.
var dummyHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

// Typed login errors. Surfaced through handleLogin to give the user
// actionable guidance instead of the catch-all "authentication failed".
// Username-enumeration safe: pre-password-verify failures (unknown user
// or wrong password) collapse to errLoginBadCredentials so an attacker
// can't tell whether the user exists. Post-password-verify failures get
// specific messages because by then the attacker already needed valid
// password to reach this branch.
var (
	errLoginBadCredentials  = errors.New("login: bad credentials")
	errLoginTOTPRequired    = errors.New("login: totp required")
	errLoginTOTPInvalid     = errors.New("login: totp invalid")
	errLoginPasskeyOnly     = errors.New("login: account uses passkey path; bind TOTP from a device with the passkey to enable password+TOTP")
	errLoginPendingApproval = errors.New("login: account pending admin approval")
)

func (s *authStore) verifyCredentials(username, password, code string) (*operatorRecord, error) {
	s.mu.RLock()
	op, ok := s.operators[username]
	s.mu.RUnlock()
	if !ok {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, errLoginBadCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(op.PasswordHash), []byte(password)); err != nil {
		return nil, errLoginBadCredentials
	}

	// === Dual-stack MFA policy ===
	//
	// Passkey path (/api/v1/auth/passkey/login/*) and password+TOTP path
	// (this function) are INDEPENDENT and parallel. Each factor is bound
	// separately at /admin/security; once bound, that path works.
	//
	//   1. Operator has TOTP bound → password+TOTP works. (Passkey may
	//      ALSO be bound — that path is unaffected.)
	//
	//   2. Operator has ONLY a passkey, no TOTP → password+TOTP is NOT a
	//      valid sign-in path on this account. Tell the user clearly so
	//      they can either (a) sign in with passkey on a device that has
	//      it, then bind TOTP at /admin/security, or (b) use the password
	//      recovery flow at /login if they've lost the passkey too.
	//
	//   3. Operator has NEITHER factor → first-login bootstrap. Allow
	//      password-only so they reach /admin/onboarding, which forces
	//      enrollment before any other action.
	hasTOTP := op.TOTPSecret != ""
	hasPasskey := len(op.Passkeys) > 0

	switch {
	case hasTOTP:
		if strings.TrimSpace(code) == "" {
			return nil, errLoginTOTPRequired
		}
		if !verifyTOTP(op.TOTPSecret, code, time.Now()) {
			return nil, errLoginTOTPInvalid
		}
	case hasPasskey:
		// Independent dual-stack: TOTP path is not enabled until TOTP
		// is bound. Direct user to passkey or the password-recovery flow.
		return nil, errLoginPasskeyOnly
	default:
		// No factors yet — fall through, Onboarding gate handles it.
	}

	if !op.Approved {
		return nil, errLoginPendingApproval
	}
	return &op, nil
}

// ----------------------------------------------------------------------------
// TOTP — RFC 6238, SHA1, 6 digits, 30s step. Accepts a ±1 step window.
// ----------------------------------------------------------------------------

func verifyTOTP(secretB32, code string, t time.Time) bool {
	code = strings.TrimSpace(strings.ReplaceAll(code, " ", ""))
	if len(code) != 6 {
		return false
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secretB32))
	if err != nil || len(key) == 0 {
		return false
	}
	counter := t.Unix() / 30
	for _, offset := range []int64{-1, 0, 1} {
		want := hotp(key, counter+offset)
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

func hotp(key []byte, counter int64) string {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(counter))
	mac := hmac.New(sha1.New, key)
	mac.Write(buf)
	h := mac.Sum(nil)
	offset := h[len(h)-1] & 0x0f
	truncated := binary.BigEndian.Uint32(h[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", truncated%1_000_000)
}

// ----------------------------------------------------------------------------
// Session token — payload.signature, both base64url-no-pad
// ----------------------------------------------------------------------------

type sessionClaims struct {
	Sub string `json:"sub"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
	Sid string `json:"sid"`
}

func (s *authStore) issueToken(username string) (token string, exp time.Time, err error) {
	sid := make([]byte, 9)
	if _, err = rand.Read(sid); err != nil {
		return "", time.Time{}, err
	}
	now := time.Now()
	exp = now.Add(sessionTTL)
	claims := sessionClaims{
		Sub: username,
		Iat: now.Unix(),
		Exp: exp.Unix(),
		Sid: base64.RawURLEncoding.EncodeToString(sid),
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig, exp, nil
}

func (s *authStore) verifyToken(token string) (*sessionClaims, error) {
	dot := strings.IndexByte(token, '.')
	if dot < 0 {
		return nil, errors.New("malformed token")
	}
	payload, sig := token[:dot], token[dot+1:]
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(sig)) != 1 {
		return nil, errors.New("bad signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, err
	}
	var c sessionClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if time.Now().Unix() > c.Exp {
		return nil, errors.New("expired")
	}
	return &c, nil
}

// ----------------------------------------------------------------------------
// Two-step login: intent ticket + device-trust cookie
// ----------------------------------------------------------------------------

// loginIntentClaims is what we encode into the short-lived HMAC-signed
// ticket cookie set between step 1 (password verify) and step 2 (TOTP
// verify). Stays small: only the username we already validated, and an
// expiry. Signature uses the same HMAC key as session tokens.
type loginIntentClaims struct {
	Sub string `json:"sub"`
	Exp int64  `json:"exp"`
	Jti string `json:"jti"` // random nonce; prevents same ticket replay across login attempts
}

func (s *authStore) issueLoginIntent(username string) (string, error) {
	nonce := make([]byte, 9)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	c := loginIntentClaims{
		Sub: username,
		Exp: time.Now().Add(loginIntentTTL).Unix(),
		Jti: base64.RawURLEncoding.EncodeToString(nonce),
	}
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("intent."))  // domain separator vs session token MAC
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig, nil
}

func (s *authStore) verifyLoginIntent(token string) (*loginIntentClaims, error) {
	dot := strings.IndexByte(token, '.')
	if dot < 0 {
		return nil, errors.New("malformed intent token")
	}
	payload, sig := token[:dot], token[dot+1:]
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("intent."))
	mac.Write([]byte(payload))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(sig)) != 1 {
		return nil, errors.New("bad intent signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, err
	}
	var c loginIntentClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if time.Now().Unix() > c.Exp {
		return nil, errors.New("intent expired")
	}
	return &c, nil
}

// checkDeviceTrust returns true (and bumps LastSeen* on a match) when
// the request carries an ncn_device_trust cookie whose value bcrypt-
// matches one of the operator's stored TrustedDevices entries. Stale
// (>deviceTrustTTL since LastSeenAt) entries are pruned in the same
// pass so the slice never grows unbounded for active operators.
func (s *authStore) checkDeviceTrust(r *http.Request, username string) bool {
	cookie, err := r.Cookie(deviceTrustCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.operators[username]
	if !ok || len(op.TrustedDevices) == 0 {
		return false
	}
	now := time.Now().Unix()
	cutoff := time.Now().Add(-deviceTrustTTL).Unix()
	matched := -1
	for i, d := range op.TrustedDevices {
		if d.LastSeenAt < cutoff {
			continue // stale entry — leave for prune below
		}
		if bcrypt.CompareHashAndPassword([]byte(d.Hash), []byte(cookie.Value)) == nil {
			matched = i
			break
		}
	}
	// Always prune stale entries opportunistically.
	pruned := op.TrustedDevices[:0]
	for _, d := range op.TrustedDevices {
		if d.LastSeenAt >= cutoff {
			pruned = append(pruned, d)
		}
	}
	op.TrustedDevices = pruned
	if matched >= 0 {
		op.TrustedDevices[matched].LastSeenAt = now
		op.TrustedDevices[matched].LastSeenIP = clientAddr(r)
		s.operators[username] = op
		// Persist async — keep the response path cheap. A crash before
		// flush only costs a slightly-stale LastSeenAt, no security impact.
		go func() {
			if err := s.persist(); err != nil {
				log.Printf("auth: device-trust LastSeen persist FAIL: %v", err)
			}
		}()
		return true
	}
	// Even if no match, persist the prune (rare write path).
	if len(pruned) != len(op.TrustedDevices) {
		s.operators[username] = op
		go func() { _ = s.persist() }()
	}
	return false
}

// registerTrustedDevice mints a fresh cookie token, stores its bcrypt
// hash on the operator's TrustedDevices list, sets the cookie on the
// response, and persists. Caller must already have validated TOTP.
func (s *authStore) registerTrustedDevice(w http.ResponseWriter, r *http.Request, username string) error {
	// 32 random bytes → 43-char base64url — enough entropy that bcrypt
	// brute-force is infeasible even with operators.json in attacker hands.
	tokenRaw := make([]byte, 32)
	if _, err := rand.Read(tokenRaw); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenRaw)
	hash, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	idRaw := make([]byte, 6)
	if _, err := rand.Read(idRaw); err != nil {
		return err
	}
	id := base64.RawURLEncoding.EncodeToString(idRaw) // 8 chars

	now := time.Now().Unix()
	td := trustedDevice{
		ID:         id,
		Hash:       string(hash),
		Label:      labelFromRequest(r), // reads Sec-CH-UA-* + UA regex
		UserAgent:  r.UserAgent(),
		CreatedAt:  now,
		LastSeenAt: now,
		LastSeenIP: clientAddr(r),
	}

	s.mu.Lock()
	op, ok := s.operators[username]
	if !ok {
		s.mu.Unlock()
		return errors.New("operator gone")
	}
	op.TrustedDevices = append(op.TrustedDevices, td)
	// Cap at maxTrustedDevices, evicting oldest (by LastSeenAt) first.
	if len(op.TrustedDevices) > maxTrustedDevices {
		// Sort by LastSeenAt asc; drop the head until we're under cap.
		// Simple O(n log n) since the list is small (≤ 16 + new = 17).
		sort.Slice(op.TrustedDevices, func(i, j int) bool {
			return op.TrustedDevices[i].LastSeenAt < op.TrustedDevices[j].LastSeenAt
		})
		op.TrustedDevices = op.TrustedDevices[len(op.TrustedDevices)-maxTrustedDevices:]
	}
	s.operators[username] = op
	s.mu.Unlock()

	if err := s.persist(); err != nil {
		log.Printf("auth: trusted device persist FAIL user=%q: %v", username, err)
		return err
	}

	secureCookie := r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil
	http.SetCookie(w, &http.Cookie{
		Name:     deviceTrustCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secureCookie,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(deviceTrustTTL),
	})
	log.Printf("auth: trusted device registered user=%q label=%q id=%s peer=%s",
		username, td.Label, id, clientAddr(r))
	auditRecord(r, AuditEvent{
		Event: "device.trust.add", Severity: auditSevWarn, Actor: username, Target: id,
		Details: map[string]any{"label": td.Label},
	})
	return nil
}

// ----------------------------------------------------------------------------
// Device label derivation — User-Agent regex + Client Hints headers.
// ----------------------------------------------------------------------------
//
// The label that appears in /admin/security's trusted-device list is
// composed from whichever signal is most accurate:
//
//   1. Sec-CH-UA-* Client Hints headers if the browser sent them (and
//      we previously responded with Accept-CH to opt in). These are
//      structurally typed — no regex guessing — so they win when present.
//
//   2. Fallback: regex over the User-Agent string. Covers all current
//      mainstream browsers (Chrome / Edge / Firefox / Safari) and all
//      mainstream OSes (Windows / macOS / Linux / iOS / Android), plus
//      the major Windows + macOS + iOS version names that turn raw
//      version strings into something readable ("Windows 11" not
//      "Windows NT 10.0").
//
// Example outputs:
//   "Chrome 130 · macOS 14"
//   "Safari 17 · iPhone (iOS 17.0)"
//   "Edge 130 · Windows 11"
//   "Chrome 130 · Pixel 9 (Android 14)"     // model from Sec-CH-UA-Model
//   "Firefox 131 · Linux"

var (
	reBrowserChrome  = regexp.MustCompile(`(?i)Chrome/(\d+)`)
	reBrowserEdge    = regexp.MustCompile(`(?i)Edg/(\d+)`)
	reBrowserFirefox = regexp.MustCompile(`(?i)Firefox/(\d+)`)
	reBrowserSafari  = regexp.MustCompile(`(?i)Version/(\d+)`) // user-facing Safari version
	reOSMacOS        = regexp.MustCompile(`(?i)Mac OS X (\d+)[_.](\d+)`)
	reOSWindows      = regexp.MustCompile(`(?i)Windows NT (\d+\.\d+)`)
	reOSiOS          = regexp.MustCompile(`(?i)OS (\d+)[_.](\d+)`)
	reOSAndroid      = regexp.MustCompile(`(?i)Android (\d+(?:\.\d+)?)`)
	reAndroidModel   = regexp.MustCompile(`(?i)Android [\d.]+; ([^;)]+?)\s*[)\\;]`)
)

// labelFromRequest is the public entry point: prefer Client Hints,
// fall back to UA regex. Result is a short human-readable string for
// the /admin/security devices list.
func labelFromRequest(r *http.Request) string {
	ua := r.UserAgent()

	// --- Browser ---
	browserName, browserVer := parseBrowserUA(ua)

	// Client Hints may carry a richer browser name (e.g. "Microsoft Edge")
	// — but they're more useful for OS and device. Keep UA-derived browser
	// for stable naming.

	// --- OS ---
	osName, osVer := parseOSUA(ua)
	// Sec-CH-UA-Platform overrides OS name when present and recognizable.
	if v := strings.Trim(r.Header.Get("Sec-CH-UA-Platform"), `"`); v != "" && v != "Unknown" {
		osName = v
	}
	// Sec-CH-UA-Platform-Version is a richer version — needs high-entropy
	// opt-in (Accept-CH response header). When present, prefer it.
	if v := strings.Trim(r.Header.Get("Sec-CH-UA-Platform-Version"), `"`); v != "" && v != "0.0.0" {
		osVer = v
	}
	// Post-process the version into a human-friendly form. For Windows
	// this is critical: Microsoft uses Sec-CH-UA-Platform-Version 13+
	// to flag Win11 (versus 1-10 for Win10), and the raw number reads
	// nothing like the marketing name. For macOS it suppresses the
	// frozen UA-derived "10" and prefers the real version from hints.
	osVer = humanizeOSVersion(osName, osVer)

	// --- Device model (only meaningful on mobile) ---
	// Chrome on Android exposes the device model via Sec-CH-UA-Model after
	// Accept-CH opt-in. Falls back to UA-extracted model token.
	model := strings.Trim(r.Header.Get("Sec-CH-UA-Model"), `"`)
	if model == "" {
		model = parseModelUA(ua)
	}

	// --- Compose ---
	var sb strings.Builder
	if browserName != "" {
		sb.WriteString(browserName)
		if browserVer != "" {
			sb.WriteByte(' ')
			sb.WriteString(browserVer)
		}
	} else {
		sb.WriteString("Unknown browser")
	}
	sb.WriteString(" · ")
	switch {
	case model != "":
		// e.g. "Pixel 9 (Android 14)"
		sb.WriteString(model)
		if osName != "" {
			sb.WriteString(" (")
			sb.WriteString(osName)
			if osVer != "" {
				sb.WriteByte(' ')
				sb.WriteString(osVer)
			}
			sb.WriteByte(')')
		}
	case osName != "":
		sb.WriteString(osName)
		if osVer != "" && shouldShowOSVersion(osName) {
			sb.WriteByte(' ')
			sb.WriteString(osVer)
		}
	default:
		sb.WriteString("Unknown OS")
	}
	return sb.String()
}

func parseBrowserUA(ua string) (name, ver string) {
	switch {
	case reBrowserEdge.MatchString(ua):
		return "Edge", firstSubmatch(reBrowserEdge, ua)
	case reBrowserChrome.MatchString(ua) && !strings.Contains(ua, "Edg/"):
		return "Chrome", firstSubmatch(reBrowserChrome, ua)
	case reBrowserFirefox.MatchString(ua):
		return "Firefox", firstSubmatch(reBrowserFirefox, ua)
	case strings.Contains(ua, "Safari/") && !strings.Contains(ua, "Chrome/"):
		// Safari major version lives in Version/N — not Safari/N (which is
		// WebKit-build flavored).
		if v := firstSubmatch(reBrowserSafari, ua); v != "" {
			return "Safari", v
		}
		return "Safari", ""
	}
	return "", ""
}

func parseOSUA(ua string) (name, ver string) {
	switch {
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad"):
		// iPhone/iPad UA contains "iPhone OS 17_0 like Mac OS X" — the
		// "OS X" tail trips the macOS branch otherwise, so this case wins.
		m := reOSiOS.FindStringSubmatch(ua)
		if len(m) >= 3 {
			return "iOS", m[1] + "." + m[2]
		}
		return "iOS", ""
	case strings.Contains(ua, "Android"):
		return "Android", firstSubmatch(reOSAndroid, ua)
	case strings.Contains(ua, "Mac OS X"):
		m := reOSMacOS.FindStringSubmatch(ua)
		if len(m) >= 3 {
			// macOS dropped "X" branding at 11; the UA still says 10_15_7
			// for older Macs and frozen "10_15_7" for newer ones (Apple
			// pins the legacy string). Best we can show without high-
			// entropy hints is the raw major from UA.
			return "macOS", m[1]
		}
		return "macOS", ""
	case strings.Contains(ua, "Windows NT"):
		// Map "Windows NT X.Y" to friendlier names.
		raw := firstSubmatch(reOSWindows, ua)
		return "Windows", mapWindowsNTVersion(raw)
	case strings.Contains(ua, "Linux"):
		return "Linux", ""
	}
	return "", ""
}

// parseModelUA pulls a device model out of the typical Android UA shape:
//
//	"Mozilla/5.0 (Linux; Android 14; Pixel 9) AppleWebKit/..."
//
// Returns "" when the UA doesn't include a recognizable model token.
func parseModelUA(ua string) string {
	m := reAndroidModel.FindStringSubmatch(ua)
	if len(m) >= 2 {
		// Trim the "Build/xxxx" suffix that some OEMs append.
		s := strings.SplitN(m[1], " Build/", 2)[0]
		return strings.TrimSpace(s)
	}
	if strings.Contains(ua, "iPhone") {
		return "iPhone"
	}
	if strings.Contains(ua, "iPad") {
		return "iPad"
	}
	return ""
}

func firstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

// mapWindowsNTVersion translates the kernel version Windows reports in
// its UA ("10.0", "6.3", etc.) into the consumer name ("11", "10", "8.1").
// 10.0 covers both Windows 10 AND Windows 11 — Microsoft never bumped
// the UA, so we can't tell them apart from the kernel string alone.
// Sec-CH-UA-Platform-Version (high-entropy) returns "13.0.0" or "10.0.0"
// for 11 vs 10 — that's the only way to disambiguate.
func mapWindowsNTVersion(nt string) string {
	switch nt {
	case "10.0":
		return "10/11"
	case "6.3":
		return "8.1"
	case "6.2":
		return "8"
	case "6.1":
		return "7"
	}
	return nt
}

// shouldShowOSVersion controls whether the parsed version string adds
// information worth showing. For Linux we don't know the distro version
// from UA at all, so suppress; for the rest, show.
func shouldShowOSVersion(name string) bool {
	return name != "Linux"
}

// humanizeOSVersion translates a raw OS version string (either UA-derived
// or from Sec-CH-UA-Platform-Version) into something a normal person
// would recognize on the trusted-device list.
//
//   Windows: Microsoft's Sec-CH-UA-Platform-Version uses the kernel
//            build number, not the marketing name. 13.0.0+ ⇒ Win11,
//            1.0.0–10.x.x ⇒ Win10, 0.0.0 ⇒ Win7/8/8.1 (ambiguous).
//            UA-derived legacy fallback already mapped by
//            mapWindowsNTVersion before reaching here.
//
//   macOS:   UA-derived version is frozen at "10" for newer Macs (Apple
//            stopped bumping it after 10.15.7). Sec-CH-UA gives the
//            real version (e.g. "14.6.1") — when present, show first
//            two segments. Otherwise suppress the misleading "10".
//
//   Other:   Pass through (Android / iOS versions are accurate as-is).
func humanizeOSVersion(osName, ver string) string {
	if ver == "" {
		return ""
	}
	switch osName {
	case "Windows":
		major := strings.SplitN(ver, ".", 2)[0]
		if n, err := strconv.Atoi(major); err == nil {
			switch {
			case n >= 13: return "11"
			case n >= 1:  return "10"
			case n == 0:  return ""  // ambiguous 7/8/8.1 — skip
			}
		}
		return ver  // fallback (e.g. legacy NT-mapped strings like "10/11")
	case "macOS":
		// Frozen UA-derived "10" is meaningless — suppress.
		if ver == "10" {
			return ""
		}
		// "14.6.1" → "14.6"; "14" → "14"
		parts := strings.SplitN(ver, ".", 3)
		if len(parts) >= 2 {
			return parts[0] + "." + parts[1]
		}
		return parts[0]
	}
	return ver
}

// ----------------------------------------------------------------------------
// HTTP handlers
// ----------------------------------------------------------------------------

type loginReq struct {
	Username       string `json:"username"`
	Password       string `json:"password"`
	TOTPCode       string `json:"totp_code"`       // legacy; new flow leaves this empty
	TurnstileToken string `json:"turnstile_token"` // CF Turnstile widget output (frontend-collected)
}

// loginVerifyTOTPReq is the body of /api/v1/auth/login/verify-totp.
// The intent ticket cookie (ncn_login_intent) is what binds this step
// to a prior successful password verify; the body just carries the TOTP
// value the operator typed and whether to remember the device.
type loginVerifyTOTPReq struct {
	TOTPCode    string `json:"totp_code"`
	TrustDevice bool   `json:"trust_device"`
}

// handleLogin — Step 1 of the two-step flow.
//
//   1. Verify username + password.
//   2. Branch on account state:
//        a. No factors bound  → bootstrap path; issue session immediately
//           (Onboarding gate forces factor enrollment before any other action).
//        b. Passkey only      → refuse; user must sign in via passkey or
//           use the recovery flow.
//        c. TOTP bound        → check device-trust cookie:
//             • Match  → issue session immediately, skip step 2.
//             • Miss   → set short-lived intent ticket cookie, return
//               { ok:false, totp_required:true }. Frontend then calls
//               /api/v1/auth/login/verify-totp to finish.
//
// Backward compat: if a (legacy) caller posts totp_code in the same body,
// we accept that — the existing single-shot path keeps working for any
// scripted clients still in the wild.
func (s *authStore) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	// Opt in to high-entropy User-Agent Client Hints on FUTURE requests
	// from this client. Chromium-based browsers see this and start
	// sending Sec-CH-UA-Platform-Version, Sec-CH-UA-Model, etc., which
	// lets labelFromRequest produce richer device labels like
	// "Chrome 130 · Pixel 9 (Android 14)" instead of just "Chrome on Linux".
	// No effect on Firefox/Safari which don't implement Client Hints,
	// but harmless — they ignore the header.
	w.Header().Set("Accept-CH", "Sec-CH-UA, Sec-CH-UA-Platform, Sec-CH-UA-Platform-Version, Sec-CH-UA-Model, Sec-CH-UA-Mobile, Sec-CH-UA-Full-Version-List")
	w.Header().Set("Critical-CH", "Sec-CH-UA-Platform-Version, Sec-CH-UA-Model")

	// Body limit needs headroom for the Cloudflare Turnstile token, which
	// commonly runs 2-3 KB. The legacy 1<<10 (1 KB) cap silently truncated
	// the JSON in transit → "bad json" decode failure → user saw
	// "鉴权失败 bad json". 16 KB is generous and matches what we use on
	// passkey endpoints (which carry attestation blobs of similar size).
	var req loginReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}

	// --- Cloudflare Turnstile gate (front-line bot protection) ---
	// Verifies BEFORE we touch the operator table, so failed challenges
	// don't even probe whether a username exists. If the secret isn't
	// configured on this host (e.g. dev / disabled), verifyTurnstileToken
	// returns nil and we proceed. Errors are deliberately vague to the
	// client — we don't want to leak the underlying CF error code.
	if err := verifyTurnstileToken(r.Context(), req.TurnstileToken, clientAddr(r)); err != nil {
		log.Printf("login: turnstile rejected for username=%q: %v", req.Username, err)
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false,
			Error: "human verification failed — please retry the check"})
		return
	}

	// --- Password verify (always required) ---
	s.mu.RLock()
	op, ok := s.operators[req.Username]
	s.mu.RUnlock()
	if !ok {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password))
		s.rejectLogin(w, r, req.Username, errLoginBadCredentials)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(op.PasswordHash), []byte(req.Password)); err != nil {
		s.rejectLogin(w, r, req.Username, errLoginBadCredentials)
		return
	}

	hasTOTP := op.TOTPSecret != ""
	hasPasskey := len(op.Passkeys) > 0

	// --- Branch on account state ---
	switch {
	case !hasTOTP && !hasPasskey:
		// Bootstrap path — no factors yet. Allow session so user can
		// reach /admin/onboarding (router gate forces enrollment).
		if !op.Approved {
			s.rejectLogin(w, r, req.Username, errLoginPendingApproval)
			return
		}
		auditRecord(r, AuditEvent{
			Event: "login.ok", Severity: auditSevWarn, Actor: req.Username,
			Details: map[string]any{"path": "bootstrap-no-mfa", "role": op.Role},
		})
		s.issueAndSetSession(w, r, &op)
		return

	case !hasTOTP && hasPasskey:
		s.rejectLogin(w, r, req.Username, errLoginPasskeyOnly)
		return
	}

	// hasTOTP must be true here. Approval check applies to TOTP-path too.
	if !op.Approved {
		s.rejectLogin(w, r, req.Username, errLoginPendingApproval)
		return
	}

	// --- Legacy single-shot: totp_code in body → verify inline, no ticket ---
	if strings.TrimSpace(req.TOTPCode) != "" {
		if !verifyTOTP(op.TOTPSecret, req.TOTPCode, time.Now()) {
			s.rejectLogin(w, r, req.Username, errLoginTOTPInvalid)
			return
		}
		auditRecord(r, AuditEvent{
			Event: "login.ok", Actor: req.Username,
			Details: map[string]any{"path": "legacy-single-shot", "role": op.Role},
		})
		s.issueAndSetSession(w, r, &op)
		return
	}

	// --- Two-step flow: check device trust ---
	if s.checkDeviceTrust(r, req.Username) {
		log.Printf("auth: login OK (trusted device) user=%q peer=%s", req.Username, clientAddr(r))
		auditRecord(r, AuditEvent{
			Event: "login.ok", Actor: req.Username,
			Details: map[string]any{"path": "trusted-device", "role": op.Role},
		})
		s.issueAndSetSession(w, r, &op)
		return
	}

	// --- Untrusted device: issue intent ticket, ask client for TOTP ---
	intent, err := s.issueLoginIntent(req.Username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "intent issuance failed"})
		return
	}
	secureCookie := r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil
	http.SetCookie(w, &http.Cookie{
		Name:     loginIntentCookie,
		Value:    intent,
		Path:     "/api/v1/auth", // only sent to the auth endpoints
		HttpOnly: true,
		Secure:   secureCookie,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(loginIntentTTL),
	})
	log.Printf("auth: login STEP1 OK user=%q peer=%s (totp required)", req.Username, clientAddr(r))
	auditRecord(r, AuditEvent{
		Event: "login.step1", Actor: req.Username,
		Details: map[string]any{"totp_required": true},
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"operator":        req.Username,
		"totp_required":   true,
		"intent_expires":  time.Now().Add(loginIntentTTL).Unix(),
	}})
}

// handleLoginVerifyTOTP — Step 2 of the two-step flow.
// Consumes the intent ticket cookie, verifies TOTP against the
// operator referenced in the ticket, optionally registers the browser
// as a trusted device, and issues a session cookie.
func (s *authStore) handleLoginVerifyTOTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}

	// Intent ticket — proves the caller already passed step 1 (password OK).
	cookie, err := r.Cookie(loginIntentCookie)
	if err != nil || cookie.Value == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "no login intent — submit username/password first"})
		return
	}
	claims, err := s.verifyLoginIntent(cookie.Value)
	if err != nil {
		// Wipe the bad/expired intent cookie regardless of why it's bad.
		http.SetCookie(w, &http.Cookie{Name: loginIntentCookie, Value: "", Path: "/api/v1/auth", MaxAge: -1})
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "login intent expired — start over"})
		return
	}

	var req loginVerifyTOTPReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}

	s.mu.RLock()
	op, ok := s.operators[claims.Sub]
	s.mu.RUnlock()
	if !ok || op.TOTPSecret == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "account state changed — sign in again"})
		return
	}

	if !verifyTOTP(op.TOTPSecret, req.TOTPCode, time.Now()) {
		log.Printf("auth: login STEP2 FAIL (totp invalid) user=%q peer=%s", claims.Sub, clientAddr(r))
		auditRecord(r, AuditEvent{
			Event: "login.fail", Severity: auditSevWarn, Actor: claims.Sub, Outcome: "fail",
			Details: map[string]any{"step": 2, "reason": "totp-invalid"},
		})
		time.Sleep(500 * time.Millisecond)
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "TOTP code didn't match — check your authenticator's clock"})
		return
	}

	// Optionally remember this device. Best-effort: a failure here is
	// logged but doesn't block the actual login.
	if req.TrustDevice {
		if err := s.registerTrustedDevice(w, r, claims.Sub); err != nil {
			log.Printf("auth: trust-device register FAIL user=%q peer=%s: %v",
				claims.Sub, clientAddr(r), err)
		}
	}

	// Consume the intent cookie (one-shot ticket).
	http.SetCookie(w, &http.Cookie{Name: loginIntentCookie, Value: "", Path: "/api/v1/auth", MaxAge: -1})

	log.Printf("auth: login STEP2 OK user=%q peer=%s trust=%v", claims.Sub, clientAddr(r), req.TrustDevice)
	auditRecord(r, AuditEvent{
		Event: "login.ok", Actor: claims.Sub,
		Details: map[string]any{"path": "totp", "trust_device": req.TrustDevice, "role": op.Role},
	})
	s.issueAndSetSession(w, r, &op)
}

// rejectLogin is the unified error response. Logs the detailed reason
// for the audit trail and returns a sanitized user-facing message.
func (s *authStore) rejectLogin(w http.ResponseWriter, r *http.Request, username string, err error) {
	log.Printf("auth: login FAIL user=%q peer=%s: %v", username, clientAddr(r), err)
	auditRecord(r, AuditEvent{
		Event: "login.fail", Severity: auditSevWarn, Actor: username, Outcome: "fail",
		Details: map[string]any{"reason": err.Error()},
	})
	time.Sleep(500 * time.Millisecond) // brute-force slowdown
	msg := "authentication failed"
	switch {
	case errors.Is(err, errLoginBadCredentials):
		msg = "invalid username or password"
	case errors.Is(err, errLoginTOTPRequired):
		msg = "enter your 6-digit TOTP code"
	case errors.Is(err, errLoginTOTPInvalid):
		msg = "TOTP code didn't match — check your authenticator's clock"
	case errors.Is(err, errLoginPasskeyOnly):
		msg = "this account hasn't bound TOTP yet — sign in with passkey on a device that has it, then bind TOTP at /admin/security"
	case errors.Is(err, errLoginPendingApproval):
		msg = "account pending admin approval"
	}
	writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: msg})
}

// issueAndSetSession factors the "mint session token + Set-Cookie + JSON
// envelope" steps so handleLogin and handleLoginVerifyTOTP both go
// through the exact same path.
func (s *authStore) issueAndSetSession(w http.ResponseWriter, r *http.Request, op *operatorRecord) {
	exp, err := s.setSessionCookie(w, r, op.Username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "session issuance failed"})
		return
	}
	log.Printf("auth: login OK user=%q peer=%s exp=%s", op.Username, clientAddr(r), exp.Format(time.RFC3339))
	// Note: this is the issueAndSetSession path used by both password-only
	// and post-step2. We don't audit here to avoid double-counting (caller
	// already audited "login.ok" with the correct path tag).
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"operator":   op.Username,
		"role":       op.Role,
		"issued_at":  time.Now().Unix(),
		"expires_at": exp.Unix(),
	}})
}

// setSessionCookie mints + sets a session cookie for `username`. Returns
// the cookie's expiry so the caller can include it in the JSON / log.
// Used by the password path (issueAndSetSession), passkey-finish, and
// SSO ingest (auth_sso.go).
func (s *authStore) setSessionCookie(w http.ResponseWriter, r *http.Request, username string) (time.Time, error) {
	token, exp, err := s.issueToken(username)
	if err != nil {
		return time.Time{}, err
	}
	secureCookie := r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secureCookie,
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
	})
	return exp, nil
}

func (s *authStore) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	if c, ok := r.Context().Value(ctxKeyAuth).(*sessionClaims); ok && c != nil {
		log.Printf("auth: logout user=%q sid=%s", c.Sub, c.Sid)
		auditRecord(r, AuditEvent{
			Event: "logout", Actor: c.Sub,
			Details: map[string]any{"sid": c.Sid},
		})
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]string{"status": "logged_out"}})
}

func (s *authStore) handleMe(w http.ResponseWriter, r *http.Request) {
	c, ok := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if !ok || c == nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	role := ""
	mfaRequired := true
	hasPasskey := false
	hasTOTP := false
	avatarURL := ""
	identities := []map[string]string{}
	s.mu.RLock()
	if op, exists := s.operators[c.Sub]; exists {
		role = op.Role
		avatarURL = op.AvatarURL
		hasPasskey = len(op.Passkeys) > 0
		hasTOTP = op.TOTPSecret != ""
		// MFA is satisfied if EITHER a passkey is registered OR a TOTP
		// secret is set. Both empty = first-login state, frontend forces
		// the operator to /admin/onboarding before granting access.
		mfaRequired = !hasPasskey && !hasTOTP
		for _, ei := range op.ExternalIdentities {
			identities = append(identities, map[string]string{"provider": ei.Provider, "email": ei.Email, "bound_at": ei.BoundAt})
		}
	}
	s.mu.RUnlock()
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"operator":           c.Sub,
		"role":               role,
		"issued_at":          c.Iat,
		"expires_at":         c.Exp,
		"session_id":         c.Sid,
		"ttl_seconds":        c.Exp - time.Now().Unix(),
		"mfa_required":       mfaRequired,
		"has_passkey":        hasPasskey,
		"has_totp":           hasTOTP,
		"avatar_url":         avatarURL,
		"external_identities": identities,
	}})
}

// ----------------------------------------------------------------------------
// Trusted-device management (admin Security page)
// ----------------------------------------------------------------------------

// trustedDeviceView is the public shape returned to /admin/security —
// strips the bcrypt hash (which never leaves the server) and adds a
// `current` flag so the UI can mark "this device" in the list.
type trustedDeviceView struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	UserAgent  string `json:"user_agent,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	LastSeenIP string `json:"last_seen_ip,omitempty"`
	Current    bool   `json:"current,omitempty"`
}

// handleListDevices — GET /api/v1/auth/devices
// Returns the calling operator's trusted devices (without hashes).
func (s *authStore) handleListDevices(w http.ResponseWriter, r *http.Request) {
	c, ok := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if !ok || c == nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	// Cookie token (if present) lets us flag the row that represents
	// "this browser" so the UI can render a self-marker.
	var currentTok string
	if dt, err := r.Cookie(deviceTrustCookie); err == nil {
		currentTok = dt.Value
	}

	s.mu.RLock()
	op, exists := s.operators[c.Sub]
	s.mu.RUnlock()
	if !exists {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "operator gone"})
		return
	}

	out := make([]trustedDeviceView, 0, len(op.TrustedDevices))
	for _, d := range op.TrustedDevices {
		current := false
		if currentTok != "" {
			if bcrypt.CompareHashAndPassword([]byte(d.Hash), []byte(currentTok)) == nil {
				current = true
			}
		}
		out = append(out, trustedDeviceView{
			ID:         d.ID,
			Label:      d.Label,
			UserAgent:  d.UserAgent,
			CreatedAt:  d.CreatedAt,
			LastSeenAt: d.LastSeenAt,
			LastSeenIP: d.LastSeenIP,
			Current:    current,
		})
	}
	// Newest first
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeenAt > out[j].LastSeenAt })
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"devices": out,
	}})
}

// handleRevokeDevice — DELETE /api/v1/auth/devices?id=<id>
// Removes one trusted-device entry by ID. If the operator revokes
// their CURRENT device, we also clear the cookie on the response so
// the next page load forces them through the TOTP step.
func (s *authStore) handleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "DELETE only"})
		return
	}
	c, ok := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if !ok || c == nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing id"})
		return
	}

	// Read the caller's current cookie token before we mutate, so we
	// can detect "you just revoked the device you're on" and clear it.
	var currentTok string
	if dt, err := r.Cookie(deviceTrustCookie); err == nil {
		currentTok = dt.Value
	}
	wasCurrent := false

	s.mu.Lock()
	op, exists := s.operators[c.Sub]
	if !exists {
		s.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "operator gone"})
		return
	}
	kept := op.TrustedDevices[:0]
	for _, d := range op.TrustedDevices {
		if d.ID == id {
			if currentTok != "" && bcrypt.CompareHashAndPassword([]byte(d.Hash), []byte(currentTok)) == nil {
				wasCurrent = true
			}
			continue
		}
		kept = append(kept, d)
	}
	op.TrustedDevices = kept
	s.operators[c.Sub] = op
	s.mu.Unlock()

	if err := s.persist(); err != nil {
		log.Printf("auth: device revoke persist FAIL user=%q: %v", c.Sub, err)
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "persist failed"})
		return
	}

	if wasCurrent {
		http.SetCookie(w, &http.Cookie{
			Name: deviceTrustCookie, Value: "", Path: "/", MaxAge: -1,
		})
	}
	log.Printf("auth: device revoke OK user=%q id=%s current=%v", c.Sub, id, wasCurrent)
	auditRecord(r, AuditEvent{
		Event: "device.trust.revoke", Severity: auditSevWarn, Actor: c.Sub, Target: id,
		Details: map[string]any{"was_current": wasCurrent},
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"removed": id, "was_current": wasCurrent}})
}

// ----------------------------------------------------------------------------
// Middleware
// ----------------------------------------------------------------------------

type ctxKey int

const ctxKeyAuth ctxKey = 1

func (s *authStore) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Authorization: Bearer <api-token> — script / CLI use.
		//    Tried first because it's an explicit header, not the
		//    implicit "I had a browser session lying around" path.
		//    API tokens grant the same operator identity the session
		//    would; downstream handlers see the same sessionClaims.
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			tok := strings.TrimPrefix(h, "Bearer ")
			if claims, err := s.verifyAPIToken(tok); err == nil {
				ctx := context.WithValue(r.Context(), ctxKeyAuth, claims)
				next(w, r.WithContext(ctx))
				return
			}
			// Bad token → 401 immediately, don't fall through to
			// the cookie check (a script with a wrong header
			// shouldn't accidentally borrow the user's browser
			// session if both happened to be present).
			writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "invalid api token"})
			return
		}
		// 2. Session cookie — browser path (the default).
		c, err := r.Cookie(cookieName)
		if err != nil || c.Value == "" {
			writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
			return
		}
		claims, err := s.verifyToken(c.Value)
		if err != nil {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
			writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "session invalid: " + err.Error()})
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyAuth, claims)
		next(w, r.WithContext(ctx))
	}
}

// ----------------------------------------------------------------------------
// Forgot password — reset via one-time recovery code
// ----------------------------------------------------------------------------

type resetRequest struct {
	Username     string `json:"username"`
	RecoveryCode string `json:"recovery_code"`
	NewPassword  string `json:"new_password"`
}

// POST /api/v1/auth/recover · public; rate-limited at the route layer.
func (s *authStore) handleRecoverPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var req resetRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.RecoveryCode = strings.ToUpper(strings.TrimSpace(req.RecoveryCode))
	if len(req.NewPassword) < 8 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "new password must be ≥ 8 chars"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	op, exists := s.operators[req.Username]
	if !exists {
		// Constant-time-ish defense against username enumeration
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(req.RecoveryCode))
		time.Sleep(500 * time.Millisecond)
		log.Printf("auth: recovery FAIL user=%q peer=%s: unknown user", req.Username, clientAddr(r))
		auditRecord(r, AuditEvent{
			Event: "recovery.fail", Severity: auditSevWarn, Actor: req.Username, Outcome: "fail",
			Details: map[string]any{"reason": "unknown-user"},
		})
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "invalid credentials"})
		return
	}

	if len(op.RecoveryCodes) == 0 {
		log.Printf("auth: recovery FAIL user=%q peer=%s: no codes provisioned", req.Username, clientAddr(r))
		auditRecord(r, AuditEvent{
			Event: "recovery.fail", Severity: auditSevWarn, Actor: req.Username, Outcome: "fail",
			Details: map[string]any{"reason": "no-codes-provisioned"},
		})
		writeJSON(w, http.StatusForbidden, envelope{OK: false, Error: "no recovery codes provisioned for this account"})
		return
	}

	// Find which code (if any) matches; consume it.
	matched := -1
	for i, h := range op.RecoveryCodes {
		if bcrypt.CompareHashAndPassword([]byte(h), []byte(req.RecoveryCode)) == nil {
			matched = i
			break
		}
	}
	if matched < 0 {
		time.Sleep(500 * time.Millisecond)
		log.Printf("auth: recovery FAIL user=%q peer=%s: code mismatch", req.Username, clientAddr(r))
		auditRecord(r, AuditEvent{
			Event: "recovery.fail", Severity: auditSevWarn, Actor: req.Username, Outcome: "fail",
			Details: map[string]any{"reason": "code-mismatch"},
		})
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "invalid recovery code"})
		return
	}

	// Hash new password
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "hash failed"})
		return
	}

	// Consume used code (remove from slice).
	op.PasswordHash = string(newHash)
	op.RecoveryCodes = append(op.RecoveryCodes[:matched], op.RecoveryCodes[matched+1:]...)
	s.operators[req.Username] = op

	// Inline persist (we hold write lock).
	ops := make([]operatorRecord, 0, len(s.operators))
	for _, o := range s.operators {
		ops = append(ops, o)
	}
	data, _ := json.MarshalIndent(ops, "", "  ")
	if err := writeOperatorsBlob(data); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "persist: " + err.Error()})
		return
	}

	log.Printf("auth: recovery OK user=%q peer=%s · remaining_codes=%d", req.Username, clientAddr(r), len(op.RecoveryCodes))
	auditRecord(r, AuditEvent{
		Event: "recovery.ok", Severity: auditSevCritical, Actor: req.Username, Target: req.Username,
		Details: map[string]any{"remaining_codes": len(op.RecoveryCodes)},
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"operator":         req.Username,
		"remaining_codes":  len(op.RecoveryCodes),
	}})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// POST /api/v1/auth/change-password · authenticated. Rotates the operator's
// own password. Requires the current password for proof-of-presence
// (defense against session-hijack chained password takeover) and a new
// password ≥ 8 chars. Does NOT touch recovery codes — those keep working.
func (s *authStore) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	claims, ok := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if !ok || claims == nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}

	var req changePasswordRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	if len(req.NewPassword) < 8 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "new password must be ≥ 8 chars"})
		return
	}
	if req.CurrentPassword == req.NewPassword {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "new password must differ from current"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	op, exists := s.operators[claims.Sub]
	if !exists {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "operator not found"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(op.PasswordHash), []byte(req.CurrentPassword)); err != nil {
		// Slow down brute-force from a stolen session.
		time.Sleep(500 * time.Millisecond)
		log.Printf("auth: change-password FAIL user=%q peer=%s: current password mismatch", claims.Sub, clientAddr(r))
		auditRecord(r, AuditEvent{
			Event: "password.change.fail", Severity: auditSevWarn, Actor: claims.Sub, Outcome: "fail",
			Details: map[string]any{"reason": "current-password-mismatch"},
		})
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "current password incorrect"})
		return
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "hash failed"})
		return
	}
	op.PasswordHash = string(newHash)
	s.operators[claims.Sub] = op

	// Inline persist — we hold the write lock; persist() would re-acquire it.
	ops := make([]operatorRecord, 0, len(s.operators))
	for _, o := range s.operators {
		ops = append(ops, o)
	}
	data, _ := json.MarshalIndent(ops, "", "  ")
	if err := writeOperatorsBlob(data); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "persist: " + err.Error()})
		return
	}

	log.Printf("auth: password changed user=%q peer=%s", claims.Sub, clientAddr(r))
	auditRecord(r, AuditEvent{
		Event: "password.change", Severity: auditSevCritical, Actor: claims.Sub, Target: claims.Sub,
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"operator":   claims.Sub,
		"changed_at": time.Now().UTC().Format(time.RFC3339),
	}})
}

// GET /api/v1/auth/recovery-status · returns remaining recovery codes count for
// the authenticated operator (auth required; doesn't leak count without login).
func (s *authStore) handleRecoveryStatus(w http.ResponseWriter, r *http.Request) {
	c, ok := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if !ok || c == nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	s.mu.RLock()
	op, exists := s.operators[c.Sub]
	s.mu.RUnlock()
	if !exists {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "operator not found"})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]int{
		"remaining": len(op.RecoveryCodes),
	}})
}

// ============================================================================
// Operator account management
//
// Roles:
//   "admin"     — can list/create/delete other operators in addition to all
//                 normal operator capabilities.
//   "operator"  — full read access, can change own password, manage own
//                 passkeys/recovery codes, open terminals, run sensitive
//                 ops with per-op confirmation. CANNOT manage other accounts.
//
// Bootstrap: the very first operator (created via bootstrap()) gets `admin`.
// Existing deployments with role="operator" on the single bootstrap account
// are auto-promoted to `admin` on startup so we don't lock anyone out.
// ============================================================================

const (
	roleAdmin    = "admin"
	roleOperator = "operator"
)

// requireRole wraps requireAuth and adds a role check. Returns 403 if the
// authenticated operator's stored role doesn't match.
func (s *authStore) requireRole(role string, next http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)
		s.mu.RLock()
		op, ok := s.operators[c.Sub]
		s.mu.RUnlock()
		if !ok || op.Role != role {
			writeJSON(w, http.StatusForbidden, envelope{OK: false, Error: "insufficient role"})
			return
		}
		next(w, r)
	})
}

// promoteFirstAdminIfNone ensures at least one admin exists. If no operator
// has role=admin we elevate the alphabetically-first one. Called once on
// startup from main.
func (s *authStore) promoteFirstAdminIfNone() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, op := range s.operators {
		if op.Role == roleAdmin {
			return // already have one
		}
	}
	if len(s.operators) == 0 {
		return // nothing to promote
	}
	// Pick the bootstrap operator deterministically.
	names := make([]string, 0, len(s.operators))
	for n := range s.operators {
		names = append(names, n)
	}
	sort.Strings(names)
	first := names[0]
	op := s.operators[first]
	op.Role = roleAdmin
	s.operators[first] = op
	log.Printf("auth: bootstrap admin promoted user=%q (no admin existed)", first)
	// Inline persist (we hold the write lock).
	ops := make([]operatorRecord, 0, len(s.operators))
	for _, o := range s.operators {
		ops = append(ops, o)
	}
	data, _ := json.MarshalIndent(ops, "", "  ")
	_ = writeOperatorsBlob(data)
}

// GET /api/v1/auth/operators — list every operator. Visible to ANY
// authenticated session (transparency about who has access). Sensitive
// fields are stripped.
func (s *authStore) handleOperatorsList(w http.ResponseWriter, r *http.Request) {
	type view struct {
		Username        string `json:"username"`
		Role            string `json:"role"`
		CreatedAt       string `json:"created_at"`
		RecoveryRemain  int    `json:"recovery_remaining"`
		PasskeysCount   int    `json:"passkeys_count"`
		HasTOTP         bool   `json:"has_totp"`
		Approved        bool   `json:"approved"`
		InvitedBy       string `json:"invited_by,omitempty"`
		InvitedAt       string `json:"invited_at,omitempty"`
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]view, 0, len(s.operators))
	for _, op := range s.operators {
		out = append(out, view{
			Username:       op.Username,
			Role:           op.Role,
			CreatedAt:      op.CreatedAt,
			RecoveryRemain: len(op.RecoveryCodes),
			PasskeysCount:  len(op.Passkeys),
			HasTOTP:        op.TOTPSecret != "",
			Approved:       op.Approved,
			InvitedBy:      op.InvitedBy,
			InvitedAt:      op.InvitedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
}

// POST /api/v1/auth/operators — admin only. Creates a new operator with an
// optional caller-provided password (if omitted, a random one is generated
// and returned in the response — ONE-TIME display). 10 fresh recovery codes
// are also generated and returned plaintext (also one-time).
type createOperatorReq struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	Password string `json:"password,omitempty"`
}
type createOperatorResp struct {
	Username      string   `json:"username"`
	Role          string   `json:"role"`
	Password      string   `json:"password"`
	RecoveryCodes []string `json:"recovery_codes"`
	CreatedAt     string   `json:"created_at"`
}

func (s *authStore) handleOperatorsCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	caller, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)

	var req createOperatorReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !isValidUsername(req.Username) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: "username must be 2-32 chars, [A-Za-z0-9._-]"})
		return
	}
	if req.Role == "" {
		req.Role = roleOperator
	}
	if req.Role != roleAdmin && req.Role != roleOperator {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "role must be admin|operator"})
		return
	}
	// Auto-generate a strong initial password if not supplied.
	if req.Password == "" {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "rand failed"})
			return
		}
		req.Password = base64.RawURLEncoding.EncodeToString(b)
	}
	if len(req.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "password must be ≥ 8 chars"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.operators[req.Username]; exists {
		writeJSON(w, http.StatusConflict, envelope{OK: false, Error: "username already exists"})
		return
	}

	pwHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "hash failed"})
		return
	}

	// 10 fresh recovery codes for the new operator. Reuses the same helper
	// that bootstrap() uses, so codes have identical format/strength.
	plainCodes, hashed, gerr := generateRecoveryCodes(10)
	if gerr != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "recovery codes: " + gerr.Error()})
		return
	}

	now := time.Now()
	createdAt := now.UTC().Format(time.RFC3339)
	s.operators[req.Username] = operatorRecord{
		Username:      req.Username,
		PasswordHash:  string(pwHash),
		Role:          req.Role,
		CreatedAt:     createdAt,
		RecoveryCodes: hashed,
		// Direct admin-create implies trust — auto-approved. Invite path
		// (handleInviteComplete) sets Approved=false and InvitedBy=admin.
		Approved:   true,
		ApprovedAt: createdAt,
	}

	// Inline persist (we hold the write lock).
	ops := make([]operatorRecord, 0, len(s.operators))
	for _, o := range s.operators {
		ops = append(ops, o)
	}
	data, _ := json.MarshalIndent(ops, "", "  ")
	if err := writeOperatorsBlob(data); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "persist: " + err.Error()})
		return
	}

	log.Printf("auth: operator CREATED username=%q role=%s by=%s peer=%s",
		req.Username, req.Role, caller.Sub, clientAddr(r))

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: createOperatorResp{
		Username:      req.Username,
		Role:          req.Role,
		Password:      req.Password,
		RecoveryCodes: plainCodes,
		CreatedAt:     createdAt,
	}})
}

// DELETE /api/v1/auth/operators?username=X — admin only. Refuses to delete:
//   - the caller themselves (you can't lock yourself out)
//   - the last admin (refuses if removing them would leave 0 admins)
func (s *authStore) handleOperatorsDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "DELETE only"})
		return
	}
	caller, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	target := strings.TrimSpace(r.URL.Query().Get("username"))
	if target == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing ?username="})
		return
	}
	if target == caller.Sub {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "cannot delete yourself"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	op, exists := s.operators[target]
	if !exists {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "unknown operator"})
		return
	}
	// Last-admin guard.
	if op.Role == roleAdmin {
		adminCount := 0
		for _, o := range s.operators {
			if o.Role == roleAdmin {
				adminCount++
			}
		}
		if adminCount <= 1 {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false,
				Error: "cannot delete the only admin — promote another operator first"})
			return
		}
	}

	delete(s.operators, target)

	ops := make([]operatorRecord, 0, len(s.operators))
	for _, o := range s.operators {
		ops = append(ops, o)
	}
	data, _ := json.MarshalIndent(ops, "", "  ")
	if err := writeOperatorsBlob(data); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "persist: " + err.Error()})
		return
	}

	log.Printf("auth: operator DELETED username=%q by=%s peer=%s",
		target, caller.Sub, clientAddr(r))
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]string{"deleted": target}})
}

// PATCH /api/v1/auth/operators — admin only. Currently supports changing
// `role`. Refuses to demote the last admin.
type updateOperatorReq struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

func (s *authStore) handleOperatorsUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "PATCH only"})
		return
	}
	caller, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)

	var req updateOperatorReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing username"})
		return
	}
	if req.Role != roleAdmin && req.Role != roleOperator {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "role must be admin|operator"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	op, exists := s.operators[req.Username]
	if !exists {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "unknown operator"})
		return
	}
	if op.Role == req.Role {
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]string{
			"operator": req.Username, "role": req.Role, "unchanged": "true",
		}})
		return
	}
	// Demotion guard: if we're moving the only admin → operator, refuse.
	if op.Role == roleAdmin && req.Role != roleAdmin {
		adminCount := 0
		for _, o := range s.operators {
			if o.Role == roleAdmin {
				adminCount++
			}
		}
		if adminCount <= 1 {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false,
				Error: "cannot demote the only admin — promote another first"})
			return
		}
	}
	op.Role = req.Role
	s.operators[req.Username] = op

	ops := make([]operatorRecord, 0, len(s.operators))
	for _, o := range s.operators {
		ops = append(ops, o)
	}
	data, _ := json.MarshalIndent(ops, "", "  ")
	if err := writeOperatorsBlob(data); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "persist: " + err.Error()})
		return
	}

	log.Printf("auth: operator ROLE-CHANGED username=%q role=%s by=%s peer=%s",
		req.Username, req.Role, caller.Sub, clientAddr(r))
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]string{
		"operator": req.Username, "role": req.Role,
	}})
}

// ----------------------------------------------------------------------------
// TOTP enrollment for the current operator (first-login MFA path).
//
// Two-step:
//   1. POST /api/v1/auth/totp/setup-begin  → generate a fresh base32 secret
//      + the standard otpauth:// URI for QR scan. Secret is NOT persisted
//      yet; the client holds it and echoes it back in step 2.
//   2. POST /api/v1/auth/totp/setup-confirm { secret, code } → server
//      verifies the 6-digit code against the supplied secret, then writes
//      the secret to the operator's record. From this point login requires
//      the TOTP code too.
//
// Statelessness between the two steps is fine because the operator is on
// both sides of the wire over TLS — knowing one's own provisional secret
// gives them no power they wouldn't already have.
// ----------------------------------------------------------------------------

type totpBeginResp struct {
	Secret  string `json:"secret"`   // base32 (no padding) — give to authenticator
	Otpauth string `json:"otpauth"`  // for QR code generation client-side
}
type totpConfirmReq struct {
	Secret string `json:"secret"`
	Code   string `json:"code"`
}

func (s *authStore) handleTOTPSetupBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	c, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if c == nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "rand failed"})
		return
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	otpauth := fmt.Sprintf(
		"otpauth://totp/AcmeNet:%s?secret=%s&issuer=Acme%%20Cloud%%20Network&algorithm=SHA1&digits=6&period=30",
		c.Sub, secret)
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: totpBeginResp{Secret: secret, Otpauth: otpauth}})
}

func (s *authStore) handleTOTPSetupConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	c, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if c == nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	var req totpConfirmReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	req.Secret = strings.TrimSpace(req.Secret)
	req.Code = strings.TrimSpace(req.Code)
	if req.Secret == "" || req.Code == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "secret and code required"})
		return
	}
	if !verifyTOTP(req.Secret, req.Code, time.Now()) {
		log.Printf("auth: TOTP-SETUP-FAIL user=%q peer=%s code mismatch", c.Sub, clientAddr(r))
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "code doesn't match — check your authenticator's clock"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	op, exists := s.operators[c.Sub]
	if !exists {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "operator gone"})
		return
	}
	op.TOTPSecret = req.Secret
	s.operators[c.Sub] = op

	ops := make([]operatorRecord, 0, len(s.operators))
	for _, o := range s.operators {
		ops = append(ops, o)
	}
	data, _ := json.MarshalIndent(ops, "", "  ")
	if err := writeOperatorsBlob(data); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "persist: " + err.Error()})
		return
	}

	log.Printf("auth: TOTP-ENABLED user=%q peer=%s", c.Sub, clientAddr(r))
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]string{
		"operator": c.Sub,
		"status":   "totp enabled",
	}})
}

// isValidUsername enforces a conservative character class so usernames are
// safe to use in filenames, log lines, and SSH/email contexts.
func isValidUsername(s string) bool {
	if len(s) < 2 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// ----------------------------------------------------------------------------

func clientAddr(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return xr
	}
	return r.RemoteAddr
}
