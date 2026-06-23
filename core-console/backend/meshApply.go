// Internal-mesh auto-apply.
//
// The mesh generator (mesh_config.go) is review-only. This adds the OPT-IN
// ability to actually wire selected nodes into the mesh — but strictly,
// additively, and reversibly:
//
//   * ADDITIVE ONLY — never overwrites an existing /etc/bird/bird.conf (which
//     may carry hand-tuned transit). It ensures the `include` line and appends
//     only the iBGP peer blocks not already present. A node with NO bird.conf
//     (a fresh box) gets the full generated config written once.
//   * Tunnels are brought up idempotently (`ip link show` guard) so an
//     existing, session-carrying link is never torn down / flapped.
//   * GRE only — WireGuard links are left to the manual snippets (two-ended
//     key exchange is out of scope for auto-apply).
//   * birdc configure soft (never restart); on a soft failure the backup is
//     restored and re-applied (rollback).
//
// Runs per target: locally when the target is the console node, else over SSH
// with the fleet key. Progress streams into the same step+log job shape as
// onboarding so the UI reuses one live-progress renderer.

package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const meshApplyTimeout = 90 * time.Second

func meshApplyConfirm(id string) string { return "APPLY MESH " + id }

// b64 encodes config text for safe transport into the remote shell (avoids any
// quoting/injection from multi-line bird config).
func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// buildApplyScript renders the additive, reversible apply script for one
// target. blocks = iBGP peer blocks to ensure present (keyed by protocol name);
// tunnels = idempotent GRE/anchor bring-up lines.
//
// Whether to APPEND vs WRITE the whole file is decided by the NCN signature
// `define MY_ASN`, not merely by the file's existence: a real PoP carries a
// hand-tuned NCN config (has the defines) → we append only the missing iBGP
// blocks, preserving its transit. A node with a vanilla distro-default
// bird.conf (no defines) — or no file at all — gets the full generated config
// written (after backup), because appending iBGP blocks that reference
// MY_ASN/MY_REGION/… onto a default config would not parse (the exit-1 case).
func buildApplyScript(blocks map[string]string, tunnels []string, fullBird, filters string) string {
	var b strings.Builder
	b.WriteString("set -uo pipefail\n")
	b.WriteString("TS=$(date +%Y%m%d-%H%M%S)\n")
	b.WriteString("BIRDCONF=/etc/bird/bird.conf\n")
	b.WriteString("FILTERS=/etc/bird/filters_templates.conf\n")
	b.WriteString("BAK=\"\"\n")
	b.WriteString("CREATED_TUNS=\"\"\n")
	// filters: write only if missing (never clobber).
	fmt.Fprintf(&b, "if [ ! -f \"$FILTERS\" ]; then echo '[filters] writing '\"$FILTERS\"; echo '%s' | base64 -d > \"$FILTERS\"; fi\n", b64(filters))

	// Back up any existing file up front (both paths are then reversible).
	b.WriteString("if [ -f \"$BIRDCONF\" ]; then BAK=\"$BIRDCONF.ncn-bak.$TS\"; cp -a \"$BIRDCONF\" \"$BAK\"; echo \"[backup] $BAK\"; fi\n")

	b.WriteString("if [ -f \"$BIRDCONF\" ] && grep -q 'define MY_ASN' \"$BIRDCONF\"; then\n")
	b.WriteString("  echo '[bird] NCN config detected — additive (preserving existing config)'\n")
	b.WriteString("  grep -q 'filters_templates.conf' \"$BIRDCONF\" || { printf '\\ninclude \"/etc/bird/filters_templates.conf\";\\n' >> \"$BIRDCONF\"; echo '[bird] + include filters_templates.conf'; }\n")
	for proto, block := range blocks {
		fmt.Fprintf(&b, "  if grep -q '^protocol bgp %s ' \"$BIRDCONF\"; then echo '[bird] %s already present — skip'; else echo '[bird] append %s'; { echo; echo '%s' | base64 -d; } >> \"$BIRDCONF\"; fi\n",
			proto, proto, proto, b64(block))
	}
	b.WriteString("else\n")
	fmt.Fprintf(&b, "  FULL='%s'\n", b64(fullBird))
	b.WriteString("  if [ -z \"$FULL\" ]; then echo '[FAIL] target has no NCN bird.conf and no full config to write — set up base bird.conf first'; exit 1; fi\n")
	b.WriteString("  echo '[bird] no NCN config — writing full generated bird.conf'; echo \"$FULL\" | base64 -d > \"$BIRDCONF\"\n")
	b.WriteString("fi\n")

	// tunnels (idempotent; no flap on existing links).
	for _, t := range tunnels {
		b.WriteString(t + "\n")
	}

	// validate + apply, with rollback. Success when bird accepted the config:
	// it replies "Reconfigured" (applied) OR "Reconfiguration in progress"
	// (accepted, applying async). A parse error replies "Reconfiguration
	// failed" / a file:line error and matches neither → rollback.
	b.WriteString("birdc configure soft > /tmp/ncn-soft.log 2>&1\n")
	b.WriteString("if grep -qiE 'reconfigured|reconfiguration in progress' /tmp/ncn-soft.log; then echo '[ok] birdc configure soft → accepted'; sed 's/^/    /' /tmp/ncn-soft.log; else\n")
	b.WriteString("  echo '[FAIL] configure soft:'; sed 's/^/    /' /tmp/ncn-soft.log\n")
	b.WriteString("  if [ -n \"$BAK\" ] && [ -f \"$BAK\" ]; then cp -a \"$BAK\" \"$BIRDCONF\"; birdc configure soft >/dev/null 2>&1 || true; echo \"[rollback] restored $BAK\"; fi\n")
	b.WriteString("  for i in $CREATED_TUNS; do ip tunnel del \"$i\" 2>/dev/null && echo \"[rollback] removed tunnel $i\"; done\n")
	b.WriteString("  exit 1\n")
	b.WriteString("fi\n")
	return b.String()
}

