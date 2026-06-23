// Fleet monitoring.
//
// Single source of truth for per-node state across the operator UI. Scrapes
// each remote PoP via SSH every 15s and caches results. The Dashboard,
// Connectivity, BIRD and Performance pages all read from /api/v1/fleet and
// pick the entry matching the selected node — no per-page endpoints.
//
// The local node (ctrl-01) skips SSH and reuses our in-process Monitor +
// birdState, but the JSON shape it emits is identical so the UI doesn't
// need to special-case it.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type fleetNode struct {
	ID      string  `json:"id"`
	Label   string  `json:"label"`
	Country string  `json:"country"`
	Address string  `json:"address"`
	Lat     float64 `json:"lat,omitempty"`
	Lon     float64 `json:"lon,omitempty"`
	Local   bool    `json:"local"`
	SSHHost string  `json:"-"`

	// Per-node SSH overrides. Empty = use fleet defaults:
	//   SSHUser     = "root"
	//   SSHIdentity = "/etc/ncn-core-console/fleet-key"
	// Newer PoPs that ship as a vendor image (e.g. Debian cloud-init)
	// often force a non-root login user; some are bootstrapped with the
	// operator's personal Termux key instead of the dedicated fleet key
	// (trade-off accepted by the operator).
	SSHUser     string `json:"-"`
	SSHIdentity string `json:"-"`
	// SSHPort is the node's sshd port. 0 = default 22. Some PoPs run sshd on a
	// non-standard port (vendor image / hardening), so onboarding + the
	// terminal must honour it instead of assuming 22.
	SSHPort int `json:"-"`
	// Region mirrors the registry region code (51=HKG …). Used by the alert
	// engine to scope a rule group to a metro at tick time.
	Region int `json:"-"`
}

// sshUser / sshIdentity return the effective values for outbound SSH,
// applying the per-node overrides on top of the fleet defaults.
func (n fleetNode) sshUser() string {
	if n.SSHUser != "" {
		return n.SSHUser
	}
	return "root"
}
func (n fleetNode) sshIdentity() string {
	if n.SSHIdentity != "" {
		return n.SSHIdentity
	}
	return "/etc/ncn-core-console/fleet-key"
}
func (n fleetNode) sshPort() int {
	if n.SSHPort > 0 {
		return n.SSHPort
	}
	return 22
}

