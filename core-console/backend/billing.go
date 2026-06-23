// Billing — VPS subscription tracker.
//
// Scope: track our OWN outgoing VPS rent so we don't (a) lose visibility
// of total monthly spend and (b) forget to renew before a provider
// auto-suspends a PoP. NOT a customer billing system — there's no
// payment processing, no Stripe, no invoice PDFs. Just bookkeeping.
//
// Storage: single JSON file at /var/log/ncn-billing/subscriptions.json,
// rewritten atomically on mutation. Same pattern as incidents.go —
// admin write rate is human-keystroke pace, no real contention.
//
// Endpoints (all admin-only):
//
//   GET    /api/v1/auth/billing/subscriptions          list
//   POST   /api/v1/auth/billing/subscriptions          create
//   PATCH  /api/v1/auth/billing/subscriptions/{id}     edit
//   DELETE /api/v1/auth/billing/subscriptions/{id}     remove
//   POST   /api/v1/auth/billing/subscriptions/{id}/paid
//          → record a payment + auto-bump next_due by billing_cycle
//
// Alert: rule "vps-renewal-soon" fires when ANY subscription's next_due
// is within 7 days (warn) or 1 day (critical). Wires into the existing
// Telegram notifier so renewals show up in the ops chat.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	billingDir  = "/var/log/ncn-billing"
	billingPath = billingDir + "/subscriptions.json"

	// Renewal alert thresholds (days from now to next_due).
	renewalWarnDays = 7
	renewalCritDays = 1

	// Billing cycle values. Stored as strings so the JSON file stays
	// readable; converting at the UI level keeps the backend dumb.
	cycleMonthly   = "monthly"
	cycleQuarterly = "quarterly"
	cycleYearly    = "yearly"
)

// VPSSubscription is the on-disk + wire shape.
type VPSSubscription struct {
	ID           string    `json:"id"`
	Label        string    `json:"label"`             // human handle e.g. "Region D VPS (Cyberjet)"
	Provider     string    `json:"provider"`          // "Cyberjet", "DataSphere", "OVH", etc.
	NodeID       string    `json:"node_id,omitempty"` // optional: maps to fleetNode.ID
	MonthlyCost  float64   `json:"monthly_cost"`      // amount per month (caller normalises if quarterly/yearly)
	Currency     string    `json:"currency"`          // ISO code: USD/HKD/CNY/JPY/EUR/SGD/...
	BillingCycle string    `json:"billing_cycle"`     // monthly | quarterly | yearly
	NextDue      time.Time `json:"next_due"`
	PortalURL    string    `json:"portal_url,omitempty"` // provider's billing/customer portal URL
	Notes        string    `json:"notes,omitempty"`
	Payments     []Payment `json:"payments,omitempty"` // history (newest first appended)
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Payment is one entry in a subscription's payment history.
type Payment struct {
	PaidAt   time.Time `json:"paid_at"`
	Amount   float64   `json:"amount"`        // actual amount paid (may differ from MonthlyCost due to FX, tax, multi-period)
	Currency string    `json:"currency"`
	Note     string    `json:"note,omitempty"` // e.g. "covers Jan-Mar" or "FX rate locked at 7.82"
	By       string    `json:"by"`             // operator who recorded the payment
}

type billingStore struct {
	mu   sync.RWMutex
	subs []*VPSSubscription
}

var globalBilling *billingStore

func newBillingStore() (*billingStore, error) {
	if err := os.MkdirAll(billingDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", billingDir, err)
	}
	s := &billingStore{subs: []*VPSSubscription{}}

	// Prefer Postgres when it already holds the document (post-cutover).
	loadedFromDB := false
	if globalDB != nil {
		if doc, err := loadConfigDoc("billing"); err != nil {
			log.Printf("billing: db load failed (%v) — falling back to file", err)
		} else if doc != nil {
			if err := json.Unmarshal(doc, &s.subs); err != nil {
				return nil, fmt.Errorf("parse db doc: %w", err)
			}
			loadedFromDB = true
		}
	}

	// Otherwise load the JSON file if present.
	if !loadedFromDB {
		b, err := os.ReadFile(billingPath)
		if err == nil && len(b) > 0 {
			if err := json.Unmarshal(b, &s.subs); err != nil {
				return nil, fmt.Errorf("parse %s: %w", billingPath, err)
			}
		}
	}

	// Migrate file→DB on the first DB-enabled boot.
	if globalDB != nil && !loadedFromDB {
		s.mu.Lock()
		err := s.persist()
		s.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("migrate billing to db: %w", err)
		}
	}
	return s, nil
}

