// ncn-login — CLI helper for SSH-signed login to admin.example.com.
//
// Usage:
//
//	ncn-login --user alice
//	ncn-login --user alice --host https://admin.example.com  # override target
//	ncn-login --user alice --print-url                       # print, don't open browser
//
// What it does:
//  1. POSTs /api/v1/auth/ssh-login/begin {operator} to get a challenge
//     plus the list of pubkey fingerprints this operator can sign with.
//  2. Asks the local ssh-agent (via $SSH_AUTH_SOCK) which of its keys
//     matches any of those fingerprints, and signs the (context‖challenge)
//     payload with the first match.
//  3. POSTs /api/v1/auth/ssh-login/finish with the signature; receives a
//     one-shot redeem URL.
//  4. Opens that URL in the user's browser (or prints it for them).
//
// The whole flow is ~5 seconds end-to-end and doesn't require typing a
// password, copying a TOTP code, or touching a browser-extension passkey
// UI. The trade-off is that ssh-agent must be running with the relevant
// key loaded — typically a non-event for ops users.
//
// Build:
//
//	cd cli/ncn-login && go build -o ncn-login
//
// Cross-compile for other platforms:
//
//	GOOS=darwin GOARCH=arm64 go build -o ncn-login-darwin-arm64
//	GOOS=linux  GOARCH=amd64 go build -o ncn-login-linux-amd64
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Defaults match the production admin host. Override with --host for
// staging / a forked deployment.
const defaultHost = "https://admin.example.com"

// Domain-separation tag — MUST match auth_ssh.go's sshLoginContext.
const sshLoginContext = "ncn-ssh-login\x00"

func main() {
	var (
		user     = flag.String("user", "", "operator username (required)")
		host     = flag.String("host", defaultHost, "admin console base URL")
		printURL = flag.Bool("print-url", false, "print the redeem URL instead of opening a browser")
		timeout  = flag.Duration("timeout", 20*time.Second, "HTTP timeout for each request")
	)
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "ncn-login — SSH-signed login to admin.example.com")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  ncn-login --user <name> [--host URL] [--print-url]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Prerequisites:")
		fmt.Fprintln(os.Stderr, "  - ssh-agent must be running ($SSH_AUTH_SOCK set)")
		fmt.Fprintln(os.Stderr, "  - the key you want to use must be loaded (`ssh-add -l` to check)")
		fmt.Fprintln(os.Stderr, "  - the corresponding public key must be registered in admin.example.com's")
		fmt.Fprintln(os.Stderr, "    Security panel → SSH Keys section")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *user == "" {
		fmt.Fprintln(os.Stderr, "error: --user is required")
		flag.Usage()
		os.Exit(2)
	}

	if err := run(*user, *host, *timeout, *printURL); err != nil {
		fmt.Fprintln(os.Stderr, "ncn-login: "+err.Error())
		os.Exit(1)
	}
}

