// peering_apply.go — public peering-application intake + admin review.
//
// Flow:
//   1. Anonymous applicant fills the form on https://example.com/peering-apply.
//   2. Frontend POSTs to /api/v1/peering/apply (this file). Rate-limited
//      per source IP to keep the queue from being flooded.
//   3. We persist to /etc/ncn-core-console/peering-applications.json,
//      send postmaster@example.com a heads-up email (admins read in webmail),
//      and a "we got your request" confirmation to the applicant's NOC
//      address. Both emails go via the operator-bridge.key signed channel
//      so we don't need a second copy of the noreply stash on tyo.
//   4. Admin opens /admin/peering, sees the queue, expands an entry, clicks
//      approve or reject (with optional notes). That POSTs to /decide here.
//   5. Decision triggers a third email to the applicant explaining next steps.
//
// Storage is a flat JSON file (same pattern as forgot-requests, invites,
// etc.). 90-day TTL — old decided applications garbage-collect on next load.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	peeringApplyPath    = authConfigDir + "/peering-applications.json"
	peeringMaxPerIP24h  = 5
	peeringMaxBodySize  = 16 << 10 // 16 KB
	peeringRequestTTL   = 90 * 24 * time.Hour
	peeringAdminMailbox = "postmaster@example.com"
)

// PeeringApplication is the persisted shape; all fields are wire-visible
// to the admin UI (the public form only sees the input subset).
type PeeringApplication struct {
	ID          string    `json:"id"`           // 12-hex
	SubmittedAt time.Time `json:"submitted_at"`
	Status      string    `json:"status"`       // pending | approved | rejected
	IP          string    `json:"ip"`
	UA          string    `json:"ua,omitempty"`

	// Identity
	ASN         uint32   `json:"asn"`
	NetworkName string   `json:"network_name"`
	ASSet       string   `json:"as_set,omitempty"`
	IRRSource   string   `json:"irr_source,omitempty"` // RIPE / ARIN / RADB / ...

	// Contact
	ContactName string `json:"contact_name,omitempty"`
	NOCEmail    string `json:"noc_email"`
	Phone       string `json:"phone,omitempty"`

	// Technical
	Prefixes6   []string `json:"prefixes6"`            // required, IPv6 CIDRs
	Prefixes4   []string `json:"prefixes4,omitempty"`  // optional; we're v6-only but we record what they hope to bring
	MaxPrefix6  int      `json:"max_prefix6,omitempty"`
	HasRPKI     bool     `json:"has_rpki"`
	BFDDesired  bool     `json:"bfd_desired,omitempty"`

	// Connectivity
	Locations    []string `json:"locations,omitempty"`     // hkg / tyo / fra
	SessionTypes []string `json:"session_types,omitempty"` // tunnel / ix
	IXMember     []string `json:"ix_member,omitempty"`     // dsix / p7ix
	Notes        string   `json:"notes,omitempty"`

	// Admin
	AdminNotes string    `json:"admin_notes,omitempty"`
	DecidedBy  string    `json:"decided_by,omitempty"`
	DecidedAt  time.Time `json:"decided_at,omitempty"`
}

type peeringFile struct {
	Version      int                  `json:"version"`
	Applications []PeeringApplication `json:"applications"`
}

type peeringStore struct {
	mu      sync.RWMutex
	apps    []PeeringApplication
	bridge  *mailBridgeService // for the operator-mail-bridge.key
	fleet   *fleetScraper      // for IRR expansion + peer-config apply (peerApply.go)
}

func newPeeringStore(bridge *mailBridgeService) (*peeringStore, error) {
	s := &peeringStore{bridge: bridge}
	loadedFromDB, err := s.load()
	if err != nil {
		return nil, err
	}
	// Migrate file→DB on the first DB-enabled boot.
	if globalDB != nil && !loadedFromDB {
		s.mu.Lock()
		err := s.persistLocked()
		s.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("migrate peering to db: %w", err)
		}
	}
	return s, nil
}

