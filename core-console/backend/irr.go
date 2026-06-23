// irr.go — IRR (Internet Routing Registry) prefix-list expansion via bgpq4,
// the first step of peering automation. Runs bgpq4 on NCN_IRR_NODE (default the
// control node) through the same runMeshScriptOnNode path rpki.go uses for its
// birdc reads, and returns a BIRD-format IPv6 prefix set. Read-only: it queries
// public IRR whois servers and touches no router.
package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// IRR source (from the peering application) → authoritative whois host for
// bgpq4's -h flag. Empty/unknown falls back to bgpq4's default (RADB, which
// mirrors most registries).
var irrWhoisHost = map[string]string{
	"RIPE":    "whois.ripe.net",
	"RADB":    "whois.radb.net",
	"ARIN":    "rr.arin.net",
	"APNIC":   "whois.apnic.net",
	"LACNIC":  "irr.lacnic.net",
	"AFRINIC": "whois.afrinic.net",
	"ALTDB":   "whois.altdb.net",
}

// asSetRe allows ASNs (AS123) and AS-SET names, including hierarchical
// (AS123:AS-FOO) and source-qualified (RADB::AS-FOO) forms. It excludes shell
// metacharacters by construction, so the value is safe to pass to bgpq4.
var asSetRe = regexp.MustCompile(`^[A-Za-z0-9:_.-]{1,80}$`)

type irrResult struct {
	Name        string    `json:"name"`         // BIRD define name, e.g. PEER_AS6939_PFX
	BirdSet     string    `json:"bird_set"`     // bgpq4 -b output: "NAME = [ ... ];"
	PrefixCount int       `json:"prefix_count"` // number of prefixes
	Source      string    `json:"source"`       // whois host used
	ASSet       string    `json:"as_set"`
	GeneratedAt time.Time `json:"generated_at"`
}

// expandASSet runs `bgpq4 -6 -A -b` on nodeID to expand asSet into a BIRD IPv6
// prefix set. name is the BIRD set identifier (e.g. PEER_AS6939_PFX).
func expandASSet(ctx context.Context, f *fleetScraper, nodeID, name, asSet, irrSource string) (*irrResult, error) {
	if f == nil {
		return nil, fmt.Errorf("fleet unavailable")
	}
	asSet = strings.ToUpper(strings.TrimSpace(asSet))
	if !asSetRe.MatchString(asSet) {
		return nil, fmt.Errorf("invalid AS-SET/ASN %q", asSet)
	}
	if name == "" {
		name = "PEER_PFX"
	}
	rec, ok := f.registry.get(nodeID)
	if !ok {
		return nil, fmt.Errorf("IRR node %q not in registry", nodeID)
	}
	host := irrWhoisHost[strings.ToUpper(strings.TrimSpace(irrSource))]
	hostArg := ""
	if host != "" {
		hostArg = "-h " + host + " "
	}
	// -6 IPv6 · -A aggregate adjacent prefixes · -b BIRD format · -l <name>.
	script := fmt.Sprintf("bgpq4 -6 -A -b -l %s %s%s", name, hostArg, asSet)

	var sb strings.Builder
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	rc, err := f.runMeshScriptOnNode(cctx, rec, script, func(l string) {
		if sb.Len() < 1<<20 { // 1 MB cap
			sb.WriteString(l)
			sb.WriteString("\n")
		}
	})
	if err != nil {
		return nil, fmt.Errorf("bgpq4 run on %s: %w", nodeID, err)
	}
	out := strings.TrimSpace(sb.String())
	if rc != 0 || out == "" {
		return nil, fmt.Errorf("bgpq4 produced no output (rc=%d) — is bgpq4 installed on %s?", rc, nodeID)
	}
	// Each prefix line carries a CIDR; the wrapping `NAME = [` / `];` carry none.
	count := strings.Count(out, "/")
	// Empty-result floor: a non-empty AS-SET that expands to 0 prefixes is almost
	// always a transient IRR/whois failure. Refuse it — never generate an empty
	// (and therefore permissive-by-omission) prefix list.
	if count == 0 {
		return nil, fmt.Errorf("AS-SET %q expanded to 0 prefixes — refusing (likely an IRR transient)", asSet)
	}
	if host == "" {
		host = "whois.radb.net (default)"
	}
	return &irrResult{
		Name: name, BirdSet: out, PrefixCount: count,
		Source: host, ASSet: asSet, GeneratedAt: time.Now().UTC(),
	}, nil
}
