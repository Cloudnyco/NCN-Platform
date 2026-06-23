// bot_agent_tg.go — the Telegram surface of the DeepSeek ops agent (agent.go).
//
// /agent <task> starts a per-(chat,user) in-memory session. Read-only tools run
// automatically. Write/command tools always pause for approval:
//   * an ADMIN's own session self-approves in place (✅/✖ on the card);
//   * an OPERATOR's (non-admin) session routes the write to admins — a card is
//     posted to the ops GROUP and DM'd to every bound admin; any admin approves
//     and the result returns to the operator's chat. Cross-actor approval is
//     audited ai.agent.approve (admin) + ai.tool.exec (on the operator's behalf).
// LLM calls run in goroutines; sessions TTL + a step cap bound cost.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	agentSessionTTL = 15 * time.Minute
	agentMaxTurns   = 24
)

type agentSession struct {
	op       string
	isAdmin  bool
	chat     string // where to deliver replies/cards for this session
	messages []aiMsg
	pending  *agentPending
	turns    int
	exp      time.Time
}

// adminApproval is one operator-initiated write awaiting an admin's sign-off.
type adminApproval struct {
	sessionKey string
	opChat     string
	op         string
	toolCallID string
	name       string
	summary    string
	exp        time.Time
}

func (n *tgNotifier) agentKey(chat string, uid int64) string {
	return chat + ":" + strconv.FormatInt(uid, 10)
}

// replyAgent — /agent <task>. The caller is already a verified operator.
func (n *tgNotifier) replyAgent(chat string, uid int64, op, task string) {
	if !n.ai.enabled() {
		n.replyTo(chat, "🤖 DeepSeek not configured (server missing NCN_DEEPSEEK_API_KEY)")
		return
	}
	task = strings.TrimSpace(task)
	if task == "" {
		n.replyTo(chat, "Usage · <code>/agent &lt;task&gt;</code> — e.g. /agent check why pop-05 looks unhealthy")
		return
	}
	key := n.agentKey(chat, uid)
	n.agentMu.Lock()
	if n.agentSessions == nil {
		n.agentSessions = map[string]*agentSession{}
	}
	now := time.Now()
	for k, s := range n.agentSessions {
		if now.After(s.exp) {
			delete(n.agentSessions, k)
		}
	}
	n.agentSessions[key] = &agentSession{
		op: op, isAdmin: operatorIsAdmin(op), chat: chat,
		messages: []aiMsg{{Role: "user", Content: task}},
		exp:      now.Add(agentSessionTTL),
	}
	n.agentMu.Unlock()
	n.replyTo(chat, "🤖 working on it…")
	go n.agentStep(key)
}

