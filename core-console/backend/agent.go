// agent.go — the DeepSeek ops-agent loop (tool-calling with a human approval
// gate on every write/command). agent_tools.go holds the tools + safety model.
//
// Statelessness: the conversation (messages, incl. assistant tool_calls and
// role:"tool" results) is carried by the CLIENT and re-sent each step, like
// handleAIChat. The server advances it: read-only tool_calls auto-execute; the
// first write/command tool_call PAUSES the loop and returns a `pending` for a
// human to approve. On approve, the server executes (re-parsing args from the
// message, never trusting client-supplied args), appends the result, and
// resumes. Surfaces: console (Assistant.vue) + bot (/agent, bot_agent_tg.go).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const agentMaxSteps = 12

const agentSystemPrompt = "You are the NCN ops agent. You can call tools to inspect the fleet and, with human approval, change it. " +
	"Read-only tools (list_nodes, fleet_status, node_detail, active_alerts, op_failures) run automatically. " +
	"Write/command tools (decommission, recommission, delete_node, mesh_apply, run_command) are reviewed and approved by a human operator before they run — propose them with a clear reason. " +
	"Investigate with read-only tools first, then propose the minimal change. For run_command prefer read-only diagnostics; keep commands targeted. Be concise.\n\n" + aiProjectBrief

// aiCtxAuth is the operator store, for the agent's admin check. Set in main.go.
var aiCtxAuth *authStore

func operatorIsAdmin(username string) bool {
	if aiCtxAuth == nil || username == "" {
		return false
	}
	aiCtxAuth.mu.RLock()
	defer aiCtxAuth.mu.RUnlock()
	op, ok := aiCtxAuth.operators[username]
	return ok && op.Role == roleAdmin
}

// aiNickPrompt is a system-prompt fragment that tells the model to address the
// operator by their self-chosen 称呼 (set in-chat via /callme). Empty when no
// 称呼 is set, so default behaviour is unchanged. Shared by the agent (here),
// the bot /ask·/summary, and the console assistant — so a 称呼 set once is
// reflected everywhere the AI talks to that operator.
func aiNickPrompt(op string) string {
	if aiCtxAuth == nil || op == "" {
		return ""
	}
	if nick := aiCtxAuth.botNick(op); nick != "" {
		return "\n\nThe operator you are assisting goes by \"" + nick + "\". Address them by that name when you greet or confirm — naturally, not in every sentence."
	}
	return ""
}

// agentPending is a write/command tool_call awaiting human approval.
type agentPending struct {
	ToolCallID string         `json:"tool_call_id"`
	Name       string         `json:"name"`
	Args       map[string]any `json:"args"`
	Summary    string         `json:"summary"` // human-readable; for run_command shows node+command
}

type agentResult struct {
	Messages []aiMsg       `json:"messages"`
	Pending  *agentPending `json:"pending,omitempty"`
	Final    string        `json:"final,omitempty"`
}

func appendToolResult(messages []aiMsg, id, name, content string) []aiMsg {
	return append(messages, aiMsg{Role: "tool", ToolCallID: id, Name: name, Content: content})
}

// firstUnansweredToolCall finds the most recent assistant message carrying
// tool_calls and returns its first tool_call that has no role:"tool" answer yet.
func firstUnansweredToolCall(messages []aiMsg) (toolCall, bool) {
	a := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
			a = i
			break
		}
	}
	if a < 0 {
		return toolCall{}, false
	}
	answered := map[string]bool{}
	for _, m := range messages[a+1:] {
		if m.Role == "tool" {
			answered[m.ToolCallID] = true
		}
	}
	for _, tc := range messages[a].ToolCalls {
		if !answered[tc.ID] {
			return tc, true
		}
	}
	return toolCall{}, false
}

func summarizePending(tc toolCall) string {
	args := parseToolArgs(tc.Function.Arguments)
	switch tc.Function.Name {
	case "run_command":
		return fmt.Sprintf("run on %s: %s", argStr(args, "node_id"), argStr(args, "command"))
	default:
		if id := argStr(args, "id"); id != "" {
			return tc.Function.Name + " " + id
		}
		return tc.Function.Name
	}
}

