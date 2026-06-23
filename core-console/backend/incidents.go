// Incidents — the data model behind the public status page (D2).
//
// Two surfaces:
//
//   * /api/v1/incidents/public         — unauthenticated, last 30 days,
//                                        feeds the StatusPage.
//   * /api/v1/auth/incidents{,/...}    — admin CRUD, used by the
//                                        IncidentManager tab in Alerts.
//
// Storage: single JSON file at /var/log/ncn-incidents/incidents.json,
// rewritten atomically (write .tmp + rename) on every mutation. Read
// path serves from an in-memory list under a single RWMutex; the rwmu
// is the only synchronization needed because admin write rate is human-
// keystroke pace (no contention worth caring about).
//
// Why a flat JSON file and not the audit-style JSONL append?
//
//   * Audit events are immutable observations; an incident is mutable
//     (status changes, updates append, severity reclassified).
//   * Total volume is tiny — even a busy month has <50 incidents.
//   * Rewriting ~10KB on a status change is cheap; saves the replay
//     logic event-sourcing would need.
//
// The audit log captures who created/changed each incident via the
// existing auditRecord() helper, so the audit trail is preserved
// separately even if someone deletes an entry from incidents.json.

package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	incidentsDir  = "/var/log/ncn-incidents"
	incidentsPath = incidentsDir + "/incidents.json"

	// Status vocabulary mirrors atlassian/statuspage conventions so the
	// UI semantics line up with what people already expect to see on a
	// status page. Don't add new values without updating the StatusPage
	// color map at the same time.
	incidentStatusInvestigating = "investigating"
	incidentStatusIdentified    = "identified"
	incidentStatusMonitoring    = "monitoring"
	incidentStatusResolved      = "resolved"

	incidentSeverityMinor    = "minor"
	incidentSeverityMajor    = "major"
	incidentSeverityCritical = "critical"

	// Public list cutoff: 30 days of history is what shows on /status.
	// Resolved incidents older than this are kept in the file (so an
	// admin export still has them) but hidden from the public view.
	incidentPublicWindow = 30 * 24 * time.Hour
)

// Incident is the wire + on-disk shape.
type Incident struct {
	ID           string           `json:"id"`             // 16-hex
	Title        string           `json:"title"`          // operator-supplied, short
	Status       string           `json:"status"`         // investigating | identified | monitoring | resolved
	Severity     string           `json:"severity"`       // minor | major | critical
	AffectedPoPs []string         `json:"affected_pops,omitempty"`
	Body         string           `json:"body"`           // initial description (markdown allowed; UI renders as plain text)
	Updates      []IncidentUpdate `json:"updates,omitempty"`
	CreatedBy    string           `json:"created_by"`     // operator username
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
	ResolvedAt   *time.Time       `json:"resolved_at,omitempty"`
}

// IncidentUpdate is a status update inside an incident's timeline.
type IncidentUpdate struct {
	TS      time.Time `json:"ts"`
	Status  string    `json:"status,omitempty"`  // optional status change at this update
	Message string    `json:"message"`
	Author  string    `json:"author"`
}

type incidentStore struct {
	mu        sync.RWMutex
	incidents []*Incident
}

var globalIncidents *incidentStore

func newIncidentStore() (*incidentStore, error) {
	if err := os.MkdirAll(incidentsDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", incidentsDir, err)
	}
	s := &incidentStore{incidents: []*Incident{}}

	// Prefer Postgres when it already holds the document (post-cutover).
	loadedFromDB := false
	if globalDB != nil {
		if doc, err := loadConfigDoc("incidents"); err != nil {
			log.Printf("incidents: db load failed (%v) — falling back to file", err)
		} else if doc != nil {
			if err := json.Unmarshal(doc, &s.incidents); err != nil {
				return nil, fmt.Errorf("parse db doc: %w", err)
			}
			loadedFromDB = true
		}
	}

	// Otherwise load the JSON file if present.
	if !loadedFromDB {
		b, err := os.ReadFile(incidentsPath)
		if err == nil && len(b) > 0 {
			if err := json.Unmarshal(b, &s.incidents); err != nil {
				return nil, fmt.Errorf("parse %s: %w", incidentsPath, err)
			}
		}
	}

	// Migrate file→DB on the first DB-enabled boot.
	if globalDB != nil && !loadedFromDB {
		s.mu.Lock()
		err := s.persist()
		s.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("migrate incidents to db: %w", err)
		}
	}
	return s, nil
}

