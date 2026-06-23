// Telegram bot command handlers — the "mini web UI" inside the chat.
//
// Long-polls `getUpdates` and dispatches slash commands to data accessors
// on alertEngine + fleetScraper. Replies use the same HTML send path as
// notify_tg.go.
//
// Access control:
//   * Only messages from the configured chat ID (NCN_TG_CHAT_ID) are
//     answered. Anyone DMing the bot directly is silently ignored.
//   * No write commands — every handler is strictly read-only, no
//     accept/ack/silence actions. Those still belong to the web UI behind
//     passkey/TOTP step-up.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// setEngineFleet wires the data sources the command handlers need. Must
// be called before runCommandLoop or commands return "not ready" responses.
func (n *tgNotifier) setEngineFleet(eng *alertEngine, f *fleetScraper) {
	n.engine = eng
	n.fleet = f
}

// setAuth wires the operator store used to verify each Telegram user's
// identity (their bound + approved operator account). Without it the bot
// can't authorize anyone, so every command is rejected.
func (n *tgNotifier) setAuth(a *authStore) { n.auth = a }

// resolveOperator maps a Telegram user id to the operator account it is bound
// to via /admin/security, returning (username, true) only when that operator
// also exists and is approved. This IS the ops-platform identity check — a
// user who has already bound their Telegram needs nothing more (满足"绑定过的
// 不用再验"); an unbound/un-approved user resolves to ("", false).
func (n *tgNotifier) resolveOperator(tgUserID int64) (string, bool) {
	if n.auth == nil || tgUserID == 0 {
		return "", false
	}
	op, ok := n.auth.findOperatorByIdentity("telegram", strconv.FormatInt(tgUserID, 10))
	if !ok || !n.auth.operatorApproved(op) {
		return "", false
	}
	return op, true
}

// displayName is how the bot addresses an operator in chat: their self-chosen
// 称呼 (set via /callme) if any, else their Telegram @username / group tag,
// else the bare operator account name. Not HTML-escaped — callers escape it.
func (n *tgNotifier) displayName(op, tgUsername string) string {
	if n.auth != nil {
		if nick := n.auth.botNick(op); nick != "" {
			return nick
		}
	}
	if tgUsername != "" {
		return "@" + tgUsername
	}
	return op
}

// runCommandLoop spins up the long-poll receiver. Called once at startup.
// Survives transient Telegram API failures via simple backoff.
func (n *tgNotifier) runCommandLoop(ctx context.Context) {
	go n.publishCommandMenu(ctx) // best-effort, async
	go func() {
		var offset int64
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			updates, newOffset, err := n.getUpdates(ctx, offset)
			if err != nil {
				log.Printf("tgbot: getUpdates err: %v", err)
				time.Sleep(5 * time.Second)
				continue
			}
			offset = newOffset
			for _, u := range updates {
				n.handleUpdate(ctx, u)
			}
		}
	}()
	log.Printf("tgbot: command long-poll loop active")
}

// publishCommandMenu calls setMyCommands so Telegram's slash-menu shows
// the supported commands. Best-effort; logs failure but keeps running.
func (n *tgNotifier) publishCommandMenu(ctx context.Context) {
	// Default menu (the group + everyone else): read-only console only. The
	// drill commands are deliberately ABSENT here so they never show up in the
	// group's command list.
	consoleCmds := []map[string]string{
		{"command": "status", "description": "fleet overview · cpu/mem/disk per PoP"},
		{"command": "alerts", "description": "active alerts · /alerts [crit|warn|info]"},
		{"command": "node", "description": "per-node detail · /node <id>"},
		{"command": "bgp", "description": "BGP peers · /bgp [node]"},
		{"command": "probes", "description": "ping RTTs · /probes [node]"},
		{"command": "billing", "description": "VPS rent subscriptions + renewal countdown"},
		{"command": "manage", "description": "alert rules · opens the console rules page"},
		{"command": "netadmin", "description": "server admin · decommission/restore/mesh/delete (inline menu)"},
		{"command": "errors", "description": "failed ops actions · review + one-tap retry (inline menu)"},
		{"command": "ask", "description": "AI Q&A · /ask <question> (grounded in live fleet data)"},
		{"command": "summary", "description": "AI ops status summary + likely root causes"},
		{"command": "chat", "description": "chat with the AI · /chat <text> (or @mention / reply)"},
		{"command": "agent", "description": "AI ops agent · /agent <task> (acts with your approval)"},
		{"command": "model", "description": "view/set AI model per purpose · /model [purpose model]"},
		{"command": "tokens", "description": "AI token usage today + cumulative (per model)"},
		{"command": "bind", "description": "bind your operator account · DM me for a one-time link"},
		{"command": "whoami", "description": "show your verified operator identity + display name"},
		{"command": "callme", "description": "set display name · /callme <name> (empty = reset)"},
		{"command": "test", "description": "verify alert delivery pipeline"},
		{"command": "help", "description": "command list"},
	}
	n.setMyCommands(ctx, consoleCmds, nil)

	// Drill menu — scoped to the storm user's PRIVATE chat only.
	if uid, err := strconv.ParseInt(stormAuthUserID, 10, 64); err == nil {
		drillCmds := []map[string]string{
			{"command": "stormtest", "description": "under-attack drill · /stormtest [maxsecs]"},
			{"command": "stop", "description": "end drill: recovery + drop load"},
			{"command": "help", "description": "drill help"},
		}
		n.setMyCommands(ctx, drillCmds, map[string]any{"type": "chat", "chat_id": uid})
	}
}

