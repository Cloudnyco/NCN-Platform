// WebSocket SSH/PTY terminal — the "修网" emergency shell.
//
// Defense in depth (in order an attacker would have to defeat them):
//
//  1. session cookie (8h TTL, HttpOnly, host-bound to admin.example.com)
//  2. same-origin WebSocket check on upgrade (CSWSH block)
//  3. /api/v1/term/ticket — POST + MFA step-up (passkey assertion OR TOTP
//     code; the session cookie already proves password). Rate-limited:
//     ≤ 5 MFA failures per 15 min per operator; lockout returns 429.
//  4. ticket — 16-byte random, single-use, 30s TTL, operator+node bound.
//  5. one concurrent session per operator (refuses opens while another live).
//  6. PTY idle-disconnect after 15 min no I/O; hard cap 8h.
//  7. asciinema v2 transcript per session in /var/log/ncn-term-sessions/,
//     mode 0600, root-only. captures every byte in + out for forensics.
//  8. journald audit start/heartbeat/end with operator id, peer IP,
//     bytes counters, exit reason.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/gorilla/websocket"
)

// --- Tuning constants. Conservative defaults; nudge after operational
//     experience.
const (
	termTicketTTL       = 30 * time.Second
	termIdleTimeout     = 15 * time.Minute
	termMaxSession      = 8 * time.Hour

	// WebSocket keepalive. Mobile carrier NATs and corporate proxies drop
	// idle TCP connections silently — typically 30-180s of no traffic. The
	// drop reaches neither end cleanly, so the next write surfaces as a
	// browser WS close with code 1006 ("abnormal closure"). To prevent that
	// we have the server send PING frames at a cadence well under any
	// reasonable NAT timeout; the browser auto-replies with a PONG, the NAT
	// sees traffic and refreshes its mapping, and we also use the pong as
	// liveness evidence (a missing pong within `termWSPongWait` triggers a
	// clean ctx cancel + close instead of a stale connection).
	termWSPingPeriod = 20 * time.Second
	termWSPongWait   = 60 * time.Second
	termMaxConcurrent   = 1
	termTicketRateMax   = 5
	termTicketRateWindow = 15 * time.Minute
	termAuditDir        = "/var/log/ncn-term-sessions"
)

// --- Ticket store ---------------------------------------------------------

type termTicket struct {
	Operator  string
	Node      string
	NotBefore time.Time
	Expires   time.Time
}

var (
	termTickets   = map[string]*termTicket{}
	termTicketsMu sync.Mutex
)

// gcTerminalTicketsLocked must be called with termTicketsMu held. Sweeps
// expired tickets and decrements the per-operator pending counter so a
// ticket the user requested but never used releases its slot at expiry.
func gcTerminalTicketsLocked(now time.Time) {
	for k, t := range termTickets {
		if now.After(t.Expires) {
			delete(termTickets, k)
			if termPendingTickets[t.Operator] > 0 {
				termPendingTickets[t.Operator]--
			}
		}
	}
}

// Per-operator brute-force limiter for /term/ticket. Only FAILED password
// attempts consume a slot — successful auth never blocks legitimate work.
// Reuses the existing newRateLimiter() from ratelimit.go.
var termPwLimit = newRateLimiter(termTicketRateMax, termTicketRateWindow)

// --- Active sessions (concurrency cap + list endpoint) --------------------

type termSession struct {
	ID        string
	Operator  string
	Node      string
	Peer      string
	StartedAt time.Time
	Cancel    context.CancelFunc

	// counters updated by the I/O loop
	bytesOut *atomic.Int64
	bytesIn  *atomic.Int64
	lastIO   *atomic.Int64 // unix ms of last byte either direction
}

var (
	termSessions   = map[string]*termSession{} // id → session
	termSessionsMu sync.Mutex
)

func termSessionsCountFor(operator string) int {
	termSessionsMu.Lock()
	defer termSessionsMu.Unlock()
	return termSessionsCountForLocked(operator)
}

func termSessionsCountForLocked(operator string) int {
	n := 0
	for _, s := range termSessions {
		if s.Operator == operator {
			n++
		}
	}
	return n
}

