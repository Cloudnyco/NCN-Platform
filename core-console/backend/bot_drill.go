// bot_drill.go — the "under-attack" emergency drill (/stormtest, /stop).
//
// Goal: rehearse the ops team's detect→diagnose→mitigate→recover response to
// a realistic multi-node DDoS, WITHOUT harming real users or services.
//
//   * The alert picture is simulated FULLY: the first wave is all four PoPs
//     + the control plane (后台运维) erroring SIMULTANEOUSLY with DDoS-shaped
//     crits, escalating every few seconds, sustained until /stop, then a
//     clean "resolved" recovery for every source. Messages reuse the REAL
//     fired/resolved format (formatTGAlert) — indistinguishable from a real
//     incident in the group. The only tell is id=drill-N in the collapsed
//     folded meta.
//   * REAL load is applied to ALL FOUR PoPs as `nice -19` busy loops with a
//     `timeout` dead-man. Every real service (bird, xray :443, dovecot) runs
//     at nice 0 and preempts the burners, so load1/CPU% genuinely rise on the
//     dashboard (so it doesn't read as "just a drill") WITHOUT starving any
//     service or its users — including pop-04's live xray circumvention users.
//   * /stop ends the attack early and runs the recovery; a hard maxsecs cap
//     ends it even if /stop is forgotten.
package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"time"
)

// stormAuthUserID — Telegram user whose PRIVATE chat may fire the drill. The
// storm always lands in the group (n.chatID); the trigger + status replies use
// the DM, so when fired from this DM the group sees only "real" alerts.
const stormAuthUserID = "6838462569"

const (
	drillBurners    = 3                       // nice-19 loops per loaded node
	drillBurnMarker = "NCN_DRILL_BURN"        // argv tag for pkill
	drillWaveEvery  = 8 * time.Second         // pacing between escalation waves
	drillDefaultSec = 90                      // default hard cap
	drillMaxSec     = 180                     // hard cap ceiling (real load on BGP nodes — keep modest)
	fraAddr         = "198.51.100.7"        // pop-05, loaded via SSH
	fleetKey        = "/etc/ncn-core-console/fleet-key"
)

// replyStormTest starts the under-attack drill. maxsecs (default 90, cap 180)
// is the hard safety cap; the attack otherwise runs until /stop.
//
//	/stormtest [maxsecs]
func (n *tgNotifier) replyStormTest(args []string, srcChat string) {
	maxsecs := drillDefaultSec
	if len(args) > 0 {
		if v, err := strconv.Atoi(args[0]); err == nil {
			maxsecs = v
		}
	}
	if maxsecs < 10 {
		maxsecs = 10
	}
	if maxsecs > drillMaxSec {
		maxsecs = drillMaxSec
	}

	n.drillMu.Lock()
	if n.drillCancel != nil {
		n.drillMu.Unlock()
		n.replyTo(srcChat, "⚠️ A drill is already running — <code>/stop</code> it first")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	n.drillCancel = cancel
	n.drillMu.Unlock()

	n.replyTo(srcChat, fmt.Sprintf("🧪 <b>Under-attack drill started</b> (shown as a drill only in your DM)\n"+
		"4 nodes + control plane simulate 🔴 DDoS at once, escalating · real nice-19 load on 4 boxes\n"+
		"<b>/stop</b> triggers recovery · %ds hard cap auto-wraps up", maxsecs))

	go n.runDrill(ctx, maxsecs, srcChat)
}

func (n *tgNotifier) runDrill(ctx context.Context, maxsecs int, srcChat string) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("notify: drill panic recovered: %v", rec)
		}
		n.killLoadBurners() // always clean up real load
		n.endBlackout()     // always lift the blackout (console 503 + :22 firewall)
		n.drillMu.Lock()
		n.drillCancel = nil
		n.drillMu.Unlock()
	}()

	// Real nice-19 load on all 4 PoPs (kernel dead-man = maxsecs).
	n.startLoadBurners(maxsecs)
	// Blackout: console 503 + :22 firewalled on all 4 PoPs (tyo allowlisted so
	// the bot's recovery SSH still works; per-node dead-man auto-restores SSH
	// after maxsecs+30 even if /stop never comes).
	n.startBlackout(maxsecs)

	attackStart := time.Now()
	deadline := time.NewTimer(time.Duration(maxsecs) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(drillWaveEvery)
	defer ticker.Stop()

	// Wave 0 — all sources erroring simultaneously.
	n.emitWave(0)
	wave := 0
	stopped := false
loop:
	for {
		select {
		case <-ctx.Done(): // /stop
			stopped = true
			break loop
		case <-deadline.C: // hard cap
			break loop
		case <-ticker.C:
			wave++
			n.emitWave(wave)
		}
	}

	// Recovery: lift blackout + kill real load, then a clean resolved per source.
	n.killLoadBurners()
	n.endBlackout()
	dur := int(time.Since(attackStart).Seconds())
	for i := range drillSources {
		n.enqueue(tgPayload{Text: drillResolved(i, dur), Disabled: false}, fmt.Sprintf("drill resolve %d", i))
	}
	how := "hard cap reached"
	if stopped {
		how = "got /stop"
	}
	n.replyTo(srcChat, fmt.Sprintf("✅ Drill ended (%s) · ran %s · sent resolved for %d sources · load dropped on 4 boxes", how, humanDuration(int64(dur)), len(drillSources)))
}

