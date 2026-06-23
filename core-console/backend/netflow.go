// Netflow / sFlow traffic analytics.
//
// PoPs export sampled flows via host-sFlow (hsflowd); a central goflow2 collector
// receives them and appends one JSON object per flow to NCN_FLOW_FILE. This store
// tails that file and keeps an in-memory sliding window (the last flowWindow) of
// per-key byte/packet counters, so the console can show top talkers + traffic
// composition (by src/dst IP, port, protocol, AS) and ingress vs egress.
//
// v1 is a CURRENT-window view (no DB history yet — see flow_agg follow-up). It's
// fully inert until the collector is deployed and starts writing the file: no
// file → empty view, no error. ASN grouping is best-effort: it lights up when
// the collector enriches src_as/dst_as (e.g. a MaxMind ASN DB); otherwise the
// IP/port/proto/direction breakdowns still work.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var globalNetflow *netflowStore

const (
	flowWindow      = 10 * time.Minute // sliding window kept in memory
	flowPollEvery   = 2 * time.Second  // how often we read newly-appended lines
	flowTopN        = 20               // entries returned per dimension
	flowDefaultFile = "/var/log/ncn-flows/flows.jsonl"
)

// goflow2 JSON record (subset). Field names match goflow2's JSON transport.
type flowRecord struct {
	TimeReceivedNs uint64 `json:"time_received_ns"`
	Bytes          uint64 `json:"bytes"`
	Packets        uint64 `json:"packets"`
	SrcAddr        string `json:"src_addr"`
	DstAddr        string `json:"dst_addr"`
	SrcPort        int    `json:"src_port"`
	DstPort        int    `json:"dst_port"`
	Proto          string `json:"proto"` // goflow2 emits a name ("UDP","TCP",…)
	SrcAS          int    `json:"src_as"`
	DstAS          int    `json:"dst_as"`
	SamplerAddress string `json:"sampler_address"`
}

// flowSample is one ingested flow, retained until it ages out of the window.
type flowSample struct {
	at        time.Time
	bytes     uint64
	packets   uint64
	srcIP     string
	dstIP     string
	dstPort   int
	proto     string
	srcAS     int
	dstAS     int
	direction string // "in" | "out" | "transit"
}

type netflowStore struct {
	mu      sync.Mutex
	samples []flowSample
	ourNets []*net.IPNet
	file    string
	offset  int64

	// ASN enrichment cache (softflowd doesn't fill src_as/dst_as, so we resolve
	// top-talker IPs → origin AS via Team Cymru DNS in the background and cache).
	asnMu    sync.Mutex
	asnCache map[string]asnInfo
}

type asnInfo struct {
	asn int
	at  time.Time
}

func newNetflowStore() *netflowStore {
	s := &netflowStore{file: getenvDefault("NCN_FLOW_FILE", flowDefaultFile), asnCache: map[string]asnInfo{}}
	// Our address space, for ingress/egress classification. Default = backbone /32.
	for _, c := range strings.Split(getenvDefault("NCN_OUR_PREFIXES", "2001:db8::/32"), ",") {
		if _, n, err := net.ParseCIDR(strings.TrimSpace(c)); err == nil {
			s.ourNets = append(s.ourNets, n)
		}
	}
	return s
}

func (s *netflowStore) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(flowPollEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.poll()
				s.refreshOurNets(ctx)
			}
		}
	}()
	// Background ASN resolver — keeps the top-talker IPs' origin-AS cached so the
	// /flow/top handler never blocks on DNS.
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.refreshASN(ctx)
			}
		}
	}()
}

// refreshOurNets folds AS64500's currently-announced prefixes into the
// classification set (best-effort, cheap — the RPKI monitor already caches them).
func (s *netflowStore) refreshOurNets(ctx context.Context) {
	if globalRPKI == nil {
		return
	}
	snap := globalRPKI.snapshot()
	if len(snap.Prefixes) == 0 {
		return
	}
	nets := make([]*net.IPNet, 0, len(snap.Prefixes)+2)
	for _, c := range strings.Split(getenvDefault("NCN_OUR_PREFIXES", "2001:db8::/32"), ",") {
		if _, n, err := net.ParseCIDR(strings.TrimSpace(c)); err == nil {
			nets = append(nets, n)
		}
	}
	for _, p := range snap.Prefixes {
		if _, n, err := net.ParseCIDR(p.Prefix); err == nil {
			nets = append(nets, n)
		}
	}
	s.mu.Lock()
	s.ourNets = nets
	s.mu.Unlock()
}

