// GRE / VXLAN tunnel scraping.
//
// `ip -d -j link show type <kind>` returns a JSON array of interfaces of the
// given type. We query gre / gretap / ip6gre / vxlan separately and merge
// into a normalized netTunnel slice.
package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// netTunnel is the normalized, UI-facing shape for one tunnel interface.
type netTunnel struct {
	Kind        string `json:"kind"`   // "gre" | "gretap" | "ip6gre" | "vxlan"
	Name        string `json:"name"`
	Up          bool   `json:"up"`
	State       string `json:"state,omitempty"`
	Local       string `json:"local,omitempty"`
	Remote      string `json:"remote,omitempty"`
	TTL         int    `json:"ttl,omitempty"`
	MTU         int    `json:"mtu,omitempty"`
	VxlanID     int    `json:"vxlan_id,omitempty"`
	DstPort     int    `json:"dst_port,omitempty"`
	UnderlayDev string `json:"underlay_dev,omitempty"`
}

// ipLink is just enough of `ip -d -j link show` to identify tunnel endpoints
// across all four supported kinds.
type ipLink struct {
	Ifname    string   `json:"ifname"`
	Flags     []string `json:"flags"`
	MTU       int      `json:"mtu"`
	Operstate string   `json:"operstate"`
	LinkInfo  struct {
		InfoKind string `json:"info_kind"`
		InfoData struct {
			Remote  string `json:"remote"`
			Local   string `json:"local"`
			TTL     int    `json:"ttl"`
			ID      int    `json:"id"`        // VXLAN VNI
			Port    int    `json:"port"`      // VXLAN UDP port
			DstPort int    `json:"dst_port"`  // some kernel versions
			Link    string `json:"link"`      // VXLAN underlay dev (by-name)
		} `json:"info_data"`
	} `json:"linkinfo"`
}

// parseIPLinkJSON accepts raw output from `ip -d -j link show type X` and
// returns the configured tunnels. Default stubs (gre0/gretap0 with remote
// "any") and empty entries are skipped.
func parseIPLinkJSON(raw string) []netTunnel {
	// The remote shell may inject noise (e.g. .bashrc errors) before/after
	// the JSON array. Extract just the bracketed JSON.
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start < 0 || end < 0 || end <= start {
		return nil
	}
	var entries []ipLink
	if err := json.Unmarshal([]byte(raw[start:end+1]), &entries); err != nil {
		return nil
	}
	out := make([]netTunnel, 0, len(entries))
	for _, e := range entries {
		if e.Ifname == "" {
			continue
		}
		kind := e.LinkInfo.InfoKind
		d := e.LinkInfo.InfoData
		// Skip unconfigured default stubs.
		if d.Remote == "" || d.Remote == "any" {
			continue
		}
		up := false
		for _, f := range e.Flags {
			if f == "LOWER_UP" || f == "UP" {
				up = true
				break
			}
		}
		t := netTunnel{
			Kind:   kind,
			Name:   e.Ifname,
			Up:     up,
			State:  strings.ToLower(e.Operstate),
			Remote: d.Remote,
			Local:  d.Local,
			TTL:    d.TTL,
			MTU:    e.MTU,
		}
		if kind == "vxlan" {
			t.VxlanID = d.ID
			t.DstPort = d.Port
			if t.DstPort == 0 {
				t.DstPort = d.DstPort
			}
			t.UnderlayDev = d.Link
		}
		out = append(out, t)
	}
	return out
}

// scrapeTunnelsLocal queries the local kernel for all four supported
// tunnel kinds and merges the results.
func scrapeTunnelsLocal() []netTunnel {
	out := []netTunnel{}
	for _, kind := range []string{"gre", "gretap", "ip6gre", "vxlan"} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		raw, err := exec.CommandContext(ctx, "ip", "-d", "-j", "link", "show", "type", kind).Output()
		cancel()
		if err != nil {
			continue
		}
		out = append(out, parseIPLinkJSON(string(raw))...)
	}
	return out
}