// load fills s.apps from Postgres when present, else the JSON file. Returns
// whether the data came from the DB (so the caller can migrate file→DB).
func (s *peeringStore) load() (bool, error) {
	if globalDB != nil {
		if doc, err := loadConfigDoc("peering_apply"); err != nil {
			log.Printf("peering: db load failed (%v) — falling back to file", err)
		} else if doc != nil {
			var f peeringFile
			if err := json.Unmarshal(doc, &f); err != nil {
				return false, fmt.Errorf("parse db doc: %w", err)
			}
			s.apps = f.Applications
			s.gcLocked(time.Now().UTC())
			return true, nil
		}
	}
	data, err := os.ReadFile(peeringApplyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var f peeringFile
	if err := json.Unmarshal(data, &f); err != nil {
		return false, fmt.Errorf("parse %s: %w", peeringApplyPath, err)
	}
	s.apps = f.Applications
	s.gcLocked(time.Now().UTC())
	return false, nil
}

func (s *peeringStore) persistLocked() error {
	f := peeringFile{Version: 1, Applications: s.apps}
	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := peeringApplyPath + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, peeringApplyPath); err != nil {
		return err
	}
	if globalDB != nil {
		if err := saveConfigDoc("peering_apply", body); err != nil {
			log.Printf("peering: db persist failed (%v) — file is current", err)
		}
	}
	return nil
}

func (s *peeringStore) gcLocked(now time.Time) {
	live := s.apps[:0]
	for _, a := range s.apps {
		// Keep pending forever-ish (operator still needs to decide).
		// Drop only decided applications older than TTL.
		if a.Status != "pending" && now.Sub(a.DecidedAt) > peeringRequestTTL {
			continue
		}
		live = append(live, a)
	}
	s.apps = live
}

// ----- Public submit ------------------------------------------------------

