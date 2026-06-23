// admincli.go — `ncn-mail admin <subcommand>` for break-glass mailbox
// management on pop-03. Mirrors core-console's admincli.go but operates
// on dovecot's passwd-file rather than operators.json.
//
// Available subcommands:
//
//   list                                show every mailbox in /etc/dovecot/users
//   reset-password <mailbox>            prompt twice for new password; bcrypt + persist
//   mint-recover   <mailbox>            mint one-shot HMAC-signed URL
//
// Why this exists: postmaster/noc/hostmaster/abuse/security are role
// mailboxes that nobody logs into routinely. When their passwords are
// lost (or were never captured at provisioning), the in-app reset flow
// can't help because it requires being logged in as an admin mailbox.
// SSH root on pop-03 is the out-of-band anchor.
//
// After any write, the dovecot passwd-file driver picks up the change
// on next auth via mtime; we additionally `doveadm reload` so the change
// is instant. No restart of ncn-mail required.
package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
)

// adminCLIEntrypoint is invoked from main() when argv[1]=="admin". Exits
// the process.
func adminCLIEntrypoint(args []string) {
	if len(args) == 0 {
		adminUsage()
		os.Exit(2)
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "list":
		adminCmdList()
	case "reset-password":
		adminCmdResetPassword(rest)
	case "mint-recover":
		adminCmdMintRecover(rest)
	case "setup-noreply":
		adminCmdSetupNoreply(rest)
	case "test-notify":
		adminCmdTestNotify(rest)
	case "test-verify":
		adminCmdTestVerify(rest)
	case "api-key":
		adminCmdAPIKey(rest)
	case "gmail-setup":
		adminCmdGmailSetup(rest)
	case "help", "-h", "--help":
		adminUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown admin subcommand: %q\n\n", cmd)
		adminUsage()
		os.Exit(2)
	}
}

func adminUsage() {
	fmt.Fprintln(os.Stderr, `ncn-mail admin — break-glass mailbox management

Usage:
  ncn-mail admin list
  ncn-mail admin reset-password <mailbox>
  ncn-mail admin mint-recover   <mailbox>
  ncn-mail admin setup-noreply
  ncn-mail admin test-notify    <recipient>
  ncn-mail admin test-verify    <recipient>
  ncn-mail admin api-key        create|list|revoke   (transactional send API keys)
  ncn-mail admin gmail-setup                          (configure Gmail relay)

Operates directly on /etc/dovecot/users (via the same write path the
in-app admin reset uses). Run on pop-03 as root. doveadm reload is
called after each write; no service restart needed.

setup-noreply: provisions noreply@example.com, installs a sieve rule that
discards all inbound to that address, and stashes the generated password
so backend code can call sendSystemMail(). Idempotent (rotates pw +
re-stashes if the mailbox already exists). Useful for system mail like
password-reset alerts and login notifications.

test-notify / test-verify: send a single smoke email to the recipient so
you can eyeball the noreply-styled HTML rendering end-to-end.
test-notify fires all three "info" notification styles in sequence:
  forgot-password ack, mailbox-recover confirmation, passkey-added alert.
test-verify fires the HTML+CTA forward-verification email.`)
}

// adminCmdList prints all rows from /etc/dovecot/users with a flag
// summary (admin status, has-password-set).
func adminCmdList() {
	data, err := os.ReadFile(managedUsersPath)
	if err != nil {
		die(err)
	}
	admins, _ := loadAdminSet()
	fmt.Printf("%-32s  %-7s  %s\n", "mailbox", "admin", "hash")
	fmt.Println(strings.Repeat("─", 70))
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, ":", 8)
		if len(fields) < 2 {
			continue
		}
		mailbox := strings.ToLower(fields[0])
		isAdmin := "—"
		if _, ok := admins[mailbox]; ok {
			isAdmin = "yes"
		}
		hash := fields[1]
		if len(hash) > 24 {
			hash = hash[:24] + "…"
		}
		fmt.Printf("%-32s  %-7s  %s\n", mailbox, isAdmin, hash)
	}
}

