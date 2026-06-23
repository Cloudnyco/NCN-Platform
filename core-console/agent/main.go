// ncn-agent — per-PoP telemetry agent.
//
// Replaces the SSH-poll transport that fleet.go on ctrl-01 currently uses to
// scrape each remote PoP every 15s. Same 14-segment shell-pipeline output,
// served over HTTPS with HMAC-bearer auth instead of fanning out ssh
// connections.
//
// Design pillars:
//
//   1. Byte-equal output. The current fleet.go scraper parses 14 sections
//      separated by `___SEP___` lines. The agent runs the SAME pipeline
//      and returns the SAME bytes — fleet.go's parser is untouched. Per-
//      PoP cutover is reversible by flipping a single Transport field.
//
//   2. Standalone module, stdlib only. No imports of core-console-api
//      types — the agent runs on edge PoPs and shouldn't be able to
//      accidentally pull admin-side code into its address space.
//
//   3. HMAC over (ts, nonce, method, path). Replay window 300s; nonce
//      cache for 600s prevents reusing a captured request. No tokens
//      in URLs or query strings; the signature is the whole credential.
//
//   4. Self-terminated TLS via Go's net/http ListenAndServeTLS. No nginx
//      in front — pop-04 doesn't have nginx (xray owns :443 there), and
//      threading every PoP through nginx-with-LE would force matching
//      basebrick on every host. Self-signed CA on tyo signs per-node
//      certs; ncn-api pins the CA.
//
//   5. Read-only. The agent NEVER mutates remote state. /v1/snapshot is
//      `GET` only. All commands it shells out to are read-only birdc /
//      ip / df / sudo -n with stderr captured (sudo -n: no password
//      prompt; if not in sudoers the command fails clean, fleet.go's
//      parser already tolerates that case).
//
// Endpoints:
//
//   GET /v1/healthz   — unauth, returns {ok, version, uptime_s}
//   GET /v1/snapshot  — HMAC-auth, text/plain, 14 sections separated by
//                       lines containing exactly "___SEP___"
//
// Files (all 0600 root, in /etc/ncn-agent/):
//
//   hmac.key  — raw bytes (≥32B random). Same value also at
//               /etc/ncn-core-console/agent-keys/<node-id>.key on tyo.
//   tls.crt   — PEM cert signed by tyo's agent CA.
//   tls.key   — PEM private key for the above.
//   agent.conf — optional key=value overrides (listen addr, node id).
//
// Wire format of the Authorization header on /v1/snapshot:
//
//   Authorization: NCNHMAC ts=<unix>,nonce=<base64url>,sig=<base64url>
//   X-NCN-Probes: name1|target1|type1,name2|target2|type2,...
//
// X-NCN-Probes carries the probe target list for THIS snapshot. The list
// is determined by the central console (it knows the fleet topology) and
// pushed per request — the agent stays stateless about which other PoPs
// exist, so adding/removing a node doesn't require re-provisioning every
// other agent's config. Empty header = no probes run, snapshot still
// returns sections 1-14 plus an empty section 15.
//
// Server computes:
//
//   sig = HMAC-SHA256(hmac.key,
//                     ts + "\n" + nonce + "\n" + METHOD + "\n" + PATH + "\n" + xprobes)
//
// Where xprobes is the raw X-NCN-Probes value (or "" if absent). Signing
// the probe list prevents a wire-level attacker from rewriting it on the
// fly — though replay protection (nonce one-shot) already neuters most of
// that risk, signing closes the gap completely.
//
// Accept if ts within 300s of now AND nonce unseen in last 600s AND
// signature matches in constant time.

package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ----- build-time metadata -----

// Version is overridden via -ldflags "-X main.Version=..." at build time.
// Kept as a var (not const) so the linker can patch it.
var Version = "dev"

// ----- config -----

