// deepseek.go — the shared DeepSeek (LLM) client + NCN context helpers.
//
// One small OpenAI-compatible chat client used by every AI feature: bot
// /ask · /summary · /chat (group companion) · op-failure AI diagnosis
// (bot_ai.go) and the console assistant (handleAIChat). The API key lives
// ONLY in the server env (NCN_DEEPSEEK_API_KEY in /etc/ncn-core-console/
// oauth.env, 0600) — never in the frontend, never logged. Gracefully disabled
// when the key is absent (features return a "未配置" notice instead of erroring).
//
// NOTE: these features send ops context (node ids/labels/status, alert text,
// failure reasons, operator messages) to DeepSeek — an external service. We
// keep the context compact and never include secrets/keys.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const deepseekEndpoint = "https://api.deepseek.com/chat/completions"

// aiProjectBrief is durable NCN background knowledge baked into every AI call's
// system prompt, so the model is "familiar with what we do" without re-feeding
// it each time. Live, volatile state (current node list, alerts) is appended
// separately per call via aiFleetContext — keep THIS to stable facts.
const aiProjectBrief = `About NCN (background knowledge — internalize this):
- NCN = Acme Net, autonomous system AS64500, a multi-PoP anycast IPv6 network (IPv4 too). Each PoP is a VPS node in a region, named like <metro>-<index> (e.g. ctrl-01, pop-01, pop-02). ctrl-01 is also the console/control node (local node, never decommissioned/deleted). The current node list and live status are in the "live data" attached to each request.
- Networking: nodes interconnect into a mesh via GRE tunnels (default) or WireGuard; BGP runs on BIRD, and config changes ALWAYS use "birdc configure soft" (never restart bird). Each node runs an agent (:9101, /v1/healthz, HMAC key auth). Mesh change flow: backup -> configure soft -> auto-rollback on failure (incl. tearing down GRE tunnels), only on active PoPs that already run bird.
- Console (core-console): frontend Vue 3 + Vite + Tailwind, backend Go (ncn-api on ctrl-01, systemd socket-activated, domain admin.example.com). Features: fleet monitoring (CPU/mem/disk/BGP/WireGuard/cert days), data-driven alert rules, VPS billing/renewals, peering applications + review, security audit, operator accounts (password/TOTP/passkey/SSH-key login + GitHub/Telegram OAuth binding).
- Telegram bot @your_bot: in-group monitoring + ops management. Every command is gated on a bound+approved operator account (/bind self-service -> confirm in console, /whoami, /callme custom display name); /netadmin decommissions/restores/meshes/deletes from chat (high-risk actions need a typed confirm word like DELETE <id> / APPLY MESH <id>); /errors reviews failed ops actions with one-tap retry; AI features /ask /summary /chat, @mention or reply triggers, AI failure diagnosis.
- Monitoring: node up/down uses Gatus (native uptime tracker) on pop-03.
- Conventions: avoid over-alerting; ctrl-01 is never decommissioned/deleted; always give concrete, actionable steps.`

// acme-ops persona shared by every AI feature (persona + project brief).
const aiSystemPrompt = "You are the ops assistant for Acme Net (NCN, AS64500). " +
	"Respond in English, concise, technically accurate, and actionable. Give concrete steps for commands. " +
	"Don't invent data you don't have; if unsure, say what you'd need.\n\n" + aiProjectBrief

type deepseekClient struct {
	key   string
	model string
	http  *http.Client
}