// setMyCommands publishes a command list, optionally scoped to a specific
// chat (scope==nil → default scope, applies to chats with no narrower scope).
func (n *tgNotifier) setMyCommands(ctx context.Context, commands []map[string]string, scope map[string]any) {
	payload := map[string]any{"commands": commands}
	if scope != nil {
		payload["scope"] = scope
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, "POST",
		"https://api.telegram.org/bot"+n.token+"/setMyCommands",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		log.Printf("tgbot: setMyCommands err: %v", err)
		return
	}
	resp.Body.Close()
}

// ---------- long-poll fetch ----------

type tgUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		MessageID int64  `json:"message_id"`
		Text      string `json:"text"`
		Chat      struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
		From struct {
			ID        int64  `json:"id"`
			Username  string `json:"username,omitempty"`
			FirstName string `json:"first_name,omitempty"`
		} `json:"from"`
		ReplyToMessage *struct {
			From struct {
				ID int64 `json:"id"`
			} `json:"from"`
		} `json:"reply_to_message,omitempty"`
	} `json:"message,omitempty"`
	CallbackQuery *struct {
		ID   string `json:"id"`
		Data string `json:"data"`
		From struct {
			ID        int64  `json:"id"`
			Username  string `json:"username,omitempty"`
			FirstName string `json:"first_name,omitempty"`
		} `json:"from"`
		Message *struct {
			MessageID int64 `json:"message_id"`
			Chat      struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message,omitempty"`
	} `json:"callback_query,omitempty"`
}

func (n *tgNotifier) getUpdates(ctx context.Context, offset int64) ([]tgUpdate, int64, error) {
	// allowed_updates = ["message","callback_query"] — the callbacks drive the
	// ⚙ management inline menus.
	url := fmt.Sprintf(
		"https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=25&allowed_updates=%%5B%%22message%%22%%2C%%22callback_query%%22%%5D",
		n.token, offset)
	// Long-poll: server may hold the connection for up to 25s. Client must
	// allow at least that + slack.
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, offset, err
	}
	client := &http.Client{Timeout: 35 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, offset, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, offset, fmt.Errorf("decode: %w · body=%q", err, string(raw[:min(200, len(raw))]))
	}
	if !env.OK {
		return nil, offset, fmt.Errorf("telegram returned ok=false · body=%q", string(raw[:min(200, len(raw))]))
	}
	if len(env.Result) > 0 {
		offset = env.Result[len(env.Result)-1].UpdateID + 1
	}
	return env.Result, offset, nil
}

// ---------- dispatch ----------