// agentAdvance drives the loop until a final answer or a write needing approval.
// allowWrites controls whether write/command tools are OFFERED to the model
// (admins on the console; everyone on the bot, where approval is routed to an
// admin). A write tool_call ALWAYS pauses (returns pending) — execution
// authority lives solely in agentExecuteApproved, which requires an admin.
// onText/onTool are optional live-progress hooks for streaming surfaces (the
// bot). onText receives final-answer text deltas; onTool fires before each
// read-only tool runs. Both nil → plain non-streaming behaviour (console/MCP).
func agentAdvance(ctx context.Context, messages []aiMsg, allowWrites bool, actor string, onText func(string), onTool func(name, summary string)) (agentResult, error) {
	if !globalAI.enabled() {
		return agentResult{}, fmt.Errorf("DeepSeek not configured")
	}
	model := aiModelFor(purposeAgent)
	sys := agentSystemPrompt
	if mem := aiMemoryFor(actor); mem != "" {
		sys += "\n\n" + mem
	}
	sys += aiNickPrompt(actor) // address the operator by their /callme 称呼
	reg := agentToolRegistry()
	for step := 0; step < agentMaxSteps; step++ {
		if tc, ok := firstUnansweredToolCall(messages); ok {
			t := reg[tc.Function.Name]
			switch {
			case t == nil:
				messages = appendToolResult(messages, tc.ID, tc.Function.Name, "ERROR: unknown tool")
			case t.write:
				// Never auto-execute a write/command — pause for human approval
				// (self-approve for an admin, or routed to an admin for an
				// operator). agentExecuteApproved is the only executor.
				return agentResult{Messages: messages, Pending: &agentPending{
					ToolCallID: tc.ID, Name: tc.Function.Name,
					Args: parseToolArgs(tc.Function.Arguments), Summary: summarizePending(tc),
				}}, nil
			default: // read-only — auto-execute
				if onTool != nil {
					onTool(tc.Function.Name, summarizePending(tc))
				}
				out, err := t.exec(ctx, parseToolArgs(tc.Function.Arguments), actor)
				if err != nil {
					out = "ERROR: " + err.Error()
				}
				messages = appendToolResult(messages, tc.ID, tc.Function.Name, out)
			}
			continue
		}
		// All tool_calls answered (or none) → ask the model for the next step.
		var asst aiMsg
		var err error
		if onText != nil {
			asst, err = globalAI.completeToolsStream(ctx, model, sys, messages, agentToolDefs(allowWrites), onText)
		} else {
			asst, err = globalAI.completeTools(ctx, model, sys, messages, agentToolDefs(allowWrites))
		}
		if err != nil {
			return agentResult{}, err
		}
		messages = append(messages, asst)
		if len(asst.ToolCalls) == 0 {
			return agentResult{Messages: messages, Final: asst.Content}, nil
		}
	}
	return agentResult{Messages: messages, Final: "(agent stopped: step limit reached — ask a narrower question)"}, nil
}

// agentExecuteApproved runs a previously-proposed write tool_call (looked up in
// messages by id so the args can't be tampered with client-side), audits it,
// appends the result, and resumes. decision "deny" records a refusal instead.
func agentExecuteApproved(ctx context.Context, messages []aiMsg, toolCallID, decision string, isAdmin bool, actor string, onText func(string), onTool func(name, summary string)) (agentResult, error) {
	// Locate the pending tool_call within the most recent assistant tool_calls.
	a := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
			a = i
			break
		}
	}
	if a < 0 {
		return agentResult{}, fmt.Errorf("no pending tool call")
	}
	var tc *toolCall
	for i := range messages[a].ToolCalls {
		if messages[a].ToolCalls[i].ID == toolCallID {
			tc = &messages[a].ToolCalls[i]
			break
		}
	}
	if tc == nil {
		return agentResult{}, fmt.Errorf("tool call %q not found", toolCallID)
	}
	// Guard: already answered?
	for _, m := range messages[a+1:] {
		if m.Role == "tool" && m.ToolCallID == toolCallID {
			return agentResult{}, fmt.Errorf("tool call already resolved")
		}
	}
	reg := agentToolRegistry()
	t := reg[tc.Function.Name]
	if t == nil || !t.write {
		return agentResult{}, fmt.Errorf("not an approvable tool")
	}
	if decision == "deny" {
		messages = appendToolResult(messages, tc.ID, tc.Function.Name, "operator denied this action")
		return agentAdvance(ctx, messages, isAdmin, actor, onText, onTool)
	}
	if !isAdmin {
		return agentResult{}, fmt.Errorf("only an admin can approve write actions")
	}
	args := parseToolArgs(tc.Function.Arguments) // SERVER-side args, not client's
	out, err := t.exec(ctx, args, actor)
	auditRecord(nil, AuditEvent{
		Event: "ai.tool.exec", Severity: auditSevWarn, Actor: "ai:" + actor,
		Target: argStr(args, "id") + argStr(args, "node_id"),
		Outcome: map[bool]string{true: "fail", false: "ok"}[err != nil],
		Details: map[string]any{"tool": tc.Function.Name, "args": args},
	})
	if err != nil {
		out = "ERROR: " + err.Error()
	}
	messages = appendToolResult(messages, tc.ID, tc.Function.Name, out)
	return agentAdvance(ctx, messages, isAdmin, actor, onText, onTool)
}

// ── HTTP (console) ───────────────────────────────────────────────────────────

