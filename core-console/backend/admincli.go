// admincli.go — `ncn-api admin <subcommand>` for break-glass operator
// management. Reads/writes operators.json directly with file lock + atomic
// rename so a live ncn-api instance never sees a half-written file.
//
// Available subcommands:
//
//   list                                show every operator + role + MFA state
//   promote   <user>                    set role=admin
//   demote    <user>                    set role=operator
//   reset-password <user>               prompt twice for new password; bcrypt + persist
//   reset-mfa <user>                    clear TOTP secret + all passkeys + recovery codes
//   mint-recover <user>                 mint a one-shot recovery URL (HMAC-signed)
//   regen-recovery-codes <user>         print 10 fresh recovery codes (plain) + persist hashes
//
// Why this exists: when the only admin loses both password AND passkey AND
// recovery codes, the in-app reset flow can't help. SSH to tyo is the
// out-of-band trust anchor.
//
// After any write, the CLI hints `systemctl restart ncn-api` so the running
// process re-reads operators.json. (We could send SIGHUP and add a watcher,
// but a 1-second restart is acceptable for break-glass ops.)
package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

// adminCLIEntrypoint is invoked from main() when argv[1]=="admin". Parses
// argv[2:] and dispatches to the right subcommand. Exits the process.
func adminCLIEntrypoint(args []string) {
	if len(args) == 0 {
		adminUsage()
		os.Exit(2)
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "list":
		adminCmdList()
	case "promote":
		adminCmdSetRole(rest, roleAdmin)
	case "demote":
		adminCmdSetRole(rest, roleOperator)
	case "reset-password":
		adminCmdResetPassword(rest)
	case "reset-mfa":
		adminCmdResetMFA(rest)
	case "regen-recovery-codes":
		adminCmdRegenRecovery(rest)
	case "mint-recover":
		adminCmdMintRecover(rest)
	case "help", "-h", "--help":
		adminUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown admin subcommand: %q\n\n", cmd)
		adminUsage()
		os.Exit(2)
	}
}

func adminUsage() {
	fmt.Fprintln(os.Stderr, `ncn-api admin — break-glass operator management

Usage:
  ncn-api admin list
  ncn-api admin promote <user>
  ncn-api admin demote  <user>
  ncn-api admin reset-password <user>
  ncn-api admin reset-mfa <user>
  ncn-api admin regen-recovery-codes <user>
  ncn-api admin mint-recover <user>

Operates directly on /etc/ncn-core-console/operators.json. Run on tyo as
root. After any write, restart the service: systemctl restart ncn-api`)
}

// adminLoadOps reads operators.json and returns a slice plus an index by
// username. Used by every subcommand. Read-only.
func adminLoadOps() ([]operatorRecord, map[string]int, error) {
	data, err := os.ReadFile(operatorsPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", operatorsPath, err)
	}
	var ops []operatorRecord
	if err := json.Unmarshal(data, &ops); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", operatorsPath, err)
	}
	idx := make(map[string]int, len(ops))
	for i, op := range ops {
		idx[op.Username] = i
	}
	return ops, idx, nil
}

// adminSaveOps writes operators.json atomically (tmp + rename). Mirrors the
// behavior of authStore.persist but operates on a flat slice.
func adminSaveOps(ops []operatorRecord) error {
	data, err := json.MarshalIndent(ops, "", "  ")
	if err != nil {
		return err
	}
	tmp := operatorsPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, operatorsPath)
}

// adminCmdList prints a fixed-width table of every operator.
func adminCmdList() {
	ops, _, err := adminLoadOps()
	if err != nil {
		die(err)
	}
	fmt.Printf("%-20s  %-9s  %-7s  %-7s  %-8s  %s\n",
		"username", "role", "passkey", "totp", "approved", "created")
	fmt.Println(strings.Repeat("─", 80))
	for _, op := range ops {
		pk := "—"
		if len(op.Passkeys) > 0 {
			pk = fmt.Sprintf("× %d", len(op.Passkeys))
		}
		totp := "—"
		if op.TOTPSecret != "" {
			totp = "set"
		}
		approved := "no"
		if op.Approved {
			approved = "yes"
		}
		fmt.Printf("%-20s  %-9s  %-7s  %-7s  %-8s  %s\n",
			op.Username, op.Role, pk, totp, approved, op.CreatedAt)
	}
}

