// Config drift detection + one-click rollback.
//
// The console can generate and apply node config, but nothing watched whether a
// box's LIVE config still matched what we intended — a manual `birdc`/editor
// change drifted silently (we hit this during the RPKI migration: a hand-edit on
// a live bird.conf caused a protocol-name clash no one was tracking). This adds
// a declarative baseline per node (config_declarations) and a periodic check
// that re-hashes the live files and flags drift, surfaced as the config_drift
// alert metric + the Connectivity "drift" UI.
//
// Tracked sources: /etc/bird/bird.conf, /etc/bird/filters_templates.conf (file
// contents), and `nft -s list ruleset` (STATELESS — `-s` omits packet/byte
// counters, which would otherwise make the hash change every second).
//
// One-click rollback restores ONLY the two BIRD files via `birdc configure soft`
// (atomic, auto-rollback on parse error). nft drift is detected + alerted but
// NOT auto-restored — blindly re-applying a firewall ruleset can lock the node
// out; that stays a human action. Everything is confirm-gated + audited.

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var globalDrift *driftStore

const (
	configDeclPath     = incidentsDir + "/config_declarations.json"
	driftCheckInterval = 90 * time.Second
)

// configDecl is one node's adopted baseline (content + node-computed hashes).
type configDecl struct {
	NodeID      string `json:"node_id"`
	BirdConf    string `json:"bird_conf"`
	Filters     string `json:"filters"`
	Nft         string `json:"nft"`
	BirdHash    string `json:"bird_hash"`
	FiltersHash string `json:"filters_hash"`
	NftHash     string `json:"nft_hash"`
	CapturedAt  int64  `json:"captured_at"`
	CapturedBy  string `json:"captured_by"`
}

type driftState struct {
	NodeID       string `json:"node_id"`
	HasBaseline  bool   `json:"has_baseline"`
	BirdDrift    bool   `json:"bird_drift"`
	FiltersDrift bool   `json:"filters_drift"`
	NftDrift     bool   `json:"nft_drift"`
	Drift        bool   `json:"drift"`
	CheckedAt    int64  `json:"checked_at"`
	CapturedAt   int64  `json:"captured_at,omitempty"`
	CapturedBy   string `json:"captured_by,omitempty"`
	Err          string `json:"error,omitempty"`
}

type driftStore struct {
	mu     sync.Mutex
	fleet  *fleetScraper
	notify *tgNotifier
	decls  map[string]*configDecl
	state  map[string]*driftState
	warned map[string]bool // dedup the drift TG alert per node
}

func newDriftStore(fleet *fleetScraper, notify *tgNotifier) *driftStore {
	s := &driftStore{
		fleet:  fleet,
		notify: notify,
		decls:  map[string]*configDecl{},
		state:  map[string]*driftState{},
		warned: map[string]bool{},
	}
	s.load()
	return s
}

func (s *driftStore) load() {
	var doc []byte
	if globalDB != nil {
		if b, err := loadConfigDoc("config_declarations"); err == nil && b != nil {
			doc = b
		}
	}
	if doc == nil {
		if b, err := os.ReadFile(configDeclPath); err == nil && len(b) > 0 {
			doc = b
		}
	}
	if doc != nil {
		_ = json.Unmarshal(doc, &s.decls)
	}
}

func (s *driftStore) persistLocked() {
	b, err := json.Marshal(s.decls)
	if err != nil {
		return
	}
	writeFileAtomic(configDeclPath, b)
	if globalDB != nil {
		if err := saveConfigDoc("config_declarations", b); err != nil {
			log.Printf("drift: db persist failed (%v) — file is current", err)
		}
	}
}

// ── periodic check ───────────────────────────────────────────────────────────

func (s *driftStore) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(driftCheckInterval)
		defer t.Stop()
		s.checkAll(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.checkAll(ctx)
			}
		}
	}()
}

const driftHashScript = `echo "BIRD $(sha256sum /etc/bird/bird.conf 2>/dev/null | cut -d' ' -f1)"
echo "FILTERS $(sha256sum /etc/bird/filters_templates.conf 2>/dev/null | cut -d' ' -f1)"
echo "NFT $(nft -s list ruleset 2>/dev/null | sha256sum | cut -d' ' -f1)"`

