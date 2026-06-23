// Pure/in-memory tests for the richer alert semantics (sustain + resolve
// debounce, auto-escalation, group-default inheritance, TG severity floor /
// suppress). They drive tickOnce against a hand-built fleet + a single compiled
// rule whose condition we flip between ticks. No disk, no real fleet scrape, and
// no real Telegram send (the notifier is never Start()ed, so NudgeCrit just
// buffers into its queue channel and is dropped).

package main

import (
	"testing"
	"time"
)

// testEngine builds an engine wired to a one-node fleet and a single compiled
// rule whose firing condition is read from *cond each tick.
func testEngine(rule alertRule, cond *bool) *alertEngine {
	f := &fleetScraper{
		nodes: []fleetNode{{ID: "n1", Region: 51}},
		cache: map[string]*fleetNodeStatus{"n1": {Node: fleetNode{ID: "n1", Region: 51}, OK: true}},
	}
	rule.Evaluate = func(*fleetNodeStatus) (bool, string) {
		if *cond {
			return true, "over threshold"
		}
		return false, ""
	}
	e := newAlertEngine(nil)
	e.setFleet(f)
	e.mu.Lock()
	e.rules = []alertRule{rule}
	e.mu.Unlock()
	return e
}

func (e *alertEngine) activeCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.active)
}

func (e *alertEngine) activeEvt() *alertEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range e.active {
		return ev
	}
	return nil
}

func TestSustainDebounce(t *testing.T) {
	cond := false
	// SustainSecs 300 → reqFire = ceil(300/30) = 10 consecutive matching ticks.
	e := testEngine(alertRule{ID: "r1", Title: "t", Severity: sevWarn, Enabled: true}, &cond)
	e.mu.Lock()
	e.rules[0].SustainSecs = 300
	e.mu.Unlock()

	cond = true
	for i := 0; i < 9; i++ {
		e.tickOnce()
		if e.activeCount() != 0 {
			t.Fatalf("fired after %d ticks; want ≥10", i+1)
		}
	}
	e.tickOnce() // 10th
	if e.activeCount() != 1 {
		t.Fatalf("not firing after 10 sustained ticks")
	}
}

func TestSustainResetsOnGap(t *testing.T) {
	cond := false
	e := testEngine(alertRule{ID: "r1", Title: "t", Severity: sevWarn, Enabled: true}, &cond)
	e.mu.Lock()
	e.rules[0].SustainSecs = 300 // req 10
	e.mu.Unlock()

	cond = true
	for i := 0; i < 5; i++ {
		e.tickOnce()
	}
	cond = false
	e.tickOnce() // one clear tick → pending reset
	cond = true
	for i := 0; i < 9; i++ {
		e.tickOnce()
		if e.activeCount() != 0 {
			t.Fatalf("fired too early after reset (tick %d)", i+1)
		}
	}
	e.tickOnce()
	if e.activeCount() != 1 {
		t.Fatalf("should fire after 10 fresh consecutive ticks")
	}
}

func TestResolveDebounce(t *testing.T) {
	cond := false
	// Fire instantly (sustain 0), but require 120s clear (reqResolve = 4) to resolve.
	e := testEngine(alertRule{ID: "r1", Title: "t", Severity: sevWarn, Enabled: true}, &cond)
	e.mu.Lock()
	e.rules[0].ResolveSecs = 120
	e.mu.Unlock()

	cond = true
	e.tickOnce()
	if e.activeCount() != 1 {
		t.Fatalf("should fire on first matching tick")
	}
	cond = false
	for i := 0; i < 3; i++ {
		e.tickOnce()
		if e.activeCount() != 1 {
			t.Fatalf("resolved too early during hold (tick %d); want held 4", i+1)
		}
	}
	e.tickOnce() // 4th clear tick → resolve
	if e.activeCount() != 0 {
		t.Fatalf("should resolve after 4 clear ticks")
	}
}

