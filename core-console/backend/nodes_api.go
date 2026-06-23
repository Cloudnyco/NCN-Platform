// Admin node-management API — the HTTP surface behind the /admin/servers page.
//
// All handlers are admin-only (wired with auth.requireRole("admin", …) in
// main.go). They mutate the persistent node registry (globalNodes) and then
// reconcile the live scraper state via fleetScraper.apply* so changes take
// effect without an ncn-api restart.
//
//	GET    /api/v1/auth/nodes                    list (registry + live status)
//	POST   /api/v1/auth/nodes                    add
//	PATCH  /api/v1/auth/nodes/{id}               edit metadata
//	POST   /api/v1/auth/nodes/{id}/decommission  soft down (reversible)
//	POST   /api/v1/auth/nodes/{id}/recommission  restore
//	DELETE /api/v1/auth/nodes/{id}               permanent delete (+ purge key)
//	POST   /api/v1/auth/nodes/{id}/provision     run agent-node-provision.sh
//	GET    /api/v1/auth/nodes/{id}/health        curl the agent /v1/healthz

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// provisionScript is the on-tyo automation that mints the HMAC key + TLS cert,
// ships the agent, and starts it. ncn-api runs as root, so it can exec this
// directly. The node id is passed as a single argv (never interpolated into a
// shell string) and is pre-validated against nodeIDRe, so there is no shell
// injection surface.
const provisionScript = "/opt/ncn-core-console/scripts/agent-node-provision.sh"

// provisionTimeout is kept just under the server's 75s WriteTimeout so the
// handler returns a clean result rather than the connection being cut. The
// script is idempotent, so a timeout is recoverable by re-running.
const provisionTimeout = 70 * time.Second

// nodeView is one row of the admin server table: the persisted record plus
// the live runtime signals the operator wants to see at a glance.
type nodeView struct {
	nodeRecord
	Local        bool `json:"local"`           // the console host itself (can't be removed)
	Scraped      bool `json:"scraped"`          // has a cache entry yet
	OK           bool `json:"ok"`               // last scrape succeeded
	CertDaysLeft int  `json:"cert_days_left"`   // agent TLS cert days remaining (0 = unknown)
}

// handleNodesRoot dispatches GET (list) and POST (create).
func (f *fleetScraper) handleNodesRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		f.handleNodesList(w, r)
	case http.MethodPost:
		f.handleNodeCreate(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
	}
}

// handleNodesItem dispatches /api/v1/auth/nodes/{id}[/action].
func (f *fleetScraper) handleNodesItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/nodes/")
	// /api/v1/auth/nodes/geo?address=IP — geo autodetect. Handled before the
	// id parse so a literal "geo" path can't be mistaken for a node id.
	if rest == "geo" && r.Method == http.MethodGet {
		f.handleNodeGeo(w, r)
		return
	}
	id, action, _ := strings.Cut(rest, "/")
	if !validNodeID(id) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid node id"})
		return
	}
	switch {
	case action == "" && r.Method == http.MethodPatch:
		f.handleNodePatch(w, r, id)
	case action == "" && r.Method == http.MethodDelete:
		f.handleNodeDelete(w, r, id)
	case action == "decommission" && r.Method == http.MethodPost:
		f.handleNodeStatus(w, r, id, nodeStatusDecommissioned)
	case action == "recommission" && r.Method == http.MethodPost:
		f.handleNodeStatus(w, r, id, nodeStatusActive)
	case action == "provision" && r.Method == http.MethodPost:
		f.handleNodeProvision(w, r, id)
	case action == "onboard":
		f.handleNodeOnboard(w, r, id)
	case action == "health" && r.Method == http.MethodGet:
		f.handleNodeHealth(w, r, id)
	case action == "mesh-config" && r.Method == http.MethodPost:
		f.handleNodeMeshConfig(w, r, id)
	case action == "mesh-apply":
		f.handleNodeMeshApply(w, r, id)
	case action == "anycast" && r.Method == http.MethodGet:
		f.handleNodeAnycast(w, r, id)
	case action == "anycast/drain":
		f.handleNodeAnycastDrain(w, r, id, false)
	case action == "anycast/undrain":
		f.handleNodeAnycastDrain(w, r, id, true)
	default:
		w.Header().Set("Allow", "PATCH, DELETE, POST, GET")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method/path not allowed"})
	}
}