func (s *driftStore) checkAll(ctx context.Context) {
	// snapshot the baseline node set under lock, then probe without holding it.
	s.mu.Lock()
	nodes := make([]string, 0, len(s.decls))
	for id := range s.decls {
		nodes = append(nodes, id)
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, id := range nodes {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			s.checkNode(ctx, id)
		}(id)
	}
	wg.Wait()
}

func (s *driftStore) checkNode(ctx context.Context, id string) {
	rec, ok := s.fleet.registry.get(id)
	if !ok {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var sb strings.Builder
	if _, err := s.fleet.runMeshScriptOnNode(cctx, rec, driftHashScript, func(l string) { sb.WriteString(l); sb.WriteString("\n") }); err != nil {
		s.mu.Lock()
		if st := s.state[id]; st != nil {
			st.Err = err.Error()
			st.CheckedAt = time.Now().Unix()
		}
		s.mu.Unlock()
		return
	}
	var birdH, filtH, nftH string
	for _, line := range strings.Split(sb.String(), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "BIRD":
			birdH = f[1]
		case "FILTERS":
			filtH = f[1]
		case "NFT":
			nftH = f[1]
		}
	}

	s.mu.Lock()
	decl := s.decls[id]
	st := &driftState{NodeID: id, HasBaseline: decl != nil, CheckedAt: time.Now().Unix()}
	if decl != nil {
		st.CapturedAt = decl.CapturedAt
		st.CapturedBy = decl.CapturedBy
		st.BirdDrift = birdH != "" && birdH != decl.BirdHash
		st.FiltersDrift = filtH != "" && filtH != decl.FiltersHash
		st.NftDrift = nftH != "" && nftH != decl.NftHash
		st.Drift = st.BirdDrift || st.FiltersDrift || st.NftDrift
	}
	s.state[id] = st
	drift := st.Drift
	s.mu.Unlock()

	s.maybeAlert(id, drift, st)
}

// maybeAlert pushes a one-shot TG warning when a node first drifts, and re-arms
// when it returns to baseline.
func (s *driftStore) maybeAlert(id string, drift bool, st *driftState) {
	if s.notify == nil {
		return
	}
	s.mu.Lock()
	was := s.warned[id]
	s.warned[id] = drift
	s.mu.Unlock()
	if drift && !was {
		var which []string
		if st.BirdDrift {
			which = append(which, "bird.conf")
		}
		if st.FiltersDrift {
			which = append(which, "filters")
		}
		if st.NftDrift {
			which = append(which, "nft")
		}
		channel := s.notify.errorChat
		if channel == "" {
			channel = s.notify.chatID
		}
		s.notify.enqueue(tgPayload{ChatID: channel, Text: fmt.Sprintf(
			"🧬 <b>配置漂移</b> — %s\n实际配置偏离声明基线: %s\n<blockquote>在 控制台 → 连通性 查看 diff,可一键回滚 BIRD(nft 需人工核对)。</blockquote>",
			id, strings.Join(which, ", "))}, "drift-"+id)
	}
}

// driftMetric feeds the config_drift alert metric. ok=false when the node has no
// adopted baseline (nothing to compare) → the rule stays quiet.
func (s *driftStore) driftMetric(id string) (float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state[id]
	if st == nil || !st.HasBaseline {
		return 0, false
	}
	if st.Drift {
		return 1, true
	}
	return 0, true
}

// ── capture baseline / fetch actual ──────────────────────────────────────────

const driftCaptureScript = `echo "BIRDHASH $(sha256sum /etc/bird/bird.conf 2>/dev/null | cut -d' ' -f1)"
echo "BIRDB64 $(base64 -w0 /etc/bird/bird.conf 2>/dev/null)"
echo "FILTERSHASH $(sha256sum /etc/bird/filters_templates.conf 2>/dev/null | cut -d' ' -f1)"
echo "FILTERSB64 $(base64 -w0 /etc/bird/filters_templates.conf 2>/dev/null)"
echo "NFTHASH $(nft -s list ruleset 2>/dev/null | sha256sum | cut -d' ' -f1)"
echo "NFTB64 $(nft -s list ruleset 2>/dev/null | base64 -w0)"`