// emitWave fires one escalating crit for every source, back-to-back, so they
// land near-simultaneously.
func (n *tgNotifier) emitWave(wave int) {
	for i := range drillSources {
		n.enqueue(tgPayload{Text: drillFired(i, wave), Disabled: false}, fmt.Sprintf("drill w%d s%d", wave, i))
	}
}

// drillSources — the four PoPs + the control plane (后台运维). pop-03/pop-04
// are simulated only; tyo/fra also get real load.
var drillSources = []string{"ctrl-01", "pop-05", "pop-03", "pop-04", "ctrl-plane"}

// drillEventBase returns the per-source crit identity (no per-wave metric yet).
func drillEventBase(idx int) alertEvent {
	switch idx % len(drillSources) {
	case 0:
		return alertEvent{NodeID: "ctrl-01", RuleID: "probe-down", Title: "connectivity probe failed",
			Threshold: "any ping target unreachable", Severity: sevCritical}
	case 1:
		return alertEvent{NodeID: "pop-05", RuleID: "node-unreachable", Title: "node unreachable",
			Threshold: "scrape OK=true", Severity: sevCritical}
	case 2:
		return alertEvent{NodeID: "pop-03", RuleID: "probe-down", Title: "connectivity probe failed",
			Threshold: "any ping target unreachable", Severity: sevCritical}
	case 3:
		return alertEvent{NodeID: "pop-04", RuleID: "probe-down", Title: "connectivity probe failed",
			Threshold: "any ping target unreachable", Severity: sevCritical}
	default:
		return alertEvent{NodeID: "ctrl-plane", RuleID: "control-plane-down", Title: "control plane anomaly (ops backend)",
			Threshold: "ncn-api 5xx / nginx 502 rate", Severity: sevCritical}
	}
}

// drillFired renders a DDoS-shaped crit, escalating with wave, in the exact
// real fired-alert format. Drill tell lives only in folded meta (id=drill-N).
func drillFired(idx, wave int) string {
	w := wave % 6
	ev := drillEventBase(idx)
	switch idx % len(drillSources) {
	case 0:
		ev.Message = fmt.Sprintf("down: cf-v6(2606:4700:4700::1111)(100%% loss, last-fail %ds ago) · SYN flood · pps≈%dk", 3+w*2, 120+w*60)
	case 1:
		ev.Message = fmt.Sprintf("scrape failed · last-attempt %ds ago · err: dial tcp [206.206.103.59]:22: i/o timeout (link saturated)", 9+w*4)
	case 2:
		ev.Message = fmt.Sprintf("down: inter-pop hkg→tyo(100%% loss) · google-v6 100%% loss · rx≈%dk pps · uplink saturated", 90+w*55)
	case 3:
		ev.Message = fmt.Sprintf("down: cf-v6 / google-v6 100%% loss · conntrack table %d%% · UDP flood", 88+w*2)
	default:
		ev.Message = fmt.Sprintf("ncn-api HTTP 5xx spiking · nginx 502 · req≈%dk/s · admin console unavailable", 8+w*5)
	}
	ev.FiredAt = time.Now().Unix()
	ev.ID = fmt.Sprintf("drill-%d", wave)
	return formatTGAlert(ev, "fired")
}

// drillResolved renders the matching recovery line (real resolved format).
func drillResolved(idx, durSecs int) string {
	ev := drillEventBase(idx)
	now := time.Now().Unix()
	ev.FiredAt = now - int64(durSecs)
	ev.ResolvedAt = now
	ev.ID = "drill-recover"
	return formatTGAlert(ev, "resolved")
}