type aiMsg struct {
	Role    string `json:"role"` // system | user | assistant | tool
	Content string `json:"content"`
	// Tool-calling (OpenAI-compatible). Assistant turns may carry ToolCalls;
	// a "tool" role message answers ONE call via ToolCallID (+ Name for clarity).
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// toolCall is one function call the model wants made. Arguments is a JSON
// string (the model's proposed args) — parsed by the executor, never trusted
// blindly for writes (the executor re-validates + re-authorizes).
type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // always "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// toolDef is a function the model is offered (JSON-schema parameters).
type toolDef struct {
	Type     string `json:"type"` // "function"
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

var globalAI *deepseekClient

func newDeepseekClient() *deepseekClient {
	return &deepseekClient{
		key:   strings.TrimSpace(os.Getenv("NCN_DEEPSEEK_API_KEY")),
		model: getenvDefault("NCN_DEEPSEEK_MODEL", "deepseek-chat"),
		http:  &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *deepseekClient) enabled() bool { return c != nil && c.key != "" }

// pickModel resolves the per-call model override, falling back to the client's
// configured default when empty.
func (c *deepseekClient) pickModel(model string) string {
	if strings.TrimSpace(model) != "" {
		return model
	}
	return c.model
}

// listModels returns the model ids DeepSeek currently offers (OpenAI-compatible
// GET /models). Best-effort; used to populate the model-picker.
func (c *deepseekClient) listModels(ctx context.Context) ([]string, error) {
	if !c.enabled() {
		return nil, fmt.Errorf("DeepSeek not configured")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.deepseek.com/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// complete runs one non-streaming chat completion. system is prepended (skip if
// empty). Returns the assistant text or an error (network / API / disabled).
func (c *deepseekClient) complete(ctx context.Context, model, system string, msgs []aiMsg, temperature float64, maxTokens int) (string, error) {
	if !c.enabled() {
		return "", fmt.Errorf("DeepSeek not configured (server missing NCN_DEEPSEEK_API_KEY)")
	}
	all := make([]aiMsg, 0, len(msgs)+1)
	if system != "" {
		all = append(all, aiMsg{Role: "system", Content: system})
	}
	all = append(all, msgs...)
	body, err := json.Marshal(map[string]any{
		"model":       c.pickModel(model),
		"messages":    all,
		"stream":      false,
		"temperature": temperature,
		"max_tokens":  maxTokens,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", deepseekEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage aiUsageResp `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode deepseek response: %w", err)
	}
	if resp.StatusCode >= 300 {
		if out.Error != nil {
			return "", fmt.Errorf("deepseek %d: %s", resp.StatusCode, out.Error.Message)
		}
		return "", fmt.Errorf("deepseek http %d", resp.StatusCode)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("deepseek: empty response")
	}
	globalAIUsage.record(c.pickModel(model), out.Usage.PromptTokens, out.Usage.CompletionTokens)
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

// aiUsageResp is the OpenAI-compatible usage block DeepSeek returns.
type aiUsageResp struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// completeTools runs one chat completion offering the given tools, returning the
// assistant message (which may carry tool_calls instead of / alongside text).
// The caller drives the agent loop: execute tool_calls, append their results as
// role:"tool" messages, and call again. temperature is low for determinism.
func (c *deepseekClient) completeTools(ctx context.Context, model, system string, msgs []aiMsg, tools []toolDef) (aiMsg, error) {
	if !c.enabled() {
		return aiMsg{}, fmt.Errorf("DeepSeek not configured (server missing NCN_DEEPSEEK_API_KEY)")
	}
	all := make([]aiMsg, 0, len(msgs)+1)
	if system != "" {
		all = append(all, aiMsg{Role: "system", Content: system})
	}
	all = append(all, msgs...)
	payload := map[string]any{
		"model":       c.pickModel(model),
		"messages":    all,
		"stream":      false,
		"temperature": 0.2,
		"max_tokens":  1500,
	}
	if len(tools) > 0 {
		payload["tools"] = tools
		payload["tool_choice"] = "auto"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return aiMsg{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", deepseekEndpoint, bytes.NewReader(body))
	if err != nil {
		return aiMsg{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return aiMsg{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Choices []struct {
			Message aiMsg `json:"message"`
		} `json:"choices"`
		Usage aiUsageResp `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return aiMsg{}, fmt.Errorf("decode deepseek response: %w", err)
	}
	if resp.StatusCode >= 300 {
		if out.Error != nil {
			return aiMsg{}, fmt.Errorf("deepseek %d: %s", resp.StatusCode, out.Error.Message)
		}
		return aiMsg{}, fmt.Errorf("deepseek http %d", resp.StatusCode)
	}
	if len(out.Choices) == 0 {
		return aiMsg{}, fmt.Errorf("deepseek: empty response")
	}
	globalAIUsage.record(c.pickModel(model), out.Usage.PromptTokens, out.Usage.CompletionTokens)
	m := out.Choices[0].Message
	m.Role = "assistant" // normalize
	return m, nil
}

// ── streaming (SSE) — for the bot agent's live output ────────────────────────

// streamState assembles a streamed completion. Text deltas accumulate into
// content (and are pushed to onText live); tool_call deltas accumulate by index
// (id/name arrive once, arguments stream in fragments). Pure + unit-testable.
type streamState struct {
	content strings.Builder
	calls   []toolCall // indexed by the delta's tool_call index
}

// applyChunk applies one decoded SSE `data:` JSON object.
func (s *streamState) applyChunk(raw []byte, onText func(string)) {
	var c struct {
		Choices []struct {
			Delta struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &c) != nil || len(c.Choices) == 0 {
		return
	}
	d := c.Choices[0].Delta
	if d.Content != "" {
		s.content.WriteString(d.Content)
		if onText != nil {
			onText(d.Content)
		}
	}
	for _, tc := range d.ToolCalls {
		for len(s.calls) <= tc.Index {
			s.calls = append(s.calls, toolCall{Type: "function"})
		}
		cur := &s.calls[tc.Index]
		if tc.ID != "" {
			cur.ID = tc.ID
		}
		if tc.Type != "" {
			cur.Type = tc.Type
		}
		if tc.Function.Name != "" {
			cur.Function.Name = tc.Function.Name
		}
		cur.Function.Arguments += tc.Function.Arguments
	}
}

// result returns the assembled assistant message (dropping any empty tool slots).
func (s *streamState) result() aiMsg {
	m := aiMsg{Role: "assistant", Content: s.content.String()}
	for _, tc := range s.calls {
		if tc.Function.Name != "" {
			m.ToolCalls = append(m.ToolCalls, tc)
		}
	}
	return m
}

// completeToolsStream is the streaming twin of completeTools: it streams the
// model's text deltas to onText as they arrive and returns the fully-assembled
// assistant message (content + tool_calls). The agent loop uses this so the bot
// can edit a live message while the answer is generated.
func (c *deepseekClient) completeToolsStream(ctx context.Context, model, system string, msgs []aiMsg, tools []toolDef, onText func(string)) (aiMsg, error) {
	if !c.enabled() {
		return aiMsg{}, fmt.Errorf("DeepSeek not configured (server missing NCN_DEEPSEEK_API_KEY)")
	}
	all := make([]aiMsg, 0, len(msgs)+1)
	if system != "" {
		all = append(all, aiMsg{Role: "system", Content: system})
	}
	all = append(all, msgs...)
	payload := map[string]any{
		"model": c.pickModel(model), "messages": all, "stream": true,
		"temperature": 0.2, "max_tokens": 1500,
	}
	if len(tools) > 0 {
		payload["tools"] = tools
		payload["tool_choice"] = "auto"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return aiMsg{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", deepseekEndpoint, bytes.NewReader(body))
	if err != nil {
		return aiMsg{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return aiMsg{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return aiMsg{}, fmt.Errorf("deepseek stream %d: %s", resp.StatusCode, string(raw))
	}
	st := &streamState{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "" || data == "[DONE]" {
			continue
		}
		st.applyChunk([]byte(data), onText)
	}
	if err := sc.Err(); err != nil && st.content.Len() == 0 && len(st.calls) == 0 {
		return aiMsg{}, err
	}
	return st.result(), nil
}
