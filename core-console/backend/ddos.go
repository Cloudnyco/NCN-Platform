// DDoS mitigation — internal nft drop/rate-limit rules ("FlowSpec-style").
//
// BIRD on Linux propagates BGP flowspec but does NOT program the kernel firewall
// from it (unlike Juniper/FRR), so "drop at our own edges" is implemented as
// nftables rules pushed to each PoP into a dedicated `inet ncn_ddos` table — also
// sidestepping the BIRD-3.3.1 roa_check fragility (no BIRD change at all). Rules
// are HUMAN-CONFIRMED (APPLY DDOS <id>), TTL auto-expire, and NEVER auto-applied.
// The anomaly watcher only POSTS A TEXT SUGGESTION (like the anycast blackhole
// watcher) — it never touches a router.
//
// Each node's ncn_ddos table is regenerated wholesale from the active rules that
// target it (one atomic `nft -f` transaction), so add/expire/revoke just re-sync.

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var globalDDoS *ddosStore

const (
	flowspecPath        = incidentsDir + "/flowspec_rules.json"
	ddosExpiryInterval  = 30 * time.Second
	ddosProposeInterval = 2 * time.Minute
	ddosProposeMinPps   = 80000 // dst pps over the flow window that triggers a suggestion
)

type flowspecRule struct {
	ID          string   `json:"id"`
	Family      string   `json:"family"` // ip | ip6 (inferred from dst/src)
	Src         string   `json:"src,omitempty"`
	Dst         string   `json:"dst,omitempty"`
	Proto       string   `json:"proto,omitempty"` // tcp | udp | icmp | icmpv6 | "" (any)
	SrcPort     int      `json:"src_port,omitempty"`
	DstPort     int      `json:"dst_port,omitempty"`
	Action      string   `json:"action"`             // drop | rate
	RatePps     int      `json:"rate_pps,omitempty"` // for action=rate
	TTLSecs     int      `json:"ttl_secs"`
	Note        string   `json:"note,omitempty"`
	CreatedBy   string   `json:"created_by"`
	CreatedAt   int64    `json:"created_at"`
	ExpiresAt   int64    `json:"expires_at,omitempty"`
	AppliedPops []string `json:"applied_pops,omitempty"`
	Status      string   `json:"status"` // draft | active | expired | revoked
}

// nftLine renders the rule as an nft match+verdict (no chain prefix).
func (r *flowspecRule) nftLine() string {
	fam := r.Family
	if fam == "" {
		if strings.Contains(r.Dst, ":") || strings.Contains(r.Src, ":") {
			fam = "ip6"
		} else {
			fam = "ip6" // our network is v6-primary; default v6
		}
	}
	var p []string
	if r.Src != "" {
		p = append(p, fam, "saddr", r.Src)
	}
	if r.Dst != "" {
		p = append(p, fam, "daddr", r.Dst)
	}
	switch r.Proto {
	case "tcp", "udp":
		if r.SrcPort > 0 || r.DstPort > 0 {
			seg := r.Proto
			if r.SrcPort > 0 {
				seg += " sport " + strconv.Itoa(r.SrcPort)
			}
			if r.DstPort > 0 {
				seg += " dport " + strconv.Itoa(r.DstPort)
			}
			p = append(p, seg)
		} else {
			p = append(p, "meta l4proto "+r.Proto)
		}
	case "icmp":
		p = append(p, "meta l4proto icmp")
	case "icmpv6", "icmp6":
		p = append(p, "meta l4proto ipv6-icmp")
	}
	verdict := "drop"
	if r.Action == "rate" && r.RatePps > 0 {
		verdict = fmt.Sprintf("limit rate over %d/second drop", r.RatePps)
	}
	p = append(p, verdict)
	return strings.TrimSpace(strings.Join(p, " "))
}

func (r *flowspecRule) summary() string {
	parts := []string{}
	if r.Src != "" {
		parts = append(parts, "src "+r.Src)
	}
	if r.Dst != "" {
		parts = append(parts, "dst "+r.Dst)
	}
	if r.Proto != "" {
		parts = append(parts, r.Proto)
	}
	if r.DstPort > 0 {
		parts = append(parts, "dport "+strconv.Itoa(r.DstPort))
	}
	act := "drop"
	if r.Action == "rate" {
		act = fmt.Sprintf("rate≤%d pps", r.RatePps)
	}
	parts = append(parts, "→ "+act)
	return strings.Join(parts, " ")
}

type ddosStore struct {
	mu     sync.Mutex
	fleet  *fleetScraper
	notify *tgNotifier
	rules  map[string]*flowspecRule
}

func newDDoSStore(fleet *fleetScraper, notify *tgNotifier) *ddosStore {
	s := &ddosStore{fleet: fleet, notify: notify, rules: map[string]*flowspecRule{}}
	s.load()
	return s
}

func (s *ddosStore) load() {
	var doc []byte
	if globalDB != nil {
		if b, err := loadConfigDoc("flowspec_rules"); err == nil && b != nil {
			doc = b
		}
	}
	if doc == nil {
		if b, err := os.ReadFile(flowspecPath); err == nil && len(b) > 0 {
			doc = b
		}
	}
	if doc != nil {
		_ = json.Unmarshal(doc, &s.rules)
	}
}

