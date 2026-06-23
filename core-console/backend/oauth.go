// OAuth / OIDC login (Google, Microsoft, GitHub) bound to operator accounts.
//
// Security model (see plan):
//   * OAuth = a FULL sign-in path, like passkey — a verified external identity
//     that is BOUND to an approved operator mints a session. The provider's own
//     MFA is the second factor.
//   * BIND-WHILE-LOGGED-IN ONLY: binding happens from /admin/security with a
//     live session; an UNBOUND external identity matches no operator and is
//     rejected. No self-provisioning.
//
// The auth-code flow reuses authStore.setSessionCookie (the same mint the
// password / passkey / SSO paths use) and mirrors auth_sso.go: verify → look
// up operator → setSessionCookie → audit → 302. State is single-use + 5-min
// TTL; google/microsoft additionally use PKCE (S256). Credentials come from
// /etc/ncn-core-console/oauth.env; a provider with no client_id/secret is
// gracefully disabled (like turnstile) and never affects other login methods.
//
// Telegram now uses its OIDC auth-code flow (BotFather Client ID/Secret) and is
// a regular provider here; identity comes from the id_token (decodeTelegramID-
// Token). oauth_telegram.go keeps only the bot-driven /bind ticket flow.

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const oauthStateTTL = 5 * time.Minute

type oauthProvider struct {
	Name         string
	Enabled      bool
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	UserinfoURL  string
	Scopes       []string
	UsePKCE      bool
}

type oauthState struct {
	Intent   string // "login" | "bind"
	Operator string // for bind
	Provider string
	Verifier string // PKCE code_verifier
	Exp      time.Time
}

type oauthService struct {
	auth         *authStore
	redirectBase string
	tgToken      string // for the Telegram Login Widget HMAC (oauth_telegram.go)
	tgBot        string // bot @username — needed to render the Login Widget
	tgBotID      string // numeric bot id (token before ':') — for Telegram.Login.auth
	providers    map[string]*oauthProvider
	client       *http.Client

	// tg, if wired (main.go), lets the bind-ticket flow DM a freshly-bound
	// Telegram user a confirmation — closing the /bind loop. May be nil.
	tg *tgNotifier

	mu     sync.Mutex
	states map[string]*oauthState
}

// newOAuthService loads provider config from env. Each provider is enabled only
// when both client id + secret are present.
func newOAuthService(auth *authStore, tgToken string) *oauthService {
	base := strings.TrimRight(getenvDefault("NCN_OAUTH_REDIRECT_BASE", "https://admin.example.com"), "/")
	s := &oauthService{
		auth:         auth,
		redirectBase: base,
		tgToken:      tgToken,
		tgBot:        strings.TrimPrefix(getenv("NCN_TG_BOT_USERNAME"), "@"),
		client:       &http.Client{Timeout: 10 * time.Second},
		states:       map[string]*oauthState{},
		providers:    map[string]*oauthProvider{},
	}
	if i := strings.IndexByte(tgToken, ':'); i > 0 {
		s.tgBotID = tgToken[:i] // numeric bot id for Telegram.Login.auth
	}
	mk := func(name, id, secret, authURL, tokenURL, userinfo string, scopes []string, pkce bool) {
		p := &oauthProvider{
			Name: name, ClientID: id, ClientSecret: secret,
			AuthURL: authURL, TokenURL: tokenURL, UserinfoURL: userinfo,
			Scopes: scopes, UsePKCE: pkce,
			Enabled: id != "" && secret != "",
		}
		s.providers[name] = p
		log.Printf("oauth: provider %s enabled=%v", name, p.Enabled)
	}
	// Only GitHub (OAuth2) + Telegram (oauth_telegram.go) are wired — Google /
	// Microsoft were dropped per ops decision. Re-add an mk(...) line to bring
	// a provider back.
	mk("github",
		getenv("NCN_OAUTH_GITHUB_CLIENT_ID"), getenv("NCN_OAUTH_GITHUB_CLIENT_SECRET"),
		"https://github.com/login/oauth/authorize", "https://github.com/login/oauth/access_token",
		"https://api.github.com/user", []string{"read:user", "user:email"}, false)
	// Telegram's new OIDC flow (BotFather → "OpenID Connect Login" → Client
	// ID/Secret). Standard auth-code + PKCE; identity arrives in the id_token
	// JWT (no userinfo endpoint), so UserinfoURL stays empty and the subject is
	// decoded from id_token below. The `id` claim is the numeric Telegram user
	// id — SAME value the old Login Widget stored — so existing bindings survive.
	mk("telegram",
		getenv("NCN_OAUTH_TELEGRAM_CLIENT_ID"), getenv("NCN_OAUTH_TELEGRAM_CLIENT_SECRET"),
		"https://oauth.telegram.org/auth", "https://oauth.telegram.org/token",
		"", []string{"openid", "profile"}, true)
	return s
}

