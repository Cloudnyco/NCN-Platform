// idp_provider.go — a minimal OAuth2 authorization-code provider so the console
// is the single sign-on Identity Provider for our other apps (Wiki.js first).
// "Use our login page": an app redirects the browser to /api/v1/auth/idp/authorize;
// if the operator already has a console session we mint a code, otherwise we
// bounce to the console's own /login (next=…) and come back. The app then
// exchanges the code at /token and reads the operator identity at /userinfo.
//
// Only an authenticated console operator (bound + approved — the console login
// already enforces that) can ever obtain a code, so the downstream app can
// safely trust every user it receives. Codes + access tokens are in-memory and
// short-lived (a restart just forces a re-login). Single registered client,
// configured via env (NCN_WIKI_OAUTH_CLIENT_ID / _SECRET / _REDIRECT).
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type idpCode struct {
	sub, redirectURI string
	exp              time.Time
}
type idpAccess struct {
	sub string
	exp time.Time
}

type idpProvider struct {
	auth         *authStore
	clientID     string
	clientSecret string
	redirectURI  string
	mu           sync.Mutex
	codes        map[string]idpCode
	tokens       map[string]idpAccess
}

func newIDPProvider(auth *authStore) *idpProvider {
	return &idpProvider{
		auth:         auth,
		clientID:     strings.TrimSpace(os.Getenv("NCN_WIKI_OAUTH_CLIENT_ID")),
		clientSecret: strings.TrimSpace(os.Getenv("NCN_WIKI_OAUTH_CLIENT_SECRET")),
		redirectURI:  strings.TrimSpace(os.Getenv("NCN_WIKI_OAUTH_REDIRECT")),
		codes:        map[string]idpCode{},
		tokens:       map[string]idpAccess{},
	}
}

func (p *idpProvider) enabled() bool {
	return p.clientID != "" && p.clientSecret != "" && p.redirectURI != ""
}

func idpRandToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (p *idpProvider) gc() {
	now := time.Now()
	for k, v := range p.codes {
		if now.After(v.exp) {
			delete(p.codes, k)
		}
	}
	for k, v := range p.tokens {
		if now.After(v.exp) {
			delete(p.tokens, k)
		}
	}
}

// GET /api/v1/auth/idp/authorize — require a console session, else bounce to /login.
func (p *idpProvider) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != p.clientID || q.Get("response_type") != "code" {
		http.Error(w, "invalid_request", http.StatusBadRequest)
		return
	}
	if q.Get("redirect_uri") != p.redirectURI {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	// Console session?
	var claims *sessionClaims
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		claims, _ = p.auth.verifyToken(c.Value)
	}
	if claims == nil {
		// Not logged in → send them to OUR login page, return here after.
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	code := idpRandToken(24)
	p.mu.Lock()
	p.gc()
	p.codes[code] = idpCode{sub: claims.Sub, redirectURI: p.redirectURI, exp: time.Now().Add(5 * time.Minute)}
	p.mu.Unlock()

	u, _ := url.Parse(p.redirectURI)
	rq := u.Query()
	rq.Set("code", code)
	if st := q.Get("state"); st != "" {
		rq.Set("state", st)
	}
	u.RawQuery = rq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// POST /api/v1/auth/idp/token — exchange the code (client-authenticated).
func (p *idpProvider) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	_ = r.ParseForm()
	cid, csec := r.FormValue("client_id"), r.FormValue("client_secret")
	if cid == "" {
		if u, pw, ok := r.BasicAuth(); ok {
			cid, csec = u, pw
		}
	}
	if cid != p.clientID || subtle.ConstantTimeCompare([]byte(csec), []byte(p.clientSecret)) != 1 {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "invalid_client"})
		return
	}
	if r.FormValue("grant_type") != "authorization_code" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "unsupported_grant_type"})
		return
	}
	code := r.FormValue("code")
	p.mu.Lock()
	cc, ok := p.codes[code]
	if ok {
		delete(p.codes, code) // single-use
	}
	p.mu.Unlock()
	if !ok || time.Now().After(cc.exp) || cc.redirectURI != r.FormValue("redirect_uri") {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid_grant"})
		return
	}
	at := idpRandToken(32)
	p.mu.Lock()
	p.tokens[at] = idpAccess{sub: cc.sub, exp: time.Now().Add(time.Hour)}
	p.mu.Unlock()
	// Raw OAuth2 token response (not our envelope — the client expects this shape).
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(`{"access_token":"` + at + `","token_type":"Bearer","expires_in":3600}`))
}

// GET /api/v1/auth/idp/userinfo — operator identity for the bearer token.
func (p *idpProvider) handleUserinfo(w http.ResponseWriter, r *http.Request) {
	at := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if at == "" {
		at = r.URL.Query().Get("access_token")
	}
	p.mu.Lock()
	ac, ok := p.tokens[at]
	p.mu.Unlock()
	if !ok || time.Now().After(ac.exp) {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "invalid_token"})
		return
	}
	name, email := ac.sub, ac.sub+"@acme.local"
	p.auth.mu.RLock()
	if op, exists := p.auth.operators[ac.sub]; exists {
		if op.BotNick != "" {
			name = op.BotNick
		}
		for _, ei := range op.ExternalIdentities {
			if ei.Email != "" {
				email = ei.Email
				break
			}
		}
	}
	p.auth.mu.RUnlock()
	// Flat OIDC-style userinfo (NOT our envelope — the OAuth2 client expects this shape).
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sub": ac.sub, "name": name, "preferred_username": ac.sub, "email": email,
	})
}