type fleetNodeStatus struct {
	Node      fleetNode `json:"node"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	Hostname  string    `json:"hostname,omitempty"`
	Iface     string    `json:"iface,omitempty"`
	Uptime    string    `json:"uptime,omitempty"`
	Load1     float64   `json:"load_1"`
	MemPct    float64   `json:"mem_pct"`
	CPUPct    float64   `json:"cpu_pct"`
	DiskPct   float64   `json:"disk_pct"`
	NetRxBps  float64   `json:"net_rx_bps"`
	NetTxBps  float64   `json:"net_tx_bps"`
	// Per-interface RX/TX bytes/sec for ALL non-lo interfaces (incl. wg*/tun*),
	// sorted by total throughput desc — powers the traffic Top-N. Computed in
	// parseSnapshot from /proc/net/dev; empty on the first tick (no delta yet).
	Ifaces []ifaceStat `json:"ifaces,omitempty"`
	MemTotal  uint64    `json:"mem_total,omitempty"`
	MemUsed   uint64    `json:"mem_used,omitempty"`
	DiskTotal uint64    `json:"disk_total,omitempty"`
	DiskUsed  uint64    `json:"disk_used,omitempty"`

	BirdVer     string           `json:"bird_version,omitempty"`
	Protocols   []birdProtocol   `json:"protocols,omitempty"`
	RouteCounts []birdRouteCount `json:"route_counts,omitempty"`
	WG          []wgIface        `json:"wg,omitempty"`
	Tunnels     []netTunnel      `json:"tunnels,omitempty"`
	Probes      []probeOut       `json:"probes,omitempty"`

	// AgentCertDaysLeft is the number of days until this node's
	// ncn-agent TLS cert reaches its notAfter. Refreshed every 24h by
	// fleetScraper.checkAgentCertExpiry. Negative = already expired. 0
	// when not yet checked (startup) — the alert rule treats 0 as
	// "unknown" and ignores it so a momentary blank doesn't fire.
	AgentCertDaysLeft int `json:"agent_cert_days_left,omitempty"`

	LoadSeries  []tsSample `json:"load_series,omitempty"`
	MemSeries   []tsSample `json:"mem_series,omitempty"`
	CPUSeries   []tsSample `json:"cpu_series,omitempty"`
	DiskSeries  []tsSample `json:"disk_series,omitempty"`
	NetRxSeries []tsSample `json:"net_rx_series,omitempty"`
	NetTxSeries []tsSample `json:"net_tx_series,omitempty"`

	FetchedAt int64  `json:"fetched_at"`
	Latency   string `json:"scrape_latency,omitempty"`

	// ConsecFail is how many consecutive scrape ticks have failed for this
	// node, INCLUDING this one (0 when OK). Mirrors fleetNodeMem.consecFail
	// onto the per-tick status so the alert engine can debounce
	// node-unreachable: a single failed tick (transient blip the in-tick
	// reconnect couldn't catch) is not yet an outage. Surfaced in JSON so
	// the operator UI can show "down (2/3)" while the grace ticks elapse.
	ConsecFail int `json:"consec_fail,omitempty"`
}

type probeTarget struct {
	Name, Target, Type string
}

// ifaceStat is one interface's current throughput (bytes/sec each way) plus
// the cumulative byte counters (exposed as Prometheus counters for Grafana).
type ifaceStat struct {
	Name    string  `json:"name"`
	RxBps   float64 `json:"rx_bps"`
	TxBps   float64 `json:"tx_bps"`
	RxTotal uint64  `json:"rx_total"`
	TxTotal uint64  `json:"tx_total"`
}

// Per-node persistent state across scrapes: deltas for rate calculations
// and ring buffers for sparklines.
type fleetNodeMem struct {
	prevIdle, prevTotal uint64
	prevRx, prevTx      uint64
	prevAt              time.Time
	iface               string

	// ifacePrev holds each interface's last cumulative rx/tx bytes [rx,tx],
	// for per-interface rate deltas (the traffic Top-N). Keyed by iface name.
	ifacePrev map[string][2]uint64

	load, mem, cpu, disk, netRx, netTx *ringBuf

	// consecFail counts consecutive failed scrapes for this node. Reset to
	// 0 on any successful scrape, incremented on each failure. Read+written
	// only from the node's own scrape goroutine (one per node per tick, and
	// scrapes can't overlap because they're bounded by scrapeBudget < the
	// 15s tick), so no extra lock is needed — same single-writer assumption
	// the prevIdle/prevRx delta fields already rely on.
	consecFail int

	// Per-node probe state, keyed by probe name.
	probeTargets []probeTarget
	probes       map[string]*fleetProbe

	// birdDetailRaw caches the most recent `birdc show protocols all`
	// output for this node, populated by parseSnapshot from section 15
	// when the agent is on Phase 4+. The BIRD detail HTTP handler reads
	// from here (cache hit = ~1ms) instead of issuing a fresh SSH per
	// request. Empty if the agent hasn't been re-provisioned to the
	// Phase 4 binary yet — handler falls back to SSH in that case.
	birdDetailMu  sync.Mutex
	birdDetailRaw string
}

type fleetProbe struct {
	mu       sync.Mutex
	target   string
	ptype    string
	lastOK   bool
	lastMS   float64
	lastTime int64
	series   *ringBuf
}

const (
	fleetTick    = 15 * time.Second
	fleetRingCap = 60 // 60 × 15s = 15 minutes

	// In-tick reconnect. When the agent fetch fails, retry up to this many
	// times within the SAME scrape tick before declaring the node down.
	// All attempts share one 13s deadline (see scrapeRemote), so the total
	// scrape time can never exceed the tick budget no matter how many
	// retries fire — there is no per-node overlap protection in refreshAll,
	// and a scrape that ran past 15s would race the next tick's goroutine.
	//
	// The point is to absorb FAST, transient failures — the agent-restart
	// window during a deploy, a TLS reset, an HMAC nonce race, a momentary
	// TCP refusal — which return in well under a second. Those get an
	// immediate reconnect and usually succeed on the 2nd try, so OK never
	// flips false and no "node unreachable" alert is born. A genuine
	// timeout, by contrast, consumes the shared deadline on the first
	// attempt; the loop then exits without burning extra time and the next
	// 15s tick becomes the natural retry.
	scrapeMaxAttempts  = 3
	scrapeRetryBackoff = 300 * time.Millisecond
	scrapeBudget       = 13 * time.Second
)

type fleetScraper struct {
	mu    sync.RWMutex
	nodes []fleetNode
	cache map[string]*fleetNodeStatus
	mem   map[string]*fleetNodeMem
	local *Monitor
	// auth is wired in by main() after construction so the terminal handler
	// can re-verify the operator's password before minting a session ticket.
	auth *authStore

	// registry is the persistent source of truth for the node list. The
	// scraper's f.nodes is the live, active-only projection of it; the admin
	// node API mutates the registry then calls the apply* methods below to
	// reconcile the runtime state without an ncn-api restart.
	registry *nodeRegistry
	// localID is which node id maps to in-process /proc reads instead of a
	// remote agent fetch (NCN_LOCAL_NODE_ID, default ctrl-01). Kept so newly
	// added nodes are marked Local consistently.
	localID string
	// alerts, when wired, lets a decommission/remove evict that node's
	// active alerts so a "node unreachable" doesn't hang forever (the node
	// leaves the scrape snapshot and would otherwise never reach resolve).
	alerts *alertEngine
	// notify, when wired, pushes server-lifecycle events (add / decommission /
	// recommission / delete / provision) to the ops Telegram chat. nil = TG
	// disabled; all NotifyEvent calls are no-ops then. Set once at boot.
	notify *tgNotifier

	// onboard tracks the live, step-by-step onboarding job per node id (key
	// bootstrap → provision → verify), so the admin UI can poll real-time
	// progress. Guarded by its own mutex (independent of f.mu — the job
	// goroutine runs long network ops we must not hold f.mu across).
	onboardMu sync.Mutex
	onboard   map[string]*onboardJob

	// meshApply tracks the live, per-target mesh auto-apply job (additive
	// bird.conf + tunnels + configure soft, with backup/rollback) keyed by the
	// new node's id. Same step+log structure as onboard. Guarded by its own
	// mutex — its goroutine runs long SSH ops we must not hold f.mu across.
	meshApplyMu sync.Mutex
	meshApply   map[string]*onboardJob

	// ── REST agent transport state ─────────────────────────────────────
	//
	// agentClient is the HTTP client used to call ncn-agent on each PoP.
	// It pins the agent CA (/etc/ncn-core-console/agent-ca/ca.crt) as the
	// ONLY trust root — a publicly-trusted cert can't impersonate an
	// agent endpoint. Initialised lazily and nil if the CA file is
	// missing; in that case every node falls back to SSH transport
	// regardless of fleetNode.Transport.
	//
	// agentKeys holds the per-node HMAC keys (32 bytes each, raw) loaded
	// from /etc/ncn-core-console/agent-keys/<node-id>.key. Missing key
	// for a given node = REST transport unusable for that node → SSH
	// fallback. Loaded ONCE at startup; rotating a key means restarting
	// ncn-api. SIGHUP-driven reload is a Phase 3+ ergonomic improvement.
	agentClient *http.Client
	agentKeys   map[string][]byte

	// agentCertDaysLeft caches the days-until-notAfter for each PoP's
	// agent TLS cert, refreshed by checkAgentCertExpiry() once at
	// startup and every 24h. The alert engine reads via
	// AgentCertDaysLeft() to fire when any node is < 30 days out.
	// Unreachable nodes keep their last reading rather than dropping to
	// zero — better to alert on a stale value than create a fake "0
	// days" flag during a network blip.
	agentCertDaysLeft map[string]int
}

func newFleetScraper(local *Monitor, reg *nodeRegistry) *fleetScraper {
	// localID picks which fleet entry maps to in-process /proc reads vs.
	// remote agent fetch. ctrl-01 is the default for backward compatibility,
	// but the DR boxes (pop-04) override via env so they correctly
	// identify themselves as the local node and scrape the others.
	localID := os.Getenv("NCN_LOCAL_NODE_ID")
	if localID == "" {
		localID = "ctrl-01"
	}

	f := &fleetScraper{
		local:    local,
		registry: reg,
		localID:  localID,
		cache:    map[string]*fleetNodeStatus{},
		mem:       map[string]*fleetNodeMem{},
		onboard:   map[string]*onboardJob{},
		meshApply: map[string]*onboardJob{},
		// Node list comes from the persistent registry (active records only).
		// Order = display order across the entire site (Landing, Looking Glass
		// map, /admin/fleet grid, bot /status output); the registry preserves
		// insertion order so it propagates everywhere.
		nodes: reg.activeFleetNodes(),
	}
	for i := range f.nodes {
		if f.nodes[i].ID == localID {
			f.nodes[i].Local = true
			f.nodes[i].SSHHost = "" // SSH never used for self
			log.Printf("fleet: local node identified as %s", localID)
		}
	}
	for _, n := range f.nodes {
		f.mem[n.ID] = f.newNodeMemLocked(n.ID)
	}

	// Best-effort REST agent transport setup. If the CA file or any key
	// is missing, the affected nodes silently stay unreachable until ops
	// runs agent-ca-bootstrap.sh + provisions a PoP — behaviour is unchanged
	// from before the registry refactor.
	f.initAgentTransport()

	log.Printf("fleet: %d active node(s) loaded from registry", len(f.nodes))
	return f
}

// newNodeMemLocked allocates a fresh per-node memory block (ring buffers +
// probe state). Caller must hold f.mu when called after construction (during
// construction f is not yet shared). probeTargetsFor reads f.nodes, so f.nodes
// must already contain the node before this runs.
func (f *fleetScraper) newNodeMemLocked(id string) *fleetNodeMem {
	mem := &fleetNodeMem{
		load:         newRing(fleetRingCap),
		mem:          newRing(fleetRingCap),
		cpu:          newRing(fleetRingCap),
		disk:         newRing(fleetRingCap),
		netRx:        newRing(fleetRingCap),
		netTx:        newRing(fleetRingCap),
		probeTargets: f.probeTargetsFor(id),
		probes:       map[string]*fleetProbe{},
	}
	for _, t := range mem.probeTargets {
		mem.probes[t.Name] = &fleetProbe{
			target: t.Target,
			ptype:  t.Type,
			series: newRing(fleetRingCap),
		}
	}
	return mem
}

// setAlerts wires the alert engine so node lifecycle changes can evict stale
// alerts. Called from main() after both are constructed.
func (f *fleetScraper) setAlerts(e *alertEngine) {
	f.mu.Lock()
	f.alerts = e
	f.mu.Unlock()
}

// nodesSnapshot returns a value copy of the live node slice under the read
// lock. Loop-heavy readers that don't already hold f.mu (refreshAll, cert
// sweep, term lookup, bird detail, bot status) range this copy so a concurrent
// add/remove can't tear the slice mid-iteration.
func (f *fleetScraper) nodesSnapshot() []fleetNode {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]fleetNode, len(f.nodes))
	copy(out, f.nodes)
	return out
}

// lookupNode returns a value copy of the live node with the given id under the
// read lock (callers must NOT take &f.nodes[i] — the slice can be mutated at
// runtime now).
func (f *fleetScraper) lookupNode(id string) (fleetNode, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, n := range f.nodes {
		if n.ID == id {
			return n, true
		}
	}
	return fleetNode{}, false
}

// activeNodeIDs returns the set of currently-live node ids. Used by the
// heartbeat store to drop decommissioned PoPs from the public status page
// (and recognise newly-added ones) without an ncn-api restart.
func (f *fleetScraper) activeNodeIDs() map[string]bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make(map[string]bool, len(f.nodes))
	for _, n := range f.nodes {
		out[n.ID] = true
	}
	return out
}

// ── Runtime node lifecycle (called by the admin node API after the registry
// has been mutated and persisted) ─────────────────────────────────────────
//
// These keep the derived runtime state (f.nodes / f.mem / f.cache / agent
// keys / probe topology / alerts) in sync with a registry change, without an
// ncn-api restart.

// applyAddNode brings a node into the live scrape set. Idempotent: if it's
// already live, it just refreshes metadata. rec must already be persisted to
// the registry as active.
func (f *fleetScraper) applyAddNode(rec nodeRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn := rec.toFleetNode()
	if fn.ID == f.localID {
		fn.Local = true
		fn.SSHHost = ""
	}
	found := false
	for i := range f.nodes {
		if f.nodes[i].ID == fn.ID {
			f.nodes[i] = fn
			found = true
			break
		}
	}
	if !found {
		f.nodes = append(f.nodes, fn)
	}
	if f.mem[fn.ID] == nil {
		f.mem[fn.ID] = f.newNodeMemLocked(fn.ID)
	}
	// A new node changes everyone's inter-PoP probe set (every node pings the
	// others), so reconcile all probe topologies, then (re)load HMAC keys so
	// the freshly provisioned node's key is picked up.
	f.rebuildProbesLocked()
	f.reloadAgentKeysLocked()
	log.Printf("fleet: node %s added to live set (%d active)", fn.ID, len(f.nodes))
}

// applyUpdateNode refreshes a live node's metadata in place. If the node is
// not currently live (decommissioned) this is a no-op — recommission rebuilds
// it from the registry.
func (f *fleetScraper) applyUpdateNode(rec nodeRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.nodes {
		if f.nodes[i].ID != rec.ID {
			continue
		}
		fn := rec.toFleetNode()
		if fn.ID == f.localID {
			fn.Local = true
			fn.SSHHost = ""
		}
		addrChanged := f.nodes[i].Address != fn.Address
		f.nodes[i] = fn
		if addrChanged {
			// Address drives probe targets (other nodes' v4 fallback) and the
			// agent endpoint URL; refresh both.
			f.rebuildProbesLocked()
			f.reloadAgentKeysLocked()
		}
		log.Printf("fleet: node %s metadata updated", rec.ID)
		return
	}
}

// applyDeactivateNode removes a node from the live scrape set (decommission or
// permanent delete). Stops it being scraped, drops its cached status, evicts
// its active alerts, and reconciles probe topology. The registry record (and,
// for decommission, the on-disk key) is handled by the caller. If purge is
// true the per-node memory + agent key are dropped too (permanent delete);
// decommission keeps mem so a later recommission preserves nothing critical
// but avoids churn.
func (f *fleetScraper) applyDeactivateNode(id string, purge bool) {
	f.mu.Lock()
	for i := range f.nodes {
		if f.nodes[i].ID == id {
			f.nodes = append(f.nodes[:i], f.nodes[i+1:]...)
			break
		}
	}
	delete(f.cache, id)
	if purge {
		delete(f.mem, id)
		delete(f.agentKeys, id)
		delete(f.agentCertDaysLeft, id)
	}
	f.rebuildProbesLocked()
	alerts := f.alerts
	f.mu.Unlock()

	// Evict outside f.mu — alertEngine has its own lock.
	if alerts != nil {
		alerts.evictNode(id)
	}
	log.Printf("fleet: node %s deactivated (purge=%v)", id, purge)
}

// rebuildProbesLocked recomputes every live node's inter-PoP probe set and
// reconciles its probe map (add new targets, drop gone ones, keep existing so
// their RTT history survives). Must hold f.mu.
func (f *fleetScraper) rebuildProbesLocked() {
	for _, n := range f.nodes {
		mem := f.mem[n.ID]
		if mem == nil {
			continue
		}
		targets := f.probeTargetsFor(n.ID)
		mem.probeTargets = targets
		want := make(map[string]bool, len(targets))
		for _, t := range targets {
			want[t.Name] = true
			if p, ok := mem.probes[t.Name]; ok {
				p.mu.Lock()
				p.target = t.Target
				p.ptype = t.Type
				p.mu.Unlock()
				continue
			}
			mem.probes[t.Name] = &fleetProbe{target: t.Target, ptype: t.Type, series: newRing(fleetRingCap)}
		}
		for name := range mem.probes {
			if !want[name] {
				delete(mem.probes, name)
			}
		}
	}
}

// initAgentTransport loads the agent CA bundle and per-node HMAC keys so
// scrapeRemoteREST can dial each PoP. Failures here NEVER abort startup —
// the SSH path is always-available fallback. Logs at info level so ops can
// see at boot whether REST is wired or still 100 % SSH.
func (f *fleetScraper) initAgentTransport() {
	caPath := "/etc/ncn-core-console/agent-ca/ca.crt"
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		log.Printf("fleet: agent CA not loaded (%v); all nodes stay on SSH", err)
		return
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		log.Printf("fleet: agent CA at %s contains no parsable certs; SSH only", caPath)
		return
	}

	// One client, shared across all per-node calls. Keep-alive is on by
	// default; tweak timeouts so a stuck agent doesn't hold the scrape
	// goroutine for the full SSH timeout (14s).
	f.agentClient = &http.Client{
		// 14s leaves 1s slack below the 15s fleet tick so a slow scrape
		// can't pile up on the next refreshAll (no per-node overlap
		// protection in refreshAll). Agent's shellTimeout*2 = 20s so the
		// client always cancels first.
		Timeout: 14 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				MinVersion: tls.VersionTLS12,
			},
			MaxIdleConns:        8,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	f.ReloadAgentKeys()
}

// checkAgentCertExpiry connects to each PoP's agent endpoint, captures
// the server certificate, and writes the days-until-notAfter into
// f.agentCertDaysLeft for the alert engine to read. Runs once at startup
// and every 24h thereafter. If a node is unreachable, the previous
// value is kept (no false "0 days" flag).
//
// The cert is the same one agent-node-provision.sh minted via the
// internal CA; it's 1-year by default, so days_left drifts from ~365
// down to ~0 over a year. The cert-expiring alert fires below 30.
func (f *fleetScraper) checkAgentCertExpiry() {
	if f.agentClient == nil {
		return
	}
	tlsCfg := &tls.Config{
		RootCAs:    nil, // we WANT to see the cert even if it's invalid
		MinVersion: tls.VersionTLS12,
	}
	// Re-use the CA pool already loaded so verification is honest, but
	// fall back to InsecureSkipVerify so we still capture an expiring
	// cert that's beyond its notAfter (otherwise the TLS dial fails
	// before we can read the cert chain).
	if tr, ok := f.agentClient.Transport.(*http.Transport); ok && tr.TLSClientConfig != nil {
		tlsCfg.RootCAs = tr.TLSClientConfig.RootCAs
	}
	tlsCfg.InsecureSkipVerify = true
	updates := map[string]int{}
	for _, n := range f.nodesSnapshot() {
		if n.Local {
			continue
		}
		addr := n.Address + ":9101"
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second},
			"tcp", addr, tlsCfg)
		if err != nil {
			continue
		}
		chain := conn.ConnectionState().PeerCertificates
		conn.Close()
		if len(chain) == 0 {
			continue
		}
		days := int(time.Until(chain[0].NotAfter).Hours() / 24)
		updates[n.ID] = days
	}
	f.mu.Lock()
	if f.agentCertDaysLeft == nil {
		f.agentCertDaysLeft = map[string]int{}
	}
	for id, d := range updates {
		f.agentCertDaysLeft[id] = d
	}
	f.mu.Unlock()
	log.Printf("fleet: agent cert expiry refreshed for %d nodes", len(updates))
}

// AgentCertDaysLeft returns a snapshot of per-node days-until-cert-expiry.
// Used by the alert engine to fire the agent-cert-expiring rule.
func (f *fleetScraper) AgentCertDaysLeft() map[string]int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make(map[string]int, len(f.agentCertDaysLeft))
	for k, v := range f.agentCertDaysLeft {
		out[k] = v
	}
	return out
}

// ReloadAgentKeys re-reads /etc/ncn-core-console/agent-keys/*.key into
// f.agentKeys, replacing the in-memory map. Called once from
// initAgentTransport at startup and again on SIGHUP (see main.go signal
// hook) so re-provisioning a PoP doesn't require an ncn-api restart.
//
// Atomic swap: builds a fresh map locally, then assigns under a brief
// write lock. Concurrent scrapeRemote readers see either the old or new
// map cleanly; there's no torn-read window.
//
// Per-node HMAC keys are 32 raw bytes (no encoding). The SAME bytes live
// at /etc/ncn-agent/hmac.key on the corresponding PoP — agent-node-
// provision.sh keeps them in lockstep.
func (f *fleetScraper) ReloadAgentKeys() {
	f.mu.Lock()
	f.reloadAgentKeysLocked()
	f.mu.Unlock()
}

// reloadAgentKeysLocked does the actual reload assuming f.mu is held. The
// apply* lifecycle methods call this directly (they already hold the write
// lock); the public ReloadAgentKeys wraps it for the SIGHUP path. The brief
// file I/O under the write lock is fine — the key files are tiny and reloads
// are infrequent (SIGHUP or a node mutation), while scrapeRemote only takes a
// read lock for a moment.
func (f *fleetScraper) reloadAgentKeysLocked() {
	fresh := map[string][]byte{}
	keysDir := "/etc/ncn-core-console/agent-keys"
	remote := 0
	for _, n := range f.nodes {
		if n.Local {
			continue // the local node doesn't scrape itself via REST
		}
		remote++
		path := filepath.Join(keysDir, n.ID+".key")
		raw, err := os.ReadFile(path)
		if err != nil {
			// Not an error per se — node may not be provisioned yet.
			continue
		}
		// Strip trailing newline if operator created the file with `echo`
		// instead of `openssl rand 32 >` — agent does the same.
		raw = []byte(strings.TrimRight(string(raw), "\r\n"))
		if len(raw) < 16 {
			log.Printf("fleet: agent key for %s too short (%d bytes); skipping", n.ID, len(raw))
			continue
		}
		fresh[n.ID] = raw
	}
	f.agentKeys = fresh
	log.Printf("fleet: agent keys loaded — %d/%d remote keys present", len(fresh), remote)
}

// probeTargetsFor returns the probe target list this node should run.
// Every node pings the three public anchors plus the OTHER PoP addresses.
// ncnProbeV6 maps each PoP to its NCN IPv6 anchor (2001:db8:5X::1). Inter-PoP
// probes target THIS (our own v6 backbone), not the providers' v4 IPs:
// v4 inter-provider reachability is incidental and unreliable — e.g. sin's
// transit (Cyberjet) has no route to tpe's v4 /24, so the v4 probe blackholed
// (~2s -W timeout every scrape, inflating sin's scrape to ~2.6s) even though
// the v6 backbone path is fine. Nodes without an anchor (pop-01: bird not yet
// announcing) fall back to their v4 address.
var ncnProbeV6 = map[string]string{
	"pop-03": "2001:db8:51::1",
	"pop-04": "2001:db8:51::2",
	"ctrl-01": "2001:db8:53::1",
	"pop-06": "2001:db8:54::1",
	"pop-05": "2001:db8:55::1",
	"pop-08": "2001:db8:56::1",
}

func (f *fleetScraper) probeTargetsFor(nodeID string) []probeTarget {
	out := []probeTarget{
		{"cloudflare-v4", "1.1.1.1", "ping4"},
		{"google-v4", "8.8.8.8", "ping4"},
		{"cloudflare-v6", "2606:4700:4700::1111", "ping6"},
	}
	for _, n := range f.nodes {
		if n.ID == nodeID {
			continue
		}
		name := "ncn/" + strings.ReplaceAll(n.ID, "-", "")
		if v6 := ncnProbeV6[n.ID]; v6 != "" {
			out = append(out, probeTarget{Name: name, Target: v6, Type: "ping6"})
		} else {
			out = append(out, probeTarget{Name: name, Target: n.Address, Type: "ping4"})
		}
	}
	// Operator-defined SLA targets — every PoP probes them so we can compute
	// real per-PoP availability/loss/latency SLOs (sla.go). Prefixed "sla:" so
	// the SLA store + status page can pick them out of the probe set.
	out = append(out, slaProbeTargets()...)
	return out
}

// RebuildProbes re-derives every node's probe set (e.g. after the SLA target
// list changes) and reconciles each node's probe map, preserving RTT history
// for unchanged targets. Safe to call at runtime.
func (f *fleetScraper) RebuildProbes() {
	f.mu.Lock()
	f.rebuildProbesLocked()
	f.mu.Unlock()
}

func (f *fleetScraper) Start(ctx context.Context) {
	log.Printf("fleet: starting scraper for %d nodes", len(f.nodes))
	go func() {
		// Initial scrape runs in-goroutine (was synchronous in Start) so it
		// doesn't block main() from reaching srv.Serve(). On a socket-
		// activated restart the new process must start accepting the queued
		// connections fast; scraping 6 PoPs first added seconds of delay and
		// caused client timeouts (curl 000) during deploys. Handlers already
		// tolerate an empty cache for the ~first scrape.
		f.refreshAll()
		t := time.NewTicker(fleetTick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				f.refreshAll()
			}
		}
	}()

	// Periodic agent TLS cert expiry sweep — once at startup, then every
	// 24h. Cheap (4 TLS dials), bounded by 5s each. Drives the
	// agent-cert-expiring alert. 24h cadence is far below the 30-day
	// alert window, so an operator gets ≥29 days of warnings before
	// any cert actually goes bad.
	go func() {
		f.checkAgentCertExpiry()
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				f.checkAgentCertExpiry()
			}
		}
	}()
}

func (f *fleetScraper) refreshAll() {
	start := time.Now()
	var wg sync.WaitGroup
	// Snapshot the node list so a concurrent add/remove (admin node API) can't
	// tear the slice mid-tick. A node removed after the snapshot still gets
	// scraped this tick, but its result is discarded below (mem gone).
	for _, n := range f.nodesSnapshot() {
		wg.Add(1)
		go func(node fleetNode) {
			defer wg.Done()
			s := f.scrapeOne(node)
			if s == nil {
				return // node was removed between snapshot and scrape
			}
			f.mu.Lock()
			// Only cache if the node is still live — otherwise a racing
			// removeNode already dropped it and we'd resurrect a stale entry.
			if _, ok := f.mem[node.ID]; ok {
				f.cache[node.ID] = s
			}
			f.mu.Unlock()
			if !s.OK {
				log.Printf("fleet: scrape %s FAIL · %s", node.ID, s.Error)
			} else {
				log.Printf("fleet: scrape %s OK · load=%.2f mem=%.0f%% cpu=%.0f%% disk=%.0f%% bird=%s peers=%d wg=%d tun=%d in %s",
					node.ID, s.Load1, s.MemPct, s.CPUPct, s.DiskPct, s.BirdVer, len(s.Protocols), len(s.WG), len(s.Tunnels), s.Latency)
			}
		}(n)
	}
	wg.Wait()
	log.Printf("fleet: refreshAll complete in %s", time.Since(start).Round(time.Millisecond))
}

func (f *fleetScraper) scrapeOne(node fleetNode) *fleetNodeStatus {
	start := time.Now()
	status := &fleetNodeStatus{Node: node, FetchedAt: time.Now().Unix()}
	f.mu.RLock()
	mem := f.mem[node.ID]
	f.mu.RUnlock()
	if mem == nil {
		return nil // node removed (decommission/delete) — skip this tick
	}

	if node.Local {
		f.scrapeLocal(status, mem)
		status.Latency = time.Since(start).Round(time.Millisecond).String()
		mem.consecFail = 0 // local /proc reads don't fail
		f.recordSeries(mem, status)
		return status
	}

	// Remote scrape — REST agent fetch (with in-tick reconnect), output
	// divided by sentinel.
	if !f.scrapeRemote(node, status, mem) {
		status.Latency = time.Since(start).Round(time.Millisecond).String()
		mem.consecFail++
		status.ConsecFail = mem.consecFail
		return status
	}
	status.Latency = time.Since(start).Round(time.Millisecond).String()
	status.OK = true
	mem.consecFail = 0
	f.recordSeries(mem, status)
	return status
}

// ---- Local node ------------------------------------------------------------

func (f *fleetScraper) scrapeLocal(status *fleetNodeStatus, mem *fleetNodeMem) {
	f.local.mu.RLock()
	status.OK = true
	status.Hostname = f.local.hostname
	status.Iface = f.local.iface
	status.Load1 = latestVal(f.local.load1)
	status.MemPct = latestVal(f.local.memPct)
	status.CPUPct = latestVal(f.local.cpu)
	status.DiskPct = latestVal(f.local.diskPct)
	status.NetRxBps = latestVal(f.local.netRx)
	status.NetTxBps = latestVal(f.local.netTx)
	status.MemTotal = f.local.memTotal
	status.DiskTotal = f.local.diskTotal
	if status.MemTotal > 0 {
		status.MemUsed = uint64(float64(status.MemTotal) * status.MemPct / 100)
	}
	if status.DiskTotal > 0 {
		status.DiskUsed = uint64(float64(status.DiskTotal) * status.DiskPct / 100)
	}
	mem.iface = f.local.iface
	f.local.mu.RUnlock()

	f.local.bird.mu.RLock()
	status.BirdVer = f.local.bird.version
	status.Protocols = append([]birdProtocol(nil), f.local.bird.protocols...)
	status.RouteCounts = append([]birdRouteCount(nil), f.local.bird.routeCounts...)
	f.local.bird.mu.RUnlock()

	// WG show (local).
	wgRaw, _, _, _ := runCmd(4*time.Second, "sudo", "-n", "wg", "show")
	status.WG = parseWireguardShow(wgRaw)

	// GRE / VXLAN tunnels (local).
	status.Tunnels = scrapeTunnelsLocal()

	// Probes — copy the Monitor's high-resolution probe state directly.
	status.Probes = make([]probeOut, 0, len(f.local.probes))
	for _, p := range f.local.probes {
		p.mu.RLock()
		status.Probes = append(status.Probes, probeOut{
			Name:     p.Name,
			Target:   p.Target,
			Type:     p.Type,
			LastOK:   p.LastOK,
			LastMS:   p.LastMS,
			LastTime: p.LastTime,
			Series:   p.Series.snapshot(),
		})
		p.mu.RUnlock()
	}

	// Uptime string.
	out, _, _, _ := runCmd(2*time.Second, "uptime")
	status.Uptime = strings.TrimSpace(out)
}

// ---- Remote node -----------------------------------------------------------

// scrapeRemote fetches a node's snapshot via the REST agent and parses
// the result. SSH transport was removed in Phase 3 (2026-05-28) once all
// four remote PoPs ran stable on REST.
//
// If the agent endpoint can't be reached — agent down, cert problem,
// HMAC mismatch, network blip — the error is surfaced via status.Error
// and shows up in the Fleet UI for that one node. Recovery is automatic
// on the next 15s tick once the agent is back. There is intentionally
// no SSH fallback: keeping two transports alive meant maintaining two
// codepaths' worth of edge cases (rc-file noise, ssh-known-hosts, dual
// auth) for a safety net that hadn't fired since the cutover. term.go
// still uses SSH for the interactive terminal — that's a different
// surface (long-lived stream, operator-initiated) and stays.
//
// Hard requirement: f.agentClient and f.agentKeys[node.ID] must both
// be populated. If they aren't, initAgentTransport failed at startup
// and we surface that as a clear error rather than silently breaking.
func (f *fleetScraper) scrapeRemote(node fleetNode, status *fleetNodeStatus, mem *fleetNodeMem) bool {
	if f.agentClient == nil {
		status.Error = "agent transport not initialised (missing /etc/ncn-core-console/agent-ca/ca.crt?)"
		return false
	}
	// Take a snapshot of the HMAC key under the same RLock the rest of
	// fleetScraper uses for shared state — ReloadAgentKeys (SIGHUP)
	// swaps the whole map under f.mu.Lock(), so reads need RLock to
	// avoid the Go map data-race panic.
	f.mu.RLock()
	key := f.agentKeys[node.ID]
	f.mu.RUnlock()
	if key == nil {
		status.Error = "no agent HMAC key on tyo for " + node.ID + " (re-run agent-node-provision.sh?)"
		return false
	}
	// In-tick reconnect: retry the fetch, but bound EVERY attempt by one
	// shared 13s deadline so the whole scrape (including retries) can never
	// run past the 15s tick. A fast failure (connection refused, TLS reset,
	// agent mid-restart, HMAC nonce race) returns in well under a second, so
	// the retries land inside the budget and usually recover on attempt 2 —
	// the node never registers as down. A genuine timeout exhausts the
	// deadline on the first attempt; ctx.Err() then short-circuits the loop
	// so we don't waste time on doomed retries, and the next 15s tick is the
	// real reconnect.
	ctx, cancel := context.WithTimeout(context.Background(), scrapeBudget)
	defer cancel()
	var out []byte
	var err error
	for attempt := 1; attempt <= scrapeMaxAttempts; attempt++ {
		out, err = f.fetchRemoteRESTWithKey(ctx, node, mem, key)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			break // shared deadline hit (timeout) — leave the retry to next tick
		}
		if attempt < scrapeMaxAttempts {
			select {
			case <-time.After(scrapeRetryBackoff):
			case <-ctx.Done():
			}
		}
	}
	if err != nil {
		status.Error = err.Error()
		return false
	}
	ok := f.parseSnapshot(out, node, status, mem)
	if ok {
		// Annotate with the cert expiry reading from the most recent
		// 24h sweep (or 0 if none yet — alert rule treats 0 as unknown).
		f.mu.RLock()
		status.AgentCertDaysLeft = f.agentCertDaysLeft[node.ID]
		f.mu.RUnlock()
	}
	return ok
}

// fetchRemoteREST calls the ncn-agent endpoint on the PoP and returns the
// same byte stream the SSH pipeline would have produced. Wire format and
// auth scheme are documented at the top of agent/main.go. Server URL uses
// the public IP (not a DNS name) for two reasons:
//
//  1. Most PoPs don't have a *.example.com DNS record yet (Phase 0 rollout).
//  2. SAN on the agent cert includes the IP, so cert verification works
//     against an IP literal as long as we pass that IP in the URL host
//     part and crypto/tls is happy with IP SANs (it is).
func (f *fleetScraper) fetchRemoteRESTWithKey(ctx context.Context, node fleetNode, mem *fleetNodeMem, key []byte) ([]byte, error) {
	if len(key) == 0 {
		return nil, fmt.Errorf("no agent key for %s", node.ID)
	}

	url := fmt.Sprintf("https://%s:9101/v1/snapshot", node.Address)
	method := http.MethodGet
	path := "/v1/snapshot"

	// Probe list → X-NCN-Probes header (pipe-delimited triples,
	// comma-separated). Same shape parsed by agent.go parseProbeHeader.
	xprobes := encodeProbeHeader(mem.probeTargets)

	// Nonce: 24 random bytes → 32-char base64url. Sufficient uniqueness
	// for the agent's 10-minute replay window; collision probability is
	// astronomically lower than the wire risk.
	var nonceBytes [24]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		return nil, fmt.Errorf("nonce gen: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes[:])
	ts := time.Now().Unix()

	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%d\n%s\n%s\n%s\n%s", ts, nonce, method, path, xprobes)
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	// ctx carries the shared per-tick deadline (scrapeBudget) set by
	// scrapeRemote, so multiple reconnect attempts collectively stay within
	// the tick budget. The http.Client's own 14s Timeout remains a backstop
	// in case ctx ever arrives without a deadline.
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization",
		fmt.Sprintf("NCNHMAC ts=%d,nonce=%s,sig=%s", ts, nonce, sig))
	if xprobes != "" {
		req.Header.Set("X-NCN-Probes", xprobes)
	}
	req.Header.Set("User-Agent", "ncn-api/fleet-scraper")

	resp, err := f.agentClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10)) // 256KB cap
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		n := 160
		if n > len(body) {
			n = len(body)
		}
		return nil, fmt.Errorf("agent %d: %s", resp.StatusCode, strings.TrimSpace(string(body[:n])))
	}
	return body, nil
}

// encodeProbeHeader serialises the per-node probe target list into the
// pipe-delimited triples comma-separated format the agent expects. Empty
// list → empty string; the agent treats that as "no probes this snapshot"
// and still emits the trailing `___SEP___` so the section index lines up.
func encodeProbeHeader(probes []probeTarget) string {
	if len(probes) == 0 {
		return ""
	}
	var b strings.Builder
	for i, p := range probes {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(p.Name)
		b.WriteByte('|')
		b.WriteString(p.Target)
		b.WriteByte('|')
		b.WriteString(p.Type)
	}
	return b.String()
}

// parseSnapshot consumes the 15-section ___SEP___-delimited byte stream
// produced by ncn-agent and fills in status + mem. Returns false with a
// status.Error message if the section count is wrong.
//
// (The pre-Phase-3 SSH path needed a stripShellNoise() pass to filter
// out rc-file spam — pop-04's /root/.bashrc emits "tg: command not
// found" on every non-interactive ssh, which leaked into CombinedOutput.
// The agent runs /bin/sh -c directly with no rc files sourced, so the
// output is clean by construction and the filter is gone.)
func (f *fleetScraper) parseSnapshot(out []byte, node fleetNode, status *fleetNodeStatus, mem *fleetNodeMem) bool {
	sections := strings.Split(string(out), "___SEP___\n")
	if len(sections) < 15 {
		status.Error = fmt.Sprintf("unexpected output (sections=%d)", len(sections))
		return false
	}

	status.Hostname = strings.TrimSpace(sections[0])

	// loadavg
	if fields := strings.Fields(sections[1]); len(fields) >= 1 {
		var v float64
		fmt.Sscanf(fields[0], "%f", &v)
		status.Load1 = v
	}

	// meminfo (kB → bytes)
	var totalKB, availKB uint64
	for _, line := range strings.Split(sections[2], "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fmt.Sscanf(line, "MemTotal: %d kB", &totalKB)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			fmt.Sscanf(line, "MemAvailable: %d kB", &availKB)
		}
	}
	if totalKB > 0 {
		status.MemTotal = totalKB * 1024
		status.MemUsed = (totalKB - availKB) * 1024
		status.MemPct = float64(totalKB-availKB) / float64(totalKB) * 100
	}

	// bird protocols + version
	birdVer, protos := parseBirdProtocols(sections[3])
	status.BirdVer = birdVer
	status.Protocols = protos

	// wg
	status.WG = parseWireguardShow(sections[4])

	// uptime
	status.Uptime = strings.TrimSpace(sections[5])

	// /proc/stat CPU delta
	idle, totalCPU := parseStatLine(sections[6])
	now := time.Now()
	if mem.prevTotal > 0 && totalCPU > mem.prevTotal {
		dTotal := totalCPU - mem.prevTotal
		dIdle := uint64(0)
		if idle > mem.prevIdle {
			dIdle = idle - mem.prevIdle
		}
		if dTotal > 0 {
			status.CPUPct = float64(dTotal-dIdle) / float64(dTotal) * 100
		}
	}
	mem.prevIdle, mem.prevTotal = idle, totalCPU

	// /proc/net/dev → bytes/sec
	iface, rx, tx := pickDefaultNetDev(sections[7], mem.iface)
	if iface != "" {
		mem.iface = iface
		status.Iface = iface
		if !mem.prevAt.IsZero() {
			dt := now.Sub(mem.prevAt).Seconds()
			if dt > 0 {
				if rx >= mem.prevRx {
					status.NetRxBps = float64(rx-mem.prevRx) / dt
				}
				if tx >= mem.prevTx {
					status.NetTxBps = float64(tx-mem.prevTx) / dt
				}
			}
		}
		mem.prevRx, mem.prevTx = rx, tx
	}
	// Per-interface breakdown: rate every non-lo interface (incl. wg*/tun*)
	// against its own previous cumulative counter, using the same dt window.
	if mem.ifacePrev == nil {
		mem.ifacePrev = map[string][2]uint64{}
	}
	allRows := parseAllNetDev(sections[7])
	if !mem.prevAt.IsZero() {
		if dt := now.Sub(mem.prevAt).Seconds(); dt > 0 {
			ifs := make([]ifaceStat, 0, len(allRows))
			for _, r := range allRows {
				st := ifaceStat{Name: r.name, RxTotal: r.rx, TxTotal: r.tx}
				if prev, ok := mem.ifacePrev[r.name]; ok {
					if r.rx >= prev[0] {
						st.RxBps = float64(r.rx-prev[0]) / dt
					}
					if r.tx >= prev[1] {
						st.TxBps = float64(r.tx-prev[1]) / dt
					}
				}
				ifs = append(ifs, st)
			}
			sort.Slice(ifs, func(i, j int) bool { return ifs[i].RxBps+ifs[i].TxBps > ifs[j].RxBps+ifs[j].TxBps })
			status.Ifaces = ifs
		}
	}
	for _, r := range allRows {
		mem.ifacePrev[r.name] = [2]uint64{r.rx, r.tx}
	}
	mem.prevAt = now

	// df → disk
	dTotal, dUsed, dPct := parseDfLine(sections[8])
	status.DiskTotal = dTotal
	status.DiskUsed = dUsed
	status.DiskPct = dPct

	// birdc show route count
	status.RouteCounts = parseBirdRouteCount(sections[9])

	// GRE / VXLAN tunnels — 4 sections of `ip -d -j link show type <kind>`.
	var tunnels []netTunnel
	tunnels = append(tunnels, parseIPLinkJSON(sections[10])...) // gre
	tunnels = append(tunnels, parseIPLinkJSON(sections[11])...) // gretap
	tunnels = append(tunnels, parseIPLinkJSON(sections[12])...) // ip6gre
	tunnels = append(tunnels, parseIPLinkJSON(sections[13])...) // vxlan
	status.Tunnels = tunnels

	// probes (PROBE name target type ms per line)
	status.Probes = f.parseProbes(sections[14], mem)

	// Section 15 (Phase 4+ agents) — `birdc show protocols all` raw text.
	// Cached per-node so the BIRD detail handler can grep out one
	// protocol's block without re-shelling out via SSH. Tolerated if
	// absent: older agents emit 15 sections and the cache just stays
	// empty for that node.
	if len(sections) >= 16 {
		mem.birdDetailMu.Lock()
		mem.birdDetailRaw = sections[15]
		mem.birdDetailMu.Unlock()
	}

	return true
}

// ---- Parsers ---------------------------------------------------------------
//
// (buildRemoteScript / shEscape / stripShellNoise were here in pre-Phase-3
// versions. The snapshot pipeline is now executed by ncn-agent at the PoP;
// fleet.go only parses what comes back. The agent has its own copy of the
// pipeline at agent/main.go snapshotPipelineStatic — when extending the
// pipeline, edit both, bump the agent version, and re-provision.)

func parseStatLine(raw string) (idle, total uint64) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "cpu ") && !strings.HasPrefix(line, "cpu\t") {
			continue
		}
		fields := strings.Fields(line)
		for i := 1; i < len(fields); i++ {
			v, _ := strconv.ParseUint(fields[i], 10, 64)
			total += v
			if i == 4 {
				idle = v
			}
		}
		return
	}
	return
}

func pickDefaultNetDev(raw, hint string) (iface string, rx, tx uint64) {
	type row struct {
		name   string
		rx, tx uint64
	}
	var rows []row
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "Inter") || strings.HasPrefix(line, "face") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		name := strings.TrimSpace(parts[0])
		if name == "lo" || name == "" {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(parts[1]))
		if len(fields) < 9 {
			continue
		}
		rxV, _ := strconv.ParseUint(fields[0], 10, 64)
		txV, _ := strconv.ParseUint(fields[8], 10, 64)
		rows = append(rows, row{name, rxV, txV})
	}
	if hint != "" {
		for _, r := range rows {
			if r.name == hint {
				return r.name, r.rx, r.tx
			}
		}
	}
	for _, r := range rows {
		if strings.HasPrefix(r.name, "wg") || strings.HasPrefix(r.name, "tun") {
			continue
		}
		if r.rx > 0 || r.tx > 0 {
			return r.name, r.rx, r.tx
		}
	}
	if len(rows) > 0 {
		return rows[0].name, rows[0].rx, rows[0].tx
	}
	return "", 0, 0
}

type netDevRow struct {
	name   string
	rx, tx uint64
}

// parseAllNetDev returns the cumulative rx/tx byte counters for every non-lo
// interface in /proc/net/dev (including wg*/tun*, which pickDefaultNetDev
// skips) — the raw material for the per-interface traffic breakdown.
func parseAllNetDev(raw string) []netDevRow {
	var rows []netDevRow
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "Inter") || strings.HasPrefix(line, "face") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		name := strings.TrimSpace(parts[0])
		if name == "lo" || name == "" {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(parts[1]))
		if len(fields) < 9 {
			continue
		}
		rxV, _ := strconv.ParseUint(fields[0], 10, 64)
		txV, _ := strconv.ParseUint(fields[8], 10, 64)
		rows = append(rows, netDevRow{name, rxV, txV})
	}
	return rows
}

// `df -PB1 / | tail -1` →
//
//	/dev/sda1 12345678901 2345678901 9876543210 20% /
func parseDfLine(raw string) (total, used uint64, pct float64) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		total, _ = strconv.ParseUint(fields[1], 10, 64)
		used, _ = strconv.ParseUint(fields[2], 10, 64)
		if total > 0 {
			pct = float64(used) / float64(total) * 100
		}
		return
	}
	return
}

// Parse PROBE lines emitted by the SSH probe section. Each line:
//
//	PROBE <name> <target> <ping4|ping6> <ms or -1>
func (f *fleetScraper) parseProbes(raw string, mem *fleetNodeMem) []probeOut {
	now := time.Now().Unix()
	gotResults := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "PROBE ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		name := fields[1]
		// fields[2] is target, fields[3] is type — already known from config
		msStr := fields[4]
		ms, _ := strconv.ParseFloat(msStr, 64)
		ok := msStr != "-1" && ms > 0
		p, found := mem.probes[name]
		if !found {
			continue
		}
		p.mu.Lock()
		p.lastOK = ok
		if ok {
			p.lastMS = ms
			p.series.push(ms)
		} else {
			p.lastMS = 0
			p.series.push(-1)
		}
		p.lastTime = now
		p.mu.Unlock()
		gotResults[name] = true
	}

	out := make([]probeOut, 0, len(mem.probeTargets))
	for _, t := range mem.probeTargets {
		p := mem.probes[t.Name]
		p.mu.Lock()
		out = append(out, probeOut{
			Name:     t.Name,
			Target:   t.Target,
			Type:     t.Type,
			LastOK:   p.lastOK,
			LastMS:   p.lastMS,
			LastTime: p.lastTime,
			Series:   p.series.snapshot(),
		})
		p.mu.Unlock()
	}
	return out
}

// ---- Series buffer ---------------------------------------------------------

func (f *fleetScraper) recordSeries(mem *fleetNodeMem, status *fleetNodeStatus) {
	if !status.OK {
		return
	}
	mem.load.push(status.Load1)
	mem.mem.push(status.MemPct)
	mem.cpu.push(status.CPUPct)
	mem.disk.push(status.DiskPct)
	mem.netRx.push(status.NetRxBps)
	mem.netTx.push(status.NetTxBps)
	status.LoadSeries = mem.load.snapshot()
	status.MemSeries = mem.mem.snapshot()
	status.CPUSeries = mem.cpu.snapshot()
	status.DiskSeries = mem.disk.snapshot()
	status.NetRxSeries = mem.netRx.snapshot()
	status.NetTxSeries = mem.netTx.snapshot()
}

// ---- HTTP handlers ---------------------------------------------------------

// fleetPublicNode is the sanitized per-node view exposed to the public
// landing page. No internal hostnames, IPs, peer names, WG keys or tunnel
// endpoints are leaked here — only headline counts and the anchor latency.
type fleetPublicNode struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	Country     string  `json:"country"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	OK          bool    `json:"ok"`
	BGPSessions int     `json:"bgp_sessions"`
	BGPTotal    int     `json:"bgp_total"`
	RoutesV6    int     `json:"routes_v6"`
	WGCount     int     `json:"wg_count"`
	TunnelCount int     `json:"tunnel_count"`
	AnchorMs    float64 `json:"anchor_ms"` // mean RTT to cloudflare-v4 + google-v4
}

