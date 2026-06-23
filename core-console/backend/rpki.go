// rpki.go — Phase 1 of completing RPKI: outside-in monitoring of whether
// AS64500's OWN announced prefixes are RPKI-valid. pop-01 runs Krill (we
// PUBLISH ROAs); this watches, from the public RPKI data, that each prefix we
// announce is actually covered by a valid ROA — surfaces it in the console and
// alerts on invalid / missing-ROA. Pure read of public data (RIPEstat). Does
// NOT touch any router or the Krill box. (Phase 2/3 = a validator + BIRD RTR.)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	rpkiDefaultRefresh = 24 * time.Hour     // default auto-poll; RPKI changes slowly. Operator-adjustable in the console.
	rpkiMinRefresh     = 5 * time.Minute    // floor — RIPEstat is public + rate-limited, don't hammer it
	rpkiMaxRefresh     = 7 * 24 * time.Hour // ceiling
	// rpkiSettingsPath persists the operator's chosen interval (durable file
	// fallback; also dual-written to Postgres when available — see persistInterval).
	rpkiSettingsPath = incidentsDir + "/rpki_settings.json"
)

// rpkiEnvDefault is the starting interval before any persisted operator choice:
// NCN_RPKI_REFRESH (a Go duration like "6h") if set + valid, else the default.
func rpkiEnvDefault() time.Duration {
	if s := strings.TrimSpace(os.Getenv("NCN_RPKI_REFRESH")); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			return clampRefresh(d)
		}
	}
	return rpkiDefaultRefresh
}

// clampRefresh keeps an interval within [floor, ceiling].
func clampRefresh(d time.Duration) time.Duration {
	if d < rpkiMinRefresh {
		return rpkiMinRefresh
	}
	if d > rpkiMaxRefresh {
		return rpkiMaxRefresh
	}
	return d
}

var globalRPKI *rpkiMonitor

type rpkiPrefix struct {
	Prefix   string `json:"prefix"`
	Validity string `json:"validity"` // valid | invalid | unknown
	ROAs     int    `json:"roas"`
}

// rovState is the LIVE route-origin-validation result read from a PoP's BIRD
// (the soft-check tags) — distinct from the "our own prefixes" view above.
type rovState struct {
	Node        string `json:"node"`
	Established bool   `json:"established"`
	VRPs        int    `json:"vrps"`
	Valid       int    `json:"valid"`
	Invalid     int    `json:"invalid"`
	Unknown     int    `json:"unknown"`
}

type rpkiState struct {
	ASN       string       `json:"asn"`
	CheckedAt int64        `json:"checked_at"`
	Prefixes  []rpkiPrefix `json:"prefixes"`
	Valid     int          `json:"valid"`
	Invalid   int          `json:"invalid"`
	Unknown   int          `json:"unknown"`
	ROV       *rovState    `json:"rov,omitempty"`        // live ROV from a PoP running soft-check
	IntervalSecs int64     `json:"interval_secs"`        // current auto-poll interval (operator-adjustable)
	Err       string       `json:"error,omitempty"`
}

type rpkiMonitor struct {
	asn      string // numeric, no "AS" prefix
	client   *http.Client
	notify   *tgNotifier
	fleet    *fleetScraper // to read live ROV tag counts off a PoP's BIRD
	rovNode  string        // node id running the soft-check (e.g. "pop-01")
	mu       sync.RWMutex
	state    rpkiState
	interval time.Duration // current auto-poll interval (guarded by mu)
	lastWarn string        // dedup the problem-prefix alert

	resetTick chan struct{} // signals Start's ticker to adopt a changed interval
}