// startLoadBurners spawns nice-19 busy loops on tyo (local) AND fra (via SSH),
// each wrapped in `timeout maxsecs`. nice 19 → bird (nice 0) always preempts →
// BGP safe. Tagged with drillBurnMarker so /stop can pkill them.
// startLoadBurners spawns nice-19 busy loops on ALL FOUR PoPs (tyo local, the
// rest via SSH), each wrapped in `timeout maxsecs`. nice 19 → every real
// service (bird, xray :443, dovecot/postfix) runs at nice 0 and preempts the
// burners, so load1/CPU% genuinely rise on the dashboard WITHOUT starving any
// service or its users. No sudo needed (nicing DOWN is unprivileged).
func (n *tgNotifier) startLoadBurners(maxsecs int) {
	for _, nd := range drillFWNodes {
		if nd.host == "local" {
			for i := 0; i < drillBurners; i++ {
				cmd := exec.Command("nice", "-n", "19", "timeout", strconv.Itoa(maxsecs),
					"bash", "-c", "while :; do :; done", drillBurnMarker)
				if err := cmd.Start(); err != nil {
					log.Printf("notify: drill local burner %d failed: %v", i, err)
					continue
				}
				go func(c *exec.Cmd) { _ = c.Wait() }(cmd)
			}
			continue
		}
		remote := fmt.Sprintf("for i in $(seq 1 %d); do nice -n 19 timeout %d bash -c 'while :; do :; done' %s >/dev/null 2>&1 & done",
			drillBurners, maxsecs, drillBurnMarker)
		if err := n.runNodeShell(nd, remote); err != nil {
			log.Printf("notify: drill load on %s failed: %v", nd.host, err)
		}
	}
	log.Printf("notify: drill started %d nice-19 burners on all %d PoPs for %ds", drillBurners, len(drillFWNodes), maxsecs)
}

// killLoadBurners terminates drill burners on every PoP.
func (n *tgNotifier) killLoadBurners() {
	for _, nd := range drillFWNodes {
		if nd.host == "local" {
			_ = exec.Command("pkill", "-f", drillBurnMarker).Run()
			continue
		}
		_ = n.runNodeShell(nd, "pkill -f "+drillBurnMarker)
	}
}

// ---- blackout: console 503 + SSH :22 timeout, recoverable ----

const drillBlackoutFlag = "/run/ncn-drill-blackout"   // nginx server-level `if -f` → 503
const drillAllowIP = "198.51.100.1"                 // tyo v4 — keeps the bot's recovery SSH alive through the :22 block
const drillAllowIP6 = "2001:db8:f::a"           // tyo v6 — same, for the ip6tables :22 block

// drillFWNode: a PoP whose inbound :22 we firewall during blackout.
type drillFWNode struct {
	host string // "local" → run on tyo directly; else SSH address
	user string // ssh user (remote only)
	sudo string // "sudo " on pop-03 (debian), "" on root nodes
}

var drillFWNodes = []drillFWNode{
	{"local", "", ""},
	{fraAddr, "root", ""},
	{"198.51.100.4", "root", ""},  // pop-04
	{"198.51.100.3", "debian", "sudo "}, // pop-03
}

func (n *tgNotifier) runNodeShell(nd drillFWNode, script string) error {
	if nd.host == "local" {
		return exec.Command("sh", "-c", script).Run()
	}
	return exec.Command("ssh", "-i", fleetKey, "-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=8", nd.user+"@"+nd.host, script).Run()
}

