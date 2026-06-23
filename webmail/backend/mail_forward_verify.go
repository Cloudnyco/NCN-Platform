// mail_forward_verify.go — verification flow for forward addresses.
//
// Flow:
//
//   1. User submits a list of forward addresses via PUT /api/v1/mail/forward.
//      Addresses already in the user's `.dovecot.sieve` (= already verified)
//      stay active. New addresses are written to a per-user
//      `.ncn-forward-pending.json` and a verification email is sent to
//      each.
//   2. The recipient clicks the verification link
//      (`https://mail.example.com/verify-forward/vfwd-<payload>.<sig>`),
//      which hits `GET /api/v1/mail/forward/verify?token=...`.
//   3. We verify the HMAC + TTL, then PROMOTE the pending address into
//      the verified set (regenerate the user's sieve script).
//
// State: the sieve script is the source of truth for "active forwards".
// Pending state lives in a small JSON next to the sieve in the user's
// maildir. The HMAC key is at `/etc/ncn-mail/forward-verify.key`
// (auto-generated 32-byte key on first mint).
package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	forwardVerifyKeyPath = "/etc/ncn-mail/forward-verify.key"
	forwardVerifyPrefix  = "vfwd-"
	forwardVerifyTTL     = 24 * time.Hour
	forwardPendingSuffix = ".ncn-forward-pending.json"
)

// loadOrCreateForwardVerifyKey returns the HMAC-SHA256 key used to sign
// forward-address verification links. Auto-generates 32 random bytes on
// first call.
var fvKeyOnce sync.Once
var fvKey []byte
var fvKeyErr error

func loadOrCreateForwardVerifyKey() ([]byte, error) {
	fvKeyOnce.Do(func() {
		data, err := os.ReadFile(forwardVerifyKeyPath)
		if err == nil {
			if len(data) < 16 {
				fvKeyErr = fmt.Errorf("%s too short", forwardVerifyKeyPath)
				return
			}
			fvKey = data
			return
		}
		if !os.IsNotExist(err) {
			fvKeyErr = err
			return
		}
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			fvKeyErr = err
			return
		}
		if err := os.WriteFile(forwardVerifyKeyPath, b, 0o600); err != nil {
			fvKeyErr = err
			return
		}
		fvKey = b
	})
	return fvKey, fvKeyErr
}

// fwdVerifyClaims is the payload signed inside a vfwd- token.
type fwdVerifyClaims struct {
	Mailbox string `json:"m"`   // owner of the forward (full address)
	Fwd     string `json:"f"`   // address being verified
	Exp     int64  `json:"exp"` // unix seconds
	Nonce   string `json:"n"`   // random
}

// mintForwardVerifyToken builds a one-shot signed token + the URL the
// recipient is supposed to open.
func mintForwardVerifyToken(mailbox, fwd string) (token, url string, err error) {
	key, err := loadOrCreateForwardVerifyKey()
	if err != nil {
		return "", "", err
	}
	nonceB := make([]byte, 8)
	_, _ = rand.Read(nonceB)
	payload, _ := json.Marshal(fwdVerifyClaims{
		Mailbox: strings.ToLower(mailbox),
		Fwd:     strings.ToLower(fwd),
		Exp:     time.Now().Add(forwardVerifyTTL).Unix(),
		Nonce:   base64.RawURLEncoding.EncodeToString(nonceB),
	})
	pb := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(pb))
	sb := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	token = forwardVerifyPrefix + pb + "." + sb
	url = "https://" + mailHost + "/verify-forward/" + token
	return token, url, nil
}

// verifyForwardToken parses + validates. Returns the claims on success.
func verifyForwardToken(token string) (*fwdVerifyClaims, error) {
	if !strings.HasPrefix(token, forwardVerifyPrefix) {
		return nil, errors.New("not a forward-verify token")
	}
	body := strings.TrimPrefix(token, forwardVerifyPrefix)
	parts := strings.Split(body, ".")
	if len(parts) != 2 {
		return nil, errors.New("malformed token")
	}
	key, err := loadOrCreateForwardVerifyKey()
	if err != nil {
		return nil, err
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode sig: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(parts[0]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, errors.New("bad signature")
	}
	var c fwdVerifyClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}
	if c.Mailbox == "" || c.Fwd == "" {
		return nil, errors.New("missing mailbox or forward")
	}
	now := time.Now().Unix()
	if c.Exp == 0 || now > c.Exp {
		return nil, errors.New("token expired")
	}
	return &c, nil
}