func newRPKIMonitor(asn string, notify *tgNotifier, fleet *fleetScraper, rovNode string) *rpkiMonitor {
	asn = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(asn)), "AS")
	if asn == "" {
		asn = "64500"
	}
	m := &rpkiMonitor{
		asn:       asn,
		notify:    notify,
		fleet:     fleet,
		rovNode:   strings.TrimSpace(rovNode),
		client:    &http.Client{Timeout: 12 * time.Second},
		interval:  rpkiEnvDefault(),
		resetTick: make(chan struct{}, 1),
		state:     rpkiState{ASN: "AS" + asn, Prefixes: []rpkiPrefix{}},
	}
	// A persisted operator choice (last set in the console) overrides the env default.
	if d, ok := m.loadInterval(); ok {
		m.interval = clampRefresh(d)
	}
	return m
}

// loadInterval reads the persisted poll interval — Postgres first (post-cutover),
// else the JSON file. Returns (0,false) when nothing is stored yet.
func (m *rpkiMonitor) loadInterval() (time.Duration, bool) {
	var doc []byte
	if globalDB != nil {
		if b, err := loadConfigDoc("rpki_settings"); err == nil && b != nil {
			doc = b
		}
	}
	if doc == nil {
		if b, err := os.ReadFile(rpkiSettingsPath); err == nil && len(b) > 0 {
			doc = b
		}
	}
	if doc == nil {
		return 0, false
	}
	var s struct {
		IntervalSecs int64 `json:"interval_secs"`
	}
	if err := json.Unmarshal(doc, &s); err != nil || s.IntervalSecs <= 0 {
		return 0, false
	}
	return time.Duration(s.IntervalSecs) * time.Second, true
}

// getInterval returns the current auto-poll interval.
func (m *rpkiMonitor) getInterval() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.interval
}

// setInterval changes the auto-poll interval (clamped), persists it, and signals
// the running ticker to adopt it immediately. Returns the effective value.
func (m *rpkiMonitor) setInterval(d time.Duration) time.Duration {
	d = clampRefresh(d)
	m.mu.Lock()
	m.interval = d
	m.mu.Unlock()
	m.persistInterval(d)
	select {
	case m.resetTick <- struct{}{}: // non-blocking: a pending reset already covers it
	default:
	}
	return d
}

// persistInterval dual-writes the chosen interval: durable JSON file (the
// globalDB==nil path + backup) and Postgres when available. Mirrors heartbeat.go.
func (m *rpkiMonitor) persistInterval(d time.Duration) {
	b, err := json.Marshal(struct {
		IntervalSecs int64 `json:"interval_secs"`
	}{int64(d / time.Second)})
	if err != nil {
		return
	}
	_ = os.MkdirAll(incidentsDir, 0o700)
	tmp := rpkiSettingsPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		_ = os.Rename(tmp, rpkiSettingsPath)
	} else {
		log.Printf("rpki: persist interval file failed: %v", err)
	}
	if globalDB != nil {
		if err := saveConfigDoc("rpki_settings", b); err != nil {
			log.Printf("rpki: db persist interval failed (%v) — file is current", err)
		}
	}
}