// persist serialises under the caller's locked mu, then dual-writes the same
// document into Postgres when available (the file stays the durable backup +
// globalDB==nil path; a DB error is non-fatal).
func (s *billingStore) persist() error {
	b, err := json.MarshalIndent(s.subs, "", "  ")
	if err != nil {
		return err
	}
	tmp := billingPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, billingPath); err != nil {
		return err
	}
	if globalDB != nil {
		if err := saveConfigDoc("billing", b); err != nil {
			log.Printf("billing: db persist failed (%v) — file is current", err)
		}
	}
	return nil
}

// listSnapshot returns a sorted (asc by NextDue — soonest renewals first)
// copy under the read lock.
func (s *billingStore) listSnapshot() []*VPSSubscription {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*VPSSubscription, len(s.subs))
	copy(out, s.subs)
	sort.Slice(out, func(i, j int) bool {
		return out[i].NextDue.Before(out[j].NextDue)
	})
	return out
}

// ────────────────────────────── helpers ──────────────────────────────

func validCycle(c string) bool {
	switch c {
	case cycleMonthly, cycleQuarterly, cycleYearly:
		return true
	}
	return false
}

// bumpNextDue advances NextDue by one billing cycle. Used by mark-paid.
// If the previous NextDue was in the past (overdue), the bump is from
// THAT date — so two consecutive overdue payments don't compress the
// schedule. For very stale entries we step until the new NextDue is in
// the future (preserves the original day-of-month).
func bumpNextDue(t time.Time, cycle string) time.Time {
	add := func(t time.Time) time.Time {
		switch cycle {
		case cycleYearly:
			return t.AddDate(1, 0, 0)
		case cycleQuarterly:
			return t.AddDate(0, 3, 0)
		default: // monthly
			return t.AddDate(0, 1, 0)
		}
	}
	next := add(t)
	now := time.Now()
	for next.Before(now) {
		next = add(next)
	}
	return next
}

// MonthlyEquivalent returns the cost normalised to a per-month figure
// (yearly / 12, quarterly / 3, monthly as-is). Used by the UI rollup.
func (sub *VPSSubscription) MonthlyEquivalent() float64 {
	switch sub.BillingCycle {
	case cycleYearly:
		return sub.MonthlyCost / 12.0
	case cycleQuarterly:
		return sub.MonthlyCost / 3.0
	}
	return sub.MonthlyCost
}

// ────────────────────────────── HTTP handlers ──────────────────────────────

func handleBillingRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		handleBillingList(w, r)
	case http.MethodPost:
		handleBillingCreate(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
	}
}

// Sub-path dispatcher: /api/v1/auth/billing/subscriptions/{id}[/paid]
func handleBillingItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/billing/subscriptions/")
	switch {
	case strings.HasSuffix(rest, "/paid") && r.Method == http.MethodPost:
		handleBillingMarkPaid(w, r)
	case !strings.Contains(rest, "/") && r.Method == http.MethodPatch:
		handleBillingPatch(w, r)
	case !strings.Contains(rest, "/") && r.Method == http.MethodDelete:
		handleBillingDelete(w, r)
	default:
		w.Header().Set("Allow", "PATCH, DELETE, POST")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method/path not allowed"})
	}
}

func handleBillingList(w http.ResponseWriter, r *http.Request) {
	if globalBilling == nil {
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: []*VPSSubscription{}})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: globalBilling.listSnapshot()})
}

