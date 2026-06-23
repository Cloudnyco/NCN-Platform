// peerRefresh.go — periodic IRR drift detection for applied peer configs. Every
// ~12h it re-expands each applied peer's AS-SET via bgpq4 and compares the
// prefix-set fingerprint to what was applied. On change it alerts the ops
// channel and marks the generation "drifted" so the console shows a badge —
// it NEVER pushes to a router. An operator regenerates + re-applies by hand.
package main

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"
)

const peerRefreshEvery = 12 * time.Hour

var globalPeerRefresh *peerRefresher

type peerRefresher struct {
	fleet   *fleetScraper
	notify  *tgNotifier
	node    string            // IRR node (where bgpq4 runs)
	alerted map[uint32]string // asn → prefix hash last alerted (dedup)
}

func newPeerRefresher(f *fleetScraper, n *tgNotifier, node string) *peerRefresher {
	return &peerRefresher{fleet: f, notify: n, node: node, alerted: map[uint32]string{}}
}

func (m *peerRefresher) Start(ctx context.Context) {
	if m.fleet == nil {
		return
	}
	go func() {
		time.Sleep(2 * time.Minute) // let startup settle
		t := time.NewTicker(peerRefreshEvery)
		defer t.Stop()
		m.check(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.check(ctx)
			}
		}
	}()
}

func (m *peerRefresher) check(ctx context.Context) {
	if globalPeerGen == nil {
		return
	}
	for _, g := range globalPeerGen.list() {
		if g.Status != "applied" && g.Status != "drifted" {
			continue
		}
		asSet := g.ASSet
		if asSet == "" {
			continue
		}
		name := fmt.Sprintf("PEER_AS%d_PFX", g.ASN)
		cctx, cancel := context.WithTimeout(ctx, 75*time.Second)
		irr, err := expandASSet(cctx, m.fleet, m.node, name, asSet, g.IRRSource)
		cancel()
		if err != nil {
			continue // transient IRR/bgpq4 failure — don't alert, retry next cycle
		}
		nh := prefixHash(irr.BirdSet)
		if nh == g.PrefixHash {
			continue // no drift
		}
		if m.alerted[g.ASN] == nh {
			continue // already alerted for this exact drift
		}
		m.alerted[g.ASN] = nh
		delta := irr.PrefixCount - g.PrefixCount
		m.alert(g, irr.PrefixCount, delta)
		// Mark drifted (keep the applied config + baseline hash as-is so the UI
		// can still show what's live; the operator regenerates to refresh it).
		g.Status = "drifted"
		g.Warnings = append([]string{fmt.Sprintf("IRR 漂移:AS-SET %s 现为 %d 条前缀(应用时 %d,%+d);请重新生成并应用", asSet, irr.PrefixCount, g.PrefixCount, delta)}, g.Warnings...)
		globalPeerGen.put(g)
	}
}

func (m *peerRefresher) alert(g *peerGeneration, newCount, delta int) {
	if m.notify == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🧭 <b>Peer IRR 漂移 · AS%d</b> %s\n", g.ASN, html.EscapeString(g.NetworkName))
	fmt.Fprintf(&b, "AS-SET <code>%s</code> 前缀数 %d → <b>%d</b>(%+d)。\n", html.EscapeString(g.ASSet), g.PrefixCount, newCount, delta)
	b.WriteString("<i>已应用的过滤表已过期。请在控制台重新生成 peer 配置并人工确认应用。不自动推送。</i>")
	channel := m.notify.errorChat
	if channel == "" {
		channel = m.notify.chatID
	}
	m.notify.enqueue(tgPayload{ChatID: channel, Text: b.String()}, "peer-drift")
}
