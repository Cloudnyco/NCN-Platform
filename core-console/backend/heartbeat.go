// Heartbeat — availability history behind the public status page.
//
// The incidents store gives the status page a human narrative; this gives
// it the machine signal: per-component up/down sampled every 60s, rolled
// up into daily buckets, kept for 90 days. That's what powers the uptime
// percentage and the day-by-day history bars.
//
// Two kinds of component:
//
//   * PoPs        — sampled by reading the fleetScraper's cached OK state
//                   (no extra network: the scraper already polls every 15s).
//   * Public URLs — sampled by an HTTP GET from this host (example.com,
//                   admin console, webmail, the API health endpoint).
//
// Surface: GET /api/v1/status/summary (unauthenticated) returns every
// component with its last status, last latency, 90-day uptime, and the
// daily history array — one fetch feeds the whole page.
//
// Storage mirrors incidents.go: a single JSON file rewritten atomically
// (.tmp + rename). Volume is tiny — N components × 90 days of small
// counters. We persist every 5 minutes and on shutdown rather than on
// every 60s sample (the loss window on a hard crash is one save interval
// of counts, which barely moves a 90-day percentage).

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	heartbeatPath          = incidentsDir + "/heartbeats.json"
	heartbeatRetentionDays = 90
	heartbeatTick          = 60 * time.Second
	heartbeatSaveInterval  = 5 * time.Minute

	// last_status vocabulary on the wire.
	hbStatusUnknown = -1
	hbStatusDown    = 0
	hbStatusUp      = 1
)

// httpCheck is a public URL we probe for reachability. Kept tiny and
// hard-coded — these are our own outward-facing surfaces, they change
// about as often as we add a PoP.
type httpCheck struct {
	name string
	url  string
}

var statusHTTPChecks = []httpCheck{
	{"Website · example.com", "https://example.com/"},
	{"Console · admin.example.com", "https://admin.example.com/"},
	{"Webmail · mail.example.com", "https://mail.example.com/"},
	{"API", "https://admin.example.com/api/v1/health"},
}

// hbDay is one UTC day's tally for a component.
type hbDay struct {
	Day  string `json:"day"` // YYYY-MM-DD (UTC)
	Up   int    `json:"up"`
	Down int    `json:"down"`
}

// hbComponent is the on-disk + in-memory shape for one monitored thing.
type hbComponent struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Type     string `json:"type"` // "pop" | "website"
	URL      string `json:"url,omitempty"`

	LastStatus  int     `json:"last_status"` // 1 up | 0 down | -1 unknown
	LastLatency float64 `json:"last_latency_ms"`
	LastCheck   string  `json:"last_check,omitempty"` // RFC3339 UTC

	Days map[string]*hbDay `json:"days"` // keyed by YYYY-MM-DD
}

type heartbeatStore struct {
	mu         sync.RWMutex
	components map[string]*hbComponent
	order      []string // stable display order
	fleet      *fleetScraper
	client     *http.Client
}

var globalHeartbeat *heartbeatStore

func newHeartbeatStore(fleet *fleetScraper) (*heartbeatStore, error) {
	if err := os.MkdirAll(incidentsDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", incidentsDir, err)
	}
	s := &heartbeatStore{
		components: map[string]*hbComponent{},
		fleet:      fleet,
		// Don't follow redirects beyond a couple hops; a login redirect
		// (302→/login) still counts as "the service answered", which is
		// what we want — reachability, not deep health.
		client: &http.Client{Timeout: 9 * time.Second},
	}

	// Component definitions. PoPs first (fleet display order), then the
	// public URLs. The fleet node order is the canonical site-wide order.
	for _, n := range fleet.nodes {
		s.define(&hbComponent{Name: n.ID, Category: "Points of Presence", Type: "pop"})
	}
	for _, c := range statusHTTPChecks {
		s.define(&hbComponent{Name: c.name, Category: "Public Services", Type: "website", URL: c.url})
	}

	// Merge persisted history onto the fresh definitions (by name). New
	// components start empty; history for components no longer defined is
	// dropped (keeps the store from accumulating ghosts after a rename).
	// Prefer Postgres when it already holds the document (post-cutover),
	// else fall back to the JSON file — same dual-path as incidents.go.
	var saved map[string]*hbComponent
	loadedFromDB := false
	if globalDB != nil {
		if doc, err := loadConfigDoc("heartbeats"); err != nil {
			log.Printf("heartbeat: db load failed (%v) — falling back to file", err)
		} else if doc != nil {
			if err := json.Unmarshal(doc, &saved); err != nil {
				return nil, fmt.Errorf("parse db doc: %w", err)
			}
			loadedFromDB = true
		}
	}
	if !loadedFromDB {
		if b, err := os.ReadFile(heartbeatPath); err == nil && len(b) > 0 {
			if err := json.Unmarshal(b, &saved); err != nil {
				return nil, fmt.Errorf("parse %s: %w", heartbeatPath, err)
			}
		}
	}
	for name, sc := range saved {
		c := s.components[name]
		if c == nil {
			continue
		}
		if sc.Days != nil {
			c.Days = sc.Days
		}
		c.LastStatus = sc.LastStatus
		c.LastLatency = sc.LastLatency
		c.LastCheck = sc.LastCheck
	}

	// Migrate file→DB on the first DB-enabled boot (persist takes its own
	// read lock; safe to call here pre-Start with no concurrent access).
	if globalDB != nil && !loadedFromDB {
		if err := s.persist(); err != nil {
			return nil, fmt.Errorf("migrate heartbeats to db: %w", err)
		}
	}
	return s, nil
}