// POST /api/v1/peering/apply
// Anonymous. Returns 200 + { id } on success (or rate-limit hit).
func (s *peeringStore) handleApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}

	var raw struct {
		ASN          uint32   `json:"asn"`
		NetworkName  string   `json:"network_name"`
		ASSet        string   `json:"as_set"`
		IRRSource    string   `json:"irr_source"`
		ContactName  string   `json:"contact_name"`
		NOCEmail     string   `json:"noc_email"`
		Phone        string   `json:"phone"`
		Prefixes6    []string `json:"prefixes6"`
		Prefixes4    []string `json:"prefixes4"`
		MaxPrefix6   int      `json:"max_prefix6"`
		HasRPKI      bool     `json:"has_rpki"`
		BFDDesired   bool     `json:"bfd_desired"`
		Locations    []string `json:"locations"`
		SessionTypes []string `json:"session_types"`
		IXMember     []string `json:"ix_member"`
		Notes        string   `json:"notes"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, peeringMaxBodySize)).Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}

	// Required-field validation.
	if raw.ASN == 0 || raw.ASN > 4294967295 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid ASN"})
		return
	}
	if strings.TrimSpace(raw.NetworkName) == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "network_name required"})
		return
	}
	noc := strings.ToLower(strings.TrimSpace(raw.NOCEmail))
	if _, err := mail.ParseAddress(noc); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid noc_email"})
		return
	}
	cleanPrefixes6 := cleanPrefixList(raw.Prefixes6, true)
	if len(cleanPrefixes6) == 0 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "at least one IPv6 prefix required"})
		return
	}
	cleanPrefixes4 := cleanPrefixList(raw.Prefixes4, false)

	now := time.Now().UTC()
	ip := clientAddr(r)
	ua := strings.TrimSpace(r.Header.Get("User-Agent"))

	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked(now)

	// Per-IP daily cap. Counts ALL submissions (decided or not) from this
	// IP in the last 24h.
	ipIn24h := 0
	for _, a := range s.apps {
		if now.Sub(a.SubmittedAt) > 24*time.Hour {
			continue
		}
		if a.IP == ip {
			ipIn24h++
		}
	}
	if ipIn24h >= peeringMaxPerIP24h {
		writeJSON(w, http.StatusTooManyRequests, envelope{OK: false,
			Error: "rate limit: too many applications from your IP today — try again tomorrow"})
		return
	}

	id := make([]byte, 6)
	_, _ = rand.Read(id)
	app := PeeringApplication{
		ID:           hex.EncodeToString(id),
		SubmittedAt:  now,
		Status:       "pending",
		IP:           ip,
		UA:           ua,
		ASN:          raw.ASN,
		NetworkName:  strings.TrimSpace(raw.NetworkName),
		ASSet:        strings.TrimSpace(raw.ASSet),
		IRRSource:    strings.TrimSpace(raw.IRRSource),
		ContactName:  strings.TrimSpace(raw.ContactName),
		NOCEmail:     noc,
		Phone:        strings.TrimSpace(raw.Phone),
		Prefixes6:    cleanPrefixes6,
		Prefixes4:    cleanPrefixes4,
		MaxPrefix6:   raw.MaxPrefix6,
		HasRPKI:      raw.HasRPKI,
		BFDDesired:   raw.BFDDesired,
		Locations:    cleanStringList(raw.Locations),
		SessionTypes: cleanStringList(raw.SessionTypes),
		IXMember:     cleanStringList(raw.IXMember),
		Notes:        strings.TrimSpace(raw.Notes),
	}
	s.apps = append(s.apps, app)
	if err := s.persistLocked(); err != nil {
		log.Printf("peering-apply: persist %s: %v", peeringApplyPath, err)
	}
	log.Printf("peering-apply: queued id=%s AS%d %s from %s",
		app.ID, app.ASN, app.NetworkName, ip)
	auditRecord(r, AuditEvent{
		Event: "peering.apply", Actor: "anonymous",
		Target:  fmt.Sprintf("AS%d", app.ASN),
		Details: map[string]any{"id": app.ID, "network": app.NetworkName, "noc_email": app.NOCEmail},
	})

	// Fire-and-forget the two notification emails. We don't fail the
	// submission if SMTP hiccups — the queue is the source of truth.
	go s.notifyAdminOfNewApplication(app)
	go s.notifyApplicantReceived(app)

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"id":   app.ID,
		"asn":  app.ASN,
		"name": app.NetworkName,
	}})
}

// ----- Admin: list + decide ----------------------------------------------

// GET /api/v1/auth/peering/applications
func (s *peeringStore) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET only"})
		return
	}
	s.mu.Lock()
	s.gcLocked(time.Now().UTC())
	out := make([]PeeringApplication, len(s.apps))
	copy(out, s.apps)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
}

// POST /api/v1/auth/peering/applications/<id>/decide
// Body: { status: "approved"|"rejected", admin_notes: "...optional..." }
func (s *peeringStore) handleDecide(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/auth/peering/applications/"), "/decide")
	id = strings.Trim(id, "/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "id required"})
		return
	}
	c, ok := r.Context().Value(ctxKeyAuth).(*sessionClaims)
	if !ok || c == nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	var raw struct {
		Status     string `json:"status"`
		AdminNotes string `json:"admin_notes"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	status := strings.ToLower(strings.TrimSpace(raw.Status))
	if status != "approved" && status != "rejected" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false,
			Error: "status must be approved or rejected"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var decided *PeeringApplication
	for i := range s.apps {
		if s.apps[i].ID == id {
			s.apps[i].Status = status
			s.apps[i].AdminNotes = strings.TrimSpace(raw.AdminNotes)
			s.apps[i].DecidedBy = c.Sub
			s.apps[i].DecidedAt = time.Now().UTC()
			decided = &s.apps[i]
			break
		}
	}
	if decided == nil {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "no such application"})
		return
	}
	if err := s.persistLocked(); err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	log.Printf("peering-apply: id=%s AS%d → %s by operator=%s",
		decided.ID, decided.ASN, status, c.Sub)
	auditSev := auditSevInfo
	if status == "approved" {
		auditSev = auditSevWarn
	}
	auditRecord(r, AuditEvent{
		Event: "peering.decide." + status, Severity: auditSev, Actor: c.Sub,
		Target: fmt.Sprintf("AS%d", decided.ASN),
		Details: map[string]any{"id": decided.ID, "network": decided.NetworkName},
	})

	// Snapshot for the async email goroutine — the slice can move under
	// the lock once we release it.
	snap := *decided
	go s.notifyApplicantDecision(snap)

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"id":     decided.ID,
		"status": decided.Status,
	}})
}

// ----- Helpers -----------------------------------------------------------

