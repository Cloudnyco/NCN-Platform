// ncn-login — CLI helper to authenticate to the NCN console.
//
// Three ways in:
//   - Default (no flags): open the console's web login page in a browser and
//     sign in however the web UI allows (OAuth / passkey / password).
//   - SSH-key signed (--user): sign a server challenge and open a one-shot
//     redeem URL. The key comes from ssh-agent if running, else ~/.ssh/id_*
//     (or -i <keyfile>) — no ssh-agent required; encrypted keys prompt.
//   - API token (--token): paste a personal token, saved to
//     ~/.config/ncn-cli/token so ncn-debug picks it up immediately.
//
// Usage:
//
//	ncn-login                        # open the web login page in a browser
//	ncn-login --print-url            # just print the login URL
//	ncn-login --user alice       # SSH-key signed login
//	ncn-login --user alice -i ~/.ssh/id_ed25519   # pick the key file
//	ncn-login --token                # paste an API token (saved for ncn-debug)
//
// --user defaults to $NCN_USER, --host to $NCN_HOST.
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
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/term"
)

// Defaults match the production admin host. Override with --host for
// staging / a forked deployment.
const defaultHost = "https://admin.example.com"

// Domain-separation tag — MUST match auth_ssh.go's sshLoginContext.
const sshLoginContext = "ncn-ssh-login\x00"

func main() {
	var (
		user      = flag.String("user", os.Getenv("NCN_USER"), "operator username (default $NCN_USER)")
		host      = flag.String("host", envOr("NCN_HOST", defaultHost), "admin console base URL (default $NCN_HOST)")
		keySel    = flag.String("key", "", "if several keys match, pick the one whose fingerprint or source contains this substring")
		identity  = flag.String("identity", "", "private key file to sign with (default: ssh-agent, then ~/.ssh/id_*)")
		printURL  = flag.Bool("print-url", false, "print the redeem URL instead of opening a browser")
		tokenMode = flag.Bool("token", false, "authenticate by pasting an API token (saved for ncn-debug) instead of SSH browser login")
		timeout   = flag.Duration("timeout", 20*time.Second, "HTTP timeout for each request")
	)
	flag.StringVar(identity, "i", "", "alias for --identity")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "ncn-login — authenticate to the NCN console")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  ncn-login                       open the web login page in a browser (default)")
		fmt.Fprintln(os.Stderr, "  ncn-login --user <name>         SSH-key signed login (key from agent or ~/.ssh, or -i)")
		fmt.Fprintln(os.Stderr, "  ncn-login --token               paste an API token (saved for ncn-debug)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		flag.PrintDefaults()
	}
	flag.Parse()

	base := strings.TrimRight(*host, "/")
	var err error
	switch {
	case *tokenMode:
		// Explicit: paste an API token.
		err = promptToken(base)
	case *user != "":
		// Username given → SSH-key signed login.
		err = run(*user, base, *keySel, *identity, *timeout, *printURL)
	default:
		// Bare `ncn-login` → browser login that hands a token back to the CLI.
		err = loopbackLogin(base, *printURL, *timeout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ncn-login: "+err.Error())
		os.Exit(1)
	}
}

