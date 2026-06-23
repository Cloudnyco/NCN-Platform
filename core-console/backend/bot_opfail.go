// bot_opfail.go — the Telegram surface of the op-failure response system
// (opfailures.go). A plain 🔴 notification per failure (reason + optional Agent
// diagnosis, NO action buttons — operators dropped the inline panel) plus the
// read-only /errors list of recent failures.
package main

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"
	"unicode/utf8"
)

// triageOpFailure decides HOW loudly to report a failure. It asks the ops Agent
// to investigate with READ-ONLY tools (no writes, no run_command — allowWrites
// false) and classify the failure as NEEDS_FIX (something on OUR side to change)
// or EXTERNAL (upstream / provider / transient — nothing for us to do). EXTERNAL
// → the error channel only; NEEDS_FIX (or AI unavailable / ambiguous → fail
// loud) → also escalates to the ops group.
func (n *tgNotifier) triageOpFailure(f *opFailure) {
	if n == nil || f == nil {
		return
	}
	// Non-crit failures: just the full card to the error channel — no Agent.
	if !isCritFailure(f) {
		n.sendOpFailureCard(f, false, "")
		return
	}
	// Crit failures: the Agent investigates (read-only) and decides whether this
	// needs OUR fix; if so we also escalate the card to the ops group.
	if n.ai == nil || !n.ai.enabled() {
		n.sendOpFailureCard(f, true, "") // can't triage a crit → escalate (loud)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		prompt := fmt.Sprintf(
			"A CRITICAL operator action just FAILED on AS64500.\n"+
				"kind=%s  node=%s  by=%s\nreason: %s\n\n"+
				"Investigate with your READ-ONLY tools (node_detail, fleet_status, active_alerts, "+
				"op_failures) and decide: does fixing this need a change on OUR side (our config / "+
				"node / agent / mesh), or is it EXTERNAL/transient (upstream, provider, remote peer, "+
				"transient network — nothing for us to change)?\n"+
				"Give a one-or-two sentence diagnosis, then end with a FINAL line that is EXACTLY one of:\n"+
				"VERDICT: NEEDS_FIX\nVERDICT: EXTERNAL",
			f.Kind, f.Target, f.Actor, f.Reason)
		escalate := true // fail-safe: AI error / ambiguous verdict on a crit → escalate
		diag := ""
		if res, err := agentAdvance(ctx, []aiMsg{{Role: "user", Content: prompt}}, false, "auto-triage", nil, nil); err == nil {
			diag = res.Final
			if strings.Contains(strings.ToUpper(diag), "VERDICT: EXTERNAL") {
				escalate = false
			}
		}
		n.sendOpFailureCard(f, escalate, diag)
	}()
}

// isCritFailure marks the failures worth an Agent probe + group escalation: the
// live-routing / destructive actions. onboard / recommission are not crit.
func isCritFailure(f *opFailure) bool {
	switch f.Kind {
	case opKindDecommission, opKindDelete, opKindMeshApply:
		return true
	}
	return false
}

// sendOpFailureCard posts the failure as a PLAIN notification — reason +
// optional Agent diagnosis, NO action buttons (operators dropped the inline
// panel). It always goes to the error channel; escalateToGroup=true (a crit
// failure the Agent flagged NEEDS_FIX) also posts it to the ops group.
func (n *tgNotifier) sendOpFailureCard(f *opFailure, escalateToGroup bool, diag string) {
	if n == nil || f == nil {
		return
	}
	channel := n.errorChat
	if channel == "" {
		channel = n.chatID
	}
	text := fmt.Sprintf("🔴 <b>Op action failed · %s</b>\n<code>node</code> · %s\n<code>by</code> · %s\n<code>reason</code> · %s",
		html.EscapeString(opKindLabel(f.Kind)), html.EscapeString(f.Target), html.EscapeString(f.Actor), html.EscapeString(f.Reason))
	if d := stripVerdict(diag); d != "" {
		text += "\n<blockquote>🤖 " + mdToTG(d) + "</blockquote>"
	}
	n.enqueue(tgPayload{ChatID: channel, Text: text}, "opfail "+f.Kind)
	if escalateToGroup && channel != n.chatID {
		n.enqueue(tgPayload{ChatID: n.chatID, Text: text}, "opfail-grp "+f.Kind)
	}
}

// stripVerdict drops the trailing "VERDICT: …" line from the AI diagnosis and
// caps it so the whole card stays under Telegram's per-message limit. The cap
// is in BYTES (3000 leaves headroom below the 4096 UTF-16 ceiling even after
// the card prefix + HTML tags) and backs off to a rune boundary so a multibyte
// char (Chinese diagnoses are common) is never split.
func stripVerdict(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(strings.ToUpper(s), "VERDICT:"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	const diagMax = 3000
	if len(s) > diagMax {
		cut := diagMax
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = strings.TrimSpace(s[:cut]) + "…"
	}
	return s
}

// replyErrors — the /errors command: a read-only list of recent op failures,
// newest first. No buttons (the inline action panel was removed); operators
// remediate from the console or the node /netadmin view directly.
func (n *tgNotifier) replyErrors() {
	list := globalOpFailures.listSnapshot(false)
	if len(list) == 0 {
		n.enqueue(tgPayload{Text: "🎉 <b>Op failures</b>\n<blockquote>No recent failures</blockquote>", Disabled: true}, "opfail list")
		return
	}
	var b strings.Builder
	b.WriteString("🔴 <b>Recent op failures</b>")
	for i, f := range list {
		if i >= 20 {
			break
		}
		when := time.Unix(f.At, 0).Format("01-02 15:04")
		fmt.Fprintf(&b, "\n\n<code>%s</code> · %s · %s\n<blockquote>%s</blockquote>",
			html.EscapeString(opKindLabel(f.Kind)), html.EscapeString(f.Target), when, html.EscapeString(f.Reason))
	}
	n.enqueue(tgPayload{Text: b.String(), Disabled: true}, "opfail list")
}
