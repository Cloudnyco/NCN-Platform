// Acme Net — Core Console · data-plane telemetry bridge.
//
// Listens on :9000 and exposes /api/v1/* endpoints that shell out to real
// Linux probes (uptime, /proc/loadavg, birdc, wg). No mock data.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ----------------------------------------------------------------------------
// Wire types
// ----------------------------------------------------------------------------

type envelope struct {
	OK    bool        `json:"ok"`
	Data  interface{} `json:"data,omitempty"`
	Error string      `json:"error,omitempty"`
	TS    string      `json:"ts"`
}

type loadInfo struct {
	Uptime    string  `json:"uptime"`
	Load1     float64 `json:"load_1"`
	Load5     float64 `json:"load_5"`
	Load15    float64 `json:"load_15"`
	Procs     string  `json:"procs"`
	LastPID   string  `json:"last_pid"`
	Hostname  string  `json:"hostname"`
	BootEpoch int64   `json:"boot_epoch,omitempty"`
}

type cmdResult struct {
	Cmd      string `json:"cmd"`
	Raw      string `json:"raw"`
	ExitCode int    `json:"exit_code"`
	Stderr   string `json:"stderr,omitempty"`
	Duration string `json:"duration"`
}

// ----------------------------------------------------------------------------
// HTTP helpers
// ----------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, env envelope) {
	env.TS = time.Now().UTC().Format(time.RFC3339Nano)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

func withMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Permissive CORS — frontend may be served from a different origin in dev (:5173).
		// Reflect origin to permit cookie-bearing requests in dev (* + credentials is illegal).
		// In prod the SPA is same-origin via nginx, so this branch is effectively unused.
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
		w.Header().Set("Access-Control-Max-Age", "600")
		w.Header().Set("X-NCN-Service", "core-console-api")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		started := time.Now()
		next(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(started))
	}
}

// runCmd executes a command with a per-call timeout. It returns stdout, stderr,
// exit code, and (only on plumbing failure) an error.
func runCmd(timeout time.Duration, name string, args ...string) (string, string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
			err = nil
		} else if ctx.Err() != nil {
			return stdout.String(), stderr.String(), -1, fmt.Errorf("timeout after %s", timeout)
		} else {
			return stdout.String(), stderr.String(), -1, err
		}
	}
	return stdout.String(), stderr.String(), exitCode, nil
}

// ----------------------------------------------------------------------------
// Endpoints
// ----------------------------------------------------------------------------

// GET /api/v1/system/load
// Shells out to `uptime` and `cat /proc/loadavg`, parses, returns JSON.
func handleSystemLoad(w http.ResponseWriter, _ *http.Request) {
	uptimeOut, _, _, err := runCmd(3*time.Second, "uptime")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{
			OK: false, Error: "uptime failed: " + err.Error(),
		})
		return
	}

	loadOut, _, _, err := runCmd(3*time.Second, "cat", "/proc/loadavg")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{
			OK: false, Error: "loadavg read failed: " + err.Error(),
		})
		return
	}

	info := loadInfo{
		Uptime: strings.TrimSpace(uptimeOut),
	}

	// /proc/loadavg format:  0.05 0.10 0.15 1/123 4567
	fields := strings.Fields(strings.TrimSpace(loadOut))
	if len(fields) >= 5 {
		info.Load1, _ = strconv.ParseFloat(fields[0], 64)
		info.Load5, _ = strconv.ParseFloat(fields[1], 64)
		info.Load15, _ = strconv.ParseFloat(fields[2], 64)
		info.Procs = fields[3]
		info.LastPID = fields[4]
	}

	if hn, err := os.Hostname(); err == nil {
		info.Hostname = hn
	}
	if stat, err := os.Stat("/proc/1"); err == nil {
		info.BootEpoch = stat.ModTime().Unix()
	}

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: info})
}

// GET /api/v1/bgp/peers
// Real exec: sudo birdc show protocols. Returns raw text in JSON envelope.
func handleBGPPeers(w http.ResponseWriter, _ *http.Request) {
	start := time.Now()
	stdout, stderr, exit, err := runCmd(6*time.Second, "sudo", "-n", "birdc", "show", "protocols")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{
			OK: false, Error: "birdc exec failed: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, envelope{
		OK: exit == 0,
		Data: cmdResult{
			Cmd:      "sudo birdc show protocols",
			Raw:      stdout,
			Stderr:   stderr,
			ExitCode: exit,
			Duration: time.Since(start).String(),
		},
	})
}

// GET /api/v1/wg/status
// Real exec: sudo wg show. Returns raw text in JSON envelope.
func handleWGStatus(w http.ResponseWriter, _ *http.Request) {
	start := time.Now()
	stdout, stderr, exit, err := runCmd(6*time.Second, "sudo", "-n", "wg", "show")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{
			OK: false, Error: "wg exec failed: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, envelope{
		OK: exit == 0,
		Data: cmdResult{
			Cmd:      "sudo wg show",
			Raw:      stdout,
			Stderr:   stderr,
			ExitCode: exit,
			Duration: time.Since(start).String(),
		},
	})
}

// GET /api/v1/health — cheap liveness probe.
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]string{
		"service": "ncn-core-console-api",
		"version": "0.1.0",
	}})
}

// ----------------------------------------------------------------------------
// Looking Glass — POST /api/v1/lg/exec
// ----------------------------------------------------------------------------

type lgRequest struct {
	Tool   string `json:"tool"`
	Target string `json:"target"`
}

// Per RFC 1123 hostname grammar (loose; we mostly rely on net.ParseIP / ParseCIDR
// for IPs and CIDRs, this regex only catches DNS names).
var hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// validateTarget rejects anything that's not an IP, optional CIDR, or DNS name.
// Defense in depth — even though we never pass `target` through a shell, we
// still want to bounce obviously malformed inputs early.
func validateTarget(target string, allowCIDR, allowHostname bool) bool {
	if target == "" || len(target) > 253 {
		return false
	}
	if strings.HasPrefix(target, "-") {
		return false // never let it look like a flag
	}
	if ip := net.ParseIP(target); ip != nil {
		return true
	}
	if allowCIDR {
		if _, _, err := net.ParseCIDR(target); err == nil {
			return true
		}
	}
	if allowHostname {
		return hostnameRe.MatchString(target)
	}
	return false
}