func TestResolveHoldCancelledByRecovery(t *testing.T) {
	cond := false
	e := testEngine(alertRule{ID: "r1", Title: "t", Severity: sevWarn, Enabled: true}, &cond)
	e.mu.Lock()
	e.rules[0].ResolveSecs = 120 // req 4
	e.mu.Unlock()

	cond = true
	e.tickOnce()
	cond = false
	e.tickOnce()
	e.tickOnce() // 2 clear ticks (not yet resolved)
	cond = true
	e.tickOnce() // condition back → clearing reset
	cond = false
	for i := 0; i < 3; i++ {
		e.tickOnce()
		if e.activeCount() != 1 {
			t.Fatalf("clearing not reset by recovery (tick %d)", i+1)
		}
	}
	e.tickOnce()
	if e.activeCount() != 0 {
		t.Fatalf("should resolve 4 ticks after the recovery")
	}
}

func TestAutoEscalate(t *testing.T) {
	cond := true
	e := testEngine(alertRule{ID: "r1", Title: "t", Severity: sevWarn, Enabled: true}, &cond)
	e.mu.Lock()
	e.rules[0].EscalateSecs = 60
	e.mu.Unlock()

	e.tickOnce() // fires as warn
	ev := e.activeEvt()
	if ev == nil || ev.Severity != sevWarn {
		t.Fatalf("want initial warn, got %v", ev)
	}
	// Backdate FiredAt so the next tick sees it as long-firing.
	e.mu.Lock()
	ev.FiredAt -= 100
	e.mu.Unlock()
	e.tickOnce()
	ev = e.activeEvt()
	if ev == nil || ev.Severity != sevCritical || !ev.escalated {
		t.Fatalf("want escalated crit, got sev=%v escalated=%v", ev.Severity, ev.escalated)
	}
}

func TestGroupDefaultSustainInheritance(t *testing.T) {
	store := &alertRuleStore{
		groups: []ruleGroup{{ID: "all", Name: "all", Enabled: true, DefaultSustainSecs: 120}},
		rules: []ruleDef{
			{ID: "inherit", GroupID: "all", Name: "i", Metric: "cpu_pct", Op: opGt, Threshold: 1, Severity: sevWarn, Enabled: true},
			{ID: "override", GroupID: "all", Name: "o", Metric: "cpu_pct", Op: opGt, Threshold: 1, Severity: sevWarn, Enabled: true, SustainSecs: 300},
		},
	}
	e := newAlertEngine(nil)
	e.setRuleStore(store) // calls reloadRules
	e.mu.RLock()
	defer e.mu.RUnlock()
	got := map[string]int{}
	for _, r := range e.rules {
		got[r.ID] = r.SustainSecs
	}
	if got["inherit"] != 120 {
		t.Fatalf("inherit sustain = %d, want 120 (group default)", got["inherit"])
	}
	if got["override"] != 300 {
		t.Fatalf("override sustain = %d, want 300 (rule wins)", got["override"])
	}
}

func TestMaybeFireTGFloorAndSuppress(t *testing.T) {
	// notifier is constructed but never Start()ed → NudgeCrit only buffers.
	n := newTGNotifier("123:abc", "999")
	if n == nil {
		t.Fatal("notifier nil")
	}
	mk := func(sev, floor severity, suppress bool) *alertEvent {
		return &alertEvent{Severity: sev, minSev: floor, notifyTG: true, suppressTG: suppress, tickCount: alertSustainTicks}
	}
	cases := []struct {
		name string
		ev   *alertEvent
		want bool // want a fire (tgFireSent)
	}{
		{"crit default floor fires", mk(sevCritical, "", false), true},
		{"warn default floor fires (事无巨细)", mk(sevWarn, "", false), true},
		{"info default floor fires (事无巨细)", mk(sevInfo, "", false), true},
		{"warn with warn floor fires", mk(sevWarn, sevWarn, false), true},
		{"crit suppressed blocked", mk(sevCritical, "", true), false},
		{"info with warn floor blocked", mk(sevInfo, sevWarn, false), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAlertEngine(nil)
			e.setNotifier(n)
			e.mu.Lock()
			e.maybeFireTG(c.ev, "k", time.Now())
			e.mu.Unlock()
			if c.ev.tgFireSent != c.want {
				t.Fatalf("tgFireSent = %v, want %v", c.ev.tgFireSent, c.want)
			}
		})
	}
}