const (
	defaultListen  = ":9101"
	defaultEtcDir  = "/etc/ncn-agent"
	hmacFile       = "hmac.key"
	tlsCertFile    = "tls.crt"
	tlsKeyFile     = "tls.key"
	configFile     = "agent.conf"
	skewWindow     = 300 * time.Second // accept ts within ±5min of now
	nonceTTL       = 600 * time.Second // remember nonces 10min (>2× skew)
	shellTimeout   = 10 * time.Second  // pipeline cap; previously 6s, bumped after
	                                    // pop-06 (small VPS) intermittently hit cpu
	                                    // ≈98 % and the snapshot couldn't finish in
	                                    // 12s (shellTimeout*2). 10s gives ≈20s
	                                    // ceiling, still below the client's 11 s
	                                    // request timeout so the timeout failure
	                                    // surfaces on the right side of the link.
	maxRequestSize = 64 << 10          // 64KB; this endpoint takes no body
)

type config struct {
	Listen string // bind address, default ":9101"
	NodeID string // self-id for logs/healthz; not used in HMAC
	EtcDir string // override /etc/ncn-agent (mostly for dev)
}

// loadConfig reads optional key=value lines from /etc/ncn-agent/agent.conf.
// Missing file is fine — all keys have sensible defaults. Lines starting
// with `#` or empty lines are skipped. Values are NOT shell-expanded.
func loadConfig() config {
	c := config{
		Listen: defaultListen,
		NodeID: "",
		EtcDir: defaultEtcDir,
	}
	if v := os.Getenv("NCN_AGENT_ETC"); v != "" {
		c.EtcDir = v
	}
	b, err := os.ReadFile(filepath.Join(c.EtcDir, configFile))
	if err != nil {
		return c
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "listen":
			c.Listen = v
		case "node_id":
			c.NodeID = v
		}
	}
	return c
}

// loadHMACKey reads /etc/ncn-agent/hmac.key and refuses to start with an
// obviously weak key. 32 bytes is the minimum for HMAC-SHA256 to deliver
// its full security level; we accept smaller for dev convenience but log.
func loadHMACKey(etcDir string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(etcDir, hmacFile))
	if err != nil {
		return nil, fmt.Errorf("read hmac key: %w", err)
	}
	// Be permissive about trailing newline — operators often `echo > key`.
	b = []byte(strings.TrimRight(string(b), "\r\n"))
	if len(b) < 16 {
		return nil, fmt.Errorf("hmac key too short (%d bytes; need ≥16)", len(b))
	}
	if len(b) < 32 {
		log.Printf("warning: hmac key is %d bytes; recommend ≥32 for HMAC-SHA256", len(b))
	}
	return b, nil
}

// ----- nonce cache -----
//
// Bounded in-memory set; nonces older than nonceTTL are evicted on insert.
// Capped at maxNonces to bound memory if a peer floods unique nonces.
// At 16 req/min (current 4-PoP poll rate × normal headroom), the working
// set is tiny; 10000 entries gives ~10 hours of replay protection at that
// rate before the eviction policy bites.

const maxNonces = 10000

type nonceCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newNonceCache() *nonceCache {
	return &nonceCache{seen: make(map[string]time.Time, 256)}
}

// check returns true iff nonce was NOT recently seen (and records it).
// false → replay; reject.
func (n *nonceCache) check(nonce string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	if _, dup := n.seen[nonce]; dup {
		return false
	}
	// Opportunistic GC: when crossing the cap, sweep expired entries.
	if len(n.seen) >= maxNonces {
		for k, t := range n.seen {
			if now.Sub(t) > nonceTTL {
				delete(n.seen, k)
			}
		}
		// If still full after GC, evict any single entry. We're under
		// flood; one extra accepted replay-after-eviction is preferable
		// to refusing all legitimate requests.
		if len(n.seen) >= maxNonces {
			for k := range n.seen {
				delete(n.seen, k)
				break
			}
		}
	}
	n.seen[nonce] = now
	return true
}

// ----- HMAC verification -----
//
// Parses an Authorization header of the form
//
//   NCNHMAC ts=<unix>,nonce=<base64url>,sig=<base64url>
//
// and verifies it against the given key and (method, path).

var (
	errAuthMissing  = errors.New("missing Authorization")
	errAuthScheme   = errors.New("unexpected auth scheme")
	errAuthFields   = errors.New("missing required auth fields")
	errAuthTime     = errors.New("timestamp out of skew window")
	errAuthSig      = errors.New("signature mismatch")
	errAuthNonce    = errors.New("nonce replayed")
	errAuthBadField = errors.New("malformed auth field")
)

