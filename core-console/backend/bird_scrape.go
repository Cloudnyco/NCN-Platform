// Periodic scraper for BIRD routing daemon.
//
// Runs `sudo -n birdc show protocols` every 15s, parses the table into a
// structured slice. Also fetches `show route count` for table-level stats.
// All state is held in memory; readers get a snapshot via getters.
package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type birdProtocol struct {
	Name      string `json:"name"`
	Proto     string `json:"proto"`
	Table     string `json:"table"`
	State     string `json:"state"`
	Since     string `json:"since"`
	Info      string `json:"info"`
	Healthy   bool   `json:"healthy"`
}

type birdRouteCount struct {
	Table string `json:"table"`
	Count int    `json:"count"`
}

// birdSession is a BGP protocol enriched with the per-neighbour detail parsed
// from `show protocols all` (the public Looking Glass "BGP Sessions" view).
type birdSession struct {
	Name           string `json:"name"`
	Proto          string `json:"proto"`
	State          string `json:"state"`           // up / start / down
	Info           string `json:"info"`            // Established / Connect / Passive / ...
	Status         string `json:"status"`          // established|connect|passive|down (bucketed)
	NeighborAddr   string `json:"neighbor_addr"`
	NeighborAS     int    `json:"neighbor_as"`
	RoutesImported int    `json:"routes_imported"`
	RoutesExported int    `json:"routes_exported"`
}

type birdState struct {
	mu          sync.RWMutex
	lastUpdate  int64
	ready       bool
	version     string
	protocols   []birdProtocol
	routeCounts []birdRouteCount
	sessions    []birdSession
	rawErr      string
}

func newBirdState() *birdState { return &birdState{} }

func (b *birdState) scrapeLoop(ctx context.Context) {
	b.scrapeOnce()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.scrapeOnce()
		}
	}
}

func (b *birdState) scrapeOnce() {
	now := time.Now().Unix()

	raw, stderr, exit, err := runCmd(5*time.Second, "sudo", "-n", "birdc", "show", "protocols")
	if err != nil || exit != 0 {
		b.mu.Lock()
		b.lastUpdate = now
		b.ready = false
		b.rawErr = strings.TrimSpace(stderr + " · exec err " + safeErr(err))
		b.mu.Unlock()
		return
	}

	version, protos := parseBirdProtocols(raw)

	rcRaw, _, _, _ := runCmd(3*time.Second, "sudo", "-n", "birdc", "show", "route", "count")
	counts := parseBirdRouteCount(rcRaw)

	// `show protocols all` enriches each BGP session with neighbour address /
	// AS / route counts for the public Looking Glass "BGP Sessions" view. The
	// output is much larger than the summary, so allow a longer timeout. Kept
	// init'd to a non-nil slice — a nil slice marshals to JSON null and breaks
	// the frontend's array ops (see the go-nil-slice memory).
	allRaw, _, allExit, allErr := runCmd(8*time.Second, "sudo", "-n", "birdc", "show", "protocols", "all")
	sessions := []birdSession{}
	if allErr == nil && allExit == 0 {
		sessions = parseBirdSessions(allRaw)
	}

	b.mu.Lock()
	b.lastUpdate = now
	b.ready = true
	b.version = version
	b.protocols = protos
	b.routeCounts = counts
	b.sessions = sessions
	b.rawErr = ""
	b.mu.Unlock()
}

// `birdc show protocols` output:
//   BIRD 2.17.1 ready.
//   Name       Proto      Table      State  Since         Info
//   device1    Device     ---        up     2026-05-17
//   skyline_v6 BGP        ---        up     2026-05-18    Established
func parseBirdProtocols(raw string) (version string, protos []birdProtocol) {
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		s := strings.TrimSpace(line)
		// Version banner ("BIRD 3.3.0 ready.") — scan ANY line, not just the
		// first: a node whose shell rc prints noise (e.g. skylineconnect's
		// `tg: command not found`) pushes the banner down. Require the token
		// after "BIRD " to start with a digit so a protocol row or stray text
		// can't false-match.
		if version == "" && strings.HasPrefix(s, "BIRD ") {
			parts := strings.Fields(s)
			if len(parts) >= 2 && parts[1] != "" && parts[1][0] >= '0' && parts[1][0] <= '9' {
				version = parts[1]
			}
			continue
		}
		if strings.HasPrefix(s, "Name") || s == "" {
			continue
		}
		// "Info" may contain spaces, so allocate 6 slots and join the tail.
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		var info string
		if len(fields) > 5 {
			info = strings.Join(fields[5:], " ")
		}
		p := birdProtocol{
			Name:  fields[0],
			Proto: fields[1],
			Table: fields[2],
			State: fields[3],
			Since: fields[4],
			Info:  info,
		}
		p.Healthy = p.State == "up" && (p.Info == "" || strings.Contains(p.Info, "Established") || p.Info == "Running")
		// BGP nuance — three buckets, not two:
		//   up    + Established      → healthy (active session)
		//   start + Passive          → healthy (listener; intentional config)
		//   any other start/up combo → unhealthy (Connect/Active/Idle = peer issue)
		if p.Proto == "BGP" {
			switch {
			case p.State == "up" && strings.Contains(p.Info, "Established"):
				p.Healthy = true
			case p.State == "start" && strings.Contains(p.Info, "Passive"):
				p.Healthy = true
			default:
				p.Healthy = false
			}
		}
		protos = append(protos, p)
	}
	return
}