func getenv(k string) string { return strings.TrimSpace(os.Getenv(k)) }
func getenvDefault(k, d string) string {
	if v := getenv(k); v != "" {
		return v
	}
	return d
}

func (svc *oauthService) redirectURI(provider string) string {
	return svc.redirectBase + "/api/v1/auth/oauth/" + provider + "/callback"
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func randB64(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b64url(b)
}

// newState creates + stores a single-use state (sweeping expired ones).
func (svc *oauthService) newState(intent, operator, provider, verifier string) string {
	st := randB64(24)
	svc.mu.Lock()
	now := time.Now()
	for k, v := range svc.states {
		if now.After(v.Exp) {
			delete(svc.states, k)
		}
	}
	svc.states[st] = &oauthState{Intent: intent, Operator: operator, Provider: provider, Verifier: verifier, Exp: now.Add(oauthStateTTL)}
	svc.mu.Unlock()
	return st
}

// consumeState returns + deletes a state (single-use). nil if missing/expired.
func (svc *oauthService) consumeState(st string) *oauthState {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	v, ok := svc.states[st]
	if !ok {
		return nil
	}
	delete(svc.states, st)
	if time.Now().After(v.Exp) {
		return nil
	}
	return v
}

// authCodeURL builds the provider authorize URL for a given state.
func (svc *oauthService) authCodeURL(p *oauthProvider, state, challenge string) string {
	q := url.Values{}
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", svc.redirectURI(p.Name))
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(p.Scopes, " "))
	q.Set("state", state)
	if p.UsePKCE && challenge != "" {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}
	return p.AuthURL + "?" + q.Encode()
}

// handleProviders — PUBLIC: which login providers are configured, for the
// login page to decide which buttons to render. Both github and telegram are
// now standard OAuth/OIDC redirect buttons (no Login Widget), so a provider is
// "enabled" iff its client id + secret are set.
func (svc *oauthService) handleProviders(w http.ResponseWriter, r *http.Request) {
	enabled := []string{}
	for _, name := range []string{"github", "telegram"} {
		if p, ok := svc.providers[name]; ok && p.Enabled {
			enabled = append(enabled, name)
		}
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"enabled": enabled}})
}

// routePublic dispatches the PUBLIC subtree /api/v1/auth/oauth/<provider>/<action>
// where action ∈ {start, callback}. (telegram/callback has its own exact route.)
func (svc *oauthService) routePublic(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/auth/oauth/"), "/")
	provider, action, _ := strings.Cut(rest, "/")
	switch action {
	case "start":
		svc.handleStart(w, r, provider)
	case "callback":
		svc.handleCallback(w, r, provider)
	default:
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "not found"})
	}
}

// routeBind dispatches the PROTECTED bind-start subtree
// /api/v1/auth/oauth-bind/<provider> (POST). telegram has a distinct flow.
func (svc *oauthService) routeBind(w http.ResponseWriter, r *http.Request) {
	provider := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/auth/oauth-bind/"), "/")
	svc.handleBindStart(w, r, provider)
}

// ── public: GET /api/v1/auth/oauth/{provider}/start  (intent=login) ──────────
func (svc *oauthService) handleStart(w http.ResponseWriter, r *http.Request, provider string) {
	p, ok := svc.providers[provider]
	if !ok || !p.Enabled {
		http.Redirect(w, r, "/login?oauth_err=provider-disabled", http.StatusFound)
		return
	}
	verifier, challenge := pkcePair(p.UsePKCE)
	state := svc.newState("login", "", provider, verifier)
	http.Redirect(w, r, svc.authCodeURL(p, state, challenge), http.StatusFound)
}

// ── protected: POST /api/v1/auth/oauth-bind/{provider} ───────────────────────
func (svc *oauthService) handleBindStart(w http.ResponseWriter, r *http.Request, provider string) {
	op := adminOperator(r)
	if op == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	p, ok := svc.providers[provider]
	if !ok || !p.Enabled {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "该登录方式未配置 / provider not configured"})
		return
	}
	verifier, challenge := pkcePair(p.UsePKCE)
	state := svc.newState("bind", op, provider, verifier)
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]string{"auth_url": svc.authCodeURL(p, state, challenge)}})
}

