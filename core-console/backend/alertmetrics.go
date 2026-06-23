// Alert metric extractors — the only code half of the data-driven rule engine.
//
// A rule is data: (metric, op, threshold). The metric names a function here
// that pulls ONE number out of a node's scraped status; the engine then
// compares it against the threshold with the op. Structured conditions that
// used to be hand-written Go (probe 3×-consecutive failure, BGP unhealthy
// count, whole-node ConsecFail, BIRD-absent heuristic) live INSIDE the
// extractor that computes their derived count — so every rule, simple or
// structured, reduces to one comparison.
//
// Adding a metric here is the ONLY way to expose a new field to user rules:
// custom rules pick a metric from this whitelist, never an arbitrary field
// path. That keeps the injection surface closed.

package main

import (
	"fmt"
	"strings"
)

// metricExtractor returns (value, ok). ok=false means "not applicable / not
// known yet" for this node → the rule does NOT fire (e.g. cert days unknown
// before the first 24h sweep). Most extractors always return ok=true.
type metricExtractor func(*fleetNodeStatus) (float64, bool)

// metricMeta drives the frontend dropdown + validation.
type metricMeta struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Unit  string `json:"unit"`  // "%", "", "ms", "GB", "count", "bool"
	Hint  string `json:"hint"`
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// metricExtractors is the whitelist. Keys are stable rule identifiers.
var metricExtractors = map[string]metricExtractor{
	"cpu_pct":  func(s *fleetNodeStatus) (float64, bool) { return s.CPUPct, true },
	"mem_pct":  func(s *fleetNodeStatus) (float64, bool) { return s.MemPct, true },
	"disk_pct": func(s *fleetNodeStatus) (float64, bool) { return s.DiskPct, true },
	"load1":    func(s *fleetNodeStatus) (float64, bool) { return s.Load1, true },

	"net_rx_mbps": func(s *fleetNodeStatus) (float64, bool) { return s.NetRxBps / 1e6, true },
	"net_tx_mbps": func(s *fleetNodeStatus) (float64, bool) { return s.NetTxBps / 1e6, true },
	// Totals across ALL interfaces (incl. wg*/tun*) — better DDoS-surge signal
	// than the single default iface above. Falls back to the default iface
	// pre-Ifaces (first tick / older agent).
	"net_rx_total_mbps": func(s *fleetNodeStatus) (float64, bool) {
		if len(s.Ifaces) == 0 {
			return s.NetRxBps / 1e6, true
		}
		var sum float64
		for _, i := range s.Ifaces {
			sum += i.RxBps
		}
		return sum / 1e6, true
	},
	"net_tx_total_mbps": func(s *fleetNodeStatus) (float64, bool) {
		if len(s.Ifaces) == 0 {
			return s.NetTxBps / 1e6, true
		}
		var sum float64
		for _, i := range s.Ifaces {
			sum += i.TxBps
		}
		return sum / 1e6, true
	},
	"mem_used_gb":  func(s *fleetNodeStatus) (float64, bool) { return float64(s.MemUsed) / (1 << 30), true },
	"disk_used_gb": func(s *fleetNodeStatus) (float64, bool) { return float64(s.DiskUsed) / (1 << 30), true },

	// cert_days_left: 0 = unknown (first 24h sweep hasn't run) → ok=false so
	// the rule stays quiet; negative = already expired (still < any positive
	// threshold, so `lte 30` correctly fires). Mirrors agent-cert-expiring.
	"cert_days_left": func(s *fleetNodeStatus) (float64, bool) {
		if s.AgentCertDaysLeft == 0 {
			return 0, false
		}
		return float64(s.AgentCertDaysLeft), true
	},

	// consec_fail: consecutive failed scrapes (0 when OK). node-unreachable =
	// consec_fail gte 3. Quiet while reachable.
	"consec_fail": func(s *fleetNodeStatus) (float64, bool) { return float64(s.ConsecFail), true },

	// reachable: 1 when the last scrape succeeded, else 0.
	"reachable": func(s *fleetNodeStatus) (float64, bool) { return b2f(s.OK), true },

	// probe_fail_count: probes whose series tail is ALL failures for
	// probeDownConsecutive samples — the exact probe-down condition.
	"probe_fail_count": func(s *fleetNodeStatus) (float64, bool) {
		n := 0
		for _, p := range s.Probes {
			if !p.LastOK && p.LastTime > 0 && probeSeriesAllFail(p.Series, probeDownConsecutive) {
				n++
			}
		}
		return float64(n), true
	},

	// probe_slow_count: probes up but RTT over 200ms — the probe-slow condition
	// (the 200 is baked into the historical rule; a custom rule can instead use
	// probe_max_rtt_ms below with its own threshold).
	"probe_slow_count": func(s *fleetNodeStatus) (float64, bool) {
		n := 0
		for _, p := range s.Probes {
			if p.LastOK && p.LastMS > 200 {
				n++
			}
		}
		return float64(n), true
	},

	// probe_max_rtt_ms: worst current RTT across up probes (0 if none up).
	"probe_max_rtt_ms": func(s *fleetNodeStatus) (float64, bool) {
		max := 0.0
		for _, p := range s.Probes {
			if p.LastOK && p.LastMS > max {
				max = p.LastMS
			}
		}
		return max, true
	},

	// bgp_down_count: BGP protocols that are genuinely unhealthy (defers to the
	// parser's Healthy flag, which already treats passive listeners as fine) —
	// the bgp-peer-down condition.
	"bgp_down_count": func(s *fleetNodeStatus) (float64, bool) {
		n := 0
		for _, p := range s.Protocols {
			if p.Proto == "BGP" && !p.Healthy {
				n++
			}
		}
		return float64(n), true
	},

	// bird_absent: 1 when a successful scrape returned neither a version banner
	// nor any protocol (BIRD likely down) — the bird-unreachable heuristic.
	// 0 when bird is demonstrably reachable OR the scrape itself failed (that's
	// node-unreachable's job).
	"bird_absent": func(s *fleetNodeStatus) (float64, bool) {
		if !s.OK || s.FetchedAt == 0 {
			return 0, true
		}
		if s.BirdVer != "" || len(s.Protocols) > 0 {
			return 0, true
		}
		return 1, true
	},

	"route_count_total": func(s *fleetNodeStatus) (float64, bool) {
		total := 0
		for _, rc := range s.RouteCounts {
			total += rc.Count
		}
		return float64(total), true
	},
	"wg_peer_count": func(s *fleetNodeStatus) (float64, bool) {
		n := 0
		for _, w := range s.WG {
			n += len(w.Peers)
		}
		return float64(n), true
	},
	"tunnel_down_count": func(s *fleetNodeStatus) (float64, bool) {
		n := 0
		for _, t := range s.Tunnels {
			if !t.Up {
				n++
			}
		}
		return float64(n), true
	},

	// link_saturation_eta_days: forecast days until this node's busier traffic
	// direction reaches 90% of its configured link capacity (capacity.go, least-
	// squares over the daily p95). ok=false when there's no forecast (unknown
	// capacity / not enough history / no upward trend) → the rule stays quiet.
	"link_saturation_eta_days": func(s *fleetNodeStatus) (float64, bool) {
		if globalCapacity == nil {
			return 0, false
		}
		return globalCapacity.etaDays(s.Node.ID)
	},

	// sla_loss_pct: worst short-window packet loss (%) across this PoP's SLA
	// targets (sla.go probes prefixed "sla:"), computed from the probe series
	// tail. ok=false when the node runs no SLA probes yet → rule stays quiet.
	"sla_loss_pct": func(s *fleetNodeStatus) (float64, bool) {
		worst, any := 0.0, false
		for _, p := range s.Probes {
			if !strings.HasPrefix(p.Name, slaProbePrefix) || len(p.Series) == 0 {
				continue
			}
			any = true
			if l := seriesLossPct(p.Series, slaLossWindow); l > worst {
				worst = l
			}
		}
		if !any {
			return 0, false
		}
		return worst, true
	},

	// sla_rtt_ms: worst current RTT across this PoP's up SLA targets.
	"sla_rtt_ms": func(s *fleetNodeStatus) (float64, bool) {
		worst, any := 0.0, false
		for _, p := range s.Probes {
			if !strings.HasPrefix(p.Name, slaProbePrefix) {
				continue
			}
			if p.LastOK {
				any = true
				if p.LastMS > worst {
					worst = p.LastMS
				}
			}
		}
		if !any {
			return 0, false
		}
		return worst, true
	},

	// config_drift: 1 when this node's live config (bird.conf / filters / nft)
	// differs from its adopted baseline (configdrift.go). ok=false when no
	// baseline has been adopted → the rule stays quiet.
	"config_drift": func(s *fleetNodeStatus) (float64, bool) {
		if globalDrift == nil {
			return 0, false
		}
		return globalDrift.driftMetric(s.Node.ID)
	},
}