func (n *tgNotifier) handleUpdate(ctx context.Context, u tgUpdate) {
	// Inline-button taps (⚙ / netadmin menus) arrive as callback queries.
	if u.CallbackQuery != nil {
		cb := u.CallbackQuery
		chat := ""
		var msgID int64
		if cb.Message != nil {
			chat = strconv.FormatInt(cb.Message.Chat.ID, 10)
			msgID = cb.Message.MessageID
		}
		// Per-operator identity: the tapping user's Telegram id must resolve to
		// a bound + approved operator (the ops-platform check) — group
		// membership alone is no longer enough, and this same gate lets a bound
		// operator drive the menus from a DM.
		op, ok := n.resolveOperator(cb.From.ID)
		if !ok {
			n.answerCallback(ctx, cb.ID, "Not a verified operator · DM me /bind to link your account")
			return
		}
		n.cmdChat = chat
		defer func() { n.cmdChat = "" }()
		switch {
		case strings.HasPrefix(cb.Data, "nd:"):
			n.handleNetCallback(ctx, cb.ID, chat, msgID, cb.Data, cb.From.ID, op)
		case strings.HasPrefix(cb.Data, "ag:"):
			n.handleAgentCallback(ctx, cb.ID, chat, msgID, cb.Data, cb.From.ID, op)
		default:
			n.handleManageCallback(ctx, cb.ID, chat, msgID, cb.Data, op)
		}
		return
	}
	if u.Message == nil {
		return
	}
	srcChat := strconv.FormatInt(u.Message.Chat.ID, 10)
	isGroup := srcChat == n.chatID
	isStormUser := srcChat == stormAuthUserID

	// Route replies to the originating chat (group or DM) for the duration.
	n.cmdChat = srcChat
	defer func() { n.cmdChat = "" }()

	op, verified := n.resolveOperator(u.Message.From.ID)

	// /bind is the self-service onboarding entry — it MUST work for an as-yet-
	// unverified user (the whole point: they aren't bound). Handle it before the
	// identity gate. The link is a one-time capability, so it's only ever
	// delivered in a private chat — never dropped into the group.
	if botCmdWord(u.Message.Text) == "/bind" {
		n.replyBind(isGroup, u.Message.From.ID, u.Message.From.Username)
		return
	}

	// Group AI companion (陪群友): casual chat is open to any group member when
	// they explicitly address the bot — /chat or an @mention. This is NOT an ops
	// command, so it bypasses the operator gate, but ONLY in the group and ONLY
	// when addressed (so the bot doesn't reply to everything / leak in DMs).
	if isGroup {
		if botCmdWord(u.Message.Text) == "/chat" {
			n.replyChat(srcChat, cmdArg(u.Message.Text))
			return
		}
		if !strings.HasPrefix(strings.TrimSpace(u.Message.Text), "/") {
			// @mention (works once privacy mode is off) OR a reply to one of the
			// bot's own messages (always delivered, even under privacy mode).
			replyToBot := u.Message.ReplyToMessage != nil && n.botID != 0 &&
				u.Message.ReplyToMessage.From.ID == n.botID
			if n.mentionsBot(u.Message.Text) || replyToBot {
				n.replyChat(srcChat, n.stripMention(u.Message.Text))
				return
			}
		}
	}

	// Ops-platform identity verification. A resolved operator is "verified" and
	// may run EVERY command — in the group OR in a private chat (authenticated
	// DM management). stormAuthUserID stays a grandfathered DM path so the
	// emergency drill still works even before that user binds their account.
	if !verified && !isStormUser {
		// Unverified. In the trusted group, nudge a slash-command sender toward
		// binding; in a DM, stay silent so a prober can't even tell the bot is up.
		if isGroup && strings.HasPrefix(strings.TrimSpace(u.Message.Text), "/") {
			n.replyTo(srcChat, "⚠️ Operator identity not verified. DM me <code>/bind</code> to link your account first.")
		}
		return
	}

	text := strings.TrimSpace(u.Message.Text)
	// A pending high-risk /netadmin confirm word ("DELETE <id>" / "APPLY MESH
	// <id>") arrives as a plain message — intercept BEFORE the slash-command
	// gate would drop it. Only a verified operator can hold a pending action.
	if verified && n.handleNetConfirm(srcChat, u.Message.From.ID, op, text) {
		return
	}
	if !strings.HasPrefix(text, "/") {
		return
	}

	// Parse "/cmd@botname arg1 arg2" → cmd + args.
	parts := strings.Fields(text)
	cmd := parts[0]
	if at := strings.Index(cmd, "@"); at >= 0 {
		cmd = cmd[:at]
	}
	args := parts[1:]

	// The drill commands (/stormtest, /stop) are STRICTLY the storm user's DM.
	// Elsewhere they don't exist — a group member (or other operator) typing
	// them gets the same "unknown command" as any typo, keeping it invisible.
	if cmd == "/stormtest" || cmd == "/storm" || cmd == "/stop" {
		if !isStormUser {
			if isGroup || verified {
				n.reply("unknown command — try /help")
			}
			return
		}
		if cmd == "/stop" {
			n.replyStop(srcChat)
		} else {
			n.replyStormTest(args, srcChat)
		}
		return
	}

	// Everything below is operator-only. A grandfathered storm user who is NOT
	// also a verified operator gets the drill-scoped help and nothing more.
	if !verified {
		if cmd == "/help" || cmd == "/start" {
			n.replyHelpStorm(srcChat)
		}
		return
	}

	switch cmd {
	case "/help", "/start":
		n.replyHelp()
	case "/status":
		n.replyStatus()
	case "/alerts":
		n.replyAlerts(args)
	case "/node":
		n.replyNode(args)
	case "/bgp":
		n.replyBGP(args)
	case "/probes":
		n.replyProbes(args)
	case "/manage", "/rules":
		n.replyManageRoot()
	case "/netadmin", "/servers":
		n.replyNetRoot()
	case "/errors", "/faults":
		n.replyErrors()
	case "/oncall":
		if globalOncall != nil {
			n.replyTo(srcChat, globalOncall.statusText())
		} else {
			n.replyTo(srcChat, "on-call 未启用")
		}
	case "/ask":
		n.replyAsk(srcChat, op, strings.Join(args, " "))
	case "/summary", "/sum":
		n.replySummary(srcChat, op)
	case "/chat":
		n.replyChat(srcChat, strings.Join(args, " "))
	case "/agent":
		n.replyAgent(srcChat, u.Message.From.ID, op, strings.Join(args, " "))
	case "/model":
		n.replyModel(srcChat, op, args)
	case "/tokens", "/aitokens":
		n.replyTo(srcChat, globalAIUsage.report())
	case "/billing", "/subs", "/vps":
		n.replyBilling()
	case "/whoami":
		n.replyWhoami(op, u.Message.From.Username)
	case "/callme":
		n.replyCallMe(op, strings.TrimSpace(strings.TrimPrefix(text, cmd)))
	case "/test", "/ping":
		n.replyTest()
	default:
		n.reply("unknown command — try /help")
	}
	_ = ctx
}

