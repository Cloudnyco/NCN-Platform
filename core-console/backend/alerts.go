// Threshold-based alert engine.
//
// Evaluates a set of built-in rules every 30s. Maintains per-rule state
// (firing-since, last-eval), keeps a rolling history of resolved alerts.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Maximum samples retained in each event's trail. At a 30s tick this gives
// ~30 minutes of evolution before the oldest entries fall off. The trail
// is what makes the alert log "detailed": the operator can see how a
// metric drifted into the danger zone, not just the latest reading.
const alertTrailCap = 60

// Tunables for out-of-band (Telegram) notification rate-limiting. Apply to
// the TG ping path ONLY — the web UI alerts page + history + trail still
// fire on every tick, so the operator never loses visibility.
const (
	// Minimum number of consecutive ticks the alert must be firing before
	// we send a TG "fired" message. At a 30s tick interval, 2 = ~60s.
	// Filters single-tick transient blips (e.g. one slow ping that times
	// out then recovers). The web UI shows it immediately regardless.
	alertSustainTicks = 2

	// After a TG "fired" message is sent for a given (node, rule), don't
	// send another fire ping for the same key until this much time has
	// elapsed — even across multiple fire→resolve flap cycles. This gate runs
	// in maybeFireTG BEFORE the Agent triage dispatches, so a repeat/flapping
	// alert within the window neither re-posts NOR re-runs the Agent (token
	// save) — "report once per window", matching the op-failure dedup. Matched
	// "resolved" pings still go through, but only when the corresponding fire
	// actually got sent (tgPosted).
	alertTGCooldown = 30 * time.Minute

	// probe-down requires the probe's series tail to be this many
	// consecutive failures before firing. At the 15s scrape cadence,
	// 3 ≈ 45s of unbroken failure. Filters the single/intermittent
	// packet drops on flaky transit (pop-04 IPv6) that aren't outages.
	probeDownConsecutive = 3

	// node-unreachable requires this many consecutive failed scrapes before
	// firing. The fleet scraper already retries WITHIN a tick (scrapeMaxAttempts)
	// to swallow sub-second blips; this gate adds tick-level debounce on top,
	// so a node has to be genuinely unreachable for ≈3×15s = 45s before the
	// alert is born. One isolated failed scrape — a deploy restarting the
	// agent, a few seconds of packet loss on the tyo→PoP path — self-heals on
	// the next tick and never reaches the chat. (The fleetNodeStatus.ConsecFail
	// counter is maintained per node by scrapeOne.)
	nodeDownConsecutive = 3

	// alertStormThreshold: when one tick produces at least this many first-fires
	// across the fleet, collapse ALL of them into ONE "alert storm" card + one
	// Agent RCA instead of many per-group cards — the storm-denoise path.
	alertStormThreshold = 6
)

// probeSeriesAllFail reports whether the last n samples of a probe's RTT
// series are ALL failures (V < 0, the sentinel pushed on a dropped
// probe). Returns false if there's less than n samples of history — we
// don't fire on a probe that hasn't accumulated enough readings to
// confirm a sustained outage (worst case: a brand-new probe stays quiet
// for its first ~45s even if down, which is acceptable).
func probeSeriesAllFail(series []tsSample, n int) bool {
	if len(series) < n {
		return false
	}
	for i := len(series) - n; i < len(series); i++ {
		if series[i].V >= 0 {
			return false // a success in the window → not a sustained outage
		}
	}
	return true
}

type alertSample struct {
	At      int64  `json:"at"`
	Message string `json:"message"`
}

type severity string

const (
	sevInfo     severity = "info"
	sevWarn     severity = "warn"
	sevCritical severity = "crit"
)

type alertRule struct {
	ID          string
	Title       string
	Description string
	Threshold   string   // human-readable threshold expression for UI display
	Severity    severity
	// Evaluator runs against ONE node's fleet-scraped status (covers local
	// ctrl-01 and the SSH-scraped pop-04 / pop-05 uniformly). Returns
	// (firing, message). When firing=false, the per-node alert resolves.
	Evaluate func(*fleetNodeStatus) (bool, string)

	// Below: compiled from the data rule + its group (see reloadRules).
	// Enabled folds in the group's enabled flag; NotifyTG gates the Telegram
	// nudge per-rule; SustainSecs delays firing; MuteUntil silences the rule's
	// group until that unix time; scope decides which nodes the rule covers.
	Enabled     bool
	NotifyTG    bool
	SustainSecs int // effective: rule's own, or the group default if rule's is 0
	MuteUntil   int64
	scope       func(nodeID string, region int) bool

	// Richer debounce / escalation / repeat (compiled from the data rule), plus
	// the group's TG policy. All zero-value = historical behaviour.
	ResolveSecs  int      // clear this long before resolving (0 = instant)
	EscalateSecs int      // bump severity → crit after firing this long (0 = off)
	RepeatSecs   int      // re-send TG every this-many secs while firing (0 = once)
	SuppressTG   bool     // group-level TG kill switch
	MinSev       severity // group TG severity floor ("" = global default = crit)
	GroupKey     string   // cross-rule correlation key — fires sharing it coalesce into one RCA card
}

// applicable reports whether this rule should evaluate on the given node right
// now (enabled, in scope, not muted). When false the engine treats it as
// not-firing, so any existing active alert for the (node,rule) resolves
// cleanly instead of hanging.
func (r alertRule) applicable(nodeID string, region int, now int64) bool {
	if !r.Enabled {
		return false
	}
	if r.MuteUntil > now {
		return false
	}
	if r.scope != nil && !r.scope(nodeID, region) {
		return false
	}
	return true
}