// fetchConfig runs the capture script and returns the decoded content + hashes.
func (s *driftStore) fetchConfig(ctx context.Context, id string) (*configDecl, error) {
	rec, ok := s.fleet.registry.get(id)
	if !ok {
		return nil, fmt.Errorf("unknown node %q", id)
	}
	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var sb strings.Builder
	if _, err := s.fleet.runMeshScriptOnNode(cctx, rec, driftCaptureScript, func(l string) { sb.WriteString(l); sb.WriteString("\n") }); err != nil {
		return nil, err
	}
	d := &configDecl{NodeID: id}
	dec := func(b64 string) string {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if err != nil {
			return ""
		}
		return string(raw)
	}
	for _, line := range strings.Split(sb.String(), "\n") {
		i := strings.IndexByte(line, ' ')
		if i < 0 {
			continue
		}
		key, val := line[:i], line[i+1:]
		switch key {
		case "BIRDHASH":
			d.BirdHash = strings.TrimSpace(val)
		case "BIRDB64":
			d.BirdConf = dec(val)
		case "FILTERSHASH":
			d.FiltersHash = strings.TrimSpace(val)
		case "FILTERSB64":
			d.Filters = dec(val)
		case "NFTHASH":
			d.NftHash = strings.TrimSpace(val)
		case "NFTB64":
			d.Nft = dec(val)
		}
	}
	if d.BirdHash == "" {
		return nil, fmt.Errorf("could not read bird.conf on %s", id)
	}
	return d, nil
}

// adopt captures the node's current config as its baseline.
func (s *driftStore) adopt(ctx context.Context, id, actor string) (*configDecl, error) {
	d, err := s.fetchConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	d.CapturedAt = time.Now().Unix()
	d.CapturedBy = actor
	s.mu.Lock()
	s.decls[id] = d
	s.warned[id] = false
	s.state[id] = &driftState{NodeID: id, HasBaseline: true, CheckedAt: time.Now().Unix(), CapturedAt: d.CapturedAt, CapturedBy: actor}
	s.persistLocked()
	s.mu.Unlock()
	return d, nil
}

// ── rollback (BIRD only) ─────────────────────────────────────────────────────

func buildDriftRollbackScript(d *configDecl) string {
	birdB64 := base64.StdEncoding.EncodeToString([]byte(d.BirdConf))
	filtB64 := base64.StdEncoding.EncodeToString([]byte(d.Filters))
	return `set -uo pipefail
cd /etc/bird || exit 1
TS=$(date +%Y%m%d-%H%M%S)
cp bird.conf "bird.conf.ncn-driftbak.$TS"
cp filters_templates.conf "filters_templates.conf.ncn-driftbak.$TS" 2>/dev/null || true
echo '` + birdB64 + `' | base64 -d > bird.conf
echo '` + filtB64 + `' | base64 -d > filters_templates.conf
birdc configure soft > /tmp/ncn-drift-soft.log 2>&1
if grep -qiE "Reconfigured|Reconfiguration in progress" /tmp/ncn-drift-soft.log; then
  echo "RESULT OK reconfigured"
else
  echo "RESULT FAIL — rolling back"
  cp "bird.conf.ncn-driftbak.$TS" bird.conf
  cp "filters_templates.conf.ncn-driftbak.$TS" filters_templates.conf 2>/dev/null || true
  birdc configure soft >> /tmp/ncn-drift-soft.log 2>&1
  cat /tmp/ncn-drift-soft.log
  exit 1
fi`
}