// termSessionReserve atomically checks the concurrency cap AND, if under,
// claims a slot by registering the session. Returns false if the operator
// already hit the cap. Closes the TOCTOU window between the cap check and
// the WS upgrade — two tabs racing to open a shell can't both win.
func termSessionReserve(s *termSession, max int) bool {
	termSessionsMu.Lock()
	defer termSessionsMu.Unlock()
	if termSessionsCountForLocked(s.Operator) >= max {
		return false
	}
	termSessions[s.ID] = s
	return true
}

func termSessionUnregister(id string) {
	termSessionsMu.Lock()
	defer termSessionsMu.Unlock()
	delete(termSessions, id)
}

// Track outstanding (minted, unconsumed) tickets per operator so a quick
// double-click on "open shell" can't mint 2 tickets whose WS upgrades
// later land within the TOCTOU window. Decremented on ticket consume,
// expire-reap, or grant failure.
var termPendingTickets = map[string]int{}

func termPendingFor(operator string) int {
	termTicketsMu.Lock()
	defer termTicketsMu.Unlock()
	return termPendingTickets[operator]
}

// --- WebSocket upgrade ----------------------------------------------------

type termCtrlMsg struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

var termUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     sameOriginCheck,
}

func sameOriginCheck(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

// --- handleTermTicket -----------------------------------------------------

// POST /api/v1/term/passkey-begin — issues a WebAuthn assertion challenge
// scoped to the CURRENT logged-in operator's registered credentials. Used
// as the MFA step-up at terminal-open time. Caller then runs
// navigator.credentials.get() and submits the response in the `passkey`
// field of /api/v1/term/ticket.
func (f *fleetScraper) handleTermPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	claims, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	if f.auth == nil || f.auth.wa == nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "webauthn not configured"})
		return
	}
	f.auth.mu.RLock()
	op, exists := f.auth.operators[claims.Sub]
	f.auth.mu.RUnlock()
	if !exists || len(op.Passkeys) == 0 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "no passkey registered for this operator"})
		return
	}
	options, sess, err := f.auth.wa.wa.BeginLogin(webauthnUser{op: op})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	challengeID := f.auth.wa.put("term-stepup", claims.Sub, sess)
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"challenge_id": challengeID,
		"options":      options.Response,
	}})
}

