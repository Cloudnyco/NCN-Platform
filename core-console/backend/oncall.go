// On-call rotation + escalation.
//
// The alert engine could auto-bump a sub-crit alert to crit (EscalateSecs) but
// it never PAGED a specific human. This adds a weekly on-call rotation and an
// escalation policy: an alert that stays firing + UNACKNOWLEDGED past a tier's
// threshold pages that tier — DM the current on-call, then DM all admins, then
// post to the group. "Pick up" = acknowledge the alert in the console (the
// existing ack already stops escalation); there's a /oncall bot command to ask
// who's on duty. Routing reuses the Telegram notifier (DM = the operator's bound
// telegram chat id). Per-alert escalation state is in-memory (resets on restart).

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

var globalOncall *oncallStore

const (
	oncallPath           = incidentsDir + "/oncall.json"
	oncallCheckInterval  = 60 * time.Second
	oncallDefaultPeriodD = 7
)

type escalationTier struct {
	AfterMin int    `json:"after_min"` // minutes firing+unacked before this tier pages
	Target   string `json:"target"`    // "oncall" | "admins" | "group"
}

type oncallConfig struct {
	Rotation   []string         `json:"rotation"`    // operator usernames, in shift order
	StartDate  string           `json:"start_date"`  // YYYY-MM-DD UTC anchor for the rotation
	PeriodDays int              `json:"period_days"` // days per shift (default 7)
	Tiers      []escalationTier `json:"tiers"`       // escalation chain (ascending AfterMin)
}

type oncallStore struct {
	mu     sync.Mutex
	cfg    oncallConfig
	engine *alertEngine
	notify *tgNotifier
	auth   *authStore
	sent   map[string]int // alertID -> number of tiers already paged
}

func newOncallStore(engine *alertEngine, notify *tgNotifier, auth *authStore) *oncallStore {
	s := &oncallStore{engine: engine, notify: notify, auth: auth, sent: map[string]int{}}
	s.load()
	return s
}

func (s *oncallStore) load() {
	var doc []byte
	if globalDB != nil {
		if b, err := loadConfigDoc("oncall"); err == nil && b != nil {
			doc = b
		}
	}
	if doc == nil {
		if b, err := os.ReadFile(oncallPath); err == nil && len(b) > 0 {
			doc = b
		}
	}
	if doc != nil {
		_ = json.Unmarshal(doc, &s.cfg)
	}
	if s.cfg.PeriodDays <= 0 {
		s.cfg.PeriodDays = oncallDefaultPeriodD
	}
}

func (s *oncallStore) persistLocked() {
	b, err := json.Marshal(s.cfg)
	if err != nil {
		return
	}
	writeFileAtomic(oncallPath, b)
	if globalDB != nil {
		if err := saveConfigDoc("oncall", b); err != nil {
			log.Printf("oncall: db persist failed (%v) — file is current", err)
		}
	}
}

// currentOncall returns the operator on duty now per the rotation, or "".
func (s *oncallStore) currentOncall(now time.Time) string {
	if len(s.cfg.Rotation) == 0 {
		return ""
	}
	period := s.cfg.PeriodDays
	if period <= 0 {
		period = oncallDefaultPeriodD
	}
	start, err := time.Parse("2006-01-02", s.cfg.StartDate)
	if err != nil {
		start = time.Unix(0, 0).UTC() // epoch anchor if unset/bad
	}
	days := int(now.UTC().Sub(start).Hours() / 24)
	if days < 0 {
		days = 0
	}
	idx := (days / period) % len(s.cfg.Rotation)
	return s.cfg.Rotation[idx]
}

// ── escalation loop ──────────────────────────────────────────────────────────

func (s *oncallStore) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(oncallCheckInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.tick()
			}
		}
	}()
}

func (s *oncallStore) tick() {
	if s.engine == nil || s.notify == nil {
		return
	}
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if len(cfg.Tiers) == 0 {
		return
	}
	now := time.Now()
	active := s.engine.activeUnacked()
	live := map[string]bool{}
	for _, ev := range active {
		live[ev.ID] = true
		ageMin := int(now.Sub(time.Unix(ev.FiredAt, 0)).Minutes())
		// highest tier whose threshold the alert has passed
		target := -1
		for i, t := range cfg.Tiers {
			if ageMin >= t.AfterMin {
				target = i
			}
		}
		if target < 0 {
			continue
		}
		s.mu.Lock()
		already := s.sent[ev.ID]
		if already >= target+1 {
			s.mu.Unlock()
			continue
		}
		s.sent[ev.ID] = target + 1
		s.mu.Unlock()
		s.page(ev, cfg.Tiers[target], ageMin, now)
	}
	// drop escalation state for alerts that resolved/acked
	s.mu.Lock()
	for id := range s.sent {
		if !live[id] {
			delete(s.sent, id)
		}
	}
	s.mu.Unlock()
}

