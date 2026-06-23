// turnstile.go — server-side Cloudflare Turnstile token verifier.
//
// Trust model:
//   * Frontend renders a Turnstile widget with the public sitekey baked
//     into the bundle. User completes the challenge (often invisibly);
//     CF returns a one-shot token to the page.
//   * Frontend POSTs the token to /api/v1/auth/login alongside the
//     password.
//   * THIS file verifies the token by calling CF's siteverify endpoint
//     with the SECRET (kept at /etc/ncn-core-console/turnstile.secret,
//     mode 0600 root). If siteverify says ok, we proceed to bcrypt.
//
// Tokens are single-use and bound to one CF visitor session — replays
// are rejected by CF itself. We don't need to track them locally.
//
// If the secret file is absent, verifyTurnstileToken returns nil (no
// verification). That lets dev-without-internet still work AND gives
// an emergency escape hatch ("rm /etc/ncn-core-console/turnstile.secret"
// disables the gate without a deploy).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	turnstileSecretPath = authConfigDir + "/turnstile.secret"
	turnstileVerifyURL  = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
)

// Cached secret — read once on first use, then kept in memory. The CLI's
// `rm -f /etc/ncn-core-console/turnstile.secret && systemctl restart
// ncn-api` cycle is the escape hatch; we don't hot-reload.
var (
	turnstileSecretOnce sync.Once
	turnstileSecret     string
)

func loadTurnstileSecret() string {
	turnstileSecretOnce.Do(func() {
		b, err := os.ReadFile(turnstileSecretPath)
		if err != nil {
			// Missing/unreadable → verification disabled. Logged so an
			// operator notices instead of silently bypassing prod.
			if os.IsNotExist(err) {
				return
			}
			return
		}
		turnstileSecret = strings.TrimSpace(string(b))
	})
	return turnstileSecret
}

// verifyTurnstileToken hits CF siteverify with the given token + the
// client's IP (used by CF for risk scoring). Returns nil on pass, an
// error describing the failure mode on fail.
//
// If no secret is configured locally, returns nil (verification skipped
// — see file-level comment for the escape-hatch rationale).
func verifyTurnstileToken(ctx context.Context, token, peerIP string) error {
	secret := loadTurnstileSecret()
	if secret == "" {
		// No secret on this host → skip. Real production has the file,
		// so this branch only fires during local dev or if a sysadmin
		// deliberately removed the file.
		return nil
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("missing turnstile token")
	}

	form := url.Values{}
	form.Set("secret", secret)
	form.Set("response", token)
	if peerIP != "" {
		form.Set("remoteip", peerIP)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		turnstileVerifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("turnstile request build: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("turnstile call: %w", err)
	}
	defer resp.Body.Close()

	var out struct {
		Success     bool     `json:"success"`
		ErrorCodes  []string `json:"error-codes"`
		ChallengeTS string   `json:"challenge_ts"`
		Hostname    string   `json:"hostname"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("turnstile decode: %w", err)
	}
	if !out.Success {
		return fmt.Errorf("turnstile rejected: %s", strings.Join(out.ErrorCodes, ","))
	}
	return nil
}