// POST /api/v1/auth/ai/agent  {messages:[…]} → {messages, pending?, final?}
func handleAIAgent(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	if op == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	if !globalAI.enabled() {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "DeepSeek not configured"})
		return
	}
	var body struct {
		Messages []aiMsg `json:"messages"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<18)).Decode(&body); err != nil || len(body.Messages) == 0 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	res, err := agentAdvance(ctx, body.Messages, operatorIsAdmin(op), op, nil, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, envelope{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: res})
}

// ── MCP bridge ───────────────────────────────────────────────────────────────
// These expose the SAME tool registry over a thin HTTP surface so an external
// MCP server (mcp/ncn-mcp.mjs, driven by a local Claude Code) can list + invoke
// fleet tools. Auth is the operator's API token (Bearer ncntok_… flows through
// requireAuth as that operator). Read-only tools are open to any operator;
// write/command tools require admin; every write is audited mcp:<op>. The
// human-in-the-loop here is the Claude Code user approving each MCP tool call.

// GET /api/v1/auth/agent/tools → {tools: [toolDef…]} (write tools only for admin)
func handleAgentTools(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	if op == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"tools": agentToolDefs(operatorIsAdmin(op))}})
}

// POST /api/v1/auth/agent/tool  {name, args} → {content} — run ONE tool.
func handleAgentToolExec(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	if op == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	var body struct {
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || body.Name == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad request"})
		return
	}
	t := agentToolRegistry()[body.Name]
	if t == nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "unknown tool"})
		return
	}
	if t.write && !operatorIsAdmin(op) {
		writeJSON(w, http.StatusForbidden, envelope{OK: false, Error: "write tools require an admin operator"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	out, err := t.exec(ctx, body.Args, op)
	if t.write {
		auditRecord(r, AuditEvent{
			Event: "ai.tool.exec", Severity: auditSevWarn, Actor: "mcp:" + op,
			Target:  argStr(body.Args, "id") + argStr(body.Args, "node_id"),
			Outcome: map[bool]string{true: "fail", false: "ok"}[err != nil],
			Details: map[string]any{"tool": body.Name, "args": body.Args, "via": "mcp"},
		})
	}
	if err != nil {
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"content": "ERROR: " + err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"content": out}})
}

// POST /api/v1/auth/ai/agent/approve  {messages, tool_call_id, decision} → agentResult
func handleAIAgentApprove(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	if op == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	var body struct {
		Messages   []aiMsg `json:"messages"`
		ToolCallID string  `json:"tool_call_id"`
		Decision   string  `json:"decision"` // approve | deny
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<18)).Decode(&body); err != nil || len(body.Messages) == 0 || body.ToolCallID == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	res, err := agentExecuteApproved(ctx, body.Messages, body.ToolCallID, body.Decision, operatorIsAdmin(op), op, nil, nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: res})
}

// ── SSE streaming (console) ──────────────────────────────────────────────────
// sseAgent runs an agent turn over Server-Sent Events so the web assistant can
// render tool steps + the answer live. Events: `tool` {name,summary}, `text`
// {delta}, `done` {agentResult}, `error` {error}. The agent loop runs in THIS
// goroutine, so the onText/onTool callbacks write SSE serially (no locking).
func sseAgent(w http.ResponseWriter, r *http.Request, run func(onText func(string), onTool func(string, string)) (agentResult, error)) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "streaming unsupported"})
		return
	}
	// Clear the server's write deadline so a long agent run isn't cut at 75s,
	// and tell nginx not to buffer the event stream.
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		_, _ = w.Write([]byte("event: " + event + "\ndata: " + string(b) + "\n\n"))
		flusher.Flush()
	}
	res, err := run(
		func(d string) { send("text", map[string]string{"delta": d}) },
		func(name, summary string) { send("tool", map[string]string{"name": name, "summary": summary}) },
	)
	if err != nil {
		send("error", map[string]string{"error": err.Error()})
		return
	}
	send("done", res)
}

// POST /api/v1/auth/ai/agent/stream  {messages} → SSE
func handleAIAgentStream(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	if op == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	if !globalAI.enabled() {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "DeepSeek not configured"})
		return
	}
	var body struct {
		Messages []aiMsg `json:"messages"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<18)).Decode(&body); err != nil || len(body.Messages) == 0 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad request"})
		return
	}
	admin := operatorIsAdmin(op)
	sseAgent(w, r, func(onText func(string), onTool func(string, string)) (agentResult, error) {
		return agentAdvance(r.Context(), body.Messages, admin, op, onText, onTool)
	})
}

// POST /api/v1/auth/ai/agent/approve/stream  {messages, tool_call_id, decision} → SSE
func handleAIAgentApproveStream(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	if op == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	var body struct {
		Messages   []aiMsg `json:"messages"`
		ToolCallID string  `json:"tool_call_id"`
		Decision   string  `json:"decision"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<18)).Decode(&body); err != nil || len(body.Messages) == 0 || body.ToolCallID == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad request"})
		return
	}
	admin := operatorIsAdmin(op)
	sseAgent(w, r, func(onText func(string), onTool func(string, string)) (agentResult, error) {
		return agentExecuteApproved(r.Context(), body.Messages, body.ToolCallID, body.Decision, admin, op, onText, onTool)
	})
}