// loopbackLogin runs a browser → CLI handoff (like `gh auth login`): it starts
// a localhost listener, opens the console login page with cli_auth params, and
// waits for the page (after you sign in by any method) to POST a freshly-minted
// API token back. The token is saved where ncn-debug looks.
func loopbackLogin(base string, noOpen bool, httpTimeout time.Duration) error {
	state, err := randHex(16)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start local listener: %w", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	tokenCh := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", base)
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var body struct{ State, Token string }
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if body.State != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		if !strings.HasPrefix(body.Token, "ncntok_") {
			http.Error(w, "not a token", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
		select {
		case tokenCh <- body.Token:
		default:
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	u := fmt.Sprintf("%s/cli-login?cli_port=%d&cli_state=%s", base, port, state)
	if noOpen {
		fmt.Println(u)
	} else {
		fmt.Fprintf(os.Stderr, "ncn-login: opening %s\n", u)
		if err := openBrowser(u); err != nil {
			fmt.Fprintln(os.Stderr, "ncn-login: couldn't open a browser; visit this URL manually:")
			fmt.Println(u)
		}
	}
	fmt.Fprintln(os.Stderr, "ncn-login: waiting for the browser login to finish… (Ctrl-C to cancel)")

	wait := 3 * time.Minute
	if httpTimeout > wait {
		wait = httpTimeout
	}
	select {
	case tok := <-tokenCh:
		return saveToken(tok)
	case <-time.After(wait):
		return errors.New("timed out waiting for the browser login")
	}
}

// randHex returns n random bytes as a hex string.
func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// promptToken reads an API token (hidden) and saves it where ncn-debug looks.
func promptToken(base string) error {
	fmt.Printf("Paste an API token from %s → Security → API Tokens.\n", base)
	fmt.Print("token (ncntok_…): ")
	var tok string
	if b, err := term.ReadPassword(int(os.Stdin.Fd())); err == nil {
		fmt.Println()
		tok = string(b)
	} else {
		// not a terminal — fall back to a plain line read
		sc := bufio.NewScanner(os.Stdin)
		if sc.Scan() {
			tok = sc.Text()
		}
	}
	return saveToken(tok)
}

// saveToken writes the token to ~/.config/ncn-cli/token (0600) — the same file
// ncn-debug resolves, so a token entered here works there immediately.
func saveToken(tok string) error {
	tok = strings.TrimSpace(tok)
	if !strings.HasPrefix(tok, "ncntok_") {
		return errors.New("that doesn't look like an API token (expected ncntok_… prefix)")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	p := filepath.Join(dir, "ncn-cli", "token")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Printf("saved API token to %s — ncn-debug will use it.\n", p)
	return nil
}

// signCandidate is one key we could sign with — from ssh-agent or a file. The
// sign closure is invoked lazily, so a file key's passphrase is only ever
// requested for the key that actually matches the server.
type signCandidate struct {
	fp   string
	desc string
	sign func(data []byte) (*ssh.Signature, error)
}

// gatherCandidates collects signing keys from ssh-agent (if running) and from
// disk (the -i path, else ~/.ssh/id_*). Returns the candidates and a cleanup
// func to close any agent connection.
func gatherCandidates(identity string) ([]signCandidate, func()) {
	var cs []signCandidate
	var closers []func()
	seen := map[string]bool{}

	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			ag := agent.NewClient(conn)
			if keys, err := ag.List(); err == nil && len(keys) > 0 {
				closers = append(closers, func() { conn.Close() })
				for _, k := range keys {
					fp := ssh.FingerprintSHA256(k)
					if seen[fp] {
						continue
					}
					seen[fp] = true
					kk := k
					cs = append(cs, signCandidate{fp: fp, desc: "ssh-agent", sign: func(d []byte) (*ssh.Signature, error) {
						pub, err := ssh.ParsePublicKey(kk.Marshal())
						if err != nil {
							return nil, err
						}
						return ag.Sign(pub, d)
					}})
				}
			} else {
				conn.Close()
			}
		}
	}

	var paths []string
	if identity != "" {
		paths = []string{identity}
	} else {
		paths = defaultKeyPaths()
	}
	for _, p := range paths {
		fp, ok := pubFingerprint(p)
		if !ok || seen[fp] {
			continue
		}
		seen[fp] = true
		pp := p
		cs = append(cs, signCandidate{fp: fp, desc: pp, sign: func(d []byte) (*ssh.Signature, error) {
			signer, err := loadPrivateKey(pp)
			if err != nil {
				return nil, err
			}
			return signWith(signer, d)
		}})
	}
	return cs, func() {
		for _, c := range closers {
			c()
		}
	}
}

// defaultKeyPaths returns the ~/.ssh/id_* private keys that exist.
func defaultKeyPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var out []string
	for _, n := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		p := filepath.Join(home, ".ssh", n)
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// pubFingerprint computes a key's SHA256 fingerprint without decrypting it,
// preferring the .pub sidecar and falling back to an unencrypted private key.
func pubFingerprint(path string) (string, bool) {
	if b, err := os.ReadFile(path + ".pub"); err == nil {
		if pk, _, _, _, err := ssh.ParseAuthorizedKey(b); err == nil {
			return ssh.FingerprintSHA256(pk), true
		}
	}
	if b, err := os.ReadFile(path); err == nil {
		if signer, err := ssh.ParsePrivateKey(b); err == nil {
			return ssh.FingerprintSHA256(signer.PublicKey()), true
		}
	}
	return "", false
}

// loadPrivateKey parses a private key, prompting (hidden) for a passphrase if
// the key is encrypted.
func loadPrivateKey(path string) (ssh.Signer, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err == nil {
		return signer, nil
	}
	var need *ssh.PassphraseMissingError
	if errors.As(err, &need) {
		fmt.Printf("passphrase for %s: ", path)
		pw, rerr := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if rerr != nil {
			return nil, rerr
		}
		return ssh.ParsePrivateKeyWithPassphrase(pem, pw)
	}
	return nil, err
}

// signWith signs data, upgrading RSA keys to rsa-sha2-256 (servers reject the
// legacy ssh-rsa/SHA-1 algorithm).
func signWith(signer ssh.Signer, data []byte) (*ssh.Signature, error) {
	if signer.PublicKey().Type() == ssh.KeyAlgoRSA {
		if as, ok := signer.(ssh.AlgorithmSigner); ok {
			return as.SignWithAlgorithm(rand.Reader, data, ssh.KeyAlgoRSASHA256)
		}
	}
	return signer.Sign(rand.Reader, data)
}

func run(user, host, keySel, identity string, timeout time.Duration, printURL bool) error {
	client := &http.Client{Timeout: timeout}
	base := strings.TrimRight(host, "/")

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

	// ── 3. Gather candidate keys (ssh-agent + on-disk) and match one ────
	wanted := make(map[string]struct{ Type, Label string })
	for _, fp := range beginData.Fingerprints {
		wanted[fp.Fingerprint] = struct{ Type, Label string }{fp.Type, fp.Label}
	}

	cands, closeAgents := gatherCandidates(identity)
	defer closeAgents()
	if len(cands) == 0 {
		return errors.New("no SSH key found — no ssh-agent key and no ~/.ssh/id_* file.\n" +
			"  pass -i <keyfile>, or just use `ncn-login --token` (no SSH needed)")
	}

	var chosen *signCandidate
	for i := range cands {
		c := &cands[i]
		if _, ok := wanted[c.fp]; !ok {
			continue
		}
		// --key pins which one to use when several match (by fp or source).
		if keySel != "" && !strings.Contains(c.fp, keySel) && !strings.Contains(c.desc, keySel) {
			continue
		}
		chosen = c
		break
	}
	if chosen == nil {
		fmt.Fprintln(os.Stderr, "ncn-login: none of your local keys is registered for this operator")
		fmt.Fprintln(os.Stderr, "  you have:")
		for _, c := range cands {
			fmt.Fprintf(os.Stderr, "    %s  (%s)\n", c.fp, c.desc)
		}
		fmt.Fprintln(os.Stderr, "  server accepts:")
		for _, fp := range beginData.Fingerprints {
			fmt.Fprintf(os.Stderr, "    %s  %q\n", fp.Fingerprint, fp.Label)
		}
		return errors.New("register one of your keys in Security → SSH Keys, or use `ncn-login --token`")
	}

	// ── 4. Sign (context || challenge) with the chosen key ─────────────
	signed := append([]byte(beginData.Context), challengeBytes...)
	meta := wanted[chosen.fp]
	fmt.Fprintf(os.Stderr, "ncn-login: signing with %q (%s)\n", meta.Label, chosen.desc)
	sig, err := chosen.sign(signed)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	chosenFP := chosen.fp

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

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