func cleanPrefixList(in []string, isV6 bool) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		_, _, err := net.ParseCIDR(p)
		if err != nil {
			continue
		}
		if isV6 {
			if !strings.Contains(p, ":") {
				continue
			}
		} else {
			if !strings.Contains(p, ".") {
				continue
			}
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

func cleanStringList(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// ----- Notification emails (via the send-bridge on pop-03) ---------------

func (s *peeringStore) notifyAdminOfNewApplication(app PeeringApplication) {
	if s.bridge == nil || len(s.bridge.key) == 0 {
		return
	}
	subj := fmt.Sprintf("[peering] new request from AS%d %s", app.ASN, app.NetworkName)
	paragraphs := []string{
		"A new peering application just landed in the queue.",
		"    network:    " + app.NetworkName + "\n" +
			"    asn:        AS" + fmt.Sprint(app.ASN) + "\n" +
			"    contact:    " + strDefault(app.ContactName, "—") + " <" + app.NOCEmail + ">\n" +
			"    as-set:     " + strDefault(app.ASSet, "—") + "\n" +
			"    irr-source: " + strDefault(app.IRRSource, "—") + "\n" +
			"    rpki:       " + boolMark(app.HasRPKI) + "\n" +
			"    locations:  " + strDefault(strings.Join(app.Locations, ", "), "—") + "\n" +
			"    sessions:   " + strDefault(strings.Join(app.SessionTypes, ", "), "—") + "\n" +
			"    ix:         " + strDefault(strings.Join(app.IXMember, ", "), "—") + "\n" +
			"    prefixes6:  " + strDefault(strings.Join(app.Prefixes6, ", "), "—"),
		"Notes from applicant:\n\n    " + strDefault(app.Notes, "(none)"),
		"Review in the admin console: https://admin.example.com/admin/peering — approve / reject from there.",
	}
	if err := s.dispatchBridgeMail(peeringAdminMailbox, subj, "New Peering Application", paragraphs, "system"); err != nil {
		log.Printf("peering-apply: admin notify failed: %v", err)
	}
}

func (s *peeringStore) notifyApplicantReceived(app PeeringApplication) {
	if s.bridge == nil || len(s.bridge.key) == 0 {
		return
	}
	subj := fmt.Sprintf("We received your peering request — AS%d", app.ASN)
	paragraphs := []string{
		"Thanks for applying to peer with Acme Net (AS64500).",
		"    your asn:    AS" + fmt.Sprint(app.ASN) + "\n" +
			"    your name:   " + app.NetworkName + "\n" +
			"    submitted:   " + app.SubmittedAt.Format(time.RFC1123Z) + "\n" +
			"    request id:  " + app.ID,
		"We typically reply within 72 hours. If you don't hear back, drop a note to noc@example.com referencing your request id above.",
		"If you didn't apply to peer with us, ignore this message — nothing happens without an operator's manual review.",
	}
	if err := s.dispatchBridgeMail(app.NOCEmail, subj, "Peering Request Received", paragraphs, "system"); err != nil {
		log.Printf("peering-apply: applicant ack failed: %v", err)
	}
}

func (s *peeringStore) notifyApplicantDecision(app PeeringApplication) {
	if s.bridge == nil || len(s.bridge.key) == 0 {
		return
	}
	var subj, headline string
	var paragraphs []string
	if app.Status == "approved" {
		subj = fmt.Sprintf("Peering approved — AS%d ↔ AS64500", app.ASN)
		headline = "Peering Approved"
		paragraphs = []string{
			"Good news — your peering application has been approved.",
			"    your asn:   AS" + fmt.Sprint(app.ASN) + "\n" +
				"    network:    " + app.NetworkName + "\n" +
				"    request id: " + app.ID,
			"Next step: an operator will reach out to noc@example.com (or whichever NOC address you supplied) within a couple of business days with the session parameters — peer IP, MD5/BFD config, tunnel endpoints if relevant.",
		}
		if app.AdminNotes != "" {
			paragraphs = append(paragraphs, "Operator note:\n\n    "+app.AdminNotes)
		}
	} else {
		subj = fmt.Sprintf("Peering request not approved — AS%d", app.ASN)
		headline = "Peering Request Not Approved"
		paragraphs = []string{
			"Thanks for your interest, but we're unable to proceed with this peering request at the moment.",
			"    your asn:   AS" + fmt.Sprint(app.ASN) + "\n" +
				"    network:    " + app.NetworkName + "\n" +
				"    request id: " + app.ID,
		}
		if app.AdminNotes != "" {
			paragraphs = append(paragraphs, "Reason:\n\n    "+app.AdminNotes)
		} else {
			paragraphs = append(paragraphs, "If you'd like more context, drop a note to noc@example.com referencing the request id above.")
		}
	}
	if err := s.dispatchBridgeMail(app.NOCEmail, subj, headline, paragraphs, app.Status); err != nil {
		log.Printf("peering-apply: decision notify failed: %v", err)
	}
}

// dispatchBridgeMail is a thin wrapper around mailBridgeService.SendSystemMail
// — kept as a method so existing call sites still type-check, but all the
// HMAC + HTTP plumbing now lives on the shared bridge service so other
// callers (invite, future) get the same protocol.
func (s *peeringStore) dispatchBridgeMail(to, subject, headline string, paragraphs []string, kind string) error {
	return s.bridge.SendSystemMail(to, subject, headline, paragraphs, "peering-apply."+kind)
}

func strDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
func boolMark(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