func (s *driftStore) rollback(ctx context.Context, id string, onLine func(string)) (bool, error) {
	s.mu.Lock()
	decl := s.decls[id]
	s.mu.Unlock()
	if decl == nil {
		return false, fmt.Errorf("no baseline for %s", id)
	}
	rec, ok := s.fleet.registry.get(id)
	if !ok {
		return false, fmt.Errorf("unknown node %q", id)
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	okFlag := false
	exit, err := s.fleet.runMeshScriptOnNode(cctx, rec, buildDriftRollbackScript(decl), func(l string) {
		if strings.Contains(l, "RESULT OK") {
			okFlag = true
		}
		onLine(l)
	})
	if err != nil {
		return false, err
	}
	if exit != 0 {
		return false, nil
	}
	return okFlag, nil
}

// ── HTTP ─────────────────────────────────────────────────────────────────────

func (s *driftStore) view() []driftState {
	s.mu.Lock()
	defer s.mu.Unlock()
	var order []string
	if s.fleet != nil {
		for _, n := range s.fleet.nodesSnapshot() {
			order = append(order, n.ID)
		}
	}
	out := make([]driftState, 0, len(order))
	for _, id := range order {
		if st := s.state[id]; st != nil {
			out = append(out, *st)
		} else {
			out = append(out, driftState{NodeID: id, HasBaseline: s.decls[id] != nil})
		}
	}
	return out
}

// GET /api/v1/auth/drift → per-node drift status.
func handleDrift(w http.ResponseWriter, _ *http.Request) {
	if globalDrift == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "drift store not ready"})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"nodes": globalDrift.view()}})
}

// GET /api/v1/auth/nodes/{id}/config-diff → declared vs live content for review.
func handleConfigDiff(w http.ResponseWriter, r *http.Request) {
	if globalDrift == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "drift store not ready"})
		return
	}
	id := r.URL.Query().Get("node")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing node id"})
		return
	}
	globalDrift.mu.Lock()
	decl := globalDrift.decls[id]
	globalDrift.mu.Unlock()
	live, err := globalDrift.fetchConfig(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, envelope{OK: false, Error: err.Error()})
		return
	}
	declOut := &configDecl{}
	if decl != nil {
		declOut = decl
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"declared": declOut, "live": live}})
}

// POST /api/v1/auth/nodes/{id}/config-adopt → capture live config as baseline.
func handleConfigAdopt(w http.ResponseWriter, r *http.Request) {
	if globalDrift == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "drift store not ready"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	id := r.URL.Query().Get("node")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing node id"})
		return
	}
	d, err := globalDrift.adopt(r.Context(), id, adminOperator(r))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, envelope{OK: false, Error: err.Error()})
		return
	}
	auditRecord(r, AuditEvent{Event: "config.adopt-baseline", Severity: "info", Actor: adminOperator(r), Target: id, Outcome: "ok"})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"captured_at": d.CapturedAt}})
}

// POST /api/v1/auth/nodes/{id}/config-rollback {"confirm":"ROLLBACK CONFIG <id>"}
// → restore the BIRD baseline via birdc configure soft (auto-rollback on parse
// error). nft is NOT touched. Confirm-gated + audited.
func handleConfigRollback(w http.ResponseWriter, r *http.Request) {
	if globalDrift == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "drift store not ready"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	id := r.URL.Query().Get("node")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing node id"})
		return
	}
	var body struct {
		Confirm string `json:"confirm"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	want := "ROLLBACK CONFIG " + id
	if strings.TrimSpace(body.Confirm) != want {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "confirm mismatch — type: " + want})
		return
	}
	var logBuf strings.Builder
	ok, err := globalDrift.rollback(r.Context(), id, func(l string) { logBuf.WriteString(l); logBuf.WriteString("\n") })
	actor := adminOperator(r)
	if err != nil || !ok {
		reason := "rollback failed"
		if err != nil {
			reason = err.Error()
		}
		auditRecord(r, AuditEvent{Event: "config.rollback", Severity: "warn", Actor: actor, Target: id, Outcome: "fail", Details: map[string]any{"reason": reason}})
		recordOpFailure(globalNotify, &opFailure{Kind: "config-rollback", Target: id, Actor: actor, Reason: reason})
		writeJSON(w, http.StatusBadGateway, envelope{OK: false, Error: reason, Data: map[string]any{"log": logBuf.String()}})
		return
	}
	auditRecord(r, AuditEvent{Event: "config.rollback", Severity: "warn", Actor: actor, Target: id, Outcome: "ok"})
	// force an immediate re-check so the UI clears the drift badge.
	go globalDrift.checkNode(context.Background(), id)
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"log": logBuf.String()}})
}