// botCmdWord returns the leading slash command of a message ("/bind@bot foo" →
// "/bind"), or "" if the text isn't a command.
func botCmdWord(text string) string {
	parts := strings.Fields(strings.TrimSpace(text))
	if len(parts) == 0 || !strings.HasPrefix(parts[0], "/") {
		return ""
	}
	cmd := parts[0]
	if at := strings.Index(cmd, "@"); at >= 0 {
		cmd = cmd[:at]
	}
	return cmd
}

// cmdArg returns everything after the leading command word ("/chat hi there" →
// "hi there"), or "" if there's no argument.
func cmdArg(text string) string {
	parts := strings.Fields(strings.TrimSpace(text))
	if len(parts) <= 1 {
		return ""
	}
	return strings.TrimSpace(strings.Join(parts[1:], " "))
}

// replyBind mints a one-time Telegram-bind ticket and replies with a link to
// /admin/bind, where the operator (once logged into the console) confirms
// binding THIS Telegram account to their operator account. The link is a
// bearer capability → only ever sent in a private chat, never the group.
func (n *tgNotifier) replyBind(isGroup bool, tgUserID int64, tgUsername string) {
	if isGroup {
		n.reply("🔐 To bind, DM me: open a private chat and send <code>/bind</code> for a one-time link (it contains a credential — don't post it in the group).")
		return
	}
	if n.auth == nil {
		n.reply("operator store not ready")
		return
	}
	tok := n.auth.mintTGBindTicket(strconv.FormatInt(tgUserID, 10), tgUsername)
	if tok == "" {
		n.reply("failed to create bind link, try again shortly")
		return
	}
	base := strings.TrimRight(getenvDefault("NCN_OAUTH_REDIRECT_BASE", "https://admin.example.com"), "/")
	link := base + "/admin/bind?t=" + tok
	n.reply(fmt.Sprintf(
		"🔐 <b>Bind operator identity</b>\nOpen the link (log in to the console first) and confirm linking <b>this Telegram account</b> to your operator account:\n\n%s\n\n<blockquote>Link is valid for 10 minutes, single-use. Don't forward it. Once bound you can use /netadmin and the rest.</blockquote>",
		html.EscapeString(link)))
}

// notifyBindSuccess DMs a freshly-bound Telegram user that their account is now
// linked — closes the /bind loop from the Telegram side. Non-silent (a small
// ping is wanted here). Safe to call on a nil notifier / empty id.
func (n *tgNotifier) notifyBindSuccess(tgID, operator string) {
	if n == nil || tgID == "" {
		return
	}
	n.enqueue(tgPayload{
		ChatID: tgID,
		Text: "✅ <b>Bound</b>\nThis Telegram is now linked to operator account <code>" + html.EscapeString(operator) +
			"</code>.\nYou can now use /netadmin, /manage, /whoami, etc. Send /callme &lt;name&gt; to set a display name.",
	}, "bind success→"+tgID)
}

