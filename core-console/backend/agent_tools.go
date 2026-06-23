// agent_tools.go — the tool registry + executors for the DeepSeek ops agent.
//
// The agent (agent.go) lets DeepSeek DRIVE the fleet through a tool-calling
// loop, but the safety model is strict and lives here:
//   * READ-ONLY tools (list_nodes, fleet_status, node_detail, active_alerts,
//     op_failures) run automatically inside the loop.
//   * WRITE / COMMAND tools (decommission, recommission, delete_node,
//     mesh_apply, run_command) are NEVER executed by the model. The loop pauses
//     and a human approves the exact call first (see agent.go). They are only
//     OFFERED to the model when the caller is an admin.
//   * Every executed write is audited as ai.tool.exec with actor "ai:<op>".
//   * ctrl-01 keeps its decommission/delete guards (the fleet methods enforce).
// The model's proposed args are re-parsed + re-validated here at execution — we
// never trust the arguments string blindly.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// agentTool is one capability offered to the model.
type agentTool struct {
	name   string
	write  bool // true → requires human approval, admin-only
	def    toolDef
	// exec runs the tool. args = the model's parsed arguments. actor = the
	// operator on whose behalf it runs (for audit). Returns text fed back to
	// the model. Read-only tools ignore actor.
	exec func(ctx context.Context, args map[string]any, actor string) (string, error)
}

const (
	agentCmdTimeout = 8 * time.Second
	agentCmdMaxLines = 200
	agentCmdMaxBytes = 8192
)

// mkToolDef builds the OpenAI-style function schema.
func mkToolDef(name, desc string, props map[string]any, required []string) toolDef {
	var d toolDef
	d.Type = "function"
	d.Function.Name = name
	d.Function.Description = desc
	params := map[string]any{
		"type":       "object",
		"properties": props,
	}
	// Only emit "required" when non-empty: a nil slice marshals to JSON null,
	// which DeepSeek's schema validator rejects ("null is not of type array").
	if len(required) > 0 {
		params["required"] = required
	}
	d.Function.Parameters = params
	return d
}

