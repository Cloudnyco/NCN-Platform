// Active SLA probing — real per-PoP availability / loss / latency SLOs.
//
// heartbeat.go answers "is each PoP up?"; this answers "from each PoP, how well
// can we reach the targets we promise an SLO on?". Operator-defined SLA targets
// (sla_targets) are folded into every PoP's probe list (fleet.go probeTargetsFor,
// prefixed "sla:") so they ride the existing agent ping pipeline. This store
// samples those probe results every minute, rolls them into per-(pop,target,
// UTC-day) buckets (sent / ok / rtt), and persists them durably (Postgres
// sla_history + JSON file fallback — same posture as heartbeat.go / capacity.go).
// The status page reads the rolled-up availability%/loss%/latency; the alert
// engine reads short-window loss/RTT through sla_loss_pct / sla_rtt_ms.

package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var globalSLA *slaStore

const (
	slaTargetsPath = incidentsDir + "/sla_targets.json"
	slaHistoryPath = incidentsDir + "/sla_history.json"

	slaSampleInterval  = 60 * time.Second
	slaPersistInterval = 10 * time.Minute
	slaRetentionDays   = 400
	slaWindowDays      = 30 // availability window shown on the status page
	slaLossWindow      = 20 // probe-series samples (~5 min) for the alert extractor
)

var slaNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

const slaProbePrefix = "sla:"

type slaTarget struct {
	Name        string  `json:"name"`
	Target      string  `json:"target"`
	Type        string  `json:"type"` // ping4 | ping6
	SLOPct      float64 `json:"slo_pct"`
	RTTBudgetMs float64 `json:"rtt_budget_ms"`
}

type slaDay struct {
	Day    string  `json:"day"`
	Sent   int     `json:"sent"`
	OK     int     `json:"ok"`
	RTTSum float64 `json:"rtt_sum_ms"`
	RTTMax float64 `json:"rtt_max_ms"`
}

type slaAccum struct {
	sent, ok       int
	rttSum, rttMax float64
}

type slaStore struct {
	mu      sync.Mutex
	fleet   *fleetScraper
	targets []slaTarget
	rows    map[string]map[string]map[string]*slaDay // pop -> target -> day -> *slaDay
	acc     map[string]map[string]*slaAccum
	curDay  string
}

func newSLAStore(fleet *fleetScraper) *slaStore {
	s := &slaStore{
		fleet:  fleet,
		rows:   map[string]map[string]map[string]*slaDay{},
		acc:    map[string]map[string]*slaAccum{},
		curDay: time.Now().UTC().Format("2006-01-02"),
	}
	s.loadTargets()
	s.loadRows()
	return s
}

// slaProbeTargets is read by fleet.probeTargetsFor to inject the SLA targets
// into every PoP's probe set. nil-safe before the store is constructed.
func slaProbeTargets() []probeTarget {
	if globalSLA == nil {
		return nil
	}
	globalSLA.mu.Lock()
	defer globalSLA.mu.Unlock()
	out := make([]probeTarget, 0, len(globalSLA.targets))
	for _, t := range globalSLA.targets {
		typ := "ping4"
		if t.Type == "ping6" {
			typ = "ping6"
		}
		out = append(out, probeTarget{Name: slaProbePrefix + t.Name, Target: t.Target, Type: typ})
	}
	return out
}

// ── persistence: targets (config-doc singleton) ─────────────────────────────

func (s *slaStore) loadTargets() {
	var doc []byte
	if globalDB != nil {
		if b, err := loadConfigDoc("sla_targets"); err == nil && b != nil {
			doc = b
		}
	}
	if doc == nil {
		if b, err := os.ReadFile(slaTargetsPath); err == nil && len(b) > 0 {
			doc = b
		}
	}
	if doc != nil {
		_ = json.Unmarshal(doc, &s.targets)
	}
}

func (s *slaStore) persistTargetsLocked() {
	b, err := json.Marshal(s.targets)
	if err != nil {
		return
	}
	writeFileAtomic(slaTargetsPath, b)
	if globalDB != nil {
		if err := saveConfigDoc("sla_targets", b); err != nil {
			log.Printf("sla: db persist sla_targets failed (%v) — file is current", err)
		}
	}
}

// ── persistence: history rows (DB primary, file fallback) ───────────────────

func (s *slaStore) loadRows() {
	if globalDB != nil {
		cutoff := time.Now().UTC().AddDate(0, 0, -slaRetentionDays).Format("2006-01-02")
		q, err := globalDB.Query(
			`SELECT pop_id, target, to_char(day,'YYYY-MM-DD'), sent, ok_count, rtt_sum_ms, rtt_max_ms
			   FROM sla_history WHERE day >= $1`, cutoff)
		if err == nil {
			defer q.Close()
			for q.Next() {
				var pop, target, day string
				var d slaDay
				if err := q.Scan(&pop, &target, &day, &d.Sent, &d.OK, &d.RTTSum, &d.RTTMax); err != nil {
					continue
				}
				d.Day = day
				s.putRowLocked(pop, target, &d)
			}
			return
		}
		log.Printf("sla: db load failed (%v) — falling back to file", err)
	}
	if b, err := os.ReadFile(slaHistoryPath); err == nil && len(b) > 0 {
		var saved map[string]map[string]map[string]*slaDay
		if json.Unmarshal(b, &saved) == nil {
			s.rows = saved
		}
	}
}

