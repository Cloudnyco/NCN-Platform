// Persistent, user-editable alert rules + rule groups.
//
// Replaces the hardcoded builtinRules() as the source of truth for what the
// alert engine evaluates. Same atomic-JSON pattern as noderegistry.go /
// billing.go. First run seeds the 11 historical built-ins + a default group
// scoped to all nodes, so behaviour is byte-identical until someone edits.
//
// A ruleDef is data; the engine compiles it into an evaluator via the metric
// extractor whitelist in alertmetrics.go.

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"sync"
	"time"
)

const alertRulesPath = nodeRegistryDir + "/alert-rules.json"

const allNodesGroupID = "all"

// ruleGroup scopes a set of rules to a subset of nodes and can mute/disable
// them as a unit. Empty NodeIDs AND empty Regions = all nodes.
type ruleGroup struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Description string  `json:"description,omitempty"`
	Enabled   bool      `json:"enabled"`
	NodeIDs   []string  `json:"node_ids,omitempty"`
	Regions   []int     `json:"regions,omitempty"`
	MuteUntil int64     `json:"mute_until,omitempty"` // unix secs; >now = muted

	// Group-level Telegram policy + defaults (all zero-value = current behaviour).
	//   SuppressTG       — true silences TG for EVERY rule in the group (still
	//                      shown in the engine / web UI / /alerts).
	//   MinSeverity      — TG severity floor; "" = global default (crit only).
	//                      Set warn/info to widen what this group pushes to chat.
	//   DefaultSustainSecs — rules in this group with SustainSecs==0 inherit it
	//                      (0 = none). A rule sets its own SustainSecs to override.
	SuppressTG         bool     `json:"suppress_tg,omitempty"`
	MinSeverity        severity `json:"min_severity,omitempty"`
	DefaultSustainSecs int      `json:"default_sustain_secs,omitempty"`

	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// matches reports whether this group's scope covers the given node.
func (g ruleGroup) matches(nodeID string, region int) bool {
	if len(g.NodeIDs) == 0 && len(g.Regions) == 0 {
		return true // unscoped = all
	}
	for _, id := range g.NodeIDs {
		if id == nodeID {
			return true
		}
	}
	for _, r := range g.Regions {
		if r == region {
			return true
		}
	}
	return false
}

// ruleDef is one data-driven rule. Metric+Op+Threshold is the condition;
// SustainSecs delays firing until the condition holds that long (0 = fire on
// first matching tick, the historical behaviour).
type ruleDef struct {
	ID          string    `json:"id"`
	GroupID     string    `json:"group_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Builtin     bool      `json:"builtin"` // seeded; can be tuned/disabled but not deleted
	Metric      string    `json:"metric"`
	Op          alertOp   `json:"op"`
	Threshold   float64   `json:"threshold"`
	// Anomaly mode (all zero-value = a normal fixed-threshold rule). When
	// AnomalySigma > 0 the rule ignores Threshold and instead fires when the
	// metric deviates from its own learned rolling baseline by this many sigma,
	// in the Op's direction (gt = high side, lt = low side, ne = both). Window
	// is the EWMA span in 30s ticks (default 120 ≈ 1h); MinDelta is an absolute
	// floor (metric units) below which a deviation is ignored as jitter.
	AnomalySigma    float64 `json:"anomaly_sigma,omitempty"`
	AnomalyWindow   int     `json:"anomaly_window,omitempty"`
	AnomalyMinDelta float64 `json:"anomaly_min_delta,omitempty"`
	// Debounce + escalation (all zero-value = historical behaviour):
	//   SustainSecs  — condition must hold this long before FIRING (0 = first tick).
	//   ResolveSecs  — condition must be clear this long before RESOLVING (0 = instant).
	//   EscalateSecs — if >0 and severity < crit, auto-bump to crit + re-notify
	//                  once the alert has been firing this long (0 = off).
	//   RepeatSecs   — while firing, re-send the TG nudge every this-many secs
	//                  (0 = one ping per incident, the historical behaviour).
	SustainSecs  int      `json:"sustain_secs,omitempty"`
	ResolveSecs  int      `json:"resolve_secs,omitempty"`
	EscalateSecs int      `json:"escalate_secs,omitempty"`
	RepeatSecs   int      `json:"repeat_secs,omitempty"`
	// GroupKey correlates DIFFERENT rules: first-fires sharing it in one tick
	// coalesce into a single root-cause card + one Agent RCA (alerts.go flushFires).
	GroupKey string `json:"group_key,omitempty"`
	Severity    severity  `json:"severity"`
	Enabled     bool      `json:"enabled"`
	NotifyTG    bool      `json:"notify_tg"`
	MuteUntil   int64     `json:"mute_until,omitempty"` // per-rule silence (unix); effective mute = max(rule, group)
	CreatedBy   string    `json:"created_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type alertRulesFile struct {
	Groups []ruleGroup `json:"groups"`
	Rules  []ruleDef   `json:"rules"`
}

