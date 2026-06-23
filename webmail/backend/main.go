// ncn-mail — standalone webmail backend for mail.example.com.
//
// Single Go process running on pop-03 alongside postfix + dovecot. Listens on
// 127.0.0.1:9000 (no public exposure — nginx fronts it). Talks to dovecot
// over loopback on :993 and postfix on :587 with TLS + SASL.
//
// No coupling to example.com's operator console — this binary is its own thing.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// ----------------------------------------------------------------------------
// Wire types — shared between handlers.
// ----------------------------------------------------------------------------

type envelope struct {
	OK    bool        `json:"ok"`
	Data  interface{} `json:"data,omitempty"`
	Error string      `json:"error,omitempty"`
	TS    string      `json:"ts"`
}

func writeJSON(w http.ResponseWriter, status int, env envelope) {
	env.TS = time.Now().UTC().Format(time.RFC3339Nano)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

// ----------------------------------------------------------------------------
// Session secret — single master key (32 random bytes) at sessionSecretPath.
// All session/cookie/encryption keys are HKDF-derived from this.
// ----------------------------------------------------------------------------

const (
	stateDir          = "/etc/ncn-mail"
	sessionSecretPath = stateDir + "/session.key"
)

func loadOrCreateSecret() ([]byte, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(sessionSecretPath); err == nil && len(data) >= 32 {
		return data, nil
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	if err := os.WriteFile(sessionSecretPath, secret, 0o600); err != nil {
		return nil, err
	}
	log.Printf("session: generated new secret → %s", sessionSecretPath)
	return secret, nil
}

// ----------------------------------------------------------------------------
// HTTP middleware — CORS-permissive (dev) + access log.
// ----------------------------------------------------------------------------

func withMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
		w.Header().Set("Access-Control-Max-Age", "600")
		w.Header().Set("X-NCN-Service", "ncn-mail")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		started := time.Now()
		next(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(started))
	}
}

// ----------------------------------------------------------------------------