// ----------------------------------------------------------------------------
// Pending state — { "pending": [{address, added_at}, ...] }
// ----------------------------------------------------------------------------

type pendingForward struct {
	Address string `json:"address"`
	AddedAt int64  `json:"added_at"`
}

type pendingForwardFile struct {
	Pending []pendingForward `json:"pending"`
}

func forwardPendingPath(mailbox string) string {
	local := strings.SplitN(mailbox, "@", 2)[0]
	return filepath.Join(maildirRoot, local, forwardPendingSuffix)
}

func readPendingForwards(mailbox string) ([]string, error) {
	data, err := os.ReadFile(forwardPendingPath(mailbox))
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var pf pendingForwardFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-forwardVerifyTTL).Unix()
	out := make([]string, 0, len(pf.Pending))
	for _, p := range pf.Pending {
		// Expired entries are silently dropped.
		if p.AddedAt < cutoff {
			continue
		}
		out = append(out, p.Address)
	}
	return out, nil
}

func writePendingForwards(mailbox string, addrs []string) error {
	p := forwardPendingPath(mailbox)
	if len(addrs) == 0 {
		_ = os.Remove(p)
		return nil
	}
	now := time.Now().Unix()
	pf := pendingForwardFile{Pending: make([]pendingForward, 0, len(addrs))}
	for _, a := range addrs {
		pf.Pending = append(pf.Pending, pendingForward{Address: strings.ToLower(a), AddedAt: now})
	}
	data, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	_ = os.Chown(tmp, 5000, 5000)
	return os.Rename(tmp, p)
}

func addPendingForward(mailbox, addr string) error {
	addr = strings.ToLower(strings.TrimSpace(addr))
	current, err := readPendingForwards(mailbox)
	if err != nil {
		return err
	}
	for _, a := range current {
		if strings.EqualFold(a, addr) {
			return nil // already pending
		}
	}
	current = append(current, addr)
	return writePendingForwards(mailbox, current)
}

func removePendingForward(mailbox, addr string) error {
	addr = strings.ToLower(strings.TrimSpace(addr))
	current, err := readPendingForwards(mailbox)
	if err != nil {
		return err
	}
	kept := current[:0]
	for _, a := range current {
		if !strings.EqualFold(a, addr) {
			kept = append(kept, a)
		}
	}
	return writePendingForwards(mailbox, kept)
}

// ----------------------------------------------------------------------------
// HTTP handler — public verify endpoint
// ----------------------------------------------------------------------------

// GET /api/v1/mail/forward/verify?token=vfwd-...
//
// Public route — trust comes from the HMAC signature, not from a session.
// On success: promotes the pending address into the verified set
// (regenerates the user's sieve script). Returns { mailbox, address }
// so the frontend can show "your forward to X is now active".
func (m *mailService) handleForwardVerify(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	c, err := verifyForwardToken(token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: err.Error()})
		return
	}

	// Pull the current verified set + pending set, move c.Fwd from
	// pending to verified, write both back.
	verified, err := readForwardAddresses(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "read sieve: " + err.Error()})
		return
	}
	// Already verified? Idempotent.
	for _, a := range verified {
		if strings.EqualFold(a, c.Fwd) {
			_ = removePendingForward(c.Mailbox, c.Fwd) // cleanup if still in pending
			writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
				"mailbox": c.Mailbox, "address": c.Fwd, "already": true,
			}})
			return
		}
	}
	pending, err := readPendingForwards(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "read pending: " + err.Error()})
		return
	}
	stillPending := false
	for _, a := range pending {
		if strings.EqualFold(a, c.Fwd) {
			stillPending = true
			break
		}
	}
	if !stillPending {
		// User probably removed the address before clicking the link.
		writeJSON(w, http.StatusConflict, envelope{OK: false,
			Error: "this address is no longer pending — perhaps the owner removed it"})
		return
	}

	verified = append(verified, c.Fwd)
	if err := writeForwardAddresses(c.Mailbox, verified); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "write sieve: " + err.Error()})
		return
	}
	if err := removePendingForward(c.Mailbox, c.Fwd); err != nil {
		// Non-fatal: address is now verified anyway, pending stale entry
		// will be GC'd by TTL on next read.
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"mailbox": c.Mailbox, "address": c.Fwd,
	}})
}