func (s *slaStore) putRowLocked(pop, target string, d *slaDay) {
	if s.rows[pop] == nil {
		s.rows[pop] = map[string]map[string]*slaDay{}
	}
	if s.rows[pop][target] == nil {
		s.rows[pop][target] = map[string]*slaDay{}
	}
	s.rows[pop][target][d.Day] = d
}

func (s *slaStore) persistRowLocked(pop, target string, d *slaDay) {
	if globalDB != nil {
		_, err := globalDB.Exec(
			`INSERT INTO sla_history (pop_id, target, day, sent, ok_count, rtt_sum_ms, rtt_max_ms)
			 VALUES ($1,$2,$3,$4,$5,$6,$7)
			 ON CONFLICT (pop_id, target, day)
			 DO UPDATE SET sent=EXCLUDED.sent, ok_count=EXCLUDED.ok_count, rtt_sum_ms=EXCLUDED.rtt_sum_ms, rtt_max_ms=EXCLUDED.rtt_max_ms`,
			pop, target, d.Day, d.Sent, d.OK, d.RTTSum, d.RTTMax)
		if err != nil {
			log.Printf("sla: db upsert failed (%v)", err)
		}
		return
	}
	if b, err := json.Marshal(s.rows); err == nil {
		writeFileAtomic(slaHistoryPath, b)
	}
}

// ── sampling ────────────────────────────────────────────────────────────────

func (s *slaStore) Start(ctx context.Context) {
	go func() {
		samp := time.NewTicker(slaSampleInterval)
		persist := time.NewTicker(slaPersistInterval)
		defer samp.Stop()
		defer persist.Stop()
		s.sample()
		for {
			select {
			case <-ctx.Done():
				s.flushToday()
				return
			case <-samp.C:
				s.sample()
			case <-persist.C:
				s.flushToday()
			}
		}
	}()
}

func (s *slaStore) sample() {
	if s.fleet == nil {
		return
	}
	nodes := s.fleet.snapshotNodes()
	now := time.Now().UTC().Format("2006-01-02")

	s.mu.Lock()
	defer s.mu.Unlock()
	if now != s.curDay {
		s.flushDayLocked(s.curDay)
		s.acc = map[string]map[string]*slaAccum{}
		s.curDay = now
		s.pruneLocked()
	}
	for _, n := range nodes {
		if n == nil {
			continue
		}
		pop := n.Node.ID
		for _, p := range n.Probes {
			if !strings.HasPrefix(p.Name, slaProbePrefix) || p.LastTime == 0 {
				continue
			}
			target := strings.TrimPrefix(p.Name, slaProbePrefix)
			if s.acc[pop] == nil {
				s.acc[pop] = map[string]*slaAccum{}
			}
			a := s.acc[pop][target]
			if a == nil {
				a = &slaAccum{}
				s.acc[pop][target] = a
			}
			a.sent++
			if p.LastOK {
				a.ok++
				a.rttSum += p.LastMS
				if p.LastMS > a.rttMax {
					a.rttMax = p.LastMS
				}
			}
		}
	}
}

func (s *slaStore) flushToday() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushDayLocked(s.curDay)
}

func (s *slaStore) flushDayLocked(day string) {
	for pop, targets := range s.acc {
		for target, a := range targets {
			if a.sent == 0 {
				continue
			}
			d := &slaDay{Day: day, Sent: a.sent, OK: a.ok, RTTSum: a.rttSum, RTTMax: a.rttMax}
			s.putRowLocked(pop, target, d)
			s.persistRowLocked(pop, target, d)
		}
	}
}

func (s *slaStore) pruneLocked() {
	cutoff := time.Now().UTC().AddDate(0, 0, -slaRetentionDays).Format("2006-01-02")
	for _, targets := range s.rows {
		for _, days := range targets {
			for day := range days {
				if day < cutoff {
					delete(days, day)
				}
			}
		}
	}
}

// ── view ─────────────────────────────────────────────────────────────────────

type slaPopStat struct {
	Pop          string  `json:"pop"`
	Sent         int     `json:"sent"`
	OK           int     `json:"ok"`
	AvailPct     float64 `json:"avail_pct"`
	LossPct      float64 `json:"loss_pct"`
	MeanRTTMs    float64 `json:"mean_rtt_ms"`
	MaxRTTMs     float64 `json:"max_rtt_ms"`
	Meets        bool    `json:"meets_slo"`
}

type slaTargetView struct {
	slaTarget
	Pops []slaPopStat `json:"pops"`
}

