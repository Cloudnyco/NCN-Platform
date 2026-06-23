// Capacity planning — long-term metric trends + link-saturation forecast.
//
// The fleet scraper only keeps 15-min in-memory rings (monitor.go ringBuf),
// lost on every ncn-api restart, so there was no way to see week-over-week
// trends or answer "when does this link saturate?". This store samples the
// live fleet snapshot every minute, rolls each metric into a per-(node,metric,
// UTC-day) bucket (max / mean / p95), and persists those daily rows durably
// (Postgres capacity_series + a JSON file fallback, same dual-write posture as
// heartbeat.go). A periodic least-squares fit over the daily p95 series
// projects each node's busier traffic direction toward its configured link
// capacity and exposes a "days until saturation" figure — surfaced as the
// link_saturation_eta_days alert metric and the /admin/capacity page.
//
// Values are read through the existing metricExtractors whitelist (alertmetrics.go)
// so this stays consistent with the alert engine and adds no new field paths.

package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

var globalCapacity *capStore

const (
	capacitySeriesPath = incidentsDir + "/capacity_series.json"
	linkCapacityPath   = incidentsDir + "/link_capacity.json"

	capCaptureInterval  = 60 * time.Second    // sample the fleet this often
	capPersistInterval  = 10 * time.Minute    // upsert today's partial bucket this often
	capForecastInterval = time.Hour           // recompute saturation ETA this often
	capRetentionDays    = 400                 // prune daily rows older than this
	capForecastWindow   = 60                  // days of history used for the trend fit
	capForecastMinDays  = 7                   // need at least this many days to forecast
	capSaturatePct      = 0.90                // "saturated" = 90% of link capacity
)

// capMetrics is the fixed set of metrics we roll up for trends. Keys MUST exist
// in metricExtractors (alertmetrics.go) — values are pulled through it.
var capMetrics = []string{"net_rx_total_mbps", "net_tx_total_mbps", "cpu_pct", "mem_pct", "disk_pct"}

// capDay is one finalized per-(node,metric,day) rollup.
type capDay struct {
	Day     string  `json:"day"` // YYYY-MM-DD (UTC)
	Max     float64 `json:"max"`
	Mean    float64 `json:"mean"`
	P95     float64 `json:"p95"`
	Samples int     `json:"samples"`
}

// capAccum accumulates the in-progress (today) values for one (node,metric).
type capAccum struct {
	vals []float64 // every sample today (reset at UTC rollover; ~1440/day worst case)
}

type capStore struct {
	mu    sync.Mutex
	fleet *fleetScraper

	// rows: node -> metric -> day(YYYY-MM-DD) -> *capDay (durable, retained).
	rows map[string]map[string]map[string]*capDay
	// acc: node -> metric -> *capAccum (today's live samples).
	acc    map[string]map[string]*capAccum
	curDay string

	caps map[string]float64 // node -> link capacity Mbps (operator-set)
	eta  map[string]float64 // node -> forecast days-until-saturation (absent = unknown)
}

func newCapacityStore(fleet *fleetScraper) *capStore {
	s := &capStore{
		fleet:  fleet,
		rows:   map[string]map[string]map[string]*capDay{},
		acc:    map[string]map[string]*capAccum{},
		caps:   map[string]float64{},
		eta:    map[string]float64{},
		curDay: time.Now().UTC().Format("2006-01-02"),
	}
	s.loadCaps()
	s.loadRows()
	return s
}

// ── persistence: link capacities (config-doc, singleton) ────────────────────

func (s *capStore) loadCaps() {
	var doc []byte
	if globalDB != nil {
		if b, err := loadConfigDoc("link_capacity"); err == nil && b != nil {
			doc = b
		}
	}
	if doc == nil {
		if b, err := os.ReadFile(linkCapacityPath); err == nil && len(b) > 0 {
			doc = b
		}
	}
	if doc != nil {
		_ = json.Unmarshal(doc, &s.caps)
	}
}