// greApplyLines renders idempotent GRE bring-up (guarded by `ip link show`).
// A tunnel this run NEWLY creates is recorded in $CREATED_TUNS so the rollback
// path can delete it — otherwise a failed apply leaves an orphan GRE tunnel on
// the peer (the rollback restores bird.conf but not interfaces).
func greApplyLines(tunIface, selfIP, peerIP string, selfRegion, selfNum int) []string {
	ll := meshLinkLocal(selfRegion, selfNum)
	return []string{
		fmt.Sprintf("if ! ip link show %s >/dev/null 2>&1; then ip tunnel add %s mode gre remote %s local %s ttl 255 && CREATED_TUNS=\"$CREATED_TUNS %s\"; echo '[tunnel] + %s'; fi",
			tunIface, tunIface, peerIP, selfIP, tunIface, tunIface),
		fmt.Sprintf("ip link set %s up mtu 1476", tunIface),
		fmt.Sprintf("ip -6 addr add %s/128 dev %s 2>/dev/null || true", ll, tunIface),
	}
}

func anchorApplyLines(anchor string) []string {
	return []string{
		"ip link add dummy0 type dummy 2>/dev/null || true; ip link set dummy0 up",
		fmt.Sprintf("ip -6 addr add %s/128 dev dummy0 2>/dev/null || true", anchor),
	}
}

