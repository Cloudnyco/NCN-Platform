// bot_ai.go — the DeepSeek-powered assistant features (deepseek.go is the
// client). Covers everything the operator asked for:
//   /ask <q>   — ops Q&A grounded in live fleet + alert context (operator-gated)
//   /summary   — a human status summary + likely root causes (operator-gated)
//   /chat <m>  — casual companion chat, open to the whole ops group (陪群友)
//   @mention   — same casual chat when the bot is @-mentioned in the group
//   console    — handleAIChat backs the /admin/assistant page
//
// Every LLM call runs in its own goroutine: the long-poll loop processes
// updates sequentially, so blocking it on a multi-second completion would stall
// the whole bot. Replies use replyTo(chat, …) with the captured chat (cmdChat is
// already cleared by the time the goroutine runs).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Markdown → Telegram-HTML (safe subset). We escape FIRST, so the only tags in
// the output are ones we insert → malformed model output can't break Telegram's
// HTML parser. Code spans (fenced + inline) are stashed aside BEFORE inline
// formatting runs, so a `*` inside code stays literal. We deliberately do NOT
// treat `_`/`__` as emphasis — that would mangle identifiers like
// bgp_down_count / ibgp_pop03. Unbalanced delimiters stay literal.
var (
	mdFence   = regexp.MustCompile("(?s)```[a-zA-Z0-9_-]*\\n?(.*?)```")
	mdCode    = regexp.MustCompile("`([^`\\n]+)`")
	mdHeading = regexp.MustCompile(`(?m)^#{1,6}[ \t]+(.+?)[ \t]*$`)
	mdLink    = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^)\s]+)\)`)
	mdBold    = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	mdStrike  = regexp.MustCompile(`~~([^~\n]+)~~`)
	mdBullet  = regexp.MustCompile(`(?m)^([ \t]*)[-*][ \t]+`)
	mdItalic  = regexp.MustCompile(`\*(\S(?:[^*\n]*?\S)?)\*`)
	mdStash   = regexp.MustCompile("\x00(\\d+)\x00")
)

func mdToTG(s string) string {
	s = html.EscapeString(s)

	// Pull code/pre out so the inline rules below never touch their contents.
	var spans []string
	stash := func(tag, body string) string {
		spans = append(spans, "<"+tag+">"+body+"</"+tag+">")
		return fmt.Sprintf("\x00%d\x00", len(spans)-1)
	}
	s = mdFence.ReplaceAllStringFunc(s, func(m string) string {
		return stash("pre", mdFence.FindStringSubmatch(m)[1])
	})
	s = mdCode.ReplaceAllStringFunc(s, func(m string) string {
		return stash("code", mdCode.FindStringSubmatch(m)[1])
	})

	s = mdHeading.ReplaceAllString(s, "<b>$1</b>") // heading → bold (keep the text)
	s = mdLink.ReplaceAllString(s, `<a href="$2">$1</a>`)
	s = mdBold.ReplaceAllString(s, "<b>$1</b>")
	s = mdStrike.ReplaceAllString(s, "<s>$1</s>")
	s = mdBullet.ReplaceAllString(s, "$1• ") // list bullets → •, keep indent
	s = mdItalic.ReplaceAllString(s, "<i>$1</i>")

	// Restore the stashed code/pre spans.
	s = mdStash.ReplaceAllStringFunc(s, func(m string) string {
		var i int
		fmt.Sscanf(m, "\x00%d\x00", &i)
		if i >= 0 && i < len(spans) {
			return spans[i]
		}
		return ""
	})
	return s
}

const aiChatPrompt = "You are a friendly, witty but reliable AI member of the Acme Net ops group. Chat naturally and casually in English; lend a hand on ops topics too. Keep replies short and natural.\n\n" + aiProjectBrief

const aiCooldown = 4 * time.Second

func (n *tgNotifier) setAI(ai *deepseekClient, botUsername string) {
	n.ai = ai
	n.botUsername = strings.TrimPrefix(strings.TrimSpace(botUsername), "@")
	n.aiMu.Lock()
	n.aiLast = map[string]time.Time{}
	n.aiMu.Unlock()
}

// aiThrottled returns true if this chat used the AI within the cooldown window
// (and otherwise stamps it). Bounds runaway cost / chat loops.
func (n *tgNotifier) aiThrottled(chat string) bool {
	n.aiMu.Lock()
	defer n.aiMu.Unlock()
	if n.aiLast == nil {
		n.aiLast = map[string]time.Time{}
	}
	if time.Since(n.aiLast[chat]) < aiCooldown {
		return true
	}
	n.aiLast[chat] = time.Now()
	return false
}

func (n *tgNotifier) mentionsBot(text string) bool {
	if n.botUsername == "" {
		return false
	}
	return strings.Contains(strings.ToLower(text), "@"+strings.ToLower(n.botUsername))
}

func (n *tgNotifier) stripMention(text string) string {
	if n.botUsername == "" {
		return strings.TrimSpace(text)
	}
	// case-insensitive removal of @botusername
	low := strings.ToLower(text)
	tag := "@" + strings.ToLower(n.botUsername)
	if i := strings.Index(low, tag); i >= 0 {
		text = text[:i] + text[i+len(tag):]
	}
	return strings.TrimSpace(text)
}

// aiReplyAsync sends the optional "thinking" line, then completes off-thread and
// posts the answer. AI text is HTML-escaped (rendered as plain text) so model
// markdown/angle-brackets can't break Telegram's HTML parse mode.
func (n *tgNotifier) aiReplyAsync(chat, thinking, model, system string, msgs []aiMsg, temp float64, maxTok int) {
	if !n.ai.enabled() {
		n.replyTo(chat, "🤖 DeepSeek not configured (server missing NCN_DEEPSEEK_API_KEY)")
		return
	}
	if thinking != "" {
		n.replyTo(chat, thinking)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		ans, err := n.ai.complete(ctx, model, system, msgs, temp, maxTok)
		if err != nil {
			n.replyTo(chat, "🤖 failed: "+html.EscapeString(err.Error()))
			return
		}
		n.replyTo(chat, "🤖 "+mdToTG(ans))
	}()
}

// aiFleetContext renders a compact, secret-free snapshot of the fleet + active
// alerts for grounding ops questions.
func aiFleetContext(f *fleetScraper, e *alertEngine) string {
	var b strings.Builder
	if f != nil {
		b.WriteString("Node status:\n")
		for _, s := range f.snapshotNodes() {
			if s == nil {
				continue
			}
			st := "up"
			if !s.OK {
				st = fmt.Sprintf("DOWN(×%d)", s.ConsecFail)
			}
			b.WriteString(fmt.Sprintf("- %s region=%d %s cpu=%s mem=%s disk=%s bird=%s cert=%dd\n",
				s.Node.ID, s.Node.Region, st, fmtPct(s.CPUPct), fmtPct(s.MemPct), fmtPct(s.DiskPct), orDash(s.BirdVer), s.AgentCertDaysLeft))
		}
	}
	if e != nil {
		alerts := e.activeSnapshot("")
		if len(alerts) == 0 {
			b.WriteString("\nNo active alerts.\n")
		} else {
			fmt.Fprintf(&b, "\nActive alerts (%d):\n", len(alerts))
			for _, a := range alerts {
				fmt.Fprintf(&b, "- [%s] %s@%s: %s\n", a.Severity, a.RuleID, a.NodeID, orDash(a.Message))
			}
		}
	}
	return b.String()
}

// replyAsk — /ask <question>, grounded in live fleet + alert context.
func (n *tgNotifier) replyAsk(chat, op, question string) {
	question = strings.TrimSpace(question)
	if question == "" {
		n.replyTo(chat, "Usage · <code>/ask &lt;question&gt;</code> — e.g. /ask why is pop-05 cert about to expire and what do I do")
		return
	}
	user := "Operator question: " + question + "\n\nCurrent live data:\n" + aiFleetContext(n.fleet, n.engine)
	n.aiReplyAsync(chat, "🤖 Thinking…", aiModelFor(purposeAsk), aiSystemPrompt+aiNickPrompt(op), []aiMsg{{Role: "user", Content: user}}, 0.3, 900)
}

// replySummary — /summary: human status summary + likely root causes.
func (n *tgNotifier) replySummary(chat, op string) {
	user := "Based on the live data below, give a short ops status summary and call out anything noteworthy or likely root causes:\n\n" + aiFleetContext(n.fleet, n.engine)
	n.aiReplyAsync(chat, "🤖 Summarizing…", aiModelFor(purposeSummary), aiSystemPrompt+aiNickPrompt(op), []aiMsg{{Role: "user", Content: user}}, 0.4, 900)
}

// replyChat — /chat or @mention: casual companion. Open to the group; throttled.
func (n *tgNotifier) replyChat(chat, msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		n.replyTo(chat, "🤖 I am here — what is up? (<code>/chat your message</code>, or @mention / reply me)")
		return
	}
	if n.aiThrottled(chat) {
		return // silently skip — bound cost / avoid loops
	}
	n.aiReplyAsync(chat, "", aiModelFor(purposeChat), aiChatPrompt, []aiMsg{{Role: "user", Content: msg}}, 0.9, 600)
}

// replyModel — /model (show per-purpose models + available) and
// /model <purpose> <model> (admin-only set).
func (n *tgNotifier) replyModel(chat, op string, args []string) {
	if globalAIModels == nil {
		n.replyTo(chat, "model store not ready")
		return
	}
	m, av := globalAIModels.snapshot()
	if len(args) == 0 {
		var b strings.Builder
		b.WriteString("🧠 <b>AI models (per purpose)</b>\n")
		for _, p := range aiPurposes {
			b.WriteString("<code>" + p + "</code> · " + html.EscapeString(m[p]) + "\n")
		}
		b.WriteString("\navailable: " + html.EscapeString(strings.Join(av, " / ")))
		b.WriteString("\n<blockquote>set (admin): /model &lt;purpose&gt; &lt;model&gt;\ne.g. /model agent deepseek-v4-flash</blockquote>")
		n.replyTo(chat, b.String())
		return
	}
	if len(args) < 2 {
		n.replyTo(chat, "usage · <code>/model &lt;purpose&gt; &lt;model&gt;</code> · purposes: "+strings.Join(aiPurposes, " "))
		return
	}
	if !operatorIsAdmin(op) {
		n.replyTo(chat, "⚠️ only an admin can change models")
		return
	}
	if err := globalAIModels.set(args[0], args[1]); err != nil {
		n.replyTo(chat, "failed: "+html.EscapeString(err.Error()))
		return
	}
	auditRecord(nil, AuditEvent{Event: "ai.model.set", Severity: auditSevInfo, Actor: "tg:" + op,
		Details: map[string]any{"purpose": args[0], "model": args[1]}})
	n.replyTo(chat, "✓ <code>"+html.EscapeString(args[0])+"</code> → "+html.EscapeString(args[1]))
}

// handleAIChat — POST /api/v1/auth/ai/chat for the console assistant page.
// Body: {"messages":[{role,content},…]}. Returns {"reply": "..."}. Grounded in
// the same live fleet context as the bot.
func handleAIChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	if !globalAI.enabled() {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "DeepSeek not configured"})
		return
	}
	var body struct {
		Messages []aiMsg `json:"messages"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad request"})
		return
	}
	if len(body.Messages) == 0 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "no messages"})
		return
	}
	// Keep only the trailing window + sanitize roles.
	msgs := body.Messages
	if len(msgs) > 20 {
		msgs = msgs[len(msgs)-20:]
	}
	for i := range msgs {
		if msgs[i].Role != "user" && msgs[i].Role != "assistant" {
			msgs[i].Role = "user"
		}
	}
	system := aiSystemPrompt + "\n\nCurrent live data:\n" + aiFleetContext(aiCtxFleet, aiCtxEngine) + aiNickPrompt(adminOperator(r))
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	ans, err := globalAI.complete(ctx, aiModelFor(purposeAsk), system, msgs, 0.5, 1200)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, envelope{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]string{"reply": ans}})
}

// aiCtxFleet / aiCtxEngine give the console AI handler the same live context the
// bot has. Set in main() after the fleet + engine exist.
var (
	aiCtxFleet  *fleetScraper
	aiCtxEngine *alertEngine
)