// POST /api/v1/lg/exec
// Body: {"tool":"ping4|ping6|trace4|trace6|bgp_route", "target":"..."}
func handleLGExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}

	var req lgRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json: " + err.Error()})
		return
	}
	req.Tool = strings.TrimSpace(req.Tool)
	req.Target = strings.TrimSpace(req.Target)

	var (
		name    string
		args    []string
		timeout time.Duration
	)

	switch req.Tool {
	case "ping4":
		if !validateTarget(req.Target, false, true) {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid IPv4 / hostname"})
			return
		}
		name, args = "ping", []string{"-4", "-c", "4", "-W", "2", "-n", req.Target}
		timeout = 12 * time.Second

	case "ping6":
		if !validateTarget(req.Target, false, true) {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid IPv6 / hostname"})
			return
		}
		name, args = "ping", []string{"-6", "-c", "4", "-W", "2", "-n", req.Target}
		timeout = 12 * time.Second

	case "trace4":
		if !validateTarget(req.Target, false, true) {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid IPv4 / hostname"})
			return
		}
		name, args = "traceroute", []string{"-4", "-n", "-w", "2", "-q", "1", "-m", "20", req.Target}
		timeout = 50 * time.Second

	case "trace6":
		if !validateTarget(req.Target, false, true) {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid IPv6 / hostname"})
			return
		}
		name, args = "traceroute", []string{"-6", "-n", "-w", "2", "-q", "1", "-m", "20", req.Target}
		timeout = 50 * time.Second

	case "bgp_route":
		// BIRD parses the IP/prefix itself; hostnames not accepted.
		if !validateTarget(req.Target, true, false) {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "expected IP or CIDR prefix"})
			return
		}
		name, args = "sudo", []string{"-n", "birdc", "show", "route", "for", req.Target}
		timeout = 5 * time.Second

	default:
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "unknown tool (allowed: ping4, ping6, trace4, trace6, bgp_route)"})
		return
	}

	start := time.Now()
	stdout, stderr, exit, err := runCmd(timeout, name, args...)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{
			OK: false, Error: req.Tool + " exec failed: " + err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, envelope{
		OK: true,
		Data: cmdResult{
			Cmd:      name + " " + strings.Join(args, " "),
			Raw:      stdout,
			Stderr:   stderr,
			ExitCode: exit,
			Duration: time.Since(start).String(),
		},
	})
}

// ----------------------------------------------------------------------------
// main
// ----------------------------------------------------------------------------

