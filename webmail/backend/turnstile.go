// turnstile.go — server-side Cloudflare Turnstile token verifier for the
// webmail login flow. Mirrors core-console/backend/turnstile.go.
//
// Trust model:
//   * Frontend renders a Turnstile widget with the public sitekey baked
//     into the bundle. User completes the challenge (often invisibly);
//     CF returns a one-shot token to the page.
//   * Frontend POSTs the token to /api/v1/mail/auth alongside the password.
//   * THIS file verifies the token by calling CF's siteverify endpoint
//     with the SECRET (kept at /etc/ncn-mail/turnstile.secret, mode 0600
//     root). If siteverify says ok, we proceed to bcrypt.
//
// Tokens are single-use and bound to one CF visitor session — replays
// are rejected by CF itself. We don't need to track them locally.
//
// If the secret file is absent, verifyTurnstileToken returns nil (no
// verification). That lets dev-without-internet still work AND gives
// an emergency escape hatch ("rm /etc/ncn-mail/turnstile.secret &&
// systemctl restart ncn-mail" disables the gate without a redeploy).
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
	turnstileSecretPath = stateDir + "/turnstile.secret"
	turnstileVerifyURL  = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
)

// Cached secret — read once on first use, then kept in memory. The
// escape hatch is "rm + systemctl restart ncn-mail"; we don't hot-reload.
var (
	turnstileSecretOnce sync.Once
	turnstileSecret     string
)

func loadTurnstileSecret() string {
	turnstileSecretOnce.Do(func() {
		b, err := os.ReadFile(turnstileSecretPath)
		if err != nil {
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
