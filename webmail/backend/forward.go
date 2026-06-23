// forward.go — per-user mail forwarding via Sieve scripts.
//
// Each user has a ~/.dovecot.sieve in their maildir that pigeonhole runs at
// LMTP delivery time. To "forward + keep local copy" the script uses Sieve's
// `:copy` modifier:
//
//	require ["copy"];
//	redirect :copy "external@example.com";
//
// Without :copy, the message is REDIRECTED (no local copy). We always use
// :copy — users almost always want a local archive too.
//
// We store nothing extra; the .sieve file IS the state. Reads parse it back
// to get the current list.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	forwardMaxAddresses = 8
)

// redirectRE matches `redirect :copy "addr"` (with optional whitespace).
var redirectRE = regexp.MustCompile(`(?m)^redirect\s+:copy\s+"([^"]+)"\s*;`)

func sievePath(mailbox string) string {
	// dovecot mail_home format: /var/mail/vhosts/<domain>/<user>
	parts := strings.SplitN(mailbox, "@", 2)
	if len(parts) != 2 {
		return ""
	}
	return filepath.Join(maildirRoot, parts[0], ".dovecot.sieve")
}

func readForwardAddresses(mailbox string) ([]string, error) {
	p := sievePath(mailbox)
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	matches := redirectRE.FindAllStringSubmatch(string(data), -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out, nil
}

func writeForwardAddresses(mailbox string, addrs []string) error {
	p := sievePath(mailbox)
	if p == "" {
		return fmt.Errorf("invalid mailbox: %s", mailbox)
	}

	// Empty list → just delete the file (dovecot falls back to the default
	// sieve which is empty).
	if len(addrs) == 0 {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	var b strings.Builder
	b.WriteString("# managed by ncn-mail · forward list — edit via webmail UI\n")
	b.WriteString(`require ["copy"];` + "\n")
	for _, a := range addrs {
		// double-quote escape: forbid quote in addr (already validated by ParseAddress)
		fmt.Fprintf(&b, `redirect :copy "%s";`+"\n", strings.ReplaceAll(a, `"`, ``))
	}

	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	if err := os.Chown(tmp, 5000, 5000); err != nil {
		// non-fatal; pigeonhole runs the script as the mail user but doesn't
		// require .sieve to be owned by them per se
	}
	return os.Rename(tmp, p)
}

// ----------------------------------------------------------------------------
// HTTP handlers
// ----------------------------------------------------------------------------

// GET /api/v1/mail/forward
//
// Returns both verified (active in sieve) and pending (awaiting
// verification — recipient hasn't clicked the link yet) sets.
func (m *mailService) handleForwardGet(w http.ResponseWriter, r *http.Request) {
	c, ok := mailClaimsFromRequest(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	addrs, err := readForwardAddresses(c.Mailbox)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	pending, err := readPendingForwards(c.Mailbox)
	if err != nil {
		// Non-fatal: surface verified, just log
		pending = []string{}
	}
	if addrs == nil {
		addrs = []string{}
	}
	if pending == nil {
		pending = []string{}
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"mailbox":   c.Mailbox,
		"addresses": addrs,
		"pending":   pending,
	}})
}

// PUT /api/v1/mail/forward
//
//	{ "addresses": ["a@x.com", "b@y.com"] }
func (m *mailService) handleForwardPut(w http.ResponseWriter, r *http.Request) {
	c, ok := mailClaimsFromRequest(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	var req struct {
		Addresses []string `json:"addresses"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	if len(req.Addresses) > forwardMaxAddresses {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: fmt.Sprintf("at most %d forward addresses allowed", forwardMaxAddresses)})
		return
	}

	clean := make([]string, 0, len(req.Addresses))
	for _, raw := range req.Addresses {
		a := strings.TrimSpace(raw)
		if a == "" {
			continue
		}
		// reject newlines / quotes that would break the sieve script
		if strings.ContainsAny(a, "\r\n\"\\") {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false,
				Error: "address contains forbidden character: " + a})
			return
		}
		addr, err := mail.ParseAddress(a)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false,
				Error: "invalid email: " + a})
			return
		}
		// Refuse self-referential forwards — easy mistake, infinite mail loop
		if strings.EqualFold(addr.Address, c.Mailbox) {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false,
				Error: "cannot forward to yourself"})
			return
		}
		clean = append(clean, addr.Address)
	}

	// --- Verification gate ---
	//
	// We classify the submitted list into three buckets:
	//   keepVerified — already active in sieve, user kept them
	//   newPending   — not yet verified, will trigger a verification mail
	//   keepPending  — pending from a previous submit that the user kept
	//
	// Anything previously verified but NOT in the submitted list is
	// removed from the sieve (user is shrinking their forward list).
	// Anything previously pending but NOT in the submitted list is
	// dropped from pending too.
	prevVerified, _ := readForwardAddresses(c.Mailbox)
	prevPending, _ := readPendingForwards(c.Mailbox)
	verifiedSet := map[string]bool{}
	for _, a := range prevVerified {
		verifiedSet[strings.ToLower(a)] = true
	}
	pendingSet := map[string]bool{}
	for _, a := range prevPending {
		pendingSet[strings.ToLower(a)] = true
	}

	keepVerified := make([]string, 0, len(clean))
	keepPending := make([]string, 0, len(clean))
	newPending := make([]string, 0, len(clean))
	for _, a := range clean {
		la := strings.ToLower(a)
		switch {
		case verifiedSet[la]:
			keepVerified = append(keepVerified, a)
		case pendingSet[la]:
			keepPending = append(keepPending, a)
		default:
			newPending = append(newPending, a)
		}
	}

	// Sieve = only verified subset. Newly-added ones don't go into sieve
	// until the recipient clicks the verification link.
	if err := writeForwardAddresses(c.Mailbox, keepVerified); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	if err := writePendingForwards(c.Mailbox, append(keepPending, newPending...)); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}

	// Send verification emails for the newly-added pending entries.
	// Don't block on these — log and continue if any fail. The user can
	// re-PUT to retry.
	sent := []string{}
	for _, addr := range newPending {
		if err := sendForwardVerification(c.Mailbox, addr); err != nil {
			log.Printf("forward: send verification %s → %s: %v", c.Mailbox, addr, err)
			continue
		}
		sent = append(sent, addr)
	}

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"mailbox":             c.Mailbox,
		"addresses":           keepVerified,
		"pending":             append(keepPending, newPending...),
		"verifications_sent":  sent,
	}})
}