// replyWhoami confirms the resolved ops identity bound to this Telegram user
// and the current 称呼 — the in-chat way to verify the binding took.
func (n *tgNotifier) replyWhoami(op, tgUsername string) {
	n.reply(fmt.Sprintf(
		"🪪 Verified operator\naccount: <code>%s</code>\ndisplay name: <b>%s</b>\n<blockquote>/callme &lt;name&gt; to set a custom name · /callme alone to reset</blockquote>",
		html.EscapeString(op), html.EscapeString(n.displayName(op, tgUsername))))
}

// replyCallMe sets (or, with an empty arg, clears) the operator's 称呼.
func (n *tgNotifier) replyCallMe(op, nick string) {
	if n.auth == nil {
		n.reply("operator store not ready")
		return
	}
	if err := n.auth.setBotNick(op, nick); err != nil {
		n.reply("failed: " + html.EscapeString(err.Error()))
		return
	}
	if strings.TrimSpace(nick) == "" {
		n.reply("Cleared — back to default (@username).")
		return
	}
	n.reply("Got it — I'll call you <b>" + html.EscapeString(n.auth.botNick(op)) + "</b>.")
}

// reply enqueues a plain HTML silent message to the chat the current command
// is being handled for (n.cmdChat — set by handleUpdate). When cmdChat is
// unset it falls back to the default group, preserving prior behavior.
func (n *tgNotifier) reply(text string) {
	n.enqueue(tgPayload{Text: text, Disabled: true, ChatID: n.cmdChat}, "bot reply")
}

// replyTo enqueues a silent message to a SPECIFIC chat (e.g. the private chat
// a drill command arrived from), instead of the default group.
func (n *tgNotifier) replyTo(chatID, text string) {
	n.enqueue(tgPayload{Text: text, Disabled: true, ChatID: chatID}, "bot reply→"+chatID)
}

// replyBilling lists the operator's VPS rent subscriptions with their
// next-renewal countdown. Pulls from the same globalBilling store the
// /admin/billing web UI + the daily renewal-digest goroutine use, so a
// quick /billing in chat gives an authoritative snapshot without having
// to open the admin console.
//
// Each line: 🟢/🟡/🔴 label (provider) · X.XX CCY/cycle · in N days · YYYY-MM-DD
//   🔴 = overdue or ≤ 1 day  (immediate action)
//   🟡 = ≤ 14 days            (heads-up window)
//   🟢 = > 14 days            (comfortable)
// Sorted soonest-first (listSnapshot's default).
//
// Totals row breaks down per-currency monthly-equivalent spend
// (yearly /12, quarterly /3, monthly as-is).
func (n *tgNotifier) replyBilling() {
	if globalBilling == nil {
		n.reply("billing store not initialised")
		return
	}
	subs := globalBilling.listSnapshot()
	if len(subs) == 0 {
		n.reply("🧾 <b>VPS billing</b>\n<blockquote>no subscriptions recorded · open /admin/billing on the console to add some</blockquote>")
		return
	}

	now := time.Now()
	monthlyByCcy := map[string]float64{}
	var lines []string
	lines = append(lines, "🧾 <b>VPS billing</b>")
	for _, s := range subs {
		days := int(s.NextDue.Sub(now).Hours() / 24)
		var flag string
		switch {
		case days < 0 || days <= renewalCritDays:
			flag = "🔴"
		case days <= renewalWarnDays:
			flag = "🟡"
		default:
			flag = "🟢"
		}
		var due string
		switch {
		case days < 0:
			due = fmt.Sprintf("OVERDUE %d day(s)", -days)
		case days == 0:
			due = "due today"
		case days == 1:
			due = "due tomorrow"
		default:
			due = fmt.Sprintf("in %d days", days)
		}
		lines = append(lines, fmt.Sprintf(
			"%s <b>%s</b> <i>(%s)</i> · %.2f %s/%s · %s · <code>%s</code>",
			flag,
			html.EscapeString(s.Label),
			html.EscapeString(s.Provider),
			s.MonthlyCost, html.EscapeString(s.Currency),
			cycleAbbrev(s.BillingCycle),
			due,
			s.NextDue.Format("2006-01-02"),
		))
		monthlyByCcy[s.Currency] += s.MonthlyEquivalent()
	}

	// Per-currency monthly-equivalent totals. Sort by descending total
	// so the dominant currency shows first.
	if len(monthlyByCcy) > 0 {
		type row struct {
			ccy string
			amt float64
		}
		var totals []row
		for k, v := range monthlyByCcy {
			totals = append(totals, row{k, v})
		}
		sort.Slice(totals, func(i, j int) bool { return totals[i].amt > totals[j].amt })
		var totalParts []string
		var cnyTotal float64
		var fxComplete = true
		for _, t := range totals {
			totalParts = append(totalParts, fmt.Sprintf("%.2f %s", t.amt, html.EscapeString(t.ccy)))
			if cny, ok := globalFX.ToCNY(t.amt, t.ccy); ok {
				cnyTotal += cny
			} else {
				fxComplete = false
			}
		}
		lines = append(lines, "")
		lines = append(lines, "<b>monthly total</b> · "+strings.Join(totalParts, " · "))
		// CNY-equivalent rollup. Only shown if FX is healthy for ALL
		// currencies present — partial conversion would be misleading.
		// The "as of" line uses FetchedAt so the operator can tell how
		// stale the conversion is (FX provider updates daily).
		if fxComplete && cnyTotal > 0 {
			fetched := globalFX.FetchedAt()
			fxAge := time.Since(fetched).Truncate(time.Minute).String()
			lines = append(lines, fmt.Sprintf(
				"<b>≈ ¥%.2f CNY/mo</b> <i>(rates %s old)</i>",
				cnyTotal, fxAge))
		} else if !fxComplete {
			lines = append(lines, "<i>(CNY conversion unavailable — FX feed missing some currencies)</i>")
		}
	}

	n.reply(strings.Join(lines, "\n"))
}