func (s *capStore) persistCapsLocked() {
	b, err := json.Marshal(s.caps)
	if err != nil {
		return
	}
	_ = os.MkdirAll(incidentsDir, 0o700)
	tmp := linkCapacityPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		_ = os.Rename(tmp, linkCapacityPath)
	}
	if globalDB != nil {
		if err := saveConfigDoc("link_capacity", b); err != nil {
			log.Printf("capacity: db persist link_capacity failed (%v) — file is current", err)
		}
	}
}

// ── persistence: daily rows (DB primary, file fallback) ─────────────────────

func (s *capStore) loadRows() {
	if globalDB != nil {
		cutoff := time.Now().UTC().AddDate(0, 0, -capRetentionDays).Format("2006-01-02")
		rowsq, err := globalDB.Query(
			`SELECT node_id, metric, to_char(day,'YYYY-MM-DD'), maxv, meanv, p95v, samples
			   FROM capacity_series WHERE day >= $1`, cutoff)
		if err == nil {
			defer rowsq.Close()
			for rowsq.Next() {
				var node, metric, day string
				var d capDay
				if err := rowsq.Scan(&node, &metric, &day, &d.Max, &d.Mean, &d.P95, &d.Samples); err != nil {
					continue
				}
				d.Day = day
				s.putRowLocked(node, metric, &d)
			}
			return
		}
		log.Printf("capacity: db load failed (%v) — falling back to file", err)
	}
	if b, err := os.ReadFile(capacitySeriesPath); err == nil && len(b) > 0 {
		var saved map[string]map[string]map[string]*capDay
		if json.Unmarshal(b, &saved) == nil {
			s.rows = saved
		}
	}
}

func (s *capStore) putRowLocked(node, metric string, d *capDay) {
	if s.rows[node] == nil {
		s.rows[node] = map[string]map[string]*capDay{}
	}
	if s.rows[node][metric] == nil {
		s.rows[node][metric] = map[string]*capDay{}
	}
	s.rows[node][metric][d.Day] = d
}

// persistRow upserts one finalized daily row (DB) and, when DB is absent,
// the whole rows map is dumped to the JSON file fallback instead.
func (s *capStore) persistRowLocked(node, metric string, d *capDay) {
	if globalDB != nil {
		_, err := globalDB.Exec(
			`INSERT INTO capacity_series (node_id, metric, day, maxv, meanv, p95v, samples)
			 VALUES ($1,$2,$3,$4,$5,$6,$7)
			 ON CONFLICT (node_id, metric, day)
			 DO UPDATE SET maxv=EXCLUDED.maxv, meanv=EXCLUDED.meanv, p95v=EXCLUDED.p95v, samples=EXCLUDED.samples`,
			node, metric, d.Day, d.Max, d.Mean, d.P95, d.Samples)
		if err != nil {
			log.Printf("capacity: db upsert failed (%v)", err)
		}
		return
	}
	s.dumpFileLocked()
}

func (s *capStore) dumpFileLocked() {
	b, err := json.Marshal(s.rows)
	if err != nil {
		return
	}
	_ = os.MkdirAll(incidentsDir, 0o700)
	tmp := capacitySeriesPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		_ = os.Rename(tmp, capacitySeriesPath)
	}
}

// ── sampling + rollover ─────────────────────────────────────────────────────

func (s *capStore) Start(ctx context.Context) {
	go func() {
		capt := time.NewTicker(capCaptureInterval)
		persist := time.NewTicker(capPersistInterval)
		forecast := time.NewTicker(capForecastInterval)
		defer capt.Stop()
		defer persist.Stop()
		defer forecast.Stop()
		s.sample()            // immediate first sample
		s.recomputeForecast() // forecast from whatever history we loaded
		for {
			select {
			case <-ctx.Done():
				s.flushToday()
				return
			case <-capt.C:
				s.sample()
			case <-persist.C:
				s.flushToday()
			case <-forecast.C:
				s.recomputeForecast()
			}
		}
	}()
}