// ── public: GET /api/v1/auth/oauth/{provider}/callback ───────────────────────
func (svc *oauthService) handleCallback(w http.ResponseWriter, r *http.Request, provider string) {
	p, ok := svc.providers[provider]
	if !ok {
		http.Redirect(w, r, "/login?oauth_err=unknown-provider", http.StatusFound)
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		http.Redirect(w, r, "/login?oauth_err="+url.QueryEscape(e), http.StatusFound)
		return
	}
	st := svc.consumeState(r.URL.Query().Get("state"))
	if st == nil || st.Provider != provider {
		http.Redirect(w, r, "/login?oauth_err=bad-state", http.StatusFound)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, "/login?oauth_err=no-code", http.StatusFound)
		return
	}
	subject, email, avatar, err := svc.exchangeAndIdentify(r.Context(), p, code, st.Verifier)
	if err != nil {
		log.Printf("oauth %s: identify failed: %v", provider, err)
		svc.failRedirect(w, r, st, "exchange-failed")
		return
	}

	if st.Intent == "bind" {
		if err := svc.auth.bindIdentity(st.Operator, provider, subject, email); err != nil {
			auditRecord(r, AuditEvent{Event: "oauth.bind.fail", Severity: auditSevWarn, Actor: st.Operator, Outcome: "fail",
				Details: map[string]any{"provider": provider, "error": err.Error()}})
			http.Redirect(w, r, "/admin/security?tab=account&bind_err="+url.QueryEscape(err.Error()), http.StatusFound)
			return
		}
		svc.auth.setAvatar(st.Operator, avatar) // best-effort: capture provider profile picture
		auditRecord(r, AuditEvent{Event: "oauth.bind", Severity: auditSevWarn, Actor: st.Operator,
			Details: map[string]any{"provider": provider, "email": email}})
		http.Redirect(w, r, "/admin/security?tab=account&bound="+provider, http.StatusFound)
		return
	}

	// login
	username, found := svc.auth.findOperatorByIdentity(provider, subject)
	if !found || !svc.auth.operatorApproved(username) {
		auditRecord(r, AuditEvent{Event: "login.fail", Severity: auditSevWarn, Actor: username, Outcome: "fail",
			Details: map[string]any{"path": "oauth-" + provider, "reason": "not-bound-or-unapproved", "email": email}})
		http.Redirect(w, r, "/login?oauth_err=not-bound", http.StatusFound)
		return
	}
	if _, err := svc.auth.setSessionCookie(w, r, username); err != nil {
		http.Redirect(w, r, "/login?oauth_err=session", http.StatusFound)
		return
	}
	svc.auth.touchIdentity(username, provider, subject)
	svc.auth.setAvatar(username, avatar) // refresh the profile picture on each login
	auditRecord(r, AuditEvent{Event: "login.ok", Actor: username,
		Details: map[string]any{"path": "oauth-" + provider, "email": email}})
	http.Redirect(w, r, "/admin", http.StatusFound)
}

func (svc *oauthService) failRedirect(w http.ResponseWriter, r *http.Request, st *oauthState, reason string) {
	if st.Intent == "bind" {
		http.Redirect(w, r, "/admin/security?tab=account&bind_err="+reason, http.StatusFound)
		return
	}
	http.Redirect(w, r, "/login?oauth_err="+reason, http.StatusFound)
}