func argStr(args map[string]any, k string) string {
	if v, ok := args[k]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// agentToolRegistry builds the tool set bound to the live fleet + engine
// (aiCtxFleet / aiCtxEngine, set in main.go). Stateless — rebuilt per request.
func agentToolRegistry() map[string]*agentTool {
	f := aiCtxFleet
	e := aiCtxEngine
	m := map[string]*agentTool{}
	add := func(t *agentTool) { m[t.name] = t }

	// ── read-only ────────────────────────────────────────────────────────────
	add(&agentTool{
		name: "list_nodes", write: false,
		def: mkToolDef("list_nodes", "List all registered PoP nodes (id, label, region, status).", map[string]any{}, nil),
		exec: func(_ context.Context, _ map[string]any, _ string) (string, error) {
			if f == nil {
				return "", fmt.Errorf("fleet not ready")
			}
			recs := f.registry.listSnapshot()
			sort.Slice(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })
			var b strings.Builder
			for _, r := range recs {
				fmt.Fprintf(&b, "%s\t%s\tregion=%d\t%s\n", r.ID, r.Label, r.Region, r.Status)
			}
			return b.String(), nil
		},
	})
	add(&agentTool{
		name: "fleet_status", write: false,
		def: mkToolDef("fleet_status", "Live status of every node: up/down, cpu/mem/disk %, bird version, cert days, BGP peers.", map[string]any{}, nil),
		exec: func(_ context.Context, _ map[string]any, _ string) (string, error) {
			return aiFleetContext(f, e), nil // reuses the compact, secret-free snapshot
		},
	})
	add(&agentTool{
		name: "node_detail", write: false,
		def: mkToolDef("node_detail", "Full live detail for one node by id.",
			map[string]any{"id": map[string]any{"type": "string", "description": "node id, e.g. pop-05"}}, []string{"id"}),
		exec: func(_ context.Context, args map[string]any, _ string) (string, error) {
			if f == nil {
				return "", fmt.Errorf("fleet not ready")
			}
			id := argStr(args, "id")
			rec, ok := f.registry.get(id)
			if !ok {
				return "", fmt.Errorf("unknown node %q", id)
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%s (%s) addr=%s region=%d status=%s\n", rec.ID, rec.Label, rec.Address, rec.Region, rec.Status)
			for _, s := range f.snapshotNodes() {
				if s == nil || s.Node.ID != id {
					continue
				}
				st := "up"
				if !s.OK {
					st = fmt.Sprintf("DOWN(×%d)", s.ConsecFail)
				}
				fmt.Fprintf(&b, "live: %s cpu=%s mem=%s disk=%s load=%.2f bird=%s cert=%dd\nbgp: %s\n",
					st, fmtPct(s.CPUPct), fmtPct(s.MemPct), fmtPct(s.DiskPct), s.Load1, orDash(s.BirdVer), s.AgentCertDaysLeft, bgpPeerSummary(s.Protocols))
			}
			return b.String(), nil
		},
	})
	add(&agentTool{
		name: "active_alerts", write: false,
		def: mkToolDef("active_alerts", "All currently-firing alerts (severity, rule, node, message).", map[string]any{}, nil),
		exec: func(_ context.Context, _ map[string]any, _ string) (string, error) {
			if e == nil {
				return "", fmt.Errorf("engine not ready")
			}
			al := e.activeSnapshot("")
			if len(al) == 0 {
				return "no active alerts", nil
			}
			var b strings.Builder
			for _, a := range al {
				fmt.Fprintf(&b, "[%s] %s@%s: %s\n", a.Severity, a.RuleID, a.NodeID, orDash(a.Message))
			}
			return b.String(), nil
		},
	})
	// memory tools — per-operator, low-risk (just notes), so they auto-run
	// (write:false) without an approval gate. actor scopes which operator's
	// memory is touched.
	add(&agentTool{
		name: "remember", write: false,
		def: mkToolDef("remember", "Save a durable fact about this operator / the fleet to your memory (persists across conversations). Use for preferences, recurring context, things worth not re-asking.",
			map[string]any{"text": map[string]any{"type": "string", "description": "the fact to remember"}}, []string{"text"}),
		exec: func(_ context.Context, args map[string]any, actor string) (string, error) {
			it, err := globalAIUsers.addMemory(actor, argStr(args, "text"))
			if err != nil {
				return "", err
			}
			if it == nil {
				return "nothing to remember (empty)", nil
			}
			return "remembered: " + it.Text, nil
		},
	})
	add(&agentTool{
		name: "forget", write: false,
		def: mkToolDef("forget", "Remove saved memory entries matching an id or a substring of their text.",
			map[string]any{"query": map[string]any{"type": "string", "description": "memory id or text substring to forget"}}, []string{"query"}),
		exec: func(_ context.Context, args map[string]any, actor string) (string, error) {
			n := globalAIUsers.deleteMemory(actor, argStr(args, "query"))
			return "forgot " + itoa(n) + " memory entr(y/ies)", nil
		},
	})
	add(&agentTool{
		name: "op_failures", write: false,
		def: mkToolDef("op_failures", "Open operational-action failures (kind, node, reason).", map[string]any{}, nil),
		exec: func(_ context.Context, _ map[string]any, _ string) (string, error) {
			list := globalOpFailures.listSnapshot(true)
			if len(list) == 0 {
				return "no open op-failures", nil
			}
			var b strings.Builder
			for _, x := range list {
				fmt.Fprintf(&b, "%s %s: %s\n", opKindLabel(x.Kind), x.Target, x.Reason)
			}
			return b.String(), nil
		},
	})

	// ── write / command (human-approved, admin-only) ──────────────────────────
	nodeArg := func(extra map[string]any, req []string) (map[string]any, []string) {
		p := map[string]any{"id": map[string]any{"type": "string", "description": "node id"}}
		for k, v := range extra {
			p[k] = v
		}
		return p, append([]string{"id"}, req...)
	}
	statusTool := func(name, desc string, run func(id, actor string) error) *agentTool {
		props, req := nodeArg(nil, nil)
		return &agentTool{
			name: name, write: true, def: mkToolDef(name, desc, props, req),
			exec: func(_ context.Context, args map[string]any, actor string) (string, error) {
				id := argStr(args, "id")
				if err := run(id, actor); err != nil {
					return "", err
				}
				return name + " applied to " + id, nil
			},
		}
	}
	add(statusTool("decommission", "Decommission (deactivate) a node — reversible.", func(id, actor string) error {
		_, err := f.decommission(id, "ai:"+actor)
		return err
	}))
	add(statusTool("recommission", "Recommission (re-activate) a decommissioned node.", func(id, actor string) error {
		_, err := f.recommission(id, "ai:"+actor)
		return err
	}))
	add(statusTool("delete_node", "PERMANENTLY delete a node from the registry — irreversible.", func(id, actor string) error {
		return f.deleteNode(id, "ai:"+actor)
	}))
	add(&agentTool{
		name: "mesh_apply", write: true,
		def: mkToolDef("mesh_apply", "Weave a node into the mesh with ALL active peers (GRE). Changes live BGP routing.",
			map[string]any{"id": map[string]any{"type": "string", "description": "node id to weave"}}, []string{"id"}),
		exec: func(_ context.Context, args map[string]any, actor string) (string, error) {
			id := argStr(args, "id")
			targets := []string{id}
			for _, r := range f.registry.listSnapshot() {
				if r.ID != id && r.active() {
					targets = append(targets, r.ID)
				}
			}
			if _, err := f.beginMeshApply(id, targets, nil, 0, "ai:"+actor); err != nil {
				return "", err
			}
			return "mesh apply started for " + id + " (all active peers, GRE) — result follows via alerts", nil
		},
	})
	add(&agentTool{
		name: "run_command", write: true,
		def: mkToolDef("run_command", "Run a shell command on a PoP via SSH (or locally on ctrl-01). Output is bounded; 8s timeout. Use for diagnosis or fixes; a human approves the exact command first.",
			map[string]any{
				"node_id": map[string]any{"type": "string", "description": "node id to run on"},
				"command": map[string]any{"type": "string", "description": "the exact shell command"},
				"reason":  map[string]any{"type": "string", "description": "why this command is needed"},
			}, []string{"node_id", "command"}),
		exec: func(ctx context.Context, args map[string]any, actor string) (string, error) {
			if f == nil {
				return "", fmt.Errorf("fleet not ready")
			}
			id := argStr(args, "node_id")
			command := argStr(args, "command")
			if command == "" {
				return "", fmt.Errorf("empty command")
			}
			rec, ok := f.registry.get(id)
			if !ok {
				return "", fmt.Errorf("unknown node %q", id)
			}
			cctx, cancel := context.WithTimeout(ctx, agentCmdTimeout)
			defer cancel()
			var lines []string
			var bytes int
			truncated := false
			exit, runErr := f.runMeshScriptOnNode(cctx, rec, command, func(l string) {
				if truncated {
					return
				}
				if len(lines) >= agentCmdMaxLines || bytes+len(l) > agentCmdMaxBytes {
					truncated = true
					lines = append(lines, "…(output truncated)")
					return
				}
				bytes += len(l)
				lines = append(lines, l)
			})
			out := strings.Join(lines, "\n")
			res := fmt.Sprintf("exit=%d\n%s", exit, out)
			if runErr != nil && exit == 0 {
				res = fmt.Sprintf("error: %v\n%s", runErr, out)
			}
			return res, nil // command failures are data for the model, not a tool error
		},
	})

	return m
}

// agentToolDefs returns the tool schemas to offer the model. Write tools are
// offered only when allowWrites (admin on console/MCP; everyone on the bot,
// where a write is routed to an admin for approval).
func agentToolDefs(allowWrites bool) []toolDef {
	reg := agentToolRegistry()
	names := make([]string, 0, len(reg))
	for n := range reg {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]toolDef, 0, len(names))
	for _, n := range names {
		t := reg[n]
		if t.write && !allowWrites {
			continue
		}
		out = append(out, t.def)
	}
	return out
}

// parseToolArgs parses a tool_call's JSON arguments string into a map.
func parseToolArgs(arguments string) map[string]any {
	m := map[string]any{}
	if strings.TrimSpace(arguments) == "" {
		return m
	}
	_ = json.Unmarshal([]byte(arguments), &m)
	return m
}
