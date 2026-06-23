// Telegram bot notifier for alert state transitions.
//
// Hooks into the alert engine via setNotifier(). On every fire/resolve
// transition the engine calls Notify() with the alertEvent; we format an
// HTML-styled message that mirrors the admin web UI card content
// (severity, node tag, title, threshold, latest sample, fired-at, rule id)
// and POST it to https://api.telegram.org/bot<TOKEN>/sendMessage in a
// background worker so the tick loop never blocks on network.
//
// Credentials are read from env vars NCN_TG_BOT_TOKEN + NCN_TG_CHAT_ID,
// which systemd loads from /etc/ncn-core-console/tg.env (mode 0600). When
// either is unset, the notifier is a no-op — operations don't fail.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type tgNotifier struct {
	token  string
	chatID string // ops group — interactive commands + needs-fix escalations
	// errorChat is the dedicated error channel (NCN_TG_ERROR_CHANNEL). All
	// op-failure reports go here; the ones an Agent triage flags as needing
	// OUR fix also escalate to chatID (group). Empty → falls back to chatID.
	errorChat string
	queue     chan tgPayload
	client    *http.Client

	// Wired post-construction (see setEngineFleet) so the command loop can
	// answer /status /alerts /node /bgp /probes queries from chat. Both
	// may be nil; commands return "not ready" until set.
	engine *alertEngine
	fleet  *fleetScraper

	// auth resolves a Telegram user id → bound operator account (the ops-
	// platform identity check). Wired via setAuth; nil → no command is
	// authorized (the bot effectively goes read-silent). See bot_tg.go
	// resolveOperator.
	auth *authStore

	// AI (DeepSeek) — wired via setAI. Powers /ask /summary /chat, the group
	// companion (@mention), and op-failure AI diagnosis. nil/disabled → the
	// features reply with a polite "未配置" notice. botUsername drives @mention
	// detection; aiLast throttles per-chat to bound spam/cost.
	ai          *deepseekClient
	botUsername string
	botID       int64 // numeric bot id (token prefix) — for reply-to-bot detection

	// Ops-agent sessions (bot_agent_tg.go) — per chat:userID, in-memory, TTL'd.
	// adminApprovals holds operator-initiated write actions awaiting an admin's
	// sign-off, keyed by a short id carried in the approval-card callback data.
	agentMu        sync.Mutex
	agentSessions  map[string]*agentSession
	adminApprovals map[string]*adminApproval
	aiMu        sync.Mutex
	aiLast      map[string]time.Time

	// cmdChat is the chat a command/callback is currently being handled for.
	// Set at the top of handleUpdate and cleared on return, it routes reply()
	// to the originating chat so management works in a DM as well as the group.
	// Safe as a bare field: the long-poll loop processes updates strictly
	// sequentially in one goroutine and no async path calls reply().
	cmdChat string

	// Drill state (DDoS emergency-drill via /stormtest, halted by /stop).
	// drillCancel is non-nil while a drill is in progress; calling it stops
	// the message flood and the load burners. Guarded by drillMu.
	drillMu     sync.Mutex
	drillCancel context.CancelFunc

	// pendingNet holds high-risk /netadmin ops (delete / mesh) awaiting their
	// typed confirm word in chat (bot_netadmin.go). Keyed by chat id, TTL'd.
	pendingMu  sync.Mutex
	pendingNet map[string]*pendingNetAction
}

type tgPayload struct {
	Text     string
	Disabled bool   // silent (no notification): resolves, banners, replies
	ChatID   string // target chat; "" → the default notifier chat (the group)
	// ReplyMarkup, when non-nil, is sent as Telegram reply_markup (e.g. an
	// inline_keyboard) — used to hang the ⚙ management buttons under alerts.
	ReplyMarkup any
}

// newTGNotifier returns nil if env vars are unset — caller treats nil as
// "feature disabled" and skips wiring.
func newTGNotifier(token, chatID string) *tgNotifier {
	if token == "" || chatID == "" {
		return nil
	}
	n := &tgNotifier{
		token:      token,
		chatID:     chatID,
		queue:      make(chan tgPayload, 128), // burst buffer; see enqueue() for full-queue policy
		client:     &http.Client{Timeout: 8 * time.Second},
		pendingNet: map[string]*pendingNetAction{},
	}
	// The numeric bot id is the token prefix before ':' — used to detect replies
	// to the bot's own messages (a robust group trigger even under privacy mode).
	if i := strings.IndexByte(token, ':'); i > 0 {
		n.botID, _ = strconv.ParseInt(token[:i], 10, 64)
	}
	return n
}

