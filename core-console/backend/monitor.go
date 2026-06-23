// Centralized monitoring state for the operator console.
//
//   - Sample CPU, memory, load, disk, network every 5s into ring buffers.
//   - Run connectivity probes (ping) every 30s against configured targets.
//   - Expose snapshots + time-series via HTTP.
//
// Pure stdlib + /proc parsing. No external deps.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ---------------------------------------------------------------------------
// Ring buffer
// ---------------------------------------------------------------------------

type tsSample struct {
	T int64   `json:"t"`
	V float64 `json:"v"`
}

type ringBuf struct {
	mu   sync.RWMutex
	data []tsSample
	head int
	size int
	cap  int
}

func newRing(capacity int) *ringBuf {
	return &ringBuf{data: make([]tsSample, capacity), cap: capacity}
}

func (r *ringBuf) push(v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[r.head] = tsSample{T: time.Now().Unix(), V: v}
	r.head = (r.head + 1) % r.cap
	if r.size < r.cap {
		r.size++
	}
}

func (r *ringBuf) snapshot() []tsSample {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.size == 0 {
		return []tsSample{}
	}
	out := make([]tsSample, r.size)
	start := (r.head - r.size + r.cap) % r.cap
	for i := 0; i < r.size; i++ {
		out[i] = r.data[(start+i)%r.cap]
	}
	return out
}

func (r *ringBuf) latest() (tsSample, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.size == 0 {
		return tsSample{}, false
	}
	return r.data[(r.head-1+r.cap)%r.cap], true
}

// ---------------------------------------------------------------------------
// /proc parsers
// ---------------------------------------------------------------------------

func readCPUStat() (idle, total uint64) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return
	}
	line := strings.SplitN(string(data), "\n", 2)[0]
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

func readMemInfo() (totalKB, availKB uint64) {
	data, _ := os.ReadFile("/proc/meminfo")
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fmt.Sscanf(line, "MemTotal: %d kB", &totalKB)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			fmt.Sscanf(line, "MemAvailable: %d kB", &availKB)
		}
	}
	return
}

func readLoad() (l1, l5, l15 float64) {
	data, _ := os.ReadFile("/proc/loadavg")
	fields := strings.Fields(string(data))
	if len(fields) >= 3 {
		l1, _ = strconv.ParseFloat(fields[0], 64)
		l5, _ = strconv.ParseFloat(fields[1], 64)
		l15, _ = strconv.ParseFloat(fields[2], 64)
	}
	return
}

func readNetDev(iface string) (rx, tx uint64) {
	data, _ := os.ReadFile("/proc/net/dev")
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, iface+":") {
			continue
		}
		after := strings.TrimSpace(strings.TrimPrefix(line, iface+":"))
		fields := strings.Fields(after)
		if len(fields) >= 9 {
			rx, _ = strconv.ParseUint(fields[0], 10, 64)
			tx, _ = strconv.ParseUint(fields[8], 10, 64)
		}
	}
	return
}

func readDisk(path string) (usedPct float64, total, used uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return
	}
	total = st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	used = total - free
	if total > 0 {
		usedPct = float64(used) / float64(total) * 100
	}
	return
}

func defaultIface() string {
	// pick the first non-lo iface that has bytes
	data, _ := os.ReadFile("/proc/net/dev")
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, ":") {
			continue
		}
		name := strings.TrimSpace(strings.SplitN(line, ":", 2)[0])
		if name == "lo" || name == "" || strings.HasPrefix(name, "Inter") {
			continue
		}
		return name
	}
	return "eth0"
}

// ---------------------------------------------------------------------------
// Monitor + probes
// ---------------------------------------------------------------------------

const (
	sampleInterval = 5 * time.Second
	ringCap        = 60 // 5 minutes at 5s
)

type connProbe struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Type   string `json:"type"` // "ping4" | "ping6"

	mu       sync.RWMutex
	LastOK   bool    `json:"last_ok"`
	LastMS   float64 `json:"last_ms"`
	LastTime int64   `json:"last_time"`
	Series   *ringBuf
}

type Monitor struct {
	mu sync.RWMutex

	hostname string
	boot     int64
	iface    string

	cpu, memPct, load1, diskPct, netRx, netTx *ringBuf
	memTotal, diskTotal                       uint64

	prevIdle, prevTotal uint64
	prevRx, prevTx      uint64

	probes []*connProbe

	bird   *birdState
	alerts *alertEngine
}