// runtimeSignals snapshots the per-node live state (ok / scraped / cert days)
// under the fleet lock for the list view.
func (f *fleetScraper) runtimeSignals() (ok, scraped map[string]bool, cert map[string]int, localID string) {
	ok = map[string]bool{}
	scraped = map[string]bool{}
	cert = map[string]int{}
	f.mu.RLock()
	for id, st := range f.cache {
		scraped[id] = true
		if st != nil {
			ok[id] = st.OK
		}
	}
	for id, d := range f.agentCertDaysLeft {
		cert[id] = d
	}
	localID = f.localID
	f.mu.RUnlock()
	return
}

func (f *fleetScraper) handleNodesList(w http.ResponseWriter, _ *http.Request) {
	recs := f.registry.listSnapshot()
	okByID, scrapedByID, certByID, localID := f.runtimeSignals()
	out := make([]nodeView, 0, len(recs))
	for _, rec := range recs {
		out = append(out, nodeView{
			nodeRecord:   rec,
			Local:        rec.ID == localID,
			Scraped:      scrapedByID[rec.ID],
			OK:           okByID[rec.ID],
			CertDaysLeft: certByID[rec.ID],
		})
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
}

func (f *fleetScraper) handleNodeCreate(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	var req struct {
		ID          string  `json:"id"`
		Label       string  `json:"label"`
		Country     string  `json:"country"`
		Address     string  `json:"address"`
		Lat         float64 `json:"lat"`
		Lon         float64 `json:"lon"`
		SSHUser     string  `json:"ssh_user"`
		SSHIdentity string  `json:"ssh_identity"`
		SSHPort     int     `json:"ssh_port"`
		Region      int     `json:"region"` // 0 = auto (same-metro) / required for a new metro
		Arch        string  `json:"arch"`
		Notes       string  `json:"notes"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json: " + err.Error()})
		return
	}
	req.ID = strings.TrimSpace(strings.ToLower(req.ID))
	req.Label = strings.TrimSpace(req.Label)
	req.Country = strings.ToUpper(strings.TrimSpace(req.Country))
	req.Address = strings.TrimSpace(req.Address)
	req.SSHUser = strings.TrimSpace(req.SSHUser)
	req.Arch = strings.TrimSpace(req.Arch)

	if !validNodeID(req.ID) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "id must match ^[a-z0-9][a-z0-9-]{1,30}$ (e.g. lax-01)"})
		return
	}
	if req.Label == "" || req.Address == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "label + address required"})
		return
	}
	if req.SSHUser == "" {
		req.SSHUser = "root"
	}
	if req.Arch == "" {
		req.Arch = "amd64"
	}
	if req.SSHPort < 0 || req.SSHPort > 65535 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "ssh_port out of range"})
		return
	}
	// Region / NodeNum for the mesh addressing convention. NodeNum comes from
	// the id suffix; Region is auto-derived for a same-metro node (sibling's
	// region or the historical map), else the operator must supply it.
	nodeNum := nodeNumFromID(req.ID)
	region := req.Region
	if region == 0 {
		region = f.registry.regionForMetro(metroOfID(req.ID))
	}
	if region < 0 || region > 999 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "region out of range (1..999)"})
		return
	}
	now := time.Now().UTC()
	rec := nodeRecord{
		ID: req.ID, Label: req.Label, Country: req.Country, Address: req.Address,
		Lat: req.Lat, Lon: req.Lon, SSHUser: req.SSHUser, SSHIdentity: req.SSHIdentity,
		SSHPort: req.SSHPort, Region: region, NodeNum: nodeNum,
		Arch: req.Arch, Notes: req.Notes, Status: nodeStatusActive,
		CreatedBy: op, CreatedAt: now, UpdatedAt: now,
	}
	if err := f.registry.add(rec); err != nil {
		writeJSON(w, http.StatusConflict, envelope{OK: false, Error: err.Error()})
		return
	}
	f.applyAddNode(rec)
	auditRecord(r, AuditEvent{
		Event: "node.create", Severity: auditSevWarn, Actor: op, Target: rec.ID,
		Details: map[string]any{"label": rec.Label, "address": rec.Address},
	})
	f.notify.NotifyEvent("➕", "Server registered", []tgField{
		{"node", rec.ID}, {"label", rec.Label}, {"addr", rec.Address}, {"by", op},
	}, false)
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: rec})
}

func (f *fleetScraper) handleNodePatch(w http.ResponseWriter, r *http.Request, id string) {
	op := adminOperator(r)
	var p nodePatch
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	rec, err := f.registry.update(id, p)
	if err != nil {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: err.Error()})
		return
	}
	f.applyUpdateNode(rec)
	auditRecord(r, AuditEvent{Event: "node.update", Severity: auditSevInfo, Actor: op, Target: id})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: rec})
}

// decommission / recommission / deleteNode are the shared node-lifecycle
// mutations, called by BOTH the HTTP handlers below and the Telegram bot
// (bot_netadmin.go). Each guards the local console node, mutates the registry,
// reconciles the live scrape set, audits, and notifies. actor = operator
// username (HTTP) or "tg:<user>" (bot). Audit uses a nil *http.Request (the
// bot has none); auditRecord tolerates that and the Actor field identifies who.
func (f *fleetScraper) decommission(id, actor string) (nodeRecord, error) {
	if id == f.localID {
		return nodeRecord{}, fmt.Errorf("cannot decommission the local console node (%s)", id)
	}
	rec, err := f.registry.setStatus(id, nodeStatusDecommissioned)
	if err != nil {
		recordOpFailure(f.notify, &opFailure{Kind: opKindDecommission, Target: id, Actor: actor, Reason: err.Error()})
		return nodeRecord{}, err
	}
	f.applyDeactivateNode(id, false)
	auditRecord(nil, AuditEvent{Event: "node.decommission", Severity: auditSevWarn, Actor: actor, Target: id})
	f.notify.NotifyEvent("⬇️", "Server decommissioned", []tgField{{"node", id}, {"label", rec.Label}, {"by", actor}}, true)
	return rec, nil
}

func (f *fleetScraper) recommission(id, actor string) (nodeRecord, error) {
	if id == f.localID {
		return nodeRecord{}, fmt.Errorf("cannot recommission the local console node (%s)", id)
	}
	rec, err := f.registry.setStatus(id, nodeStatusActive)
	if err != nil {
		recordOpFailure(f.notify, &opFailure{Kind: opKindRecommission, Target: id, Actor: actor, Reason: err.Error()})
		return nodeRecord{}, err
	}
	f.applyAddNode(rec)
	auditRecord(nil, AuditEvent{Event: "node.recommission", Severity: auditSevInfo, Actor: actor, Target: id})
	f.notify.NotifyEvent("⬆️", "Server recommissioned", []tgField{{"node", id}, {"label", rec.Label}, {"by", actor}}, false)
	return rec, nil
}

func (f *fleetScraper) deleteNode(id, actor string) error {
	if id == f.localID {
		return fmt.Errorf("cannot delete the local console node (%s)", id)
	}
	if _, ok := f.registry.get(id); !ok {
		return fmt.Errorf("node not found")
	}
	if err := f.registry.remove(id); err != nil {
		recordOpFailure(f.notify, &opFailure{Kind: opKindDelete, Target: id, Actor: actor, Reason: err.Error()})
		return err
	}
	f.applyDeactivateNode(id, true)
	// Purge the on-disk HMAC key for this node (best-effort — absence is fine).
	if err := os.Remove(filepath.Join("/etc/ncn-core-console/agent-keys", id+".key")); err != nil && !os.IsNotExist(err) {
		log.Printf("nodes: delete %s — could not remove key file: %v", id, err)
	}
	auditRecord(nil, AuditEvent{Event: "node.delete", Severity: auditSevCritical, Actor: actor, Target: id})
	f.notify.NotifyEvent("🗑", "Server permanently deleted", []tgField{{"node", id}, {"by", actor}}, true)
	return nil
}

func (f *fleetScraper) handleNodeStatus(w http.ResponseWriter, r *http.Request, id, status string) {
	op := adminOperator(r)
	if id == f.localID {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "cannot change status of the local console node (" + id + ")"})
		return
	}
	var rec nodeRecord
	var err error
	if status == nodeStatusDecommissioned {
		rec, err = f.decommission(id, op)
	} else {
		rec, err = f.recommission(id, op)
	}
	if err != nil {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: rec})
}

func (f *fleetScraper) handleNodeDelete(w http.ResponseWriter, r *http.Request, id string) {
	op := adminOperator(r)
	if id == f.localID {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "cannot delete the local console node (" + id + ")"})
		return
	}
	if _, ok := f.registry.get(id); !ok {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "node not found"})
		return
	}
	if err := f.deleteNode(id, op); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true})
}

// handleNodeProvision runs the provisioning script for a registered node. The
// node must already exist in the registry (so its ssh_user/arch/address are
// available to the script via the registry JSON). Returns the script's
// combined output for the operator to read.
func (f *fleetScraper) handleNodeProvision(w http.ResponseWriter, r *http.Request, id string) {
	op := adminOperator(r)
	if id == f.localID {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "the local console node does not run a remote agent"})
		return
	}
	rec, ok := f.registry.get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "node not found — add it first"})
		return
	}
	if _, err := os.Stat(provisionScript); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "provision script not found at " + provisionScript})
		return
	}
	auditRecord(r, AuditEvent{Event: "node.provision", Severity: auditSevWarn, Actor: op, Target: id})
	var sb strings.Builder
	exit, err := f.execProvision(id, rec, func(l string) { sb.WriteString(l); sb.WriteByte('\n') })
	// On success, reload HMAC keys so the freshly-minted key is picked up
	// without waiting for a SIGHUP.
	if err == nil && exit == 0 {
		f.ReloadAgentKeys()
		f.notify.NotifyEvent("🟢", "Server provisioned", []tgField{
			{"node", id}, {"label", rec.Label}, {"by", op},
		}, false)
	} else {
		reason := fmt.Sprintf("exit=%d", exit)
		if err != nil {
			reason = err.Error()
		}
		f.notify.NotifyEvent("🔴", "Provision failed", []tgField{
			{"node", id}, {"reason", reason}, {"by", op},
		}, true)
	}
	combined := strings.TrimRight(sb.String(), "\n")
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	writeJSON(w, http.StatusOK, envelope{OK: err == nil && exit == 0, Data: map[string]any{
		"exit":   exit,
		"output": combined,
		"error":  errStr,
	}})
}

// execProvision runs the provisioning script for one node and STREAMS each
// output line to onLine as it arrives — so the onboard UI can show live
// progress instead of one spinner for the whole ~30–60s run. Node params are
// passed via environment (no jq needed); id is the sole argv and is
// pre-validated against nodeIDRe, so there is no shell-injection surface.
// Returns (exitCode, runErr).
func (f *fleetScraper) execProvision(id string, rec nodeRecord, onLine func(string)) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
	defer cancel()
	port := rec.SSHPort
	if port <= 0 {
		port = 22
	}
	cmd := exec.CommandContext(ctx, provisionScript, id)
	cmd.Env = append(os.Environ(),
		"NCN_PROV_SSH_USER="+rec.SSHUser,
		"NCN_PROV_ARCH="+rec.Arch,
		"NCN_PROV_SAN_IP="+rec.Address,
		"NCN_PROV_SSH_PORT="+strconv.Itoa(port),
	)
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
	scan := func(r io.Reader) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r")
			if strings.TrimSpace(line) != "" {
				onLine(line)
			}
		}
	}
	wg.Add(2)
	go scan(stdout)
	go scan(stderr)
	wg.Wait()
	err = cmd.Wait()
	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exit = ee.ExitCode()
		} else {
			exit = -1
		}
	}
	return exit, err
}

// handleNodeHealth curls the node's agent /v1/healthz (no HMAC required) using
// the CA-pinned client, so the operator can confirm the agent is up.
func (f *fleetScraper) handleNodeHealth(w http.ResponseWriter, _ *http.Request, id string) {
	rec, ok := f.registry.get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "node not found"})
		return
	}
	if f.agentClient == nil {
		writeJSON(w, http.StatusOK, envelope{OK: false, Error: "agent transport not initialised (no agent CA)"})
		return
	}
	url := fmt.Sprintf("https://%s:9101/v1/healthz", rec.Address)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := f.agentClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, envelope{OK: false, Error: err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	writeJSON(w, http.StatusOK, envelope{OK: resp.StatusCode == http.StatusOK, Data: map[string]any{
		"status": resp.StatusCode,
		"body":   strings.TrimSpace(string(body)),
	}})
}
