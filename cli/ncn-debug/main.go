// ncn-debug — authenticated debug CLI for the NCN console REST API.
//
// A thin, read-only client over the same /api/v1 surface the web console
// uses, for ops members who'd rather stay in a terminal. Pure stdlib, single
// static binary, zero config beyond a token.
//
// Auth: a personal API token (mint one in admin.example.com → Security → API
// Tokens; format `ncntok_…`). Resolution order, highest first:
//
//	--token <tok>            flag
//	$NCN_TOKEN               environment
//	~/.config/ncn-cli/token  file written by `ncn-debug token <tok>`
//
// The `status` command is public and needs no token.
//
// Usage:
//
//	ncn-debug [--host URL] [--json] [--timeout D] <command> [args]
//
// Commands:
//
//	whoami              verify the token; show operator, role, session TTL
//	fleet               one-line-per-PoP health table
//	node <id>           full detail for one PoP (cpu/mem/disk/bird/probes/…)
//	bgp [id]            BGP sessions across the fleet, or just one PoP
//	incidents           open + recent (30-day) incidents
//	status              public uptime summary (no token required)
//	get <path>          raw authenticated GET to any /api path, pretty JSON
//	token <ncntok_…>    save a token to ~/.config/ncn-cli/token (0600)
//
// Build:
//
//	cd cli/ncn-debug && go build -o ncn-debug
//	GOOS=linux  GOARCH=amd64 go build -o ncn-debug-linux-amd64
//	GOOS=darwin GOARCH=arm64 go build -o ncn-debug-darwin-arm64
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

const defaultHost = "https://admin.example.com"

// Globals resolved in main(), read by command funcs.
var (
	gHost    string
	gToken   string
	gJSON    bool
	gTimeout time.Duration
	gColor   bool
)

func main() {
	args := os.Args[1:]

	// Hand-rolled flag scan so flags may sit before OR after the command,
	// and so `--flag value` and `--flag=value` both work. Anything not a
	// recognized flag is positional (the command + its args).
	gHost = envOr("NCN_HOST", defaultHost)
	gTimeout = 20 * time.Second
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json" || a == "-json":
			gJSON = true
		case a == "--host" || a == "-host":
			i++
			if i < len(args) {
				gHost = args[i]
			}
		case strings.HasPrefix(a, "--host="):
			gHost = strings.TrimPrefix(a, "--host=")
		case a == "--token" || a == "-token":
			i++
			if i < len(args) {
				gToken = args[i]
			}
		case strings.HasPrefix(a, "--token="):
			gToken = strings.TrimPrefix(a, "--token=")
		case a == "--timeout" || a == "-timeout":
			i++
			if i < len(args) {
				if d, err := time.ParseDuration(args[i]); err == nil {
					gTimeout = d
				}
			}
		case a == "-h" || a == "--help" || a == "help":
			usage()
			return
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "ncn-debug: unknown flag %q\n", a)
			os.Exit(2)
		default:
			pos = append(pos, a)
		}
	}

	gHost = strings.TrimRight(gHost, "/")
	gColor = colorEnabled()

	if len(pos) == 0 {
		usage()
		os.Exit(2)
	}

	cmd, rest := pos[0], pos[1:]
	var err error
	switch cmd {
	case "whoami":
		err = cmdWhoami()
	case "fleet":
		err = cmdFleet()
	case "node":
		err = cmdNode(rest)
	case "bgp":
		err = cmdBGP(rest)
	case "incidents":
		err = cmdIncidents()
	case "status":
		err = cmdStatus()
	case "get":
		err = cmdGet(rest)
	case "token":
		err = cmdToken(rest)
	default:
		fmt.Fprintf(os.Stderr, "ncn-debug: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ncn-debug: "+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `ncn-debug — authenticated debug CLI for the NCN console API

Usage:
  ncn-debug [--host URL] [--json] [--timeout D] <command> [args]

Commands:
  whoami            verify token; show operator, role, session TTL
  fleet             one-line-per-PoP health table
  node <id>         full detail for one PoP
  bgp [id]          BGP sessions across the fleet (or just one PoP)
  incidents         open + recent (30-day) incidents
  status            public uptime summary (no token needed)
  get <path>        raw authenticated GET to any /api path (pretty JSON)
  token <ncntok_…>  save an API token to ~/.config/ncn-cli/token

Auth (all but 'status'):  --token  >  $NCN_TOKEN  >  ~/.config/ncn-cli/token
Mint a token at admin.example.com → Security → API Tokens.

Flags:
  --host URL     console base URL (default `+defaultHost+`, or $NCN_HOST)
  --json         emit raw JSON instead of formatted output
  --timeout D    per-request timeout (default 20s)
`)
}

