// Pure/in-memory tests for the agent loop's decision logic. No DeepSeek call,
// no real fleet mutation, no real command — every assertion exercises a branch
// that returns BEFORE any network/LLM call or tool execution.

package main

import (
	"context"
	"testing"
)

func mkToolCall(id, name, args string) toolCall {
	tc := toolCall{ID: id, Type: "function"}
	tc.Function.Name = name
	tc.Function.Arguments = args
	return tc
}

func TestFirstUnansweredToolCall(t *testing.T) {
	// assistant proposes two calls; first is answered, second isn't.
	msgs := []aiMsg{
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []toolCall{mkToolCall("a", "list_nodes", "{}"), mkToolCall("b", "fleet_status", "{}")}},
		{Role: "tool", ToolCallID: "a", Name: "list_nodes", Content: "..."},
	}
	tc, ok := firstUnansweredToolCall(msgs)
	if !ok || tc.ID != "b" {
		t.Fatalf("want unanswered=b, got ok=%v id=%q", ok, tc.ID)
	}
	// answer b too → none unanswered.
	msgs = append(msgs, aiMsg{Role: "tool", ToolCallID: "b", Name: "fleet_status", Content: "..."})
	if _, ok := firstUnansweredToolCall(msgs); ok {
		t.Fatalf("expected all answered")
	}
	// plain assistant text → none.
	if _, ok := firstUnansweredToolCall([]aiMsg{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}); ok {
		t.Fatalf("plain text should have no tool calls")
	}
}

func TestToolDefsAdminGate(t *testing.T) {
	has := func(defs []toolDef, name string) bool {
		for _, d := range defs {
			if d.Function.Name == name {
				return true
			}
		}
		return false
	}
	op := agentToolDefs(false)
	if !has(op, "list_nodes") || !has(op, "fleet_status") {
		t.Fatalf("operator must get read-only tools")
	}
	for _, w := range []string{"decommission", "delete_node", "mesh_apply", "run_command"} {
		if has(op, w) {
			t.Fatalf("non-admin must NOT be offered write tool %q", w)
		}
	}
	adm := agentToolDefs(true)
	for _, w := range []string{"decommission", "recommission", "delete_node", "mesh_apply", "run_command"} {
		if !has(adm, w) {
			t.Fatalf("admin must be offered write tool %q", w)
		}
	}
}

func TestToolReadOnlyClassification(t *testing.T) {
	reg := agentToolRegistry()
	want := map[string]bool{ // true = write
		"list_nodes": false, "fleet_status": false, "node_detail": false,
		"active_alerts": false, "op_failures": false,
		"decommission": true, "recommission": true, "delete_node": true,
		"mesh_apply": true, "run_command": true,
	}
	for name, write := range want {
		tl := reg[name]
		if tl == nil {
			t.Fatalf("missing tool %q", name)
		}
		if tl.write != write {
			t.Fatalf("tool %q write=%v want %v", name, tl.write, write)
		}
	}
}

func TestSummarizePending(t *testing.T) {
	rc := summarizePending(mkToolCall("c", "run_command", `{"node_id":"pop-05","command":"birdc show proto"}`))
	if rc != "run on pop-05: birdc show proto" {
		t.Fatalf("run_command summary = %q", rc)
	}
	d := summarizePending(mkToolCall("c", "decommission", `{"id":"pop-07"}`))
	if d != "decommission pop-07" {
		t.Fatalf("decommission summary = %q", d)
	}
}

// A write tool_call must PAUSE (return pending) without executing — no tool
// result appended, no fleet touched. globalAI is faked-enabled but never called
// (the pending path returns before any completeTools).
func TestAgentAdvanceWritePauses(t *testing.T) {
	saved := globalAI
	globalAI = &deepseekClient{key: "test-fake", model: "x"}
	defer func() { globalAI = saved }()

	msgs := []aiMsg{
		{Role: "user", Content: "decommission pop-07"},
		{Role: "assistant", ToolCalls: []toolCall{mkToolCall("w1", "decommission", `{"id":"pop-07"}`)}},
	}
	res, err := agentAdvance(context.Background(), msgs, true, "alice", nil, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Pending == nil || res.Pending.Name != "decommission" || res.Pending.ToolCallID != "w1" {
		t.Fatalf("want pending decommission, got %+v", res.Pending)
	}
	for _, m := range res.Messages {
		if m.Role == "tool" {
			t.Fatalf("write must NOT have executed (found a tool result)")
		}
	}
}

func TestAgentApproveGuards(t *testing.T) {
	saved := globalAI
	globalAI = &deepseekClient{key: "test-fake", model: "x"}
	defer func() { globalAI = saved }()

	base := []aiMsg{
		{Role: "user", Content: "x"},
		{Role: "assistant", ToolCalls: []toolCall{mkToolCall("w1", "delete_node", `{"id":"pop-07"}`)}},
	}
	// unknown tool_call id
	if _, err := agentExecuteApproved(context.Background(), base, "nope", "approve", true, "alice", nil, nil); err == nil {
		t.Fatalf("expected error for unknown tool_call id")
	}
	// non-admin cannot approve a write
	if _, err := agentExecuteApproved(context.Background(), base, "w1", "approve", false, "bob", nil, nil); err == nil {
		t.Fatalf("expected error: non-admin approving a write")
	}
	// a read-only tool_call is not approvable
	readMsgs := []aiMsg{
		{Role: "user", Content: "x"},
		{Role: "assistant", ToolCalls: []toolCall{mkToolCall("r1", "list_nodes", "{}")}},
	}
	if _, err := agentExecuteApproved(context.Background(), readMsgs, "r1", "approve", true, "alice", nil, nil); err == nil {
		t.Fatalf("expected error: read-only tool is not approvable")
	}
}