func (s *heartbeatStore) define(c *hbComponent) {
	if c.Days == nil {
		c.Days = map[string]*hbDay{}
	}
	c.LastStatus = hbStatusUnknown
	s.components[c.Name] = c
	s.order = append(s.order, c.Name)
}

// ensurePop lazily defines a PoP component (idempotent). Used by sample() so a
// node added through the admin server page starts accruing uptime history
// immediately. New PoPs are appended after the existing ones, preserving the
// stable display order for the components that were already present.
func (s *heartbeatStore) ensurePop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.components[id] != nil {
		return
	}
	s.define(&hbComponent{Name: id, Category: "Points of Presence", Type: "pop"})
}

func (s *heartbeatStore) Start(ctx context.Context) {
	go func() {
		tick := time.NewTicker(heartbeatTick)
		defer tick.Stop()
		save := time.NewTicker(heartbeatSaveInterval)
		defer save.Stop()
		s.sample() // immediate first sample so the page isn't all-unknown
		for {
			select {
			case <-ctx.Done():
				if err := s.persist(); err != nil {
					log.Printf("heartbeat: final persist: %v", err)
				}
				return
			case <-tick.C:
				s.sample()
			case <-save.C:
				if err := s.persist(); err != nil {
					log.Printf("heartbeat: persist: %v", err)
				}
			}
		}
	}()
}

// sample takes one reading of every component and records it.
func (s *heartbeatStore) sample() {
	// PoPs — read the scraper's cached OK state. Skip nodes whose cache is
	// still nil (never scraped yet): recording them as "down" would unfairly
	// dent uptime during the first scrape window.
	type popRead struct {
		id  string
		ok  bool
		lat float64
	}
	var pops []popRead
	s.fleet.mu.RLock()
	for _, n := range s.fleet.nodes {
		st := s.fleet.cache[n.ID]
		if st == nil {
			continue
		}
		pops = append(pops, popRead{id: n.ID, ok: st.OK, lat: anchorLatency(st)})
	}
	s.fleet.mu.RUnlock()
	for _, p := range pops {
		// A node added at runtime (admin server page) won't have a heartbeat
		// component yet — lazily define it so it starts accruing uptime and
		// shows on /status without an ncn-api restart.
		s.ensurePop(p.id)
		s.record(p.id, p.ok, p.lat)
	}

	// Public URLs — probe concurrently.
	var wg sync.WaitGroup
	for _, c := range statusHTTPChecks {
		wg.Add(1)
		go func(c httpCheck) {
			defer wg.Done()
			ok, ms := s.httpProbe(c.url)
			s.record(c.name, ok, ms)
		}(c)
	}
	wg.Wait()
}

// anchorLatency = mean RTT to the public v4 anchors a PoP probes. Mirrors
// the figure handlePublic exposes, so the status page and the looking-glass
// agree on "how far is the internet from this PoP".
func anchorLatency(st *fleetNodeStatus) float64 {
	var sum float64
	var n int
	for _, pr := range st.Probes {
		if pr.LastOK && (pr.Name == "cloudflare-v4" || pr.Name == "google-v4") {
			sum += pr.LastMS
			n++
		}
	}
	if n > 0 {
		return sum / float64(n)
	}
	return 0
}

func (s *heartbeatStore) httpProbe(url string) (ok bool, ms float64) {
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, 0
	}
	req.Header.Set("User-Agent", "ncn-status/1.0 (+https://example.com/status)")
	start := time.Now()
	resp, err := s.client.Do(req)
	if err != nil {
		return false, 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	ms = float64(time.Since(start).Microseconds()) / 1000.0
	// 2xx/3xx = the service answered. A 3xx (e.g. login redirect) still
	// proves the front door is up, which is all this check asserts.
	return resp.StatusCode >= 200 && resp.StatusCode < 400, ms
}

