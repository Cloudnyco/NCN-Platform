// Server onboarding — the "make a new box live" machinery behind the admin
// Servers page, beyond the plain registry CRUD in nodes_api.go:
//
//   1. Geo autodetect   — turn an IP into country / city / lat / lon so the
//                         operator doesn't hand-type the map position.
//   2. SSH key bootstrap — using a ONE-TIME root password the operator
//                         supplies, install the fleet public key into the
//                         box's authorized_keys so all later access is
//                         key-based. The password is used in-memory for that
//                         single SSH session and never stored or logged.
//   3. Onboard job      — runs key-bootstrap → provision → verify as a
//                         tracked, pollable, step-by-step job so the UI can
//                         show live progress.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ───────────────────────────── Geo autodetect ─────────────────────────────

type geoResult struct {
	Country string  `json:"country"` // ISO-3166 alpha-2
	Label   string  `json:"label"`   // human label, e.g. "Region B, DE"
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	Source  string  `json:"source"` // "ipwho.is" | "cymru" | "none"
}

// countryCentroid is a small built-in lat/lon table used as the fallback when
// the external city-level lookup is unavailable — country-accurate only, the
// operator fine-tunes from there. Covers where PoPs realistically land.
var countryCentroid = map[string][2]float64{
	"HK": {22.32, 114.17}, "JP": {35.68, 139.69}, "TW": {25.03, 121.56},
	"SG": {1.30, 103.82}, "US": {37.75, -97.82}, "DE": {51.16, 10.45},
	"GB": {54.0, -2.0}, "FR": {46.6, 2.2}, "NL": {52.13, 5.29},
	"KR": {36.5, 127.8}, "CN": {35.86, 104.20}, "CA": {56.13, -106.35},
	"AU": {-25.27, 133.78}, "IN": {20.59, 78.96}, "BR": {-14.24, -51.93},
	"RU": {61.52, 105.32}, "ZA": {-30.56, 22.94}, "AE": {23.42, 53.85},
	"SE": {60.13, 18.64}, "CH": {46.82, 8.23}, "PL": {51.92, 19.15},
	"ID": {-0.79, 113.92}, "MY": {4.21, 101.98}, "TH": {15.87, 100.99},
	"VN": {14.06, 108.28}, "PH": {12.88, 121.77}, "IT": {41.87, 12.57},
	"ES": {40.46, -3.75}, "FI": {61.92, 25.75}, "NO": {60.47, 8.47},
}

// handleNodeGeo — GET /api/v1/auth/nodes/geo?address=<ip>. Best-effort geo
// lookup for the add-server form. Tries the external city-level service first,
// then falls back to the in-house Cymru country lookup + a country centroid,
// so it always returns SOMETHING usable (operator can edit). Never fatal.
func (f *fleetScraper) handleNodeGeo(w http.ResponseWriter, r *http.Request) {
	addr := strings.TrimSpace(r.URL.Query().Get("address"))
	ip := net.ParseIP(addr)
	if ip == nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "address must be a valid IP"})
		return
	}
	if g, ok := geoViaIPWhois(addr); ok {
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: g})
		return
	}
	// Fallback: Cymru (in-house, DNS-based) for country + centroid for coords.
	info := &visitorInfo{}
	resolveVisitor(info, ip)
	g := geoResult{Country: strings.ToUpper(info.Country), Source: "cymru"}
	if g.Country == "" {
		g.Source = "none"
	} else {
		g.Label = g.Country
		if c, ok := countryCentroid[g.Country]; ok {
			g.Lat, g.Lon = c[0], c[1]
		}
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: g})
}

// geoViaIPWhois queries ipwho.is (free, HTTPS, no API key) for city-level geo.
// One-shot, 5s budget. The PoP IP is already public (announced via BGP), so
// the lookup leaks nothing sensitive. Returns ok=false on any failure so the
// caller falls back to the in-house path.
func geoViaIPWhois(ip string) (geoResult, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://ipwho.is/"+ip, nil)
	if err != nil {
		return geoResult{}, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return geoResult{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return geoResult{}, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return geoResult{}, false
	}
	var p struct {
		Success     bool    `json:"success"`
		City        string  `json:"city"`
		Country     string  `json:"country"`
		CountryCode string  `json:"country_code"`
		Latitude    float64 `json:"latitude"`
		Longitude   float64 `json:"longitude"`
	}
	if err := json.Unmarshal(body, &p); err != nil || !p.Success || p.CountryCode == "" {
		return geoResult{}, false
	}
	cc := strings.ToUpper(p.CountryCode)
	label := cc
	if p.City != "" {
		label = p.City + ", " + cc
	}
	return geoResult{Country: cc, Label: label, Lat: p.Latitude, Lon: p.Longitude, Source: "ipwho.is"}, true
}

// ───────────────────────────── Onboard job ─────────────────────────────

type onboardStep struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // pending | running | ok | fail | skip
	Message string `json:"message,omitempty"`
	At      int64  `json:"at,omitempty"`      // last update (ms epoch)
	Started int64  `json:"started,omitempty"` // when it first entered "running" (ms)
	Ended   int64  `json:"ended,omitempty"`   // when it reached a terminal status (ms)
}