type alertEvent struct {
	ID          string        `json:"id"`
	NodeID      string        `json:"node_id"`     // which PoP this alert fired on
	RuleID      string        `json:"rule_id"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Threshold   string        `json:"threshold,omitempty"`
	Severity    severity      `json:"severity"`
	Message     string        `json:"message"`     // latest sample (kept for backwards compat)
	Trail       []alertSample `json:"trail,omitempty"` // chronological evolution
	FiredAt     int64         `json:"fired_at"`
	ResolvedAt  int64         `json:"resolved_at,omitempty"`
	State       string        `json:"state"`        // "firing" | "resolved"
	Acked       bool          `json:"acked,omitempty"`    // operator acknowledged → silence repeat/escalation
	AckedBy     string        `json:"acked_by,omitempty"`
	updated     time.Time
	// TG notification state — NOT serialized; tracked in memory only.
	// tickCount counts consecutive firing ticks; tgFireSent records whether
	// a "fired" TG ping has been emitted for this incident (drives whether
	// a matching "resolved" ping should be sent).
	tickCount   int
	tgFireSent  bool
	// tgPosted: a fire message was ACTUALLY pushed to TG for this incident.
	// Distinct from tgFireSent (which is set the moment we claim the incident,
	// before the async crit triage runs) — a crit the Agent judged EXPECTED
	// sets tgFireSent but NOT tgPosted, so it gets no repeat and no lone
	// "resolved". The resolved + repeat paths gate on tgPosted.
	tgPosted bool
	// notifyTG carries the firing rule's per-rule TG toggle so maybeFireTG can
	// gate without looking the rule back up. NOT serialized. The rest mirror the
	// compiled rule's escalation / repeat / group-TG-policy so maybeFireTG and
	// the escalation check work off the event alone.
	notifyTG     bool
	suppressTG   bool
	minSev       severity
	repeatSecs   int
	escalateSecs int
	escalated    bool   // set once after a warn→crit auto-escalation (one-shot)
	groupKey     string // cross-rule correlation key (carried from the compiled rule)
}

type alertEngine struct {
	mu      sync.RWMutex
	monitor   *Monitor        // kept for backward compat (legacy access points)
	fleet     *fleetScraper   // SOURCE OF TRUTH — covers all PoPs uniformly
	notify    *tgNotifier     // optional Telegram notifier — nil = disabled
	ruleStore *alertRuleStore // persistent, user-editable rule definitions
	rules     []alertRule     // compiled from ruleStore by reloadRules()
	// pending[nodeID:ruleID] = consecutive matching ticks, for SustainSecs
	// (a rule only becomes active once it has matched long enough). 0 secs =
	// one tick = fire immediately (the historical behaviour).
	pending map[string]int
	// clearing[nodeID:ruleID] = consecutive NON-matching ticks while an alert is
	// active, for ResolveSecs (resolve-debounce). An alert stays "firing" until
	// it has been clear long enough, so a brief recovery doesn't flap it closed.
	clearing map[string]int
	// active[nodeID + ":" + ruleID] = currently firing event for THAT node.
	// Same rule can fire concurrently on multiple PoPs without clobbering.
	active map[string]*alertEvent
	// history (resolved or recent active) — ring of last N events across
	// all nodes combined.
	history []alertEvent
	histCap int
	// historyDirty: the ring changed since the last DB flush. The runLoop
	// persists the ring (alert_history JSONB doc) when set, so history survives
	// restarts — loaded back in newAlertEngine.
	historyDirty bool

	// lastTGFire[key] = wall-clock time of the most recent TG "fired" send
	// for that (node, rule). Drives the cooldown logic: subsequent fires
	// within alertTGCooldown are throttled at the notify path (the event
	// still goes to history + trail + web UI).
	lastTGFire map[string]time.Time

	// Anomaly-detection state (alertanomaly.go). baselines[nodeID|metric|window]
	// is the rolling EWMA for one series; anomalySeries lists the distinct
	// (metric, window) pairs any anomaly rule needs, so tickOnce can feed them
	// once per node per tick. Both touched only under mu inside tickOnce/reload.
	baselines     map[string]*ewmaStat
	anomalySeries []anomalySeriesKey

	lastSeq int64
}

// anomalySeriesKey is one (metric, window) the engine must keep a baseline for,
// with the resolved extractor cached.
type anomalySeriesKey struct {
	metric string
	window int
	ext    metricExtractor
}

func newAlertEngine(m *Monitor) *alertEngine {
	e := &alertEngine{
		monitor:    m,
		active:     map[string]*alertEvent{},
		histCap:    200,
		lastTGFire: map[string]time.Time{},
		pending:    map[string]int{},
		clearing:   map[string]int{},
		baselines:  map[string]*ewmaStat{},
	}
	// Restore history from Postgres so the Alerts page's history survives
	// restarts (the ring used to be ephemeral). Best-effort; empty on miss.
	if globalDB != nil {
		if doc, err := loadConfigDoc("alert_history"); err != nil {
			log.Printf("alerts: history db load failed (%v) — starting empty", err)
		} else if doc != nil {
			var h []alertEvent
			if json.Unmarshal(doc, &h) == nil {
				if len(h) > e.histCap {
					h = h[len(h)-e.histCap:]
				}
				e.history = h
			}
		}
	}
	return e
}

// flushHistory persists the history ring to Postgres when it has changed.
// Called from runLoop after each tick; snapshots under the lock, writes outside.
func (e *alertEngine) flushHistory() {
	if globalDB == nil {
		return
	}
	e.mu.Lock()
	if !e.historyDirty {
		e.mu.Unlock()
		return
	}
	snap := make([]alertEvent, len(e.history))
	copy(snap, e.history)
	e.historyDirty = false
	e.mu.Unlock()
	b, err := json.Marshal(snap)
	if err != nil {
		return
	}
	if err := saveConfigDoc("alert_history", b); err != nil {
		log.Printf("alerts: history db flush failed (%v)", err)
		e.mu.Lock()
		e.historyDirty = true // retry next tick
		e.mu.Unlock()
	}
}

// setRuleStore wires the persistent rule store and compiles the first rule
// set. Called from main.go once newAlertRuleStore() has run.
func (e *alertEngine) setRuleStore(s *alertRuleStore) {
	e.mu.Lock()
	e.ruleStore = s
	e.mu.Unlock()
	e.reloadRules()
}

// reloadRules recompiles e.rules from the store: each enabled data rule + its
// group becomes an alertRule with a generated Evaluate closure and a scope
// matcher. Atomic swap under the lock. Called after every API mutation (like
// fleet.ReloadAgentKeys) so edits take effect without a restart.
func (e *alertEngine) reloadRules() {
	e.mu.RLock()
	store := e.ruleStore
	e.mu.RUnlock()
	if store == nil {
		return
	}
	groups, defs := store.snapshot()
	gmap := map[string]ruleGroup{}
	for _, g := range groups {
		gmap[g.ID] = g
	}
	compiled := make([]alertRule, 0, len(defs))
	seriesSeen := map[string]anomalySeriesKey{}
	for _, d := range defs {
		g, ok := gmap[d.GroupID]
		if !ok {
			continue
		}
		ext, ok := metricExtractors[d.Metric]
		if !ok {
			continue // unknown metric (shouldn't happen — validated on write)
		}
		rd := d // capture
		grp := g
		sym := alertOpSymbol[rd.Op]

		// Anomaly rule: build a baseline-relative evaluator instead of the
		// fixed-threshold one, and register its (metric, window) series so
		// tickOnce keeps the EWMA fed.
		if rd.AnomalySigma > 0 && rd.Enabled && grp.Enabled {
			spec := anomalySpec{sigma: rd.AnomalySigma, window: rd.AnomalyWindow, minDelta: rd.AnomalyMinDelta, dir: rd.Op}
			if spec.window < 4 {
				spec.window = 120 // ~1h at 30s ticks
			}
			skey := fmt.Sprintf("%s|%d", rd.Metric, spec.window)
			seriesSeen[skey] = anomalySeriesKey{metric: rd.Metric, window: spec.window, ext: ext}
			metric := rd.Metric
			sustain := rd.SustainSecs
			if sustain == 0 {
				sustain = grp.DefaultSustainSecs
			}
			compiled = append(compiled, alertRule{
				ID:    rd.ID,
				Title: rd.Name,
				Description: rd.Description,
				Threshold:   fmt.Sprintf("%s 偏离基线 %s%.1fσ", rd.Metric, sym, spec.sigma),
				Severity:    rd.Severity,
				Enabled:     true,
				NotifyTG:    rd.NotifyTG,
				SustainSecs: sustain,
				ResolveSecs:  rd.ResolveSecs,
				EscalateSecs: rd.EscalateSecs,
				RepeatSecs:   rd.RepeatSecs,
				SuppressTG:   grp.SuppressTG,
				MinSev:       grp.MinSeverity,
				GroupKey:     rd.GroupKey,
				MuteUntil:   maxInt64(grp.MuteUntil, rd.MuteUntil),
				scope:       func(nodeID string, region int) bool { return grp.matches(nodeID, region) },
				Evaluate: func(s *fleetNodeStatus) (bool, string) {
					v, ok := ext(s)
					if !ok {
						return false, ""
					}
					bkey := s.Node.ID + "|" + metric + "|" + strconv.Itoa(spec.window)
					mean, sd, ready := e.baselines[bkey].stats(spec.minSamples())
					if !ready || !spec.judge(v, mean, sd) {
						return false, ""
					}
					zs := "∞"
					if sd > 1e-9 {
						zs = fmt.Sprintf("%.1fσ", (v-mean)/sd)
					}
					return true, fmt.Sprintf("%s=%s 异常 (基线 %s±%s, z=%s)",
						metric, fmtNum(v, 2), fmtNum(mean, 2), fmtNum(sd, 2), zs)
				},
			})
			continue
		}
		// Effective sustain: the rule's own, else inherit the group default.
		sustain := rd.SustainSecs
		if sustain == 0 {
			sustain = grp.DefaultSustainSecs
		}
		compiled = append(compiled, alertRule{
			ID:          rd.ID,
			Title:       rd.Name,
			Description: rd.Description,
			Threshold:   fmt.Sprintf("%s %s %s", rd.Metric, sym, fmtNum(rd.Threshold, 2)),
			Severity:    rd.Severity,
			Enabled:     rd.Enabled && grp.Enabled,
			NotifyTG:    rd.NotifyTG,
			SustainSecs: sustain,
			ResolveSecs:  rd.ResolveSecs,
			EscalateSecs: rd.EscalateSecs,
			RepeatSecs:   rd.RepeatSecs,
			SuppressTG:   grp.SuppressTG,
			MinSev:       grp.MinSeverity,
			MuteUntil:   maxInt64(grp.MuteUntil, rd.MuteUntil), // effective mute = max(group, rule)
			scope:       func(nodeID string, region int) bool { return grp.matches(nodeID, region) },
			Evaluate: func(s *fleetNodeStatus) (bool, string) {
				v, ok := ext(s)
				if !ok || !cmpMetric(v, rd.Op, rd.Threshold) {
					return false, ""
				}
				msg := fmt.Sprintf("%s=%s (thr %s %s)", rd.Metric, fmtNum(v, 2), sym, fmtNum(rd.Threshold, 2))
				if det := metricDetail[rd.Metric]; det != nil {
					if extra := det(s); extra != "" {
						msg += " · " + extra
					}
				}
				return true, msg
			},
		})
	}
	series := make([]anomalySeriesKey, 0, len(seriesSeen))
	for _, sk := range seriesSeen {
		series = append(series, sk)
	}
	e.mu.Lock()
	e.rules = compiled
	e.anomalySeries = series
	// Drop baselines no longer referenced by any rule (keeps the map bounded
	// as rules come and go); surviving series keep their warmed-up history.
	if len(series) == 0 {
		e.baselines = map[string]*ewmaStat{}
	}
	e.mu.Unlock()
}

// setFleet wires the fleet scraper after both have been constructed.
// Called from main.go once newFleetScraper() has run. Until set, the
// engine evaluates nothing — see tickOnce's nil-check.
func (e *alertEngine) setFleet(f *fleetScraper) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fleet = f
}

// setNotifier wires an optional Telegram (or other) notifier. Pass nil to
// disable. Engine continues to track active+history as normal; setting a
// notifier only adds an out-of-band send on state transitions.
func (e *alertEngine) setNotifier(n *tgNotifier) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notify = n
}

func fmtPct(v float64) string { return fmtNum(v, 1) + "%" }
func fmtNum(v float64, dp int) string {
	if dp <= 0 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', dp, 64)
}

// evalRuleSafe runs one rule's evaluator with a panic guard. A nil-deref or
// index panic inside any single Evaluate() used to unwind tickOnce and kill
// the whole runLoop goroutine — silently stopping ALL alerting until a
// restart (a prime suspect for "出错误了没发信息"). Now a bad rule on a bad
// node just yields "not firing" + a log line; every other rule still runs.
func evalRuleSafe(r alertRule, n *fleetNodeStatus) (firing bool, msg string) {
	defer func() {
		if rec := recover(); rec != nil {
			firing, msg = false, ""
			node := "?"
			if n != nil {
				node = n.Node.ID
			}
			log.Printf("alerts: rule %q panicked on node %s: %v", r.ID, node, rec)
		}
	}()
	return r.Evaluate(n)
}

func (e *alertEngine) runLoop(ctx context.Context) {
	// Last-resort guard: if tickOnce ever panics despite evalRuleSafe, don't
	// let the alert engine die permanently — log, pause, and restart the loop.
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("alerts: runLoop panic recovered, restarting in 5s: %v", rec)
			time.Sleep(5 * time.Second)
			go e.runLoop(ctx)
		}
	}()
	// Initial pass once collectors have data
	time.Sleep(10 * time.Second)
	e.tickOnce()
	e.flushHistory()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			e.flushHistory() // best-effort persist on shutdown
			return
		case <-t.C:
			e.tickOnce()
			e.flushHistory()
		}
	}
}

func (e *alertEngine) tickOnce() {
	e.mu.Lock()
	fleet := e.fleet
	e.mu.Unlock()
	if fleet == nil {
		return // fleet not wired yet (very early in startup)
	}
	nodes := fleet.snapshotNodes()
	if len(nodes) == 0 {
		return
	}

	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()

	nowU := now.Unix()
	var fires []pendingFire // first-fires this tick, dispatched grouped-by-rule after the loop
	for _, n := range nodes {
		if n == nil {
			continue // node hasn't been scraped yet at all
		}
		// Feed each anomaly series' EWMA baseline for this node BEFORE evaluating
		// (the single new sample barely shifts a slow EWMA, so the evaluator below
		// sees an essentially current-but-unpolluted baseline).
		for _, sk := range e.anomalySeries {
			v, ok := sk.ext(n)
			if !ok {
				continue
			}
			bkey := n.Node.ID + "|" + sk.metric + "|" + strconv.Itoa(sk.window)
			st := e.baselines[bkey]
			if st == nil {
				st = newEWMA(sk.window)
				e.baselines[bkey] = st
			}
			st.observe(v)
		}
		for _, r := range e.rules {
			key := n.Node.ID + ":" + r.ID
			existing := e.active[key]

			// A rule that's disabled / muted / out-of-scope evaluates as
			// not-firing, so any existing active alert resolves cleanly
			// (no hanging alerts when a group is muted or rescoped).
			cond, msg := false, ""
			if r.applicable(n.Node.ID, n.Node.Region, nowU) {
				cond, msg = evalRuleSafe(r, n)
			}
			// Two-sided debounce, in 30s ticks. reqFire (SustainSecs): a fresh
			// rule must match this many consecutive ticks before activating.
			// reqResolve (ResolveSecs): an active alert must be CLEAR this many
			// consecutive ticks before resolving. *=1 (secs 0) = act on the
			// first tick = historical behaviour.
			reqFire := 1
			if r.SustainSecs > 0 {
				reqFire = (r.SustainSecs + 29) / 30
			}
			reqResolve := 1
			if r.ResolveSecs > 0 {
				reqResolve = (r.ResolveSecs + 29) / 30
			}

			var firing bool
			if existing != nil {
				// Already active: hold it firing through brief recoveries until
				// the condition has been clear for reqResolve consecutive ticks.
				if cond {
					firing = true
					delete(e.clearing, key)
				} else {
					e.clearing[key]++
					firing = e.clearing[key] < reqResolve
				}
				delete(e.pending, key) // unused while active
			} else {
				// Not active: arm only after reqFire consecutive matches.
				if cond {
					if e.pending[key] < reqFire {
						e.pending[key]++
					}
				} else {
					delete(e.pending, key)
				}
				firing = cond && e.pending[key] >= reqFire
			}

			switch {
			case firing && existing == nil:
				// New firing — seed the trail. CRIT nudge defers to the
				// next tick (sustain) so transient blips never reach the
				// chat. Web UI surfaces the alert immediately regardless.
				e.lastSeq++
				ev := &alertEvent{
					ID:          strconv.FormatInt(e.lastSeq, 10),
					NodeID:      n.Node.ID,
					RuleID:      r.ID,
					Title:       r.Title,
					Description: r.Description,
					Threshold:   r.Threshold,
					Severity:    r.Severity,
					Message:     msg,
					Trail:       []alertSample{{At: now.Unix(), Message: msg}},
					FiredAt:     now.Unix(),
					State:       "firing",
					updated:     now,
					tickCount:   1,
					notifyTG:    r.NotifyTG,
					suppressTG:   r.SuppressTG,
					minSev:       r.MinSev,
					repeatSecs:   r.RepeatSecs,
					escalateSecs: r.EscalateSecs,
					groupKey:     r.GroupKey,
				}
				e.active[key] = ev
				e.pushHistory(*ev)
				webhookNotify(*ev, "fired") // generic non-TG channel (NCN_ALERT_WEBHOOK)
				e.maybeFireTG(ev, key, now)

			case firing && existing != nil:
				// Continuing. Only refresh message/trail when the condition is
				// actually matching THIS tick — during a resolve-hold cond is
				// false and msg empty, so we keep the last real sample.
				if cond {
					existing.Message = msg
					if len(existing.Trail) == 0 || existing.Trail[len(existing.Trail)-1].Message != msg {
						existing.Trail = append(existing.Trail, alertSample{At: now.Unix(), Message: msg})
						if len(existing.Trail) > alertTrailCap {
							existing.Trail = existing.Trail[len(existing.Trail)-alertTrailCap:]
						}
					}
				}
				existing.tickCount++
				// Auto-escalation: a sub-crit alert firing past its EscalateSecs
				// becomes crit and re-notifies, exactly once.
				if existing.escalateSecs > 0 && !existing.escalated && !existing.Acked &&
					sevWeight(existing.Severity) < sevWeight(sevCritical) &&
					now.Unix()-existing.FiredAt >= int64(existing.escalateSecs) {
					existing.Severity = sevCritical
					existing.escalated = true
					existing.Trail = append(existing.Trail, alertSample{At: now.Unix(),
						Message: fmt.Sprintf("↑ escalated to crit (firing %dm)", existing.escalateSecs/60)})
					if len(existing.Trail) > alertTrailCap {
						existing.Trail = existing.Trail[len(existing.Trail)-alertTrailCap:]
					}
					existing.tgFireSent = false // permit a fresh crit nudge
				}
				existing.updated = now
				e.updateHistoryByID(existing.ID, *existing)
				if pf := e.maybeFireTG(existing, key, now); pf != nil {
					fires = append(fires, *pf)
				}

			case !firing && existing != nil:
				// Resolved — close the trail. Push a matching silent
				// "✅ resolved" nudge ONLY if the corresponding fire
				// nudge was actually sent (otherwise the operator would
				// see a standalone close without an open).
				existing.ResolvedAt = now.Unix()
				existing.State = "resolved"
				existing.Trail = append(existing.Trail, alertSample{At: now.Unix(), Message: "✓ resolved"})
				if len(existing.Trail) > alertTrailCap {
					existing.Trail = existing.Trail[len(existing.Trail)-alertTrailCap:]
				}
				existing.updated = now
				e.updateHistoryByID(existing.ID, *existing)
				webhookNotify(*existing, "resolved") // generic non-TG channel
				if e.notify != nil && existing.tgPosted {
					evCopy := *existing
					go e.notify.NudgeCrit(evCopy, "resolved")
				}
				delete(e.active, key)
				delete(e.pending, key)
				delete(e.clearing, key)
			}
		}
	}
	e.flushFires(fires) // dispatch this tick's first-fires, coalesced by rule
}

// maybeFireTG decides whether to send the alert card to Telegram (the error
// channel) for the given event right now. The card is the full formatTGAlert
// (severity · node · title, the subject line naming which peer/tunnel/probe,
// + a foldable threshold/fired/rule block). Four gates:
//
//   1. NotifyTG: the firing rule's per-rule Telegram toggle. Off = silent on
//      TG (still in the engine / web UI / /alerts). This is how the reachability
//      rules (node-unreachable, probe-down — now owned by the uptime tracker)
//      and any rule a member chooses to quiet stay out of the chat.
//   2. Severity: default floor is sevInfo → info/warn/crit all post (事无巨细).
//      A group's MinSeverity can raise the floor for its own rules.
//   3. Sustain: ev.tickCount must have reached alertSustainTicks.
//   4. Cooldown: alertTGCooldown since the LAST send for this (node, rule).
//
// Caller must hold e.mu. Returns a non-nil *pendingFire for a FIRST fire that
// passed every gate — the caller (tickOnce) collects these across the whole
// tick and dispatches them grouped-by-rule, so a fleet-wide event becomes ONE
// coalesced card + ONE Agent triage instead of N. Repeat (RepeatSecs) re-pings
// are dispatched here, per-node, and return nil.
func (e *alertEngine) maybeFireTG(ev *alertEvent, key string, now time.Time) *pendingFire {
	if e.notify == nil {
		return nil
	}
	// Per-rule toggle + group-level kill switch.
	if !ev.notifyTG || ev.suppressTG {
		return nil
	}
	// Severity floor: group MinSeverity if set, else the global default. Default
	// is now sevInfo — the error channel reports EVERYTHING (info/warn/crit),
	// 事无巨细. Per-rule NotifyTG=false and group suppress_tg/min_severity remain
	// the escape hatches for anything intentionally kept quiet.
	floor := ev.minSev
	if floor == "" {
		floor = sevInfo
	}
	if sevWeight(ev.Severity) < sevWeight(floor) {
		return nil
	}
	if ev.tickCount < alertSustainTicks {
		return nil
	}
	// Inhibition: a node-down / bird-down root cause suppresses its dependents
	// on the same node (no TG storm for one root cause; still in engine/web).
	if e.inhibitedLocked(ev.NodeID, ev.RuleID) {
		return nil
	}
	if !ev.tgFireSent {
		// First nudge for this incident — honour the flap cooldown.
		if last, ok := e.lastTGFire[key]; ok && now.Sub(last) < alertTGCooldown {
			return nil
		}
		ev.tgFireSent = true
		e.lastTGFire[key] = now
		return &pendingFire{ev: *ev, key: key}
	}
	// Already nudged — re-send periodically while still firing if the rule asks
	// for it (RepeatSecs). Floored at 60s so it can't be set absurdly chatty.
	// Never repeat what we never posted (a crit the Agent judged EXPECTED), and
	// stay quiet once an operator acknowledged it.
	if !ev.tgPosted || ev.repeatSecs <= 0 || ev.Acked {
		return nil
	}
	iv := time.Duration(ev.repeatSecs) * time.Second
	if iv < time.Minute {
		iv = time.Minute
	}
	if last, ok := e.lastTGFire[key]; ok && now.Sub(last) < iv {
		return nil
	}
	e.lastTGFire[key] = now
	go e.notify.NudgeCrit(*ev, "fired")
	return nil
}

// pendingFire is a first-fire event captured during a tick, awaiting dispatch
// (possibly coalesced with same-rule fires on other nodes).
type pendingFire struct {
	ev  alertEvent
	key string
}

// flushFires dispatches the tick's first-fires grouped by rule: a lone fire
// goes through the per-node path; ≥2 nodes on the SAME rule coalesce into one
// card + one Agent triage. Called by tickOnce while holding e.mu.
func (e *alertEngine) flushFires(fires []pendingFire) {
	if len(fires) == 0 {
		return
	}
	// Storm denoise: a flood of distinct first-fires in one tick → ONE storm
	// card + one RCA covering them all (no per-group spray).
	if len(fires) >= alertStormThreshold {
		e.dispatchGroup(fires, fmt.Sprintf("告警风暴 · %d 条同时触发", len(fires)))
		return
	}
	// Otherwise coalesce by correlation key: a rule's GroupKey when set (so
	// DIFFERENT rules describing one root cause merge), else the rule ID (the
	// historical same-rule-multi-node coalescing).
	byKey := map[string][]pendingFire{}
	order := []string{}
	for _, pf := range fires {
		k := pf.ev.groupKey
		if k == "" {
			k = "rule:" + pf.ev.RuleID
		}
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], pf)
	}
	for _, k := range order {
		g := byKey[k]
		if len(g) == 1 {
			e.dispatchFire(g[0])
		} else {
			e.dispatchGroup(g, groupTitle(g))
		}
	}
}

// groupTitle names a coalesced card: the shared rule title when all fires are
// the same rule, else a correlation label built from the shared GroupKey.
func groupTitle(g []pendingFire) string {
	rid := g[0].ev.RuleID
	same := true
	for _, pf := range g {
		if pf.ev.RuleID != rid {
			same = false
			break
		}
	}
	if same {
		return g[0].ev.Title
	}
	gk := g[0].ev.groupKey
	if gk == "" {
		gk = "相关告警"
	}
	return "关联告警 · " + gk
}

// dispatchFire sends a single-node first-fire (the common case). Under e.mu.
func (e *alertEngine) dispatchFire(pf pendingFire) {
	if pf.ev.Severity == sevCritical || pf.ev.Severity == sevWarn {
		go e.triageAlertFire(pf.ev, pf.key)
	} else {
		go e.notify.NudgeCrit(pf.ev, "fired")
		if cur := e.active[pf.key]; cur != nil {
			cur.tgPosted = true
		}
	}
}

// dispatchGroup coalesces ≥2 fires (same-rule, same-GroupKey, or a storm) under
// one card titled `title`. Severity = the max in the group. Under e.mu.
func (e *alertEngine) dispatchGroup(g []pendingFire, title string) {
	maxSev := sevInfo
	for _, pf := range g {
		if sevWeight(pf.ev.Severity) > sevWeight(maxSev) {
			maxSev = pf.ev.Severity
		}
	}
	if maxSev == sevCritical || maxSev == sevWarn {
		go e.triageAlertGroup(g, maxSev, title)
	} else {
		go e.notify.sendAlertGroupCard(title, maxSev, groupItems(g), "")
		for _, pf := range g {
			if cur := e.active[pf.key]; cur != nil {
				cur.tgPosted = true
			}
		}
	}
}

// markPosted flags the given incident keys tgPosted (so their resolves pair).
// Used by the async group/single triage after it actually posts.
func (e *alertEngine) markPosted(keys []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, k := range keys {
		if cur := e.active[k]; cur != nil {
			cur.tgPosted = true
		}
	}
}

// activeUnacked returns a copy of every currently-firing, un-acknowledged alert
// — the input to the on-call escalation loop (oncall.go).
func (e *alertEngine) activeUnacked() []alertEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []alertEvent
	for _, ev := range e.active {
		if ev != nil && ev.State == "firing" && !ev.Acked {
			out = append(out, *ev)
		}
	}
	return out
}

func groupItems(g []pendingFire) []alertGroupItem {
	out := make([]alertGroupItem, 0, len(g))
	for _, pf := range g {
		out = append(out, alertGroupItem{Node: pf.ev.NodeID, Msg: pf.ev.Message})
	}
	return out
}

// triageAlertGroup runs ONE read-only Agent triage covering all nodes a rule
// fired on this tick (REAL vs objectively-EXPECTED for the fleet), then posts a
// single coalesced card — instead of N triages + N cards. crit carries the
// diagnosis; warn is discern-only. EXPECTED → silent on TG.
func (e *alertEngine) triageAlertGroup(g []pendingFire, sev severity, title string) {
	if e == nil || e.notify == nil || len(g) == 0 {
		return
	}
	keys := make([]string, 0, len(g))
	var nodes []string
	for _, pf := range g {
		keys = append(keys, pf.key)
		nodes = append(nodes, pf.ev.NodeID)
	}
	rule := g[0].ev.RuleID
	post := func(diag string) {
		e.notify.sendAlertGroupCard(title, sev, groupItems(g), diag)
		e.markPosted(keys)
	}
	if e.notify.ai == nil || !e.notify.ai.enabled() {
		post("") // can't discern → report (fail safe)
		return
	}
	wantDiag := sev == sevCritical
	ask := "Reply with ONLY a FINAL line that is EXACTLY one of:"
	if wantDiag {
		ask = "Give a concise 1-2 sentence diagnosis (likely cause + most useful next step), then end with a FINAL line that is EXACTLY one of:"
	}
	var detail strings.Builder
	for _, pf := range g {
		fmt.Fprintf(&detail, "\n- %s: %s", pf.ev.NodeID, pf.ev.Message)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	prompt := fmt.Sprintf(
		"A %s-severity alert '%s' (rule %s) just fired on %d nodes at once:%s\n\n"+
			"Investigate with your READ-ONLY tools (node_detail, fleet_status, "+
			"active_alerts, op_failures). Judge OBJECTIVELY whether this is a REAL "+
			"problem warranting operator attention, or an EXPECTED/benign condition "+
			"normal for this network (e.g. long-haul RTT between far PoPs, a value "+
			"within baseline). Consider that it hit MULTIPLE nodes at once (often a "+
			"shared upstream / our-side change). %s\nVERDICT: REAL\nVERDICT: EXPECTED",
		sev, title, rule, len(g), detail.String(), ask)
	res, err := agentAdvance(ctx, []aiMsg{{Role: "user", Content: prompt}}, false, "alert-triage-group", nil, nil)
	if err != nil {
		post("") // fail safe
		return
	}
	if strings.Contains(strings.ToUpper(res.Final), "VERDICT: EXPECTED") {
		return // objectively benign — stay silent on TG (still in engine/web)
	}
	diag := ""
	if wantDiag {
		diag = stripVerdict(res.Final)
	}
	post(diag)
}

// triageAlertFire runs the ops Agent (READ-ONLY) on a freshly-fired warn/crit
// alert to discern whether it's a REAL problem or an objectively EXPECTED /
// benign condition (e.g. high RTT between far-apart PoPs like fra↔tyo is just
// physical distance). On EXPECTED it stays silent on TG (the alert still shows
// in the engine / web UI). On REAL — or AI unavailable / error / ambiguous
// verdict (fail safe) — it posts ONE card and marks the incident tgPosted.
// CRIT cards also carry the Agent's diagnosis; WARN cards are discern-only (no
// diagnosis text). Once per incident.
func (e *alertEngine) triageAlertFire(ev alertEvent, key string) {
	if e == nil || e.notify == nil {
		return
	}
	post := func(diag string) {
		e.notify.sendAlertCard(ev, diag)
		e.mu.Lock()
		if cur := e.active[key]; cur != nil {
			cur.tgPosted = true
		}
		e.mu.Unlock()
	}
	if e.notify.ai == nil || !e.notify.ai.enabled() {
		post("") // can't discern → report (fail safe)
		return
	}
	// Only crit gets a written diagnosis; warn is discern-only, so we don't even
	// ask the model to write one.
	wantDiag := ev.Severity == sevCritical
	ask := "Reply with ONLY a FINAL line that is EXACTLY one of:"
	if wantDiag {
		ask = "Give a concise 1-2 sentence diagnosis (likely cause + most useful next step), then end with a FINAL line that is EXACTLY one of:"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	prompt := fmt.Sprintf(
		"A %s-severity alert just fired on AS64500.\n"+
			"node=%s  rule=%s\ntitle: %s\ndetail: %s\n\n"+
			"Investigate with your READ-ONLY tools (node_detail, fleet_status, "+
			"active_alerts, op_failures). Judge OBJECTIVELY whether this is a REAL "+
			"problem that warrants operator attention, or an EXPECTED/benign "+
			"condition that is normal for this network and needs no action — e.g. "+
			"high RTT between far-apart PoPs (fra↔tyo etc.) is just physical "+
			"distance; a value within its normal baseline; one distant probe being "+
			"slow. %s\nVERDICT: REAL\nVERDICT: EXPECTED",
		ev.Severity, ev.NodeID, ev.RuleID, ev.Title, ev.Message, ask)
	res, err := agentAdvance(ctx, []aiMsg{{Role: "user", Content: prompt}}, false, "alert-triage", nil, nil)
	if err != nil {
		post("") // fail safe: report on error
		return
	}
	if strings.Contains(strings.ToUpper(res.Final), "VERDICT: EXPECTED") {
		return // objectively benign — stay silent on TG (still in engine/web)
	}
	diag := ""
	if wantDiag {
		diag = stripVerdict(res.Final)
	}
	post(diag)
}

// evictNode drops every active alert (and its TG-cooldown bookkeeping) for a
// node that has just been decommissioned or deleted. Without this the node's
// in-flight alerts (e.g. node-unreachable) would hang forever: tickOnce only
// processes nodes present in the fleet snapshot, so a removed node never
// reaches the resolve branch. Called by fleetScraper.applyDeactivateNode.
func (e *alertEngine) evictNode(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	prefix := id + ":"
	for key, ev := range e.active {
		if ev.NodeID == id || strings.HasPrefix(key, prefix) {
			delete(e.active, key)
			delete(e.lastTGFire, key)
		}
	}
}

func (e *alertEngine) pushHistory(ev alertEvent) {
	e.history = append(e.history, ev)
	if len(e.history) > e.histCap {
		e.history = e.history[len(e.history)-e.histCap:]
	}
	e.historyDirty = true
}

// activeSnapshot returns a shallow copy of the currently-firing events,
// optionally filtered by severity. Used by the TG bot's /alerts command.
// Safe for callers outside the engine — copies out of the lock.
func (e *alertEngine) activeSnapshot(sevFilter severity) []alertEvent {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]alertEvent, 0, len(e.active))
	for _, v := range e.active {
		if sevFilter != "" && v.Severity != sevFilter {
			continue
		}
		out = append(out, *v)
	}
	return out
}

func (e *alertEngine) updateHistoryByID(id string, ev alertEvent) {
	for i := range e.history {
		if e.history[i].ID == id {
			e.history[i] = ev
			e.historyDirty = true
			return
		}
	}
}

func (e *alertEngine) handleAlerts(w http.ResponseWriter, _ *http.Request) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	active := make([]alertEvent, 0, len(e.active))
	for _, v := range e.active {
		active = append(active, *v)
	}
	sort.Slice(active, func(i, j int) bool { return active[i].FiredAt > active[j].FiredAt })

	history := make([]alertEvent, len(e.history))
	copy(history, e.history)
	sort.Slice(history, func(i, j int) bool { return history[i].FiredAt > history[j].FiredAt })

	rules := make([]map[string]string, 0, len(e.rules))
	for _, r := range e.rules {
		rules = append(rules, map[string]string{
			"id":          r.ID,
			"title":       r.Title,
			"description": r.Description,
			"threshold":   r.Threshold,
			"severity":    string(r.Severity),
		})
	}

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"active":  active,
		"history": history,
		"rules":   rules,
	}})
}

// ── completeness pass (2026-06-22): inhibition, ack, webhook, per-rule mute ──

// maxInt64 returns the larger of two int64 (effective mute = max(group, rule)).
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// alertInhibits: when the KEY rule is active on a node, the listed rules on the
// SAME node are suppressed from TG — the root cause (node/bird down) makes its
// dependents (bgp/probe/resource) redundant noise. Mirrors Alertmanager
// inhibition. Dependents still show in the engine + web UI; only TG is muted.
var alertInhibits = map[string][]string{
	"node-unreachable": {"bgp-peer-down", "bird-unreachable", "probe-down", "probe-slow", "cpu-high", "cpu-saturated", "mem-pressure", "disk-pressure", "load-high"},
	"bird-unreachable": {"bgp-peer-down"},
}

// inhibitedLocked reports whether an active inhibitor on the same node should
// suppress this (node, rule) TG notification. Caller holds e.mu.
func (e *alertEngine) inhibitedLocked(nodeID, ruleID string) bool {
	for inhibitor, suppressed := range alertInhibits {
		if _, active := e.active[nodeID+":"+inhibitor]; !active {
			continue
		}
		for _, s := range suppressed {
			if s == ruleID {
				return true
			}
		}
	}
	return false
}

// webhookNotify POSTs an alert state change to NCN_ALERT_WEBHOOK (if set) — a
// generic non-Telegram channel (Slack/Discord/PagerDuty/custom). Async + best
// effort. kind = "fired" | "resolved".
func webhookNotify(ev alertEvent, kind string) {
	url := strings.TrimSpace(os.Getenv("NCN_ALERT_WEBHOOK"))
	if url == "" {
		return
	}
	go func() {
		body, _ := json.Marshal(map[string]any{
			"kind": kind, "node": ev.NodeID, "rule": ev.RuleID, "title": ev.Title,
			"severity": string(ev.Severity), "message": ev.Message,
			"fired_at": ev.FiredAt, "resolved_at": ev.ResolvedAt, "as": "AS64500",
		})
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("alert webhook: %v", err)
			return
		}
		resp.Body.Close()
	}()
}

// handleAlertAck — POST /api/v1/auth/alerts/ack {id} | {node_id,rule_id}. Marks
// the active alert acknowledged so maybeFireTG stops repeat re-pings + auto-
// escalation for it until it resolves. Admin-gated.
func (e *alertEngine) handleAlertAck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var req struct {
		ID     string `json:"id"`
		NodeID string `json:"node_id"`
		RuleID string `json:"rule_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	op := adminOperator(r)
	e.mu.Lock()
	defer e.mu.Unlock()
	var hit *alertEvent
	if req.NodeID != "" && req.RuleID != "" {
		hit = e.active[req.NodeID+":"+req.RuleID]
	} else {
		for _, ev := range e.active {
			if ev.ID == req.ID {
				hit = ev
				break
			}
		}
	}
	if hit == nil {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "no active alert with that id"})
		return
	}
	hit.Acked = true
	hit.AckedBy = op
	e.updateHistoryByID(hit.ID, *hit)
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"id": hit.ID, "acked_by": op}})
}