func (s *ddosStore) persistLocked() {
	b, err := json.Marshal(s.rules)
	if err != nil {
		return
	}
	writeFileAtomic(flowspecPath, b)
	if globalDB != nil {
		if err := saveConfigDoc("flowspec_rules", b); err != nil {
			log.Printf("ddos: db persist failed (%v) — file is current", err)
		}
	}
}

// activeForNodeLocked returns the live (active, unexpired) rules targeting a node.
func (s *ddosStore) activeForNodeLocked(node string, now int64) []*flowspecRule {
	var out []*flowspecRule
	for _, r := range s.rules {
		if r.Status != "active" {
			continue
		}
		if r.ExpiresAt > 0 && now >= r.ExpiresAt {
			continue
		}
		for _, p := range r.AppliedPops {
			if p == node {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// genNftFile builds the atomic `nft -f` script for a node from its active rules.
// Always emits the table + both chains so an empty set cleanly means "no drops".
func (s *ddosStore) genNftFile(node string, now int64) string {
	var b strings.Builder
	b.WriteString("add table inet ncn_ddos\n")
	b.WriteString("flush table inet ncn_ddos\n")
	b.WriteString("add chain inet ncn_ddos c_in { type filter hook input priority -300 ; policy accept ; }\n")
	b.WriteString("add chain inet ncn_ddos c_fwd { type filter hook forward priority -300 ; policy accept ; }\n")
	for _, r := range s.activeForNodeLocked(node, now) {
		line := r.nftLine()
		fmt.Fprintf(&b, "add rule inet ncn_ddos c_in %s comment \"%s\"\n", line, r.ID)
		fmt.Fprintf(&b, "add rule inet ncn_ddos c_fwd %s comment \"%s\"\n", line, r.ID)
	}
	return b.String()
}

// snapshot returns the rules for the UI, newest first.
func (s *ddosStore) snapshot() []flowspecRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]flowspecRule, 0, len(s.rules))
	for _, r := range s.rules {
		out = append(out, *r)
	}
	// newest first
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].CreatedAt > out[i].CreatedAt {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func ddosNewID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ── expiry loop + anomaly propose ────────────────────────────────────────────

func (s *ddosStore) Start(ctx context.Context) {
	go func() {
		exp := time.NewTicker(ddosExpiryInterval)
		prop := time.NewTicker(ddosProposeInterval)
		defer exp.Stop()
		defer prop.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-exp.C:
				s.expire(ctx)
			case <-prop.C:
				s.propose()
			}
		}
	}()
}

// expire marks rules past their TTL expired and re-syncs the nodes they were on
// (so the drop actually lifts on the routers), then notifies.
func (s *ddosStore) expire(ctx context.Context) {
	now := time.Now().Unix()
	affected := map[string]bool{}
	var expired []string
	s.mu.Lock()
	for _, r := range s.rules {
		if r.Status == "active" && r.ExpiresAt > 0 && now >= r.ExpiresAt {
			r.Status = "expired"
			expired = append(expired, r.ID)
			for _, p := range r.AppliedPops {
				affected[p] = true
			}
		}
	}
	if len(expired) > 0 {
		s.persistLocked()
	}
	s.mu.Unlock()
	if len(expired) == 0 {
		return
	}
	for node := range affected {
		_, _ = s.syncNode(ctx, node, func(string) {})
	}
	if s.notify != nil {
		ch := s.notify.errorChat
		if ch == "" {
			ch = s.notify.chatID
		}
		s.notify.enqueue(tgPayload{ChatID: ch, Text: fmt.Sprintf("🌀 DDoS 缓解规则到期已自动撤销: %s", strings.Join(expired, ", "))}, "ddos-expire")
	}
}

// propose inspects current flow data for an abnormal destination and POSTS A
// TEXT SUGGESTION only — never applies. Inert until the sFlow collector feeds
// netflow (no flows → nothing to suggest).
func (s *ddosStore) propose() {
	if s.notify == nil || globalNetflow == nil {
		return
	}
	res := globalNetflow.top()
	dst, _ := res["dst_ip"].([]flowEntry)
	if len(dst) == 0 {
		return
	}
	winSecs := flowWindow.Seconds()
	covered := func(ip string) bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, r := range s.rules {
			if r.Status == "active" && r.Dst == ip {
				return true
			}
		}
		return false
	}
	for _, e := range dst {
		pps := float64(e.Packets) / winSecs
		if pps < ddosProposeMinPps || covered(e.Key) {
			continue
		}
		ch := s.notify.errorChat
		if ch == "" {
			ch = s.notify.chatID
		}
		s.notify.enqueue(tgPayload{ChatID: ch, Text: fmt.Sprintf(
			"🚨 <b>疑似 DDoS</b>\n目标 <code>%s</code> 收包速率 ~%.0f pps(采样窗口 %d 分钟)\n建议(需人工确认): 在 控制台 → 缓解 生成 dst=%s 的 drop 规则并下发。\n<blockquote>仅提议,不自动下发。</blockquote>",
			e.Key, pps, int(winSecs/60), e.Key)}, "ddos-propose-"+e.Key)
	}
}
