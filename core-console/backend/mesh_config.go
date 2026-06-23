// Internal-mesh + BIRD config GENERATOR.
//
// Given the node registry, this renders review-ready config for a node's
// internal mesh — strictly following the conventions already on the live PoPs
// (extracted from ctrl-01), NOT improvising:
//
//   * the node's full /etc/bird/bird.conf (defines + standard protocol blocks
//     + a full-mesh of iBGP peer blocks `from ibgp_tpl`),
//   * the shared /etc/bird/filters_templates.conf (embedded verbatim),
//   * GRE or (per-link) WireGuard bring-up commands for each mesh link, and
//   * the snippet to ADD on every existing peer (one iBGP block + one tunnel).
//
// It is a PURE GENERATOR: it never touches a live machine and never runs birdc.
// Upstream eBGP + node-specific statics are emitted as commented placeholders —
// we never invent a neighbour or ASN.

package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"golang.org/x/crypto/curve25519"
)

// filtersTemplatesConf is /etc/bird/filters_templates.conf captured verbatim
// from the live fleet (identical across nodes). Emitted unchanged.
//
//go:embed assets/filters_templates.conf
var filtersTemplatesConf string

// meshIface is the interface / protocol-suffix name for a node: the id with
// dashes removed, matching the live convention (pop-03 → pop03, used as both
// the GRE/WG iface name and the `ibgp_<name>` protocol name).
func meshIface(id string) string { return strings.ReplaceAll(id, "-", "") }

// meshAnchor is the node's v6 anchor on dummy0: 2001:db8:<region>::<num>.
func meshAnchor(region, num int) string { return fmt.Sprintf("2001:db8:%d::%d", region, num) }

// meshLinkLocal is the per-link link-local source: fe80::<region>:<num>.
func meshLinkLocal(region, num int) string { return fmt.Sprintf("fe80::%d:%d", region, num) }

type meshPeerSnippet struct {
	NodeID    string `json:"node_id"`
	Label     string `json:"label"`
	Transport string `json:"transport"` // "gre" | "wg"
	Bird      string `json:"bird"`      // iBGP peer block to add on THIS peer
	Tunnel    string `json:"tunnel"`    // tunnel bring-up commands on THIS peer
}

type meshConfigBundle struct {
	NodeID       string            `json:"node_id"`
	Region       int               `json:"region"`
	NodeNum      int               `json:"node_num"`
	Anchor       string            `json:"anchor"`
	NewNodeBird  string            `json:"new_node_bird"`
	Filters      string            `json:"filters"`
	Bringup      []string          `json:"bringup"`
	PeerSnippets []meshPeerSnippet `json:"peer_snippets"`
	Warnings     []string          `json:"warnings,omitempty"`
}

// genWGKeypair returns a fresh WireGuard (Curve25519) keypair, base64 std —
// the exact format `wg genkey`/`wg pubkey` produce. Private key is for display
// only (it goes onto the operator's machine); never stored server-side.
func genWGKeypair() (priv, pub string, err error) {
	var p [32]byte
	if _, err = rand.Read(p[:]); err != nil {
		return "", "", err
	}
	// Curve25519 clamping (per WireGuard / RFC 7748).
	p[0] &= 248
	p[31] &= 127
	p[31] |= 64
	pubBytes, err := curve25519.X25519(p[:], curve25519.Basepoint)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(p[:]), base64.StdEncoding.EncodeToString(pubBytes), nil
}

// greBringup renders the GRE link bring-up on `selfIP`/`selfRegion`,`selfNum`
// toward `peerIP`, using `tunIface` as the local interface name. Mirrors the
// live convention: IPv4 GRE, ttl 255, MTU 1476, link-local /128 source.
func greBringup(tunIface, selfIP, peerIP string, selfRegion, selfNum int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ip tunnel add %s mode gre remote %s local %s ttl 255\n", tunIface, peerIP, selfIP)
	fmt.Fprintf(&b, "ip link set %s up mtu 1476\n", tunIface)
	fmt.Fprintf(&b, "ip -6 addr add %s/128 dev %s", meshLinkLocal(selfRegion, selfNum), tunIface)
	return b.String()
}