// metricDetail names WHICH entities tripped a count metric — which BGP peer,
// which tunnel, which probe — so the alert message (web + Telegram) reads
// "bgp_down_count=2 · peers: ncn_fra01 (Active), ncn_sin01 (Connect)" instead
// of a bare number. Only metrics whose count hides specific subjects need an
// entry; metrics that are already self-describing (cpu_pct, …) have none.
var metricDetail = map[string]func(*fleetNodeStatus) string{
	"bgp_down_count": func(s *fleetNodeStatus) string {
		var names []string
		for _, p := range s.Protocols {
			if p.Proto == "BGP" && !p.Healthy {
				d := p.Name
				st := strings.TrimSpace(p.State + " " + p.Info)
				if st != "" {
					d += " (" + st + ")"
				}
				names = append(names, d)
			}
		}
		return joinDetail("peer", names)
	},
	"tunnel_down_count": func(s *fleetNodeStatus) string {
		var names []string
		for _, t := range s.Tunnels {
			if !t.Up {
				d := t.Name
				if t.Remote != "" {
					d += "→" + t.Remote
				}
				names = append(names, d)
			}
		}
		return joinDetail("tunnel", names)
	},
	"probe_fail_count": func(s *fleetNodeStatus) string {
		var names []string
		for _, p := range s.Probes {
			if !p.LastOK && p.LastTime > 0 && probeSeriesAllFail(p.Series, probeDownConsecutive) {
				names = append(names, p.Name)
			}
		}
		return joinDetail("probe", names)
	},
	"probe_slow_count": func(s *fleetNodeStatus) string {
		var names []string
		for _, p := range s.Probes {
			if p.LastOK && p.LastMS > 200 {
				names = append(names, fmt.Sprintf("%s %.0fms", p.Name, p.LastMS))
			}
		}
		return joinDetail("probe", names)
	},
}