// persist serialises the current list atomically (write .tmp + rename), then
// dual-writes the same document into Postgres when available (the file stays
// the durable backup + globalDB==nil path; a DB error is non-fatal). Must be
// called with s.mu held (caller's responsibility).
func (s *incidentStore) persist() error {
	b, err := json.MarshalIndent(s.incidents, "", "  ")
	if err != nil {
		return err
	}
	tmp := incidentsPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, incidentsPath); err != nil {
		return err
	}
	if globalDB != nil {
		if err := saveConfigDoc("incidents", b); err != nil {
			log.Printf("incidents: db persist failed (%v) — file is current", err)
		}
	}
	return nil
}

// listSnapshot returns a sorted (desc by CreatedAt) deep-ish copy under
// the read lock. The slice is fresh (won't be mutated by writers) but
// the *Incident pointers inside still alias the store — read-only
// callers should not mutate the returned items.
func (s *incidentStore) listSnapshot() []*Incident {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Incident, len(s.incidents))
	copy(out, s.incidents)
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func (s *incidentStore) findByID(id string) *Incident {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, inc := range s.incidents {
		if inc.ID == id {
			return inc
		}
	}
	return nil
}

func (s *incidentStore) create(inc *Incident) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.incidents = append(s.incidents, inc)
	return s.persist()
}

func (s *incidentStore) update(id string, mut func(*Incident) error) (*Incident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, inc := range s.incidents {
		if inc.ID == id {
			if err := mut(inc); err != nil {
				return nil, err
			}
			inc.UpdatedAt = time.Now().UTC()
			if err := s.persist(); err != nil {
				return nil, err
			}
			return inc, nil
		}
	}
	return nil, fmt.Errorf("incident %s not found", id)
}

// ────────────────────────────── Helpers ──────────────────────────────

func newIncidentID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func isValidIncidentStatus(s string) bool {
	switch s {
	case incidentStatusInvestigating, incidentStatusIdentified,
		incidentStatusMonitoring, incidentStatusResolved:
		return true
	}
	return false
}

func isValidIncidentSeverity(s string) bool {
	switch s {
	case incidentSeverityMinor, incidentSeverityMajor, incidentSeverityCritical:
		return true
	}
	return false
}

