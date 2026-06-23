// DDoS mitigation — node sync + confirm-gated HTTP handlers.
//
// syncNode regenerates a PoP's `inet ncn_ddos` table from the active rules that
// target it and applies it in one atomic `nft -f`. Apply is confirm-gated
// ("APPLY DDOS <id>"), audited, and records an op-failure on error. Revoke is
// restorative (no confirm). Nothing here ever runs without an operator action;
// the only automatic caller is the TTL expiry loop (ddos.go), which only LIFTS
// drops.

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const ddosMaxTTL = 7 * 24 * 3600 // 7 days

// syncNode pushes the node's current ncn_ddos table (atomic nft -f). Returns
// (applied-ok, error). Fails cleanly if nft isn't installed on that node.
func (s *ddosStore) syncNode(ctx context.Context, node string, onLine func(string)) (bool, error) {
	rec, ok := s.fleet.registry.get(node)
	if !ok {
		return false, fmt.Errorf("unknown node %q", node)
	}
	s.mu.Lock()
	nftFile := s.genNftFile(node, time.Now().Unix())
	s.mu.Unlock()
	b64 := base64.StdEncoding.EncodeToString([]byte(nftFile))
	script := `set -uo pipefail
command -v nft >/dev/null 2>&1 || { echo "RESULT FAIL nft-not-installed"; exit 1; }
echo '` + b64 + `' | base64 -d > /tmp/ncn_ddos.nft
if nft -f /tmp/ncn_ddos.nft; then echo "RESULT OK"; else echo "RESULT FAIL nft-load"; nft -c -f /tmp/ncn_ddos.nft 2>&1 | head -5; exit 1; fi`
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	okFlag := false
	exit, err := s.fleet.runMeshScriptOnNode(cctx, rec, script, func(l string) {
		if strings.Contains(l, "RESULT OK") {
			okFlag = true
		}
		onLine(l)
	})
	if err != nil {
		return false, err
	}
	return exit == 0 && okFlag, nil
}

func ddosConfirm(id string) string { return "APPLY DDOS " + id }

// GET /api/v1/auth/ddos → all rules (newest first).
func handleDDoSList(w http.ResponseWriter, _ *http.Request) {
	if globalDDoS == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "ddos store not ready"})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"rules": globalDDoS.snapshot()}})
}

// POST /api/v1/auth/ddos/create → store a DRAFT rule + return its nft preview.
// Does NOT touch any router.
func handleDDoSCreate(w http.ResponseWriter, r *http.Request) {
	if globalDDoS == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "ddos store not ready"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var rule flowspecRule
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&rule); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad body"})
		return
	}
	rule.Src = strings.TrimSpace(rule.Src)
	rule.Dst = strings.TrimSpace(rule.Dst)
	// Refuse a match-everything rule — must constrain by at least one field.
	if rule.Src == "" && rule.Dst == "" && rule.Proto == "" && rule.DstPort == 0 && rule.SrcPort == 0 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "rule too broad — set at least dst/src/proto/port"})
		return
	}
	if rule.Action != "rate" {
		rule.Action = "drop"
	}
	if rule.Action == "rate" && rule.RatePps <= 0 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "rate action needs rate_pps > 0"})
		return
	}
	if rule.TTLSecs <= 0 || rule.TTLSecs > ddosMaxTTL {
		rule.TTLSecs = 3600
	}
	rule.ID = ddosNewID()
	rule.Status = "draft"
	rule.CreatedBy = adminOperator(r)
	rule.CreatedAt = time.Now().Unix()
	rule.AppliedPops = nil
	globalDDoS.mu.Lock()
	globalDDoS.rules[rule.ID] = &rule
	globalDDoS.persistLocked()
	globalDDoS.mu.Unlock()
	auditRecord(r, AuditEvent{Event: "ddos.create", Severity: "info", Actor: rule.CreatedBy, Target: rule.ID, Outcome: "ok", Details: map[string]any{"rule": rule.summary()}})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"rule": rule, "nft": rule.nftLine()}})
}

// POST /api/v1/auth/ddos/apply {"id","confirm":"APPLY DDOS <id>","nodes":[...]}
// → activate the rule on the chosen PoPs (atomic nft -f each). Confirm-gated.
func handleDDoSApply(w http.ResponseWriter, r *http.Request) {
	if globalDDoS == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "ddos store not ready"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var body struct {
		ID      string   `json:"id"`
		Confirm string   `json:"confirm"`
		Nodes   []string `json:"nodes"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || body.ID == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad body"})
		return
	}
	if len(body.Nodes) == 0 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "no target nodes"})
		return
	}
	if strings.TrimSpace(body.Confirm) != ddosConfirm(body.ID) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "confirm mismatch — type: " + ddosConfirm(body.ID)})
		return
	}
	globalDDoS.mu.Lock()
	rule := globalDDoS.rules[body.ID]
	if rule == nil {
		globalDDoS.mu.Unlock()
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "no such rule"})
		return
	}
	rule.Status = "active"
	rule.AppliedPops = body.Nodes
	rule.ExpiresAt = time.Now().Unix() + int64(rule.TTLSecs)
	globalDDoS.persistLocked()
	globalDDoS.mu.Unlock()

	actor := adminOperator(r)
	logs := map[string]string{}
	applied := []string{}
	failed := map[string]string{}
	for _, node := range body.Nodes {
		var b strings.Builder
		ok, err := globalDDoS.syncNode(r.Context(), node, func(l string) { b.WriteString(l); b.WriteString("\n") })
		logs[node] = b.String()
		if err != nil || !ok {
			reason := "nft apply failed"
			if err != nil {
				reason = err.Error()
			}
			failed[node] = reason
		} else {
			applied = append(applied, node)
		}
	}
	sev := "warn"
	outcome := "ok"
	if len(failed) > 0 {
		outcome = "fail"
		recordOpFailure(globalNotify, &opFailure{Kind: "ddos-apply", Target: body.ID, Actor: actor, Reason: fmt.Sprintf("%v", failed)})
	}
	auditRecord(r, AuditEvent{Event: "ddos.apply", Severity: sev, Actor: actor, Target: body.ID, Outcome: outcome, Details: map[string]any{"applied": applied, "failed": failed}})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"applied": applied, "failed": failed, "logs": logs}})
}

// POST /api/v1/auth/ddos/revoke {"id"} → deactivate + lift the rule from its
// PoPs. Restorative, so no confirm string.
func handleDDoSRevoke(w http.ResponseWriter, r *http.Request) {
	if globalDDoS == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "ddos store not ready"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || body.ID == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad body"})
		return
	}
	globalDDoS.mu.Lock()
	rule := globalDDoS.rules[body.ID]
	if rule == nil {
		globalDDoS.mu.Unlock()
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "no such rule"})
		return
	}
	pops := append([]string{}, rule.AppliedPops...)
	rule.Status = "revoked"
	globalDDoS.persistLocked()
	globalDDoS.mu.Unlock()

	for _, node := range pops {
		_, _ = globalDDoS.syncNode(r.Context(), node, func(string) {})
	}
	auditRecord(r, AuditEvent{Event: "ddos.revoke", Severity: "info", Actor: adminOperator(r), Target: body.ID, Outcome: "ok"})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"revoked": body.ID, "nodes": pops}})
}