// sendNow posts a message synchronously and returns its message_id (for live
// editing). Silent. 0,err on failure.
func (n *tgNotifier) sendNow(ctx context.Context, chat, text string) (int64, error) {
	body, _ := json.Marshal(map[string]any{
		"chat_id": chat, "text": text, "parse_mode": "HTML",
		"disable_web_page_preview": true, "disable_notification": true,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.telegram.org/bot"+n.token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || !out.OK {
		return 0, fmt.Errorf("sendMessage failed")
	}
	return out.Result.MessageID, nil
}

// agentStep advances the session's loop one server turn and STREAMS the result
// by live-editing a single message: read-only tool steps appear as they run and
// the final answer streams in (throttled to respect Telegram's edit limits).
func (n *tgNotifier) agentStep(key string) {
	n.agentMu.Lock()
	sess := n.agentSessions[key]
	if sess == nil {
		n.agentMu.Unlock()
		return
	}
	if sess.turns >= agentMaxTurns {
		chat := sess.chat
		n.agentMu.Unlock()
		n.replyTo(chat, "🤖 step limit reached — start a narrower /agent task")
		return
	}
	sess.turns++
	msgs := append([]aiMsg(nil), sess.messages...)
	op, chat := sess.op, sess.chat
	n.agentMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	msgID, _ := n.sendNow(ctx, chat, "🤖 …") // 0 → fall back to a plain final reply

	var toolLog []string
	var answer strings.Builder
	var lastEdit time.Time
	render := func(force bool) {
		if msgID == 0 {
			return
		}
		now := time.Now()
		if !force && now.Sub(lastEdit) < 1200*time.Millisecond {
			return
		}
		lastEdit = now
		var b strings.Builder
		for _, l := range toolLog {
			b.WriteString(l + "\n")
		}
		if answer.Len() > 0 {
			if len(toolLog) > 0 {
				b.WriteString("\n")
			}
			b.WriteString("🤖 " + mdToTG(answer.String()))
		}
		txt := strings.TrimSpace(b.String())
		if txt == "" {
			txt = "🤖 …"
		}
		n.editMessage(ctx, chat, msgID, txt, nil)
	}
	onTool := func(name, summary string) {
		toolLog = append(toolLog, "🔧 <code>"+html.EscapeString(name)+"</code> "+html.EscapeString(summary))
		render(false)
	}
	onText := func(d string) { answer.WriteString(d); render(false) }

	// allowWrites=true: operators may PROPOSE writes; the approval gate (self for
	// admins, routed-to-admin for operators) decides whether they run.
	res, err := agentAdvance(ctx, msgs, true, op, onText, onTool)
	if err != nil {
		if msgID != 0 {
			n.editMessage(ctx, chat, msgID, "🤖 failed: "+html.EscapeString(err.Error()), nil)
		} else {
			n.replyTo(chat, "🤖 failed: "+html.EscapeString(err.Error()))
		}
		return
	}
	n.agentMu.Lock()
	if s := n.agentSessions[key]; s != nil {
		s.messages = res.Messages
		s.pending = res.Pending
		s.exp = time.Now().Add(agentSessionTTL)
	}
	n.agentMu.Unlock()

	if res.Pending != nil {
		// finalize the streamed status, then post the approval card(s).
		if msgID != 0 {
			hdr := strings.Join(toolLog, "\n")
			if hdr != "" {
				hdr += "\n"
			}
			n.editMessage(ctx, chat, msgID, hdr+"⏳ proposing <code>"+html.EscapeString(res.Pending.Name)+"</code> — awaiting approval", nil)
		}
		n.agentRenderSession(key, res)
		return
	}
	// Final answer — the streamed message already shows it; force a last edit so
	// the complete text is rendered. (Don't route through agentRenderSession,
	// which would send a duplicate message.)
	out := res.Final
	if out == "" {
		out = "(done)"
	}
	if msgID != 0 {
		answer.Reset()
		answer.WriteString(out)
		render(true)
	} else {
		n.replyTo(chat, "🤖 "+mdToTG(out))
	}
}

// agentRenderSession renders one step's outcome: final text, a self-approve
// card (admin session), or a routed-to-admin approval (operator session).
func (n *tgNotifier) agentRenderSession(key string, res agentResult) {
	n.agentMu.Lock()
	sess := n.agentSessions[key]
	n.agentMu.Unlock()
	if sess == nil {
		return
	}
	if res.Pending != nil {
		if sess.isAdmin {
			p := res.Pending
			text := "⚠️ <b>Approve action?</b>\n<code>" + html.EscapeString(p.Name) + "</code>\n<pre>" +
				html.EscapeString(p.Summary) + "</pre>"
			rows := [][]tgKbBtn{{
				{Text: "✅ Approve", Data: "ag:ok:" + p.ToolCallID},
				{Text: "✖ Deny", Data: "ag:no:" + p.ToolCallID},
			}}
			n.enqueue(tgPayload{Text: text, ChatID: sess.chat, ReplyMarkup: kbMarkup(rows)}, "agent self-approve")
		} else {
			n.routeWriteToAdmins(key, sess, res.Pending)
		}
		return
	}
	out := res.Final
	if out == "" {
		out = "(done)"
	}
	n.replyTo(sess.chat, "🤖 "+mdToTG(out))
}

// routeWriteToAdmins posts an operator's pending write to the ops group AND DMs
// every bound admin, with approve/deny buttons carrying a short single-use id.
func (n *tgNotifier) routeWriteToAdmins(key string, sess *agentSession, p *agentPending) {
	var b [4]byte
	_, _ = rand.Read(b[:])
	sid := hex.EncodeToString(b[:])

	n.agentMu.Lock()
	if n.adminApprovals == nil {
		n.adminApprovals = map[string]*adminApproval{}
	}
	now := time.Now()
	for k, a := range n.adminApprovals {
		if now.After(a.exp) {
			delete(n.adminApprovals, k)
		}
	}
	n.adminApprovals[sid] = &adminApproval{
		sessionKey: key, opChat: sess.chat, op: sess.op,
		toolCallID: p.ToolCallID, name: p.Name, summary: p.Summary,
		exp: now.Add(agentSessionTTL),
	}
	n.agentMu.Unlock()

	n.replyTo(sess.chat, "⏳ 写操作需管理员批准,已转交:\n<code>"+html.EscapeString(p.Name)+"</code> · "+html.EscapeString(p.Summary))

	card := "🛑 <b>Admin approval needed</b>\noperator <code>" + html.EscapeString(sess.op) + "</code> wants to run:\n<code>" +
		html.EscapeString(p.Name) + "</code>\n<pre>" + html.EscapeString(p.Summary) + "</pre>"
	mk := kbMarkup([][]tgKbBtn{{
		{Text: "✅ Approve", Data: "ag:adm:ok:" + sid},
		{Text: "✖ Deny", Data: "ag:adm:no:" + sid},
	}})
	// ops group (default chat)
	n.enqueue(tgPayload{Text: card, ReplyMarkup: mk}, "agent admin-approval group")
	// every bound admin's DM
	if n.auth != nil {
		for _, tgid := range n.auth.adminTelegramIDs() {
			n.enqueue(tgPayload{Text: card, ChatID: tgid, ReplyMarkup: mk}, "agent admin-approval dm")
		}
	}
}

// handleAgentCallback dispatches ag:ok/ag:no (self-approve) and ag:adm:ok/no
// (cross-actor admin approval). op is the resolved operator of the tapper.
func (n *tgNotifier) handleAgentCallback(ctx context.Context, cbID, chat string, _ int64, data string, userID int64, op string) {
	switch {
	case strings.HasPrefix(data, "ag:adm:"):
		n.handleAdminApproval(ctx, cbID, data, op)
	case strings.HasPrefix(data, "ag:ok:"), strings.HasPrefix(data, "ag:no:"):
		n.handleSelfApproval(ctx, cbID, chat, data, userID)
	default:
		n.answerCallback(ctx, cbID, "?")
	}
}

// handleSelfApproval — an admin approving their OWN session's write (same
// chat+user key). Execution still requires admin (agentExecuteApproved).
func (n *tgNotifier) handleSelfApproval(ctx context.Context, cbID, chat, data string, userID int64) {
	decision := "approve"
	id := strings.TrimPrefix(data, "ag:ok:")
	if strings.HasPrefix(data, "ag:no:") {
		decision = "deny"
		id = strings.TrimPrefix(data, "ag:no:")
	}
	key := n.agentKey(chat, userID)
	n.agentMu.Lock()
	sess := n.agentSessions[key]
	ok := sess != nil && sess.pending != nil && sess.pending.ToolCallID == id
	n.agentMu.Unlock()
	if !ok {
		n.answerCallback(ctx, cbID, "session expired or no pending action")
		return
	}
	n.answerCallback(ctx, cbID, map[bool]string{true: "approved — executing", false: "denied"}[decision == "approve"])
	go func() {
		n.agentMu.Lock()
		s := n.agentSessions[key]
		if s == nil || s.pending == nil {
			n.agentMu.Unlock()
			return
		}
		msgs := append([]aiMsg(nil), s.messages...)
		isAdmin, sop, tcid := s.isAdmin, s.op, s.pending.ToolCallID
		s.pending = nil
		n.agentMu.Unlock()
		cctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		res, err := agentExecuteApproved(cctx, msgs, tcid, decision, isAdmin, sop, nil, nil)
		if err != nil {
			n.replyTo(key2chat(key), "🤖 failed: "+html.EscapeString(err.Error()))
			return
		}
		n.agentMu.Lock()
		if s2 := n.agentSessions[key]; s2 != nil {
			s2.messages = res.Messages
			s2.pending = res.Pending
			s2.exp = time.Now().Add(agentSessionTTL)
		}
		n.agentMu.Unlock()
		n.agentRenderSession(key, res)
	}()
}

// handleAdminApproval — any admin approving/denying an operator's routed write.
func (n *tgNotifier) handleAdminApproval(ctx context.Context, cbID, data, tapper string) {
	decision := "approve"
	sid := strings.TrimPrefix(data, "ag:adm:ok:")
	if strings.HasPrefix(data, "ag:adm:no:") {
		decision = "deny"
		sid = strings.TrimPrefix(data, "ag:adm:no:")
	}
	if !operatorIsAdmin(tapper) {
		n.answerCallback(ctx, cbID, "需要 admin 权限")
		return
	}
	n.agentMu.Lock()
	appr := n.adminApprovals[sid]
	if appr != nil {
		delete(n.adminApprovals, sid) // single-use; first admin wins
	}
	n.agentMu.Unlock()
	if appr == nil {
		n.answerCallback(ctx, cbID, "已处理或已过期")
		return
	}
	n.answerCallback(ctx, cbID, map[bool]string{true: "approved", false: "denied"}[decision == "approve"])
	go func() {
		n.agentMu.Lock()
		sess := n.agentSessions[appr.sessionKey]
		if sess == nil {
			n.agentMu.Unlock()
			n.replyTo(appr.opChat, "🤖 会话已过期,审批作废")
			return
		}
		msgs := append([]aiMsg(nil), sess.messages...)
		sess.pending = nil
		n.agentMu.Unlock()

		cctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		res, err := agentExecuteApproved(cctx, msgs, appr.toolCallID, decision, true, appr.op, nil, nil)
		auditRecord(nil, AuditEvent{
			Event: "ai.agent.approve", Severity: auditSevWarn, Actor: "tg:" + tapper, Target: appr.op,
			Outcome: map[bool]string{true: "denied", false: "ok"}[decision == "deny"],
			Details: map[string]any{"tool": appr.name, "summary": appr.summary, "decision": decision},
		})
		if err != nil {
			n.replyTo(appr.opChat, "🤖 failed: "+html.EscapeString(err.Error()))
			return
		}
		n.agentMu.Lock()
		if s2 := n.agentSessions[appr.sessionKey]; s2 != nil {
			s2.messages = res.Messages
			s2.pending = res.Pending
			s2.exp = time.Now().Add(agentSessionTTL)
		}
		n.agentMu.Unlock()
		verb := map[bool]string{true: "批准", false: "拒绝"}[decision == "approve"]
		n.replyTo(appr.opChat, "✅ 管理员 <b>"+html.EscapeString(n.displayName(tapper, ""))+"</b> 已"+verb+"该操作")
		n.agentRenderSession(appr.sessionKey, res)
	}()
}

// key2chat extracts the chat from an agent session key (chat:userID). Chat ids
// are numeric (no ':'), so split on the last ':'.
func key2chat(key string) string {
	if i := strings.LastIndexByte(key, ':'); i >= 0 {
		return key[:i]
	}
	return key
}