func main() {
	// Break-glass admin CLI. When invoked as `ncn-api admin <subcmd>`, we
	// short-circuit before any flag/network init so the binary can also act
	// as the operator-management tool over plain SSH. Lives in admincli.go.
	if len(os.Args) >= 2 && os.Args[1] == "admin" {
		adminCLIEntrypoint(os.Args[2:])
		return
	}

	addr := flag.String("addr", ":9000", "listen address")
	flag.Parse()

	// Persistence foundation: open Postgres (if NCN_DATABASE_URL set) + run
	// migrations. Non-fatal — globalDB stays nil and stores run file-backed
	// when DB is unset/unreachable. Stores are migrated to it one at a time.
	initDB()
	reconcileConfigDocs() // heal any file↔PG divergence (post-failover) before stores load

	auth, err := loadAuthStore()
	if err != nil {
		log.Fatalf("auth init: %v", err)
	}
	// If there is at least one operator but none has admin role, promote
	// the alphabetically-first one. Idempotent; runs every startup.
	auth.promoteFirstAdminIfNone()

	// Audit log — open before anything that might want to call auditRecord().
	// Failure to open is fatal: a security-relevant binary running without
	// audit is worse than not starting.
	if astore, aerr := newAuditStore(); aerr != nil {
		log.Fatalf("audit init: %v", aerr)
	} else {
		globalAudit = astore
		log.Printf("audit: log open at %s", auditPath)
		auditRecord(nil, AuditEvent{
			Event: "service.start", Severity: auditSevInfo, Actor: "system", Outcome: "ok",
			Details: map[string]any{"pid": os.Getpid()},
		})
	}

	// Incidents store — drives the public status page + admin CRUD.
	// Non-fatal on init failure: a missing file becomes empty list, a
	// corrupt file would block startup but that's the right call (we'd
	// silently lose history otherwise).
	if istore, ierr := newIncidentStore(); ierr != nil {
		log.Fatalf("incidents init: %v", ierr)
	} else {
		globalIncidents = istore
		log.Printf("incidents: store open at %s (%d entries)", incidentsPath, len(istore.incidents))
	}

	// Billing store — VPS rent tracker (NOT customer billing). Drives
	// the /admin/billing page + the vps-renewal-soon alert rule. Same
	// init posture as incidents.
	if bstore, berr := newBillingStore(); berr != nil {
		log.Fatalf("billing init: %v", berr)
	} else {
		globalBilling = bstore
		log.Printf("billing: store open at %s (%d entries)", billingPath, len(bstore.subs))
	}

	// FX store — converts non-CNY billing totals to a CNY-equivalent
	// for the rollup row. Refreshes every 12h from open.er-api.com.
	// Non-fatal: if FX is unavailable the UI degrades to per-currency
	// totals only. (refresher goroutine is started below after ctx
	// is created.)
	globalFX = newFXStore()

	// API token store — bearer auth for CLI/scripts. Missing file is
	// fine (fresh install starts with zero tokens); a corrupt file is
	// fatal because we don't want to silently un-revoke leaked tokens.
	apiTokStore, aterr := newAPITokenStore()
	if aterr != nil {
		log.Fatalf("api-tokens init: %v", aterr)
	}
	globalAPITokens = apiTokStore

	// Operator → webmail bridge. Loads the shared HMAC key if present;
	// missing key is non-fatal (endpoint will return 503 until provisioned).
	mailBridge, err := newMailBridgeService(auth)
	if err != nil {
		log.Printf("mail-bridge init: %v (continuing without)", err)
	}
	// Make the bridge reachable from authStore methods (invite mail send).
	auth.mailBridge = mailBridge

	// Break-glass recovery via signed URL. Verifies tokens minted by
	// `ncn-api admin mint-recover`. Key may be absent at boot — created on
	// first mint — so this can never fail fatally.
	recoverBoot, err := newRecoverBootstrapService(auth)
	if err != nil {
		log.Printf("recover-bootstrap init: %v (continuing without)", err)
	}

	// Restore outstanding + recently-used invite tokens so an admin's already-
	// shared invite link survives an ncn-api restart.
	loadInvitesFromDisk()

	// WebAuthn relying-party config.
	//
	// RPID stays `example.com` (the registrable origin). A credential created
	// against this RPID can be USED on the parent AND any subdomain — that's
	// why credentials registered before the admin split still work on
	// admin.example.com after the split.
	//
	// RPOrigins is the assertion-time check: every Origin a browser may use
	// to authenticate must be enumerated here. Add admin.example.com (where
	// login now lives) plus the originals.
	if err := auth.initWebAuthn(
		"example.com",
		"Acme Net",
		[]string{
			"https://example.com",
			"https://www.example.com",
			"https://admin.example.com",
		},
	); err != nil {
		log.Fatalf("webauthn init: %v", err)
	}

	// Start the monitoring subsystem (collectors, probes, BIRD scrape, alerts).
	monitor := NewMonitor()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor.Start(ctx)

	// FX rates refresher — kicked off here (not at globalFX init) because
	// it needs ctx and an active goroutine. Initial fetch is synchronous
	// inside startFXRefresher so the first /billing has a CNY figure.
	startFXRefresher(ctx)

	// Node registry — persistent, runtime-editable PoP list (replaces the
	// old hardcoded slice in fleet.go). First run seeds it with the historical
	// fleet so behaviour is unchanged. Fatal on a corrupt file: we don't want
	// to silently boot with a wrong / empty fleet.
	nodeReg, nrerr := newNodeRegistry()
	if nrerr != nil {
		log.Fatalf("node registry init: %v", nrerr)
	}
	globalNodes = nodeReg
	log.Printf("nodes: registry open at %s (%d records)", nodeRegistryPath, len(nodeReg.recs))

	// Fleet scraper — HTTPS to ncn-agent on each active PoP every 15s, cache
	// results. Node list comes from the registry. (Was SSH pre-Phase-3.)
	fleet := newFleetScraper(monitor, nodeReg)
	fleet.auth = auth
	fleet.Start(ctx)

	// Heartbeat store — availability history behind the public status page
	// (uptime % + day-by-day bars). Reads the fleet cache for PoPs and GETs
	// our public URLs every 60s. Same init posture as incidents/billing:
	// a missing file is an empty history, a corrupt one blocks startup.
	if hb, herr := newHeartbeatStore(fleet); herr != nil {
		log.Fatalf("heartbeat init: %v", herr)
	} else {
		globalHeartbeat = hb
		hb.Start(ctx)
		log.Printf("heartbeat: store open at %s (%d components)", heartbeatPath, len(hb.order))
	}

	// Capacity planning — durable per-(node,metric,day) rollups + least-squares
	// link-saturation forecast. Samples the fleet snapshot every minute; the
	// forecast drives the link_saturation_eta_days alert metric + /admin/capacity.
	globalCapacity = newCapacityStore(fleet)
	globalCapacity.Start(ctx)
	log.Printf("capacity: store started")

	// Active SLA probing — operator-defined targets folded into every PoP's probe
	// set; per-(pop,target,day) availability/loss/latency buckets behind the
	// status page + the sla_loss_pct / sla_rtt_ms alert metrics. RebuildProbes
	// picks up any persisted targets so they probe without an ncn-api restart.
	globalSLA = newSLAStore(fleet)
	globalSLA.Start(ctx)
	fleet.RebuildProbes()
	log.Printf("capacity: sla store started (%d targets)", len(globalSLA.targets))

	// SIGHUP → reload per-node HMAC keys from /etc/ncn-core-console/agent-keys/.
	// Lets ops re-provision a PoP and pick up the new key without an ncn-api
	// restart (which used to mean a brief 401 window after every key
	// rotation). systemctl reload ncn-api is the canonical trigger; the
	// unit's ExecReload sends SIGHUP.
	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)
	go func() {
		for range hupCh {
			log.Printf("main: SIGHUP — reloading agent HMAC keys")
			fleet.ReloadAgentKeys()
		}
	}()

	// Wire the alert engine to the fleet so rules evaluate across all PoPs
	// (ctrl-01 / pop-04 / pop-05). Until this is set, the engine no-ops; it
	// starts firing real alerts on the next 30s tick after wiring.
	monitor.alerts.setFleet(fleet)
	// Let node lifecycle changes (decommission / delete) evict that node's
	// active alerts so a "node unreachable" doesn't hang after it's removed.
	fleet.setAlerts(monitor.alerts)

	// Persistent, user-editable alert rules. First run seeds the historical
	// built-ins + a default all-nodes group so behaviour is unchanged; admins
	// then edit/group/scope them via the console. setRuleStore compiles them
	// into the engine immediately.
	var alertRulesAPI *alertRulesAPI
	if ruleStore, rserr := newAlertRuleStore(); rserr != nil {
		log.Printf("main: alert rule store init failed (%v) — alerting falls back to empty rule set", rserr)
	} else {
		monitor.alerts.setRuleStore(ruleStore)
		alertRulesAPI = newAlertRulesAPI(ruleStore, monitor.alerts, fleet)
	}

	// Optional Telegram notifier — only enabled when both env vars are set.
	// Loaded from /etc/ncn-core-console/tg.env via systemd EnvironmentFile.
	// Absent file or missing vars → notifier stays nil, alert engine just
	// skips the out-of-band send. Web UI alerts continue regardless.
	// DeepSeek (LLM) client — powers the bot AI features + console assistant
	// (deepseek.go / bot_ai.go). Key from NCN_DEEPSEEK_API_KEY (server env only).
	// Gracefully disabled when absent. aiCtx* give the console handler the same
	// live context the bot has.
	globalAI = newDeepseekClient()
	aiCtxFleet = fleet
	aiCtxEngine = monitor.alerts
	aiCtxAuth = auth // for the ops agent's admin check (agent.go)
	// Per-purpose model selection (model_config.go): load saved mapping + fetch
	// the available model list from DeepSeek (async, best-effort).
	globalAIModels = newAIModelStore()
	go globalAIModels.refreshAvailable(globalAI)
	// Per-operator conversation history + memory (ai_user_store.go).
	globalAIUsers = newAIUserStore()
	if globalAI.enabled() {
		log.Printf("ai: DeepSeek enabled (model=%s)", globalAI.model)
	} else {
		log.Printf("ai: DeepSeek disabled (NCN_DEEPSEEK_API_KEY unset)")
	}

	var tgRef *tgNotifier
	if tg := newTGNotifier(os.Getenv("NCN_TG_BOT_TOKEN"), os.Getenv("NCN_TG_CHAT_ID")); tg != nil {
		tgRef = tg
		globalNotify = tg                                                  // live notifier for out-of-band op-failure triggers
		tg.errorChat = strings.TrimSpace(os.Getenv("NCN_TG_ERROR_CHANNEL")) // op-failure reports route here
		tg.Start(ctx)
		// Notifier still wired into the alert engine — see alerts.go
		// tickOnce for the (currently disabled) auto-push hooks. Kept
		// pointer in case we ever re-enable selective push.
		monitor.alerts.setNotifier(tg)
		// Wire engine + fleet pointers so the long-poll command handlers
		// can read state (active alerts, per-node scrape snapshot).
		tg.setEngineFleet(monitor.alerts, fleet)
		// Wire the operator store so the bot can verify each Telegram user's
		// identity against the ops platform (bound + approved operator) before
		// honoring any command — and address them by their chosen 称呼.
		tg.setAuth(auth)
		// Wire the DeepSeek client + bot @username (for @mention detection) so
		// /ask · /summary · /chat · the group companion · AI failure diagnosis work.
		tg.setAI(globalAI, os.Getenv("NCN_TG_BOT_USERNAME"))
		// Let the fleet push server-lifecycle events (add / decommission /
		// recommission / delete / provision) to the chat. Set before Serve,
		// so no node-API request can read it mid-write.
		fleet.notify = tg
		// One silent "service online" banner per boot — the operator asked
		// for a heads-up when a new build goes live. Silent (no phone ring);
		// just a line in the chat with the active PoP list.
		tg.SendStartup(fleet.nodesSnapshot())
		// Start the slash-command receiver. Long-polls getUpdates, only
		// answers messages from the configured chat ID, all commands
		// strictly read-only. Auto-push to the chat is limited to: crit
		// alert fire/resolve nudges (maybeFireTG), the silent startup banner
		// above, and server-lifecycle events (NotifyEvent) — everything else
		// is pull-only via slash commands.
		tg.runCommandLoop(ctx)
	} else {
		log.Printf("notify: telegram disabled (NCN_TG_BOT_TOKEN / NCN_TG_CHAT_ID unset)")
	}

	// RPKI ROA-validity monitoring (rpki.go) — outside-in watch that AS64500's
	// own announced prefixes are RPKI-valid (we publish ROAs via Krill on
	// pop-01). Alerts the ops group on invalid / missing-ROA. Read-only public
	// data (RIPEstat); touches no router. ASN overridable via NCN_ASN.
	globalRPKI = newRPKIMonitor(getenvDefault("NCN_ASN", "64500"), tgRef, fleet, getenvDefault("NCN_RPKI_ROV_NODE", "pop-01"))
	globalRPKI.Start(ctx)

	// Prefix-hijack detection — streams live BGP UPDATEs from RIPE RIS Live
	// filtered to our prefixes; alerts when someone else originates our space.
	// Reuses globalRPKI.announced as the source of which prefixes to watch.
	globalHijack = newHijackMonitor(getenvDefault("NCN_ASN", "64500"), tgRef, globalRPKI.announced)
	globalHijack.Start(ctx)

	// Replication watchdog — the HA foundation (PG streaming replication
	// ctrl-01→pop-03) has no self-monitoring otherwise; alert the error channel
	// if the standby stops streaming or lags. Primary-side, no-op without PG.
	newReplMonitor(globalNotify).Start(ctx)

	// Anycast blackhole watcher — proposes (never auto-runs) a drain when a node
	// still announces anycast while its own probes fail. Human approves in the
	// console. Skips the local control node.
	newAnycastWatcher(fleet, globalNotify).Start(ctx)

	// Config drift detection — periodically re-hashes each node's live bird.conf /
	// filters / nft against its adopted baseline (config_declarations) and alerts
	// on drift. One-click rollback (BIRD only) is confirm-gated. globalNotify may
	// be nil (TG disabled) → drift still detected + surfaced in the console.
	globalDrift = newDriftStore(fleet, globalNotify)
	globalDrift.Start(ctx)
	log.Printf("drift: store started")

	// On-call rotation + escalation — pages the on-duty operator (then admins,
	// then the group) when an alert stays firing+unacked past a tier threshold.
	// No-op without TG (globalNotify nil) or an empty policy.
	globalOncall = newOncallStore(monitor.alerts, globalNotify, auth)
	globalOncall.Start(ctx)
	log.Printf("oncall: escalation loop started")

	// Netflow/sFlow analytics — tails the central goflow2 collector's JSON file
	// (NCN_FLOW_FILE) into an in-memory sliding window of top talkers. Inert
	// until the collector is deployed + PoPs export sFlow (see deploy/sflow/).
	globalNetflow = newNetflowStore()
	globalNetflow.Start(ctx)
	log.Printf("netflow: store started (file=%s)", getenvDefault("NCN_FLOW_FILE", flowDefaultFile))

	// DDoS mitigation — confirm-gated nft drop/rate rules pushed to our edge PoPs,
	// TTL auto-expire, anomaly watcher proposes (text only). Never auto-applies.
	globalDDoS = newDDoSStore(fleet, globalNotify)
	globalDDoS.Start(ctx)
	log.Printf("ddos: mitigation store started")

	// VPS billing renewal digest — daily background job that surfaces
	// "due soon" subscriptions in the ops Telegram chat. Independent of
	// the fleet alert engine because the trigger is calendar-based, not
	// telemetry-based. Always launches even if tgRef is nil; the digest
	// then just logs to the journal.
	startBillingRenewalNotifier(ctx, tgRef)

	pdb := newPeeringDBState()
	pdb.Start(ctx)

	mux := http.NewServeMux()

	// Public Looking Glass: 20 requests / 5 min / IP.
	lgLimiter := newRateLimiter(20, 5*time.Minute)

	// Public — liveness + auth bootstrap + visitor whois + public Looking Glass
	mux.HandleFunc("/api/v1/health",       withMiddleware(handleHealth))
	mux.HandleFunc("/api/v1/visitor",      withMiddleware(handleVisitor))
	mux.HandleFunc("/api/v1/fleet/public", withMiddleware(fleet.handlePublic))
	// Public status-page incidents feed — 30-day window, no auth, cached
	// 15s at the CDN/browser layer. Whitelisted in nginx-ncn-core-console.conf.
	mux.HandleFunc("/api/v1/incidents/public", withMiddleware(handleIncidentsPublic))
	// Public status-page availability feed — components + 90-day uptime
	// history. No auth; same nginx whitelist as incidents/public.
	mux.HandleFunc("/api/v1/status/summary", withMiddleware(handleStatusSummary))
	// Public SLA feed — per-(target,PoP) availability/loss/latency vs SLO (sla.go).
	mux.HandleFunc("/api/v1/status/sla", withMiddleware(handleStatusSLA))
	// Live inter-PoP RTT matrix — backs the topology map's click-to-show-latency.
	mux.HandleFunc("/api/v1/status/latency", withMiddleware(fleet.handleLatency))
	mux.HandleFunc("/api/v1/peeringdb",    withMiddleware(pdb.handleHTTP))

	// Peering-application intake — public (anonymous, rate-limited).
	// Stored at peeringApplyPath; admins review at /admin/peering.
	peering, perr := newPeeringStore(mailBridge)
	if perr != nil {
		log.Fatalf("peering store init: %v", perr)
	}
	peering.fleet = fleet           // for IRR expand + peer-config apply
	globalPeerGen = newPeerGenStore() // generated per-peer BIRD configs
	{
		irrNode := getenvDefault("NCN_IRR_NODE", "")
		if irrNode == "" {
			irrNode = fleet.localID
		}
		globalPeerRefresh = newPeerRefresher(fleet, tgRef, irrNode)
		globalPeerRefresh.Start(ctx)
	}
	mux.HandleFunc("/api/v1/peering/apply", withMiddleware(peering.handleApply))
	mux.HandleFunc("/api/v1/auth/login",   withMiddleware(auth.handleLogin))
	// Step 2 of the password-path flow — verifies the TOTP code against
	// the intent ticket cookie set by step 1. Public route because the
	// ticket cookie itself proves "password OK"; no session yet.
	mux.HandleFunc("/api/v1/auth/login/verify-totp", withMiddleware(auth.handleLoginVerifyTOTP))
	mux.HandleFunc("/api/v1/auth/recover", withMiddleware(auth.handleRecoverPassword))
	// Break-glass recovery via signed URL minted by `ncn-api admin
	// mint-recover`. Public because the user has lost their MFA — trust
	// comes from the HMAC signature, not from a session.
	if recoverBoot != nil {
		mux.HandleFunc("/api/v1/auth/bootstrap-recover",         withMiddleware(recoverBoot.handleSubmit))
		mux.HandleFunc("/api/v1/auth/bootstrap-recover/preview", withMiddleware(recoverBoot.handlePreview))
	}
	mux.HandleFunc("/api/v1/auth/passkey/login/begin",  withMiddleware(auth.handlePasskeyLoginBegin))
	mux.HandleFunc("/api/v1/auth/passkey/login/finish", withMiddleware(auth.handlePasskeyLoginFinish))
	// SSH-signed login (challenge/response via the ncn-login CLI tool).
	// All three endpoints are public — no session yet at /begin or
	// /finish, and /redeem is what sets the session in the first place.
	mux.HandleFunc("/api/v1/auth/ssh-login/begin",  withMiddleware(auth.handleSSHLoginBegin))
	mux.HandleFunc("/api/v1/auth/ssh-login/finish", withMiddleware(auth.handleSSHLoginFinish))
	mux.HandleFunc("/api/v1/auth/ssh-login/redeem", withMiddleware(auth.handleSSHLoginRedeem))

	// OAuth/OIDC + Telegram login, bound to operator accounts (oauth.go).
	// start + callback are PUBLIC (no session yet); bind-start + identities are
	// protected (registered below). A provider with no creds is disabled and
	// its start endpoint redirects to /login?oauth_err=provider-disabled.
	oauthSvc := newOAuthService(auth, os.Getenv("NCN_TG_BOT_TOKEN"))
	oauthSvc.tg = tgRef // nil if telegram disabled; lets /bind DM a bind confirmation
	// exact pattern (wins over the subtree): provider list. Telegram now uses
	// the generic {provider}/callback route below (OIDC, like github).
	mux.HandleFunc("/api/v1/auth/oauth/providers", withMiddleware(oauthSvc.handleProviders))
	// public subtree: {provider}/start + {provider}/callback.
	mux.HandleFunc("/api/v1/auth/oauth/", withMiddleware(oauthSvc.routePublic))

	// Self-hosted wiki (wikistore.go / wiki_api.go). Public tier = anonymous,
	// publicOnly (nginx allow-lists /api/v1/wiki/); internal read = any operator;
	// write = admin + audited. Server-side publicOnly so internal pages never
	// leak through the public routes.
	mux.HandleFunc("/api/v1/wiki/tree",       withMiddleware(handleWikiTreePublic))
	mux.HandleFunc("/api/v1/wiki/page",       withMiddleware(handleWikiPagePublic))
	mux.HandleFunc("/api/v1/wiki/search",     withMiddleware(handleWikiSearchPublic))
	mux.HandleFunc("/api/v1/auth/wiki/tree",  withMiddleware(auth.requireAuth(handleWikiTree)))
	mux.HandleFunc("/api/v1/auth/wiki/page",  withMiddleware(auth.requireAuth(handleWikiPageRead)))
	mux.HandleFunc("/api/v1/auth/wiki/search", withMiddleware(auth.requireAuth(handleWikiSearch)))
	mux.HandleFunc("/api/v1/auth/wiki/save",  withMiddleware(auth.requireRole("admin", handleWikiSave)))
	mux.HandleFunc("/api/v1/auth/wiki/delete", withMiddleware(auth.requireRole("admin", handleWikiDelete)))
	mux.HandleFunc("/api/v1/auth/wiki/versions", withMiddleware(auth.requireRole("admin", handleWikiVersions)))
	mux.HandleFunc("/api/v1/auth/wiki/revert", withMiddleware(auth.requireRole("admin", handleWikiRevert)))

	// SSO Identity Provider — the console's login page is the single sign-on for
	// other apps (Wiki.js). Distinct /idp/ prefix so it never collides with the
	// oauth-client /oauth/ subtree above. Enabled only when the client env is set.
	idp := newIDPProvider(auth)
	if idp.enabled() {
		mux.HandleFunc("/api/v1/auth/idp/authorize", withMiddleware(idp.handleAuthorize))
		mux.HandleFunc("/api/v1/auth/idp/token", withMiddleware(idp.handleToken))
		mux.HandleFunc("/api/v1/auth/idp/userinfo", withMiddleware(idp.handleUserinfo))
		log.Printf("idp: OAuth2 SSO provider enabled (client=%s)", idp.clientID)
	}

	mux.HandleFunc("/api/v1/lg/exec",      withMiddleware(lgLimiter.Middleware(handleLGExec)))
	// Public BGP-sessions overview (all PoPs) for the Looking Glass.
	mux.HandleFunc("/api/v1/lg/sessions",  withMiddleware(fleet.handleLGSessions))

	// Logout reads the session if present, so it goes through requireAuth.
	mux.HandleFunc("/api/v1/auth/logout", withMiddleware(auth.requireAuth(auth.handleLogout)))

	// Protected — operator-only telemetry
	protected := func(h http.HandlerFunc) http.HandlerFunc {
		return withMiddleware(auth.requireAuth(h))
	}
	mux.HandleFunc("/api/v1/auth/me",     protected(auth.handleMe))
	mux.HandleFunc("/api/v1/auth/recovery-status",     protected(auth.handleRecoveryStatus))
	mux.HandleFunc("/api/v1/auth/change-password",     protected(auth.handleChangePassword))
	// OAuth bind-start (per-provider) + identity list/unbind — operator self-
	// service, distinct prefixes so they don't collide with the public /oauth/.
	mux.HandleFunc("/api/v1/auth/oauth-bind/",        protected(oauthSvc.routeBind))
	mux.HandleFunc("/api/v1/auth/oauth-identities",   protected(oauthSvc.handleIdentities))
	mux.HandleFunc("/api/v1/auth/oauth-identities/",  protected(oauthSvc.handleIdentities))
	// Operational-action failure log (opfailures.go) — admin query.
	mux.HandleFunc("/api/v1/auth/op-failures", protected(handleOpFailures))
	// DeepSeek-backed console AI assistant (bot_ai.go handleAIChat).
	mux.HandleFunc("/api/v1/auth/ai/chat", protected(handleAIChat))
	// DeepSeek ops AGENT — tool-calling loop with a human approval gate on every
	// write/command (agent.go). Operator-protected; writes admin-gated inside.
	mux.HandleFunc("/api/v1/auth/ai/agent", protected(handleAIAgent))
	mux.HandleFunc("/api/v1/auth/ai/agent/approve", protected(handleAIAgentApprove))
	// SSE streaming variants for the web assistant (live tool steps + text).
	mux.HandleFunc("/api/v1/auth/ai/agent/stream", protected(handleAIAgentStream))
	mux.HandleFunc("/api/v1/auth/ai/agent/approve/stream", protected(handleAIAgentApproveStream))
	// Per-operator conversation history + memory (ai_history_api.go).
	mux.HandleFunc("/api/v1/auth/ai/conversations", protected(handleAIConversations))
	mux.HandleFunc("/api/v1/auth/ai/conversations/", protected(handleAIConversationItem))
	mux.HandleFunc("/api/v1/auth/ai/memory", protected(handleAIMemory))
	mux.HandleFunc("/api/v1/auth/ai/memory/", protected(handleAIMemoryItem))
	// MCP bridge: same tool registry over HTTP for an external MCP server
	// (mcp/ncn-mcp.mjs) so a local Claude Code can drive the fleet. API-token
	// (Bearer) auth; writes admin-gated + audited (agent.go).
	mux.HandleFunc("/api/v1/auth/agent/tools", protected(handleAgentTools))
	mux.HandleFunc("/api/v1/auth/agent/tool", protected(handleAgentToolExec))
	// Per-purpose AI model selection (model_config.go). GET view / POST set (admin).
	mux.HandleFunc("/api/v1/auth/ai/models", protected(handleAIModels))
	mux.HandleFunc("/api/v1/auth/rpki", protected(handleRPKI))
	mux.HandleFunc("/api/v1/auth/rpki/refresh", protected(handleRPKIRefresh))
	mux.HandleFunc("/api/v1/auth/rpki/interval", protected(handleRPKIInterval))
	mux.HandleFunc("/api/v1/auth/capacity", protected(handleCapacity))
	mux.HandleFunc("/api/v1/auth/capacity/link", protected(handleCapacitySetLink))
	mux.HandleFunc("/api/v1/auth/sla/targets", protected(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handleSLATargetsSet(w, r)
		} else {
			handleSLATargets(w, r)
		}
	}))
	mux.HandleFunc("/api/v1/auth/drift", protected(handleDrift))
	mux.HandleFunc("/api/v1/auth/config-diff", protected(handleConfigDiff))
	mux.HandleFunc("/api/v1/auth/config-adopt", protected(handleConfigAdopt))
	mux.HandleFunc("/api/v1/auth/config-rollback", protected(handleConfigRollback))
	mux.HandleFunc("/api/v1/auth/oncall", protected(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handleOncallSet(w, r)
		} else {
			handleOncall(w, r)
		}
	}))
	mux.HandleFunc("/api/v1/auth/flow/top", protected(handleFlowTop))
	mux.HandleFunc("/api/v1/auth/ddos", protected(handleDDoSList))
	mux.HandleFunc("/api/v1/auth/ddos/create", protected(handleDDoSCreate))
	mux.HandleFunc("/api/v1/auth/ddos/apply", protected(handleDDoSApply))
	mux.HandleFunc("/api/v1/auth/ddos/revoke", protected(handleDDoSRevoke))
	mux.HandleFunc("/api/v1/auth/hijack", protected(handleHijack))
	mux.HandleFunc("/api/v1/debug/test-opfail", withMiddleware(handleDebugTestOpFail)) // gated by NCN_DEBUG_OPFAIL=1
	// Bot-driven Telegram bind (/bind command → /admin/bind page). GET peeks
	// the ticket, POST consumes it and binds. Operator-authenticated.
	mux.HandleFunc("/api/v1/auth/telegram/bind-ticket", protected(oauthSvc.handleTGBindTicket))
	// SSH key registration (per-operator self-service; any role).
	mux.HandleFunc("/api/v1/auth/ssh-keys", protected(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			auth.handleSSHKeysList(w, r)
		case http.MethodPost:
			auth.handleSSHKeyAdd(w, r)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET/POST only"})
		}
	}))
	mux.HandleFunc("/api/v1/auth/ssh-keys/", protected(auth.handleSSHKeyDelete))
	// API tokens — bearer auth for CLI/scripts (operator-only self-service).
	mux.HandleFunc("/api/v1/auth/api-tokens", protected(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			auth.handleAPITokenList(w, r)
		case http.MethodPost:
			auth.handleAPITokenCreate(w, r)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET/POST only"})
		}
	}))
	mux.HandleFunc("/api/v1/auth/api-tokens/", protected(auth.handleAPITokenDelete))

	// Operator → webmail self-invite (HMAC-signed token for pop-03's
	// ncn-mail to verify offline). 503 until the bridge key is installed.
	if mailBridge != nil {
		mux.HandleFunc("/api/v1/auth/mail-self-invite", protected(mailBridge.handleSelfInvite))
		// Admin-driven role mailbox recovery (proxies to ncn-mail on
		// pop-03 over the same bridge key). Admin-only — see body.
		mux.HandleFunc("/api/v1/auth/mail-role-recover",
			withMiddleware(auth.requireRole("admin", mailBridge.handleRoleRecover)))
		// Forgot-password queue mirror: list + dismiss, admin-only.
		// Proxies to the same forgot-store ncn-mail's admin panel uses,
		// so the NCN admin console can render the queue too.
		mux.HandleFunc("/api/v1/auth/mail-forgot",
			withMiddleware(auth.requireRole("admin", mailBridge.handleForgotList)))
		// /<id>/approve → POST → forward as forgot-approve via bridge.
		// /<id>       → DELETE → forward as forgot-dismiss (existing).
		// Branching happens here so each handler can keep its method check.
		mux.HandleFunc("/api/v1/auth/mail-forgot/", withMiddleware(auth.requireRole("admin",
			func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/approve") {
					mailBridge.handleForgotApprove(w, r)
					return
				}
				mailBridge.handleForgotDismiss(w, r)
			})))

		// Mutual SSO with webmail. ingest is GET (browser-redirected from
		// webmail); mail-ticket needs a live operator session.
		ssoBr := newSSOBridge(auth, mailBridge)
		mux.HandleFunc("/api/v1/auth/sso/ingest",      ssoBr.handleIngest)
		mux.HandleFunc("/api/v1/auth/sso/mail-ticket", protected(ssoBr.handleMintMailTicket))
		// Entry point for "Sign in with NCN Admin" on the webmail login
		// page. Browser hits this directly via window.location; the
		// handler resolves session-or-login internally with 302s.
		mux.HandleFunc("/api/v1/auth/sso-out",         ssoBr.handleSSOOut)
	}

	// Peering application review — admin-only. The intake handler
	// (peering.handleApply) is public; everything below this point
	// requires an authenticated admin operator session.
	mux.HandleFunc("/api/v1/auth/peering/applications",
		withMiddleware(auth.requireRole("admin", peering.handleList)))
	mux.HandleFunc("/api/v1/auth/peering/applications/",
		withMiddleware(auth.requireRole("admin", peering.handleDecide)))
	// Peering/IRR automation: generate per-peer BIRD config (bgpq4) → review →
	// confirm-gated apply. peer-apply touches routers (admin + confirm word).
	mux.HandleFunc("/api/v1/auth/peering/peer-config", withMiddleware(auth.requireRole("admin", peering.handlePeerConfig)))
	mux.HandleFunc("/api/v1/auth/peering/peer-apply", withMiddleware(auth.requireRole("admin", peering.handlePeerApply)))
	mux.HandleFunc("/api/v1/auth/peering/peer-gens", withMiddleware(auth.requireRole("admin", peering.handlePeerGens)))

	// Security audit log — admin-only. List paginates newest-first with
	// server-side filtering; stats powers the panel's 24h header strip;
	// export streams the filtered set as JSONL for offline analysis.
	// Embedded Grafana (admin-gated reverse proxy → ncn-grafana-tunnel → pop-03).
	mux.HandleFunc("/grafana/", withMiddleware(auth.requireRole("admin", newGrafanaProxy())))

	// Lightweight auth probe for nginx auth_request (gates the internal wiki at
	// admin.example.com/wiki). 204 if an admin session is present, else 401/403.
	mux.HandleFunc("/api/v1/auth/check", withMiddleware(auth.requireRole("admin", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))

	mux.HandleFunc("/api/v1/auth/audit",        withMiddleware(auth.requireRole("admin", handleAuditQuery)))
	mux.HandleFunc("/api/v1/auth/audit/stats",  withMiddleware(auth.requireRole("admin", handleAuditStats)))
	mux.HandleFunc("/api/v1/auth/audit/export", withMiddleware(auth.requireRole("admin", handleAuditExport)))

	// Incidents admin CRUD — exact /incidents handles GET/POST (list/create);
	// /incidents/ prefix handles per-item PATCH/DELETE + POST /{id}/updates.
	mux.HandleFunc("/api/v1/auth/incidents",  withMiddleware(auth.requireRole("admin", handleIncidentsAdminRoot)))
	mux.HandleFunc("/api/v1/auth/incidents/", withMiddleware(auth.requireRole("admin", handleIncidentsAdminItem)))

	// VPS billing tracker — list/create at the base path; per-item
	// PATCH/DELETE + POST /{id}/paid at the prefix.
	mux.HandleFunc("/api/v1/auth/billing/subscriptions",  withMiddleware(auth.requireRole("admin", handleBillingRoot)))
	mux.HandleFunc("/api/v1/auth/billing/subscriptions/", withMiddleware(auth.requireRole("admin", handleBillingItem)))

	// Server / PoP management (admin) — runtime-editable node registry behind
	// the /admin/servers page. Add / edit / decommission / delete / provision
	// PoPs without editing Go or restarting ncn-api.
	mux.HandleFunc("/api/v1/auth/nodes",  withMiddleware(auth.requireRole("admin", fleet.handleNodesRoot)))
	mux.HandleFunc("/api/v1/auth/nodes/", withMiddleware(auth.requireRole("admin", fleet.handleNodesItem)))

	// Alert rules / groups — user-editable, admin-gated, audited.
	if alertRulesAPI != nil {
		mux.HandleFunc("/api/v1/auth/alert-rules",   withMiddleware(auth.requireRole("admin", alertRulesAPI.handleRulesRoot)))
		mux.HandleFunc("/api/v1/auth/alert-rules/",  withMiddleware(auth.requireRole("admin", alertRulesAPI.handleRulesItem)))
		mux.HandleFunc("/api/v1/auth/alert-preview", withMiddleware(auth.requireRole("admin", alertRulesAPI.handlePreview)))
		mux.HandleFunc("/api/v1/auth/alert-groups",  withMiddleware(auth.requireRole("admin", alertRulesAPI.handleGroupsRoot)))
		mux.HandleFunc("/api/v1/auth/alert-groups/", withMiddleware(auth.requireRole("admin", alertRulesAPI.handleGroupsItem)))
		mux.HandleFunc("/api/v1/auth/alert-metrics", withMiddleware(auth.requireRole("admin", alertRulesAPI.handleMetrics)))
	}

	// FX rates (CNY-base) — feeds the billing page's CNY-equivalent rollup.
	mux.HandleFunc("/api/v1/auth/fx/rates", withMiddleware(auth.requireRole("admin", handleFXRates)))

	// TOTP enrollment — first-login MFA path (passkey path lives elsewhere).
	mux.HandleFunc("/api/v1/auth/totp/setup-begin",   protected(auth.handleTOTPSetupBegin))
	mux.HandleFunc("/api/v1/auth/totp/setup-confirm", protected(auth.handleTOTPSetupConfirm))

	// Trusted-device management. List + revoke for the current operator.
	// Same path, dispatched by HTTP method inside the handler:
	mux.HandleFunc("/api/v1/auth/devices", protected(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			auth.handleListDevices(w, r)
		case http.MethodDelete:
			auth.handleRevokeDevice(w, r)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET or DELETE only"})
		}
	}))

	// Public invite endpoints (no auth — the invite token IS the auth).
	mux.HandleFunc("/api/v1/auth/invite/preview",       withMiddleware(auth.handleInvitePreview))
	mux.HandleFunc("/api/v1/auth/invite/complete",      withMiddleware(auth.handleInviteComplete))
	mux.HandleFunc("/api/v1/auth/invite/passkey/begin", withMiddleware(auth.handleInvitePasskeyBegin))

	// Invite token management (admin only)
	mux.HandleFunc("/api/v1/auth/invites", withMiddleware(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			auth.requireRole("admin", auth.handleInvitesList)(w, r)
		case http.MethodPost:
			auth.requireRole("admin", auth.handleInvitesCreate)(w, r)
		case http.MethodDelete:
			auth.requireRole("admin", auth.handleInvitesRevoke)(w, r)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET/POST/DELETE only"})
		}
	}))
	// Resend an invite mail for an existing token — admin only.
	// Path: /api/v1/auth/invites/<token>/resend
	mux.HandleFunc("/api/v1/auth/invites/",
		withMiddleware(auth.requireRole("admin", auth.handleInviteResend)))

	// Approve a pending invite-registered operator (admin only)
	mux.HandleFunc("/api/v1/auth/operators/approve",
		withMiddleware(auth.requireRole("admin", auth.handleOperatorsApprove)))

	// Operator account management.
	//   GET    /operators          — listed for every authed user (transparency)
	//   POST   /operators          — admin only (create)
	//   DELETE /operators          — admin only
	//   PATCH  /operators          — admin only (role change)
	mux.HandleFunc("/api/v1/auth/operators", withMiddleware(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			auth.requireAuth(auth.handleOperatorsList)(w, r)
		case http.MethodPost:
			auth.requireRole("admin", auth.handleOperatorsCreate)(w, r)
		case http.MethodDelete:
			auth.requireRole("admin", auth.handleOperatorsDelete)(w, r)
		case http.MethodPatch:
			auth.requireRole("admin", auth.handleOperatorsUpdate)(w, r)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET/POST/DELETE/PATCH only"})
		}
	}))
	mux.HandleFunc("/api/v1/auth/passkey",             protected(auth.handlePasskeyList))
	mux.HandleFunc("/api/v1/auth/passkey/delete",      protected(auth.handlePasskeyDelete))
	mux.HandleFunc("/api/v1/auth/passkey/register/begin",  protected(auth.handlePasskeyRegBegin))
	mux.HandleFunc("/api/v1/auth/passkey/register/finish", protected(auth.handlePasskeyRegFinish))

	// New monitoring + ops endpoints
	mux.HandleFunc("/api/v1/perf",             protected(monitor.handlePerf))
	mux.HandleFunc("/api/v1/connectivity",     protected(monitor.handleConnectivity))
	mux.HandleFunc("/api/v1/bird",             protected(monitor.bird.handleBirdSummary))
	mux.HandleFunc("/api/v1/bird/protocol",    protected(fleet.handleBirdProtocolDetail))
	mux.HandleFunc("/api/v1/alerts",           protected(monitor.alerts.handleAlerts))
	mux.HandleFunc("/api/v1/auth/alerts/ack",  withMiddleware(auth.requireRole("admin", monitor.alerts.handleAlertAck)))
	// Prometheus scrape target (hand-rolled text format; optional NCN_METRICS_TOKEN gate).
	mux.HandleFunc("/metrics", withMiddleware(metricsHandler(fleet, monitor.alerts)))
	mux.HandleFunc("/api/v1/fleet",            protected(fleet.handleFleet))
	mux.HandleFunc("/api/v1/term",             protected(fleet.handleTerm))
	mux.HandleFunc("/api/v1/term/ticket",      protected(fleet.handleTermTicket))
	mux.HandleFunc("/api/v1/term/passkey-begin", protected(fleet.handleTermPasskeyBegin))
	mux.HandleFunc("/api/v1/term/sessions",    protected(fleet.handleTermSessions))

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      75 * time.Second, // long enough for full traceroute
		IdleTimeout:       60 * time.Second,
	}

	// Graceful drain on stop so a deploy doesn't sever in-flight requests.
	// systemd sends SIGTERM on `systemctl stop|restart`; we Shutdown (stop
	// accepting, let active handlers finish) within a bounded window, then
	// exit so the replacement process picks up the socket. Long-lived
	// handlers (terminal WS) are cut at the deadline — acceptable for a
	// deploy. SIGHUP stays wired to key-reload above, NOT to shutdown.
	go func() {
		stopCh := make(chan os.Signal, 1)
		signal.Notify(stopCh, syscall.SIGTERM, syscall.SIGINT)
		<-stopCh
		log.Printf("ncn-api: stop signal — draining (≤10s)")
		dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dcancel()
		_ = srv.Shutdown(dctx)
		cancel() // tear down background loops (fleet, heartbeat persist, …)
	}()

	// Listener: prefer a systemd socket-activation fd so `systemctl restart`
	// keeps the listening socket OPEN across the binary swap — incoming
	// connections queue in the kernel backlog instead of getting connection-
	// refused, so nginx never sees a 502 during a deploy. Falls back to
	// binding *addr directly when not socket-activated (local runs, or the
	// first deploy before the .socket unit is enabled), so the binary works
	// both ways and the socket can be rolled in separately.
	ln, lerr := apiListener(*addr)
	if lerr != nil {
		log.Fatalf("listen: %v", lerr)
	}
	log.Printf("ncn-core-console-api serving on %s", ln.Addr())
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("http: %v", err)
	}
}

// apiListener returns the systemd socket-activation listener when this process
// was started with one (LISTEN_PID == our pid, LISTEN_FDS ≥ 1, fd 3 per the
// systemd convention); otherwise it binds addr itself.
func apiListener(addr string) (net.Listener, error) {
	if pid, _ := strconv.Atoi(os.Getenv("LISTEN_PID")); pid == os.Getpid() {
		if n, _ := strconv.Atoi(os.Getenv("LISTEN_FDS")); n >= 1 {
			const sdListenFdsStart = 3 // SD_LISTEN_FDS_START
			f := os.NewFile(uintptr(sdListenFdsStart), "ncn-api-systemd-socket")
			ln, err := net.FileListener(f)
			if err == nil {
				log.Printf("ncn-api: using systemd socket-activation fd %d", sdListenFdsStart)
				return ln, nil
			}
			log.Printf("ncn-api: socket-activation fd unusable (%v) — binding %s instead", err, addr)
		}
	}
	return net.Listen("tcp", addr)
}