type fleetPublic struct {
	Nodes       []fleetPublicNode `json:"nodes"`
	PoPsOnline  int               `json:"pops_online"`
	PoPsTotal   int               `json:"pops_total"`
	BGPSessions int               `json:"bgp_sessions"`  // sum of established across fleet
	RoutesV6    int               `json:"routes_v6"`     // max across nodes (best feed we have)
	WGTotal     int               `json:"wg_total"`
	Tunnels     int               `json:"tunnels"`       // WG + GRE/VXLAN combined
	UpdatedAt   int64             `json:"updated_at"`
}

// snapshotNodes returns a shallow copy of the per-node status cache. The
// alert engine reads from this on every tick so it can evaluate rules
// against ALL PoPs (tyo/fra/hkg) — not just the local in-process
// monitor. Caller gets a slice ordered the same as `f.nodes` so the UI's
// per-rule output sorts predictably; entries with no scrape yet are
// included as zero-value pointers (caller must nil-check).
func (f *fleetScraper) snapshotNodes() []*fleetNodeStatus {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]*fleetNodeStatus, 0, len(f.nodes))
	for _, n := range f.nodes {
		out = append(out, f.cache[n.ID])
	}
	return out
}

// /api/v1/fleet/public — unauthenticated, safe for the landing page.
func (f *fleetScraper) handlePublic(w http.ResponseWriter, _ *http.Request) {
	f.mu.RLock()
	out := fleetPublic{PoPsTotal: len(f.nodes)}
	for _, n := range f.nodes {
		s := f.cache[n.ID]
		if s == nil {
			out.Nodes = append(out.Nodes, fleetPublicNode{
				ID: n.ID, Label: n.Label, Country: n.Country, Lat: n.Lat, Lon: n.Lon,
				OK: false,
			})
			continue
		}

		bgpEst, bgpTot := 0, 0
		for _, p := range s.Protocols {
			if p.Proto != "BGP" {
				continue
			}
			bgpTot++
			if p.Healthy {
				bgpEst++
			}
		}

		var routesV6 int
		for _, rc := range s.RouteCounts {
			if strings.Contains(rc.Table, "6") && rc.Count > routesV6 {
				routesV6 = rc.Count
			}
		}

		// Mean RTT to public anchors — gives a "from this PoP, the internet
		// is X ms away" signal without exposing peer relationships.
		var sumMs float64
		var nMs int
		for _, pr := range s.Probes {
			if pr.LastOK && (pr.Name == "cloudflare-v4" || pr.Name == "google-v4") {
				sumMs += pr.LastMS
				nMs++
			}
		}
		var anchor float64
		if nMs > 0 {
			anchor = sumMs / float64(nMs)
		}

		pn := fleetPublicNode{
			ID: n.ID, Label: n.Label, Country: n.Country, Lat: n.Lat, Lon: n.Lon,
			OK:          s.OK,
			BGPSessions: bgpEst,
			BGPTotal:    bgpTot,
			RoutesV6:    routesV6,
			WGCount:     len(s.WG),
			TunnelCount: len(s.Tunnels),
			AnchorMs:    anchor,
		}
		out.Nodes = append(out.Nodes, pn)

		if s.OK {
			out.PoPsOnline++
			out.BGPSessions += bgpEst
			out.WGTotal += len(s.WG)
			out.Tunnels += len(s.WG) + len(s.Tunnels)
			if routesV6 > out.RoutesV6 {
				out.RoutesV6 = routesV6
			}
			if s.FetchedAt > out.UpdatedAt {
				out.UpdatedAt = s.FetchedAt
			}
		}
	}
	f.mu.RUnlock()

	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
}