func main() {
	// Break-glass mailbox CLI. When invoked as `ncn-mail admin <subcmd>`,
	// short-circuit before any flag/network init so the binary can also
	// act as the mailbox-management tool over plain SSH. Lives in
	// admincli.go.
	if len(os.Args) >= 2 && os.Args[1] == "admin" {
		adminCLIEntrypoint(os.Args[2:])
		return
	}

	addr := flag.String("addr", "127.0.0.1:9000", "listen address")
	flag.Parse()

	secret, err := loadOrCreateSecret()
	if err != nil {
		log.Fatalf("session secret: %v", err)
	}

	mailSvc, err := newMailService(secret)
	if err != nil {
		log.Fatalf("mail service init: %v", err)
	}
	// Expose globally so non-mailService code paths can wipe stash on
	// password reset. See forgetStashEverywhere() docstring.
	mailSvcGlobal = mailSvc

	inviteSvc, err := newInviteStore()
	if err != nil {
		log.Fatalf("invite store init: %v", err)
	}

	passkeySvc, err := newPasskeyService(mailSvc)
	if err != nil {
		log.Fatalf("passkey init: %v", err)
	}

	forgotSvc, err := newForgotStore(inviteSvc)
	if err != nil {
		log.Fatalf("forgot store init: %v", err)
	}

	ssoSvc := newSSOService(mailSvc, inviteSvc)

	// Transactional send API (Resend-style): bearer `ncntok_` keys →
	// POST /api/v1/mail/api/send, local or gmail transport. The token store
	// may be absent at boot (created on first `api-key create`).
	apiTokenSvc, err := newAPITokenStore()
	if err != nil {
		log.Fatalf("api-token store init: %v", err)
	}
	globalAPITokens = apiTokenSvc
	apiSendSvc := newAPISendService(apiTokenSvc)

	// Break-glass mailbox recovery via signed URL minted by `ncn-mail
	// admin mint-recover`. Key may be absent at boot — created on first
	// mint — so this can never fail fatally.
	mailboxRecover, err := newMailboxRecoverService()
	if err != nil {
		log.Printf("mailbox-recover init: %v (continuing without)", err)
	}

	mux := http.NewServeMux()

	mailReq := func(h http.HandlerFunc) http.HandlerFunc {
		return withMiddleware(mailSvc.requireAuth(h))
	}

	mux.HandleFunc("/api/v1/mail/auth", withMiddleware(mailSvc.handleAuth))
	mux.HandleFunc("/api/v1/mail/logout", withMiddleware(mailSvc.handleLogout))
	mux.HandleFunc("/api/v1/mail/me", mailReq(mailSvc.handleMe))
	mux.HandleFunc("/api/v1/mail/folders", mailReq(mailSvc.handleFolders))
	mux.HandleFunc("/api/v1/mail/folders/", mailReq(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search") && r.Method == http.MethodGet {
			mailSvc.handleSearch(w, r)
			return
		}
		mailSvc.handleListMessages(w, r)
	}))
	mux.HandleFunc("/api/v1/mail/messages/", mailReq(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/flag") && r.Method == http.MethodPost:
			mailSvc.handleFlag(w, r)
		case strings.HasSuffix(r.URL.Path, "/move") && r.Method == http.MethodPost:
			mailSvc.handleMove(w, r)
		case strings.Contains(r.URL.Path, "/attachments/") && r.Method == http.MethodGet:
			mailSvc.handleAttachmentDownload(w, r)
		case r.Method == http.MethodGet:
			mailSvc.handleReadMessage(w, r)
		case r.Method == http.MethodDelete:
			mailSvc.handleDelete(w, r)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET/DELETE/POST only"})
		}
	}))
	mux.HandleFunc("/api/v1/mail/send", mailReq(mailSvc.handleSend))

	// Transactional send API — bearer-token auth (NOT cookie), so it gets
	// withMiddleware (CORS/logging) but not requireAuth; the handler does
	// its own `Authorization: Bearer ncntok_…` check.
	mux.HandleFunc("/api/v1/mail/api/send", withMiddleware(apiSendSvc.handleSend))

	// Server-Sent Events stream for new-mail push (IMAP IDLE backed).
	mux.HandleFunc("/api/v1/mail/events", mailReq(mailSvc.handleEvents))

	// Drafts (autosave path)
	mux.HandleFunc("/api/v1/mail/draft", mailReq(mailSvc.handleDraftSave))
	mux.HandleFunc("/api/v1/mail/draft/", mailReq(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "DELETE only"})
			return
		}
		mailSvc.handleDraftDelete(w, r)
	}))

	// Invite system. Admin endpoints (require mail session + admin list
	// membership), plus two public ones authenticated by token possession.
	mux.HandleFunc("/api/v1/mail/invites", mailReq(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			inviteSvc.handleList(w, r)
		case http.MethodPost:
			inviteSvc.handleCreate(w, r)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET or POST"})
		}
	}))
	mux.HandleFunc("/api/v1/mail/invites/", mailReq(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "DELETE only"})
			return
		}
		inviteSvc.handleRevoke(w, r)
	}))
	mux.HandleFunc("/api/v1/mail/invite/preview", withMiddleware(inviteSvc.handlePreview))
	mux.HandleFunc("/api/v1/mail/invite/complete", withMiddleware(inviteSvc.handleComplete))

	// Forwarding (Sieve script per user)
	mux.HandleFunc("/api/v1/mail/forward", mailReq(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			mailSvc.handleForwardGet(w, r)
		case http.MethodPut:
			mailSvc.handleForwardPut(w, r)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET or PUT only"})
		}
	}))
	// Public — verify a forward-address ownership token. Anonymous: trust
	// comes from the HMAC signature in the URL.
	mux.HandleFunc("/api/v1/mail/forward/verify", withMiddleware(mailSvc.handleForwardVerify))

	// Admin password reset
	mux.HandleFunc("/api/v1/mail/admin/reset-password", mailReq(inviteSvc.handleAdminResetPassword))

	// Break-glass mailbox recovery (signed URL minted by `ncn-mail admin
	// mint-recover` on pop-03). Public route — trust comes from the HMAC
	// signature, not from a session.
	if mailboxRecover != nil {
		mux.HandleFunc("/api/v1/mail/admin/bootstrap-recover",         withMiddleware(mailboxRecover.handleSubmit))
		mux.HandleFunc("/api/v1/mail/admin/bootstrap-recover/preview", withMiddleware(mailboxRecover.handlePreview))
	}

	// Admin-console-driven role mailbox recovery. Reuses the existing
	// operator-bridge.key for signing — when ncn-api on tyo wants to mint
	// a recovery URL for postmaster/noc/etc, it POSTs here with an
	// X-Bridge-Sig header and we mint a mb- token + return the URL.
	mux.HandleFunc("/api/v1/mail/admin/role-recover", withMiddleware(inviteSvc.handleRoleRecover))

	// Forgot-password request queue (anonymous submit, admin list/dismiss).
	mux.HandleFunc("/api/v1/mail/forgot/request", withMiddleware(forgotSvc.handleRequest))
	mux.HandleFunc("/api/v1/mail/forgot/requests", mailReq(forgotSvc.handleList))
	// All /requests/<id>… paths dispatch through handleByID which branches
	// on suffix: DELETE …/<id> → dismiss; POST …/<id>/approve → mint URL +
	// email + remove. Both admin-only (gate inside handleByID).
	mux.HandleFunc("/api/v1/mail/forgot/requests/", mailReq(forgotSvc.handleByID))

	// Operator-bridge: same queue, but accessible to the admin console on tyo
	// via the operator-mail-bridge.key HMAC instead of a mailbox session.
	mux.HandleFunc("/api/v1/mail/admin/forgot-bridge/list",    withMiddleware(forgotSvc.handleBridgeList))
	mux.HandleFunc("/api/v1/mail/admin/forgot-bridge/dismiss", withMiddleware(forgotSvc.handleBridgeDismiss))
	mux.HandleFunc("/api/v1/mail/admin/forgot-bridge/approve", withMiddleware(forgotSvc.handleBridgeApprove))

	// Mutual SSO with admin.example.com. Both endpoints share the
	// operator-mail-bridge.key HMAC. handleIngest is GET (browser-redirected
	// from admin); handleMintAdminTicket needs a live mailbox session.
	mux.HandleFunc("/api/v1/mail/sso/ingest",        withMiddleware(ssoSvc.handleIngest))
	mux.HandleFunc("/api/v1/mail/sso/admin-ticket",  mailReq(ssoSvc.handleMintAdminTicket))

	// Privacy image proxy — every external <img src> in an HTML email is
	// rewritten by parseMessage to /api/v1/mail/img-proxy?u=<b64url>. The
	// proxy fetches the upstream once (per browser cache), strips request
	// fingerprints, and streams bytes back so the sender can't track
	// per-user opens. SSRF defense inside the handler.
	mux.HandleFunc("/api/v1/mail/img-proxy",         mailReq(mailSvc.handleImgProxy))

	// Bridge endpoint: admin operators on tyo dispatch arbitrary noreply-
	// styled system mail through here (HMAC-signed). Used by the peering-
	// application flow and any future admin-initiated notification.
	sendBridge := newSendBridgeService(mailSvc, inviteSvc)
	mux.HandleFunc("/api/v1/mail/admin/send-bridge",  withMiddleware(sendBridge.handleSend))

	// WebAuthn passkey login
	mux.HandleFunc("/api/v1/mail/passkey/register/begin", mailReq(passkeySvc.handleRegBegin))
	mux.HandleFunc("/api/v1/mail/passkey/register/finish", mailReq(passkeySvc.handleRegFinish))
	mux.HandleFunc("/api/v1/mail/passkey/login/begin", withMiddleware(passkeySvc.handleLoginBegin))
	mux.HandleFunc("/api/v1/mail/passkey/login/finish", withMiddleware(passkeySvc.handleLoginFinish))
	mux.HandleFunc("/api/v1/mail/passkey", mailReq(passkeySvc.handleList))
	mux.HandleFunc("/api/v1/mail/passkey/", mailReq(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "DELETE only"})
			return
		}
		passkeySvc.handleDelete(w, r)
	}))

	// Trivial health endpoint — handy for systemd readiness checks.
	mux.HandleFunc("/api/v1/mail/health", withMiddleware(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: "ok"})
	}))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      75 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("ncn-mail listening on %s", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
