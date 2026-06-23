// audit.go — structured, append-only audit log for sensitive operations.
//
// Every operator-impacting or auth-relevant handler calls auditRecord(...)
// which appends one JSON object per line to /var/log/ncn-audit/audit.jsonl.
// The directory and file are mode 0700/0600 root.
//
// Operational hardening (set by ops, NOT by this code so dev iteration
// stays clean):
//   1. DONE (2026-05-28): logrotate — daily, keep 365 days, on tyo + fmt.
//      Config lives in the repo at deploy/logrotate/ncn-audit. Uses
//      `copytruncate` because this process appends with O_APPEND and never
//      reopens on a signal — copytruncate preserves the inode so the live fd
//      keeps writing (verified: forced rotate kept inode + restored +a).
//   2. DONE: `chattr +a /var/log/ncn-audit/audit.jsonl` — append-only at the
//      FS level. The logrotate prerotate/postrotate hooks drop and restore
//      the flag around the truncate (chattr refuses to truncate an +a file).
//   3. TODO: daily rsync to an offsite location for tamper-evident retention.
//
// The read API at GET /api/v1/auth/audit is admin-only and supports
// server-side filtering + cursor pagination (newest first). Since this is
// admin-team-only, we just hold the whole file in memory and re-read on
// each query — at ~200 bytes/event and ~5000 events/day, a year of audit
// is ~365 MB worst-case, but realistic volume is closer to 50 MB. If/when
// the file grows past where in-memory scan is comfortable we can swap in
// an indexed reader; for now simplicity wins.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	auditDir  = "/var/log/ncn-audit"
	auditPath = auditDir + "/audit.jsonl"

	// Read paging caps. Browser side asks for 100 by default; admin can
	// crank to 1000 for export-style views.
	auditDefaultLimit = 100
	auditMaxLimit     = 1000

	// Severity vocabulary — kept tiny so the UI's color mapping stays
	// trivial. info = "happened", warn = "someone got something wrong",
	// critical = "privilege change or break-glass".
	auditSevInfo     = "info"
	auditSevWarn     = "warn"
	auditSevCritical = "critical"
)

// AuditEvent is the wire and on-disk shape. The fields are deliberately
// flat — nested objects make grep / jq harder.
type AuditEvent struct {
	ID       string         `json:"id"`                // 16-hex
	TS       time.Time      `json:"ts"`                // UTC, RFC3339Nano on the wire
	Event    string         `json:"event"`             // dot-separated event name e.g. "login.ok"
	Severity string         `json:"severity"`          // info | warn | critical
	Actor    string         `json:"actor"`             // operator username, "anonymous", or "system"
	Peer     string         `json:"peer,omitempty"`    // host:port from r.RemoteAddr (sanitized via clientAddr)
	UA       string         `json:"ua,omitempty"`      // User-Agent header
	Target   string         `json:"target,omitempty"`  // who/what was acted on (mailbox, peer ASN, other operator's username)
	Outcome  string         `json:"outcome"`           // ok | fail | denied
	Details  map[string]any `json:"details,omitempty"` // free-form context (kept small — never log secrets)
}

// auditStore is a process-singleton writer. Reads are stateless — they
// re-open and tail the file on every query.
type auditStore struct {
	mu sync.Mutex
	f  *os.File
}

// newAuditStore opens (creating if absent) the audit log for append.
// Must be called once at startup. Subsequent calls to auditRecord write
// through this singleton.
func newAuditStore() (*auditStore, error) {
	if err := os.MkdirAll(auditDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", auditDir, err)
	}
	f, err := os.OpenFile(auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", auditPath, err)
	}
	return &auditStore{f: f}, nil
}

// globalAudit is wired up in main(). Helpers below fall back to a no-op if
// the store hasn't been initialized (early-init code paths shouldn't lose
// data, but they shouldn't crash either).
var globalAudit *auditStore