// latencyEdge is one directed PoP→PoP RTT sample (ICMP round-trip, ms).
type latencyEdge struct {
	From  string  `json:"from"`
	To    string  `json:"to"`
	RTTMs float64 `json:"rtt_ms"`
}

// handleLatency — GET /api/v1/status/latency (unauthenticated). The live
// inter-PoP RTT matrix behind the topology map's click-to-show-latency.
// Each PoP's agent already pings every other PoP (probeTargetsFor → ncn/<id>),
// so this just reshapes the cached probe results into directed edges. Only
// our own PoP↔PoP mesh is exposed — no transit/peer relationships.
func (f *fleetScraper) handleLatency(w http.ResponseWriter, _ *http.Request) {
	edges := []latencyEdge{}
	f.mu.RLock()
	// "pop04" (probe-name suffix) → "pop-04" (node id)
	idBySuffix := make(map[string]string, len(f.nodes))
	for _, n := range f.nodes {
		idBySuffix[strings.ReplaceAll(n.ID, "-", "")] = n.ID
	}
	for _, n := range f.nodes {
		s := f.cache[n.ID]
		if s == nil {
			continue
		}
		for _, p := range s.Probes {
			suf, ok := strings.CutPrefix(p.Name, "ncn/")
			if !ok || !p.LastOK || p.LastMS <= 0 {
				continue
			}
			if to := idBySuffix[suf]; to != "" {
				edges = append(edges, latencyEdge{From: n.ID, To: to, RTTMs: p.LastMS})
			}
		}
	}
	f.mu.RUnlock()
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"edges": edges}})
}