func (s *netflowStore) ours(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range s.ourNets {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// poll reads newly-appended lines from the collector's JSONL file. Handles
// rotation/truncation by resetting to the start when the file shrinks.
func (s *netflowStore) poll() {
	f, err := os.Open(s.file)
	if err != nil {
		return // collector not running yet → nothing to do
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	s.mu.Lock()
	off := s.offset
	s.mu.Unlock()
	if fi.Size() < off {
		off = 0 // rotated/truncated
	}
	if _, err := f.Seek(off, 0); err != nil {
		return
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	now := time.Now()
	var fresh []flowSample
	for sc.Scan() {
		var r flowRecord
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.Bytes == 0 {
			continue
		}
		fs := flowSample{
			at: now, bytes: r.Bytes, packets: r.Packets,
			srcIP: r.SrcAddr, dstIP: r.DstAddr, dstPort: r.DstPort,
			proto: r.Proto, srcAS: r.SrcAS, dstAS: r.DstAS,
		}
		so, do := s.ours(r.SrcAddr), s.ours(r.DstAddr)
		switch {
		case so && !do:
			fs.direction = "out"
		case !so && do:
			fs.direction = "in"
		default:
			fs.direction = "transit"
		}
		fresh = append(fresh, fs)
	}
	newOff, _ := f.Seek(0, 1)

	s.mu.Lock()
	s.offset = newOff
	s.samples = append(s.samples, fresh...)
	// evict anything older than the window
	cut := now.Add(-flowWindow)
	i := 0
	for i < len(s.samples) && s.samples[i].at.Before(cut) {
		i++
	}
	if i > 0 {
		s.samples = s.samples[i:]
	}
	s.mu.Unlock()
}

// ── ASN enrichment (Team Cymru IP→ASN over DNS) ──────────────────────────────

const (
	asnTTL    = 6 * time.Hour    // re-resolve a known IP this infrequently
	asnNegTTL = 30 * time.Minute // don't hammer DNS for IPs that resolved to nothing
	asnPerRun = 40               // cap lookups per refresh cycle
)

// cachedASN returns the cached origin AS for an IP (0 if unknown). Cheap; used by
// the view under no DNS.
func (s *netflowStore) cachedASN(ip string) int {
	s.asnMu.Lock()
	defer s.asnMu.Unlock()
	return s.asnCache[ip].asn
}

func (s *netflowStore) asnFresh(ip string) bool {
	s.asnMu.Lock()
	defer s.asnMu.Unlock()
	e, ok := s.asnCache[ip]
	if !ok {
		return false
	}
	ttl := asnTTL
	if e.asn == 0 {
		ttl = asnNegTTL
	}
	return time.Since(e.at) < ttl
}

// refreshASN resolves the origin AS for the current top-traffic IPs (bounded).
func (s *netflowStore) refreshASN(ctx context.Context) {
	s.mu.Lock()
	byIP := map[string]uint64{}
	for i := range s.samples {
		byIP[s.samples[i].srcIP] += s.samples[i].bytes
		byIP[s.samples[i].dstIP] += s.samples[i].bytes
	}
	s.mu.Unlock()
	type kv struct {
		ip string
		b  uint64
	}
	arr := make([]kv, 0, len(byIP))
	for ip, b := range byIP {
		arr = append(arr, kv{ip, b})
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].b > arr[j].b })
	done := 0
	for _, e := range arr {
		if done >= asnPerRun {
			break
		}
		if e.ip == "" || s.ours(e.ip) || s.asnFresh(e.ip) {
			continue
		}
		asn := cymruASN(ctx, e.ip)
		s.asnMu.Lock()
		s.asnCache[e.ip] = asnInfo{asn: asn, at: time.Now()}
		s.asnMu.Unlock()
		done++
	}
}

func hexDigit(n byte) byte {
	if n < 10 {
		return '0' + n
	}
	return 'a' + (n - 10)
}

// cymruASN resolves an IP's origin AS via Team Cymru's DNS service (no license).
func cymruASN(ctx context.Context, ipStr string) int {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return 0
	}
	var q string
	if v4 := ip.To4(); v4 != nil {
		q = fmt.Sprintf("%d.%d.%d.%d.origin.asn.cymru.com", v4[3], v4[2], v4[1], v4[0])
	} else {
		var b strings.Builder
		v6 := ip.To16()
		for i := 15; i >= 0; i-- {
			b.WriteByte(hexDigit(v6[i] & 0xf))
			b.WriteByte('.')
			b.WriteByte(hexDigit(v6[i] >> 4))
			b.WriteByte('.')
		}
		q = b.String() + "origin6.asn.cymru.com"
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	txts, err := net.DefaultResolver.LookupTXT(cctx, q)
	if err != nil || len(txts) == 0 {
		return 0
	}
	// "13335 | 2606:4700::/32 | US | arin | ..."  (first field may list >1 ASN)
	first := strings.TrimSpace(strings.SplitN(txts[0], "|", 2)[0])
	fields := strings.Fields(first)
	if len(fields) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(fields[0])
	return n
}

// ── view ─────────────────────────────────────────────────────────────────────

type flowEntry struct {
	Key     string `json:"key"`
	Bytes   uint64 `json:"bytes"`
	Packets uint64 `json:"packets"`
}

func protoName(p string) string {
	if p == "" {
		return "?"
	}
	return strings.ToLower(p)
}


func topN(m map[string]*flowEntry, n int) []flowEntry {
	out := make([]flowEntry, 0, len(m))
	for _, e := range m {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func (s *netflowStore) top() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	add := func(m map[string]*flowEntry, k string, b, p uint64) {
		if k == "" {
			return
		}
		e := m[k]
		if e == nil {
			e = &flowEntry{Key: k}
			m[k] = e
		}
		e.Bytes += b
		e.Packets += p
	}
	srcIP := map[string]*flowEntry{}
	dstIP := map[string]*flowEntry{}
	port := map[string]*flowEntry{}
	proto := map[string]*flowEntry{}
	srcAS := map[string]*flowEntry{}
	dstAS := map[string]*flowEntry{}
	var inB, outB, transitB uint64
	for i := range s.samples {
		f := &s.samples[i]
		add(srcIP, f.srcIP, f.bytes, f.packets)
		add(dstIP, f.dstIP, f.bytes, f.packets)
		add(port, protoName(f.proto)+"/"+strconv.Itoa(f.dstPort), f.bytes, f.packets)
		add(proto, protoName(f.proto), f.bytes, f.packets)
		sa := f.srcAS
		if sa == 0 {
			sa = s.cachedASN(f.srcIP)
		}
		if sa != 0 {
			add(srcAS, "AS"+strconv.Itoa(sa), f.bytes, f.packets)
		}
		da := f.dstAS
		if da == 0 {
			da = s.cachedASN(f.dstIP)
		}
		if da != 0 {
			add(dstAS, "AS"+strconv.Itoa(da), f.bytes, f.packets)
		}
		switch f.direction {
		case "in":
			inB += f.bytes
		case "out":
			outB += f.bytes
		default:
			transitB += f.bytes
		}
	}
	return map[string]any{
		"window_secs": int(flowWindow.Seconds()),
		"flows":       len(s.samples),
		"src_ip":      topN(srcIP, flowTopN),
		"dst_ip":      topN(dstIP, flowTopN),
		"port":        topN(port, flowTopN),
		"proto":       topN(proto, flowTopN),
		"src_as":      topN(srcAS, flowTopN),
		"dst_as":      topN(dstAS, flowTopN),
		"in_bytes":    inB,
		"out_bytes":   outB,
		"transit_bytes": transitB,
	}
}

// GET /api/v1/auth/flow/top → top talkers + composition over the current window.
func handleFlowTop(w http.ResponseWriter, _ *http.Request) {
	if globalNetflow == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "netflow not ready"})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: globalNetflow.top()})
}