// onboardView is the mutex-free, serializable snapshot of a job (what the
// poll endpoint returns). Kept separate so we never copy the job's mutex.
type onboardView struct {
	NodeID    string        `json:"node_id"`
	Steps     []onboardStep `json:"steps"`
	Log       []string      `json:"log"` // live, streamed output (capped)
	Running   bool          `json:"running"`
	Done      bool          `json:"done"`
	OK        bool          `json:"ok"`
	StartedAt int64         `json:"started_at"`
}

const onboardLogCap = 400 // keep the last N lines of streamed output

type onboardJob struct {
	mu        sync.Mutex
	nodeID    string
	steps     []onboardStep
	log       []string
	running   bool
	done      bool
	ok        bool
	startedAt int64
}

func (j *onboardJob) snapshot() onboardView {
	j.mu.Lock()
	defer j.mu.Unlock()
	return onboardView{
		NodeID:    j.nodeID,
		Steps:     append([]onboardStep(nil), j.steps...),
		Log:       append([]string(nil), j.log...),
		Running:   j.running,
		Done:      j.done,
		OK:        j.ok,
		StartedAt: j.startedAt,
	}
}

func (j *onboardJob) set(i int, status, msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if i < 0 || i >= len(j.steps) {
		return
	}
	nowMs := time.Now().UnixMilli()
	st := &j.steps[i]
	st.Status = status
	st.Message = msg
	st.At = nowMs
	// Stamp Started on the first transition into "running" (the provision step
	// re-enters set() on every streamed line, so guard against overwriting),
	// and Ended once it reaches a terminal status. These drive the per-step
	// duration + overall progress in the UI.
	if status == "running" && st.Started == 0 {
		st.Started = nowMs
	}
	if (status == "ok" || status == "fail" || status == "skip") && st.Ended == 0 {
		if st.Started == 0 {
			st.Started = nowMs // skipped steps that never ran
		}
		st.Ended = nowMs
	}
}

// appendLog adds one streamed output line (capped to the last onboardLogCap).
func (j *onboardJob) appendLog(line string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.log = append(j.log, line)
	if len(j.log) > onboardLogCap {
		j.log = j.log[len(j.log)-onboardLogCap:]
	}
}

// handleNodeOnboard dispatches POST (start) / GET (poll status) for
// /api/v1/auth/nodes/{id}/onboard.
func (f *fleetScraper) handleNodeOnboard(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		f.onboardMu.Lock()
		job := f.onboard[id]
		f.onboardMu.Unlock()
		if job == nil {
			writeJSON(w, http.StatusOK, envelope{OK: true, Data: nil})
			return
		}
		snap := job.snapshot()
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: snap})
	case http.MethodPost:
		f.startOnboard(w, r, id)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
	}
}