func (s *heartbeatStore) record(name string, up bool, latencyMs float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.components[name]
	if c == nil {
		return
	}
	now := time.Now().UTC()
	day := now.Format("2006-01-02")
	d := c.Days[day]
	if d == nil {
		d = &hbDay{Day: day}
		c.Days[day] = d
	}
	if up {
		d.Up++
		c.LastStatus = hbStatusUp
	} else {
		d.Down++
		c.LastStatus = hbStatusDown
	}
	c.LastLatency = latencyMs
	c.LastCheck = now.Format(time.RFC3339)

	// Prune anything past the retention window. Lexicographic compare is
	// valid for zero-padded YYYY-MM-DD.
	cutoff := now.AddDate(0, 0, -heartbeatRetentionDays).Format("2006-01-02")
	for k := range c.Days {
		if k < cutoff {
			delete(c.Days, k)
		}
	}
}

// persist writes the whole store atomically. Takes the read lock for the
// marshal — record() holds the write lock, so the two never interleave.
func (s *heartbeatStore) persist() error {
	s.mu.RLock()
	b, err := json.MarshalIndent(s.components, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	tmp := heartbeatPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, heartbeatPath); err != nil {
		return err
	}
	// Dual-write to Postgres when available; the file stays the durable
	// backup + globalDB==nil path, so a DB error here is non-fatal.
	if globalDB != nil {
		if err := saveConfigDoc("heartbeats", b); err != nil {
			log.Printf("heartbeat: db persist failed (%v) — file is current", err)
		}
	}
	return nil
}

// ────────────────────────────── Public view ──────────────────────────────

type hbDayOut struct {
	Day   string `json:"day"`
	Up    int    `json:"up"`
	Down  int    `json:"down"`
	Total int    `json:"total"`
}

type hbComponentOut struct {
	Name        string     `json:"name"`
	Category    string     `json:"category"`
	Type        string     `json:"type"`
	URL         string     `json:"url,omitempty"`
	LastStatus  int        `json:"last_status"`
	LastLatency float64    `json:"last_latency_ms"`
	LastCheck   string     `json:"last_check,omitempty"`
	Uptime      float64    `json:"uptime"` // fraction over the window, 0..1
	Days        []hbDayOut `json:"days"`   // oldest→newest, gaps filled with zeroes
}

// summary returns every component with a dense `days` array of the last
// `window` UTC days (gaps zero-filled so the frontend can render a fixed
// number of bars) plus the window uptime fraction.
func (s *heartbeatStore) summary(window int) []hbComponentOut {
	// Active PoP set is fetched before taking s.mu (separate lock domains) so
	// decommissioned nodes drop off the public status page — their history is
	// retained in the store for a possible recommission, just not shown.
	active := map[string]bool{}
	if s.fleet != nil {
		active = s.fleet.activeNodeIDs()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	today := time.Now().UTC()
	out := make([]hbComponentOut, 0, len(s.order))
	for _, name := range s.order {
		c := s.components[name]
		if c == nil {
			continue
		}
		// Hide decommissioned PoPs (website checks are always shown).
		if c.Type == "pop" && !active[c.Name] {
			continue
		}
		days := make([]hbDayOut, 0, window)
		var sumUp, sumDown int
		for i := window - 1; i >= 0; i-- {
			key := today.AddDate(0, 0, -i).Format("2006-01-02")
			up, down := 0, 0
			if d := c.Days[key]; d != nil {
				up, down = d.Up, d.Down
			}
			sumUp += up
			sumDown += down
			days = append(days, hbDayOut{Day: key, Up: up, Down: down, Total: up + down})
		}
		uptime := 0.0
		if sumUp+sumDown > 0 {
			uptime = float64(sumUp) / float64(sumUp+sumDown)
		}
		out = append(out, hbComponentOut{
			Name:        c.Name,
			Category:    c.Category,
			Type:        c.Type,
			URL:         c.URL,
			LastStatus:  c.LastStatus,
			LastLatency: c.LastLatency,
			LastCheck:   c.LastCheck,
			Uptime:      uptime,
			Days:        days,
		})
	}
	return out
}

// GET /api/v1/status/summary — unauthenticated. The single feed behind the
// public status page's component cards + uptime bars.
func handleStatusSummary(w http.ResponseWriter, _ *http.Request) {
	if globalHeartbeat == nil {
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
			"components": []hbComponentOut{}, "window_days": heartbeatRetentionDays,
		}})
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"components":  globalHeartbeat.summary(heartbeatRetentionDays),
		"window_days": heartbeatRetentionDays,
	}})
}