// auditRecord persists one event. r may be nil for system-triggered or
// CLI-triggered events. Unset fields get sane defaults:
//   - ID: 16-hex random
//   - TS: time.Now().UTC()
//   - Severity: "info"
//   - Outcome: "ok"
//   - Peer/UA: extracted from r if non-nil
//
// Writes are best-effort: a logging failure is itself logged via log.Printf
// so journald keeps a trace, but the calling handler MUST NOT fail just
// because the audit append failed.
func auditRecord(r *http.Request, ev AuditEvent) {
	if ev.ID == "" {
		var b [8]byte
		_, _ = rand.Read(b[:])
		ev.ID = hex.EncodeToString(b[:])
	}
	if ev.TS.IsZero() {
		ev.TS = time.Now().UTC()
	}
	if ev.Severity == "" {
		ev.Severity = auditSevInfo
	}
	if ev.Outcome == "" {
		ev.Outcome = "ok"
	}
	if r != nil {
		if ev.Peer == "" {
			ev.Peer = clientAddr(r)
		}
		if ev.UA == "" {
			ev.UA = r.UserAgent()
		}
	}

	// Postgres-backed when available (durable beyond ctrl-01's disk); the JSONL
	// file stays the fallback so an event is never lost if the DB errors.
	if globalDB != nil {
		if err := auditInsertDB(ev); err == nil {
			return
		} else {
			log.Printf("audit: db insert failed (%v) — using file", err)
		}
	}

	if globalAudit == nil {
		log.Printf("audit: DROPPED (store not initialized) event=%s actor=%s", ev.Event, ev.Actor)
		return
	}

	line, err := json.Marshal(ev)
	if err != nil {
		log.Printf("audit: marshal FAIL event=%s: %v", ev.Event, err)
		return
	}
	line = append(line, '\n')

	globalAudit.mu.Lock()
	_, werr := globalAudit.f.Write(line)
	globalAudit.mu.Unlock()
	if werr != nil {
		log.Printf("audit: write FAIL event=%s: %v", ev.Event, werr)
	}
}

// auditCols is the audit table column list in struct order (insert + scan).
const auditCols = `id,ts,event,severity,actor,peer,ua,target,outcome,details`