// ─────────────────────────────── Commands ───────────────────────────────

func cmdWhoami() error {
	data, err := apiGet("/api/v1/auth/me", true)
	if err != nil {
		return err
	}
	if gJSON {
		return printJSON(data)
	}
	var me struct {
		Operator   string `json:"operator"`
		Role       string `json:"role"`
		TTLSeconds int64  `json:"ttl_seconds"`
		HasPasskey bool   `json:"has_passkey"`
		HasTOTP    bool   `json:"has_totp"`
	}
	_ = json.Unmarshal(data, &me)
	fmt.Printf("%s  %s\n", bold(me.Operator), dim("("+me.Role+")"))
	if me.TTLSeconds > 0 {
		fmt.Printf("  session valid for %s\n", (time.Duration(me.TTLSeconds) * time.Second).Round(time.Minute))
	}
	return nil
}

func cmdFleet() error {
	nodes, err := fetchFleet()
	if err != nil {
		return err
	}
	if gJSON {
		return printJSONv(nodes)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, dim("POP\tSTATE\tLOAD\tCPU\tMEM\tDISK\tBIRD\tBGP\tWG\tTUN\tSCRAPE"))
	for _, n := range nodes {
		state := green("● up")
		if !n.OK {
			state = red("● down")
		}
		est, tot := bgpCounts(n)
		bgpCell := fmt.Sprintf("%d/%d", est, tot)
		if tot > 0 && est < tot {
			bgpCell = yellow(bgpCell)
		}
		row := fmt.Sprintf("%s\t%s\t%.2f\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s",
			bold(n.Node.ID), state, n.Load1,
			pct(n.CPUPct), pct(n.MemPct), pct(n.DiskPct),
			orDash(n.BirdVer), bgpCell, len(n.WG), len(n.Tunnels), orDash(n.Latency))
		if !n.OK && n.Error != "" {
			row += "\t" + dim(n.Error)
		}
		fmt.Fprintln(tw, row)
	}
	return tw.Flush()
}