type alertRuleStore struct {
	mu     sync.RWMutex
	path   string
	groups []ruleGroup
	rules  []ruleDef
}

var ruleIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}$`)

func validRuleID(id string) bool { return ruleIDRe.MatchString(id) }

const alertRulesDocID = "singleton"

// newAlertRuleStore loads the rules, preferring Postgres when populated, else
// the JSON file (seeding the historical built-ins on first run). When the DB
// is configured but empty, the file/seed is migrated into it.
func newAlertRuleStore() (*alertRuleStore, error) {
	if err := os.MkdirAll(nodeRegistryDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", nodeRegistryDir, err)
	}
	s := &alertRuleStore{path: alertRulesPath}

	// Prefer Postgres when it already holds the document (post-cutover).
	loadedFromDB := false
	if globalDB != nil {
		if f, err := alertRulesLoadDB(); err != nil {
			log.Printf("alertrules: db load failed (%v) — falling back to file", err)
		} else if f != nil {
			s.groups, s.rules = f.Groups, f.Rules
			loadedFromDB = true
		}
	}

	// Otherwise load the JSON file, seeding the built-ins on first run.
	seeded := false
	if !loadedFromDB {
		b, err := os.ReadFile(alertRulesPath)
		switch {
		case err == nil && len(b) > 0:
			var f alertRulesFile
			if err := json.Unmarshal(b, &f); err != nil {
				return nil, fmt.Errorf("parse %s: %w", alertRulesPath, err)
			}
			s.groups, s.rules = f.Groups, f.Rules
		default:
			s.groups, s.rules = seedAlertRules()
			seeded = true
		}
	}

	// Forward-compat: ensure the default group always exists.
	ensuredDefault := false
	if s.group(allNodesGroupID) == nil {
		now := time.Now().UTC()
		s.groups = append([]ruleGroup{{ID: allNodesGroupID, Name: "默认 · 全部节点", Enabled: true, CreatedAt: now, UpdatedAt: now}}, s.groups...)
		ensuredDefault = true
	}

	// Forward-compat: backfill GroupKey onto already-seeded built-ins so the
	// cross-rule correlation works on existing installs (the seed only runs on
	// first boot, so prod's pre-existing rules would otherwise have none).
	healed := false
	gkByID := map[string]string{
		"node-unreachable": "node-health", "bird-unreachable": "node-health", "bgp-peer-down": "node-health",
		"probe-down": "reachability", "probe-slow": "reachability", "probe-latency-anomaly": "reachability", "sla-loss": "reachability",
	}
	for i := range s.rules {
		if gk, ok := gkByID[s.rules[i].ID]; ok && s.rules[i].Builtin && s.rules[i].GroupKey == "" {
			s.rules[i].GroupKey = gk
			healed = true
		}
	}

	// Persist when we seeded, added the default group, healed group keys, or need
	// to migrate file/seed into an empty DB. persistLocked dual-writes (file + DB).
	if seeded || ensuredDefault || healed || (globalDB != nil && !loadedFromDB) {
		s.mu.Lock()
		err := s.persistLocked()
		s.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("persist alert rules: %w", err)
		}
	}
	return s, nil
}

// alertRulesLoadDB returns the stored document, or (nil, nil) when the DB has
// no row yet (first DB-enabled boot → caller migrates the file/seed in).
func alertRulesLoadDB() (*alertRulesFile, error) {
	var doc []byte
	err := globalDB.QueryRow(`SELECT doc FROM alert_rules WHERE id=$1`, alertRulesDocID).Scan(&doc)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f alertRulesFile
	if err := json.Unmarshal(doc, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// persistLocked dual-writes during the Postgres transition: the JSON file is
// always written (durable backup + the globalDB==nil path), then the same
// document is upserted into Postgres. A DB error is non-fatal — the file is
// already saved, so a write never fails just because Postgres hiccupped.
func (s *alertRuleStore) persistLocked() error {
	b, err := json.MarshalIndent(alertRulesFile{Groups: s.groups, Rules: s.rules}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if globalDB != nil {
		if _, err := globalDB.Exec(`INSERT INTO alert_rules (id, doc, updated_at)
			VALUES ($1, $2, now()) ON CONFLICT (id) DO UPDATE SET doc = EXCLUDED.doc, updated_at = now()`,
			alertRulesDocID, b); err != nil {
			log.Printf("alertrules: db persist failed (%v) — file is current", err)
		}
	}
	return nil
}

func (s *alertRuleStore) group(id string) *ruleGroup {
	for i := range s.groups {
		if s.groups[i].ID == id {
			return &s.groups[i]
		}
	}
	return nil
}

// snapshot returns copies safe to hand to a handler / the engine.
func (s *alertRuleStore) snapshot() ([]ruleGroup, []ruleDef) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g := make([]ruleGroup, len(s.groups))
	copy(g, s.groups)
	r := make([]ruleDef, len(s.rules))
	copy(r, s.rules)
	return g, r
}

// validateRule checks a rule before persisting.
func validateRule(r ruleDef) error {
	if !validRuleID(r.ID) {
		return fmt.Errorf("rule id must match ^[a-z0-9][a-z0-9-]{1,40}$")
	}
	if r.Name == "" {
		return fmt.Errorf("name required")
	}
	if _, ok := metricExtractors[r.Metric]; !ok {
		return fmt.Errorf("unknown metric %q", r.Metric)
	}
	if !validAlertOp(r.Op) {
		return fmt.Errorf("invalid op %q", r.Op)
	}
	if r.AnomalySigma != 0 {
		if r.AnomalySigma < 1 || r.AnomalySigma > 10 {
			return fmt.Errorf("anomaly_sigma out of range 1..10")
		}
		if r.AnomalyWindow != 0 && (r.AnomalyWindow < 4 || r.AnomalyWindow > 5000) {
			return fmt.Errorf("anomaly_window out of range 4..5000")
		}
		if r.AnomalyMinDelta < 0 {
			return fmt.Errorf("anomaly_min_delta must be >= 0")
		}
		if r.Op != opGt && r.Op != opLt && r.Op != opNe {
			return fmt.Errorf("anomaly rules need op gt (high), lt (low), or ne (both)")
		}
	}
	if r.Severity != sevInfo && r.Severity != sevWarn && r.Severity != sevCritical {
		return fmt.Errorf("invalid severity %q", r.Severity)
	}
	if r.SustainSecs < 0 || r.SustainSecs > 3600 {
		return fmt.Errorf("sustain_secs out of range 0..3600")
	}
	if r.ResolveSecs < 0 || r.ResolveSecs > 3600 {
		return fmt.Errorf("resolve_secs out of range 0..3600")
	}
	if r.EscalateSecs < 0 || r.EscalateSecs > 86400 {
		return fmt.Errorf("escalate_secs out of range 0..86400")
	}
	if r.RepeatSecs < 0 || r.RepeatSecs > 86400 {
		return fmt.Errorf("repeat_secs out of range 0..86400")
	}
	return nil
}

// validateGroup checks group-level fields before persisting.
func validateGroup(g ruleGroup) error {
	switch g.MinSeverity {
	case "", sevInfo, sevWarn, sevCritical:
	default:
		return fmt.Errorf("invalid min_severity %q", g.MinSeverity)
	}
	if g.DefaultSustainSecs < 0 || g.DefaultSustainSecs > 3600 {
		return fmt.Errorf("default_sustain_secs out of range 0..3600")
	}
	return nil
}

// addRule / updateRule / removeRule / addGroup / updateGroup / removeGroup all
// persist under the lock and return the mutated copy. Builtin rules cannot be
// removed (only tuned/disabled).

func (s *alertRuleStore) addRule(r ruleDef) (ruleDef, error) {
	if err := validateRule(r); err != nil {
		return ruleDef{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.GroupID == "" {
		r.GroupID = allNodesGroupID
	}
	if s.group(r.GroupID) == nil {
		return ruleDef{}, fmt.Errorf("unknown group %q", r.GroupID)
	}
	for _, x := range s.rules {
		if x.ID == r.ID {
			return ruleDef{}, fmt.Errorf("rule %q already exists", r.ID)
		}
	}
	now := time.Now().UTC()
	r.Builtin = false // API-created rules are never builtin
	r.CreatedAt, r.UpdatedAt = now, now
	s.rules = append(s.rules, r)
	return r, s.persistLocked()
}

// rulefields patches a subset of an existing rule.
type rulePatch struct {
	Name        *string  `json:"name,omitempty"`
	Description *string  `json:"description,omitempty"`
	GroupID     *string  `json:"group_id,omitempty"`
	Metric      *string  `json:"metric,omitempty"`
	Op          *alertOp `json:"op,omitempty"`
	Threshold   *float64 `json:"threshold,omitempty"`
	AnomalySigma    *float64 `json:"anomaly_sigma,omitempty"`
	AnomalyWindow   *int     `json:"anomaly_window,omitempty"`
	AnomalyMinDelta *float64 `json:"anomaly_min_delta,omitempty"`
	SustainSecs  *int    `json:"sustain_secs,omitempty"`
	ResolveSecs  *int    `json:"resolve_secs,omitempty"`
	EscalateSecs *int    `json:"escalate_secs,omitempty"`
	RepeatSecs   *int    `json:"repeat_secs,omitempty"`
	Severity    *severity `json:"severity,omitempty"`
	Enabled     *bool    `json:"enabled,omitempty"`
	NotifyTG    *bool    `json:"notify_tg,omitempty"`
	MuteUntil   *int64   `json:"mute_until,omitempty"` // per-rule silence (unix); 0 = unmute
}

func (s *alertRuleStore) updateRule(id string, p rulePatch) (ruleDef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rules {
		if s.rules[i].ID != id {
			continue
		}
		r := s.rules[i]
		if p.Name != nil {
			r.Name = *p.Name
		}
		if p.Description != nil {
			r.Description = *p.Description
		}
		if p.GroupID != nil {
			if s.group(*p.GroupID) == nil {
				return ruleDef{}, fmt.Errorf("unknown group %q", *p.GroupID)
			}
			r.GroupID = *p.GroupID
		}
		// Metric/op/threshold are tunable on ALL rules (incl builtin — that's
		// the whole point: members re-threshold a noisy builtin).
		if p.Metric != nil {
			r.Metric = *p.Metric
		}
		if p.Op != nil {
			r.Op = *p.Op
		}
		if p.Threshold != nil {
			r.Threshold = *p.Threshold
		}
		if p.AnomalySigma != nil {
			r.AnomalySigma = *p.AnomalySigma
		}
		if p.AnomalyWindow != nil {
			r.AnomalyWindow = *p.AnomalyWindow
		}
		if p.AnomalyMinDelta != nil {
			r.AnomalyMinDelta = *p.AnomalyMinDelta
		}
		if p.SustainSecs != nil {
			r.SustainSecs = *p.SustainSecs
		}
		if p.ResolveSecs != nil {
			r.ResolveSecs = *p.ResolveSecs
		}
		if p.EscalateSecs != nil {
			r.EscalateSecs = *p.EscalateSecs
		}
		if p.RepeatSecs != nil {
			r.RepeatSecs = *p.RepeatSecs
		}
		if p.Severity != nil {
			r.Severity = *p.Severity
		}
		if p.Enabled != nil {
			r.Enabled = *p.Enabled
		}
		if p.NotifyTG != nil {
			r.NotifyTG = *p.NotifyTG
		}
		if p.MuteUntil != nil {
			r.MuteUntil = *p.MuteUntil
		}
		if err := validateRule(r); err != nil {
			return ruleDef{}, err
		}
		r.UpdatedAt = time.Now().UTC()
		s.rules[i] = r
		return r, s.persistLocked()
	}
	return ruleDef{}, fmt.Errorf("rule %q not found", id)
}

func (s *alertRuleStore) removeRule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rules {
		if s.rules[i].ID == id {
			if s.rules[i].Builtin {
				return fmt.Errorf("builtin rule %q cannot be deleted — disable it instead", id)
			}
			s.rules = append(s.rules[:i], s.rules[i+1:]...)
			return s.persistLocked()
		}
	}
	return fmt.Errorf("rule %q not found", id)
}

func (s *alertRuleStore) addGroup(g ruleGroup) (ruleGroup, error) {
	if !validRuleID(g.ID) {
		return ruleGroup{}, fmt.Errorf("group id must match ^[a-z0-9][a-z0-9-]{1,40}$")
	}
	if g.Name == "" {
		return ruleGroup{}, fmt.Errorf("name required")
	}
	if err := validateGroup(g); err != nil {
		return ruleGroup{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.group(g.ID) != nil {
		return ruleGroup{}, fmt.Errorf("group %q already exists", g.ID)
	}
	now := time.Now().UTC()
	g.CreatedAt, g.UpdatedAt = now, now
	s.groups = append(s.groups, g)
	return g, s.persistLocked()
}

type groupPatch struct {
	Name        *string   `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	Enabled     *bool     `json:"enabled,omitempty"`
	NodeIDs     *[]string `json:"node_ids,omitempty"`
	Regions     *[]int    `json:"regions,omitempty"`
	MuteUntil   *int64    `json:"mute_until,omitempty"`
	SuppressTG         *bool     `json:"suppress_tg,omitempty"`
	MinSeverity        *severity `json:"min_severity,omitempty"`
	DefaultSustainSecs *int      `json:"default_sustain_secs,omitempty"`
}