// sample reads the live fleet snapshot and folds each tracked metric into
// today's accumulator, handling UTC day rollover (flush + reset).
func (s *capStore) sample() {
	if s.fleet == nil {
		return
	}
	nodes := s.fleet.snapshotNodes()
	now := time.Now().UTC().Format("2006-01-02")

	s.mu.Lock()
	defer s.mu.Unlock()
	if now != s.curDay {
		s.flushDayLocked(s.curDay) // finalize yesterday
		s.acc = map[string]map[string]*capAccum{}
		s.curDay = now
		s.pruneLocked()
	}
	for _, n := range nodes {
		if n == nil || !n.OK {
			continue
		}
		id := n.Node.ID
		for _, m := range capMetrics {
			ex, ok := metricExtractors[m]
			if !ok {
				continue
			}
			v, ok := ex(n)
			if !ok {
				continue
			}
			if s.acc[id] == nil {
				s.acc[id] = map[string]*capAccum{}
			}
			a := s.acc[id][m]
			if a == nil {
				a = &capAccum{}
				s.acc[id][m] = a
			}
			a.vals = append(a.vals, v)
		}
	}
}

// flushToday finalizes the in-progress day into rows (without resetting acc),
// so a restart keeps today's partial trend.
func (s *capStore) flushToday() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushDayLocked(s.curDay)
}

func (s *capStore) flushDayLocked(day string) {
	for node, metrics := range s.acc {
		for metric, a := range metrics {
			if len(a.vals) == 0 {
				continue
			}
			d := summarize(day, a.vals)
			s.putRowLocked(node, metric, d)
			s.persistRowLocked(node, metric, d)
		}
	}
}

func (s *capStore) pruneLocked() {
	cutoff := time.Now().UTC().AddDate(0, 0, -capRetentionDays).Format("2006-01-02")
	for _, metrics := range s.rows {
		for _, days := range metrics {
			for day := range days {
				if day < cutoff {
					delete(days, day)
				}
			}
		}
	}
}