// startOnboard kicks off an onboarding job for a registered node. Body:
//
//	{ "ssh_user"?: "root", "ssh_password"?: "...",
//	  "ssh_private_key"?: "-----BEGIN ...", "ssh_key_passphrase"?: "..." }
//
// If ssh_password OR ssh_private_key is present the job first bootstraps the
// fleet key over that first-contact SSH session; otherwise it assumes key auth
// already works and the key-install step is skipped. The private key is for the
// case where the box has password auth disabled — supply a key that already has
// access. Both the password and the supplied key are consumed by the goroutine,
// in-memory only, never persisted or logged.
func (f *fleetScraper) startOnboard(w http.ResponseWriter, r *http.Request, id string) {
	op := adminOperator(r)
	if id == f.localID {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "the local console node does not onboard a remote agent"})
		return
	}
	rec, ok := f.registry.get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "node not found — add it first"})
		return
	}
	var req struct {
		SSHUser          string `json:"ssh_user"`
		SSHPassword      string `json:"ssh_password"`
		SSHPrivateKey    string `json:"ssh_private_key"` // ONLY when password auth isn't available
		SSHKeyPassphrase string `json:"ssh_key_passphrase"`
	}
	// allow a larger body so a PEM private key fits
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<17)).Decode(&req)
	user := strings.TrimSpace(req.SSHUser)
	if user == "" {
		user = rec.SSHUser
	}
	if user == "" {
		user = "root"
	}
	password := req.SSHPassword                     // one-time, in-memory only
	privKey := strings.TrimSpace(req.SSHPrivateKey) // one-time, in-memory only (used only if no password)
	keyPass := req.SSHKeyPassphrase

	f.onboardMu.Lock()
	if cur := f.onboard[id]; cur != nil {
		if s := cur.snapshot(); s.Running {
			f.onboardMu.Unlock()
			writeJSON(w, http.StatusConflict, envelope{OK: false, Error: "onboarding already in progress for " + id})
			return
		}
	}
	steps := []onboardStep{}
	keyStep := -1
	if password != "" || privKey != "" {
		keyStep = len(steps)
		steps = append(steps, onboardStep{Name: "安装 fleet 公钥 · install SSH key", Status: "pending"})
	}
	provStep := len(steps)
	steps = append(steps, onboardStep{Name: "配置 agent · provision", Status: "pending"})
	verifyStep := len(steps)
	steps = append(steps, onboardStep{Name: "校验 agent · verify", Status: "pending"})

	job := &onboardJob{nodeID: id, steps: steps, running: true, startedAt: time.Now().Unix()}
	f.onboard[id] = job
	f.onboardMu.Unlock()

	bootstrapMethod := "none"
	if privKey != "" {
		bootstrapMethod = "private_key"
	} else if password != "" {
		bootstrapMethod = "password"
	}
	auditRecord(r, AuditEvent{Event: "node.onboard", Severity: auditSevWarn, Actor: op, Target: id,
		Details: map[string]any{"key_bootstrap": keyStep >= 0, "bootstrap_method": bootstrapMethod}})

	go f.runOnboard(job, rec, user, password, privKey, keyPass, op, keyStep, provStep, verifyStep)

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: job.snapshot()})
}

func (f *fleetScraper) runOnboard(job *onboardJob, rec nodeRecord, user, password, privateKey, keyPass, op string, keyStep, provStep, verifyStep int) {
	finish := func(ok bool) {
		job.mu.Lock()
		job.running = false
		job.done = true
		job.ok = ok
		job.mu.Unlock()
	}

	// Step 1: install fleet key over a first-contact SSH session (if creds were
	// given). Auth is by the operator's one-time PASSWORD, or — only when
	// password auth isn't available on the box — a one-time PRIVATE KEY.
	if keyStep >= 0 {
		job.set(keyStep, "running", "")
		port := rec.SSHPort
		if port <= 0 {
			port = 22
		}
		method := "password"
		if privateKey != "" {
			method = "private key"
		}
		job.appendLog(fmt.Sprintf("[key] connecting to %s:%d as %s (%s) …", rec.Address, port, user, method))
		if err := bootstrapFleetKey(rec.Address, port, user, password, privateKey, keyPass); err != nil {
			job.set(keyStep, "fail", err.Error())
			job.appendLog("[key] FAILED · " + err.Error())
			recordOpFailure(f.notify, &opFailure{Kind: opKindOnboard, Target: rec.ID, Actor: op, Reason: "key bootstrap: " + err.Error()})
			finish(false)
			return
		}
		job.set(keyStep, "ok", "fleet 公钥已安装并校验")
		job.appendLog("[key] fleet public key installed + key-auth verified")
	}
	password, privateKey, keyPass = "", "", "" // drop secrets the moment they're no longer needed

	// Step 2: provision — stream the script's output line-by-line into the job
	// log so the UI shows live progress, and surface the latest line as the
	// step's message.
	job.set(provStep, "running", "")
	exit, err := f.execProvision(rec.ID, rec, func(l string) {
		job.appendLog(l)
		job.set(provStep, "running", l)
	})
	if err != nil || exit != 0 {
		reason := fmt.Sprintf("exit=%d", exit)
		if err != nil {
			reason = err.Error()
		}
		job.set(provStep, "fail", reason)
		job.appendLog("[provision] FAILED · " + reason)
		recordOpFailure(f.notify, &opFailure{Kind: opKindOnboard, Target: rec.ID, Actor: op, Reason: "provision: " + reason})
		finish(false)
		return
	}
	f.ReloadAgentKeys()
	job.set(provStep, "ok", "agent 已部署")

	// Step 3: verify the agent answers /v1/healthz.
	job.set(verifyStep, "running", "")
	job.appendLog("[verify] GET https://" + rec.Address + ":9101/v1/healthz …")
	if err := f.verifyAgentHealth(rec.Address); err != nil {
		job.set(verifyStep, "fail", err.Error())
		job.appendLog("[verify] FAILED · " + err.Error())
		recordOpFailure(f.notify, &opFailure{Kind: opKindOnboard, Target: rec.ID, Actor: op, Reason: "verify: " + err.Error()})
		finish(false)
		return
	}
	job.set(verifyStep, "ok", "agent 健康")
	job.appendLog("[verify] agent healthy · onboard complete")

	f.notify.NotifyEvent("🟢", "Server onboarded", []tgField{{"node", rec.ID}, {"label", rec.Label}, {"by", op}}, false)
	finish(true)
}

