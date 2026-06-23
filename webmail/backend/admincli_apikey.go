// admincli_apikey.go — `ncn-mail admin api-key …` and `gmail-setup`.
//
// Manages the transactional send API (api_send.go): mint/list/revoke the
// `ncntok_` bearer keys, and configure the optional Gmail relay credentials.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"
)

func adminCmdAPIKey(args []string) {
	if len(args) == 0 {
		apiKeyUsage()
		os.Exit(2)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create":
		adminCmdAPIKeyCreate(rest)
	case "list":
		adminCmdAPIKeyList()
	case "revoke":
		adminCmdAPIKeyRevoke(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown api-key subcommand: %q\n\n", sub)
		apiKeyUsage()
		os.Exit(2)
	}
}

func apiKeyUsage() {
	fmt.Fprintln(os.Stderr, `ncn-mail admin api-key — transactional send API keys

Usage:
  ncn-mail admin api-key create --mailbox <from> [--label "..."] \
                                [--transport local,gmail] [--expires-days N]
  ncn-mail admin api-key list
  ncn-mail admin api-key revoke <id>

A key is bound to ONE sender identity (--mailbox) and authorises the
POST /api/v1/mail/api/send endpoint to emit mail as that address over the
permitted transports. The raw token is shown ONCE at creation — store it
immediately (it is bcrypt-hashed at rest and cannot be recovered).

  transport local : injected into local postfix → rspamd DKIM-signs as
                    @example.com. No mailbox password needed.
  transport gmail : relayed via smtp.gmail.com using the credentials from
                    ` + gmailRelayPath + ` (see: ncn-mail admin gmail-setup).
                    The --mailbox must be a verified "send mail as" alias
                    on that Gmail account.`)
}

func adminCmdAPIKeyCreate(args []string) {
	fs := flag.NewFlagSet("api-key create", flag.ExitOnError)
	mailbox := fs.String("mailbox", "", "sender identity this key may send as (required)")
	label := fs.String("label", "", "human label for the key")
	transport := fs.String("transport", "local,gmail", "comma-list of allowed transports: local,gmail")
	expDays := fs.Int("expires-days", 0, "expire after N days (0 = never)")
	_ = fs.Parse(args)

	if strings.TrimSpace(*mailbox) == "" {
		fmt.Fprintln(os.Stderr, "error: --mailbox is required")
		os.Exit(2)
	}
	var transports []string
	for _, t := range strings.Split(*transport, ",") {
		t = strings.TrimSpace(strings.ToLower(t))
		if t == "" {
			continue
		}
		if t != "local" && t != "gmail" {
			die(fmt.Errorf("bad --transport %q (want local and/or gmail)", t))
		}
		transports = append(transports, t)
	}
	if len(transports) == 0 {
		die(fmt.Errorf("--transport produced no valid transports"))
	}
	var expiresAt int64
	if *expDays > 0 {
		expiresAt = time.Now().Add(time.Duration(*expDays) * 24 * time.Hour).Unix()
	}

	store, err := newAPITokenStore()
	if err != nil {
		die(err)
	}
	rec, raw, err := store.mint(*label, *mailbox, transports, expiresAt)
	if err != nil {
		die(err)
	}

	fmt.Println("API key created.")
	fmt.Printf("  id:         %s\n", rec.ID)
	fmt.Printf("  mailbox:    %s\n", rec.Mailbox)
	fmt.Printf("  transports: %s\n", strings.Join(rec.Transports, ", "))
	if rec.ExpiresAt > 0 {
		fmt.Printf("  expires:    %s\n", time.Unix(rec.ExpiresAt, 0).UTC().Format(time.RFC1123Z))
	}
	fmt.Println()
	fmt.Println("  ┌─────────────────────────────────────────────────────────────┐")
	fmt.Println("  │  TOKEN (shown once — copy it now):                            │")
	fmt.Printf("  │  %s\n", raw)
	fmt.Println("  └─────────────────────────────────────────────────────────────┘")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Printf("  curl -X POST https://%s/api/v1/mail/api/send \\\n", mailHost)
	fmt.Printf("    -H 'Authorization: Bearer %s' \\\n", raw)
	fmt.Println("    -H 'Content-Type: application/json' \\")
	fmt.Printf("    -d '{\"from\":\"%s\",\"to\":\"someone@example.com\",", rec.Mailbox)
	fmt.Println("\"subject\":\"Hi\",\"html\":\"<p>hello</p>\",\"transport\":\"local\"}'")
	fmt.Println()
	fmt.Println("⚠  This wrote " + apiTokensPath + " — run scripts/backup-secrets.sh + commit.")
}

func adminCmdAPIKeyList() {
	store, err := newAPITokenStore()
	if err != nil {
		die(err)
	}
	keys := store.list()
	sort.Slice(keys, func(i, j int) bool { return keys[i].CreatedAt < keys[j].CreatedAt })
	fmt.Printf("%-10s  %-26s  %-13s  %-16s  %s\n", "id", "mailbox", "transports", "last used", "label")
	fmt.Println(strings.Repeat("─", 92))
	if len(keys) == 0 {
		fmt.Println("(no API keys)")
		return
	}
	for _, k := range keys {
		last := "never"
		if k.LastUsedAt > 0 {
			last = time.Unix(k.LastUsedAt, 0).UTC().Format("2006-01-02 15:04")
		}
		exp := ""
		if k.ExpiresAt > 0 {
			exp = "  (expires " + time.Unix(k.ExpiresAt, 0).UTC().Format("2006-01-02") + ")"
		}
		fmt.Printf("%-10s  %-26s  %-13s  %-16s  %s%s\n",
			k.ID, k.Mailbox, strings.Join(k.Transports, ","), last, k.Label, exp)
	}
}

func adminCmdAPIKeyRevoke(args []string) {
	if len(args) < 1 {
		die(fmt.Errorf("usage: ncn-mail admin api-key revoke <id>"))
	}
	store, err := newAPITokenStore()
	if err != nil {
		die(err)
	}
	if err := store.revoke(args[0]); err != nil {
		die(err)
	}
	fmt.Printf("Revoked API key %s. (run scripts/backup-secrets.sh + commit)\n", args[0])
}

// adminCmdGmailSetup interactively writes /etc/ncn-mail/gmail-relay.json.
func adminCmdGmailSetup(args []string) {
	fs := flag.NewFlagSet("gmail-setup", flag.ExitOnError)
	username := fs.String("username", "", "the Gmail account address")
	allowed := fs.String("allowed-from", "", "comma-list of verified send-as identities (default: the username)")
	_ = fs.Parse(args)

	rd := bufio.NewReader(os.Stdin)
	user := strings.TrimSpace(*username)
	if user == "" {
		fmt.Print("Gmail account address: ")
		line, _ := rd.ReadString('\n')
		user = strings.TrimSpace(line)
	}
	if user == "" {
		die(fmt.Errorf("username required"))
	}

	fmt.Print("Gmail app password (16 chars, input hidden): ")
	pwBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		die(err)
	}
	appPw := strings.ReplaceAll(strings.TrimSpace(string(pwBytes)), " ", "")
	if appPw == "" {
		die(fmt.Errorf("app password required (Google Account → Security → App passwords)"))
	}

	var allowedFrom []string
	for _, a := range strings.Split(*allowed, ",") {
		a = strings.TrimSpace(a)
		if a != "" {
			allowedFrom = append(allowedFrom, a)
		}
	}
	if len(allowedFrom) == 0 {
		allowedFrom = []string{user}
	}

	cfg := gmailRelayConfig{
		Host:        "smtp.gmail.com",
		Port:        "587",
		Username:    user,
		AppPassword: appPw,
		AllowedFrom: allowedFrom,
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	tmp := gmailRelayPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		die(err)
	}
	if err := os.Rename(tmp, gmailRelayPath); err != nil {
		die(err)
	}
	fmt.Printf("Wrote %s (0600).\n", gmailRelayPath)
	fmt.Printf("  username:     %s\n", user)
	fmt.Printf("  allowed_from: %s\n", strings.Join(allowedFrom, ", "))
	fmt.Println()
	fmt.Println("NOTE: each allowed_from address must be a verified 'Send mail as'")
	fmt.Println("alias in that Gmail account (Settings → Accounts → Send mail as),")
	fmt.Println("otherwise Gmail rewrites From to the account address.")
	fmt.Println("⚠  Secret written — run scripts/backup-secrets.sh + commit.")
}
