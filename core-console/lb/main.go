// ncn-lb — a self-hosted Cloudflare-Load-Balancer equivalent: health-checked
// origin pools with automatic active-passive failover. Built so we don't pay
// for CF LB. Runs on pop-04 (so it survives a ctrl-01 death) and watches the
// console origins; when the active (highest-priority healthy) origin changes,
// it fails over by running a failover script (promote the PG replica, start the
// standby ncn-api, repoint the Cloudflare DNS origin via the CF API).
//
// SAFETY: starts in OBSERVE mode (mode="observe") — it detects + logs what it
// WOULD do but takes no action. Set mode="armed" only after the failover script
// is in place + tested. No automatic fail-BACK (anti-flap): once failed over to
// the standby, it stays there until an operator resets.
//
// Config: /etc/ncn-lb/config.json (built-in defaults if absent). Status: a
// JSON endpoint on :8090 mirroring CF LB's pool dashboard.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"
)

const defaultConfigPath = "/etc/ncn-lb/config.json"

type origin struct {
	Name     string `json:"name"`
	URL      string `json:"url"`            // primary health-check URL (probe the ORIGIN directly, not via CF)
	AltURL   string `json:"alt_url,omitempty"` // fallback path — origin is DOWN only if BOTH fail (avoids false failover on a single-link blip)
	Host     string `json:"host"`           // Host header to send (the proxied vhost)
	Priority int    `json:"priority"`       // lower = preferred (1 = primary)
}

type config struct {
	Mode               string   `json:"mode"`                 // observe | armed
	Origins            []origin `json:"origins"`
	IntervalSecs       int      `json:"interval_secs"`        // between health sweeps
	TimeoutSecs        int      `json:"timeout_secs"`         // per-probe timeout
	UnhealthyThreshold int      `json:"unhealthy_threshold"`  // consecutive fails → down
	HealthyThreshold   int      `json:"healthy_threshold"`    // consecutive oks → up
	FailoverScript     string   `json:"failover_script"`      // run on failover (armed only)
	StatusAddr         string   `json:"status_addr"`
}

func defaultConfig() config {
	return config{
		Mode:               "observe",
		IntervalSecs:       10,
		TimeoutSecs:        5,
		UnhealthyThreshold: 3,
		HealthyThreshold:   2,
		FailoverScript:     "/etc/ncn-lb/failover.sh",
		StatusAddr:         ":8090",
		Origins: []origin{
			{Name: "ctrl-01", URL: "https://[2001:db8:53::1]/api/v1/health", Host: "admin.example.com", Priority: 1},
			{Name: "pop-04", URL: "https://[2001:db8:51::2]/api/v1/health", Host: "admin.example.com", Priority: 2},
		},
	}
}

func loadConfig(path string) config {
	c := defaultConfig()
	b, err := os.ReadFile(path)
	if err != nil {
		log.Printf("config: %s absent (%v) — using built-in defaults", path, err)
		return c
	}
	if err := json.Unmarshal(b, &c); err != nil {
		log.Printf("config: parse %s failed (%v) — using built-in defaults", path, err)
		return defaultConfig()
	}
	if c.Mode == "" {
		c.Mode = "observe"
	}
	if c.IntervalSecs <= 0 {
		c.IntervalSecs = 10
	}
	if c.TimeoutSecs <= 0 {
		c.TimeoutSecs = 5
	}
	if c.UnhealthyThreshold <= 0 {
		c.UnhealthyThreshold = 3
	}
	if c.HealthyThreshold <= 0 {
		c.HealthyThreshold = 2
	}
	if c.StatusAddr == "" {
		c.StatusAddr = ":8090"
	}
	// Armed mode must have a runnable failover script — otherwise the LB would
	// detect an outage and silently fail to act. Refuse to arm (downgrade to
	// observe, loudly) when the script is missing or not executable.
	if c.Mode == "armed" {
		if fi, err := os.Stat(c.FailoverScript); err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
			log.Printf("config: mode=armed but failover script %q is missing/not executable (%v) — DOWNGRADING to observe", c.FailoverScript, err)
			c.Mode = "observe"
		}
	}
	return c
}

// originState is the live health bookkeeping for one origin.
type originState struct {
	Origin    origin `json:"origin"`
	Healthy   bool   `json:"healthy"`
	OKs       int    `json:"-"`
	Fails     int    `json:"-"`
	LastErr   string `json:"last_err,omitempty"`
	LastCode  int    `json:"last_code"`
	LastCheck string `json:"last_check"`
}

type controller struct {
	mu      sync.Mutex
	cfg     config
	states  map[string]*originState
	active  string // name of the current active origin
	hc      *http.Client
	failing bool // a failover has been triggered (latched; no auto-failback)
}

func newController(cfg config) *controller {
	c := &controller{
		cfg:    cfg,
		states: map[string]*originState{},
		hc: &http.Client{
			Timeout: time.Duration(cfg.TimeoutSecs) * time.Second,
			// Probe origins by IP / over the backbone → certs won't match the
			// vhost; we only care that the origin answers, not its cert chain.
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		},
	}
	for i := range cfg.Origins {
		o := cfg.Origins[i]
		c.states[o.Name] = &originState{Origin: o}
	}
	return c
}

// probeURL does one GET; healthy = 2xx/3xx.
func (c *controller) probeURL(u, host string) (bool, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.cfg.TimeoutSecs)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return false, 0, err
	}
	if host != "" {
		req.Host = host
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return false, 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400, resp.StatusCode, nil
}