func (f *fleetScraper) handleFleet(w http.ResponseWriter, _ *http.Request) {
	f.mu.RLock()
	out := make([]*fleetNodeStatus, 0, len(f.nodes))
	for _, n := range f.nodes {
		if s := f.cache[n.ID]; s != nil {
			out = append(out, s)
		}
	}
	f.mu.RUnlock()
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
}

// lgNodeSessions is one PoP's BGP session list for the public Looking Glass.
type lgNodeSessions struct {
	ID       string         `json:"id"`
	Label    string         `json:"label"`
	Country  string         `json:"country"`
	Local    bool           `json:"local"`
	Ready    bool           `json:"ready"`
	Sessions []birdSession  `json:"sessions"`
	Counts   map[string]int `json:"counts"`
}

func countSessions(sess []birdSession) map[string]int {
	c := map[string]int{"established": 0, "connect": 0, "passive": 0, "down": 0}
	for _, s := range sess {
		c[s.Status]++
	}
	return c
}

// handleLGSessions — PUBLIC. BGP sessions for EVERY PoP. The local node
// (ctrl-01) uses the in-process bird scraper; every other PoP is parsed from
// the `show protocols all` raw text the fleet agent already caches
// (mem.birdDetailRaw). A PoP on a pre-Phase-4 agent has no raw cached → empty
// session list + ready:false (the frontend shows a "no detail" state). All
// slices kept non-nil (go-nil-slice memory).
func (f *fleetScraper) handleLGSessions(w http.ResponseWriter, _ *http.Request) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	nodes := make([]lgNodeSessions, 0, len(f.nodes))
	for _, n := range f.nodes {
		var sess []birdSession
		ready := false
		if n.Local {
			f.local.bird.mu.RLock()
			sess = append([]birdSession(nil), f.local.bird.sessions...)
			ready = f.local.bird.ready
			f.local.bird.mu.RUnlock()
		} else if mem := f.mem[n.ID]; mem != nil {
			mem.birdDetailMu.Lock()
			raw := mem.birdDetailRaw
			mem.birdDetailMu.Unlock()
			if raw != "" {
				sess = parseBirdSessions(raw)
				ready = true
			}
		}
		if sess == nil {
			sess = []birdSession{}
		}
		nodes = append(nodes, lgNodeSessions{
			ID: n.ID, Label: n.Label, Country: n.Country, Local: n.Local,
			Ready: ready, Sessions: sess, Counts: countSessions(sess),
		})
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"nodes":   nodes,
		"default": "ctrl-01",
	}})
}