func NewMonitor() *Monitor {
	hn, _ := os.Hostname()
	m := &Monitor{
		hostname: hn,
		iface:    defaultIface(),
		cpu:      newRing(ringCap),
		memPct:   newRing(ringCap),
		load1:    newRing(ringCap),
		diskPct:  newRing(ringCap),
		netRx:    newRing(ringCap),
		netTx:    newRing(ringCap),
	}
	if st, err := os.Stat("/proc/1"); err == nil {
		m.boot = st.ModTime().Unix()
	}
	m.probes = []*connProbe{
		// External anchors
		{Name: "cloudflare-v4",  Target: "1.1.1.1",              Type: "ping4", Series: newRing(ringCap)},
		{Name: "google-v4",      Target: "8.8.8.8",              Type: "ping4", Series: newRing(ringCap)},
		{Name: "cloudflare-v6",  Target: "2606:4700:4700::1111", Type: "ping6", Series: newRing(ringCap)},
		// NCN intra-network PoPs (probed from this host to monitor inter-PoP RTT).
		// Keep in sync with fleet.go nodes[]. The remote-node probe list is
		// auto-generated by probeTargetsFor(), but ctrl-01's in-process
		// Monitor uses THIS hard-coded list, so new PoPs must be added by
		// hand. (pop-06 was missing here until 2026-05-30; added with pop-05.)
		// Inter-PoP probes target the NCN IPv6 anchors (our backbone), not the
		// providers' v4 IPs — see ncnProbeV6 in fleet.go for why. pop-01 has no
		// anchor yet (bird not announcing) → stays v4.
		{Name: "ncn/pop04",      Target: "2001:db8:51::2",      Type: "ping6", Series: newRing(ringCap)},
		{Name: "ncn/pop03",      Target: "2001:db8:51::1",      Type: "ping6", Series: newRing(ringCap)},
		{Name: "ncn/pop06",      Target: "2001:db8:54::1",      Type: "ping6", Series: newRing(ringCap)},
		{Name: "ncn/pop05",      Target: "2001:db8:55::1",      Type: "ping6", Series: newRing(ringCap)},
		{Name: "ncn/pop01",      Target: "198.51.100.2",          Type: "ping4", Series: newRing(ringCap)},
		{Name: "ncn/pop08",      Target: "2001:db8:56::1",      Type: "ping6", Series: newRing(ringCap)},
	}
	m.bird = newBirdState()
	m.alerts = newAlertEngine(m)
	return m
}

func (m *Monitor) Start(ctx context.Context) {
	m.prevIdle, m.prevTotal = readCPUStat()
	m.prevRx, m.prevTx = readNetDev(m.iface)
	go m.collectLoop(ctx)
	go m.probeLoop(ctx)
	go m.bird.scrapeLoop(ctx)
	go m.alerts.runLoop(ctx)
}

func (m *Monitor) collectLoop(ctx context.Context) {
	t := time.NewTicker(sampleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.collectOnce()
		}
	}
}

func (m *Monitor) collectOnce() {
	idle, total := readCPUStat()
	cpu := 0.0
	if total > m.prevTotal {
		idleD := float64(idle - m.prevIdle)
		totalD := float64(total - m.prevTotal)
		cpu = (1 - idleD/totalD) * 100
		if cpu < 0 {
			cpu = 0
		}
		if cpu > 100 {
			cpu = 100
		}
	}
	m.prevIdle, m.prevTotal = idle, total
	m.cpu.push(cpu)

	totalKB, availKB := readMemInfo()
	if totalKB > 0 {
		m.mu.Lock()
		m.memTotal = totalKB * 1024
		m.mu.Unlock()
		m.memPct.push(float64(totalKB-availKB) / float64(totalKB) * 100)
	}

	l1, _, _ := readLoad()
	m.load1.push(l1)

	pct, total64, _ := readDisk("/")
	m.mu.Lock()
	m.diskTotal = total64
	m.mu.Unlock()
	m.diskPct.push(pct)

	rx, tx := readNetDev(m.iface)
	dt := sampleInterval.Seconds()
	rxBps := float64(rx-m.prevRx) / dt
	txBps := float64(tx-m.prevTx) / dt
	if rxBps < 0 {
		rxBps = 0
	}
	if txBps < 0 {
		txBps = 0
	}
	m.prevRx, m.prevTx = rx, tx
	m.netRx.push(rxBps)
	m.netTx.push(txBps)
}

func (m *Monitor) probeLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	m.runProbes()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.runProbes()
		}
	}
}

func (m *Monitor) runProbes() {
	var wg sync.WaitGroup
	for _, p := range m.probes {
		wg.Add(1)
		go func(p *connProbe) {
			defer wg.Done()
			ok, ms := runPingProbe(p.Target, p.Type == "ping6")
			p.mu.Lock()
			p.LastOK = ok
			p.LastMS = ms
			p.LastTime = time.Now().Unix()
			p.mu.Unlock()
			if ok {
				p.Series.push(ms)
			} else {
				p.Series.push(-1)
			}
		}(p)
	}
	wg.Wait()
}

func runPingProbe(target string, v6 bool) (ok bool, ms float64) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	args := []string{"-c", "2", "-W", "2", "-n"}
	if v6 {
		args = append([]string{"-6"}, args...)
	} else {
		args = append([]string{"-4"}, args...)
	}
	args = append(args, target)
	out, err := exec.CommandContext(ctx, "ping", args...).CombinedOutput()
	if err != nil {
		return false, 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "rtt ") {
			continue
		}
		parts := strings.Split(line, "=")
		if len(parts) < 2 {
			continue
		}
		vals := strings.Fields(parts[1])
		if len(vals) == 0 {
			continue
		}
		nums := strings.Split(vals[0], "/")
		if len(nums) >= 2 {
			ms, _ = strconv.ParseFloat(nums[1], 64)
			return true, ms
		}
	}
	return false, 0
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

