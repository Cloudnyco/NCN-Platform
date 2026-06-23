// anycast.go — anycast PoP drain / undrain (BGP withdrawal of an unhealthy node).
//
// A node announces the anycast aggregate to the world over its UPSTREAM eBGP
// sessions (named `upstream_*` by convention; `ibgp_*` is the internal mesh,
// never touched here). "Withdrawing anycast" from a node = `birdc disable` each
// upstream session, so traffic reroutes to healthy PoPs while the iBGP mesh and
// the box's own management reachability stay up. Fully reversible: undrain
// `birdc enable`s them again.
//
// Safety model (matches the rest of the console): the executor is admin-gated,
// confirm-token-guarded, audited, and REFUSES the local control node (ctrl-01) —
// draining it would cut the console's own transit. The AUTOMATIC path only
// PROPOSES a drain (a text notice to the error channel + a flag the console
// surfaces); a human approves the actual drain in the console. We deliberately
// do NOT put a one-click drain button in the broadcast channel.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const anycastUpstreamPrefix = "upstream_"

// protoNameRe guards protocol names before they reach a birdc command line
// (they originate from the scrape — semi-trusted — so never interpolate raw).
var protoNameRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// anycastUpstreams splits a node's upstream eBGP sessions into up / down by name
// convention. up = currently announcing (drain targets); down = not announcing.
func anycastUpstreams(protos []birdProtocol) (up, down []string) {
	// Non-nil so JSON is [] not null — a node with no upstream_* sessions would
	// otherwise serialize upstreams_up/down as null, crashing the BIRD page's
	// `.length`/`.join` (ErrorBoundary then blanks the whole Connectivity route).
	up, down = []string{}, []string{}
	for _, p := range protos {
		if p.Proto != "BGP" || !strings.HasPrefix(p.Name, anycastUpstreamPrefix) || !protoNameRe.MatchString(p.Name) {
			continue
		}
		if p.Healthy || strings.EqualFold(p.State, "up") {
			up = append(up, p.Name)
		} else {
			down = append(down, p.Name)
		}
	}
	return
}

// anycastDrainScript builds the birdc disable/enable script for the given
// upstream sessions (idempotent; birdc tolerates already-disabled/enabled).
func anycastDrainScript(upstreams []string, enable bool) string {
	verb := "disable"
	if enable {
		verb = "enable"
	}
	var b strings.Builder
	b.WriteString("set -e\n")
	for _, u := range upstreams {
		fmt.Fprintf(&b, "birdc %s %q\n", verb, u)
	}
	// Echo the resulting upstream state so the operator sees the outcome.
	b.WriteString("birdc show protocols 2>/dev/null | awk '$1 ~ /^upstream_/ {print \"  \"$1\" \"$4\" \"$6}'\n")
	return b.String()
}

type anycastStateView struct {
	NodeID       string   `json:"node_id"`
	Local        bool     `json:"local"`          // the control node — drain refused
	UpstreamsUp  []string `json:"upstreams_up"`   // sessions currently announcing
	UpstreamsDown []string `json:"upstreams_down"` // sessions not announcing
	Drained      bool     `json:"drained"`        // all upstreams down = withdrawn
	DrainScript   string  `json:"drain_script"`   // exactly what drain would run
	UndrainScript string  `json:"undrain_script"` // exactly what undrain would run
	Confirm       string  `json:"confirm"`        // confirm word the drain POST requires
}

// nodeProtocols returns a copy of a node's last-scraped BIRD protocols.
func (f *fleetScraper) nodeProtocols(id string) []birdProtocol {
	f.mu.RLock()
	defer f.mu.RUnlock()
	s := f.cache[id]
	if s == nil {
		return nil
	}
	return append([]birdProtocol(nil), s.Protocols...)
}

func anycastDrainConfirm(id string) string { return "DRAIN " + id }

// anycastState computes the current drain view for a node.
func (f *fleetScraper) anycastState(id string) anycastStateView {
	protos := f.nodeProtocols(id)
	up, down := anycastUpstreams(protos)
	all := append(append([]string{}, up...), down...)
	v := anycastStateView{
		NodeID:        id,
		Local:         id == f.localID,
		UpstreamsUp:   up,
		UpstreamsDown: down,
		Drained:       len(up) == 0 && len(down) > 0,
		Confirm:       anycastDrainConfirm(id),
	}
	if len(all) > 0 {
		v.DrainScript = anycastDrainScript(all, false)
		v.UndrainScript = anycastDrainScript(all, true)
	}
	return v
}

