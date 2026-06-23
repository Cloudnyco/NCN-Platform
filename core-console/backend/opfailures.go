// opfailures.go — the operational-action failure-response system.
//
// Every operator-triggered fleet action (decommission / recommission / delete /
// mesh-apply / onboard) that fails mid-flight funnels through recordOpFailure,
// which does two things the operators asked for:
//   1. SURFACE + NOTIFY — a plain Telegram notification (reason + optional Agent
//      diagnosis) instead of a silent error. Crit failures are first triaged by
//      the ops Agent (read-only) and, if they need OUR fix, escalated from the
//      error channel to the ops group. NO action buttons (operators dropped the
//      inline retry/manage/dismiss panel). (see bot_opfail.go)
//   2. RECORD + QUERY — durable in Postgres (in-memory ring fallback), queryable
//      from the bot (read-only /errors) and a console API, PLUS a durable audit
//      `opfail.*` record.
//
// We deliberately do NOT auto-remediate. Incidents (incidents.go) stay a
// separate, manual, public-facing concept; this is the automatic, internal
// action-failure log.
package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

// globalNotify is the live Telegram notifier (set in main when the bot is
// configured) — lets out-of-band callers (e.g. the gated test trigger below)
// drive the real recordOpFailure → triage → channel flow.
var globalNotify *tgNotifier

// op-failure kinds — match the action that failed.
const (
	opKindDecommission = "decommission"
	opKindRecommission = "recommission"
	opKindDelete       = "delete"
	opKindMeshApply    = "mesh-apply"
	opKindOnboard      = "onboard"
)

const opFailureCap = 100

// opFailure is one recorded operational-action failure.
type opFailure struct {
	ID     string `json:"id"`     // 8-hex, stable for retry/dismiss callbacks
	Kind   string `json:"kind"`   // opKind*
	Target string `json:"target"` // node id the action was against
	Actor  string `json:"actor"`  // operator account ("tg:<op>" / username)
	Reason string `json:"reason"` // failure detail (exit=N / err text)
	At     int64  `json:"at"`     // unix seconds
	Status string `json:"status"` // open | dismissed

	// Retry context for mesh-apply (the only action that needs more than the
	// target id to re-dispatch). Empty for the others.
	MeshTargets    []string          `json:"mesh_targets,omitempty"`
	MeshTransports map[string]string `json:"mesh_transports,omitempty"`
	MeshRegion     int               `json:"mesh_region,omitempty"`
}

func opKindLabel(kind string) string {
	switch kind {
	case opKindDecommission:
		return "decommission"
	case opKindRecommission:
		return "recommission"
	case opKindDelete:
		return "delete"
	case opKindMeshApply:
		return "mesh apply"
	case opKindOnboard:
		return "onboard / provision"
	default:
		return kind
	}
}

type opFailureStore struct {
	mu   sync.Mutex
	ring []*opFailure
}

var globalOpFailures = &opFailureStore{}

// Postgres-backed when globalDB != nil (durable across restarts), else the
// in-memory ring (the original behaviour). Every DB path falls back to the
// ring on error so an op-failure is never silently lost.
const opFailureCols = `id,kind,target,actor,reason,at,status,mesh_targets,mesh_transports,mesh_region`

// scanOpFailure reads one row in opFailureCols order (*sql.Row or *sql.Rows).
func scanOpFailure(sc interface{ Scan(...any) error }) (*opFailure, error) {
	f := &opFailure{}
	var mt, mtr []byte
	if err := sc.Scan(&f.ID, &f.Kind, &f.Target, &f.Actor, &f.Reason, &f.At, &f.Status, &mt, &mtr, &f.MeshRegion); err != nil {
		return nil, err
	}
	if len(mt) > 0 {
		_ = json.Unmarshal(mt, &f.MeshTargets)
	}
	if len(mtr) > 0 {
		_ = json.Unmarshal(mtr, &f.MeshTransports)
	}
	return f, nil
}