func (f *fleetScraper) handleTermTicket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	claims, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}

	var req struct {
		Node string `json:"node"`
		// MFA step-up — supply exactly ONE of these. The session cookie
		// already proves password possession, so we don't re-ask for it.
		TOTPCode string `json:"totp_code,omitempty"`
		Passkey  *struct {
			ChallengeID string          `json:"challenge_id"`
			Response    json.RawMessage `json:"response"`
		} `json:"passkey,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}

	if _, ok := f.lookupNode(req.Node); !ok {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "unknown node"})
		return
	}

	if f.auth == nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "auth not wired"})
		return
	}
	f.auth.mu.RLock()
	op, exists := f.auth.operators[claims.Sub]
	f.auth.mu.RUnlock()
	if !exists {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "operator gone"})
		return
	}
	// === MFA step-up ===
	// The session cookie already proves password possession (any cookie
	// holder either typed it on /login or completed a passkey sign-in).
	// We require ONE of: passkey assertion OR TOTP code. Refuse outright
	// if the operator hasn't bound any MFA — should never happen since
	// /admin/onboarding forces enrollment at first login, but defense in
	// depth.
	hasPasskey := len(op.Passkeys) > 0
	hasTOTP := op.TOTPSecret != ""
	if !hasPasskey && !hasTOTP {
		writeJSON(w, http.StatusForbidden, envelope{OK: false,
			Error: "MFA required — bind a passkey or TOTP at /admin/onboarding first"})
		return
	}

	mfaSatisfied := false
	mfaMethod := ""

	if req.Passkey != nil && hasPasskey {
		if f.auth.wa == nil {
			writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "webauthn not configured"})
			return
		}
		p := f.auth.wa.take(req.Passkey.ChallengeID)
		if p == nil || p.Kind != "term-stepup" || p.User != claims.Sub {
			log.Printf("term: TICKET-FAIL operator=%s peer=%s · passkey challenge invalid/expired",
				claims.Sub, clientAddr(r))
			writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "passkey challenge expired"})
			return
		}
		parsed, perr := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(req.Passkey.Response))
		if perr != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "passkey parse: " + perr.Error()})
			return
		}
		cred, verr := f.auth.wa.wa.ValidateLogin(webauthnUser{op: op}, *p.Session, parsed)
		if verr != nil {
			log.Printf("term: TICKET-FAIL operator=%s peer=%s · passkey assertion failed: %v",
				claims.Sub, clientAddr(r), verr)
			writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "passkey verification failed"})
			return
		}
		// Update sign count + BackupState — needs write lock.
		f.auth.mu.Lock()
		fresh, exists2 := f.auth.operators[claims.Sub]
		if exists2 {
			for i := range fresh.Passkeys {
				if bytes.Equal(fresh.Passkeys[i].ID, cred.ID) {
					fresh.Passkeys[i].SignCount = cred.Authenticator.SignCount
					fresh.Passkeys[i].Flags.BackupState = cred.Flags.BackupState
					break
				}
			}
			f.auth.operators[claims.Sub] = fresh
			_ = f.auth.persistLocked()
		}
		f.auth.mu.Unlock()
		mfaSatisfied = true
		mfaMethod = "passkey"
	} else if req.TOTPCode != "" && hasTOTP {
		if !verifyTOTP(op.TOTPSecret, req.TOTPCode, time.Now()) {
			// Slow & count toward lockout — TOTP brute-force is a real path.
			time.Sleep(500 * time.Millisecond)
			allowed, retryAfter := termPwLimit.Allow(claims.Sub)
			if !allowed {
				w.Header().Set("Retry-After", fmt.Sprintf("%.0f", retryAfter.Seconds()))
				writeJSON(w, http.StatusTooManyRequests, envelope{OK: false,
					Error: fmt.Sprintf("too many MFA failures; locked for %.1fm", retryAfter.Minutes())})
				return
			}
			log.Printf("term: TICKET-FAIL operator=%s peer=%s · TOTP code mismatch",
				claims.Sub, clientAddr(r))
			writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "TOTP code incorrect"})
			return
		}
		mfaSatisfied = true
		mfaMethod = "totp"
	}

	if !mfaSatisfied {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: "MFA verification required — supply totp_code or passkey assertion"})
		return
	}
	_ = mfaMethod // method captured below in log line

	// Concurrency cap — count BOTH live sessions AND outstanding (minted but
	// unconsumed) tickets so a rapid double-click can't slip past while a
	// ticket is still in flight. Done atomically with ticket mint.
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "rand failed"})
		return
	}
	tk := base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now()

	termTicketsMu.Lock()
	gcTerminalTicketsLocked(now)  // also adjusts pending counter
	pending := termPendingTickets[claims.Sub]
	live := termSessionsCountFor(claims.Sub) // separate mutex — order matters
	if pending+live >= termMaxConcurrent {
		termTicketsMu.Unlock()
		log.Printf("term: TICKET-DENY operator=%s peer=%s · pending=%d live=%d",
			claims.Sub, clientAddr(r), pending, live)
		writeJSON(w, http.StatusConflict, envelope{OK: false,
			Error: fmt.Sprintf("you already have %d active session(s) (or %d pending tickets) — disconnect first", live, pending)})
		return
	}
	termTickets[tk] = &termTicket{
		Operator:  claims.Sub,
		Node:      req.Node,
		NotBefore: now,
		Expires:   now.Add(termTicketTTL),
	}
	termPendingTickets[claims.Sub]++
	termTicketsMu.Unlock()

	log.Printf("term: TICKET    operator=%s node=%s peer=%s mfa=%s · valid %.0fs",
		claims.Sub, req.Node, clientAddr(r), mfaMethod, termTicketTTL.Seconds())
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"ticket":     tk,
		"expires_in": int(termTicketTTL.Seconds()),
	}})
}

// --- handleTerm (WS) ------------------------------------------------------

func (f *fleetScraper) handleTerm(w http.ResponseWriter, r *http.Request) {
	claims, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if claims == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	operator := claims.Sub
	peer := clientAddr(r)

	ticket := r.URL.Query().Get("ticket")
	if ticket == "" {
		http.Error(w, "missing ticket", http.StatusUnauthorized)
		return
	}
	termTicketsMu.Lock()
	tk, ok := termTickets[ticket]
	if ok {
		delete(termTickets, ticket)
		// Consuming a ticket releases its "pending" slot — the slot will be
		// re-claimed below by the registered termSession instead.
		if termPendingTickets[tk.Operator] > 0 {
			termPendingTickets[tk.Operator]--
		}
	}
	termTicketsMu.Unlock()
	if !ok {
		http.Error(w, "ticket invalid or already used", http.StatusUnauthorized)
		return
	}
	if time.Now().After(tk.Expires) {
		http.Error(w, "ticket expired", http.StatusUnauthorized)
		return
	}
	if tk.Operator != operator {
		http.Error(w, "ticket operator mismatch", http.StatusUnauthorized)
		return
	}

	nodeID := r.URL.Query().Get("node")
	if nodeID == "" {
		nodeID = tk.Node
	}
	if nodeID != tk.Node {
		http.Error(w, "ticket node mismatch", http.StatusUnauthorized)
		return
	}
	target, ok := f.lookupNode(nodeID)
	if !ok {
		http.Error(w, "unknown node", http.StatusBadRequest)
		return
	}

	// Reserve the concurrency slot ATOMICALLY against the live session
	// table. This is the only place that can race with another WS upgrade,
	// so the lock-acquire here is the source of truth — count and insert
	// happen under the same mutex.
	sid := operator + "-" + ticket[:8]
	pendingSess := &termSession{
		ID: sid, Operator: operator, Node: nodeID, Peer: peer,
		StartedAt: time.Now(),
		bytesOut:  &atomic.Int64{},
		bytesIn:   &atomic.Int64{},
		lastIO:    &atomic.Int64{},
	}
	pendingSess.lastIO.Store(pendingSess.StartedAt.UnixMilli())
	if !termSessionReserve(pendingSess, termMaxConcurrent) {
		http.Error(w, "concurrent session limit reached", http.StatusConflict)
		return
	}
	// On any abort path below, release the reserved slot.
	registered := true
	defer func() {
		if registered {
			termSessionUnregister(sid)
		}
	}()

	ws, err := termUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	// Two-step close: send a Close control frame so the browser reports the
	// configured close code (e.g. 1000 normal) instead of 1006 (abnormal —
	// "no Close frame received"). Without this, every clean session end —
	// including a bash `exit` / Ctrl+D, an idle-kill, a max-session cap, or
	// the user clicking "disconnect" — looked to the client like an
	// unexpected network failure, because gorilla's bare ws.Close() just
	// drops TCP without an application-layer close handshake.
	//
	// We try CloseNormalClosure with a short message; if the peer is already
	// gone (network died), the WriteControl errors out silently and we move
	// on to the actual TCP close. Either way, ws.Close() runs.
	defer func() {
		_ = ws.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session ended"),
			time.Now().Add(2*time.Second),
		)
		_ = ws.Close()
	}()

	// Build the command. Unified SSH launch — even the local node (ctrl-01)
	// goes through `ssh root@127.0.0.1` rather than a bare `/bin/bash -l`
	// child. The previous bare-bash path inherited ncn-api's working
	// directory (`/opt/ncn-core-console`) and skipped PAM, so operators
	// landed in the repo dir with no MOTD; on every other PoP they got a
	// normal `Last login:` + MOTD + cwd=$HOME. The SSH-to-self path now
	// gives ctrl-01 the same entry experience as pop-03 / pop-04 / pop-05.
	// The fleet-key is already trusted in /root/.ssh/authorized_keys on
	// every node (including ctrl-01 itself), so no extra setup is needed.
	//
	// Honour per-node SSH overrides — pop-03 logs in as `debian` with
	// `sudo -n` for privileged ops, not root. sshUser() / sshIdentity()
	// fall back to the fleet defaults (root + fleet-key) when no override.
	sshAddr := target.Address
	if target.Local {
		// Localhost loopback. Avoids routing the connection back through
		// the public address (which would also work, but is wasteful and
		// drags in firewall / NAT considerations).
		sshAddr = "127.0.0.1"
	}
	cmd := exec.Command("ssh",
		"-tt",
		"-p", strconv.Itoa(target.sshPort()),
		"-i", target.sshIdentity(),
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=/etc/ncn-core-console/fleet-known-hosts",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=2",
		fmt.Sprintf("%s@%s", target.sshUser(), sshAddr),
	)
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
	)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		ws.WriteMessage(websocket.BinaryMessage,
			[]byte(fmt.Sprintf("\r\n[term] failed to start: %v\r\n", err)))
		return
	}
	defer func() {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}()

	startedAt := time.Now()
	pid := -1
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}

	// --- Audit transcript (asciinema cast v2) ---
	cast, castPath, castErr := openCastFile(operator, target.ID, pid, startedAt)
	if castErr != nil {
		log.Printf("term: WARN audit transcript open failed (%v) — session will proceed without recording", castErr)
	} else {
		defer func() { _ = cast.Close() }()
	}

	// Reuse the session slot reserved BEFORE the upgrade (see termSessionReserve
	// above). Update the now-known startedAt + Cancel onto it.
	sess := pendingSess
	sess.StartedAt = startedAt
	bytesOut := sess.bytesOut
	bytesIn := sess.bytesIn
	lastIO := sess.lastIO
	lastIO.Store(startedAt.UnixMilli())

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	sess.Cancel = cancel

	// All WebSocket writes go through this mutex. gorilla/websocket is NOT
	// safe for concurrent writers — without this, the pty→ws goroutine,
	// the watchdog goroutine, and the ping sender racing to send can
	// corrupt frames or panic.
	var wsMu sync.Mutex
	wsSend := func(t int, data []byte) error {
		wsMu.Lock()
		defer wsMu.Unlock()
		return ws.WriteMessage(t, data)
	}
	// WriteControl (used for PING) is also not concurrency-safe with
	// WriteMessage; route it through the same mutex.
	wsWriteControl := func(t int, data []byte, deadline time.Time) error {
		wsMu.Lock()
		defer wsMu.Unlock()
		return ws.WriteControl(t, data, deadline)
	}

	// --- Keepalive setup --------------------------------------------------
	// SetReadDeadline + SetPongHandler turn the reader goroutine into a
	// liveness detector: if no pong arrives within termWSPongWait the next
	// ReadMessage returns an error, which cancels ctx and tears down the
	// session cleanly (no half-open zombie). Each pong (and any incoming
	// frame, since gorilla resets the deadline implicitly only when you do
	// it from the handler) refreshes the deadline.
	_ = ws.SetReadDeadline(time.Now().Add(termWSPongWait))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(termWSPongWait))
	})

	log.Printf("term: START operator=%s node=%s peer=%s pid=%d sid=%s transcript=%s",
		operator, nodeID, peer, pid, sid, castPath)

	banner := fmt.Sprintf(
		"\x1b[32m[ncn-term]\x1b[0m connected · operator=%s · node=%s · session=%s\r\n"+
			"\x1b[90m[ncn-term] idle disconnect after %.0fm · session cap %.0fh · transcript recorded\x1b[0m\r\n",
		operator, target.ID, sid, termIdleTimeout.Minutes(), termMaxSession.Hours())
	_ = wsSend(websocket.BinaryMessage, []byte(banner))
	castWrite(cast, startedAt, "o", []byte(banner))

	// pty → ws
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				bytesOut.Add(int64(n))
				lastIO.Store(time.Now().UnixMilli())
				castWrite(cast, startedAt, "o", buf[:n])
				if werr := wsSend(websocket.BinaryMessage, buf[:n]); werr != nil {
					log.Printf("term: WS-WRITE-FAIL sid=%s pty→ws err=%v", sid, werr)
					cancel()
					return
				}
			}
			if rerr != nil {
				if !errors.Is(rerr, io.EOF) {
					log.Printf("term: PTY-READ-FAIL sid=%s err=%v", sid, rerr)
					_ = wsSend(websocket.BinaryMessage,
						[]byte(fmt.Sprintf("\r\n[term] read error: %v\r\n", rerr)))
				} else {
					log.Printf("term: PTY-EOF sid=%s (shell exited)", sid)
				}
				cancel()
				return
			}
		}
	}()

	// ws → pty
	go func() {
		for {
			mt, data, err := ws.ReadMessage()
			if err != nil {
				log.Printf("term: WS-READ-FAIL sid=%s ws→pty err=%v", sid, err)
				cancel()
				return
			}
			// Any inbound frame (data OR a control frame indirectly via the
			// pong handler) is liveness evidence. Refresh the deadline so
			// an actively-typing operator never gets idle-killed by the
			// keepalive layer even if a few pongs are lost in transit.
			_ = ws.SetReadDeadline(time.Now().Add(termWSPongWait))
			switch mt {
			case websocket.BinaryMessage, websocket.TextMessage:
				if mt == websocket.TextMessage && len(data) > 0 && data[0] == '{' {
					var c termCtrlMsg
					if json.Unmarshal(data, &c) == nil {
						if c.Type == "resize" && c.Cols > 0 && c.Rows > 0 {
							_ = pty.Setsize(ptmx, &pty.Winsize{Rows: c.Rows, Cols: c.Cols})
							continue
						}
					}
				}
				bytesIn.Add(int64(len(data)))
				lastIO.Store(time.Now().UnixMilli())
				castWrite(cast, startedAt, "i", data)
				if _, werr := ptmx.Write(data); werr != nil {
					cancel()
					return
				}
			}
		}
	}()

	// Ping sender — emits a PING control frame every termWSPingPeriod. The
	// browser auto-replies with PONG (no application code needed on the
	// client). The cadence is well under any reasonable carrier NAT idle
	// timeout, so the connection has visible traffic and the NAT mapping
	// stays alive. The first PingMessage write that fails (e.g. the WS is
	// already in a bad state) cancels the session.
	go func() {
		t := time.NewTicker(termWSPingPeriod)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := wsWriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
					log.Printf("term: WS-PING-FAIL sid=%s err=%v", sid, err)
					cancel()
					return
				}
			}
		}
	}()

	// Idle + max-session watchdog
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				lastMs := lastIO.Load()
				idleFor := time.Duration(time.Now().UnixMilli()-lastMs) * time.Millisecond
				if idleFor > termIdleTimeout {
					msg := fmt.Sprintf("\r\n\x1b[33m[ncn-term] disconnecting — idle %.0fm > limit %.0fm\x1b[0m\r\n",
						idleFor.Minutes(), termIdleTimeout.Minutes())
					_ = wsSend(websocket.BinaryMessage, []byte(msg))
					castWrite(cast, startedAt, "o", []byte(msg))
					log.Printf("term: IDLE-KILL operator=%s sid=%s idle=%.0fm", operator, sid, idleFor.Minutes())
					cancel()
					return
				}
				if time.Since(startedAt) > termMaxSession {
					msg := fmt.Sprintf("\r\n\x1b[33m[ncn-term] disconnecting — session age %.1fh > cap %.0fh\x1b[0m\r\n",
						time.Since(startedAt).Hours(), termMaxSession.Hours())
					_ = wsSend(websocket.BinaryMessage, []byte(msg))
					castWrite(cast, startedAt, "o", []byte(msg))
					log.Printf("term: CAP-KILL operator=%s sid=%s age=%.1fh", operator, sid, time.Since(startedAt).Hours())
					cancel()
					return
				}
			}
		}
	}()

	// 5-min heartbeat to journald
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				log.Printf("term: HEARTBEAT operator=%s sid=%s pid=%d secs=%.0f bytesOut=%d bytesIn=%d",
					operator, sid, pid, time.Since(startedAt).Seconds(),
					bytesOut.Load(), bytesIn.Load())
			}
		}
	}()

	<-ctx.Done()
	log.Printf("term: END   operator=%s node=%s peer=%s pid=%d sid=%s secs=%.1f bytesOut=%d bytesIn=%d transcript=%s",
		operator, nodeID, peer, pid, sid, time.Since(startedAt).Seconds(),
		bytesOut.Load(), bytesIn.Load(), castPath)
}

// --- /api/v1/term/sessions ------------------------------------------------

// handleTermSessions lists every currently-open terminal session. The
// authenticated operator only sees their own sessions (an `admin` role
// could see all — out of scope for the single-operator setup).
func (f *fleetScraper) handleTermSessions(w http.ResponseWriter, r *http.Request) {
	claims, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}

	termSessionsMu.Lock()
	defer termSessionsMu.Unlock()

	now := time.Now()
	type row struct {
		ID         string `json:"id"`
		Node       string `json:"node"`
		Peer       string `json:"peer"`
		StartedAt  int64  `json:"started_at"`
		AgeSecs    int    `json:"age_secs"`
		IdleSecs   int    `json:"idle_secs"`
		BytesOut   int64  `json:"bytes_out"`
		BytesIn    int64  `json:"bytes_in"`
	}
	out := []row{}
	for _, s := range termSessions {
		if s.Operator != claims.Sub {
			continue
		}
		lastMs := s.lastIO.Load()
		out = append(out, row{
			ID:        s.ID,
			Node:      s.Node,
			Peer:      s.Peer,
			StartedAt: s.StartedAt.Unix(),
			AgeSecs:   int(now.Sub(s.StartedAt).Seconds()),
			IdleSecs:  int(time.Duration(now.UnixMilli()-lastMs) * time.Millisecond / time.Second),
			BytesOut:  s.bytesOut.Load(),
			BytesIn:   s.bytesIn.Load(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt < out[j].StartedAt })
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
}

// --- Audit transcript (asciinema cast v2 format) --------------------------
//
// One file per session at /var/log/ncn-term-sessions/<ts>-<operator>-<node>-pidN.cast.
// Mode 0600, root-only. Replay with: asciinema play <file>
// (Also human-readable as line-per-event JSON.)
//
// Format (https://docs.asciinema.org/manual/asciicast/v2/):
//   line 1: {"version": 2, "width": ..., "height": ..., "timestamp": ...}
//   line 2..N: [<seconds_since_start>, "o"|"i", "<utf8 chunk>"]

func openCastFile(operator, node string, pid int, started time.Time) (*os.File, string, error) {
	if err := os.MkdirAll(termAuditDir, 0o700); err != nil {
		return nil, "", err
	}

	// `chattr +a` the directory so its entries can't be unlinked from inside
	// any shell — even a root shell launched by THIS very service. New files
	// can still be created (that's what append-only on a dir means), so our
	// next openCastFile keeps working. Best-effort: silently no-op on
	// non-ext4 FS or when chattr binary is missing.
	_ = exec.Command("chattr", "+a", termAuditDir).Run()

	// Sanitize fragments — no path traversal even if values came from user.
	safe := func(s string) string {
		return strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
				return r
			}
			return '_'
		}, s)
	}
	name := fmt.Sprintf("%d-%s-%s-pid%d.cast",
		started.Unix(), safe(operator), safe(node), pid)
	path := filepath.Join(termAuditDir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, "", err
	}

	// Set +a on the new file. Once applied, even root can only APPEND to it
	// (which is what we want — our castWrite() uses O_APPEND). To delete or
	// truncate, an attacker would first need to `chattr -a` — leaving a
	// loud audit trail. Idempotent if already +a from a prior open.
	_ = exec.Command("chattr", "+a", path).Run()
	header := map[string]any{
		"version":   2,
		"width":     80,
		"height":    24,
		"timestamp": started.Unix(),
		"env": map[string]string{
			"TERM":         "xterm-256color",
			"NCN_OPERATOR": operator,
			"NCN_NODE":     node,
		},
	}
	if b, err := json.Marshal(header); err == nil {
		f.Write(b)
		f.Write([]byte("\n"))
	}
	return f, path, nil
}

// castWrite appends one asciinema event. Errors are swallowed: a failed
// transcript write should NEVER kill the active operator session.
func castWrite(f *os.File, start time.Time, kind string, data []byte) {
	if f == nil || len(data) == 0 {
		return
	}
	entry := []any{
		time.Since(start).Seconds(),
		kind,
		string(data),
	}
	if b, err := json.Marshal(entry); err == nil {
		f.Write(b)
		f.Write([]byte("\n"))
	}
}