type authParts struct {
	ts    int64
	nonce string
	sig   []byte
}

func parseAuthHeader(h string) (authParts, error) {
	var p authParts
	if h == "" {
		return p, errAuthMissing
	}
	const prefix = "NCNHMAC "
	if !strings.HasPrefix(h, prefix) {
		return p, errAuthScheme
	}
	body := h[len(prefix):]
	for _, kv := range strings.Split(body, ",") {
		kv = strings.TrimSpace(kv)
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return p, errAuthBadField
		}
		switch k {
		case "ts":
			ts, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return p, errAuthBadField
			}
			p.ts = ts
		case "nonce":
			p.nonce = v
		case "sig":
			sig, err := base64.RawURLEncoding.DecodeString(v)
			if err != nil {
				return p, errAuthBadField
			}
			p.sig = sig
		}
	}
	if p.ts == 0 || p.nonce == "" || len(p.sig) == 0 {
		return p, errAuthFields
	}
	return p, nil
}

func verifyHMAC(key []byte, p authParts, method, path, xprobes string, now time.Time, nc *nonceCache) error {
	if delta := now.Sub(time.Unix(p.ts, 0)); delta < -skewWindow || delta > skewWindow {
		return errAuthTime
	}
	if !nc.check(p.nonce) {
		return errAuthNonce
	}
	mac := hmac.New(sha256.New, key)
	// Order MUST match the client (see top-of-file wire format docs).
	// xprobes is the raw X-NCN-Probes header value, or "" if absent.
	fmt.Fprintf(mac, "%d\n%s\n%s\n%s\n%s", p.ts, p.nonce, method, path, xprobes)
	want := mac.Sum(nil)
	if subtle.ConstantTimeCompare(want, p.sig) != 1 {
		return errAuthSig
	}
	return nil
}

// ----- handlers -----

// handleHealthz: unauthenticated. Cheap. Used by Nginx-on-tyo for upstream
// health checks AND by uptime probes. Returns version + uptime so a misroll
// is visible immediately (e.g. tyo expected v1.2.0, pop-04 still on v1.1.0).
func handleHealthz(start time.Time, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		body := map[string]any{
			"ok":        true,
			"version":   version,
			"uptime_s":  int64(time.Since(start).Seconds()),
			"server_ts": time.Now().Unix(),
		}
		_ = json.NewEncoder(w).Encode(body)
	}
}

// handleSnapshot: runs the 14-section shell pipeline (same script the SSH
// scraper on ctrl-01 currently feeds over stdin). Returns the EXACT same
// byte stream so fleet.go's parser doesn't need to know which transport
// produced it.
//
// IMPORTANT: this list MUST stay in lockstep with backend/fleet.go's
// buildScrapeScript (lines ~490-510). When fleet.go gains a 15th section,
// add it here too — and bump the agent version.
func handleSnapshot(key []byte, nc *nonceCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		xprobes := r.Header.Get("X-NCN-Probes")
		// Auth — must precede any work. xprobes participates in the MAC.
		ap, err := parseAuthHeader(r.Header.Get("Authorization"))
		if err != nil {
			httpAuthFail(w, err)
			return
		}
		if err := verifyHMAC(key, ap, r.Method, r.URL.Path, xprobes, time.Now(), nc); err != nil {
			httpAuthFail(w, err)
			return
		}

		probes, err := parseProbeHeader(xprobes)
		if err != nil {
			http.Error(w, "bad probes header: "+err.Error(), http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), shellTimeout*2)
		defer cancel()

		out, err := runSnapshotPipeline(ctx, probes)
		if err != nil {
			http.Error(w, "snapshot failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(out)
	}
}

// probeSpec mirrors fleet.go's probeTarget. Three fields, pipe-separated
// inside each entry, comma-separated between entries.
type probeSpec struct{ Name, Target, Type string }

// parseProbeHeader is permissive: empty header = no probes (matches what
// the SSH script does when probeTargets is empty). Strict on field shape
// (exactly 3 fields, type ∈ {ping4, ping6}, name/target charset) because
// these values become shell arguments — we don't want a malformed entry
// turning into command injection.
func parseProbeHeader(h string) ([]probeSpec, error) {
	if h == "" {
		return nil, nil
	}
	var out []probeSpec
	for _, entry := range strings.Split(h, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		fields := strings.SplitN(entry, "|", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("entry %q: need name|target|type", entry)
		}
		name, target, typ := fields[0], fields[1], fields[2]
		if typ != "ping4" && typ != "ping6" {
			return nil, fmt.Errorf("entry %q: type must be ping4 or ping6", entry)
		}
		if !probeFieldOK(name) || !probeFieldOK(target) {
			return nil, fmt.Errorf("entry %q: name/target charset", entry)
		}
		out = append(out, probeSpec{Name: name, Target: target, Type: typ})
	}
	return out, nil
}

// probeFieldOK gates the name + target charset to what fleet.go's
// shEscape was happy to pass through: alphanumerics, dot, dash, colon,
// slash. No shell metacharacters allowed in this whitelist.
func probeFieldOK(s string) bool {
	if s == "" || len(s) > 80 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == ':' || r == '/' || r == '_':
		default:
			return false
		}
	}
	return true
}