// cycleAbbrev shortens the on-disk cycle string so the per-line cost
// reads like "8.00 EUR/mo" rather than "8.00 EUR/monthly".
func cycleAbbrev(c string) string {
	switch c {
	case cycleYearly:
		return "yr"
	case cycleQuarterly:
		return "qtr"
	}
	return "mo"
}

// replyTest pushes a message through the SAME queue + retry path a real alert
// takes, so a successful delivery confirms the whole outbound pipeline is
// healthy — not merely that the bot can read commands. Use it to answer
// "is alerting actually working right now?" on demand.
func (n *tgNotifier) replyTest() {
	n.enqueue(tgPayload{
		Text: fmt.Sprintf("🧪 <b>alert self-test OK</b> · %s\n<blockquote>queue %d/%d · same send+retry path a 🔴 crit alert uses</blockquote>",
			time.Now().UTC().Format("15:04:05Z"), len(n.queue), cap(n.queue)),
		Disabled: false, // important: exercises the no-drop + retry path
	}, "manual /test")
}

// (replyStormTest / replyStop / load-drill live in bot_drill.go)

// ---------- command handlers ----------

func (n *tgNotifier) replyHelp() {
	// Live PoP list — pulled from fleet so a new node added to fleet.go
	// shows up here without having to edit help text. Falls back to a
	// hint if the fleet pointer hasn't been wired yet.
	popList := "(loading...)"
	if n.fleet != nil {
		ids := []string{}
		for _, nd := range n.fleet.nodesSnapshot() {
			ids = append(ids, "<code>"+html.EscapeString(nd.ID)+"</code>")
		}
		if len(ids) > 0 {
			popList = strings.Join(ids, " · ")
		}
	}
	n.reply(strings.Join([]string{
		"🤖 <b>NCN bot · read-only fleet console</b>",
		"",
		"<b>Commands</b>",
		"<code>/status</code> — fleet overview · one line per PoP",
		"<code>/alerts</code> <i>[sev]</i> — active alerts; sev = <code>crit</code> | <code>warn</code> | <code>info</code>",
		"<code>/node &lt;id&gt;</code> — full per-node detail · system + BIRD + WG + active alerts",
		"<code>/bgp</code> <i>[node]</i> — BGP peer up/down, with down-peer state details",
		"<code>/probes</code> <i>[node]</i> — per-target ping RTT (Cloudflare / Google / inter-PoP)",
		"<code>/billing</code> — VPS rent subscriptions + days-until-renewal (also <code>/subs</code> / <code>/vps</code>)",
		"<code>/test</code> — push a test alert through the real send path (verify delivery)",
		"<code>/help</code> — this message",
		"",
		"<b>PoP IDs</b>",
		popList,
		"",
		"<b>Examples</b>",
		"<code>/alerts crit</code> — only critical active",
		"<code>/node pop-03</code> — full status for pop-03",
		"<code>/bgp ctrl-01</code> — only ctrl-01's BGP peers",
		"<code>/probes pop-05</code> — only pop-05's probes",
		"",
		"<b>Push policy</b>",
		"🔴 <code>crit</code> → auto-push on fire (sustain 60s · cooldown 10m per node+rule)",
		"🟡 <code>warn</code> · 🔵 <code>info</code> → silent · query with <code>/alerts</code>",
		"",
		"<i>Bot is strictly read-only. All write actions (ack / silence / restart) live in the web UI behind passkey + TOTP step-up.</i>",
	}, "\n"))
}