// `birdc show route count` returns something like:
//   ... 912346 of 912346 routes for 412110 networks in table master4
func parseBirdRouteCount(raw string) []birdRouteCount {
	out := []birdRouteCount{}
	for _, line := range strings.Split(raw, "\n") {
		s := strings.TrimSpace(line)
		// Look for "<N> of <N> routes for <N> networks in table <NAME>"
		idx := strings.Index(s, "in table ")
		if idx < 0 {
			continue
		}
		table := strings.TrimSpace(s[idx+len("in table "):])
		// Total route count is the first number on the line.
		fields := strings.Fields(s)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		out = append(out, birdRouteCount{Table: table, Count: n})
	}
	return out
}

func safeErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// parseBirdSessions walks `show protocols all` output. A non-indented line is
// a protocol header (Name Proto Table State Since Info…); the indented lines
// under it carry "Neighbor address:", "Neighbor AS:" and per-channel "Routes:"
// detail. Only BGP protocols are returned. Always returns a non-nil slice.
func parseBirdSessions(raw string) []birdSession {
	out := []birdSession{}
	var cur *birdSession
	flush := func() {
		if cur != nil && cur.Proto == "BGP" {
			cur.Status = sessionStatus(cur.State, cur.Info)
			out = append(out, *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indented := line[0] == ' ' || line[0] == '\t'
		if !indented {
			s := strings.TrimSpace(line)
			if strings.HasPrefix(s, "BIRD ") || strings.HasPrefix(s, "Name") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			flush()
			info := ""
			if len(fields) > 5 {
				info = strings.Join(fields[5:], " ")
			}
			cur = &birdSession{Name: fields[0], Proto: fields[1], State: fields[3], Info: info}
			continue
		}
		if cur == nil {
			continue
		}
		s := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(s, "Neighbor address:"):
			cur.NeighborAddr = strings.TrimSpace(strings.TrimPrefix(s, "Neighbor address:"))
		case strings.HasPrefix(s, "Neighbor AS:"):
			if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(s, "Neighbor AS:"))); err == nil {
				cur.NeighborAS = n
			}
		case strings.HasPrefix(s, "Routes:"):
			// e.g. "237739 imported, 0 filtered, 19 exported, 1 preferred".
			// A dual-stack protocol prints one Routes line per channel; sum them.
			toks := strings.Fields(strings.ReplaceAll(strings.TrimSpace(strings.TrimPrefix(s, "Routes:")), ",", " "))
			for i := 0; i+1 < len(toks); i++ {
				n, err := strconv.Atoi(toks[i])
				if err != nil {
					continue
				}
				switch toks[i+1] {
				case "imported":
					cur.RoutesImported += n
				case "exported":
					cur.RoutesExported += n
				}
			}
		}
	}
	flush()
	return out
}

// sessionStatus buckets a BGP session for the status-count pills. Mirrors the
// three-bucket logic in parseBirdProtocols: Established (active), Passive
// (intentional listener), down (hard), everything else → connect (Connect /
// Active / Idle / OpenSent — a peer that isn't up yet).
func sessionStatus(state, info string) string {
	switch {
	case strings.Contains(info, "Established"):
		return "established"
	case strings.Contains(info, "Passive"):
		return "passive"
	case state == "down":
		return "down"
	default:
		return "connect"
	}
}

// handleLGSessions — PUBLIC. The Looking Glass "BGP Sessions" overview: the
// structured session list from the local (ctrl-01) vantage plus status counts.
func (b *birdState) handleLGSessions(w http.ResponseWriter, _ *http.Request) {
	b.mu.RLock()
	sessions := b.sessions
	ready := b.ready
	upd := b.lastUpdate
	b.mu.RUnlock()
	if sessions == nil {
		sessions = []birdSession{}
	}
	counts := map[string]int{"established": 0, "connect": 0, "passive": 0, "down": 0}
	for _, s := range sessions {
		counts[s.Status]++
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"vantage":     "ctrl-01",
		"ready":       ready,
		"last_update": upd,
		"sessions":    sessions,
		"counts":      counts,
	}})
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

func (b *birdState) handleBirdSummary(w http.ResponseWriter, _ *http.Request) {
	b.mu.RLock()
	resp := map[string]any{
		"ready":        b.ready,
		"version":      b.version,
		"protocols":    b.protocols,
		"route_counts": b.routeCounts,
		"last_update":  b.lastUpdate,
		"error":        b.rawErr,
	}
	b.mu.RUnlock()
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: resp})
}

func (b *birdState) handleBirdProtocolDetail(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing ?name="})
		return
	}
	// Validate strictly: only allow [A-Za-z0-9_-] to keep shell-free safety.
	for _, c := range name {
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-'
		if !ok {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid protocol name"})
			return
		}
	}
	start := time.Now()
	out, _, exit, err := runCmd(5*time.Second, "sudo", "-n", "birdc", "show", "protocols", "all", name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"name":     name,
		"raw":      out,
		"exit":     exit,
		"duration": time.Since(start).String(),
	}})
}