// startBlackout makes the console return a bare 503 and inbound SSH time out
// on all four PoPs. ONLY :22 is firewalled — xray :443, mail, and BGP are on
// other ports and untouched. tyo's IP is allowlisted so the bot's recovery
// SSH still reaches the nodes. A per-node nice dead-man (`sleep N; iptables
// -D`) auto-restores SSH after maxsecs+30 even if /stop never comes or the
// bot dies. ncn-api itself is never touched, so /stop (via Telegram, fully
// out-of-band) always works.
func (n *tgNotifier) startBlackout(maxsecs int) {
	_ = exec.Command("touch", drillBlackoutFlag).Run() // console 503
	dead := maxsecs + 30
	for _, nd := range drillFWNodes {
		s := nd.sudo
		// Block inbound :22 over BOTH v4 (iptables) and v6 (ip6tables) — these
		// nodes are dual-stack/IPv6-reachable, so v4-only would leave SSH open.
		// Only :22 (xray :443, mail, BGP untouched). tyo allowlisted (v4+v6) so
		// the bot's recovery SSH still works. Dead-man removes all 4 rules.
		script := fmt.Sprintf(
			"%siptables  -I INPUT 1 -p tcp --dport 22 -j DROP; "+
				"%siptables  -I INPUT 1 -p tcp --dport 22 -s %s -j ACCEPT; "+
				"%sip6tables -I INPUT 1 -p tcp --dport 22 -j DROP; "+
				"%sip6tables -I INPUT 1 -p tcp --dport 22 -s %s -j ACCEPT; "+
				"nohup sh -c 'sleep %d; "+
				"%siptables  -D INPUT -p tcp --dport 22 -j DROP 2>/dev/null; %siptables  -D INPUT -p tcp --dport 22 -s %s -j ACCEPT 2>/dev/null; "+
				"%sip6tables -D INPUT -p tcp --dport 22 -j DROP 2>/dev/null; %sip6tables -D INPUT -p tcp --dport 22 -s %s -j ACCEPT 2>/dev/null' >/dev/null 2>&1 &",
			s, s, drillAllowIP, s, s, drillAllowIP6, dead,
			s, s, drillAllowIP, s, s, drillAllowIP6)
		if err := n.runNodeShell(nd, script); err != nil {
			log.Printf("notify: drill blackout :22 on %s failed (graceful skip): %v", nd.host, err)
		}
	}
	log.Printf("notify: drill blackout ON — console 503 + :22 firewalled (v4+v6) on 4 PoPs (dead-man %ds)", dead)
}

// endBlackout lifts the console 503 and removes the :22 firewall on all nodes.
func (n *tgNotifier) endBlackout() {
	_ = exec.Command("rm", "-f", drillBlackoutFlag).Run()
	for _, nd := range drillFWNodes {
		s := nd.sudo
		script := fmt.Sprintf(
			"%siptables  -D INPUT -p tcp --dport 22 -j DROP 2>/dev/null; %siptables  -D INPUT -p tcp --dport 22 -s %s -j ACCEPT 2>/dev/null; "+
				"%sip6tables -D INPUT -p tcp --dport 22 -j DROP 2>/dev/null; %sip6tables -D INPUT -p tcp --dport 22 -s %s -j ACCEPT 2>/dev/null",
			s, s, drillAllowIP, s, s, drillAllowIP6)
		_ = n.runNodeShell(nd, script)
	}
	log.Printf("notify: drill blackout OFF — console restored + :22 (v4+v6) unblocked on 4 PoPs")
}

// replyStop ends an in-progress drill: cancel → runDrill runs recovery; also
// kill load immediately for promptness.
func (n *tgNotifier) replyStop(srcChat string) {
	n.drillMu.Lock()
	cancel := n.drillCancel
	n.drillMu.Unlock()
	n.killLoadBurners()
	n.endBlackout()
	if cancel != nil {
		cancel()
		n.replyTo(srcChat, "🛑 <b>Emergency stop</b> · recovering: drop load + lift :22 ban + reset console + send resolved")
	} else {
		n.replyTo(srcChat, "(no drill in progress; cleaned up any leftover burners)")
	}
}

// replyHelpStorm — trimmed help for the storm-only DM user.
func (n *tgNotifier) replyHelpStorm(srcChat string) {
	n.replyTo(srcChat, "🧪 <b>NCN under-attack emergency drill</b>\n\n"+
		"<code>/stormtest [maxsecs]</code> — full DDoS simulation (default 90s, cap 180s):\n"+
		"  · 4 nodes + control plane go 🔴 crit at once, escalating (in the group)\n"+
		"  · admin.example.com serves a bare 503 site-wide (locked out)\n"+
		"  · SSH (:22) banned on 4 boxes → sessions time out\n"+
		"  · real nice-19 load on 4 boxes (xray/mail/BGP unaffected)\n"+
		"<code>/stop</code> — one-tap recovery: drop load + unban :22 + reset console + send resolved\n\n"+
		"<i>Only :22 is banned → xray:443/mail/BGP unaffected; tyo is allowlisted to guarantee recovery; each box has a dead-man switch (auto-reset at maxsecs+30). /stop goes via Telegram, not nginx, so the API can not be taken down.</i>")
}