// adminCmdResetPassword reads a new password twice from /dev/tty and
// rewrites the bcrypt hash via the shared replaceMailboxPassword helper.
func adminCmdResetPassword(args []string) {
	if len(args) != 1 {
		die(fmt.Errorf("usage: ncn-mail admin reset-password <mailbox>"))
	}
	mailbox := args[0]
	if !strings.Contains(mailbox, "@") {
		// Convenience: allow bare local-part; assume @example.com.
		mailbox = mailbox + "@" + mailDomain
	}

	pw1, err := readSecret("new password for " + mailbox + ": ")
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

	if err := replaceMailboxPassword(mailbox, pw1); err != nil {
		die(err)
	}
	fmt.Printf("✓ %s password reset (doveadm reload triggered)\n", mailbox)
}

// adminCmdMintRecover prints a one-shot URL the user can open in any
// browser to set a new mailbox password. Verifier is in mailbox_recover.go.
func adminCmdMintRecover(args []string) {
	if len(args) != 1 {
		die(fmt.Errorf("usage: ncn-mail admin mint-recover <mailbox>"))
	}
	mailbox := args[0]
	if !strings.Contains(mailbox, "@") {
		mailbox = mailbox + "@" + mailDomain
	}

	// Confirm the mailbox exists in /etc/dovecot/users.
	data, err := os.ReadFile(managedUsersPath)
	if err != nil {
		die(err)
	}
	prefix := strings.ToLower(mailbox) + ":"
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.ToLower(line), prefix) {
			found = true
			break
		}
	}
	if !found {
		die(fmt.Errorf("mailbox not found in %s: %s", managedUsersPath, mailbox))
	}

	key, err := loadOrCreateMailboxRecoveryKey()
	if err != nil {
		die(err)
	}

	nonce := make([]byte, 12)
	_, _ = rand.Read(nonce)
	exp := time.Now().Add(15 * time.Minute).Unix()

	payload, _ := json.Marshal(struct {
		Mailbox string `json:"mb"`
		Exp     int64  `json:"exp"`
		Nonce   string `json:"n"`
	}{Mailbox: strings.ToLower(mailbox), Exp: exp, Nonce: base64.RawURLEncoding.EncodeToString(nonce)})

	pb := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(pb))
	sb := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	token := "mb-" + pb + "." + sb
	url := "https://" + mailHost + "/admin-recover/" + token

	fmt.Printf("✓ one-shot recovery URL for %s (expires in 15 min):\n\n", mailbox)
	fmt.Printf("  %s\n\n", url)
	fmt.Println("  open in any browser → set new password → token burns on first successful use.")
	fmt.Println("  no restart required.")
}