// runMeshScriptOnNode executes a script on a target: locally when it's the
// console node, else over SSH with the fleet key. Non-root login users get a
// `sudo -n` wrapper (the fleet contract guarantees passwordless sudo). Streams
// each output line to onLine. Returns (exitCode, err).
func (f *fleetScraper) runMeshScriptOnNode(ctx context.Context, rec nodeRecord, script string, onLine func(string)) (int, error) {
	var cmd *exec.Cmd
	if rec.ID == f.localID {
		// ncn-api runs as root on the console node — run the script directly.
		cmd = exec.CommandContext(ctx, "bash", "-c", script)
	} else {
		port := rec.SSHPort
		if port <= 0 {
			port = 22
		}
		user := rec.SSHUser
		if user == "" {
			user = "root"
		}
		remote := "bash -s"
		if user != "root" {
			remote = "sudo -n bash -s"
		}
		args := []string{
			"-p", itoa(port),
			"-i", "/etc/ncn-core-console/fleet-key",
			"-o", "StrictHostKeyChecking=accept-new",
			"-o", "UserKnownHostsFile=/etc/ncn-core-console/fleet-known-hosts",
			"-o", "BatchMode=yes",
			"-o", "ConnectTimeout=10",
			user + "@" + rec.Address, remote,
		}
		cmd = exec.CommandContext(ctx, "ssh", args...)
		cmd.Stdin = strings.NewReader(script)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return -1, err
	}
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	var wg sync.WaitGroup
	scan := func(rd io.Reader) {
		defer wg.Done()
		sc := bufio.NewScanner(rd)
		sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
		for sc.Scan() {
			onLine(strings.TrimRight(sc.Text(), "\r"))
		}
	}
	wg.Add(2)
	go scan(stdout)
	go scan(stderr)
	wg.Wait()
	werr := cmd.Wait()
	exit := 0
	if werr != nil {
		if ee, ok := werr.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			exit = -1
		}
	}
	return exit, werr
}

// handleNodeMeshApply dispatches POST (start) / GET (poll) for
// /api/v1/auth/nodes/{id}/mesh-apply.
func (f *fleetScraper) handleNodeMeshApply(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		f.meshApplyMu.Lock()
		job := f.meshApply[id]
		f.meshApplyMu.Unlock()
		if job == nil {
			writeJSON(w, http.StatusOK, envelope{OK: true, Data: nil})
			return
		}
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: job.snapshot()})
	case http.MethodPost:
		f.startMeshApply(w, r, id)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
	}
}

