// Admin API for the user-editable alert rules + groups.
//
// All routes are admin-gated (registered with auth.requireRole("admin", …) in
// main.go) and audited. Every mutation calls engine.reloadRules() so edits take
// effect on the next tick without a restart. Preview evaluates a candidate rule
// against the live fleet snapshot so an operator sees the blast radius before
// saving.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type alertRulesAPI struct {
	store  *alertRuleStore
	engine *alertEngine
	fleet  *fleetScraper
}

func newAlertRulesAPI(store *alertRuleStore, engine *alertEngine, fleet *fleetScraper) *alertRulesAPI {
	return &alertRulesAPI{store: store, engine: engine, fleet: fleet}
}

func (a *alertRulesAPI) decode(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(v)
}

// ── /api/v1/auth/alert-rules ───────────────────────────────────────────────

func (a *alertRulesAPI) handleRulesRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		groups, rules := a.store.snapshot()
		sortGroupsRules(groups, rules)
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"groups": groups, "rules": rules}})
	case http.MethodPost:
		op := adminOperator(r)
		var rd ruleDef
		if err := a.decode(r, &rd); err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
			return
		}
		rd.CreatedBy = op
		out, err := a.store.addRule(rd)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: err.Error()})
			return
		}
		a.engine.reloadRules()
		auditRecord(r, AuditEvent{Event: "alert-rule.create", Severity: auditSevWarn, Actor: op, Target: out.ID,
			Details: map[string]any{"metric": out.Metric, "op": out.Op, "threshold": out.Threshold, "group": out.GroupID}})
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
	}
}

func (a *alertRulesAPI) handleRulesItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/alert-rules/")
	if !validRuleID(id) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid rule id"})
		return
	}
	op := adminOperator(r)
	switch r.Method {
	case http.MethodPatch:
		var p rulePatch
		if err := a.decode(r, &p); err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
			return
		}
		out, err := a.store.updateRule(id, p)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: err.Error()})
			return
		}
		a.engine.reloadRules()
		auditRecord(r, AuditEvent{Event: "alert-rule.update", Severity: auditSevWarn, Actor: op, Target: id})
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
	case http.MethodDelete:
		if err := a.store.removeRule(id); err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: err.Error()})
			return
		}
		a.engine.reloadRules()
		auditRecord(r, AuditEvent{Event: "alert-rule.delete", Severity: auditSevWarn, Actor: op, Target: id})
		writeJSON(w, http.StatusOK, envelope{OK: true})
	default:
		w.Header().Set("Allow", "PATCH, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
	}
}

// ── /api/v1/auth/alert-preview ──────────────────────────────────────────────
// Evaluate an ad-hoc (metric, op, threshold[, scope]) against the live fleet
// and report which nodes would fire RIGHT NOW — the blast-radius guardrail.

type previewResult struct {
	NodeID string  `json:"node_id"`
	Value  float64 `json:"value"`
	OK     bool    `json:"ok"`
	Firing bool    `json:"firing"`
}

func (a *alertRulesAPI) handlePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
		return
	}
	var req struct {
		Metric    string   `json:"metric"`
		Op        alertOp  `json:"op"`
		Threshold float64  `json:"threshold"`
		NodeIDs   []string `json:"node_ids,omitempty"`
		Regions   []int    `json:"regions,omitempty"`
	}
	if err := a.decode(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	ext, ok := metricExtractors[req.Metric]
	if !ok {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "unknown metric"})
		return
	}
	if !validAlertOp(req.Op) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid op"})
		return
	}
	scope := ruleGroup{NodeIDs: req.NodeIDs, Regions: req.Regions}
	out := []previewResult{}
	firingCount := 0
	for _, n := range a.fleet.snapshotNodes() {
		if n == nil {
			continue
		}
		if !scope.matches(n.Node.ID, n.Node.Region) {
			continue
		}
		v, vok := ext(n)
		firing := vok && cmpMetric(v, req.Op, req.Threshold)
		if firing {
			firingCount++
		}
		out = append(out, previewResult{NodeID: n.Node.ID, Value: v, OK: vok, Firing: firing})
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"results": out, "firing_count": firingCount}})
}

// ── /api/v1/auth/alert-groups ───────────────────────────────────────────────

func (a *alertRulesAPI) handleGroupsRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		groups, _ := a.store.snapshot()
		sortGroupsRules(groups, nil)
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: groups})
	case http.MethodPost:
		op := adminOperator(r)
		var g ruleGroup
		if err := a.decode(r, &g); err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
			return
		}
		g.CreatedBy = op
		out, err := a.store.addGroup(g)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: err.Error()})
			return
		}
		a.engine.reloadRules()
		auditRecord(r, AuditEvent{Event: "alert-group.create", Severity: auditSevWarn, Actor: op, Target: out.ID})
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
	}
}

func (a *alertRulesAPI) handleGroupsItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/alert-groups/")
	if !validRuleID(id) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid group id"})
		return
	}
	op := adminOperator(r)
	switch r.Method {
	case http.MethodPatch:
		var p groupPatch
		if err := a.decode(r, &p); err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
			return
		}
		out, err := a.store.updateGroup(id, p)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: err.Error()})
			return
		}
		a.engine.reloadRules()
		auditRecord(r, AuditEvent{Event: "alert-group.update", Severity: auditSevWarn, Actor: op, Target: id})
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
	case http.MethodDelete:
		if err := a.store.removeGroup(id); err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: err.Error()})
			return
		}
		a.engine.reloadRules()
		auditRecord(r, AuditEvent{Event: "alert-group.delete", Severity: auditSevWarn, Actor: op, Target: id})
		writeJSON(w, http.StatusOK, envelope{OK: true})
	default:
		w.Header().Set("Allow", "PATCH, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
	}
}

// ── /api/v1/auth/alert-metrics ──────────────────────────────────────────────

func (a *alertRulesAPI) handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: metricCatalog})
}