// joinDetail formats a named list ("peer: a, b" / "peers: a, b, +3 more"),
// capping at 6 so a fleet-wide outage doesn't produce a wall of text.
func joinDetail(noun string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	label := noun
	if len(names) > 1 {
		label = noun + "s"
	}
	const max = 6
	if len(names) > max {
		extra := fmt.Sprintf("+%d more", len(names)-max)
		names = append(append([]string{}, names[:max]...), extra)
	}
	return label + ": " + strings.Join(names, ", ")
}

// metricCatalog is the ordered metadata served to the UI (stable order).
var metricCatalog = []metricMeta{
	{"cpu_pct", "CPU 使用率", "%", "1m CPU 利用率"},
	{"mem_pct", "内存使用率", "%", "已用内存占比"},
	{"disk_pct", "磁盘使用率", "%", "根分区使用率"},
	{"load1", "Load (1m)", "", "1 分钟平均负载"},
	{"net_rx_mbps", "入向带宽", "Mbps", "接收速率"},
	{"net_tx_mbps", "出向带宽", "Mbps", "发送速率"},
	{"net_rx_total_mbps", "总入向带宽", "Mbps", "全部接口接收合计(DDoS 突增检测)"},
	{"net_tx_total_mbps", "总出向带宽", "Mbps", "全部接口发送合计"},
	{"mem_used_gb", "已用内存", "GB", ""},
	{"disk_used_gb", "已用磁盘", "GB", ""},
	{"cert_days_left", "Agent 证书剩余", "天", "0=未知则跳过;负=已过期"},
	{"consec_fail", "连续抓取失败", "次", "节点不可达=≥3"},
	{"reachable", "可达 (1/0)", "bool", "最近一次抓取是否成功"},
	{"probe_fail_count", "失败探针数", "个", "连续失败 ≥3 次的探针"},
	{"probe_slow_count", "慢探针数 (>200ms)", "个", ""},
	{"probe_max_rtt_ms", "最大探针 RTT", "ms", "在线探针中的最差 RTT"},
	{"bgp_down_count", "异常 BGP 邻居数", "个", "非健康(忽略 passive)"},
	{"bird_absent", "BIRD 缺失 (1/0)", "bool", "抓取成功但无版本+0协议"},
	{"route_count_total", "路由总数", "条", ""},
	{"wg_peer_count", "WG 对端数", "个", ""},
	{"tunnel_down_count", "隧道 down 数", "个", ""},
	{"link_saturation_eta_days", "链路饱和预计(天)", "天", "按 p95 趋势预计达 90% 容量;无趋势则跳过"},
	{"sla_loss_pct", "SLA 丢包率", "%", "各 SLA 目标最近窗口最差丢包率"},
	{"sla_rtt_ms", "SLA 最大 RTT", "ms", "各 SLA 目标当前最差 RTT"},
	{"config_drift", "配置漂移", "bool", "实际 bird/filters/nft 偏离声明基线"},
}

// alertOp comparators.
type alertOp string

const (
	opGt  alertOp = "gt"
	opGte alertOp = "gte"
	opLt  alertOp = "lt"
	opLte alertOp = "lte"
	opEq  alertOp = "eq"
	opNe  alertOp = "ne"
)

var alertOpSymbol = map[alertOp]string{opGt: ">", opGte: "≥", opLt: "<", opLte: "≤", opEq: "==", opNe: "≠"}

func validAlertOp(op alertOp) bool { _, ok := alertOpSymbol[op]; return ok }

func cmpMetric(v float64, op alertOp, thr float64) bool {
	switch op {
	case opGt:
		return v > thr
	case opGte:
		return v >= thr
	case opLt:
		return v < thr
	case opLte:
		return v <= thr
	case opEq:
		return v == thr
	case opNe:
		return v != thr
	}
	return false
}
