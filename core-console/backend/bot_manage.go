// In-chat alert-rule management — the ⚙ inline menus hung under bot messages.
//
// A "⚙ 管理规则" button rides every crit alert; /manage opens the same menu.
// Tapping drives a small callback-query state machine (root → group → rule)
// with write actions (toggle enable, toggle Telegram, enable/mute a whole
// group) against the persistent rule store. Each action reloads the engine
// and is audited. Gated to the configured ops group (handleUpdate checks the
// chat before dispatching here) — the same trust boundary as the read
// commands. Rule AUTHORING (new custom rules, thresholds) still lives on the
// console page; the bot does the fast knobs: silence the noise from chat.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func nowUnix() int64 { return time.Now().Unix() }

type tgKbBtn struct {
	Text string `json:"text"`
	Data string `json:"callback_data,omitempty"` // callback button …
	URL  string `json:"url,omitempty"`           // … or a link button (mutually exclusive)
}

func kbMarkup(rows [][]tgKbBtn) map[string]any { return map[string]any{"inline_keyboard": rows} }

// manageRulesURL is the console alert-rules page — rule management lives there
// now, not in chat.
func manageRulesURL() string {
	base := strings.TrimRight(getenvDefault("NCN_OAUTH_REDIRECT_BASE", "https://admin.example.com"), "/")
	return base + "/admin/alert_rules"
}


func (n *tgNotifier) rstore() *alertRuleStore {
	if n == nil || n.engine == nil {
		return nil
	}
	return n.engine.ruleStore
}

func mlabel(metric string) string {
	for _, m := range metricCatalog {
		if m.Key == metric {
			return m.Label
		}
	}
	return metric
}

func onoff(b bool) string {
	if b {
		return "✅"
	}
	return "⛔"
}

// ── views: each returns (HTML text, inline keyboard) ────────────────────────

func (n *tgNotifier) manageRootView() (string, map[string]any) {
	s := n.rstore()
	groups, rules := s.snapshot()
	sortGroupsRules(groups, rules)
	rows := [][]tgKbBtn{}
	for _, g := range groups {
		cnt := 0
		for _, r := range rules {
			if r.GroupID == g.ID {
				cnt++
			}
		}
		tag := ""
		if !g.Enabled {
			tag = " ⛔off"
		} else if g.MuteUntil > nowUnix() {
			tag = " 🔕muted"
		}
		rows = append(rows, []tgKbBtn{{Text: fmt.Sprintf("%s · %d rules%s", g.Name, cnt, tag), Data: "mg:g:" + g.ID}})
	}
	return "<b>⚙ Alert rule management</b>\nPick a rule group (toggle / mute / TG inside):", kbMarkup(rows)
}

func (n *tgNotifier) manageGroupView(gid string) (string, map[string]any) {
	s := n.rstore()
	groups, rules := s.snapshot()
	var g *ruleGroup
	for i := range groups {
		if groups[i].ID == gid {
			g = &groups[i]
			break
		}
	}
	if g == nil {
		return n.manageRootView()
	}
	muted := g.MuteUntil > nowUnix()
	scope := "all nodes"
	parts := []string{}
	if len(g.NodeIDs) > 0 {
		parts = append(parts, strings.Join(g.NodeIDs, ","))
	}
	if len(g.Regions) > 0 {
		rs := make([]string, len(g.Regions))
		for i, r := range g.Regions {
			rs[i] = strconv.Itoa(r)
		}
		parts = append(parts, "regions "+strings.Join(rs, ","))
	}
	if len(parts) > 0 {
		scope = strings.Join(parts, " · ")
	}
	text := fmt.Sprintf("<b>Group · %s</b>\nscope: <code>%s</code>\nstatus: %s%s",
		html.EscapeString(g.Name), html.EscapeString(scope),
		map[bool]string{true: "enabled", false: "disabled"}[g.Enabled],
		map[bool]string{true: " · 🔕muted", false: ""}[muted])

	rows := [][]tgKbBtn{}
	enLabel := "Disable group"
	if !g.Enabled {
		enLabel = "Enable group"
	}
	muteBtn := tgKbBtn{Text: "🔕 Mute 1h", Data: "mg:gm:" + g.ID + ":60"}
	if muted {
		muteBtn = tgKbBtn{Text: "🔔 Unmute", Data: "mg:gm:" + g.ID + ":0"}
	}
	rows = append(rows, []tgKbBtn{{Text: enLabel, Data: "mg:gt:" + g.ID}, muteBtn})
	for _, r := range rules {
		if r.GroupID != g.ID {
			continue
		}
		tg := ""
		if !r.NotifyTG {
			tg = " 🔕"
		}
		rows = append(rows, []tgKbBtn{{Text: fmt.Sprintf("%s %s%s", onoff(r.Enabled), r.Name, tg), Data: "mg:r:" + r.ID}})
	}
	rows = append(rows, []tgKbBtn{{Text: "⬅ Back", Data: "mg:root"}})
	return text, kbMarkup(rows)
}