func (s *alertRuleStore) updateGroup(id string, p groupPatch) (ruleGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.groups {
		if s.groups[i].ID != id {
			continue
		}
		g := s.groups[i]
		if p.Name != nil {
			g.Name = *p.Name
		}
		if p.Description != nil {
			g.Description = *p.Description
		}
		if p.Enabled != nil {
			g.Enabled = *p.Enabled
		}
		if p.NodeIDs != nil {
			g.NodeIDs = *p.NodeIDs
		}
		if p.Regions != nil {
			g.Regions = *p.Regions
		}
		if p.MuteUntil != nil {
			g.MuteUntil = *p.MuteUntil
		}
		if p.SuppressTG != nil {
			g.SuppressTG = *p.SuppressTG
		}
		if p.MinSeverity != nil {
			g.MinSeverity = *p.MinSeverity
		}
		if p.DefaultSustainSecs != nil {
			g.DefaultSustainSecs = *p.DefaultSustainSecs
		}
		if err := validateGroup(g); err != nil {
			return ruleGroup{}, err
		}
		g.UpdatedAt = time.Now().UTC()
		s.groups[i] = g
		return g, s.persistLocked()
	}
	return ruleGroup{}, fmt.Errorf("group %q not found", id)
}

func (s *alertRuleStore) removeGroup(id string) error {
	if id == allNodesGroupID {
		return fmt.Errorf("the default group cannot be deleted")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if r.GroupID == id {
			return fmt.Errorf("group %q still has rules — move or delete them first", id)
		}
	}
	for i := range s.groups {
		if s.groups[i].ID == id {
			s.groups = append(s.groups[:i], s.groups[i+1:]...)
			return s.persistLocked()
		}
	}
	return fmt.Errorf("group %q not found", id)
}