func cmdNode(rest []string) error {
	if len(rest) == 0 {
		return errors.New("usage: ncn-debug node <id>")
	}
	id := rest[0]
	nodes, err := fetchFleet()
	if err != nil {
		return err
	}
	var n *fleetNodeStatus
	for i := range nodes {
		if nodes[i].Node.ID == id {
			n = &nodes[i]
			break
		}
	}
	if n == nil {
		return fmt.Errorf("no PoP %q in fleet (try `ncn-debug fleet`)", id)
	}
	if gJSON {
		return printJSONv(n)
	}
	state := green("up")
	if !n.OK {
		state = red("down")
	}
	fmt.Printf("%s  %s  %s\n", bold(n.Node.ID), dim(n.Node.Label), state)
	if n.Error != "" {
		fmt.Println("  " + red("error: "+n.Error))
	}
	line := func(k, v string) { fmt.Printf("  %-14s %s\n", dim(k), v) }
	if n.Hostname != "" {
		line("hostname", n.Hostname)
	}
	if n.Uptime != "" {
		line("uptime", n.Uptime)
	}
	line("load1", fmt.Sprintf("%.2f", n.Load1))
	line("cpu", pct(n.CPUPct))
	line("mem", fmt.Sprintf("%s  (%s / %s)", pct(n.MemPct), humanBytes(n.MemUsed), humanBytes(n.MemTotal)))
	line("disk", fmt.Sprintf("%s  (%s / %s)", pct(n.DiskPct), humanBytes(n.DiskUsed), humanBytes(n.DiskTotal)))
	line("net", fmt.Sprintf("↓ %s/s  ↑ %s/s", humanBytes(uint64(n.NetRxBps)), humanBytes(uint64(n.NetTxBps))))
	if n.BirdVer != "" {
		line("bird", n.BirdVer)
	}
	if n.AgentCertDaysLeft != 0 {
		v := fmt.Sprintf("%d days", n.AgentCertDaysLeft)
		if n.AgentCertDaysLeft < 30 {
			v = yellow(v)
		}
		line("agent cert", v)
	}
	if len(n.Probes) > 0 {
		fmt.Println("  " + dim("probes"))
		for _, p := range n.Probes {
			st := green("ok")
			ms := fmt.Sprintf("%.2f ms", p.LastMS)
			if !p.LastOK {
				st, ms = red("FAIL"), "—"
			}
			fmt.Printf("    %-16s %-5s %s\n", p.Name, st, ms)
		}
	}
	if len(n.Protocols) > 0 {
		fmt.Println("  " + dim("bird protocols"))
		for _, p := range n.Protocols {
			st := green(p.State)
			if !p.Healthy {
				st = yellow(p.State)
			}
			fmt.Printf("    %-18s %-6s %-8s %s\n", p.Name, p.Proto, st, dim(p.Info))
		}
	}
	if len(n.RouteCounts) > 0 {
		var parts []string
		for _, rc := range n.RouteCounts {
			parts = append(parts, fmt.Sprintf("%s=%d", rc.Table, rc.Count))
		}
		line("routes", strings.Join(parts, "  "))
	}
	if len(n.Tunnels) > 0 {
		fmt.Println("  " + dim("tunnels"))
		for _, t := range n.Tunnels {
			st := green("up")
			if !t.Up {
				st = red("down")
			}
			fmt.Printf("    %-12s %-8s %-5s %s→%s\n", t.Name, t.Kind, st, t.Local, t.Remote)
		}
	}
	return nil
}