// /api/v1/bird/protocol?name=X&node=Y — on-demand SSH dispatch for the
// expandable per-protocol detail view in the UI.
func (f *fleetScraper) handleBirdProtocolDetail(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	nodeID := strings.TrimSpace(r.URL.Query().Get("node"))

	if name == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing ?name="})
		return
	}
	// Strict name validation — we feed this into birdc.
	for _, c := range name {
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-'
		if !ok {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid protocol name"})
			return
		}
	}

	// Resolve the target node by value under the read lock (taking pointers
	// into f.nodes would be unsafe now the slice can be mutated at runtime).
	var target fleetNode
	found := false
	f.mu.RLock()
	for _, n := range f.nodes {
		if (nodeID == "" && n.Local) || (nodeID != "" && n.ID == nodeID) {
			target = n
			found = true
			break
		}
	}
	f.mu.RUnlock()
	if !found {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "unknown node"})
		return
	}

	start := time.Now()
	var (
		out    string
		exit   int
		err    error
		source string // for the debug field: "local" / "cache"
	)
	switch {
	case target.Local:
		// Local node: cheaper to just shell out than wait for the next
		// scrape tick. ctrl-01 has birdc on PATH and sudo NOPASSWD.
		source = "local"
		out, _, exit, err = runCmd(5*time.Second, "sudo", "-n", "birdc", "show", "protocols", "all", name)
	default:
		// Remote node: snapshot cache only. Section 15 of every scrape
		// (every 15s) carries the full `birdc show protocols all` output
		// from the agent; we slice out the requested protocol's block.
		// Phase 4 transitional SSH fallback was removed once all PoPs ran
		// stable on phase4+ agents; if the cache is empty here, it's a
		// real config error (agent down / missing CA / wrong HMAC key),
		// not something we should paper over with an extra SSH path.
		source = "cache"
		f.mu.RLock()
		mem := f.mem[target.ID]
		f.mu.RUnlock()
		if mem == nil {
			err = fmt.Errorf("no scrape state for node %s yet", target.ID)
			break
		}
		mem.birdDetailMu.Lock()
		cached := mem.birdDetailRaw
		mem.birdDetailMu.Unlock()
		if cached == "" {
			err = fmt.Errorf("no BIRD detail cached for %s — check that ncn-agent on the node is running and on phase4+", target.ID)
			break
		}
		block, ok := extractBirdProtocolBlock(cached, name)
		if !ok {
			err = fmt.Errorf("protocol %q not found in %s's BIRD state", name, target.ID)
			break
		}
		out = block
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error() + " · " + out})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"name":     name,
		"node":     target.ID,
		"raw":      out,
		"exit":     exit,
		"source":   source,
		"duration": time.Since(start).String(),
	}})
}