func (s *opFailureStore) add(f *opFailure) {
	if globalDB != nil {
		mt, _ := json.Marshal(f.MeshTargets)
		mtr, _ := json.Marshal(f.MeshTransports)
		_, err := globalDB.Exec(`INSERT INTO op_failures (`+opFailureCols+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (id) DO NOTHING`,
			f.ID, f.Kind, f.Target, f.Actor, f.Reason, f.At, f.Status, mt, mtr, f.MeshRegion)
		if err == nil {
			return
		}
		log.Printf("opfail: db insert failed (%v) — using in-memory ring", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ring = append(s.ring, f)
	if len(s.ring) > opFailureCap {
		s.ring = s.ring[len(s.ring)-opFailureCap:]
	}
}

func (s *opFailureStore) get(id string) *opFailure {
	if globalDB != nil {
		f, err := scanOpFailure(globalDB.QueryRow(`SELECT `+opFailureCols+` FROM op_failures WHERE id=$1`, id))
		if err == nil {
			return f
		}
		if err == sql.ErrNoRows {
			return nil
		}
		log.Printf("opfail: db get failed (%v) — falling back to ring", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.ring {
		if f.ID == id {
			return f
		}
	}
	return nil
}

// listSnapshot returns a newest-first copy. open=true → only Status=="open".
func (s *opFailureStore) listSnapshot(openOnly bool) []opFailure {
	if globalDB != nil {
		q := `SELECT ` + opFailureCols + ` FROM op_failures`
		if openOnly {
			q += ` WHERE status='open'`
		}
		q += fmt.Sprintf(` ORDER BY at DESC LIMIT %d`, opFailureCap)
		if rows, err := globalDB.Query(q); err == nil {
			defer rows.Close()
			out := make([]opFailure, 0, opFailureCap)
			for rows.Next() {
				f, serr := scanOpFailure(rows)
				if serr != nil {
					out = nil
					break
				}
				out = append(out, *f)
			}
			if out != nil && rows.Err() == nil {
				return out
			}
			log.Printf("opfail: db list scan error — falling back to ring")
		} else {
			log.Printf("opfail: db list failed (%v) — falling back to ring", err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]opFailure, 0, len(s.ring))
	for _, f := range s.ring {
		if openOnly && f.Status != "open" {
			continue
		}
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}

func (s *opFailureStore) dismiss(id string) bool {
	if globalDB != nil {
		if res, err := globalDB.Exec(`UPDATE op_failures SET status='dismissed' WHERE id=$1`, id); err == nil {
			n, _ := res.RowsAffected()
			return n > 0
		} else {
			log.Printf("opfail: db dismiss failed (%v) — falling back to ring", err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.ring {
		if f.ID == id {
			f.Status = "dismissed"
			return true
		}
	}
	return false
}

func (s *opFailureStore) openCount() int {
	if globalDB != nil {
		var n int
		if err := globalDB.QueryRow(`SELECT count(*) FROM op_failures WHERE status='open'`).Scan(&n); err == nil {
			return n
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, f := range s.ring {
		if f.Status == "open" {
			n++
		}
	}
	return n
}

// opNotifyDedupWindow: an identical op-failure seen again within this window is
// not re-notified (still recorded + audited). Saves repeat Agent triages + keeps
// the channel from echoing the same error on retry loops / per-tick failures.
const opNotifyDedupWindow = 30 * time.Minute

var opNotifyDedup = struct {
	mu   sync.Mutex
	last map[string]int64 // signature → last-notified unix
}{last: map[string]int64{}}

// opFailureShouldNotify reports whether this failure should be notified now,
// stamping its signature. Returns false for a repeat within opNotifyDedupWindow.
func opFailureShouldNotify(f *opFailure) bool {
	sig := f.Kind + "|" + f.Target + "|" + f.Reason
	now := time.Now().Unix()
	win := int64(opNotifyDedupWindow.Seconds())
	opNotifyDedup.mu.Lock()
	defer opNotifyDedup.mu.Unlock()
	last, seen := opNotifyDedup.last[sig]
	opNotifyDedup.last[sig] = now
	// Bound the map: drop entries well past the window.
	for k, t := range opNotifyDedup.last {
		if now-t > win*4 {
			delete(opNotifyDedup.last, k)
		}
	}
	return !seen || now-last >= win
}

// recordOpFailure is THE funnel: stamp + store + audit + actionable notify.
// notify may be nil (telegram disabled) — store + audit still happen.
func recordOpFailure(notify *tgNotifier, f *opFailure) *opFailure {
	if f.ID == "" {
		var b [4]byte
		_, _ = rand.Read(b[:])
		f.ID = hex.EncodeToString(b[:])
	}
	if f.At == 0 {
		f.At = time.Now().Unix()
	}
	if f.Status == "" {
		f.Status = "open"
	}
	globalOpFailures.add(f)
	auditRecord(nil, AuditEvent{
		Event: "opfail." + f.Kind, Severity: auditSevWarn, Actor: f.Actor,
		Target: f.Target, Outcome: "fail",
		Details: map[string]any{"reason": f.Reason},
	})
	// Dedup repeats: an identical failure (same kind+target+reason) within the
	// window is still stored + audited for history, but NOT re-notified — so we
	// don't re-run the Agent triage (token cost) or repeat the same card in the
	// channel. Report once per window.
	if opFailureShouldNotify(f) {
		notify.triageOpFailure(f) // Agent triages → channel (always) + group (if needs-fix); nil-safe
	} else {
		log.Printf("opfail: deduped repeat notify kind=%s target=%s (within %s)", f.Kind, f.Target, opNotifyDedupWindow)
	}
	return f
}

// handleDebugTestOpFail — gated trigger to exercise the op-failure → Agent
// triage → error-channel flow end to end on the LIVE server. Disabled (404)
// unless NCN_DEBUG_OPFAIL=1; meant to be curled from localhost on tyo, then
// turned back off. Injects a synthetic failure through the real recordOpFailure.
func handleDebugTestOpFail(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("NCN_DEBUG_OPFAIL") != "1" {
		http.NotFound(w, r)
		return
	}
	reason := r.URL.Query().Get("reason")
	if reason == "" {
		reason = "test: dial tcp 203.0.113.9:22: connect: connection refused"
	}
	target := r.URL.Query().Get("target")
	if target == "" {
		target = "pop-05"
	}
	f := recordOpFailure(globalNotify, &opFailure{Kind: opKindOnboard, Target: target, Actor: "debug-test", Reason: reason})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"id": f.ID, "note": "triage running async — watch the error channel",
	}})
}

// GET /api/v1/auth/op-failures?open=1&limit=N — admin query of the failure log.
func handleOpFailures(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET only"})
		return
	}
	openOnly := r.URL.Query().Get("open") == "1"
	list := globalOpFailures.listSnapshot(openOnly)
	if s := r.URL.Query().Get("limit"); s != "" {
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 && n < len(list) {
			list = list[:n]
		}
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"failures": list,
		"open":     globalOpFailures.openCount(),
	}})
}