func run(user, host string, timeout time.Duration, printURL bool) error {
	client := &http.Client{Timeout: timeout}
	base := strings.TrimRight(host, "/")

	// ── 1. Connect to ssh-agent ─────────────────────────────────────────
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return errors.New("SSH_AUTH_SOCK not set — start ssh-agent and `ssh-add` your key first")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return fmt.Errorf("dial ssh-agent at %s: %w", sock, err)
	}
	defer conn.Close()
	ag := agent.NewClient(conn)

	agentKeys, err := ag.List()
	if err != nil {
		return fmt.Errorf("list agent keys: %w", err)
	}
	if len(agentKeys) == 0 {
		return errors.New("ssh-agent has no keys loaded — run `ssh-add ~/.ssh/id_ed25519` (or similar)")
	}

	// ── 2. /begin: ask server for challenge + accepted fingerprints ────
	fmt.Fprintf(os.Stderr, "ncn-login: starting flow for operator=%s\n", user)

	beginResp, err := jsonPost(client, base+"/api/v1/auth/ssh-login/begin",
		map[string]any{"operator": user})
	if err != nil {
		return fmt.Errorf("POST /ssh-login/begin: %w", err)
	}
	var beginData struct {
		ChallengeID  string `json:"challenge_id"`
		ChallengeB64 string `json:"challenge_b64"`
		Context      string `json:"context"`
		Fingerprints []struct {
			Fingerprint string `json:"fingerprint"`
			Type        string `json:"type"`
			Label       string `json:"label"`
		} `json:"fingerprints"`
		ExpiresAt int64 `json:"expires_at"`
	}
	if err := json.Unmarshal(beginResp, &beginData); err != nil {
		return fmt.Errorf("parse /begin response: %w", err)
	}
	if len(beginData.Fingerprints) == 0 {
		return fmt.Errorf("server returned no registered SSH keys for operator=%s — register one in the Security panel first", user)
	}
	challengeBytes, err := base64.StdEncoding.DecodeString(beginData.ChallengeB64)
	if err != nil {
		return fmt.Errorf("decode challenge: %w", err)
	}
	// Trust the server's context value rather than baking it in — lets
	// us bump the domain tag server-side without re-releasing the CLI.
	if beginData.Context == "" {
		beginData.Context = sshLoginContext
	}

	// ── 3. Pick a matching agent key ────────────────────────────────────
	wanted := make(map[string]struct {
		Type, Label string
	})
	for _, fp := range beginData.Fingerprints {
		wanted[fp.Fingerprint] = struct{ Type, Label string }{fp.Type, fp.Label}
	}

	var (
		chosen   *agent.Key
		chosenFP string
	)
	for _, k := range agentKeys {
		fp := ssh.FingerprintSHA256(k)
		if meta, ok := wanted[fp]; ok {
			chosen = k
			chosenFP = fp
			fmt.Fprintf(os.Stderr, "ncn-login: signing with %q (%s, %s)\n", meta.Label, meta.Type, fp[:24]+"…")
			break
		}
	}
	if chosen == nil {
		fmt.Fprintln(os.Stderr, "ncn-login: no agent key matches a registered SSH key for this operator")
		fmt.Fprintln(os.Stderr, "  agent has:")
		for _, k := range agentKeys {
			fmt.Fprintf(os.Stderr, "    %s  %s\n", ssh.FingerprintSHA256(k), k.Comment)
		}
		fmt.Fprintln(os.Stderr, "  server accepts:")
		for _, fp := range beginData.Fingerprints {
			fmt.Fprintf(os.Stderr, "    %s  %q\n", fp.Fingerprint, fp.Label)
		}
		return errors.New("add one of the agent keys to admin.example.com, or `ssh-add` the registered one")
	}

	// ── 4. Sign (context || challenge) with the agent ──────────────────
	signed := append([]byte(beginData.Context), challengeBytes...)
	pubFromAgent, err := ssh.ParsePublicKey(chosen.Marshal())
	if err != nil {
		return fmt.Errorf("parse agent pubkey: %w", err)
	}
	sig, err := ag.Sign(pubFromAgent, signed)
	if err != nil {
		return fmt.Errorf("agent sign: %w", err)
	}

	// ── 5. /finish: send signature, receive redeem URL ────────────────
	finishResp, err := jsonPost(client, base+"/api/v1/auth/ssh-login/finish",
		map[string]any{
			"challenge_id":     beginData.ChallengeID,
			"fingerprint":      chosenFP,
			"signature_format": sig.Format,
			"signature_b64":    base64.StdEncoding.EncodeToString(sig.Blob),
		})
	if err != nil {
		return fmt.Errorf("POST /ssh-login/finish: %w", err)
	}
	var finishData struct {
		RedeemURL string `json:"redeem_url"`
		ExpiresAt int64  `json:"expires_at"`
		Operator  string `json:"operator"`
	}
	if err := json.Unmarshal(finishResp, &finishData); err != nil {
		return fmt.Errorf("parse /finish response: %w", err)
	}
	full := base + finishData.RedeemURL
	ttl := time.Until(time.Unix(finishData.ExpiresAt, 0)).Round(time.Second)
	fmt.Fprintf(os.Stderr, "ncn-login: redeem URL valid for %s\n", ttl)

	// ── 6. Either open the browser, or just print the URL ─────────────
	if printURL {
		fmt.Println(full)
		return nil
	}
	if err := openBrowser(full); err != nil {
		fmt.Fprintln(os.Stderr, "ncn-login: couldn't open a browser; visit this URL manually:")
		fmt.Println(full)
		return nil
	}
	fmt.Fprintln(os.Stderr, "ncn-login: opened browser — finish in there.")
	return nil
}

// jsonPost wraps the request/response/error-decode boilerplate. The
// envelope shape ({ok, data, error, ts}) matches every other ncn-api
// endpoint; we surface `error` on non-ok and return raw `data` on ok.
func jsonPost(c *http.Client, url string, body any) ([]byte, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ncn-login/1.0 ("+runtime.GOOS+"-"+runtime.GOARCH+")")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var env struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.Unmarshal(respBody, &env); err != nil {
		return nil, fmt.Errorf("non-JSON response (HTTP %d): %s",
			resp.StatusCode, truncate(string(respBody), 200))
	}
	if !env.OK {
		if env.Error == "" {
			env.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, errors.New(env.Error)
	}
	return env.Data, nil
}

// openBrowser tries the platform-native "open this URL" command.
// Non-fatal: if we can't open the browser the caller prints the URL.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default: // linux + others
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