// adminCmdSetupNoreply provisions noreply@example.com (idempotent: rotates
// password if the mailbox already exists), drops a sieve script that
// discards every incoming message, and stashes the credential via
// /etc/ncn-mail/mail-creds.json so backend handlers can later call
// sendSystemMail() / sendForwardVerification() without prompting the user.
func adminCmdSetupNoreply(args []string) {
	if len(args) != 0 {
		die(fmt.Errorf("usage: ncn-mail admin setup-noreply (takes no args)"))
	}
	mailbox := "noreply@" + mailDomain

	// Generate a 24-byte (32-char base64) random password. Operators
	// never type this; it lives only in mail-creds.json (AES-GCM).
	pwBytes := make([]byte, 24)
	if _, err := rand.Read(pwBytes); err != nil {
		die(err)
	}
	pw := base64.RawURLEncoding.EncodeToString(pwBytes)

	exists := mailboxExistsInDovecot(mailbox)
	if exists {
		fmt.Printf("noreply@ already exists — rotating password\n")
		if err := replaceMailboxPassword(mailbox, pw); err != nil {
			die(fmt.Errorf("rotate password: %w", err))
		}
	} else {
		fmt.Printf("provisioning new mailbox %s\n", mailbox)
		if err := provisionMailbox(mailbox, pw); err != nil {
			die(fmt.Errorf("provision: %w", err))
		}
	}

	// Per-user sieve: discard every message. Compile to .svbin alongside.
	// vmail uid:gid (5000:5000) must own these so dovecot can recompile if
	// the source changes later.
	sieveDir := filepath.Join("/var/mail/vhosts", mailDomain, "noreply", "sieve")
	if err := os.MkdirAll(sieveDir, 0o700); err != nil {
		die(fmt.Errorf("mkdir sieve dir: %w", err))
	}
	sievePath := filepath.Join(sieveDir, "discard-all.sieve")
	if err := os.WriteFile(sievePath, []byte("# Auto-discard every inbound to noreply@example.com.\n"+
		"# This account is for outbound system mail only.\n"+
		"discard;\nstop;\n"), 0o644); err != nil {
		die(fmt.Errorf("write sieve: %w", err))
	}
	// Activate via the user's home symlink ~/.dovecot.sieve. dovecot's
	// `sieve_script personal { active_path = ~/.dovecot.sieve }` is what
	// makes it the running script.
	activePath := filepath.Join("/var/mail/vhosts", mailDomain, "noreply", ".dovecot.sieve")
	_ = os.Remove(activePath)
	if err := os.Symlink(sievePath, activePath); err != nil {
		die(fmt.Errorf("activate sieve: %w", err))
	}
	if err := chownRecursive(filepath.Join("/var/mail/vhosts", mailDomain, "noreply"), 5000, 5000); err != nil {
		die(fmt.Errorf("chown home: %w", err))
	}
	// Compile sieve so dovecot can run it instantly without an extra
	// auto-recompile on first delivery (which can race with discard).
	if out, err := exec.Command("/usr/bin/sievec", sievePath).CombinedOutput(); err != nil {
		// Non-fatal: dovecot can compile on demand. Just warn.
		fmt.Fprintf(os.Stderr, "warning: sievec %s failed (continuing): %v · %s\n",
			sievePath, err, strings.TrimSpace(string(out)))
	}

	// Stash the credential so backend code can look it up. We write to
	// the canonical mail-creds.json via forgetStashEverywhere's mirror
	// helper — but for STASH (encrypt + persist) we need a minimal
	// mailService. Use the package-level constructor we already have.
	secret, err := loadOrCreateSecret()
	if err != nil {
		die(fmt.Errorf("load session secret: %w", err))
	}
	svc, err := newMailService(secret)
	if err != nil {
		die(fmt.Errorf("init mail service: %w", err))
	}
	if err := svc.stash(mailbox, pw); err != nil {
		die(fmt.Errorf("stash: %w", err))
	}

	fmt.Printf("✓ %s ready · password stashed · inbound discarded by sieve\n", mailbox)
	fmt.Println("  the password is never displayed — backend code calls sendSystemMail() to use it.")
	fmt.Println("  rotate again any time with: ncn-mail admin setup-noreply")
	fmt.Println("  run: systemctl restart ncn-mail (so the running process loads the new stash)")
}