// runSnapshotPipeline executes the same shell pipeline fleet.go used to
// send over SSH stdin. Byte-equal output is the contract — fleet.go's
// existing parser is the source of truth for the section layout, and the
// agent serves output it can swallow without modification.
//
// The static 14 sections (hostname → vxlan list) are a fixed string
// literal — no command injection surface. The 15th section (probes) is
// built from the X-NCN-Probes header values, which have already been
// charset-gated by parseProbeHeader so shell expansion is safe.
//
// We run via `/bin/sh -c` because the pipeline uses shell built-ins
// (`echo`, `||`, `&`, `wait`) and stream composition that's awkward to
// reproduce in pure Go exec.Command primitives.
func runSnapshotPipeline(ctx context.Context, probes []probeSpec) ([]byte, error) {
	var b strings.Builder
	b.WriteString(snapshotPipelineStatic)
	// Probes — `probe_one NAME TARGET TYPE` sends TWO pings (interval
	// 0.3s, 2s deadline) and prints `PROBE name target type ms`, where
	// ms is the FIRST successful reply's RTT, or -1 if BOTH packets are
	// lost. grep|head picks the first `time=` so 1-lost-1-ok still
	// reports the good RTT.
	//
	// Why 2 packets (was 1, pre-2026-05-29): pop-04's IPv6 transit to
	// the Cloudflare anchor drops ~40 % of single packets while v4 on
	// the same box is 0 %. A single-shot probe therefore flapped the
	// cloudflare-v6 indicator red ~40 % of scrapes despite the link
	// being usable. Two packets cut the visible-down rate to the joint
	// loss probability (~0.4² ≈ 16 %, and in practice lower since the
	// drops aren't independent). It does NOT mask a genuine outage —
	// if the target is truly down, both packets fail and we still
	// report -1. The local-node path (monitor.go runPingProbe) already
	// used -c 2; this brings the agent path in line.
	//
	// -i 0.3 keeps the two packets within ~0.3s so the probe section
	// doesn't add a full second per target to snapshot latency. 0.3s
	// is above the 0.2s unprivileged-interval floor, so no CAP_NET_RAW
	// needed even though the agent already runs as root.
	b.WriteString(`probe_one() {
  local cmd=ping; [ "$3" = ping6 ] && cmd="ping -6";
  local out=$($cmd -c 2 -i 0.3 -W 2 -n "$2" 2>/dev/null || true)
  local ms=$(echo "$out" | grep -oE 'time=[0-9.]+' | head -1 | cut -d= -f2)
  echo "PROBE $1 $2 $3 ${ms:--1}"
}
`)
	for _, p := range probes {
		b.WriteString("probe_one " + shellQuote(p.Name) + " " + shellQuote(p.Target) + " " + p.Type + " &\n")
	}
	b.WriteString("wait\n")

	// Section 15 (one-indexed: 16th overall) — full BIRD protocol detail.
	// Added in Phase 4 (2026-05-28) so the admin UI's BIRD detail panel
	// can read from the scrape cache (~1 ms) instead of fanning out an
	// SSH call per request (1-3 s). Output is the same as fleet.go's
	// section 3 (summary) but with per-protocol detail blocks appended.
	// fleet.go's parseSnapshot tolerates older agents that don't emit
	// this section — they just stay at 15 sections and the cache is
	// empty for that node until the agent gets re-provisioned.
	b.WriteString("echo ___SEP___\n")
	b.WriteString("sudo -n birdc show protocols all 2>&1 || true\n")

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", b.String())
	cmd.Stdin = nil
	cmd.Env = []string{ // minimal env: keep $PATH for birdc / ip / df / wg
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C", "LC_ALL=C", // stable command output across locales
	}
	return cmd.CombinedOutput()
}