// Start spins up the send worker. Non-blocking; safe to call once.
func (n *tgNotifier) Start(ctx context.Context) {
	go n.runLoop(ctx)
	log.Printf("notify: telegram notifier active · chat=%s", n.chatID)
}

// SendStartup posts a one-shot "service online" banner. Single visible
// line — node count + timestamp — with the per-PoP list folded inside an
// expandable blockquote. Once per process; if the daemon restarts often
// during a deploy window the operator will see one terse line each time,
// not a paragraph.
func (n *tgNotifier) SendStartup(nodes []fleetNode) {
	if n == nil {
		return
	}
	ids := make([]string, 0, len(nodes))
	for _, nd := range nodes {
		ids = append(ids, html.EscapeString(nd.ID))
	}
	text := fmt.Sprintf(
		"🟢 <b>NCN online</b> · %d PoP · %s\n"+
			"<blockquote expandable>%s</blockquote>",
		len(nodes),
		time.Now().UTC().Format("15:04Z"),
		strings.Join(ids, " · "),
	)
	n.enqueue(tgPayload{Text: text, Disabled: true}, "startup banner")
}

func (n *tgNotifier) runLoop(ctx context.Context) {
	// Belt-and-suspenders: a panic in the send path must never permanently
	// kill the only goroutine that drains the queue (that would silently
	// stop ALL telegram delivery). Recover + restart.
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("notify: tg runLoop panic recovered, restarting in 3s: %v", rec)
			time.Sleep(3 * time.Second)
			go n.runLoop(ctx)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-n.queue:
			n.deliver(ctx, p)
			// Telegram rate-limit headroom: max ~20 msgs/min to a group.
			// Spread sends ≥250ms apart so a burst (e.g. multi-node BGP
			// flap) doesn't hit the cap.
			time.Sleep(250 * time.Millisecond)
		}
	}
}