// wgBringup renders a WireGuard link config in the live house style, for the
// END whose key is selfPriv, peering the END at peerPub/peerEndpoint. The
// PostUp installs the link-local /128 ↔ peer link-local convention.
func wgBringup(tunIface, selfPriv string, selfRegion, selfNum int, listenPort int, peerPub, peerEndpoint string, peerRegion, peerNum int) string {
	var b strings.Builder
	b.WriteString("[Interface]\n")
	b.WriteString("Table = off\n")
	b.WriteString("MTU = 1420\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", selfPriv)
	fmt.Fprintf(&b, "ListenPort = %d\n", listenPort)
	fmt.Fprintf(&b, "PostUp = ip addr add dev %%i %s/128 peer %s/128\n", meshLinkLocal(selfRegion, selfNum), meshLinkLocal(peerRegion, peerNum))
	b.WriteString("\n[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", peerPub)
	b.WriteString("AllowedIPs = ::/0\n")
	fmt.Fprintf(&b, "Endpoint = %s", peerEndpoint)
	return b.String()
}

// ibgpBlock renders one full-mesh iBGP peer block in the exact live style
// (tab-indented, `from ibgp_tpl`). peerIface is the link-local zone + the
// protocol-name suffix.
func ibgpBlock(peerIface string, peerRegion, peerNum int) string {
	return fmt.Sprintf("protocol bgp ibgp_%s from ibgp_tpl {\n\tneighbor %s%%%s;\n}",
		peerIface, meshLinkLocal(peerRegion, peerNum), peerIface)
}

