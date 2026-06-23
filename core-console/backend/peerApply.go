// peerApply.go — HTTP surface for peering automation: generate a per-peer BIRD
// config from an approved application (irr.go + peergen.go), and apply it to a
// node behind an explicit confirm word. Apply is generate-then-human-approve:
// it writes a per-peer include file, runs `birdc configure soft`, and rolls
// back on parse failure. It NEVER applies automatically.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func peerApplyConfirm(asn uint32) string { return fmt.Sprintf("APPLY PEER AS%d", asn) }

// irrNodeID resolves which node runs bgpq4: NCN_IRR_NODE, else the control node.
func (s *peeringStore) irrNodeID() string {
	if n := strings.TrimSpace(os.Getenv("NCN_IRR_NODE")); n != "" {
		return n
	}
	if s.fleet != nil {
		return s.fleet.localID
	}
	return "ctrl-01"
}

func (s *peeringStore) getByID(id string) (PeeringApplication, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.apps {
		if a.ID == id {
			return a, true
		}
	}
	return PeeringApplication{}, false
}

// POST /api/v1/auth/peering/peer-config — generate (or regenerate) a peer's
// BIRD config from its approved application. Admin only. Does NOT touch routers.
func (s *peeringStore) handlePeerConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var req struct {
		AppID      string `json:"app_id"`
		NeighborV6 string `json:"neighbor_v6"`
		TargetNode string `json:"target_node"`
		MaxPrefix6 int    `json:"max_prefix6"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	app, ok := s.getByID(strings.TrimSpace(req.AppID))
	if !ok {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "application not found"})
		return
	}
	if app.Status != "approved" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "application is not approved (status=" + app.Status + ")"})
		return
	}
	if s.fleet == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "fleet unavailable"})
		return
	}
	if req.MaxPrefix6 > 0 {
		app.MaxPrefix6 = req.MaxPrefix6
	}
	asSet := strings.TrimSpace(app.ASSet)
	if asSet == "" {
		asSet = fmt.Sprintf("AS%d", app.ASN) // no AS-SET registered → just the ASN's routes
	}
	name := fmt.Sprintf("PEER_AS%d_PFX", app.ASN)
	node := s.irrNodeID()

	ctx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
	defer cancel()
	irr, err := expandASSet(ctx, s.fleet, node, name, asSet, app.IRRSource)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, envelope{OK: false, Error: "IRR expand failed: " + err.Error()})
		return
	}
	gen := genPeerConfig(app, irr, req.NeighborV6, true, adminOperator(r))
	globalPeerGen.put(gen)
	auditRecord(r, AuditEvent{Event: "peering.peer-config", Severity: auditSevInfo, Actor: adminOperator(r),
		Target: fmt.Sprintf("AS%d", app.ASN), Details: map[string]any{"prefixes": irr.PrefixCount, "as_set": asSet}})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: gen})
}

// POST /api/v1/auth/peering/peer-apply — apply a generated peer config to one
// or more nodes. Admin only. Requires confirm == "APPLY PEER AS<asn>".
func (s *peeringStore) handlePeerApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var req struct {
		ASN         uint32   `json:"asn"`
		Confirm     string   `json:"confirm"`
		TargetNodes []string `json:"target_nodes"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	if strings.TrimSpace(req.Confirm) != peerApplyConfirm(req.ASN) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "confirm word mismatch — type: " + peerApplyConfirm(req.ASN)})
		return
	}
	gen := globalPeerGen.get(req.ASN)
	if gen == nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "no generated config for this ASN — generate it first"})
		return
	}
	if gen.NeighborV6 == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "generated config has no neighbor v6 — regenerate with the session address"})
		return
	}
	if len(req.TargetNodes) == 0 || s.fleet == nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "no target nodes / fleet unavailable"})
		return
	}

	script := buildPeerApplyScript(req.ASN, gen.Config)
	var logb strings.Builder
	applied := []string{}
	failed := map[string]string{}
	for _, node := range req.TargetNodes {
		rec, ok := s.fleet.registry.get(node)
		if !ok {
			failed[node] = "not in registry"
			continue
		}
		fmt.Fprintf(&logb, "===== %s =====\n", node)
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		rc, err := s.fleet.runMeshScriptOnNode(ctx, rec, script, func(l string) { logb.WriteString(l + "\n") })
		cancel()
		if err != nil || rc != 0 {
			failed[node] = fmt.Sprintf("rc=%d err=%v", rc, err)
		} else {
			applied = append(applied, node)
		}
	}

	if len(applied) > 0 {
		gen.Status = "applied"
		gen.AppliedAt = time.Now().UTC()
		gen.AppliedNodes = applied
		globalPeerGen.put(gen)
	}
	sev := auditSevWarn
	auditRecord(r, AuditEvent{Event: "peering.peer-apply", Severity: sev, Actor: adminOperator(r),
		Target: fmt.Sprintf("AS%d", req.ASN), Details: map[string]any{"applied": applied, "failed": failed}})

	writeJSON(w, http.StatusOK, envelope{OK: len(failed) == 0, Data: map[string]any{
		"applied": applied, "failed": failed, "log": logb.String(),
	}})
}