func (n *tgNotifier) manageRuleView(rid string) (string, map[string]any) {
	s := n.rstore()
	_, rules := s.snapshot()
	var r *ruleDef
	for i := range rules {
		if rules[i].ID == rid {
			r = &rules[i]
			break
		}
	}
	if r == nil {
		return n.manageRootView()
	}
	sym := alertOpSymbol[r.Op]
	text := fmt.Sprintf("<b>%s</b> <code>%s</code>\ncond: <code>%s %s %s</code>\nseverity: %s · group: <code>%s</code>\nenabled: %s · TG: %s%s",
		html.EscapeString(r.Name), html.EscapeString(r.ID),
		html.EscapeString(mlabel(r.Metric)), sym, fmtNum(r.Threshold, 2),
		string(r.Severity), html.EscapeString(r.GroupID),
		onoff(r.Enabled), onoff(r.NotifyTG),
		map[bool]string{true: " · builtin", false: ""}[r.Builtin])

	enLabel := "Disable this rule"
	if !r.Enabled {
		enLabel = "Enable this rule"
	}
	tgLabel := "Turn off TG push"
	if !r.NotifyTG {
		tgLabel = "Turn on TG push"
	}
	rows := [][]tgKbBtn{
		{{Text: enLabel, Data: "mg:re:" + r.ID}, {Text: tgLabel, Data: "mg:rt:" + r.ID}},
		{{Text: "🔕 Mute this group 1h", Data: "mg:gm:" + r.GroupID + ":60"}},
		{{Text: "⬅ Back to group", Data: "mg:g:" + r.GroupID}},
	}
	return text, kbMarkup(rows)
}

// replyManageRoot is the /manage command entry — sends a fresh menu message.
func (n *tgNotifier) replyManageRoot() {
	n.reply("⚙ Alert rule management lives in the console:\n" + manageRulesURL())
}

// ── callback dispatch ───────────────────────────────────────────────────────

func (n *tgNotifier) handleManageCallback(ctx context.Context, cbID, chat string, msgID int64, data, user string) {
	s := n.rstore()
	if s == nil {
		n.answerCallback(ctx, cbID, "rule store not ready")
		return
	}
	toast := ""
	switch {
	case data == "mg:root":
	case strings.HasPrefix(data, "mg:g:"):
		// just navigate
	case strings.HasPrefix(data, "mg:r:"):
		// just navigate
	case strings.HasPrefix(data, "mg:re:"):
		rid := strings.TrimPrefix(data, "mg:re:")
		toast = n.toggleRuleField(rid, "enabled", user)
		data = "mg:r:" + rid
	case strings.HasPrefix(data, "mg:rt:"):
		rid := strings.TrimPrefix(data, "mg:rt:")
		toast = n.toggleRuleField(rid, "notify_tg", user)
		data = "mg:r:" + rid
	case strings.HasPrefix(data, "mg:gt:"):
		gid := strings.TrimPrefix(data, "mg:gt:")
		toast = n.toggleGroupEnabled(gid, user)
		data = "mg:g:" + gid
	case strings.HasPrefix(data, "mg:gm:"):
		rest := strings.TrimPrefix(data, "mg:gm:")
		gid, mins, _ := strings.Cut(rest, ":")
		toast = n.muteGroup(gid, mins, user)
		data = "mg:g:" + gid
	default:
		n.answerCallback(ctx, cbID, "?")
		return
	}

	var text string
	var mk map[string]any
	switch {
	case strings.HasPrefix(data, "mg:g:"):
		text, mk = n.manageGroupView(strings.TrimPrefix(data, "mg:g:"))
	case strings.HasPrefix(data, "mg:r:"):
		text, mk = n.manageRuleView(strings.TrimPrefix(data, "mg:r:"))
	default:
		text, mk = n.manageRootView()
	}
	n.editMessage(ctx, chat, msgID, text, mk)
	n.answerCallback(ctx, cbID, toast)
}