// auditInsertDB writes one event to Postgres. details is stored as JSONB.
func auditInsertDB(ev AuditEvent) error {
	var det []byte
	if len(ev.Details) > 0 {
		det, _ = json.Marshal(ev.Details)
	}
	_, err := globalDB.Exec(`INSERT INTO audit (`+auditCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (id) DO NOTHING`,
		ev.ID, ev.TS, ev.Event, ev.Severity, ev.Actor, ev.Peer, ev.UA, ev.Target, ev.Outcome, det)
	return err
}

// scanAuditRow reads one row in auditCols order (*sql.Row or *sql.Rows).
func scanAuditRow(sc interface{ Scan(...any) error }) (AuditEvent, error) {
	var ev AuditEvent
	var det []byte
	if err := sc.Scan(&ev.ID, &ev.TS, &ev.Event, &ev.Severity, &ev.Actor,
		&ev.Peer, &ev.UA, &ev.Target, &ev.Outcome, &det); err != nil {
		return ev, err
	}
	if len(det) > 0 {
		_ = json.Unmarshal(det, &ev.Details)
	}
	ev.TS = ev.TS.UTC()
	return ev, nil
}

// auditScan returns events (optionally time-bounded) from Postgres when
// available, else the JSONL file. Time bounds are pushed down as the cheap big
// reducer; all OTHER filtering stays in auditFilter.match so the DB and file
// paths share one source of truth. Falls back to the file on any DB error.
func auditScan(since, until time.Time) ([]AuditEvent, error) {
	if globalDB != nil {
		if evs, err := auditScanDB(since, until); err == nil {
			return evs, nil
		} else {
			log.Printf("audit: db scan failed (%v) — falling back to file", err)
		}
	}
	return auditScanFile(since, until)
}

func auditScanDB(since, until time.Time) ([]AuditEvent, error) {
	q := `SELECT ` + auditCols + ` FROM audit`
	var args []any
	var conds []string
	if !since.IsZero() {
		args = append(args, since)
		conds = append(conds, fmt.Sprintf("ts >= $%d", len(args)))
	}
	if !until.IsZero() {
		args = append(args, until)
		conds = append(conds, fmt.Sprintf("ts <= $%d", len(args)))
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY ts DESC, id DESC"
	rows, err := globalDB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		ev, e := scanAuditRow(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func auditScanFile(since, until time.Time) ([]AuditEvent, error) {
	fh, err := os.Open(auditPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []AuditEvent{}, nil
		}
		return nil, fmt.Errorf("open %s: %w", auditPath, err)
	}
	defer fh.Close()
	var out []AuditEvent
	dec := json.NewDecoder(fh)
	for {
		var ev AuditEvent
		if err := dec.Decode(&ev); err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("audit: skip malformed line: %v", err)
			continue
		}
		if !since.IsZero() && ev.TS.Before(since) {
			continue
		}
		if !until.IsZero() && ev.TS.After(until) {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

// ----------------------------------------------------------------------------
// Read API
// ----------------------------------------------------------------------------

// auditFilter is parsed from URL query params on /api/v1/auth/audit.
// All filters are AND-combined. Empty filter = "give me the newest N".
type auditFilter struct {
	Event    string    // exact match or "prefix.*" (e.g. "login.*")
	Actor    string    // exact match
	Severity string    // info | warn | critical (empty = all)
	Outcome  string    // ok | fail | denied
	Since    time.Time // UTC
	Until    time.Time // UTC
	Q        string    // case-insensitive substring across actor/target/peer/details

	Limit  int    // ≤ auditMaxLimit
	Cursor string // ID of the last event seen; next page returns events older than that
}

func (f auditFilter) match(ev AuditEvent) bool {
	if f.Event != "" {
		if strings.HasSuffix(f.Event, ".*") {
			prefix := strings.TrimSuffix(f.Event, ".*") + "."
			if !strings.HasPrefix(ev.Event, prefix) && ev.Event != strings.TrimSuffix(f.Event, ".*") {
				return false
			}
		} else if ev.Event != f.Event {
			return false
		}
	}
	if f.Actor != "" && ev.Actor != f.Actor {
		return false
	}
	if f.Severity != "" && ev.Severity != f.Severity {
		return false
	}
	if f.Outcome != "" && ev.Outcome != f.Outcome {
		return false
	}
	if !f.Since.IsZero() && ev.TS.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && ev.TS.After(f.Until) {
		return false
	}
	if f.Q != "" {
		needle := strings.ToLower(f.Q)
		hay := strings.ToLower(strings.Join([]string{
			ev.Actor, ev.Target, ev.Peer, ev.UA, ev.Event,
		}, " "))
		if !strings.Contains(hay, needle) {
			// Also scan details as JSON for free-text match.
			if len(ev.Details) > 0 {
				detB, _ := json.Marshal(ev.Details)
				if !strings.Contains(strings.ToLower(string(detB)), needle) {
					return false
				}
			} else {
				return false
			}
		}
	}
	return true
}

// auditQuery loads the audit file, filters and sorts newest-first, applies
// cursor (if set) by dropping events ≥ cursor.TS, then takes Limit. Returns
// (page, nextCursor, error). nextCursor is empty when the page is the last.
func auditQuery(f auditFilter) ([]AuditEvent, string, error) {
	if f.Limit <= 0 {
		f.Limit = auditDefaultLimit
	}
	if f.Limit > auditMaxLimit {
		f.Limit = auditMaxLimit
	}

	events, err := auditScan(f.Since, f.Until)
	if err != nil {
		return nil, "", err
	}
	var all []AuditEvent
	for _, ev := range events {
		if f.match(ev) {
			all = append(all, ev)
		}
	}

	sort.Slice(all, func(i, j int) bool { return all[i].TS.After(all[j].TS) })

	// Cursor: drop events newer-or-equal-to the cursor ID's timestamp.
	// We locate the cursor's index, then start the page just after it.
	startIdx := 0
	if f.Cursor != "" {
		for i, ev := range all {
			if ev.ID == f.Cursor {
				startIdx = i + 1
				break
			}
		}
	}
	if startIdx >= len(all) {
		return []AuditEvent{}, "", nil
	}

	end := startIdx + f.Limit
	if end > len(all) {
		end = len(all)
	}
	page := all[startIdx:end]

	nextCursor := ""
	if end < len(all) {
		nextCursor = page[len(page)-1].ID
	}
	return page, nextCursor, nil
}

// auditStats returns a coarse histogram of the last 24h, bucketed hourly,
// plus the count by severity. Used by the UI's "last 24h" header strip.
type auditStats struct {
	Now       time.Time              `json:"now"`
	Total24h  int                    `json:"total_24h"`
	BySeverity map[string]int        `json:"by_severity"`
	ByEvent    map[string]int        `json:"by_event"`
	Hourly24h []auditHourBucket      `json:"hourly_24h"` // 24 entries, oldest → newest
}

type auditHourBucket struct {
	Hour  time.Time `json:"hour"`  // UTC, hour-truncated
	Count int       `json:"count"`
}

func auditComputeStats(now time.Time) (auditStats, error) {
	since := now.Add(-24 * time.Hour).Truncate(time.Hour)
	out := auditStats{
		Now:        now.UTC(),
		BySeverity: map[string]int{},
		ByEvent:    map[string]int{},
	}
	// Pre-seed 24 hour buckets so the sparkline never has gaps.
	out.Hourly24h = make([]auditHourBucket, 24)
	for i := 0; i < 24; i++ {
		out.Hourly24h[i].Hour = since.Add(time.Duration(i) * time.Hour)
	}

	events, err := auditScan(since, time.Time{})
	if err != nil {
		return out, err
	}
	for _, ev := range events {
		if ev.TS.Before(since) {
			continue
		}
		out.Total24h++
		out.BySeverity[ev.Severity]++
		out.ByEvent[ev.Event]++
		hourIdx := int(ev.TS.Sub(since) / time.Hour)
		if hourIdx >= 0 && hourIdx < 24 {
			out.Hourly24h[hourIdx].Count++
		}
	}
	return out, nil
}

// ----------------------------------------------------------------------------
// HTTP handlers
// ----------------------------------------------------------------------------

// handleAuditQuery is mounted at GET /api/v1/auth/audit. Admin-only.
//
// Query params (all optional):
//   event=login.ok            exact
//   event=login.*             prefix
//   actor=alice
//   severity=info|warn|critical
//   outcome=ok|fail|denied
//   since=2026-05-26T00:00:00Z
//   until=2026-05-27T00:00:00Z
//   q=substring               free-text search across actor/target/peer/UA/details
//   limit=100                 ≤ 1000
//   cursor=<id>               opaque pagination cursor (ID of last seen event)
func handleAuditQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET only"})
		return
	}

	q := r.URL.Query()
	f := auditFilter{
		Event:    strings.TrimSpace(q.Get("event")),
		Actor:    strings.TrimSpace(q.Get("actor")),
		Severity: strings.TrimSpace(q.Get("severity")),
		Outcome:  strings.TrimSpace(q.Get("outcome")),
		Q:        strings.TrimSpace(q.Get("q")),
		Cursor:   strings.TrimSpace(q.Get("cursor")),
	}
	if s := q.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "since: " + err.Error()})
			return
		}
		f.Since = t.UTC()
	}
	if s := q.Get("until"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "until: " + err.Error()})
			return
		}
		f.Until = t.UTC()
	}
	if s := q.Get("limit"); s != "" {
		var n int
		_, err := fmt.Sscanf(s, "%d", &n)
		if err == nil && n > 0 {
			f.Limit = n
		}
	}

	page, next, err := auditQuery(f)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, envelope{
		OK: true,
		Data: map[string]any{
			"events":      page,
			"next_cursor": next,
			"count":       len(page),
		},
	})
}

// handleAuditStats is mounted at GET /api/v1/auth/audit/stats. Admin-only.
// Returns the 24h sparkline + severity/event breakdown for the panel header.
func handleAuditStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET only"})
		return
	}
	stats, err := auditComputeStats(time.Now().UTC())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: stats})
}

// handleAuditExport is mounted at GET /api/v1/auth/audit/export. Admin-only.
// Streams the matching filtered events as JSONL with a download disposition.
// Limited to 10000 events per call to keep memory bounded.
func handleAuditExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET only"})
		return
	}
	q := r.URL.Query()
	f := auditFilter{
		Event:    strings.TrimSpace(q.Get("event")),
		Actor:    strings.TrimSpace(q.Get("actor")),
		Severity: strings.TrimSpace(q.Get("severity")),
		Outcome:  strings.TrimSpace(q.Get("outcome")),
		Q:        strings.TrimSpace(q.Get("q")),
		Limit:    10000,
	}
	if s := q.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err == nil {
			f.Since = t.UTC()
		}
	}
	if s := q.Get("until"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err == nil {
			f.Until = t.UTC()
		}
	}

	page, _, err := auditQuery(f)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/jsonl; charset=utf-8")
	w.Header().Set("Content-Disposition",
		`attachment; filename="ncn-audit-`+time.Now().UTC().Format("20060102-150405")+`.jsonl"`)
	enc := json.NewEncoder(w)
	for _, ev := range page {
		_ = enc.Encode(ev)
	}
}