func (s *slaStore) view() []slaTargetView {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().UTC().AddDate(0, 0, -slaWindowDays).Format("2006-01-02")

	// stable PoP order
	var popOrder []string
	if s.fleet != nil {
		for _, n := range s.fleet.nodesSnapshot() {
			popOrder = append(popOrder, n.ID)
		}
	}

	out := make([]slaTargetView, 0, len(s.targets))
	for _, t := range s.targets {
		tv := slaTargetView{slaTarget: t}
		for _, pop := range popOrder {
			days := s.rows[pop][t.Name]
			if len(days) == 0 {
				continue
			}
			var sent, ok int
			var rttSum, rttMax float64
			for day, d := range days {
				if day < cutoff {
					continue
				}
				sent += d.Sent
				ok += d.OK
				rttSum += d.RTTSum
				if d.RTTMax > rttMax {
					rttMax = d.RTTMax
				}
			}
			if sent == 0 {
				continue
			}
			avail := float64(ok) / float64(sent) * 100
			mean := 0.0
			if ok > 0 {
				mean = rttSum / float64(ok)
			}
			meets := avail >= t.SLOPct && (t.RTTBudgetMs <= 0 || mean <= t.RTTBudgetMs)
			tv.Pops = append(tv.Pops, slaPopStat{
				Pop: pop, Sent: sent, OK: ok,
				AvailPct:  round1(avail),
				LossPct:   round1(100 - avail),
				MeanRTTMs: round1(mean),
				MaxRTTMs:  round1(rttMax),
				Meets:     meets,
			})
		}
		out = append(out, tv)
	}
	return out
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

// seriesLossPct returns the packet-loss percentage over the last n samples of a
// probe series (a sample value < 0 = lost). Uses whatever samples exist if the
// series is shorter than n. 0 when empty.
func seriesLossPct(series []tsSample, n int) float64 {
	if len(series) == 0 {
		return 0
	}
	if n > len(series) {
		n = len(series)
	}
	tail := series[len(series)-n:]
	lost := 0
	for _, s := range tail {
		if s.V < 0 {
			lost++
		}
	}
	return float64(lost) / float64(n) * 100
}

// ── HTTP ─────────────────────────────────────────────────────────────────────

// GET /api/v1/status/sla → public per-(target,PoP) availability/loss/latency SLOs.
func handleStatusSLA(w http.ResponseWriter, _ *http.Request) {
	if globalSLA == nil {
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"targets": []slaTargetView{}, "window_days": slaWindowDays}})
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"targets":     globalSLA.view(),
		"window_days": slaWindowDays,
	}})
}

// GET /api/v1/auth/sla/targets → the configured SLA target list (admin view).
func handleSLATargets(w http.ResponseWriter, _ *http.Request) {
	if globalSLA == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "sla store not ready"})
		return
	}
	globalSLA.mu.Lock()
	ts := append([]slaTarget{}, globalSLA.targets...)
	globalSLA.mu.Unlock()
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"targets": ts}})
}

// POST /api/v1/auth/sla/targets {"targets":[...]} → replace the SLA target list.
// Validates, persists, and rebuilds every PoP's probe set so changes take effect
// on the next scrape (no ncn-api restart).
func handleSLATargetsSet(w http.ResponseWriter, r *http.Request) {
	if globalSLA == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "sla store not ready"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var body struct {
		Targets []slaTarget `json:"targets"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<18)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad body"})
		return
	}
	if len(body.Targets) > 30 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "too many targets (max 30)"})
		return
	}
	seen := map[string]bool{}
	clean := make([]slaTarget, 0, len(body.Targets))
	for _, t := range body.Targets {
		t.Name = strings.TrimSpace(t.Name)
		t.Target = strings.TrimSpace(t.Target)
		if !slaNameRe.MatchString(t.Name) {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad target name: " + t.Name + " (use a-z0-9-)"})
			return
		}
		if seen[t.Name] {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "duplicate name: " + t.Name})
			return
		}
		seen[t.Name] = true
		if t.Target == "" {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "empty target for " + t.Name})
			return
		}
		if t.Type != "ping6" {
			t.Type = "ping4"
		}
		if t.SLOPct <= 0 || t.SLOPct > 100 {
			t.SLOPct = 99.9
		}
		clean = append(clean, t)
	}
	globalSLA.mu.Lock()
	globalSLA.targets = clean
	globalSLA.persistTargetsLocked()
	globalSLA.mu.Unlock()
	if globalSLA.fleet != nil {
		globalSLA.fleet.RebuildProbes()
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"targets": clean}})
}

// writeFileAtomic writes b to path via a tmp file + rename (0600), creating the
// parent dir. Best-effort: errors are logged, not fatal (DB is the primary).
func writeFileAtomic(path string, b []byte) {
	_ = os.MkdirAll(incidentsDir, 0o700)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		_ = os.Rename(tmp, path)
	} else {
		log.Printf("sla: write %s failed: %v", path, err)
	}
}