// deliver sends one payload, retrying transient failures with exponential
// backoff and honoring Telegram 429 retry_after. THIS is the core of the
// "an alert fired but no message arrived" fix: a single network blip, a
// timeout, a 5xx, or a rate-limit no longer silently drops the message.
// Permanent 4xx errors (bad token / chat / malformed HTML) are not retried.
// After maxAttempts we give up and log loudly.
func (n *tgNotifier) deliver(ctx context.Context, p tgPayload) {
	const maxAttempts = 5
	backoff := 500 * time.Millisecond
	for attempt := 1; ; attempt++ {
		retryAfter, permanent, err := n.send(ctx, p)
		if err == nil {
			return
		}
		if permanent {
			log.Printf("notify: tg send permanent failure (not retrying): %v", err)
			return
		}
		if attempt >= maxAttempts {
			log.Printf("notify: tg send FAILED after %d attempts, giving up: %v", maxAttempts, err)
			return
		}
		wait := backoff
		if retryAfter > 0 {
			wait = retryAfter
		}
		log.Printf("notify: tg send attempt %d/%d failed (%v) — retry in %s", attempt, maxAttempts, err, wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if backoff < 8*time.Second {
			backoff *= 2
		}
	}
}

// enqueue puts a payload on the send queue. Silent payloads (resolves,
// startup banners, bot replies) are best-effort — dropped if the queue is
// full. Important payloads (a crit FIRE that must ring phones) wait briefly
// for room instead of being dropped on the floor: the entire point of the
// alert is that it gets through.
func (n *tgNotifier) enqueue(p tgPayload, what string) {
	if n == nil {
		return
	}
	if p.Disabled {
		select {
		case n.queue <- p:
		default:
			log.Printf("notify: tg queue full, dropped silent msg (%s)", what)
		}
		return
	}
	select {
	case n.queue <- p:
	case <-time.After(3 * time.Second):
		log.Printf("notify: tg queue full 3s, DROPPING important msg (%s) — telegram backend stalled", what)
	}
}

// send posts one payload to Telegram. Return contract:
//   - err == nil          → delivered.
//   - permanent == true   → unrecoverable 4xx (bad token, chat not found,
//                           malformed HTML). Don't retry.
//   - retryAfter > 0      → Telegram 429; wait that long, then retry.
//   - else (err != nil)   → transient (network/timeout/5xx); retry w/ backoff.
func (n *tgNotifier) send(ctx context.Context, p tgPayload) (retryAfter time.Duration, permanent bool, err error) {
	chat := p.ChatID
	if chat == "" {
		chat = n.chatID
	}
	payload := map[string]any{
		"chat_id":                  chat,
		"text":                     p.Text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
		"disable_notification":     p.Disabled,
	}
	if p.ReplyMarkup != nil {
		payload["reply_markup"] = p.ReplyMarkup
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, true, err // an unmarshalable payload won't improve on retry
	}
	url := "https://api.telegram.org/bot" + n.token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return 0, true, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return 0, false, err // network / timeout — transient
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	switch {
	case resp.StatusCode < 300:
		return 0, false, nil
	case resp.StatusCode == 429:
		// {"ok":false,"parameters":{"retry_after":N}}
		var r struct {
			Parameters struct {
				RetryAfter int `json:"retry_after"`
			} `json:"parameters"`
		}
		_ = json.Unmarshal(raw, &r)
		wait := time.Duration(r.Parameters.RetryAfter) * time.Second
		if wait <= 0 {
			wait = 3 * time.Second
		}
		return wait, false, fmt.Errorf("429 rate-limited (retry_after=%ds)", r.Parameters.RetryAfter)
	case resp.StatusCode >= 500:
		return 0, false, fmt.Errorf("status %d · %s", resp.StatusCode, string(raw)) // transient
	default:
		return 0, true, fmt.Errorf("status %d · %s", resp.StatusCode, string(raw)) // 4xx — permanent
	}
}

// tgField is one labelled detail line in an ops-event notification.
type tgField struct{ K, V string }

// NotifyEvent posts an operational EVENT (not a threshold alert) to the chat —
// e.g. a server being added / decommissioned / deleted / provisioned. Layout:
// one bold headline line + one "<code>key</code> · value" line per non-empty
// field. ring=false sends it silently (visible in chat, no notification sound)
// — right for changes the operator just made themselves; ring=true is for
// things that warrant attention (e.g. a failed provision). nil-safe.
func (n *tgNotifier) NotifyEvent(emoji, title string, fields []tgField, ring bool) {
	if n == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%s</b>", emoji, html.EscapeString(title))
	for _, f := range fields {
		if f.V == "" {
			continue
		}
		fmt.Fprintf(&b, "\n<code>%s</code> · %s", html.EscapeString(f.K), html.EscapeString(f.V))
	}
	n.enqueue(tgPayload{Text: b.String(), Disabled: !ring}, "event "+title)
}

// NudgeCrit posts a DETAILED critical-event report — the full formatTGAlert
// card (severity · node · title, the latest metric/subject line naming WHICH
// peer/tunnel/probe tripped, plus a foldable threshold/fired/rule block). It
// used to be a one-line "go check /alerts crit" nudge; operators asked for the
// detail inline so the cause is readable straight from the notification.
//
// Routes to the dedicated error channel (NCN_TG_ERROR_CHANNEL) — same home as
// op-failure reports, so all "something broke" surfaces consolidate there.
// Falls back to the group when no channel is configured. Resolved is silent.
func (n *tgNotifier) NudgeCrit(ev alertEvent, kind string) {
	if n == nil {
		return
	}
	if kind != "fired" && kind != "resolved" {
		return
	}
	channel := n.errorChat
	if channel == "" {
		channel = n.chatID
	}
	n.enqueue(tgPayload{
		ChatID:   channel,
		Text:     formatTGAlert(ev, kind),
		Disabled: kind == "resolved",
	}, "crit "+ev.NodeID+"/"+ev.RuleID+" "+kind)
}

// sendAlertCard posts a fired warn/crit alert as ONE message: the full
// formatTGAlert card with the Agent's 🤖 diagnosis folded in (no longer a
// separate follow-up). Routes to the error channel. Called by the engine's
// triageAlertFire after it decides the alert is worth reporting.
func (n *tgNotifier) sendAlertCard(ev alertEvent, diag string) {
	if n == nil {
		return
	}
	channel := n.errorChat
	if channel == "" {
		channel = n.chatID
	}
	text := formatTGAlert(ev, "fired")
	if d := strings.TrimSpace(diag); d != "" {
		text += "\n<blockquote>🤖 " + mdToTG(d) + "</blockquote>"
	}
	n.enqueue(tgPayload{ChatID: channel, Text: text}, "alert "+ev.NodeID+"/"+ev.RuleID+" fired")
}

// alertGroupItem is one node's line in a coalesced multi-node alert card.
type alertGroupItem struct{ Node, Msg string }

// sendAlertGroupCard posts ONE card for a rule that fired on multiple nodes in
// the same tick (coalesced): severity · title · N nodes, each node's subject
// line, and the Agent's 🤖 diagnosis (crit) folded in. Routes to the error
// channel. This is how a fleet-wide event becomes one message, not N.
func (n *tgNotifier) sendAlertGroupCard(title string, sev severity, items []alertGroupItem, diag string) {
	if n == nil || len(items) == 0 {
		return
	}
	channel := n.errorChat
	if channel == "" {
		channel = n.chatID
	}
	sevEmoji := map[severity]string{sevInfo: "🔵", sevWarn: "🟡", sevCritical: "🔴"}[sev]
	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>[%s]</b> %s · <b>%d nodes</b>",
		sevEmoji, html.EscapeString(string(sev)), html.EscapeString(title), len(items))
	for _, it := range items {
		fmt.Fprintf(&b, "\n<code>@%s</code> · %s", html.EscapeString(orDash(it.Node)), html.EscapeString(orDash(it.Msg)))
	}
	if d := strings.TrimSpace(diag); d != "" {
		b.WriteString("\n<blockquote>🤖 " + mdToTG(d) + "</blockquote>")
	}
	n.enqueue(tgPayload{ChatID: channel, Text: b.String()}, "alert-group "+title)
}