// exchangeAndIdentify trades the code for an access token and returns the
// provider's stable (subject, email).
func (svc *oauthService) exchangeAndIdentify(ctx context.Context, p *oauthProvider, code, verifier string) (subject, email, avatar string, err error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", svc.redirectURI(p.Name))
	form.Set("client_id", p.ClientID)
	form.Set("client_secret", p.ClientSecret)
	if p.UsePKCE && verifier != "" {
		form.Set("code_verifier", verifier)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json") // GitHub returns form-encoded otherwise
	resp, err := svc.client.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 300 {
		return "", "", "", fmt.Errorf("token endpoint %d: %s", resp.StatusCode, string(raw))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		return "", "", "", fmt.Errorf("token response parse")
	}
	// Telegram OIDC: no userinfo endpoint — identity is in the id_token JWT.
	if p.Name == "telegram" {
		return decodeTelegramIDToken(tok.IDToken)
	}
	if tok.AccessToken == "" {
		return "", "", "", fmt.Errorf("no access_token in response")
	}
	return svc.fetchIdentity(ctx, p, tok.AccessToken)
}

// decodeTelegramIDToken pulls the stable subject + display name out of
// Telegram's OIDC id_token. The token was just fetched server-to-server from
// oauth.telegram.org over TLS (auth-code flow), so — like our OIDC userinfo
// path — we trust the transport and decode the JWT payload without a separate
// signature check. subject = the `id` claim (numeric Telegram user id, the same
// value the legacy Login Widget stored); display = preferred_username / name.
func decodeTelegramIDToken(idToken string) (subject, display, avatar string, err error) {
	if idToken == "" {
		return "", "", "", fmt.Errorf("telegram: no id_token in token response")
	}
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return "", "", "", fmt.Errorf("telegram: malformed id_token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", "", fmt.Errorf("telegram: id_token payload decode: %w", err)
	}
	var claims struct {
		ID                json.Number `json:"id"`
		Sub               string      `json:"sub"`
		PreferredUsername string      `json:"preferred_username"`
		Name              string      `json:"name"`
		PhotoURL          string      `json:"photo_url"`
		Picture           string      `json:"picture"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", "", "", fmt.Errorf("telegram: id_token claims parse: %w", err)
	}
	subject = claims.ID.String()
	if subject == "" {
		subject = claims.Sub // fallback if a future payload omits `id`
	}
	if subject == "" {
		return "", "", "", fmt.Errorf("telegram: id_token missing id/sub")
	}
	display = claims.PreferredUsername
	if display == "" {
		display = claims.Name
	}
	avatar = claims.PhotoURL
	if avatar == "" {
		avatar = claims.Picture
	}
	return subject, display, avatar, nil
}

func (svc *oauthService) fetchIdentity(ctx context.Context, p *oauthProvider, accessToken string) (subject, email, avatar string, err error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.UserinfoURL, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := svc.client.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 300 {
		return "", "", "", fmt.Errorf("userinfo %d: %s", resp.StatusCode, string(raw))
	}
	if p.Name == "github" {
		var u struct {
			ID        int64  `json:"id"`
			Login     string `json:"login"`
			Email     string `json:"email"`
			AvatarURL string `json:"avatar_url"`
		}
		if err := json.Unmarshal(raw, &u); err != nil || u.ID == 0 {
			return "", "", "", fmt.Errorf("github user parse")
		}
		em := u.Email
		if em == "" {
			em = svc.githubPrimaryEmail(ctx, accessToken)
		}
		return strconv.FormatInt(u.ID, 10), em, u.AvatarURL, nil
	}
	// OIDC (google / microsoft): the userinfo `sub` is the stable subject.
	var oi struct {
		Sub     string `json:"sub"`
		Email   string `json:"email"`
		Picture string `json:"picture"`
	}
	if err := json.Unmarshal(raw, &oi); err != nil || oi.Sub == "" {
		return "", "", "", fmt.Errorf("oidc userinfo parse (no sub)")
	}
	return oi.Sub, oi.Email, oi.Picture, nil
}

func (svc *oauthService) githubPrimaryEmail(ctx context.Context, accessToken string) string {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user/emails", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := svc.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&emails)
	for _, e := range emails {
		if e.Primary && e.Verified {
			return e.Email
		}
	}
	return ""
}

// ── protected: GET / DELETE /api/v1/auth/oauth/identities[/{provider}] ───────
func (svc *oauthService) handleIdentities(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	if op == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		svc.auth.mu.RLock()
		rec := svc.auth.operators[op]
		svc.auth.mu.RUnlock()
		out := []map[string]string{}
		for _, ei := range rec.ExternalIdentities {
			out = append(out, map[string]string{"provider": ei.Provider, "email": ei.Email, "bound_at": ei.BoundAt, "last_used_at": ei.LastUsedAt})
		}
		// also advertise which providers are configured (for the bind buttons)
		enabled := []string{}
		for name, p := range svc.providers {
			if p.Enabled {
				enabled = append(enabled, name)
			}
		}
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"identities": out, "enabled_providers": enabled}})
	case http.MethodDelete:
		provider := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/auth/oauth-identities/"), "/")
		if provider == "" || strings.Contains(provider, "/") {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "provider required"})
			return
		}
		if err := svc.auth.unbindIdentity(op, provider); err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: err.Error()})
			return
		}
		auditRecord(r, AuditEvent{Event: "oauth.unbind", Severity: auditSevWarn, Actor: op, Details: map[string]any{"provider": provider}})
		writeJSON(w, http.StatusOK, envelope{OK: true})
	default:
		w.Header().Set("Allow", "GET, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func pkcePair(use bool) (verifier, challenge string) {
	if !use {
		return "", ""
	}
	verifier = randB64(32)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, b64url(sum[:])
}