// extractBirdProtocolBlock slices one protocol's detail out of the raw
// `birdc show protocols all` text cached in mem.birdDetailRaw.
//
// birdc output shape:
//
//   BIRD x.y.z ready.
//   Name       Proto      Table      State  Since         Info
//   ospf1      OSPF       master4    up     2026-05-28    Running
//     Channel ipv4
//       State:          UP
//       ...
//   ospf2      OSPF       master4    up     2026-05-28    ...
//     ...
//
// Protocol blocks start at column 0 (the name + summary line). Continuation
// lines for that protocol's detail are indented. The next column-0 line
// (with a different protocol name as field 1) starts the next protocol.
//
// We re-emit the BIRD header + the column header line + the matching
// protocol's block so the response looks like what `birdc show protocols
// all <name>` (single-arg) would have produced — keeps the UI parser
// happy with no changes.
func extractBirdProtocolBlock(raw, name string) (string, bool) {
	if raw == "" {
		return "", false
	}
	lines := strings.Split(raw, "\n")
	var (
		bIRDHeader string
		colHeader  string
		body       strings.Builder
		inBlock    bool
		found      bool
	)
	for i, line := range lines {
		// First line: "BIRD x.y.z ready." (or similar). Capture verbatim.
		if i == 0 && strings.HasPrefix(line, "BIRD ") {
			bIRDHeader = line
			continue
		}
		// Second line: the column header "Name Proto Table State Since Info".
		if colHeader == "" && strings.HasPrefix(line, "Name") {
			colHeader = line
			continue
		}
		// Column-0 line (no leading whitespace, non-empty): a protocol
		// summary row. If it matches `name`, start capturing; if it's a
		// different protocol AFTER we started capturing, stop.
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			fields := strings.Fields(line)
			if len(fields) > 0 && fields[0] == name {
				inBlock = true
				found = true
				body.WriteString(line)
				body.WriteByte('\n')
				continue
			}
			if inBlock {
				break // next protocol started — we're done
			}
			continue
		}
		// Indented continuation line — belongs to whatever block is open.
		if inBlock {
			body.WriteString(line)
			body.WriteByte('\n')
		}
	}
	if !found {
		return "", false
	}
	var b strings.Builder
	if bIRDHeader != "" {
		b.WriteString(bIRDHeader)
		b.WriteByte('\n')
	}
	if colHeader != "" {
		b.WriteString(colHeader)
		b.WriteByte('\n')
	}
	b.WriteString(body.String())
	return b.String(), true
}