// adminCmdSetRole mutates one operator's role. Refuses to demote the last
// admin (else nobody can manage the system from the UI side).
func adminCmdSetRole(args []string, role string) {
	if len(args) != 1 {
		die(fmt.Errorf("usage: ncn-api admin %s <user>", role))
	}
	user := args[0]
	ops, idx, err := adminLoadOps()
	if err != nil {
		die(err)
	}
	i, ok := idx[user]
	if !ok {
		die(fmt.Errorf("no such user: %q", user))
	}
	if ops[i].Role == role {
		fmt.Fprintf(os.Stderr, "%s already has role=%s — no change.\n", user, role)
		return
	}
	if role == roleOperator {
		// Last admin guard
		admins := 0
		for _, op := range ops {
			if op.Role == roleAdmin {
				admins++
			}
		}
		if ops[i].Role == roleAdmin && admins <= 1 {
			die(fmt.Errorf("refusing to demote %q — it is the only remaining admin", user))
		}
	}
	ops[i].Role = role
	if err := adminSaveOps(ops); err != nil {
		die(err)
	}
	fmt.Printf("✓ %s → role=%s\n", user, role)
	fmt.Println("  run: systemctl restart ncn-api")
}

// adminCmdResetPassword reads a new password twice from /dev/tty and writes
// its bcrypt hash. Does NOT touch TOTP, passkeys, or recovery codes.
func adminCmdResetPassword(args []string) {
	if len(args) != 1 {
		die(fmt.Errorf("usage: ncn-api admin reset-password <user>"))
	}
	user := args[0]
	ops, idx, err := adminLoadOps()
	if err != nil {
		die(err)
	}
	i, ok := idx[user]
	if !ok {
		die(fmt.Errorf("no such user: %q", user))
	}

	pw1, err := readSecret("new password for " + user + ": ")
	if err != nil {
		die(err)
	}
	if len(pw1) < 8 {
		die(fmt.Errorf("password must be at least 8 characters"))
	}
	pw2, err := readSecret("confirm: ")
	if err != nil {
		die(err)
	}
	if pw1 != pw2 {
		die(fmt.Errorf("passwords do not match"))
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(pw1), bcrypt.DefaultCost)
	if err != nil {
		die(err)
	}
	ops[i].PasswordHash = string(hash)
	if err := adminSaveOps(ops); err != nil {
		die(err)
	}
	fmt.Printf("✓ %s password reset\n", user)
	fmt.Println("  run: systemctl restart ncn-api")
}

// adminCmdResetMFA wipes every second factor on an account so the user can
// log in with password alone. Use when the user lost their phone, lost their
// passkey device, AND lost their recovery codes.
func adminCmdResetMFA(args []string) {
	if len(args) != 1 {
		die(fmt.Errorf("usage: ncn-api admin reset-mfa <user>"))
	}
	user := args[0]
	ops, idx, err := adminLoadOps()
	if err != nil {
		die(err)
	}
	i, ok := idx[user]
	if !ok {
		die(fmt.Errorf("no such user: %q", user))
	}
	ops[i].TOTPSecret = ""
	pkCount := len(ops[i].Passkeys)
	ops[i].Passkeys = nil
	rcCount := len(ops[i].RecoveryCodes)
	ops[i].RecoveryCodes = nil
	if err := adminSaveOps(ops); err != nil {
		die(err)
	}
	fmt.Printf("✓ %s MFA cleared (TOTP wiped, %d passkey(s) removed, %d recovery code(s) removed)\n",
		user, pkCount, rcCount)
	fmt.Println("  user can now log in with password only; ask them to re-enroll MFA immediately")
	fmt.Println("  run: systemctl restart ncn-api")
}