// seedAlertRules maps the 11 historical builtinRules into data rules + a single
// all-nodes default group. The (metric, op, threshold) here MUST reproduce the
// old Evaluate logic exactly. SustainSecs=0 everywhere → fire on first matching
// tick, identical to before. node-unreachable/probe-down seed NotifyTG=false
// (the uptime tracker owns reachability now; replaces the old kumaOwnedAlert).
func seedAlertRules() ([]ruleGroup, []ruleDef) {
	now := time.Now().UTC()
	g := []ruleGroup{{ID: allNodesGroupID, Name: "默认 · 全部节点", Enabled: true, CreatedAt: now, UpdatedAt: now}}
	// sustain = seconds the condition must hold before firing (0 = first tick).
	// Tuned so transient blips don't page: BGP must be down a sustained 5 min,
	// CPU/load 2 min, etc. probe-down/node-unreachable already carry their own
	// consecutive-sample gate, so they keep sustain=0.
	mk := func(id, name, desc, metric string, op alertOp, thr float64, sev severity, tg bool, sustain int) ruleDef {
		return ruleDef{
			ID: id, GroupID: allNodesGroupID, Name: name, Description: desc, Builtin: true,
			Metric: metric, Op: op, Threshold: thr, Severity: sev, Enabled: true, NotifyTG: tg,
			SustainSecs: sustain,
			CreatedAt: now, UpdatedAt: now,
		}
	}
	r := []ruleDef{
		mk("cpu-high", "CPU 高负载", "1m CPU > 85% 持续 2 分钟", "cpu_pct", opGt, 85, sevWarn, true, 120),
		mk("cpu-saturated", "CPU 持续打满", "CPU ≥ 95% 持续 2 分钟", "cpu_pct", opGte, 95, sevCritical, true, 120),
		mk("agent-cert-expiring", "Agent 证书即将到期", "ncn-agent TLS cert 剩余 ≤ 30 天", "cert_days_left", opLte, 30, sevWarn, true, 0),
		mk("load-high", "Load average 高", "1m load > 2.0 持续 2 分钟", "load1", opGt, 2.0, sevWarn, true, 120),
		mk("mem-pressure", "内存压力", "已用内存 > 90% 持续 1 分钟", "mem_pct", opGt, 90, sevCritical, true, 60),
		mk("disk-pressure", "磁盘紧张", "根分区使用率 > 90% 持续 1 分钟", "disk_pct", opGt, 90, sevCritical, true, 60),
		mk("probe-down", "连通性探针失败", "任意目标连续失败 ≥3 次", "probe_fail_count", opGte, 1, sevCritical, false, 0),
		mk("probe-slow", "连通性延迟过高", "任意探针 RTT > 200ms 持续 2 分钟", "probe_slow_count", opGte, 1, sevWarn, true, 120),
		mk("bgp-peer-down", "BGP 邻居异常", "非健康 BGP 协议(忽略 passive)持续 5 分钟", "bgp_down_count", opGte, 1, sevCritical, true, 300),
		mk("bird-unreachable", "BIRD 不可达", "抓取成功但无版本+0协议", "bird_absent", opGte, 1, sevCritical, true, 60),
		mk("node-unreachable", "节点不可达", "scrape 连续失败 ≥3 次", "consec_fail", opGte, 3, sevCritical, false, 0),
		mk("link-saturation", "链路将饱和", "按 p95 趋势预计 30 天内达 90% 链路容量", "link_saturation_eta_days", opLt, 30, sevWarn, true, 0),
		mk("sla-loss", "SLA 丢包", "某 PoP 对 SLA 目标丢包 > 1% 持续 5 分钟", "sla_loss_pct", opGt, 1, sevWarn, true, 300),
		mk("config-drift", "配置漂移", "节点实际配置偏离声明基线", "config_drift", opGte, 1, sevWarn, true, 0),
	}
	// One seeded anomaly rule: probe RTT that is abnormally high vs THAT probe's
	// own learned baseline — catches a latency regression on a normally-fast link
	// without false-firing on a link (e.g. fra→tyo) that is legitimately high.
	lat := mk("probe-latency-anomaly", "探针延迟异常", "最大探针 RTT 偏离自身基线 ≥5σ(且 >30ms),持续 2 分钟", "probe_max_rtt_ms", opGt, 0, sevWarn, true, 120)
	lat.AnomalySigma, lat.AnomalyWindow, lat.AnomalyMinDelta = 5, 120, 30
	r = append(r, lat)
	// Correlate related rules so a shared root cause yields ONE coalesced card +
	// one RCA instead of a flood of separate cards (alerts.go cross-rule grouping).
	for i := range r {
		switch r[i].ID {
		case "node-unreachable", "bird-unreachable", "bgp-peer-down":
			r[i].GroupKey = "node-health"
		case "probe-down", "probe-slow", "probe-latency-anomaly", "sla-loss":
			r[i].GroupKey = "reachability"
		}
	}
	return g, r
}

// sortGroupsRules gives a stable display order (default group first).
func sortGroupsRules(g []ruleGroup, r []ruleDef) {
	sort.SliceStable(g, func(i, j int) bool {
		if g[i].ID == allNodesGroupID {
			return true
		}
		if g[j].ID == allNodesGroupID {
			return false
		}
		return g[i].ID < g[j].ID
	})
	sort.SliceStable(r, func(i, j int) bool {
		if r[i].GroupID != r[j].GroupID {
			return r[i].GroupID < r[j].GroupID
		}
		return r[i].ID < r[j].ID
	})
}