func (f *fleetScraper) startMeshApply(w http.ResponseWriter, r *http.Request, id string) {
	op := adminOperator(r)
	var req struct {
		Targets    []string          `json:"targets"`    // node ids to auto-apply
		Transports map[string]string `json:"transports"` // peerID → gre|wg
		Region     int               `json:"region"`
		Confirm    string            `json:"confirm"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	if req.Confirm != meshApplyConfirm(id) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "confirm word mismatch — type: " + meshApplyConfirm(id)})
		return
	}
	job, err := f.beginMeshApply(id, req.Targets, req.Transports, req.Region, op)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: job.snapshot()})
}

// beginMeshApply is the shared core (no HTTP, no confirm parsing) used by the
// HTTP handler above and the Telegram bot (bot_netadmin.go). Resolves the peer
// set, builds the per-target additive bird.conf + GRE bring-up scripts, spins up
// the async job (backup / configure soft / rollback per target), audits, and
// notifies. transports nil → all GRE. actor = operator or "tg:<user>".
func (f *fleetScraper) beginMeshApply(id string, targets []string, transports map[string]string, region int, actor string) (*onboardJob, error) {
	target, ok := f.registry.get(id)
	if !ok {
		return nil, fmt.Errorf("node not found")
	}
	if region > 0 {
		target.Region = region
	}
	if target.NodeNum == 0 {
		target.NodeNum = nodeNumFromID(target.ID)
	}
	if target.Region <= 0 || target.NodeNum <= 0 {
		return nil, fmt.Errorf("target missing region/node_num")
	}
	if transports == nil {
		transports = map[string]string{}
	}

	all := map[string]nodeRecord{}
	var peers []nodeRecord
	for _, rec := range f.registry.listSnapshot() {
		all[rec.ID] = rec
		if rec.ID != id && rec.active() {
			peers = append(peers, rec)
		}
	}
	want := map[string]bool{}
	for _, t := range targets {
		want[t] = true
	}
	if len(want) == 0 {
		return nil, fmt.Errorf("no targets selected")
	}

	type applyTarget struct {
		rec    nodeRecord
		script string
		label  string
	}
	var workList []applyTarget
	selfIface := meshIface(target.ID)
	for tid := range want {
		rec, ok := all[tid]
		if !ok || !rec.active() {
			return nil, fmt.Errorf("unknown/inactive target: %s", tid)
		}
		if tid == id {
			blocks := map[string]string{}
			var tuns []string
			tuns = append(tuns, anchorApplyLines(meshAnchor(target.Region, target.NodeNum))...)
			for _, p := range peers {
				if p.Region <= 0 || p.NodeNum <= 0 || strings.ToLower(transports[p.ID]) == "wg" {
					continue
				}
				pi := meshIface(p.ID)
				blocks["ibgp_"+pi] = ibgpBlock(pi, p.Region, p.NodeNum)
				tuns = append(tuns, greApplyLines(pi, target.Address, p.Address, target.Region, target.NodeNum)...)
			}
			script := buildApplyScript(blocks, tuns, mustNewNodeBird(target, peers), filtersTemplatesConf)
			workList = append(workList, applyTarget{rec: rec, script: script, label: "新节点 " + tid})
		} else {
			if strings.ToLower(transports[tid]) == "wg" {
				return nil, fmt.Errorf("%s link is WireGuard — auto-apply is GRE-only; use the manual snippet", tid)
			}
			blocks := map[string]string{"ibgp_" + selfIface: ibgpBlock(selfIface, target.Region, target.NodeNum)}
			tuns := greApplyLines(selfIface, rec.Address, target.Address, rec.Region, rec.NodeNum)
			script := buildApplyScript(blocks, tuns, "", filtersTemplatesConf)
			workList = append(workList, applyTarget{rec: rec, script: script, label: tid})
		}
	}

	f.meshApplyMu.Lock()
	if cur := f.meshApply[id]; cur != nil && cur.snapshot().Running {
		f.meshApplyMu.Unlock()
		return nil, fmt.Errorf("mesh apply already running for %s", id)
	}
	steps := make([]onboardStep, 0, len(workList))
	for _, wt := range workList {
		steps = append(steps, onboardStep{Name: "应用 · " + wt.label, Status: "pending"})
	}
	job := &onboardJob{nodeID: id, steps: steps, running: true, startedAt: time.Now().UnixMilli()}
	f.meshApply[id] = job
	f.meshApplyMu.Unlock()

	auditRecord(nil, AuditEvent{Event: "node.mesh-apply", Severity: auditSevWarn, Actor: actor, Target: id,
		Details: map[string]any{"targets": targets}})

	go func() {
		allOK := true
		var failed []string // per-peer "label: reason" for the aggregate failure card
		for i, wt := range workList {
			job.set(i, "running", "")
			job.appendLog("── " + wt.label + " ──")
			ctx, cancel := context.WithTimeout(context.Background(), meshApplyTimeout)
			exit, err := f.runMeshScriptOnNode(ctx, wt.rec, wt.script, func(l string) { job.appendLog(l) })
			cancel()
			if err != nil || exit != 0 {
				reason := fmt.Sprintf("exit=%d", exit)
				if err != nil {
					reason = err.Error()
				}
				job.set(i, "fail", reason)
				job.appendLog("[FAIL] " + wt.label + " · " + reason)
				failed = append(failed, wt.label+": "+reason)
				allOK = false
				continue
			}
			job.set(i, "ok", "configure soft 已应用")
		}
		job.mu.Lock()
		job.running = false
		job.done = true
		job.ok = allOK
		job.mu.Unlock()
		if allOK {
			f.notify.NotifyEvent("🟢", "Mesh applied", []tgField{{"node", id}, {"targets", strings.Join(targets, " ")}, {"by", actor}}, false)
		} else {
			// One actionable failure card for the whole weave — retry re-runs it.
			recordOpFailure(f.notify, &opFailure{
				Kind: opKindMeshApply, Target: id, Actor: actor,
				Reason:         strings.Join(failed, " · "),
				MeshTargets:    targets,
				MeshTransports: transports,
				MeshRegion:     region,
			})
		}
	}()

	return job, nil
}

// mustNewNodeBird renders the new node's full bird.conf (for the fresh-write
// path), tolerating the generator's error by returning a minimal comment so we
// never panic mid-apply (the additive path is what runs on an existing file).
func mustNewNodeBird(target nodeRecord, peers []nodeRecord) string {
	b, err := genMeshConfig(target, peers, nil)
	if err != nil {
		return "# (config generation failed: " + err.Error() + ")\n"
	}
	return b.NewNodeBird
}