// genMeshConfig builds the full review bundle for `target`, peering each entry
// in `peers` (already filtered to active, non-self nodes). transport[peerID]
// selects "gre" (default) or "wg" per link. The WG endpoint port for each link
// is assigned deterministically from wgPortBase upward.
func genMeshConfig(target nodeRecord, peers []nodeRecord, transport map[string]string) (meshConfigBundle, error) {
	const wgPortBase = 51820
	selfIface := meshIface(target.ID)
	bundle := meshConfigBundle{
		NodeID:  target.ID,
		Region:  target.Region,
		NodeNum: target.NodeNum,
		Anchor:  meshAnchor(target.Region, target.NodeNum),
		Filters: filtersTemplatesConf,
	}
	if target.Region <= 0 || target.NodeNum <= 0 {
		bundle.Warnings = append(bundle.Warnings,
			"该节点尚未分配 region / node_num — 先在「编辑」里补全（同城会自动推导）。")
		return bundle, nil
	}

	// Stable peer order (registry/display order is by slice; sort by id for
	// determinism in the generated file).
	sort.Slice(peers, func(i, j int) bool { return peers[i].ID < peers[j].ID })

	// ── new node's bird.conf ──────────────────────────────────────────────
	var bird strings.Builder
	fmt.Fprintf(&bird, `# BIRD configuration
# (C) 2026 Acme Net. All Right Reserved.
#
# Generated by NCN core-console for %s — REVIEW before applying.
# Apply with: birdc configure soft   (never: systemctl restart bird)

define MY_ASN = 64500;
define MY_ADDR = %s;
define MY_ADDR6 = %s;
define MY_SADDR6 = %s;
define MY_SUBNET_V6 = [ 2001:db8:50::/48+ ];
define MY_REGION = %d;

log syslog all;

router id MY_ADDR;

watchdog warning 30 s;
watchdog timeout 120 s;

protocol device {
	scan time 60;
}

protocol kernel {
	ipv6 {
		import none;
		export filter { krt_prefsrc = MY_ADDR6; accept; };
	};
	metric 1024;
	scan time 60;
}

protocol direct direct1 {
	ipv6;
	interface "dummy0";
}

protocol static ncn_agg {
	ipv6;
	route 2001:db8:50::/48 blackhole;
	route 2001:db8:%d::/48 blackhole;
	route 2001:db8:50::/44 blackhole;
}

# ── 上游 transit（eBGP）：按本节点 provider 手动填写，例如：
#   protocol bgp upstream_<provider> from ebgp_upstream_tpl {
#       neighbor <对端 v6> as <对端 ASN>;
#   }
# ── 节点特有 static（on-link / 下游 session）：按需手动添加 ──

include "/etc/bird/filters_templates.conf";

`, target.ID, target.Address, bundle.Anchor, meshLinkLocal(target.Region, target.NodeNum), target.Region, target.Region)

	for _, p := range peers {
		bird.WriteString(ibgpBlock(meshIface(p.ID), p.Region, p.NodeNum))
		bird.WriteString("\n\n")
	}
	bundle.NewNodeBird = strings.TrimRight(bird.String(), "\n") + "\n"

	// ── new node's mesh bring-up (anchor on dummy0 + each link) ───────────
	bundle.Bringup = append(bundle.Bringup,
		"# anchor on dummy0 (this node's loopback identity)",
		"ip link add dummy0 type dummy 2>/dev/null || true; ip link set dummy0 up",
		fmt.Sprintf("ip -6 addr add %s/128 dev dummy0", bundle.Anchor),
	)

	wgPort := wgPortBase
	for _, p := range peers {
		peerIface := meshIface(p.ID)
		if p.Region <= 0 || p.NodeNum <= 0 {
			bundle.Warnings = append(bundle.Warnings,
				fmt.Sprintf("peer %s 缺 region/node_num — 跳过其链路；先补全它的编辑。", p.ID))
			continue
		}
		t := strings.ToLower(transport[p.ID])

		snip := meshPeerSnippet{
			NodeID: p.ID, Label: p.Label, Transport: "gre",
			Bird: ibgpBlock(selfIface, target.Region, target.NodeNum),
		}

		if t == "wg" {
			snip.Transport = "wg"
			// One keypair per END of the link.
			selfPriv, selfPub, err := genWGKeypair()
			if err != nil {
				return bundle, err
			}
			peerPriv, peerPub, err := genWGKeypair()
			if err != nil {
				return bundle, err
			}
			port := wgPort
			wgPort++
			// New node's interface (peers toward p).
			bundle.Bringup = append(bundle.Bringup,
				fmt.Sprintf("# WireGuard link → %s  (/etc/wireguard/%s.conf)", p.ID, peerIface),
				wgBringup(peerIface, selfPriv, target.Region, target.NodeNum, port, peerPub, p.Address+":"+itoa(port+1), p.Region, p.NodeNum),
			)
			// Peer's interface (peers back toward the new node).
			snip.Tunnel = fmt.Sprintf("# /etc/wireguard/%s.conf on %s\n%s",
				selfIface, p.ID,
				wgBringup(selfIface, peerPriv, p.Region, p.NodeNum, port+1, selfPub, target.Address+":"+itoa(port), target.Region, target.NodeNum))
		} else {
			// GRE (default).
			bundle.Bringup = append(bundle.Bringup,
				fmt.Sprintf("# GRE link → %s", p.ID),
				greBringup(peerIface, target.Address, p.Address, target.Region, target.NodeNum),
			)
			snip.Tunnel = fmt.Sprintf("# on %s (%s):\n%s",
				p.ID, p.Address,
				greBringup(selfIface, p.Address, target.Address, p.Region, p.NodeNum))
		}
		bundle.PeerSnippets = append(bundle.PeerSnippets, snip)
	}

	bundle.Warnings = append(bundle.Warnings,
		"核对 ncn_agg 聚合策略是否符合本节点角色（是否 originate 全局 /44 / /48 聚合）。",
		"上游 eBGP 与节点特有 static 为占位 — 按本节点 transit 手动填写。",
		"全 mesh：每台现有节点都需贴它对应的「对端片段」并执行 `birdc configure soft`。")
	return bundle, nil
}

// handleNodeMeshConfig — POST /api/v1/auth/nodes/{id}/mesh-config (admin).
// Body: { "transports": { "<peer-id>": "gre"|"wg" }, "region": <int?> }.
// Pure generation — no side effects, no machine is touched.
func (f *fleetScraper) handleNodeMeshConfig(w http.ResponseWriter, r *http.Request, id string) {
	op := adminOperator(r)
	target, ok := f.registry.get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "node not found"})
		return
	}
	var req struct {
		Transports map[string]string `json:"transports"`
		Region     int               `json:"region"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
	}
	// An explicit region override (new-metro first node, or correction).
	if req.Region > 0 {
		target.Region = req.Region
	}
	if target.NodeNum == 0 {
		target.NodeNum = nodeNumFromID(target.ID)
	}

	// Peers = all other active nodes (full mesh).
	var peers []nodeRecord
	for _, rec := range f.registry.listSnapshot() {
		if rec.ID == id || !rec.active() {
			continue
		}
		peers = append(peers, rec)
	}

	bundle, err := genMeshConfig(target, peers, req.Transports)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	auditRecord(r, AuditEvent{Event: "node.mesh-config", Severity: auditSevInfo, Actor: op, Target: id})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: bundle})
}
