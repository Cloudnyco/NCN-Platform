// Visitor identification — looks up the requesting IP's ASN, holder name
// and country via Team Cymru's free DNS-based whois service.
//
// Two TXT queries:
//   1) <reverse-ip>.origin[6].asn.cymru.com  → "ASN | prefix | CC | registry | allocated"
//   2) AS<num>.asn.cymru.com                 → "ASN | CC | registry | allocated | AS-name, CC"
//
// No API key, no rate limit (DNS is cached upstream), works for v4 + v6.
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type visitorInfo struct {
	IP          string `json:"ip"`
	IPv6        bool   `json:"ipv6"`
	ASN         string `json:"asn,omitempty"`         // e.g. "AS13335"
	ASOrg       string `json:"as_org,omitempty"`      // e.g. "CLOUDFLARENET"
	Country     string `json:"country,omitempty"`     // ISO-3166 alpha-2
	Prefix      string `json:"prefix,omitempty"`      // e.g. "1.1.1.0/24"
	Registry    string `json:"registry,omitempty"`    // arin / ripencc / apnic / lacnic / afrinic
	AllocatedAt string `json:"allocated_at,omitempty"`
	Source      string `json:"source,omitempty"`      // "cymru" or "none"
}

// In-memory cache to keep DNS load tiny on a refresh-happy operator.
type visitorCacheEntry struct {
	data visitorInfo
	t    time.Time
}

var (
	visitorCache   sync.Map // ip(string) → *visitorCacheEntry
	visitorCacheTTL = 30 * time.Minute
)

// GET /api/v1/visitor
func handleVisitor(w http.ResponseWriter, r *http.Request) {
	peer := clientAddr(r)
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}

	info := visitorInfo{IP: peer, Source: "none"}

	parsed := net.ParseIP(peer)
	if parsed == nil {
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: info})
		return
	}
	info.IPv6 = parsed.To4() == nil

	// Cache hit?
	if cached, ok := visitorCache.Load(peer); ok {
		entry := cached.(*visitorCacheEntry)
		if time.Since(entry.t) < visitorCacheTTL {
			writeJSON(w, http.StatusOK, envelope{OK: true, Data: entry.data})
			return
		}
	}

	resolveVisitor(&info, parsed)

	// Stash even partial results to avoid re-querying broken paths.
	visitorCache.Store(peer, &visitorCacheEntry{data: info, t: time.Now()})

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: info})
}

func resolveVisitor(info *visitorInfo, ip net.IP) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var originQ string
	if info.IPv6 {
		originQ = ipv6CymruQuery(ip)
	} else {
		originQ = ipv4CymruQuery(ip)
	}

	originTXT, err := net.DefaultResolver.LookupTXT(ctx, originQ)
	if err != nil || len(originTXT) == 0 {
		return
	}
	// Format: "13335 | 1.1.1.0/24 | US | arin | 2010-07-14"
	parts := splitCymru(originTXT[0])
	if len(parts) >= 5 {
		info.ASN = "AS" + parts[0]
		info.Prefix = parts[1]
		info.Country = parts[2]
		info.Registry = parts[3]
		info.AllocatedAt = parts[4]
		info.Source = "cymru"
	} else if len(parts) >= 1 && parts[0] != "" {
		info.ASN = "AS" + parts[0]
		info.Source = "cymru"
	}

	// Second hop — resolve AS-name.
	if info.ASN != "" {
		nameTXT, err := net.DefaultResolver.LookupTXT(ctx, info.ASN+".asn.cymru.com")
		if err == nil && len(nameTXT) > 0 {
			// Format: "13335 | US | arin | 2010-07-14 | CLOUDFLARENET, US"
			np := splitCymru(nameTXT[0])
			if len(np) >= 5 {
				org := np[4]
				// Strip trailing ", CC" — keep just the org name.
				if idx := strings.LastIndex(org, ","); idx > 0 {
					org = strings.TrimSpace(org[:idx])
				}
				info.ASOrg = org
			}
		}
	}
}

func splitCymru(line string) []string {
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func ipv4CymruQuery(ip net.IP) string {
	v4 := ip.To4()
	return fmt.Sprintf("%d.%d.%d.%d.origin.asn.cymru.com", v4[3], v4[2], v4[1], v4[0])
}

// ipv6CymruQuery builds 32-nibble reverse PTR-style under origin6.asn.cymru.com.
// e.g. 2001:db8::1 → 1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.origin6.asn.cymru.com
func ipv6CymruQuery(ip net.IP) string {
	v6 := ip.To16()
	nibbles := make([]string, 32)
	for i, b := range v6 {
		nibbles[30-2*i] = fmt.Sprintf("%x", b&0x0f)
		nibbles[31-2*i] = fmt.Sprintf("%x", b>>4)
	}
	return strings.Join(nibbles, ".") + ".origin6.asn.cymru.com"
}