// publicView strips internal fields that shouldn't appear on the public
// status page. Currently that's the operator username on CreatedBy +
// Update.Author — these are internal identities, not for external eyes.
type incidentPublic struct {
	ID           string                 `json:"id"`
	Title        string                 `json:"title"`
	Status       string                 `json:"status"`
	Severity     string                 `json:"severity"`
	AffectedPoPs []string               `json:"affected_pops,omitempty"`
	Body         string                 `json:"body"`
	Updates      []incidentUpdatePublic `json:"updates,omitempty"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
	ResolvedAt   *time.Time             `json:"resolved_at,omitempty"`
}

type incidentUpdatePublic struct {
	TS      time.Time `json:"ts"`
	Status  string    `json:"status,omitempty"`
	Message string    `json:"message"`
}

func (inc *Incident) publicView() incidentPublic {
	updates := make([]incidentUpdatePublic, len(inc.Updates))
	for i, u := range inc.Updates {
		updates[i] = incidentUpdatePublic{TS: u.TS, Status: u.Status, Message: u.Message}
	}
	return incidentPublic{
		ID:           inc.ID,
		Title:        inc.Title,
		Status:       inc.Status,
		Severity:     inc.Severity,
		AffectedPoPs: inc.AffectedPoPs,
		Body:         inc.Body,
		Updates:      updates,
		CreatedAt:    inc.CreatedAt,
		UpdatedAt:    inc.UpdatedAt,
		ResolvedAt:   inc.ResolvedAt,
	}
}

// ────────────────────────────── HTTP handlers ──────────────────────────────

// GET /api/v1/incidents/public — unauthenticated. Returns last 30 days
// of incidents (resolved + open) sorted desc by CreatedAt. Hides
// internal CreatedBy + Author fields. Cached for 15s via Cache-Control
// since the data only ticks on admin keystrokes.
func handleIncidentsPublic(w http.ResponseWriter, r *http.Request) {
	if globalIncidents == nil {
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: []incidentPublic{}})
		return
	}
	cutoff := time.Now().Add(-incidentPublicWindow)
	all := globalIncidents.listSnapshot()
	out := make([]incidentPublic, 0, len(all))
	for _, inc := range all {
		if inc.CreatedAt.Before(cutoff) {
			continue
		}
		out = append(out, inc.publicView())
	}
	w.Header().Set("Cache-Control", "public, max-age=15")
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
}

// adminOperator extracts the operator username from the context, which
// the requireRole("admin", ...) middleware has already populated. Should
// only be called from handlers that are wrapped by that middleware —
// the cast is unchecked because the middleware refuses to invoke the
// handler if the context is missing.
func adminOperator(r *http.Request) string {
	c, _ := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if c == nil {
		return ""
	}
	return c.Sub
}

// GET/POST /api/v1/auth/incidents
//   GET  → list (admin sees all, including resolved + closed)
//   POST → create
//
// Single handler because the ServeMux registers by path and we don't
// want two entries; method dispatch happens here.
func handleIncidentsAdminRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		handleIncidentsAdminList(w, r)
	case http.MethodPost:
		handleIncidentsAdminCreate(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
	}
}

// PATCH/POST /api/v1/auth/incidents/{id} | /{id}/updates
//
// Sub-path routing:
//   PATCH /incidents/{id}            → update fields
//   POST  /incidents/{id}/updates    → append a timeline entry
//
// One handler so ServeMux only needs the /api/v1/auth/incidents/ prefix
// registered once. Branches by suffix + method.
func handleIncidentsAdminItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/incidents/")
	switch {
	case strings.HasSuffix(rest, "/updates") && r.Method == http.MethodPost:
		handleIncidentsAdminAddUpdate(w, r)
	case !strings.Contains(rest, "/") && r.Method == http.MethodPatch:
		handleIncidentsAdminPatch(w, r)
	case !strings.Contains(rest, "/") && r.Method == http.MethodDelete:
		handleIncidentsAdminDelete(w, r)
	default:
		w.Header().Set("Allow", "PATCH, DELETE, POST")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method/path not allowed"})
	}
}

// Internal list handler — invoked by the dispatcher above. NOT
// directly registered with the mux.
func handleIncidentsAdminList(w http.ResponseWriter, r *http.Request) {
	if globalIncidents == nil {
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: []*Incident{}})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: globalIncidents.listSnapshot()})
}

// DELETE /api/v1/auth/incidents/{id} — remove a stale / mistaken
// entry entirely (audit log still records the deletion). Distinct from
// "resolved" — resolved entries stay visible on /status for the
// trailing 30 days; deleted entries vanish immediately.
func handleIncidentsAdminDelete(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/incidents/")
	globalIncidents.mu.Lock()
	defer globalIncidents.mu.Unlock()
	idx := -1
	for i, inc := range globalIncidents.incidents {
		if inc.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "not found"})
		return
	}
	deleted := globalIncidents.incidents[idx]
	globalIncidents.incidents = append(globalIncidents.incidents[:idx], globalIncidents.incidents[idx+1:]...)
	if err := globalIncidents.persist(); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	auditRecord(r, AuditEvent{
		Event: "incident.delete", Severity: auditSevWarn, Actor: op,
		Target: id, Details: map[string]any{"title": deleted.Title, "status": deleted.Status},
	})
	writeJSON(w, http.StatusOK, envelope{OK: true})
}

// POST /api/v1/auth/incidents — admin create.
//
// Body shape:
//   { "title": "...", "severity": "major", "affected_pops": ["pop-04"],
//     "status": "investigating", "body": "..." }
//
// Status defaults to "investigating" if omitted. Severity defaults to
// "minor". Body must be non-empty (it's the first timeline entry).
func handleIncidentsAdminCreate(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	var req struct {
		Title        string   `json:"title"`
		Status       string   `json:"status"`
		Severity     string   `json:"severity"`
		AffectedPoPs []string `json:"affected_pops"`
		Body         string   `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json: " + err.Error()})
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Body = strings.TrimSpace(req.Body)
	if req.Title == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "title required"})
		return
	}
	if req.Body == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "body required"})
		return
	}
	if req.Status == "" {
		req.Status = incidentStatusInvestigating
	}
	if req.Severity == "" {
		req.Severity = incidentSeverityMinor
	}
	if !isValidIncidentStatus(req.Status) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid status"})
		return
	}
	if !isValidIncidentSeverity(req.Severity) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid severity"})
		return
	}

	now := time.Now().UTC()
	inc := &Incident{
		ID:           newIncidentID(),
		Title:        req.Title,
		Status:       req.Status,
		Severity:     req.Severity,
		AffectedPoPs: req.AffectedPoPs,
		Body:         req.Body,
		CreatedBy:    op,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if req.Status == incidentStatusResolved {
		inc.ResolvedAt = &now
	}
	if err := globalIncidents.create(inc); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	auditRecord(r, AuditEvent{
		Event: "incident.create", Severity: auditSevWarn, Actor: op,
		Target: inc.ID, Details: map[string]any{"title": inc.Title, "severity": inc.Severity},
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: inc})
}