// verifyAgentHealth GETs the agent's /v1/healthz via the CA-pinned client.
func (f *fleetScraper) verifyAgentHealth(addr string) error {
	if f.agentClient == nil {
		return fmt.Errorf("agent transport not initialised (no agent CA)")
	}
	url := fmt.Sprintf("https://%s:9101/v1/healthz", addr)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := f.agentClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz status %d", resp.StatusCode)
	}
	return nil
}

// ───────────────────────────── SSH key bootstrap ─────────────────────────────

// bootstrapFleetKey opens ONE first-contact SSH session to a fresh box,
// appends the fleet public key to the login user's authorized_keys
// (idempotent), then re-dials with the fleet PRIVATE key to confirm key auth
// now works.
//
// Auth: the operator's one-time root PASSWORD is the default. When the box has
// password auth disabled (PasswordAuthentication no), the operator may instead
// supply a one-time PRIVATE KEY that already grants access (e.g. the provider's
// initial root key). Both the password and the supplied key live only on the
// stack here — never persisted, never logged. Exactly one of password / key is
// used; the supplied key (if any) wins.
//
// Host key: we have no prior knowledge of a brand-new box, so the bootstrap
// dial uses TOFU (InsecureIgnoreHostKey). This is the one unavoidable trust
// step in first-contact onboarding; all subsequent access is key-pinned via
// the fleet known_hosts the provision script populates with accept-new.
func bootstrapFleetKey(address string, port int, user, password, privateKey, keyPassphrase string) error {
	if port <= 0 {
		port = 22
	}
	pubBytes, err := os.ReadFile("/etc/ncn-core-console/fleet-key.pub")
	if err != nil {
		return fmt.Errorf("read fleet pubkey: %w", err)
	}
	pub := strings.TrimSpace(string(pubBytes))
	if pub == "" || strings.ContainsAny(pub, "'\n\r") {
		return fmt.Errorf("fleet pubkey malformed")
	}

	// Pick the first-contact auth method: supplied private key (only when
	// needed) takes precedence, otherwise the one-time password.
	var auth ssh.AuthMethod
	dialDesc := "password"
	if privateKey != "" {
		var signer ssh.Signer
		var perr error
		if keyPassphrase != "" {
			signer, perr = ssh.ParsePrivateKeyWithPassphrase([]byte(privateKey), []byte(keyPassphrase))
		} else {
			signer, perr = ssh.ParsePrivateKey([]byte(privateKey))
		}
		if perr != nil {
			return fmt.Errorf("parse supplied SSH private key (wrong passphrase or unsupported format?): %w", perr)
		}
		auth = ssh.PublicKeys(signer)
		dialDesc = "private key"
	} else {
		auth = ssh.Password(password)
	}

	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TOFU — first contact, no known host yet
		Timeout:         12 * time.Second,
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(address, strconv.Itoa(port)), cfg)
	if err != nil {
		return fmt.Errorf("%s SSH dial failed (check user/credentials/port %d): %w", dialDesc, port, err)
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("open session: %w", err)
	}
	// Single-quote the pubkey (validated above to contain no quote/newline) so
	// the remote shell treats it literally. Idempotent: only append if absent.
	q := "'" + pub + "'"
	cmd := "set -e; mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && " +
		"grep -qxF " + q + " ~/.ssh/authorized_keys || printf '%s\\n' " + q + " >> ~/.ssh/authorized_keys"
	if out, err := sess.CombinedOutput(cmd); err != nil {
		sess.Close()
		return fmt.Errorf("install key: %w · %s", err, strings.TrimSpace(string(out)))
	}
	sess.Close()

	// Confirm key auth now works (so we fail loudly here rather than at the
	// provision step if e.g. the box ignores authorized_keys).
	if err := verifyFleetKeyAuth(address, port, user); err != nil {
		return fmt.Errorf("key installed but key-auth still failing: %w", err)
	}
	return nil
}

// verifyFleetKeyAuth dials with the fleet PRIVATE key and runs `true`.
func verifyFleetKeyAuth(address string, port int, user string) error {
	if port <= 0 {
		port = 22
	}
	keyBytes, err := os.ReadFile("/etc/ncn-core-console/fleet-key")
	if err != nil {
		return fmt.Errorf("read fleet key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return fmt.Errorf("parse fleet key: %w", err)
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         12 * time.Second,
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(address, strconv.Itoa(port)), cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	return sess.Run("true")
}