// readROV runs the soft-check tag counters on the ROV node's BIRD (via the
// fleet-key SSH runner) and returns the live valid/invalid/unknown split.
// Best-effort: any failure → nil (the panel just omits the ROV section).
func (m *rpkiMonitor) readROV(ctx context.Context) *rovState {
	if m.fleet == nil || m.rovNode == "" {
		return nil
	}
	rec, ok := m.fleet.registry.get(m.rovNode)
	if !ok {
		return nil
	}
	// CF RTR scheme (rpki_cloudflare protocol → roa6_cloudflare table), enforcing
	// in the import filter: ROA_INVALID is rejected (so it won't appear in the
	// table → INVALID counts ~0, which is correct), ROA_UNKNOWN is accepted with
	// lower local-pref. Counts use inline roa_check (no community tags anymore).
	script := `S=$(birdc show protocols rpki_cloudflare 2>/dev/null | grep -oE 'Established|Connect|Down' | head -1); echo "STATE ${S:-none}"
echo "VRPS $(birdc show route table roa6_cloudflare count 2>/dev/null | grep -oE '[0-9]+ of' | head -1 | grep -oE '[0-9]+')"
n(){ birdc show route where "roa_check(roa6_cloudflare, net, bgp_path.last) = $1" count 2>/dev/null | grep -oE '[0-9]+ of' | head -1 | grep -oE '[0-9]+'; }
echo "VALID $(n ROA_VALID)"
echo "INVALID $(n ROA_INVALID)"
echo "UNKNOWN $(n ROA_UNKNOWN)"`
	var sb strings.Builder
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := m.fleet.runMeshScriptOnNode(cctx, rec, script, func(l string) { sb.WriteString(l); sb.WriteString("\n") }); err != nil {
		return nil
	}
	rov := &rovState{Node: m.rovNode}
	atoi := func(s string) int { n := 0; fmt.Sscanf(strings.TrimSpace(s), "%d", &n); return n }
	for _, line := range strings.Split(sb.String(), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "STATE":
			rov.Established = f[1] == "Established"
		case "VRPS":
			rov.VRPs = atoi(f[1])
		case "VALID":
			rov.Valid = atoi(f[1])
		case "INVALID":
			rov.Invalid = atoi(f[1])
		case "UNKNOWN":
			rov.Unknown = atoi(f[1])
		}
	}
	return rov
}

func (m *rpkiMonitor) Start(ctx context.Context) {
	go func() {
		m.refresh(ctx)
		t := time.NewTicker(m.getInterval())
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.refresh(ctx)
			case <-m.resetTick: // interval changed in the console → adopt it now
				t.Reset(m.getInterval())
			}
		}
	}()
}

func (m *rpkiMonitor) snapshot() rpkiState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st := m.state
	st.IntervalSecs = int64(m.interval / time.Second)
	return st
}

func (m *rpkiMonitor) getJSON(ctx context.Context, u string, v any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ripestat %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

// announced returns the prefixes AS<n> currently originates (per RIPEstat).
func (m *rpkiMonitor) announced(ctx context.Context) ([]string, error) {
	u := "https://stat.ripe.net/data/announced-prefixes/data.json?sourceapp=ncn-console&resource=AS" + m.asn
	var body struct {
		Data struct {
			Prefixes []struct {
				Prefix string `json:"prefix"`
			} `json:"prefixes"`
		} `json:"data"`
	}
	if err := m.getJSON(ctx, u, &body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.Data.Prefixes))
	for _, p := range body.Data.Prefixes {
		if p.Prefix != "" {
			out = append(out, p.Prefix)
		}
	}
	return out, nil
}

// validity returns valid|invalid|unknown + the ROA count for one prefix+origin.
func (m *rpkiMonitor) validity(ctx context.Context, prefix string) (string, int) {
	u := "https://stat.ripe.net/data/rpki-validation/data.json?sourceapp=ncn-console&resource=AS" + m.asn + "&prefix=" + url.QueryEscape(prefix)
	var body struct {
		Data struct {
			Status         string `json:"status"`
			ValidatingROAs []struct {
				Validity string `json:"validity"`
			} `json:"validating_roas"`
		} `json:"data"`
	}
	if err := m.getJSON(ctx, u, &body); err != nil {
		return "unknown", 0
	}
	st := strings.ToLower(body.Data.Status)
	switch {
	case strings.Contains(st, "invalid"):
		st = "invalid"
	case strings.Contains(st, "valid"):
		st = "valid"
	default:
		st = "unknown" // RIPEstat uses "unknown" / "not-found" when no ROA covers it
	}
	return st, len(body.Data.ValidatingROAs)
}