func (s *oncallStore) page(ev alertEvent, tier escalationTier, ageMin int, now time.Time) {
	text := fmt.Sprintf("🆘 <b>升级</b> · %s\n<b>%s</b> @ %s 已触发 %d 分钟未确认\n%s\n<blockquote>在控制台 ack 此告警即可停止升级。</blockquote>",
		strings.ToUpper(string(ev.Severity)), html.EscapeString(ev.Title), ev.NodeID, ageMin, html.EscapeString(ev.Message))

	switch tier.Target {
	case "group":
		ch := s.notify.chatID
		s.notify.enqueue(tgPayload{ChatID: ch, Text: text}, "escalate-group-"+ev.ID)
	case "admins":
		for _, u := range s.auth.adminUsernames() {
			if chat, ok := s.auth.telegramChatFor(u); ok {
				s.notify.enqueue(tgPayload{ChatID: chat, Text: text}, "escalate-admin-"+u+"-"+ev.ID)
			}
		}
	default: // "oncall"
		who := s.currentOncall(now)
		if who == "" {
			// no rotation configured → fall back to the group so it isn't lost
			s.notify.enqueue(tgPayload{ChatID: s.notify.chatID, Text: text + "\n(无值班轮转,已发群)"}, "escalate-nooncall-"+ev.ID)
			return
		}
		if chat, ok := s.auth.telegramChatFor(who); ok {
			s.notify.enqueue(tgPayload{ChatID: chat, Text: "👤 你正在值班\n" + text}, "escalate-oncall-"+ev.ID)
		} else {
			s.notify.enqueue(tgPayload{ChatID: s.notify.chatID, Text: text + fmt.Sprintf("\n(值班 %s 未绑定 Telegram,已发群)", who)}, "escalate-oncall-unbound-"+ev.ID)
		}
	}
	log.Printf("oncall: escalated alert %s (%s/%s, %dm) → tier %s", ev.ID, ev.NodeID, ev.RuleID, ageMin, tier.Target)
}

// statusText is the /oncall bot reply: who's on duty + the rotation.
func (s *oncallStore) statusText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cfg.Rotation) == 0 {
		return "未配置值班轮转。"
	}
	now := time.Now()
	cur := ""
	if len(s.cfg.Rotation) > 0 {
		period := s.cfg.PeriodDays
		if period <= 0 {
			period = oncallDefaultPeriodD
		}
		start, err := time.Parse("2006-01-02", s.cfg.StartDate)
		if err != nil {
			start = time.Unix(0, 0).UTC()
		}
		days := int(now.UTC().Sub(start).Hours() / 24)
		if days < 0 {
			days = 0
		}
		cur = s.cfg.Rotation[(days/period)%len(s.cfg.Rotation)]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "👤 当前值班: <b>%s</b>\n轮转(每 %d 天): %s", html.EscapeString(cur), s.cfg.PeriodDays, html.EscapeString(strings.Join(s.cfg.Rotation, " → ")))
	if len(s.cfg.Tiers) > 0 {
		b.WriteString("\n升级策略:")
		for _, t := range s.cfg.Tiers {
			fmt.Fprintf(&b, " %dm→%s", t.AfterMin, t.Target)
		}
	}
	return b.String()
}

// ── HTTP ─────────────────────────────────────────────────────────────────────

// GET /api/v1/auth/oncall → config + who's on now + the operator list (for the editor).
func handleOncall(w http.ResponseWriter, _ *http.Request) {
	if globalOncall == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "oncall not ready"})
		return
	}
	globalOncall.mu.Lock()
	cfg := globalOncall.cfg
	globalOncall.mu.Unlock()
	cur := globalOncall.currentOncall(time.Now())
	var ops []string
	if globalOncall.auth != nil {
		ops = globalOncall.auth.listOperatorNames()
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"config": cfg, "current": cur, "operators": ops}})
}

// POST /api/v1/auth/oncall → replace the rotation + escalation policy.
func handleOncallSet(w http.ResponseWriter, r *http.Request) {
	if globalOncall == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "oncall not ready"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var cfg oncallConfig
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<18)).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad body"})
		return
	}
	if cfg.PeriodDays <= 0 {
		cfg.PeriodDays = oncallDefaultPeriodD
	}
	// keep tiers sorted ascending by AfterMin
	sort.SliceStable(cfg.Tiers, func(i, j int) bool { return cfg.Tiers[i].AfterMin < cfg.Tiers[j].AfterMin })
	for i := range cfg.Tiers {
		switch cfg.Tiers[i].Target {
		case "oncall", "admins", "group":
		default:
			cfg.Tiers[i].Target = "oncall"
		}
	}
	globalOncall.mu.Lock()
	globalOncall.cfg = cfg
	globalOncall.persistLocked()
	globalOncall.mu.Unlock()
	auditRecord(r, AuditEvent{Event: "oncall.set", Severity: "info", Actor: adminOperator(r), Outcome: "ok"})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"config": cfg, "current": globalOncall.currentOncall(time.Now())}})
}

// ── authStore helpers (on-call needs operator → telegram chat + the roster) ──

func (s *authStore) telegramChatFor(username string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	op, ok := s.operators[username]
	if !ok {
		return "", false
	}
	for _, ei := range op.ExternalIdentities {
		if ei.Provider == "telegram" && ei.Subject != "" {
			return ei.Subject, true
		}
	}
	return "", false
}

func (s *authStore) listOperatorNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.operators))
	for name := range s.operators {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (s *authStore) adminUsernames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for name, op := range s.operators {
		if op.Role == "admin" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