func handleBillingCreate(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	var req struct {
		Label        string    `json:"label"`
		Provider     string    `json:"provider"`
		NodeID       string    `json:"node_id,omitempty"`
		MonthlyCost  float64   `json:"monthly_cost"`
		Currency     string    `json:"currency"`
		BillingCycle string    `json:"billing_cycle"`
		NextDue      time.Time `json:"next_due"`
		PortalURL    string    `json:"portal_url,omitempty"`
		Notes        string    `json:"notes,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json: " + err.Error()})
		return
	}
	req.Label = strings.TrimSpace(req.Label)
	req.Provider = strings.TrimSpace(req.Provider)
	req.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
	if req.BillingCycle == "" {
		req.BillingCycle = cycleMonthly
	}
	if req.Label == "" || req.Provider == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "label + provider required"})
		return
	}
	if req.MonthlyCost <= 0 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "monthly_cost must be > 0"})
		return
	}
	if req.Currency == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "currency required (USD/HKD/CNY/...)"})
		return
	}
	if !validCycle(req.BillingCycle) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "billing_cycle must be monthly|quarterly|yearly"})
		return
	}
	if req.NextDue.IsZero() {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "next_due required"})
		return
	}

	now := time.Now().UTC()
	sub := &VPSSubscription{
		ID:           newIncidentID(), // reuse the 16-hex generator; collision odds neg.
		Label:        req.Label,
		Provider:     req.Provider,
		NodeID:       req.NodeID,
		MonthlyCost:  req.MonthlyCost,
		Currency:     req.Currency,
		BillingCycle: req.BillingCycle,
		NextDue:      req.NextDue,
		PortalURL:    req.PortalURL,
		Notes:        req.Notes,
		CreatedBy:    op,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	globalBilling.mu.Lock()
	globalBilling.subs = append(globalBilling.subs, sub)
	err := globalBilling.persist()
	globalBilling.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	auditRecord(r, AuditEvent{
		Event: "billing.create", Severity: auditSevInfo, Actor: op,
		Target: sub.ID, Details: map[string]any{"label": sub.Label, "provider": sub.Provider, "currency": sub.Currency},
	})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: sub})
}

func handleBillingPatch(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/billing/subscriptions/")
	var req struct {
		Label        *string    `json:"label,omitempty"`
		Provider     *string    `json:"provider,omitempty"`
		NodeID       *string    `json:"node_id,omitempty"`
		MonthlyCost  *float64   `json:"monthly_cost,omitempty"`
		Currency     *string    `json:"currency,omitempty"`
		BillingCycle *string    `json:"billing_cycle,omitempty"`
		NextDue      *time.Time `json:"next_due,omitempty"`
		PortalURL    *string    `json:"portal_url,omitempty"`
		Notes        *string    `json:"notes,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	globalBilling.mu.Lock()
	defer globalBilling.mu.Unlock()
	for _, sub := range globalBilling.subs {
		if sub.ID == id {
			if req.Label != nil {
				sub.Label = strings.TrimSpace(*req.Label)
			}
			if req.Provider != nil {
				sub.Provider = strings.TrimSpace(*req.Provider)
			}
			if req.NodeID != nil {
				sub.NodeID = *req.NodeID
			}
			if req.MonthlyCost != nil {
				if *req.MonthlyCost <= 0 {
					writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "monthly_cost must be > 0"})
					return
				}
				sub.MonthlyCost = *req.MonthlyCost
			}
			if req.Currency != nil {
				sub.Currency = strings.ToUpper(strings.TrimSpace(*req.Currency))
			}
			if req.BillingCycle != nil {
				if !validCycle(*req.BillingCycle) {
					writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid billing_cycle"})
					return
				}
				sub.BillingCycle = *req.BillingCycle
			}
			if req.NextDue != nil {
				sub.NextDue = *req.NextDue
			}
			if req.PortalURL != nil {
				sub.PortalURL = *req.PortalURL
			}
			if req.Notes != nil {
				sub.Notes = *req.Notes
			}
			sub.UpdatedAt = time.Now().UTC()
			if err := globalBilling.persist(); err != nil {
				writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
				return
			}
			auditRecord(r, AuditEvent{
				Event: "billing.update", Severity: auditSevInfo, Actor: op,
				Target: id,
			})
			writeJSON(w, http.StatusOK, envelope{OK: true, Data: sub})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "not found"})
}

func handleBillingDelete(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/billing/subscriptions/")
	globalBilling.mu.Lock()
	defer globalBilling.mu.Unlock()
	for i, sub := range globalBilling.subs {
		if sub.ID == id {
			deleted := sub
			globalBilling.subs = append(globalBilling.subs[:i], globalBilling.subs[i+1:]...)
			if err := globalBilling.persist(); err != nil {
				writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
				return
			}
			auditRecord(r, AuditEvent{
				Event: "billing.delete", Severity: auditSevWarn, Actor: op,
				Target: id, Details: map[string]any{"label": deleted.Label},
			})
			writeJSON(w, http.StatusOK, envelope{OK: true})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "not found"})
}