// probe returns (healthy, statusCode, err). The origin is healthy if its
// primary URL OR its AltURL responds — so a single-link blip (e.g. the private
// backbone flapping while the public path is fine) does NOT trip a failover.
// Only when ALL configured paths fail is the origin considered down.
func (c *controller) probe(o origin) (bool, int, error) {
	ok, code, err := c.probeURL(o.URL, o.Host)
	if ok || o.AltURL == "" {
		return ok, code, err
	}
	ok2, code2, _ := c.probeURL(o.AltURL, o.Host)
	if ok2 {
		return true, code2, nil
	}
	return false, code, err // report the primary path's status for the log
}

// sweep probes every origin once, updates health (with thresholds), then
// re-evaluates the active origin and triggers failover if it changed.
func (c *controller) sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, st := range c.states {
		ok, code, err := c.probe(st.Origin)
		st.LastCheck = now
		st.LastCode = code
		st.LastErr = ""
		if err != nil {
			st.LastErr = err.Error()
		}
		if ok {
			st.OKs++
			st.Fails = 0
			if !st.Healthy && st.OKs >= c.cfg.HealthyThreshold {
				st.Healthy = true
				log.Printf("origin %s → HEALTHY (code=%d)", st.Origin.Name, code)
			}
		} else {
			st.Fails++
			st.OKs = 0
			if st.Healthy && st.Fails >= c.cfg.UnhealthyThreshold {
				st.Healthy = false
				log.Printf("origin %s → UNHEALTHY (%d consecutive; code=%d err=%q)", st.Origin.Name, st.Fails, code, st.LastErr)
			}
			// First-run: an origin that's never been healthy stays down silently
			// until it crosses HealthyThreshold.
		}
	}
	c.reconcileLocked()
}

// reconcileLocked drives active-passive failover. The PRIMARY is the
// lowest-priority-number origin. While it serves, we stay on it. When it goes
// unhealthy we fail over to the next-priority STANDBY — which is normally cold
// (not serving) until the failover script promotes its DB + starts its ncn-api,
// so we do NOT gate the decision on the standby's serving health. Latched: no
// automatic fail-back (anti-flap) — an operator resets after the primary heals.
func (c *controller) reconcileLocked() {
	prio := c.byPriorityLocked()
	if len(prio) == 0 {
		return
	}
	primary := prio[0]
	if c.active == "" {
		c.active = primary
		log.Printf("reconcile: initial active origin = %s", primary)
		return
	}
	if c.failing {
		// Already failed over. If the primary recovered, say so but don't fail back.
		if st := c.states[primary]; st != nil && st.Healthy && c.active != primary {
			log.Printf("reconcile: %s recovered but staying on %s (anti-flap; reset to fail back)", primary, c.active)
		}
		return
	}
	// Not yet failed over → the primary should be active and healthy.
	if st := c.states[primary]; st != nil && st.Healthy {
		c.active = primary
		return
	}
	// Primary unhealthy → fail over to the next-priority origin (the standby).
	var target string
	for _, name := range prio[1:] {
		target = name
		break
	}
	if target == "" {
		log.Printf("reconcile: primary %s UNHEALTHY but no standby configured to fail over to", primary)
		return
	}
	if c.cfg.Mode == "armed" {
		log.Printf("FAILOVER [armed]: primary %s down → %s — running %s", primary, target, c.cfg.FailoverScript)
		c.active = target
		c.failing = true
		go c.runFailover(primary, target)
	} else {
		log.Printf("FAILOVER [observe]: WOULD fail over primary %s (down) → %s (run %s %s %s). No action taken.",
			primary, target, c.cfg.FailoverScript, primary, target)
		c.active = target
		c.failing = true
	}
}

// byPriorityLocked returns origin names sorted by priority (lowest number first).
func (c *controller) byPriorityLocked() []string {
	names := make([]string, 0, len(c.states))
	for n := range c.states {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		return c.states[names[i]].Origin.Priority < c.states[names[j]].Origin.Priority
	})
	return names
}

func (c *controller) runFailover(from, to string) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.cfg.FailoverScript, from, to)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("FAILOVER script FAILED (%v): %s", err, string(out))
		return
	}
	log.Printf("FAILOVER script ok: %s", string(out))
}

func (c *controller) handleStatus(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]any{
		"mode":     c.cfg.Mode,
		"active":   c.active,
		"failing":  c.failing,
		"origins":  c.states,
		"interval": c.cfg.IntervalSecs,
		"ts":       time.Now().UTC().Format(time.RFC3339),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func main() {
	once := flag.Bool("once", false, "run a single sweep and exit (for testing)")
	cfgPath := flag.String("config", defaultConfigPath, "path to config json")
	flag.Parse()

	cfg := loadConfig(*cfgPath)
	c := newController(cfg)
	log.Printf("ncn-lb starting · mode=%s · %d origins · interval=%ds · status=%s",
		cfg.Mode, len(cfg.Origins), cfg.IntervalSecs, cfg.StatusAddr)

	if *once {
		c.sweep()
		b, _ := json.MarshalIndent(c.states, "", "  ")
		fmt.Println(string(b))
		return
	}

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/status", c.handleStatus)
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
		log.Printf("status endpoint on %s", cfg.StatusAddr)
		if err := http.ListenAndServe(cfg.StatusAddr, mux); err != nil {
			log.Printf("status server: %v", err)
		}
	}()

	t := time.NewTicker(time.Duration(cfg.IntervalSecs) * time.Second)
	defer t.Stop()
	c.sweep() // immediate first sweep
	for range t.C {
		c.sweep()
	}
}