// GET /api/v1/auth/peering/peer-gens — list all generated peer configs. Admin.
func (s *peeringStore) handlePeerGens(w http.ResponseWriter, r *http.Request) {
	if globalPeerGen == nil {
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: []*peerGeneration{}})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: globalPeerGen.list()})
}

// buildPeerApplyScript writes /etc/bird/peers/peer_as<asn>.conf (base64), makes
// sure bird.conf includes peers/*.conf, then `birdc configure soft` with
// rollback to the prior file (or removal) on a parse failure.
func buildPeerApplyScript(asn uint32, conf string) string {
	var b strings.Builder
	b.WriteString("set -uo pipefail\n")
	b.WriteString("TS=$(date +%Y%m%d-%H%M%S)\n")
	b.WriteString("PEERDIR=/etc/bird/peers\n")
	fmt.Fprintf(&b, "CONF=$PEERDIR/peer_as%d.conf\n", asn)
	b.WriteString("BIRDCONF=/etc/bird/bird.conf\n")
	b.WriteString("mkdir -p \"$PEERDIR\"\n")
	// Back up an existing peer file before overwriting (reversible).
	b.WriteString("BAK=\"\"; if [ -f \"$CONF\" ]; then BAK=\"$CONF.ncn-bak.$TS\"; cp -a \"$CONF\" \"$BAK\"; echo \"[backup] $BAK\"; fi\n")
	// Write the new peer config FIRST, so the include glob always matches ≥1 file.
	fmt.Fprintf(&b, "echo '%s' | base64 -d > \"$CONF\"; echo \"[write] $CONF\"\n", b64(conf))
	b.WriteString("grep -q 'peers/\\*.conf' \"$BIRDCONF\" || { printf '\\ninclude \"/etc/bird/peers/*.conf\";\\n' >> \"$BIRDCONF\"; echo '[bird] + include peers/*.conf'; }\n")
	b.WriteString("birdc configure soft > /tmp/ncn-peer-soft.log 2>&1\n")
	b.WriteString("if grep -qiE 'reconfigured|reconfiguration in progress' /tmp/ncn-peer-soft.log; then\n")
	b.WriteString("  echo '[ok] birdc configure soft → accepted'; sed 's/^/    /' /tmp/ncn-peer-soft.log\n")
	b.WriteString("else\n")
	b.WriteString("  echo '[FAIL] configure soft:'; sed 's/^/    /' /tmp/ncn-peer-soft.log\n")
	b.WriteString("  if [ -n \"$BAK\" ] && [ -f \"$BAK\" ]; then cp -a \"$BAK\" \"$CONF\"; echo \"[rollback] restored $BAK\"; else rm -f \"$CONF\"; echo '[rollback] removed new peer file'; fi\n")
	b.WriteString("  birdc configure soft >/dev/null 2>&1 || true\n")
	b.WriteString("  exit 1\n")
	b.WriteString("fi\n")
	return b.String()
}