func (n *tgNotifier) replyStatus() {
	if n.fleet == nil || n.engine == nil {
		n.reply("not ready")
		return
	}
	nodes := n.fleet.snapshotNodes()
	var lines []string
	lines = append(lines, "<b>NCN fleet</b>")
	for _, s := range nodes {
		if s == nil {
			lines = append(lines, "• <code>?</code> · no scrape yet")
			continue
		}
		flag := "🟢"
		if !s.OK {
			flag = "🔴"
		}
		peers := bgpPeerSummary(s.Protocols)
		lines = append(lines,
			fmt.Sprintf("%s <code>@%s</code> · cpu=%s mem=%s disk=%s · bird=%s · bgp=%s · wg=%d",
				flag,
				html.EscapeString(s.Node.ID),
				fmtPct(s.CPUPct), fmtPct(s.MemPct), fmtPct(s.DiskPct),
				html.EscapeString(orDash(s.BirdVer)),
				peers,
				len(s.WG),
			))
	}
	// Alert summary
	all := n.engine.activeSnapshot("")
	crit, warn, info := 0, 0, 0
	for _, a := range all {
		switch a.Severity {
		case sevCritical:
			crit++
		case sevWarn:
			warn++
		case sevInfo:
			info++
		}
	}
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("alerts · 🔴 %d crit · 🟡 %d warn · 🔵 %d info", crit, warn, info))
	n.reply(strings.Join(lines, "\n"))
}

func (n *tgNotifier) replyAlerts(args []string) {
	if n.engine == nil {
		n.reply("not ready")
		return
	}
	var filter severity
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "crit", "critical":
			filter = sevCritical
		case "warn", "warning":
			filter = sevWarn
		case "info":
			filter = sevInfo
		}
	}
	list := n.engine.activeSnapshot(filter)
	if len(list) == 0 {
		if filter != "" {
			n.reply(fmt.Sprintf("✓ no active <code>%s</code> alerts", html.EscapeString(string(filter))))
		} else {
			n.reply("✓ no active alerts")
		}
		return
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Severity != list[j].Severity {
			// crit > warn > info ordering
			return sevWeight(list[i].Severity) > sevWeight(list[j].Severity)
		}
		return list[i].FiredAt < list[j].FiredAt
	})
	now := time.Now().Unix()
	var lines []string
	lines = append(lines, fmt.Sprintf("<b>%d active alert(s)</b>", len(list)))
	for _, a := range list {
		sym := map[severity]string{sevCritical: "🔴", sevWarn: "🟡", sevInfo: "🔵"}[a.Severity]
		dur := humanDuration(now - a.FiredAt)
		lines = append(lines,
			fmt.Sprintf("%s <code>@%s</code> %s · %s\n  <code>%s</code>",
				sym,
				html.EscapeString(a.NodeID),
				html.EscapeString(a.RuleID),
				dur,
				html.EscapeString(orDash(a.Message)),
			))
	}
	n.reply(strings.Join(lines, "\n"))
}