// adminCmdTestNotify sends each of the three "info" system-mail variants
// (forgot-password ack, mailbox-recover confirmation, passkey-added alert)
// to the given recipient, in sequence. Lets us eyeball every styled
// system notification end-to-end with one CLI call.
func adminCmdTestNotify(args []string) {
	if len(args) != 1 {
		die(fmt.Errorf("usage: ncn-mail admin test-notify <recipient>"))
	}
	recipient := args[0]
	secret, err := loadOrCreateSecret()
	if err != nil {
		die(err)
	}
	svc, err := newMailService(secret)
	if err != nil {
		die(err)
	}
	mailSvcGlobal = svc

	fixtureMailbox := "smoke@example.com"

	// 1 — forgot-password ack (info, no link)
	if err := sendSystemMail(
		recipient,
		"We received your password recovery request",
		"Password Recovery Requested",
		[]string{
			"We received a password recovery request for " + fixtureMailbox + ".",
			"What happens next: an operator will contact you out-of-band (usually within a day) to verify your identity and walk you through the reset. We deliberately do NOT send reset links by email to avoid phishing patterns.",
			"If you didn't make this request, ignore this message — nothing will change without an operator's manual action.",
		},
	); err != nil {
		die(fmt.Errorf("forgot-ack smoke: %w", err))
	}
	fmt.Printf("✓ 1/3 forgot-password ack sent to %s\n", recipient)

	// 2 — mailbox-recover confirmation (security alert)
	if err := sendSystemMail(
		recipient,
		"Your mailbox password was reset",
		"Mailbox Password Reset",
		[]string{
			"The password for " + fixtureMailbox + " was just reset via the break-glass recovery URL.",
			"If this was you, no action needed.",
			"If this was NOT you, contact postmaster@example.com immediately — the recovery URL can only be minted by an operator with SSH root on the pop-03 host.",
		},
	); err != nil {
		die(fmt.Errorf("mailbox-recover smoke: %w", err))
	}
	fmt.Printf("✓ 2/3 mailbox-recover confirmation sent to %s\n", recipient)

	// 3 — passkey-added alert (security alert with key:value table)
	if err := sendSystemMail(
		recipient,
		"New passkey added to your mailbox",
		"New Passkey Added",
		[]string{
			"A new passkey was just registered on " + fixtureMailbox + ":",
			"    name: smoke-test-key\n    time: " + time.Now().UTC().Format(time.RFC1123Z),
			"If you did this, no action needed.",
			"If this was NOT you, sign in immediately and revoke the passkey from Settings → Passkeys, then rotate your password.",
		},
	); err != nil {
		die(fmt.Errorf("passkey-added smoke: %w", err))
	}
	fmt.Printf("✓ 3/3 passkey-added alert sent to %s\n", recipient)
	fmt.Printf("  (all three use fixture mailbox %s — production callers substitute the real owner)\n", fixtureMailbox)
}

// adminCmdTestVerify dispatches a real forward-verification email to the
// recipient so we can eyeball the HTML+inline-logo rendering end-to-end.
// The mailbox owner is "smoke@example.com" — not a real account; the
// HMAC link in the email will be well-formed but useless if clicked.
// This is for visual smoke-testing of the noreply-styled email path only.
func adminCmdTestVerify(args []string) {
	if len(args) != 1 {
		die(fmt.Errorf("usage: ncn-mail admin test-verify <recipient>"))
	}
	recipient := args[0]
	secret, err := loadOrCreateSecret()
	if err != nil {
		die(err)
	}
	svc, err := newMailService(secret)
	if err != nil {
		die(err)
	}
	mailSvcGlobal = svc
	if err := sendForwardVerification("smoke@example.com", recipient); err != nil {
		die(err)
	}
	fmt.Printf("✓ forward-verify smoke email sent to %s\n", recipient)
	fmt.Printf("  (owner=smoke@example.com · the link in the email is well-formed but inert)\n")
}

// mailboxExistsInDovecot scans /etc/dovecot/users for a matching mailbox
// row. Lowercase-comparison; not a full parse.
func mailboxExistsInDovecot(mailbox string) bool {
	data, err := os.ReadFile(managedUsersPath)
	if err != nil {
		return false
	}
	prefix := strings.ToLower(mailbox) + ":"
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.ToLower(line), prefix) {
			return true
		}
	}
	return false
}

// loadAdminSet reads admins.json and returns the set of admin mailboxes
// (lowercased). Best-effort: returns an empty set on missing/malformed file.
func loadAdminSet() (map[string]struct{}, error) {
	out := make(map[string]struct{})
	data, err := os.ReadFile(adminsPath)
	if err != nil {
		return out, err
	}
	var af adminsFile
	if err := json.Unmarshal(data, &af); err != nil {
		return out, err
	}
	for _, a := range af.Admins {
		out[strings.ToLower(a)] = struct{}{}
	}
	return out, nil
}

// stdinReader is shared across multiple readSecret() calls so the bufio
// buffer survives between prompts. Without this, the SECOND call gets
// EOF because the first call's bufio buffer ate the second line and was
// then thrown away.
var stdinReader = bufio.NewReader(os.Stdin)

// readSecret reads a line without echo. Falls back to a shared bufio
// reader when stdin isn't a TTY (CI / pipes), printing a warning.
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
