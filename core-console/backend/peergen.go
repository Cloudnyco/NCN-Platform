// peergen.go — turn an approved peering application + an IRR-expanded prefix
// set into a reviewable per-peer BIRD config, and remember what was generated
// (for diffing/drift). Pure generator (genPeerConfig has no side effects) plus
// a small dual-write store. Applying the config to a router is peerApply.go;
// this file never touches a node.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const peerGenerationsPath = authConfigDir + "/peer-generations.json"

// peerGeneration is a stored, versioned per-peer config (keyed by ASN).
type peerGeneration struct {
	ASN          uint32    `json:"asn"`
	AppID        string    `json:"app_id,omitempty"`
	NetworkName  string    `json:"network_name,omitempty"`
	ASSet        string    `json:"as_set,omitempty"`
	IRRSource    string    `json:"irr_source,omitempty"`
	NeighborV6   string    `json:"neighbor_v6,omitempty"`
	PrefixCount  int       `json:"prefix_count"`
	PrefixHash   string    `json:"prefix_hash"` // sha256 of the sorted prefixes → drift signal
	MaxPrefix    int       `json:"max_prefix"`
	Config       string    `json:"config"` // full peer_as<asn>.conf text
	Warnings     []string  `json:"warnings,omitempty"`
	Status       string    `json:"status"` // generated | applied | drifted
	GeneratedAt  time.Time `json:"generated_at"`
	GeneratedBy  string    `json:"generated_by,omitempty"`
	AppliedAt    time.Time `json:"applied_at,omitempty"`
	AppliedNodes []string  `json:"applied_nodes,omitempty"`
}

// genPeerConfig is the pure generator. hasROV = the target PoP runs the
// roa_ncn community tagging (so the RPKI-invalid gate is meaningful).
func genPeerConfig(app PeeringApplication, irr *irrResult, neighborV6 string, hasROV bool, actor string) *peerGeneration {
	asn := app.ASN
	var warn []string

	maxPfx := app.MaxPrefix6
	if maxPfx <= 0 {
		// No operator value → headroom over the current count so legitimate
		// growth doesn't trip the limit, but a leak still does.
		maxPfx = irr.PrefixCount + irr.PrefixCount/2 + 100
		warn = append(warn, fmt.Sprintf("申请未填 max-prefix6,按当前前缀数自动取 %d(=1.5×+100);建议核实后填实际值", maxPfx))
	}

	neighborV6 = strings.TrimSpace(neighborV6)
	neighborLine := fmt.Sprintf("neighbor %s as %d;", neighborV6, asn)
	if neighborV6 == "" {
		neighborLine = fmt.Sprintf("neighbor <填入 peer 的 v6 会话地址> as %d;", asn)
		warn = append(warn, "未提供 neighbor v6 地址,会话行留占位符——应用前必须填写")
	}

	// RPKI-invalid gate via the already-deployed (MY_ASN,1000,0) community tag,
	// NOT roa_check() — roa_check crashes BIRD 3.3.1, the community match works
	// on every version. If the target PoP doesn't tag (no ROV), emit it as a
	// comment so a non-tagging node still parses, and warn.
	rpkiGate := fmt.Sprintf("\tif (MY_ASN, 1000, 0) ~ bgp_large_community then return false;   # RPKI invalid(社区标记,非 roa_check)")
	if !hasROV {
		rpkiGate = "\t# (目标 PoP 未运行 ROV/roa_ncn 标记,RPKI invalid 门已注释;启用 soft-check 后取消注释)\n\t# if (MY_ASN, 1000, 0) ~ bgp_large_community then return false;"
		warn = append(warn, "目标 PoP 未运行 ROV(roa_ncn 社区标记),生成的配置中 RPKI-invalid 门为注释状态")
	}

	pfxName := fmt.Sprintf("PEER_AS%d_PFX", asn)
	var b strings.Builder
	fmt.Fprintf(&b, "# === NCN peer AS%d %s ===\n", asn, app.NetworkName)
	fmt.Fprintf(&b, "# 生成于 %s · AS-SET %s via %s · %d 条前缀 · bgpq4\n", irr.GeneratedAt.Format(time.RFC3339), irr.ASSet, irr.Source, irr.PrefixCount)
	fmt.Fprintf(&b, "# 审阅后应用:写入本文件 + `birdc configure soft`(见 peer-apply runbook)。请勿手工乱改。\n\n")
	fmt.Fprintf(&b, "define PEER_AS%d_MAXPFX = %d;\n\n", asn, maxPfx)
	// irr.BirdSet is `PEER_AS<asn>_PFX = [ ... ];` (bgpq4 -b output); make it a define.
	fmt.Fprintf(&b, "define %s\n\n", strings.TrimSpace(irr.BirdSet))
	fmt.Fprintf(&b, "function peer_as%d_import() -> bool {\n", asn)
	fmt.Fprintf(&b, "\tif ! (net ~ %s) then return false;   # IRR 前缀门(AS-SET 派生)\n", pfxName)
	fmt.Fprintf(&b, "%s\n", rpkiGate)
	fmt.Fprintf(&b, "\treturn ebgp_import_filter();   # 共享 sanity:路径长度、自身前缀、region tag 等\n")
	fmt.Fprintf(&b, "}\n\n")
	fmt.Fprintf(&b, "protocol bgp peer_as%d from ebgp_peer_tpl {\n", asn)
	fmt.Fprintf(&b, "\t%s\n", neighborLine)
	fmt.Fprintf(&b, "\tipv6 {\n")
	fmt.Fprintf(&b, "\t\timport limit PEER_AS%d_MAXPFX action block;   # max-prefix\n", asn)
	fmt.Fprintf(&b, "\t\timport filter {\n")
	fmt.Fprintf(&b, "\t\t\tif peer_as%d_import() then {\n", asn)
	fmt.Fprintf(&b, "\t\t\t\tbgp_large_community.add(NCN_FROM_PEER);\n")
	fmt.Fprintf(&b, "\t\t\t\tbgp_local_pref = bgp_local_pref + 1;\n")
	fmt.Fprintf(&b, "\t\t\t\taccept;\n\t\t\t}\n\t\t\treject;\n\t\t};\n")
	fmt.Fprintf(&b, "\t\texport where ebgp_peer_export_filter();\n")
	fmt.Fprintf(&b, "\t};\n}\n")

	return &peerGeneration{
		ASN: asn, AppID: app.ID, NetworkName: app.NetworkName, ASSet: irr.ASSet,
		IRRSource: app.IRRSource, NeighborV6: neighborV6,
		PrefixCount: irr.PrefixCount, PrefixHash: prefixHash(irr.BirdSet), MaxPrefix: maxPfx,
		Config: b.String(), Warnings: warn, Status: "generated",
		GeneratedAt: time.Now().UTC(), GeneratedBy: actor,
	}
}