// shellQuote wraps a string in single quotes for sh -c, escaping any
// embedded single quotes. Equivalent to fleet.go shEscape — kept tiny
// and inlined here so the agent has zero internal package deps.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// snapshotPipelineStatic is sections 1-14 (the parts that don't depend
// on per-request input). Lockstep with backend/fleet.go buildRemoteScript
// lines ~492-509. When fleet.go gains a new pre-probes section, add it
// here and bump the agent version (so a /v1/healthz check can spot a
// version skew between pop-04 and tyo).
const snapshotPipelineStatic = `
hostname
echo ___SEP___
cat /proc/loadavg
echo ___SEP___
grep -E '^(MemTotal|MemAvailable):' /proc/meminfo
echo ___SEP___
sudo -n birdc show protocols 2>&1 || true
echo ___SEP___
sudo -n wg show 2>&1 || true
echo ___SEP___
uptime
echo ___SEP___
head -1 /proc/stat
echo ___SEP___
cat /proc/net/dev
echo ___SEP___
df -PB1 / | tail -1
echo ___SEP___
sudo -n birdc show route count 2>&1 || true
echo ___SEP___
sudo -n ip -d -j link show type gre 2>/dev/null || echo '[]'
echo ___SEP___
sudo -n ip -d -j link show type gretap 2>/dev/null || echo '[]'
echo ___SEP___
sudo -n ip -d -j link show type ip6gre 2>/dev/null || echo '[]'
echo ___SEP___
sudo -n ip -d -j link show type vxlan 2>/dev/null || echo '[]'
echo ___SEP___
`

// httpAuthFail returns 401 without revealing WHY it failed (timing, sig,
// nonce, etc.). The agent log records the reason; the client gets a flat
// "unauthorized" so probing is fruitless.
func httpAuthFail(w http.ResponseWriter, why error) {
	log.Printf("auth fail: %v", why)
	w.Header().Set("WWW-Authenticate", `NCNHMAC realm="ncn-agent"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// ----- main -----

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC | log.Lmicroseconds)
	log.SetPrefix("ncn-agent: ")

	cfg := loadConfig()
	key, err := loadHMACKey(cfg.EtcDir)
	if err != nil {
		log.Fatalf("startup: %v", err)
	}

	mux := http.NewServeMux()
	start := time.Now()
	mux.HandleFunc("/v1/healthz", handleHealthz(start, Version))
	mux.HandleFunc("/v1/snapshot", handleSnapshot(key, newNonceCache()))

	// 404 everything else with a non-revealing body. We deliberately do
	// NOT serve a / handler that lists endpoints — anyone probing the
	// IP allowlist boundary gets nothing useful.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           http.MaxBytesHandler(mux, maxRequestSize),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second, // snapshot can be slow on a hot node
		IdleTimeout:       60 * time.Second,
	}

	certPath := filepath.Join(cfg.EtcDir, tlsCertFile)
	keyPath := filepath.Join(cfg.EtcDir, tlsKeyFile)

	log.Printf("starting %s on %s (node_id=%q etc=%s)", Version, cfg.Listen, cfg.NodeID, cfg.EtcDir)
	err = srv.ListenAndServeTLS(certPath, keyPath)
	if !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("listen: %v", err)
	}
}