// PATCH /api/v1/auth/incidents/{id} — admin update.
//
// Body fields are all optional; whatever is set overwrites the
// corresponding field. The common path is { "status": "monitoring" }
// or { "severity": "major", "affected_pops": ["pop-04","pop-06"] }.
//
// Status transition to "resolved" stamps ResolvedAt; transition AWAY
// from "resolved" clears it (rare — re-opened incidents).
func handleIncidentsAdminPatch(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/incidents/")
	if id == "" || strings.Contains(id, "/") {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing id"})
		return
	}
	var req struct {
		Title        *string  `json:"title,omitempty"`
		Status       *string  `json:"status,omitempty"`
		Severity     *string  `json:"severity,omitempty"`
		AffectedPoPs []string `json:"affected_pops,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	inc, err := globalIncidents.update(id, func(inc *Incident) error {
		if req.Title != nil {
			t := strings.TrimSpace(*req.Title)
			if t == "" {
				return fmt.Errorf("title cannot be empty")
			}
			inc.Title = t
		}
		if req.Status != nil {
			if !isValidIncidentStatus(*req.Status) {
				return fmt.Errorf("invalid status")
			}
			inc.Status = *req.Status
			now := time.Now().UTC()
			if *req.Status == incidentStatusResolved {
				inc.ResolvedAt = &now
			} else {
				inc.ResolvedAt = nil
			}
		}
		if req.Severity != nil {
			if !isValidIncidentSeverity(*req.Severity) {
				return fmt.Errorf("invalid severity")
			}
			inc.Severity = *req.Severity
		}
		if req.AffectedPoPs != nil {
			inc.AffectedPoPs = req.AffectedPoPs
		}
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: err.Error()})
		return
	}
	auditRecord(r, AuditEvent{
		Event: "incident.update", Severity: auditSevInfo, Actor: op,
		Target: id, Details: map[string]any{"status": inc.Status, "severity": inc.Severity},
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: inc})
}

// POST /api/v1/auth/incidents/{id}/updates — append a timeline entry.
//
// Body: { "message": "...", "status": "monitoring" (optional) }
//
// If status is set, the incident's top-level Status updates too AND
// gets stamped through the same ResolvedAt logic as PATCH.
func handleIncidentsAdminAddUpdate(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	// Strip the trailing /updates from path → leaves the id.
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/incidents/")
	id = strings.TrimSuffix(id, "/updates")
	if id == "" || strings.Contains(id, "/") {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing id"})
		return
	}
	var req struct {
		Message string `json:"message"`
		Status  string `json:"status,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "message required"})
		return
	}
	if req.Status != "" && !isValidIncidentStatus(req.Status) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid status"})
		return
	}
	now := time.Now().UTC()
	inc, err := globalIncidents.update(id, func(inc *Incident) error {
		inc.Updates = append(inc.Updates, IncidentUpdate{
			TS: now, Status: req.Status, Message: req.Message, Author: op,
		})
		if req.Status != "" {
			inc.Status = req.Status
			if req.Status == incidentStatusResolved {
				inc.ResolvedAt = &now
			} else {
				inc.ResolvedAt = nil
			}
		}
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: err.Error()})
		return
	}
	auditRecord(r, AuditEvent{
		Event: "incident.update.append", Severity: auditSevInfo, Actor: op,
		Target: id, Details: map[string]any{"status": req.Status, "len": len(req.Message)},
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: inc})
}

// (filepath import kept available for future paths; unused for now)
var _ = filepath.Join