func (m *rpkiMonitor) refresh(ctx context.Context) {
	st := rpkiState{ASN: "AS" + m.asn, CheckedAt: time.Now().Unix(), Prefixes: []rpkiPrefix{}}
	prefixes, err := m.announced(ctx)
	if err != nil {
		st.Err = err.Error()
		m.mu.Lock()
		m.state = st
		m.mu.Unlock()
		return
	}
	if len(prefixes) > 100 { // safety cap on fan-out
		prefixes = prefixes[:100]
	}
	for _, p := range prefixes {
		v, roas := m.validity(ctx, p)
		st.Prefixes = append(st.Prefixes, rpkiPrefix{Prefix: p, Validity: v, ROAs: roas})
		switch v {
		case "valid":
			st.Valid++
		case "invalid":
			st.Invalid++
		default:
			st.Unknown++
		}
	}
	sort.Slice(st.Prefixes, func(i, j int) bool { return st.Prefixes[i].Prefix < st.Prefixes[j].Prefix })
	st.ROV = m.readROV(ctx) // live route-origin-validation from the soft-check PoP
	m.mu.Lock()
	m.state = st
	m.mu.Unlock()
	m.maybeAlert(st)
}

// maybeAlert posts to the ops group when an announced prefix is invalid or has
// no covering ROA. Deduped on the set of problem prefixes so it fires on change
// (and recovery → empty set re-arms), not every hour.
func (m *rpkiMonitor) maybeAlert(st rpkiState) {
	if m.notify == nil {
		return
	}
	var probs []string
	for _, p := range st.Prefixes {
		if p.Validity != "valid" {
			probs = append(probs, p.Prefix+":"+p.Validity)
		}
	}
	key := strings.Join(probs, ",")
	if key == m.lastWarn {
		return
	}
	m.lastWarn = key
	if len(probs) == 0 {
		return // recovered (or nothing wrong) — state re-armed, no message
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🛡️ <b>RPKI</b> — %s\n%d announced prefix(es) not valid:", html.EscapeString(st.ASN), len(probs))
	shown := 0
	for _, p := range st.Prefixes {
		if p.Validity == "valid" {
			continue
		}
		if shown >= 15 {
			fmt.Fprintf(&b, "\n… +%d more", len(probs)-shown)
			break
		}
		emoji := "❔"
		if p.Validity == "invalid" {
			emoji = "⛔"
		}
		fmt.Fprintf(&b, "\n%s <code>%s</code> · %s", emoji, html.EscapeString(p.Prefix), p.Validity)
		shown++
	}
	b.WriteString("\n<blockquote>invalid = wrong/over-broad ROA · unknown = no ROA. Fix in Krill (pop-01).</blockquote>")
	// Route to the dedicated error channel (same home as op-failures + crit
	// alerts), falling back to the group when no channel is configured.
	channel := m.notify.errorChat
	if channel == "" {
		channel = m.notify.chatID
	}
	m.notify.enqueue(tgPayload{ChatID: channel, Text: b.String()}, "rpki-alert")
}

// GET /api/v1/auth/rpki → current RPKI validity of our announced prefixes.
func handleRPKI(w http.ResponseWriter, r *http.Request) {
	if globalRPKI == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "rpki monitor not ready"})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: globalRPKI.snapshot()})
}

// POST /api/v1/auth/rpki/refresh → force an immediate refresh (the auto-poll
// interval is operator-adjustable, default daily) and return the fresh snapshot.
// refresh() is mutex-safe, so calling it alongside the background ticker is fine.
func handleRPKIRefresh(w http.ResponseWriter, r *http.Request) {
	if globalRPKI == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "rpki monitor not ready"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	globalRPKI.refresh(ctx)
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: globalRPKI.snapshot()})
}

// POST /api/v1/auth/rpki/interval {"seconds":N} → set the auto-poll interval.
// Clamped to [5m, 7d], persisted, and adopted by the running ticker immediately.
// Returns the fresh snapshot (carrying the effective interval_secs).
func handleRPKIInterval(w http.ResponseWriter, r *http.Request) {
	if globalRPKI == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "rpki monitor not ready"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var body struct {
		Seconds int64 `json:"seconds"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad body"})
		return
	}
	if body.Seconds <= 0 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "seconds must be > 0"})
		return
	}
	globalRPKI.setInterval(time.Duration(body.Seconds) * time.Second)
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: globalRPKI.snapshot()})
}