// ── store mutations (audited) ───────────────────────────────────────────────

func (n *tgNotifier) toggleRuleField(rid, field, user string) string {
	s := n.rstore()
	_, rules := s.snapshot()
	var cur *ruleDef
	for i := range rules {
		if rules[i].ID == rid {
			cur = &rules[i]
			break
		}
	}
	if cur == nil {
		return "rule not found"
	}
	var p rulePatch
	var label string
	if field == "enabled" {
		v := !cur.Enabled
		p.Enabled = &v
		label = "enabled=" + onoff(v)
	} else {
		v := !cur.NotifyTG
		p.NotifyTG = &v
		label = "TG=" + onoff(v)
	}
	if _, err := s.updateRule(rid, p); err != nil {
		return err.Error()
	}
	n.engine.reloadRules()
	auditRecord(nil, AuditEvent{Event: "alert-rule.update", Severity: auditSevWarn, Actor: "tg:" + user, Target: rid,
		Details: map[string]any{"via": "bot", "field": field}})
	return rid + " · " + label
}

func (n *tgNotifier) toggleGroupEnabled(gid, user string) string {
	s := n.rstore()
	groups, _ := s.snapshot()
	var cur *ruleGroup
	for i := range groups {
		if groups[i].ID == gid {
			cur = &groups[i]
			break
		}
	}
	if cur == nil {
		return "group not found"
	}
	v := !cur.Enabled
	if _, err := s.updateGroup(gid, groupPatch{Enabled: &v}); err != nil {
		return err.Error()
	}
	n.engine.reloadRules()
	auditRecord(nil, AuditEvent{Event: "alert-group.update", Severity: auditSevWarn, Actor: "tg:" + user, Target: gid,
		Details: map[string]any{"via": "bot", "enabled": v}})
	return gid + " · enabled=" + onoff(v)
}

func (n *tgNotifier) muteGroup(gid, mins, user string) string {
	m, _ := strconv.Atoi(mins)
	var until int64
	if m > 0 {
		until = nowUnix() + int64(m)*60
	}
	if _, err := n.rstore().updateGroup(gid, groupPatch{MuteUntil: &until}); err != nil {
		return err.Error()
	}
	n.engine.reloadRules()
	auditRecord(nil, AuditEvent{Event: "alert-group.update", Severity: auditSevWarn, Actor: "tg:" + user, Target: gid,
		Details: map[string]any{"via": "bot", "mute_until": until}})
	if until == 0 {
		return gid + " · unmuted"
	}
	return gid + " · muted " + mins + " min"
}

// ── raw Telegram calls (direct, not via the send queue) ─────────────────────

func (n *tgNotifier) editMessage(ctx context.Context, chat string, msgID int64, text string, markup any) {
	payload := map[string]any{
		"chat_id": chat, "message_id": msgID, "text": text,
		"parse_mode": "HTML", "disable_web_page_preview": true,
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	n.tgPost(ctx, "editMessageText", payload)
}

func (n *tgNotifier) answerCallback(ctx context.Context, id, text string) {
	p := map[string]any{"callback_query_id": id}
	if text != "" {
		p["text"] = text
	}
	n.tgPost(ctx, "answerCallbackQuery", p)
}

func (n *tgNotifier) tgPost(ctx context.Context, method string, payload map[string]any) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.telegram.org/bot"+n.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if resp, err := n.client.Do(req); err == nil {
		resp.Body.Close()
	}
}