// prefixHash is a stable fingerprint of the prefix set (sorted CIDR lines),
// used to detect IRR drift between regenerations.
func prefixHash(birdSet string) string {
	var cidrs []string
	for _, ln := range strings.Split(birdSet, "\n") {
		ln = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(ln), ","))
		if strings.Contains(ln, "/") && !strings.Contains(ln, "[") {
			cidrs = append(cidrs, ln)
		}
	}
	sort.Strings(cidrs)
	sum := sha256.Sum256([]byte(strings.Join(cidrs, "\n")))
	return hex.EncodeToString(sum[:])
}

// ---- store (dual-write file + Postgres, keyed by ASN) ----------------------

type peerGenStore struct {
	mu   sync.RWMutex
	gens map[uint32]*peerGeneration
}

var globalPeerGen *peerGenStore

func newPeerGenStore() *peerGenStore {
	s := &peerGenStore{gens: map[uint32]*peerGeneration{}}
	loaded := false
	if globalDB != nil {
		if doc, err := loadConfigDoc("peer_generations"); err == nil && doc != nil {
			_ = json.Unmarshal(doc, &s.gens)
			loaded = true
		}
	}
	if !loaded {
		if b, err := os.ReadFile(peerGenerationsPath); err == nil && len(b) > 0 {
			_ = json.Unmarshal(b, &s.gens)
		}
	}
	if s.gens == nil {
		s.gens = map[uint32]*peerGeneration{}
	}
	return s
}

func (s *peerGenStore) persistLocked() {
	b, err := json.MarshalIndent(s.gens, "", "  ")
	if err != nil {
		return
	}
	tmp := peerGenerationsPath + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, peerGenerationsPath)
	}
	if globalDB != nil {
		_ = saveConfigDoc("peer_generations", b)
	}
}

func (s *peerGenStore) put(g *peerGeneration) {
	s.mu.Lock()
	s.gens[g.ASN] = g
	s.persistLocked()
	s.mu.Unlock()
}

func (s *peerGenStore) get(asn uint32) *peerGeneration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gens[asn]
}

func (s *peerGenStore) list() []*peerGeneration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*peerGeneration, 0, len(s.gens))
	for _, g := range s.gens {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ASN < out[j].ASN })
	return out
}