func cmdBGP(rest []string) error {
	nodes, err := fetchFleet()
	if err != nil {
		return err
	}
	filter := ""
	if len(rest) > 0 {
		filter = rest[0]
	}
	if gJSON {
		return printJSONv(nodes)
	}
	any := false
	for _, n := range nodes {
		if filter != "" && n.Node.ID != filter {
			continue
		}
		var bgps []birdProtocol
		for _, p := range n.Protocols {
			if strings.EqualFold(p.Proto, "BGP") {
				bgps = append(bgps, p)
			}
		}
		if len(bgps) == 0 {
			continue
		}
		any = true
		est, tot := bgpCounts(n)
		fmt.Printf("%s  %s\n", bold(n.Node.ID), dim(fmt.Sprintf("%d/%d established", est, tot)))
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		for _, p := range bgps {
			st := green(p.State)
			if !p.Healthy {
				st = yellow(p.State)
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", p.Name, st, dim(p.Since), dim(p.Info))
		}
		tw.Flush()
	}
	if !any {
		if filter != "" {
			return fmt.Errorf("no BGP sessions for %q", filter)
		}
		fmt.Println(dim("no BGP sessions reported"))
	}
	return nil
}

func cmdIncidents() error {
	data, err := apiGet("/api/v1/incidents/public", true)
	if err != nil {
		return err
	}
	if gJSON {
		return printJSON(data)
	}
	var incs []struct {
		Title        string   `json:"title"`
		Status       string   `json:"status"`
		Severity     string   `json:"severity"`
		AffectedPoPs []string `json:"affected_pops"`
		CreatedAt    string   `json:"created_at"`
		ResolvedAt   *string  `json:"resolved_at"`
	}
	if err := json.Unmarshal(data, &incs); err != nil {
		return err
	}
	if len(incs) == 0 {
		fmt.Println(dim("no incidents in the last 30 days"))
		return nil
	}
	for _, in := range incs {
		mark := yellow("●")
		if in.Status == "resolved" {
			mark = green("●")
		}
		sev := in.Severity
		if in.Severity == "critical" {
			sev = red(sev)
		} else if in.Severity == "major" {
			sev = yellow(sev)
		}
		fmt.Printf("%s %s  %s  %s\n", mark, bold(in.Title), sev, dim(in.Status))
		meta := "  opened " + in.CreatedAt
		if in.ResolvedAt != nil {
			meta += " · resolved " + *in.ResolvedAt
		}
		if len(in.AffectedPoPs) > 0 {
			meta += " · " + strings.Join(in.AffectedPoPs, ",")
		}
		fmt.Println(dim(meta))
	}
	return nil
}

func cmdStatus() error {
	data, err := apiGet("/api/v1/status/summary", false) // public
	if err != nil {
		return err
	}
	if gJSON {
		return printJSON(data)
	}
	var sum struct {
		Components []struct {
			Name       string  `json:"name"`
			Category   string  `json:"category"`
			LastStatus int     `json:"last_status"`
			LastMS     float64 `json:"last_latency_ms"`
			Uptime     float64 `json:"uptime"`
		} `json:"components"`
		WindowDays int `json:"window_days"`
	}
	if err := json.Unmarshal(data, &sum); err != nil {
		return err
	}
	cat := ""
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	for _, c := range sum.Components {
		if c.Category != cat {
			tw.Flush()
			fmt.Println(dim("── " + c.Category + " ──"))
			cat = c.Category
		}
		st := green("● up")
		lat := fmt.Sprintf("%.0f ms", c.LastMS)
		switch c.LastStatus {
		case 0:
			st, lat = red("● down"), "—"
		case -1:
			st, lat = dim("● ?"), "—"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", c.Name, st, lat, uptimeColor(c.Uptime))
	}
	tw.Flush()
	fmt.Printf("%s\n", dim(fmt.Sprintf("uptime over %d days", sum.WindowDays)))
	return nil
}

func cmdGet(rest []string) error {
	if len(rest) == 0 {
		return errors.New("usage: ncn-debug get <path>   e.g. ncn-debug get /api/v1/bird/protocol?node=pop-03")
	}
	path := rest[0]
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	// `get` is the escape hatch; always send the token if we have one (the
	// path may be admin-only). Public paths just ignore it.
	data, err := apiGet(path, false)
	if err != nil {
		return err
	}
	return printJSON(data)
}

func cmdToken(rest []string) error {
	if len(rest) == 0 {
		return errors.New("usage: ncn-debug token <ncntok_…>")
	}
	tok := strings.TrimSpace(rest[0])
	if !strings.HasPrefix(tok, "ncntok_") {
		return errors.New("that doesn't look like an API token (expected ncntok_… prefix)")
	}
	p, err := tokenPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Printf("saved token to %s (mode 0600)\n", p)
	return nil
}

// ─────────────────────────────── HTTP ───────────────────────────────

// apiGet performs a GET against base+path and returns the envelope's `data`.
// requireToken=true errors early if no token is resolvable; requireToken=false
// still sends the token when present (used by `get` and public endpoints).
func apiGet(path string, requireToken bool) (json.RawMessage, error) {
	tok, terr := resolveToken()
	if requireToken && terr != nil {
		return nil, terr
	}
	req, err := http.NewRequest(http.MethodGet, gHost+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ncn-debug/1.0 ("+runtime.GOOS+"-"+runtime.GOARCH+")")
	req.Header.Set("Accept", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	client := &http.Client{Timeout: gTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var env struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("non-JSON response (HTTP %d): %s", resp.StatusCode, truncate(string(body), 200))
	}
	if !env.OK {
		if env.Error == "" {
			env.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			env.Error += "  (token missing/expired? `ncn-debug token <ncntok_…>` or set $NCN_TOKEN)"
		}
		return nil, errors.New(env.Error)
	}
	return env.Data, nil
}

func fetchFleet() ([]fleetNodeStatus, error) {
	data, err := apiGet("/api/v1/fleet", true)
	if err != nil {
		return nil, err
	}
	var nodes []fleetNodeStatus
	if err := json.Unmarshal(data, &nodes); err != nil {
		return nil, err
	}
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].Node.ID < nodes[j].Node.ID })
	return nodes, nil
}

// ─────────────────────────────── Token resolution ───────────────────────────────

func resolveToken() (string, error) {
	if gToken != "" {
		return gToken, nil
	}
	if v := os.Getenv("NCN_TOKEN"); v != "" {
		return v, nil
	}
	if p, err := tokenPath(); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			if t := strings.TrimSpace(string(b)); t != "" {
				return t, nil
			}
		}
	}
	return "", errors.New("no API token — pass --token, set $NCN_TOKEN, or run `ncn-debug token <ncntok_…>` (mint one at admin.example.com → Security → API Tokens)")
}

func tokenPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ncn-cli", "token"), nil
}

// ─────────────────────────────── Rendering helpers ───────────────────────────────

func bgpCounts(n fleetNodeStatus) (est, tot int) {
	for _, p := range n.Protocols {
		if strings.EqualFold(p.Proto, "BGP") {
			tot++
			if p.Healthy {
				est++
			}
		}
	}
	return
}

func printJSON(raw json.RawMessage) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		fmt.Println(string(raw)) // fall back to raw
		return nil
	}
	fmt.Println(buf.String())
	return nil
}

func printJSONv(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func pct(v float64) string {
	s := fmt.Sprintf("%.0f%%", v)
	switch {
	case v >= 90:
		return red(s)
	case v >= 75:
		return yellow(s)
	default:
		return s
	}
}
func uptimeColor(frac float64) string {
	p := frac * 100
	s := fmt.Sprintf("%.2f%%", p)
	switch {
	case frac == 0:
		return dim("— no data")
	case p >= 99.9:
		return green(s)
	case p >= 99:
		return s
	case p >= 95:
		return yellow(s)
	default:
		return red(s)
	}
}
func humanBytes(b uint64) string {
	const u = 1024
	if b < u {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(u), 0
	for n := b / u; n >= u; n /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(b)/float64(div), "KMGTPE"[exp])
}
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ─────────────────────────────── Color ───────────────────────────────

func colorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
func wrap(code, s string) string {
	if !gColor {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
func bold(s string) string   { return wrap("1", s) }
func dim(s string) string    { return wrap("2", s) }
func red(s string) string    { return wrap("31", s) }
func green(s string) string  { return wrap("32", s) }
func yellow(s string) string { return wrap("33", s) }

// ─────────────────────────────── Wire types ───────────────────────────────
// Subset of the backend shapes we render. Mirrors core-console/backend
// fleet.go / bird_scrape.go / tunnel.go — keep the JSON tags in sync.

type fleetNode struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Country string `json:"country"`
}

type birdProtocol struct {
	Name    string `json:"name"`
	Proto   string `json:"proto"`
	Table   string `json:"table"`
	State   string `json:"state"`
	Since   string `json:"since"`
	Info    string `json:"info"`
	Healthy bool   `json:"healthy"`
}

type birdRouteCount struct {
	Table string `json:"table"`
	Count int    `json:"count"`
}

type probeOut struct {
	Name   string  `json:"name"`
	LastOK bool    `json:"last_ok"`
	LastMS float64 `json:"last_ms"`
}

type netTunnel struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Up     bool   `json:"up"`
	Local  string `json:"local,omitempty"`
	Remote string `json:"remote,omitempty"`
}

type wgIface struct {
	Name string `json:"name"`
}

type fleetNodeStatus struct {
	Node      fleetNode `json:"node"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	Hostname  string    `json:"hostname,omitempty"`
	Uptime    string    `json:"uptime,omitempty"`
	Load1     float64   `json:"load_1"`
	MemPct    float64   `json:"mem_pct"`
	CPUPct    float64   `json:"cpu_pct"`
	DiskPct   float64   `json:"disk_pct"`
	NetRxBps  float64   `json:"net_rx_bps"`
	NetTxBps  float64   `json:"net_tx_bps"`
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

	AgentCertDaysLeft int `json:"agent_cert_days_left,omitempty"`

	FetchedAt int64  `json:"fetched_at"`
	Latency   string `json:"scrape_latency,omitempty"`
}