type perfSnapshot struct {
	Hostname  string  `json:"hostname"`
	Iface     string  `json:"iface"`
	BootEpoch int64   `json:"boot_epoch"`
	CPUPct    float64 `json:"cpu_pct"`
	MemPct    float64 `json:"mem_pct"`
	MemTotal  uint64  `json:"mem_total"`
	Load1     float64 `json:"load_1"`
	DiskPct   float64 `json:"disk_pct"`
	DiskTotal uint64  `json:"disk_total"`
	NetRxBps  float64 `json:"net_rx_bps"`
	NetTxBps  float64 `json:"net_tx_bps"`
}

func latestVal(r *ringBuf) float64 {
	s, ok := r.latest()
	if !ok {
		return 0
	}
	return s.V
}

func (m *Monitor) handlePerf(w http.ResponseWriter, _ *http.Request) {
	m.mu.RLock()
	hostname, iface, boot := m.hostname, m.iface, m.boot
	memTotal, diskTotal := m.memTotal, m.diskTotal
	m.mu.RUnlock()

	snap := perfSnapshot{
		Hostname:  hostname,
		Iface:     iface,
		BootEpoch: boot,
		CPUPct:    latestVal(m.cpu),
		MemPct:    latestVal(m.memPct),
		MemTotal:  memTotal,
		Load1:     latestVal(m.load1),
		DiskPct:   latestVal(m.diskPct),
		DiskTotal: diskTotal,
		NetRxBps:  latestVal(m.netRx),
		NetTxBps:  latestVal(m.netTx),
	}

	resp := map[string]any{
		"snapshot": snap,
		"series": map[string][]tsSample{
			"cpu":   m.cpu.snapshot(),
			"mem":   m.memPct.snapshot(),
			"load":  m.load1.snapshot(),
			"disk":  m.diskPct.snapshot(),
			"netRx": m.netRx.snapshot(),
			"netTx": m.netTx.snapshot(),
		},
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: resp})
}

type probeOut struct {
	Name     string     `json:"name"`
	Target   string     `json:"target"`
	Type     string     `json:"type"`
	LastOK   bool       `json:"last_ok"`
	LastMS   float64    `json:"last_ms"`
	LastTime int64      `json:"last_time"`
	Series   []tsSample `json:"series"`
}

func (m *Monitor) handleConnectivity(w http.ResponseWriter, _ *http.Request) {
	out := make([]probeOut, 0, len(m.probes))
	for _, p := range m.probes {
		p.mu.RLock()
		out = append(out, probeOut{
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

	// Also include WG status (parsed from `wg show`) as part of connectivity.
	wgRaw, _, _, _ := runCmd(4*time.Second, "sudo", "-n", "wg", "show")
	wgTunnels := parseWireguardShow(wgRaw)

	resp := map[string]any{
		"probes":  out,
		"wg":      wgTunnels,
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: resp})
}

// ---------------------------------------------------------------------------
// WireGuard `wg show` parsing
// ---------------------------------------------------------------------------

type wgPeer struct {
	PublicKey     string `json:"public_key"`
	Endpoint      string `json:"endpoint"`
	AllowedIPs    string `json:"allowed_ips"`
	LastHandshake string `json:"last_handshake"`
	Transfer      string `json:"transfer"`
	Keepalive     string `json:"keepalive"`
}

type wgIface struct {
	Name          string   `json:"name"`
	PublicKey     string   `json:"public_key"`
	ListeningPort string   `json:"listening_port"`
	Peers         []wgPeer `json:"peers"`
}

func parseWireguardShow(raw string) []wgIface {
	out := []wgIface{}
	var cur *wgIface
	var pcur *wgPeer
	flushPeer := func() {
		if pcur != nil && cur != nil {
			cur.Peers = append(cur.Peers, *pcur)
			pcur = nil
		}
	}
	flushIface := func() {
		flushPeer()
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(raw, "\n") {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		if strings.HasPrefix(s, "interface:") {
			flushIface()
			cur = &wgIface{Name: strings.TrimSpace(strings.TrimPrefix(s, "interface:"))}
			continue
		}
		if strings.HasPrefix(s, "peer:") {
			flushPeer()
			pcur = &wgPeer{PublicKey: strings.TrimSpace(strings.TrimPrefix(s, "peer:"))}
			continue
		}
		k, v, ok := strings.Cut(s, ":")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		if pcur != nil {
			switch key {
			case "endpoint":
				pcur.Endpoint = val
			case "allowed ips":
				pcur.AllowedIPs = val
			case "latest handshake":
				pcur.LastHandshake = val
			case "transfer":
				pcur.Transfer = val
			case "persistent keepalive":
				pcur.Keepalive = val
			}
		} else if cur != nil {
			switch key {
			case "public key":
				cur.PublicKey = val
			case "listening port":
				cur.ListeningPort = val
			}
		}
	}
	flushIface()
	return out
}