func (n *tgNotifier) replyNode(args []string) {
	if n.fleet == nil {
		n.reply("not ready")
		return
	}
	if len(args) == 0 {
		n.reply("usage · <code>/node &lt;id&gt;</code> — try ctrl-01, pop-05, pop-03, pop-04")
		return
	}
	want := strings.ToLower(args[0])
	for _, s := range n.fleet.snapshotNodes() {
		if s == nil || strings.ToLower(s.Node.ID) != want {
			continue
		}
		var lines []string
		flag := "🟢"
		if !s.OK {
			flag = "🔴"
		}
		lines = append(lines,
			fmt.Sprintf("%s <b><code>@%s</code></b> · %s",
				flag, html.EscapeString(s.Node.ID), html.EscapeString(s.Node.Label)))
		if s.Hostname != "" {
			lines = append(lines, fmt.Sprintf("host <code>%s</code> · addr <code>%s</code>",
				html.EscapeString(s.Hostname), html.EscapeString(s.Node.Address)))
		}
		if !s.OK {
			lines = append(lines, fmt.Sprintf("scrape err: <code>%s</code>", html.EscapeString(s.Error)))
		}
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("<code>cpu=%s mem=%s disk=%s load1=%s</code>",
			fmtPct(s.CPUPct), fmtPct(s.MemPct), fmtPct(s.DiskPct), fmtNum(s.Load1, 2)))
		lines = append(lines, fmt.Sprintf("<code>bird=%s · peers=%s · wg=%d ifaces · tun=%d</code>",
			html.EscapeString(orDash(s.BirdVer)),
			bgpPeerSummary(s.Protocols),
			len(s.WG),
			len(s.Tunnels),
		))
		if s.FetchedAt > 0 {
			lines = append(lines, fmt.Sprintf("<i>last scrape · %ds ago</i>",
				time.Now().Unix()-s.FetchedAt))
		}
		// Active alerts on this node
		if n.engine != nil {
			active := n.engine.activeSnapshot("")
			mine := []alertEvent{}
			for _, a := range active {
				if a.NodeID == s.Node.ID {
					mine = append(mine, a)
				}
			}
			lines = append(lines, "")
			if len(mine) == 0 {
				lines = append(lines, "<i>active alerts: none</i>")
			} else {
				lines = append(lines, fmt.Sprintf("<b>active alerts · %d</b>", len(mine)))
				for _, a := range mine {
					sym := map[severity]string{sevCritical: "🔴", sevWarn: "🟡", sevInfo: "🔵"}[a.Severity]
					lines = append(lines, fmt.Sprintf("%s %s · <code>%s</code>",
						sym, html.EscapeString(a.RuleID), html.EscapeString(orDash(a.Message))))
				}
			}
		}
		n.reply(strings.Join(lines, "\n"))
		return
	}
	n.reply(fmt.Sprintf("unknown node <code>%s</code>", html.EscapeString(args[0])))
}

func (n *tgNotifier) replyBGP(args []string) {
	if n.fleet == nil {
		n.reply("not ready")
		return
	}
	wantNode := ""
	if len(args) > 0 {
		wantNode = strings.ToLower(args[0])
	}
	var lines []string
	lines = append(lines, "<b>BGP peers</b>")
	for _, s := range n.fleet.snapshotNodes() {
		if s == nil {
			continue
		}
		if wantNode != "" && strings.ToLower(s.Node.ID) != wantNode {
			continue
		}
		up, down := 0, 0
		var rows []string
		for _, p := range s.Protocols {
			if p.Proto != "BGP" {
				continue
			}
			if p.Healthy {
				up++
				continue
			}
			down++
			rows = append(rows, fmt.Sprintf("  🔴 %s · state=%s · info=%s",
				html.EscapeString(p.Name), html.EscapeString(p.State), html.EscapeString(p.Info)))
		}
		lines = append(lines, fmt.Sprintf("<code>@%s</code> · ✓ %d up · ✗ %d down",
			html.EscapeString(s.Node.ID), up, down))
		lines = append(lines, rows...)
	}
	n.reply(strings.Join(lines, "\n"))
}

func (n *tgNotifier) replyProbes(args []string) {
	if n.fleet == nil {
		n.reply("not ready")
		return
	}
	wantNode := ""
	if len(args) > 0 {
		wantNode = strings.ToLower(args[0])
	}
	var lines []string
	lines = append(lines, "<b>probes · last RTT</b>")
	for _, s := range n.fleet.snapshotNodes() {
		if s == nil || len(s.Probes) == 0 {
			continue
		}
		if wantNode != "" && strings.ToLower(s.Node.ID) != wantNode {
			continue
		}
		lines = append(lines, fmt.Sprintf("<code>@%s</code>", html.EscapeString(s.Node.ID)))
		for _, p := range s.Probes {
			sym := "✓"
			rtt := fmtNum(p.LastMS, 0) + "ms"
			if !p.LastOK {
				sym = "✗"
				rtt = "down"
			}
			lines = append(lines, fmt.Sprintf("  %s %s · <code>%s</code>",
				sym, html.EscapeString(p.Name), rtt))
		}
	}
	n.reply(strings.Join(lines, "\n"))
}

// ---------- helpers ----------

func sevWeight(s severity) int {
	switch s {
	case sevCritical:
		return 3
	case sevWarn:
		return 2
	case sevInfo:
		return 1
	}
	return 0
}

// bgpPeerSummary returns "7/8" — N established BGP peers out of M total.
func bgpPeerSummary(protos []birdProtocol) string {
	up, tot := 0, 0
	for _, p := range protos {
		if p.Proto != "BGP" {
			continue
		}
		tot++
		if p.Healthy {
			up++
		}
	}
	return fmt.Sprintf("%d/%d", up, tot)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