// anycastSetDrain disables (drain) or enables (undrain) every upstream session
// on a node. Refuses the local control node. Returns the streamed output.
func (f *fleetScraper) anycastSetDrain(id, actor string, enable bool) (string, error) {
	if id == f.localID {
		return "", fmt.Errorf("refusing to drain the local control node (%s)", id)
	}
	rec, ok := f.registry.get(id)
	if !ok {
		return "", fmt.Errorf("unknown node %q", id)
	}
	up, down := anycastUpstreams(f.nodeProtocols(id))
	targets := append(append([]string{}, up...), down...)
	if len(targets) == 0 {
		return "", fmt.Errorf("no upstream eBGP sessions found on %s (nothing to %s)", id, map[bool]string{false: "drain", true: "restore"}[enable])
	}
	script := anycastDrainScript(targets, enable)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out strings.Builder
	exit, err := f.runMeshScriptOnNode(ctx, rec, script, func(l string) { out.WriteString(l); out.WriteByte('\n') })
	verb := "drain"
	sev := auditSevWarn
	if enable {
		verb = "undrain"
		sev = auditSevInfo
	}
	if err != nil || exit != 0 {
		auditRecord(nil, AuditEvent{Event: "node.anycast-" + verb + ".fail", Severity: auditSevWarn, Actor: actor, Target: id,
			Details: map[string]any{"exit": exit, "err": fmt.Sprint(err)}})
		if err == nil {
			err = fmt.Errorf("birdc exited %d", exit)
		}
		return out.String(), err
	}
	auditRecord(nil, AuditEvent{Event: "node.anycast-" + verb, Severity: sev, Actor: actor, Target: id,
		Details: map[string]any{"upstreams": targets}})
	if f.notify != nil {
		emoji, title := "📤", "Anycast withdrawn (drained)"
		if enable {
			emoji, title = "📥", "Anycast restored (undrained)"
		}
		f.notify.NotifyEvent(emoji, title, []tgField{{"node", id}, {"upstreams", strings.Join(targets, ", ")}, {"by", actor}}, true)
	}
	return out.String(), nil
}

// ── HTTP (admin-only; wired in main.go) ──────────────────────────────────────

func (f *fleetScraper) handleNodeAnycast(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: f.anycastState(id)})
	default:
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
	}
}

func (f *fleetScraper) handleNodeAnycastDrain(w http.ResponseWriter, r *http.Request, id string, enable bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
		return
	}
	// Drain requires a typed confirm word (it withdraws live anycast); undrain
	// (restorative) does not.
	if !enable {
		var req struct {
			Confirm string `json:"confirm"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req)
		if req.Confirm != anycastDrainConfirm(id) {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "confirm word mismatch — type: " + anycastDrainConfirm(id)})
			return
		}
	}
	out, err := f.anycastSetDrain(id, adminOperator(r), enable)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, envelope{OK: false, Error: err.Error(), Data: out})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"output": out, "state": f.anycastState(id)}})
}

// ── auto-proposal watcher ────────────────────────────────────────────────────
//
// Detects the blackhole case — a node still announcing anycast (upstream eBGP
// up) while its own connectivity probes are failing, i.e. it attracts traffic it
// can't serve — and PROPOSES a drain. Per the safety model it never drains
// automatically: it posts a text recommendation to the error channel (no
// one-click button in chat) so an operator drains it from the console. Edge-
// triggered + debounced, mirroring replmon.

const (
	anycastCheckInterval = 60 * time.Second
	anycastBadStreak     = 2 // consecutive bad checks before proposing
)

type anycastWatcher struct {
	fleet  *fleetScraper
	notify *tgNotifier
	bad    map[string]int  // nodeID → consecutive blackhole-looking checks
	posted map[string]bool // nodeID → a proposal is currently outstanding
}

func newAnycastWatcher(f *fleetScraper, n *tgNotifier) *anycastWatcher {
	return &anycastWatcher{fleet: f, notify: n, bad: map[string]int{}, posted: map[string]bool{}}
}

func (m *anycastWatcher) Start(ctx context.Context) {
	if m.fleet == nil {
		return
	}
	go func() {
		time.Sleep(40 * time.Second) // let the first scrape settle
		t := time.NewTicker(anycastCheckInterval)
		defer t.Stop()
		m.check()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.check()
			}
		}
	}()
}

func (m *anycastWatcher) check() {
	for _, s := range m.fleet.snapshotNodes() {
		if s == nil || s.Node.ID == m.fleet.localID {
			continue // skip unscraped + the control node (never drained)
		}
		id := s.Node.ID
		up, _ := anycastUpstreams(s.Protocols)
		// Blackhole signal: still announcing (≥1 upstream up) but its own probes
		// are failing — it's pulling traffic it can't serve.
		probeFails := 0
		if ext := metricExtractors["probe_fail_count"]; ext != nil {
			if v, ok := ext(s); ok {
				probeFails = int(v)
			}
		}
		blackhole := len(up) > 0 && probeFails > 0
		if blackhole {
			m.bad[id]++
		} else {
			m.bad[id] = 0
		}
		switch {
		case m.bad[id] >= anycastBadStreak && !m.posted[id]:
			m.posted[id] = true
			m.propose(id, up, probeFails)
		case !blackhole && m.posted[id]:
			m.posted[id] = false // recovered; allow a future proposal
		}
	}
}

func (m *anycastWatcher) propose(id string, up []string, probeFails int) {
	if m.notify == nil {
		return
	}
	text := fmt.Sprintf("🕳️ <b>疑似 anycast 黑洞 · %s</b>\n仍在宣告 anycast(%d 个上游 up)但本机探针失败 %d 个 — 可能在吸引无法服务的流量。\n建议在控制台对 <code>%s</code> 执行 <b>anycast drain</b>(撤回宣告,流量回切到健康 PoP;可逆)。<i>不自动执行,需人工批准。</i>",
		id, len(up), probeFails, id)
	channel := m.notify.errorChat
	if channel == "" {
		channel = m.notify.chatID
	}
	m.notify.enqueue(tgPayload{ChatID: channel, Text: text}, "anycast-watch")
	auditRecord(nil, AuditEvent{Event: "node.anycast-blackhole-proposed", Severity: auditSevWarn, Actor: "system", Target: id,
		Details: map[string]any{"upstreams_up": up, "probe_fails": probeFails}})
}