// POST /api/v1/auth/billing/subscriptions/{id}/paid
//
// Body: { "amount"?: float, "currency"?: string, "note"?: string }
//
// Records a payment in the subscription's history and advances NextDue
// by one billing cycle. Amount/currency default to MonthlyCost/Currency.
func handleBillingMarkPaid(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/billing/subscriptions/")
	id := strings.TrimSuffix(rest, "/paid")
	var req struct {
		Amount   *float64 `json:"amount,omitempty"`
		Currency string   `json:"currency,omitempty"`
		Note     string   `json:"note,omitempty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req) // body is optional

	globalBilling.mu.Lock()
	defer globalBilling.mu.Unlock()
	for _, sub := range globalBilling.subs {
		if sub.ID == id {
			amt := sub.MonthlyCost
			if req.Amount != nil {
				amt = *req.Amount
			}
			ccy := sub.Currency
			if c := strings.ToUpper(strings.TrimSpace(req.Currency)); c != "" {
				ccy = c
			}
			pay := Payment{
				PaidAt: time.Now().UTC(), Amount: amt, Currency: ccy,
				Note: strings.TrimSpace(req.Note), By: op,
			}
			// Newest-first: prepend.
			sub.Payments = append([]Payment{pay}, sub.Payments...)
			sub.NextDue = bumpNextDue(sub.NextDue, sub.BillingCycle)
			sub.UpdatedAt = time.Now().UTC()
			if err := globalBilling.persist(); err != nil {
				writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
				return
			}
			auditRecord(r, AuditEvent{
				Event: "billing.paid", Severity: auditSevInfo, Actor: op,
				Target: id, Details: map[string]any{"amount": amt, "currency": ccy, "next_due": sub.NextDue},
			})
			writeJSON(w, http.StatusOK, envelope{OK: true, Data: sub})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "not found"})
}

// BillingRenewalState answers the alert rule: returns the most-urgent
// subscription whose next_due is within the alert window, or nil if
// nothing is due soon.
func BillingRenewalState() (subs []*VPSSubscription, mostUrgent *VPSSubscription, daysLeft int) {
	if globalBilling == nil {
		return nil, nil, 0
	}
	now := time.Now()
	cutoff := now.AddDate(0, 0, renewalWarnDays)
	all := globalBilling.listSnapshot()
	for _, sub := range all {
		if sub.NextDue.Before(cutoff) {
			subs = append(subs, sub)
			d := int(time.Until(sub.NextDue).Hours() / 24)
			if mostUrgent == nil || d < daysLeft {
				mostUrgent = sub
				daysLeft = d
			}
		}
	}
	return
}

// startBillingRenewalNotifier launches a goroutine that emits a daily TG
// digest of VPS subscriptions due within the next 7 days. Runs once at
// startup and then every 24h. tg may be nil — in that case we just log
// and skip the actual send, the admin can still see status in the web UI.
//
// One digest per day is enough: more frequent reminders become noise
// and the operator usually only checks TG once or twice a day anyway.
func startBillingRenewalNotifier(ctx context.Context, tg *tgNotifier) {
	go func() {
		// Wait 30s before the first run so we don't race against the
		// initial fleet scrape and the startup banner — keeps the TG
		// channel from getting three messages in five seconds.
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		runBillingRenewalCheck(tg)
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				runBillingRenewalCheck(tg)
			}
		}
	}()
}

func runBillingRenewalCheck(tg *tgNotifier) {
	subs, _, _ := BillingRenewalState()
	if len(subs) == 0 {
		return
	}
	// Split into critical (<=1 day) and warning (2..7 days) buckets so
	// the digest reads with urgency at the top.
	var critical, warning []*VPSSubscription
	for _, sub := range subs {
		days := int(time.Until(sub.NextDue).Hours() / 24)
		if days <= renewalCritDays {
			critical = append(critical, sub)
		} else {
			warning = append(warning, sub)
		}
	}
	log.Printf("billing: renewal digest — %d critical, %d warning", len(critical), len(warning))

	if tg == nil {
		return
	}
	var sb strings.Builder
	if len(critical) > 0 {
		sb.WriteString("🚨 <b>VPS renewal due NOW</b>\n")
		for _, sub := range critical {
			days := int(time.Until(sub.NextDue).Hours() / 24)
			fmt.Fprintf(&sb, "  • %s (%s) — %d day(s) · %.2f %s\n",
				htmlEscapeStr(sub.Label), htmlEscapeStr(sub.Provider),
				days, sub.MonthlyCost, sub.Currency)
		}
		sb.WriteString("\n")
	}
	if len(warning) > 0 {
		sb.WriteString("⏰ <b>VPS renewal coming up</b>\n")
		for _, sub := range warning {
			days := int(time.Until(sub.NextDue).Hours() / 24)
			fmt.Fprintf(&sb, "  • %s (%s) — in %d days · %.2f %s\n",
				htmlEscapeStr(sub.Label), htmlEscapeStr(sub.Provider),
				days, sub.MonthlyCost, sub.Currency)
		}
	}
	tg.enqueue(tgPayload{Text: sb.String()}, "billing renewal digest")
}

// htmlEscapeStr is a tiny HTML escaper for the Telegram payload (which
// is sent with parse_mode=HTML by the notifier). Mirrors what audit.go's
// formatters do — small enough to redeclare locally and keep billing
// independent of those callers.
func htmlEscapeStr(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
