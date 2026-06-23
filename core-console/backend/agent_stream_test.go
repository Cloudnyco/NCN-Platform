// Pure test for the streaming SSE accumulator (no network). Verifies text
// deltas concatenate + push live, and a tool_call streamed across chunks
// reassembles (id/name once, arguments in fragments).

package main

import (
	"strings"
	"testing"
)

func TestStreamStateText(t *testing.T) {
	s := &streamState{}
	var live strings.Builder
	for _, d := range []string{
		`{"choices":[{"delta":{"content":"Hel"}}]}`,
		`{"choices":[{"delta":{"content":"lo, "}}]}`,
		`{"choices":[{"delta":{"content":"fleet"}}]}`,
	} {
		s.applyChunk([]byte(d), func(x string) { live.WriteString(x) })
	}
	r := s.result()
	if r.Content != "Hello, fleet" {
		t.Fatalf("content = %q", r.Content)
	}
	if live.String() != "Hello, fleet" {
		t.Fatalf("live deltas = %q", live.String())
	}
	if len(r.ToolCalls) != 0 {
		t.Fatalf("unexpected tool calls")
	}
}

func TestStreamStateToolCall(t *testing.T) {
	s := &streamState{}
	for _, c := range []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"node_detail","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"id\":\""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"pop-05\"}"}}]}}]}`,
	} {
		s.applyChunk([]byte(c), nil)
	}
	r := s.result()
	if len(r.ToolCalls) != 1 {
		t.Fatalf("want 1 tool call, got %d", len(r.ToolCalls))
	}
	tc := r.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "node_detail" || tc.Function.Arguments != `{"id":"pop-05"}` {
		t.Fatalf("assembled tool call wrong: id=%q name=%q args=%q", tc.ID, tc.Function.Name, tc.Function.Arguments)
	}
}