func summarize(day string, vals []float64) *capDay {
	d := &capDay{Day: day, Samples: len(vals)}
	if len(vals) == 0 {
		return d
	}
	var sum float64
	cp := make([]float64, len(vals))
	for i, v := range vals {
		sum += v
		cp[i] = v
		if v > d.Max {
			d.Max = v
		}
	}
	d.Mean = sum / float64(len(vals))
	sort.Float64s(cp)
	idx := int(math.Ceil(0.95*float64(len(cp)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	d.P95 = cp[idx]
	return d
}

// ── forecast ────────────────────────────────────────────────────────────────

// recomputeForecast fits a least-squares line over the last capForecastWindow
// days of the busier traffic direction's p95 and projects days-until-saturation
// (90% of the configured link capacity). Only nodes with a known capacity, an
// upward slope, and ≥ capForecastMinDays of history get an ETA.
func (s *capStore) recomputeForecast() {
	s.mu.Lock()
	defer s.mu.Unlock()
	eta := map[string]float64{}
	for node, metrics := range s.rows {
		capMbps := s.caps[node]
		if capMbps <= 0 {
			continue
		}
		// busier direction = whichever of rx/tx has the higher latest p95.
		dir := "net_rx_total_mbps"
		if p95Latest(metrics["net_tx_total_mbps"]) > p95Latest(metrics["net_rx_total_mbps"]) {
			dir = "net_tx_total_mbps"
		}
		days := metrics[dir]
		if len(days) < capForecastMinDays {
			continue
		}
		slope, latest, ok := trendSlope(days, capForecastWindow)
		if !ok || slope <= 0 {
			continue
		}
		target := capSaturatePct * capMbps
		if latest >= target {
			eta[node] = 0 // already saturated
			continue
		}
		eta[node] = (target - latest) / slope
	}
	s.eta = eta
}

func p95Latest(days map[string]*capDay) float64 {
	if days == nil {
		return 0
	}
	var keys []string
	for k := range days {
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return 0
	}
	sort.Strings(keys)
	return days[keys[len(keys)-1]].P95
}

// trendSlope fits y = a + b*x over the last `window` days (x = day index 0..n-1)
// using the daily p95. Returns (slope Mbps/day, latest p95, ok).
func trendSlope(days map[string]*capDay, window int) (float64, float64, bool) {
	var keys []string
	for k := range days {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > window {
		keys = keys[len(keys)-window:]
	}
	n := len(keys)
	if n < 2 {
		return 0, 0, false
	}
	var sx, sy, sxx, sxy float64
	for i, k := range keys {
		x := float64(i)
		y := days[k].P95
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	fn := float64(n)
	denom := fn*sxx - sx*sx
	if denom == 0 {
		return 0, 0, false
	}
	slope := (fn*sxy - sx*sy) / denom
	return slope, days[keys[n-1]].P95, true
}

// etaDays exposes the forecast to the alert metric extractor. ok=false when no
// forecast exists (unknown capacity / not enough history / no upward trend) so
// the rule stays quiet rather than firing on a bogus 0.
func (s *capStore) etaDays(node string) (float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.eta[node]
	return v, ok
}

// ── HTTP ─────────────────────────────────────────────────────────────────────

type capNodeView struct {
	Node        string              `json:"node"`
	CapacityMbps float64            `json:"capacity_mbps"`
	EtaDays     float64             `json:"eta_days"`     // -1 = unknown
	Series      map[string][]capDay `json:"series"`        // metric -> oldest→newest
}

func (s *capStore) view() []capNodeView {
	s.mu.Lock()
	defer s.mu.Unlock()
	// stable node order from the fleet
	var order []string
	seen := map[string]bool{}
	if s.fleet != nil {
		for _, n := range s.fleet.nodesSnapshot() {
			order = append(order, n.ID)
			seen[n.ID] = true
		}
	}
	for node := range s.rows {
		if !seen[node] {
			order = append(order, node)
			seen[node] = true
		}
	}
	out := make([]capNodeView, 0, len(order))
	for _, node := range order {
		metrics := s.rows[node]
		nv := capNodeView{Node: node, CapacityMbps: s.caps[node], EtaDays: -1, Series: map[string][]capDay{}}
		if v, ok := s.eta[node]; ok {
			nv.EtaDays = v
		}
		for _, m := range capMetrics {
			days := metrics[m]
			if len(days) == 0 {
				continue
			}
			var keys []string
			for k := range days {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			series := make([]capDay, 0, len(keys))
			for _, k := range keys {
				series = append(series, *days[k])
			}
			nv.Series[m] = series
		}
		out = append(out, nv)
	}
	return out
}

// GET /api/v1/auth/capacity → per-node daily trends + capacity + saturation ETA.
func handleCapacity(w http.ResponseWriter, _ *http.Request) {
	if globalCapacity == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "capacity store not ready"})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"nodes":   globalCapacity.view(),
		"metrics": capMetrics,
	}})
}

// POST /api/v1/auth/capacity/link {"node":"ctrl-01","mbps":1000} → set a node's
// link capacity (drives the saturation forecast). mbps<=0 clears it.
func handleCapacitySetLink(w http.ResponseWriter, r *http.Request) {
	if globalCapacity == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "capacity store not ready"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var body struct {
		Node string  `json:"node"`
		Mbps float64 `json:"mbps"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || body.Node == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad body"})
		return
	}
	globalCapacity.mu.Lock()
	if body.Mbps > 0 {
		globalCapacity.caps[body.Node] = body.Mbps
	} else {
		delete(globalCapacity.caps, body.Node)
	}
	globalCapacity.persistCapsLocked()
	globalCapacity.mu.Unlock()
	globalCapacity.recomputeForecast()
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"nodes": globalCapacity.view()}})
}