// Notify enqueues a formatted alert message. Drop oldest if the queue is
// full so we don't block the tick loop on a wedged Telegram backend.
func (n *tgNotifier) Notify(ev alertEvent, kind string) {
	if n == nil {
		return
	}
	text := formatTGAlert(ev, kind)
	payload := tgPayload{Text: text}
	// Resolved messages don't need to ring phones at 3am — flag silent.
	if kind == "resolved" {
		payload.Disabled = true
	}
	n.enqueue(payload, "alert "+ev.NodeID+"/"+ev.RuleID+" "+kind)
}

// formatTGAlert builds a terse TG message. Two compact lines visible by
// default; the rest (threshold, fired-at, rule id) sits inside an
// `<blockquote expandable>` that modern Telegram clients render collapsed
// behind a "Show more" tap. Older clients show it as a regular blockquote
// — degrades gracefully.
//
// Resolved messages collapse to a single line — duration + node + title.
// No need to repeat the metric snapshot since the alert is over.
func formatTGAlert(ev alertEvent, kind string) string {
	node := html.EscapeString(ev.NodeID)
	if node == "" {
		node = "—"
	}
	title := html.EscapeString(ev.Title)

	if kind == "resolved" {
		// One line, silent. Telegram already shows the message timestamp;
		// the duration tells the operator how long the incident lasted.
		dur := ""
		if ev.ResolvedAt > 0 {
			dur = " · " + humanDuration(ev.ResolvedAt-ev.FiredAt)
		}
		return fmt.Sprintf("✅ <code>@%s</code> %s resolved%s", node, title, dur)
	}

	// Fire (or any other future kind treated as fire-like).
	sevEmoji := map[severity]string{
		sevInfo:     "🔵",
		sevWarn:     "🟡",
		sevCritical: "🔴",
	}[ev.Severity]

	// Visible header (one line) + visible metric line (one line) is all
	// most alerts need to triage from a phone notification. Everything
	// else folds.
	visible := fmt.Sprintf(
		"%s <b>[%s]</b> <code>@%s</code> · %s\n"+
			"<code>%s</code>",
		sevEmoji,
		html.EscapeString(string(ev.Severity)),
		node, title,
		html.EscapeString(orDash(ev.Message)),
	)

	// Fold the meta — threshold, exact fire time, rule + id. These are
	// only needed when post-morteming, not in the heat of the moment.
	folded := fmt.Sprintf(
		"<blockquote expandable>thr · %s\nfired · %s\nrule · %s · id=%s</blockquote>",
		html.EscapeString(orDash(ev.Threshold)),
		time.Unix(ev.FiredAt, 0).UTC().Format("2006-01-02 15:04:05Z"),
		html.EscapeString(ev.RuleID),
		html.EscapeString(ev.ID),
	)
	return visible + "\n" + folded
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// humanDuration: "12s" / "3m 42s" / "1h 5m"
func humanDuration(s int64) string {
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	if s < 3600 {
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %dm", s/3600, (s%3600)/60)
}