// adminCmdRegenRecovery prints 10 fresh plain-text recovery codes and stores
// their bcrypt hashes. Discards the previous code set.
func adminCmdRegenRecovery(args []string) {
	if len(args) != 1 {
		die(fmt.Errorf("usage: ncn-api admin regen-recovery-codes <user>"))
	}
	user := args[0]
	ops, idx, err := adminLoadOps()
	if err != nil {
		die(err)
	}
	i, ok := idx[user]
	if !ok {
		die(fmt.Errorf("no such user: %q", user))
	}

	plain := make([]string, 10)
	hashes := make([]string, 10)
	for k := 0; k < 10; k++ {
		buf := make([]byte, 10)
		if _, err := rand.Read(buf); err != nil {
			die(err)
		}
		s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)[:16]
		plain[k] = s[:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
		h, err := bcrypt.GenerateFromPassword([]byte(plain[k]), bcrypt.DefaultCost)
		if err != nil {
			die(err)
		}
		hashes[k] = string(h)
	}
	ops[i].RecoveryCodes = hashes
	if err := adminSaveOps(ops); err != nil {
		die(err)
	}
	fmt.Printf("✓ %s — 10 fresh recovery codes (each one-time use):\n\n", user)
	for k, c := range plain {
		fmt.Printf("  [%2d/10]  %s\n", k+1, c)
	}
	fmt.Println("\n  capture these now — they will not be shown again")
	fmt.Println("  run: systemctl restart ncn-api")
}

// adminCmdMintRecover prints a one-shot URL the user can open in any browser
// to set a new password. The token is HMAC-signed with a key at
// /etc/ncn-core-console/recovery-bootstrap.key (auto-generated on first
// call) and lives 15 minutes. See backend/recover_bootstrap.go for the
// verifier and nonce-bookkeeping.
func adminCmdMintRecover(args []string) {
	if len(args) != 1 {
		die(fmt.Errorf("usage: ncn-api admin mint-recover <user>"))
	}
	user := args[0]
	_, idx, err := adminLoadOps()
	if err != nil {
		die(err)
	}
	if _, ok := idx[user]; !ok {
		die(fmt.Errorf("no such user: %q", user))
	}

	key, err := loadOrCreateRecoveryBootstrapKey()
	if err != nil {
		die(err)
	}

	nonce := make([]byte, 12)
	_, _ = rand.Read(nonce)
	exp := time.Now().Add(15 * time.Minute).Unix()

	payload, _ := json.Marshal(struct {
		User  string `json:"user"`
		Exp   int64  `json:"exp"`
		Nonce string `json:"n"`
	}{User: user, Exp: exp, Nonce: base64.RawURLEncoding.EncodeToString(nonce)})

	pb := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(pb))
	sb := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	token := "rcv-" + pb + "." + sb
	url := "https://admin.example.com/recover/" + token

	fmt.Printf("✓ one-shot recovery URL for %s (expires in 15 min):\n\n", user)
	fmt.Printf("  %s\n\n", url)
	fmt.Println("  open in any browser → set new password → token burns on first successful use.")
	fmt.Println("  no restart required (the verifier reads the key on each request).")
}

// ---- shared helpers ----

// stdinReader is shared across multiple readSecret() calls so the bufio
// buffer survives between prompts. Without this, the SECOND call gets EOF
// because the first call's bufio buffer ate the second line and was then
// thrown away with the reader.
var stdinReader = bufio.NewReader(os.Stdin)

// readSecret reads a line without echo. Falls back to a shared bufio
// reader when stdin isn't a TTY (CI / pipes), printing a single-line
// warning.
func readSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	fmt.Fprintln(os.Stderr, "(stdin is not a TTY — input will be visible)")
	line, err := stdinReader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
