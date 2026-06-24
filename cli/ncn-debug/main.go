// ncn-debug — authenticated debug CLI for the NCN console REST API.
//
// A thin, read-only client over the same /api/v1 surface the web console
// uses, for ops members who'd rather stay in a terminal. Single static binary
// (CGO off), zero config beyond a token. The only dependency is
// golang.org/x/term, for the interactive console's line editing / history /
// Tab completion.
//
// Auth: a personal API token (mint one in admin.example.com → Security → API
// Tokens; format `ncntok_…`). Resolution order, highest first:
//
//	--token <tok>            flag
//	$NCN_TOKEN               environment
//	~/.config/ncn-cli/token  file written by `ncn-debug token <tok>`
//
// The `status` command is public and needs no token.
//
// Usage:
//
//	ncn-debug [--host URL] [--json] [--timeout D] <command> [args]
//
// Commands:
//
//	whoami              verify the token; show operator, role, session TTL
//	fleet               one-line-per-PoP health table
//	node <id>           full detail for one PoP (cpu/mem/disk/bird/probes/…)
//	bgp [id]            BGP sessions across the fleet, or just one PoP
//	incidents           open + recent (30-day) incidents
//	alerts [--all]      active alerts (firing); --all adds recent history
//	rpki                ROA-validity summary for our prefixes (+ live ROV)
//	oncall              who is on call now + the rotation
//	peering [--pending] peering applications (default: pending only)
//	errors [--all]      operation-failure log (default: open only)
//	audit [--limit N]   recent audit-log events (default 20)
//	status              public uptime summary (no token required)
//	get <path>          raw authenticated GET to any /api path, pretty JSON
//	token <ncntok_…>    save a token to ~/.config/ncn-cli/token (0600)
//	console             interactive REPL (Tab completion, history); default on a tty
//	completion [sh]     print a bash/zsh completion script
//	welcome             print the ASCII banner
//	help [command]      overview, or detailed help for one command
//
// Build:
//
//	cd cli/ncn-debug && go build -o ncn-debug
//	GOOS=linux  GOARCH=amd64 go build -o ncn-debug-linux-amd64
//	GOOS=darwin GOARCH=arm64 go build -o ncn-debug-darwin-arm64
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
)

const defaultHost = "https://admin.example.com"

// Globals resolved in main(), read by command funcs.
var (
	gHost    string
	gToken   string
	gJSON    bool
	gTimeout time.Duration
	gColor   bool
)

func main() {
	args := os.Args[1:]

	// Hand-rolled flag scan so flags may sit before OR after the command,
	// and so `--flag value` and `--flag=value` both work. Anything not a
	// recognized flag is positional (the command + its args).
	gHost = envOr("NCN_HOST", defaultHost)
	gTimeout = 20 * time.Second
	gColor = colorEnabled() // set early so the banner/usage is colored on every path
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json" || a == "-json":
			gJSON = true
		case a == "--host" || a == "-host":
			i++
			if i < len(args) {
				gHost = args[i]
			}
		case strings.HasPrefix(a, "--host="):
			gHost = strings.TrimPrefix(a, "--host=")
		case a == "--token" || a == "-token":
			i++
			if i < len(args) {
				gToken = args[i]
			}
		case strings.HasPrefix(a, "--token="):
			gToken = strings.TrimPrefix(a, "--token=")
		case a == "--timeout" || a == "-timeout":
			i++
			if i < len(args) {
				if d, err := time.ParseDuration(args[i]); err == nil {
					gTimeout = d
				}
			}
		case a == "-h" || a == "--help":
			usage()
			return
		case strings.HasPrefix(a, "-"):
			// Global flags (above) may sit anywhere. Any other flag is only
			// valid AFTER the command — pass it through for the subcommand to
			// interpret (e.g. `audit --limit 5`, `peering --all`). A flag
			// before the command is a genuine unknown.
			if len(pos) > 0 {
				pos = append(pos, a)
			} else {
				fmt.Fprintf(os.Stderr, "ncn-debug: unknown flag %q\n", a)
				os.Exit(2)
			}
		default:
			pos = append(pos, a)
		}
	}

	gHost = strings.TrimRight(gHost, "/")

	// No command: drop into the interactive console when we're on a terminal,
	// otherwise print usage (keeps pipes/scripts predictable).
	if len(pos) == 0 {
		if isInteractive() {
			exitOnErr(cmdConsole())
			return
		}
		usage()
		os.Exit(2)
	}

	cmd, rest := pos[0], pos[1:]
	if cmd == "console" || cmd == "repl" || cmd == "shell" {
		exitOnErr(cmdConsole())
		return
	}

	if err := runCommand(cmd, rest); err != nil {
		if errors.Is(err, errUnknownCmd) {
			fmt.Fprintf(os.Stderr, "ncn-debug: %v\n\n", err)
			usage()
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "ncn-debug: "+err.Error())
		os.Exit(1)
	}
}

func exitOnErr(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ncn-debug: "+err.Error())
		os.Exit(1)
	}
}

var errUnknownCmd = errors.New("unknown command")

// runCommand dispatches a single command. Shared by the one-shot CLI and the
// interactive console so both stay in lockstep.
func runCommand(cmd string, rest []string) error {
	switch cmd {
	case "whoami":
		return cmdWhoami()
	case "fleet":
		return cmdFleet()
	case "node":
		return cmdNode(rest)
	case "bgp":
		return cmdBGP(rest)
	case "incidents":
		return cmdIncidents()
	case "alerts":
		return cmdAlerts(rest)
	case "rpki":
		return cmdRPKI()
	case "oncall":
		return cmdOncall()
	case "peering":
		return cmdPeering(rest)
	case "errors", "opfail":
		return cmdOpFailures(rest)
	case "audit":
		return cmdAudit(rest)
	case "status":
		return cmdStatus()
	case "get":
		return cmdGet(rest)
	case "token":
		return cmdToken(rest)
	case "completion":
		return cmdCompletion(rest)
	case "welcome", "banner":
		fmt.Println(banner())
		return nil
	case "help", "?":
		fmt.Print(helpText(rest))
		return nil
	default:
		return fmt.Errorf("%w %q (try `help`)", errUnknownCmd, cmd)
	}
}

// isInteractive reports whether stdin is a terminal, so a bare invocation can
// open the console instead of printing usage.
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// cmdConsole runs an interactive REPL over the same commands. On a real
// terminal it uses golang.org/x/term for line editing, ↑/↓ history and Tab
// completion; on piped stdin it falls back to a plain line reader (so
// `echo cmds | ncn-debug console` still works).
func cmdConsole() error {
	fmt.Println(banner())
	fmt.Println(dim("interactive console · Tab completes · ↑/↓ history · `help` · `exit`/Ctrl-D to quit"))
	if _, err := resolveToken(); err != nil {
		fmt.Println(yellow("no API token set — only `status` works until you run `token <ncntok_…>`"))
	}
	if isInteractive() {
		if err := consoleInteractive(); err == nil || err != errNotATerminal {
			return err
		}
		// MakeRaw failed (e.g. emulated tty) — fall through to the plain loop.
	}
	return consolePiped()
}

var errNotATerminal = errors.New("not a terminal")

// consoleBuiltins are REPL-only commands (handled here, not in runCommand).
const consoleBuiltins = "help history clear exit quit"

// consoleConsumes is the shared per-line handler. Returns done=true to leave.
func consoleConsumes(line string, out io.Writer) (done bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	cmd, rest := fields[0], fields[1:]
	// Persist to history — but NEVER a `token …` line (would write the secret
	// to disk in plaintext).
	if cmd != "token" {
		appendHistory(line)
	}
	switch cmd {
	case "exit", "quit", "q":
		return true
	case "help", "?":
		fmt.Fprint(out, helpText(rest))
		return false
	case "history":
		printHistory(out)
		return false
	case "clear":
		fmt.Fprint(out, "\x1b[H\x1b[2J")
		return false
	}
	if err := runCommand(cmd, rest); err != nil {
		fmt.Fprintln(os.Stderr, red("error: ")+err.Error())
	}
	return false
}

// ─────────────── console history (cross-session, secret-safe) ───────────────

func historyPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ncn-cli", "history"), nil
}

// appendHistory appends one command line to the history file (best-effort;
// silently no-ops on error so the REPL never breaks over it).
func appendHistory(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	p, err := historyPath()
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

// printHistory shows the last 50 lines of the history file.
func printHistory(out io.Writer) {
	p, err := historyPath()
	if err != nil {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		fmt.Fprintln(out, dim("no history yet"))
		return
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	const max = 50
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	for i, l := range lines {
		fmt.Fprintf(out, "%s  %s\n", dim(fmt.Sprintf("%3d", i+1)), l)
	}
}

// consolePiped is the plain line-reader path (no editing/history) for
// non-terminal stdin.
func consolePiped() error {
	sc := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print(cyan("ncn") + dim("› "))
		if !sc.Scan() {
			fmt.Println()
			return nil
		}
		if consoleConsumes(sc.Text(), os.Stdout) {
			return nil
		}
	}
}

// consoleInteractive is the rich path: raw-mode line editing + history + Tab
// completion via x/term. Command output runs in cooked mode so handlers'
// "\n" still format correctly.
func consoleInteractive() error {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return errNotATerminal
	}
	defer term.Restore(fd, old)

	t := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{os.Stdin, os.Stdout}, "")
	t.SetPrompt(string(t.Escape.Cyan) + "ncn› " + string(t.Escape.Reset))
	t.AutoCompleteCallback = completeConsole

	for {
		line, err := t.ReadLine()
		if err != nil { // io.EOF on Ctrl-D, ErrInterrupt on Ctrl-C
			fmt.Fprintln(t)
			return nil
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Drop to cooked mode so the command's plain stdout writes format
		// normally, then return to raw for the next line.
		term.Restore(fd, old)
		done := consoleConsumes(line, os.Stdout)
		if _, e := term.MakeRaw(fd); e != nil || done {
			return nil
		}
	}
}

// nodeIDCache memoizes the fleet's PoP ids for `node`/`bgp` Tab completion.
var nodeIDCache []string

func nodeIDs() []string {
	if nodeIDCache != nil {
		return nodeIDCache
	}
	nodes, err := fetchFleet()
	if err != nil {
		return nil // no token / unreachable — just skip id completion
	}
	for _, n := range nodes {
		nodeIDCache = append(nodeIDCache, n.Node.ID)
	}
	return nodeIDCache
}

// flagsFor returns the flags a given command accepts (for Tab completion).
func flagsFor(cmd string) []string {
	switch cmd {
	case "alerts", "errors", "opfail":
		return []string{"--all", "--json"}
	case "peering":
		return []string{"--all", "--pending", "--json"}
	case "audit":
		return []string{"--limit", "--json"}
	default:
		return []string{"--json"}
	}
}

// completeConsole is the Tab-completion callback. It completes, by position:
//   - the command word → command/builtin names
//   - a `-flag` token  → the flags that command accepts
//   - the arg of node/bgp → live PoP ids
func completeConsole(line string, pos int, key rune) (string, int, bool) {
	if key != '\t' {
		return "", 0, false
	}
	head := line[:pos]
	tail := line[pos:]
	endsSpace := strings.HasSuffix(head, " ")
	toks := strings.Fields(head)

	// Completing the command word (nothing typed yet, or one partial token).
	if len(toks) == 0 || (len(toks) == 1 && !endsSpace) {
		partial := ""
		if len(toks) == 1 {
			partial = toks[0]
		}
		cands := append(strings.Fields(completionCommands), strings.Fields(consoleBuiltins)...)
		return applyCompletion(head, tail, partial, filterPrefix(cands, partial))
	}

	// Completing an argument/flag of an already-typed command.
	cmd := toks[0]
	partial := ""
	if !endsSpace {
		partial = toks[len(toks)-1]
	}
	var cands []string
	switch {
	case strings.HasPrefix(partial, "-"):
		cands = filterPrefix(flagsFor(cmd), partial)
	case cmd == "node" || cmd == "bgp":
		cands = filterPrefix(nodeIDs(), partial)
	default:
		return "", 0, false
	}
	return applyCompletion(head, tail, partial, cands)
}

func filterPrefix(all []string, prefix string) []string {
	var out []string
	for _, s := range all {
		if strings.HasPrefix(s, prefix) {
			out = append(out, s)
		}
	}
	return out
}

// applyCompletion replaces the trailing `partial` of head with the longest
// common prefix of the candidates (adding a space when unambiguous), and
// rebuilds the full line for the term callback.
func applyCompletion(head, tail, partial string, cands []string) (string, int, bool) {
	if len(cands) == 0 {
		return "", 0, false
	}
	common := cands[0]
	for _, c := range cands[1:] {
		common = commonPrefix(common, c)
	}
	if len(cands) == 1 {
		common += " "
	}
	if common == partial {
		return "", 0, false // nothing new to add
	}
	newHead := head[:len(head)-len(partial)] + common
	return newHead + tail, len(newHead), true
}

func commonPrefix(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return a[:i]
}

// appVersion is surfaced in the banner and the HTTP User-Agent.
const appVersion = "1.1.0"

// banner returns the colored ASCII welcome art + tagline.
func banner() string {
	var art string
	switch w := termWidth(); {
	case w == 0 || w >= 104:
		art = bannerWide
	case w >= 57:
		art = bannerMedium
	default:
		return cyan(" N·C·N ") + dim("— NCN · debug cli v"+appVersion)
	}
	return cyan(art) + "\n    network operations · debug cli  " + dim("v"+appVersion)
}

// termWidth returns stdout's column count, or 0 when it isn't a terminal
// (piped/redirected — nothing to wrap), so banner() can size the art to fit.
func termWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 0
}

// bannerWide is the single-line art (needs ~104 cols).
const bannerWide = `
  _   _  ____ _   _
 | \ | |/ ___| \ | |
 |  \| | |   |  \| |
 | |\  | |___| |\  |
 |_| \_|\____|_| \_|`

// bannerMedium is the two-line art (fits ~57 cols).
const bannerMedium = `
  _   _  ____ _   _
 | \ | |/ ___| \ | |
 |  \| | |   |  \| |
 | |\  | |___| |\  |
 |_| \_|\____|_| \_|`

// helpDetails holds the per-command long help shown by `help <command>`.
// Each entry: a usage line followed by indented description / examples.
var helpDetails = map[string]string{
	"whoami":     "whoami\n  Verify your token and print the operator, role and session TTL.",
	"fleet":      "fleet\n  One line per PoP: load, CPU, mem, disk, BIRD version, BGP\n  established/total, WireGuard & tunnel counts, scrape latency.",
	"node":       "node <id>\n  Full detail for one PoP: host, uptime, cpu/mem/disk, probes,\n  BIRD protocols, route counts, tunnels.\n  In the console, Tab completes the id.   e.g.  node ctrl-01",
	"bgp":        "bgp [id]\n  BGP sessions across the whole fleet, or just one PoP.\n  e.g.  bgp        (all)\n        bgp pop-03 (one)",
	"incidents":  "incidents\n  Open + recent (30-day) incidents from the public status feed.",
	"alerts":     "alerts [--all]\n  Active (firing) alerts. --all also lists recent history.",
	"rpki":       "rpki\n  ROA-validity summary for our prefixes (valid/invalid/unknown),\n  the live ROV state from a PoP, and the auto-poll interval.",
	"oncall":     "oncall\n  Who is on call right now, plus the rotation and shift length.",
	"peering":    "peering [--all|--pending]\n  Peering applications. Default shows pending only; --all shows\n  every status (approved/rejected too).",
	"errors":     "errors [--all]            (alias: opfail)\n  Operation-failure log. Default shows open only; --all shows\n  dismissed ones too.",
	"audit":      "audit [--limit N]\n  Recent audit-log events (default 20). e.g.  audit --limit 50",
	"status":     "status\n  Public uptime summary across PoPs and services. Needs NO token.",
	"get":        "get <path>\n  Raw authenticated GET to any /api path; prints pretty JSON.\n  The escape hatch for endpoints without a dedicated command.\n  e.g.  get /api/v1/auth/capacity",
	"token":      "token <ncntok_…>\n  Save an API token to ~/.config/ncn-cli/token (mode 0600).\n  Mint one at the console → Security → API Tokens.\n  (Never recorded in console history.)",
	"completion": "completion [bash|zsh]\n  Print a shell-completion script.\n  e.g.  source <(ncn-debug completion bash)",
	"console":    "console            (also: repl, shell)\n  Interactive REPL: Tab completion, ↑/↓ history, `help`, `clear`,\n  `history`, `exit`. Bare `ncn-debug` on a terminal opens it too.",
	"welcome":    "welcome            (also: banner)\n  Print the NCN ASCII banner.",
	"history":    "history            (console only)\n  Show the last 50 commands from ~/.config/ncn-cli/history.",
	"clear":      "clear              (console only)\n  Clear the screen.",
	"exit":       "exit               (console only; also: quit, q, Ctrl-D)\n  Leave the interactive console.",
}

// helpText returns the help body: a per-command detail for `help <cmd>`, or a
// command overview for bare `help`.
func helpText(args []string) string {
	if len(args) > 0 {
		name := args[0]
		if name == "opfail" {
			name = "errors"
		}
		if name == "banner" {
			name = "welcome"
		}
		if h, ok := helpDetails[name]; ok {
			return h + "\n"
		}
		return fmt.Sprintf("no help for %q — `help` lists all commands\n", args[0])
	}
	return `ncn-debug commands — ` + dim("`help <command>` for details") + `

  whoami            verify token; operator, role, session TTL
  fleet             per-PoP health table
  node <id>         full detail for one PoP
  bgp [id]          BGP sessions (all PoPs, or one)
  incidents         open + recent incidents
  alerts [--all]    active alerts (+ history)
  rpki              ROA validity + live ROV
  oncall            current on-call + rotation
  peering [--all]   peering applications
  errors [--all]    operation-failure log
  audit [--limit N] recent audit events
  status            public uptime (no token)
  get <path>        raw GET to any /api path
  token <tok>       save an API token
  console           interactive REPL
  completion [sh]   shell-completion script
  welcome           print the banner

  ` + dim("console builtins: help · history · clear · exit") + `
`
}

func usage() {
	fmt.Fprintln(os.Stderr, banner())
	fmt.Fprint(os.Stderr, `
ncn-debug — authenticated debug CLI for the NCN console API

Usage:
  ncn-debug [--host URL] [--json] [--timeout D] <command> [args]

Commands:
  whoami            verify token; show operator, role, session TTL
  fleet             one-line-per-PoP health table
  node <id>         full detail for one PoP
  bgp [id]          BGP sessions across the fleet (or just one PoP)
  incidents         open + recent (30-day) incidents
  alerts [--all]    active alerts (firing); --all also lists recent history
  rpki              ROA-validity summary for our prefixes (+ live ROV)
  oncall            who is on call now + the rotation
  peering [--pending] peering applications (default: pending only)
  errors [--all]    operation-failure log (default: open only)   [alias: opfail]
  audit [--limit N] recent audit-log events (default 20)
  status            public uptime summary (no token needed)
  get <path>        raw authenticated GET to any /api path (pretty JSON)
  token <ncntok_…>  save an API token to ~/.config/ncn-cli/token
  completion [bash|zsh]  print a shell-completion script
  console           interactive REPL (also the default when run with no args)
  welcome           print the NCN banner

Auth (all but 'status'):  --token  >  $NCN_TOKEN  >  ~/.config/ncn-cli/token
Mint a token at admin.example.com → Security → API Tokens.

Flags:
  --host URL     console base URL (default `+defaultHost+`, or $NCN_HOST)
  --json         emit raw JSON instead of formatted output
  --timeout D    per-request timeout (default 20s)
`)
}

// ─────────────────────────────── Commands ───────────────────────────────

func cmdWhoami() error {
	data, err := apiGet("/api/v1/auth/me", true)
	if err != nil {
		return err
	}
	if gJSON {
		return printJSON(data)
	}
	var me struct {
		Operator   string `json:"operator"`
		Role       string `json:"role"`
		TTLSeconds int64  `json:"ttl_seconds"`
		HasPasskey bool   `json:"has_passkey"`
		HasTOTP    bool   `json:"has_totp"`
	}
	_ = json.Unmarshal(data, &me)
	fmt.Printf("%s  %s\n", bold(me.Operator), dim("("+me.Role+")"))
	if me.TTLSeconds > 0 {
		fmt.Printf("  session valid for %s\n", (time.Duration(me.TTLSeconds) * time.Second).Round(time.Minute))
	}
	return nil
}

func cmdFleet() error {
	nodes, err := fetchFleet()
	if err != nil {
		return err
	}
	if gJSON {
		return printJSONv(nodes)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, dim("POP\tSTATE\tLOAD\tCPU\tMEM\tDISK\tBIRD\tBGP\tWG\tTUN\tSCRAPE"))
	for _, n := range nodes {
		state := green("● up")
		if !n.OK {
			state = red("● down")
		}
		est, tot := bgpCounts(n)
		bgpCell := fmt.Sprintf("%d/%d", est, tot)
		if tot > 0 && est < tot {
			bgpCell = yellow(bgpCell)
		}
		row := fmt.Sprintf("%s\t%s\t%.2f\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s",
			bold(n.Node.ID), state, n.Load1,
			pct(n.CPUPct), pct(n.MemPct), pct(n.DiskPct),
			orDash(n.BirdVer), bgpCell, len(n.WG), len(n.Tunnels), orDash(n.Latency))
		if !n.OK && n.Error != "" {
			row += "\t" + dim(n.Error)
		}
		fmt.Fprintln(tw, row)
	}
	return tw.Flush()
}

func cmdNode(rest []string) error {
	if len(rest) == 0 {
		return errors.New("usage: ncn-debug node <id>")
	}
	id := rest[0]
	nodes, err := fetchFleet()
	if err != nil {
		return err
	}
	var n *fleetNodeStatus
	for i := range nodes {
		if nodes[i].Node.ID == id {
			n = &nodes[i]
			break
		}
	}
	if n == nil {
		return fmt.Errorf("no PoP %q in fleet (try `ncn-debug fleet`)", id)
	}
	if gJSON {
		return printJSONv(n)
	}
	state := green("up")
	if !n.OK {
		state = red("down")
	}
	fmt.Printf("%s  %s  %s\n", bold(n.Node.ID), dim(n.Node.Label), state)
	if n.Error != "" {
		fmt.Println("  " + red("error: "+n.Error))
	}
	line := func(k, v string) { fmt.Printf("  %-14s %s\n", dim(k), v) }
	if n.Hostname != "" {
		line("hostname", n.Hostname)
	}
	if n.Uptime != "" {
		line("uptime", n.Uptime)
	}
	line("load1", fmt.Sprintf("%.2f", n.Load1))
	line("cpu", pct(n.CPUPct))
	line("mem", fmt.Sprintf("%s  (%s / %s)", pct(n.MemPct), humanBytes(n.MemUsed), humanBytes(n.MemTotal)))
	line("disk", fmt.Sprintf("%s  (%s / %s)", pct(n.DiskPct), humanBytes(n.DiskUsed), humanBytes(n.DiskTotal)))
	line("net", fmt.Sprintf("↓ %s/s  ↑ %s/s", humanBytes(uint64(n.NetRxBps)), humanBytes(uint64(n.NetTxBps))))
	if n.BirdVer != "" {
		line("bird", n.BirdVer)
	}
	if n.AgentCertDaysLeft != 0 {
		v := fmt.Sprintf("%d days", n.AgentCertDaysLeft)
		if n.AgentCertDaysLeft < 30 {
			v = yellow(v)
		}
		line("agent cert", v)
	}
	if len(n.Probes) > 0 {
		fmt.Println("  " + dim("probes"))
		for _, p := range n.Probes {
			st := green("ok")
			ms := fmt.Sprintf("%.2f ms", p.LastMS)
			if !p.LastOK {
				st, ms = red("FAIL"), "—"
			}
			fmt.Printf("    %-16s %-5s %s\n", p.Name, st, ms)
		}
	}
	if len(n.Protocols) > 0 {
		fmt.Println("  " + dim("bird protocols"))
		for _, p := range n.Protocols {
			st := green(p.State)
			if !p.Healthy {
				st = yellow(p.State)
			}
			fmt.Printf("    %-18s %-6s %-8s %s\n", p.Name, p.Proto, st, dim(p.Info))
		}
	}
	if len(n.RouteCounts) > 0 {
		var parts []string
		for _, rc := range n.RouteCounts {
			parts = append(parts, fmt.Sprintf("%s=%d", rc.Table, rc.Count))
		}
		line("routes", strings.Join(parts, "  "))
	}
	if len(n.Tunnels) > 0 {
		fmt.Println("  " + dim("tunnels"))
		for _, t := range n.Tunnels {
			st := green("up")
			if !t.Up {
				st = red("down")
			}
			fmt.Printf("    %-12s %-8s %-5s %s→%s\n", t.Name, t.Kind, st, t.Local, t.Remote)
		}
	}
	return nil
}

func cmdBGP(rest []string) error {
	nodes, err := fetchFleet()
	if err != nil {
		return err
	}
	filter := ""
	if len(rest) > 0 {
		filter = rest[0]
	}
	if gJSON {
		return printJSONv(nodes)
	}
	any := false
	for _, n := range nodes {
		if filter != "" && n.Node.ID != filter {
			continue
		}
		var bgps []birdProtocol
		for _, p := range n.Protocols {
			if strings.EqualFold(p.Proto, "BGP") {
				bgps = append(bgps, p)
			}
		}
		if len(bgps) == 0 {
			continue
		}
		any = true
		est, tot := bgpCounts(n)
		fmt.Printf("%s  %s\n", bold(n.Node.ID), dim(fmt.Sprintf("%d/%d established", est, tot)))
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		for _, p := range bgps {
			st := green(p.State)
			if !p.Healthy {
				st = yellow(p.State)
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", p.Name, st, dim(p.Since), dim(p.Info))
		}
		tw.Flush()
	}
	if !any {
		if filter != "" {
			return fmt.Errorf("no BGP sessions for %q", filter)
		}
		fmt.Println(dim("no BGP sessions reported"))
	}
	return nil
}

func cmdIncidents() error {
	data, err := apiGet("/api/v1/incidents/public", true)
	if err != nil {
		return err
	}
	if gJSON {
		return printJSON(data)
	}
	var incs []struct {
		Title        string   `json:"title"`
		Status       string   `json:"status"`
		Severity     string   `json:"severity"`
		AffectedPoPs []string `json:"affected_pops"`
		CreatedAt    string   `json:"created_at"`
		ResolvedAt   *string  `json:"resolved_at"`
	}
	if err := json.Unmarshal(data, &incs); err != nil {
		return err
	}
	if len(incs) == 0 {
		fmt.Println(dim("no incidents in the last 30 days"))
		return nil
	}
	for _, in := range incs {
		mark := yellow("●")
		if in.Status == "resolved" {
			mark = green("●")
		}
		sev := in.Severity
		if in.Severity == "critical" {
			sev = red(sev)
		} else if in.Severity == "major" {
			sev = yellow(sev)
		}
		fmt.Printf("%s %s  %s  %s\n", mark, bold(in.Title), sev, dim(in.Status))
		meta := "  opened " + in.CreatedAt
		if in.ResolvedAt != nil {
			meta += " · resolved " + *in.ResolvedAt
		}
		if len(in.AffectedPoPs) > 0 {
			meta += " · " + strings.Join(in.AffectedPoPs, ",")
		}
		fmt.Println(dim(meta))
	}
	return nil
}

func cmdStatus() error {
	data, err := apiGet("/api/v1/status/summary", false) // public
	if err != nil {
		return err
	}
	if gJSON {
		return printJSON(data)
	}
	var sum struct {
		Components []struct {
			Name       string  `json:"name"`
			Category   string  `json:"category"`
			LastStatus int     `json:"last_status"`
			LastMS     float64 `json:"last_latency_ms"`
			Uptime     float64 `json:"uptime"`
		} `json:"components"`
		WindowDays int `json:"window_days"`
	}
	if err := json.Unmarshal(data, &sum); err != nil {
		return err
	}
	cat := ""
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	for _, c := range sum.Components {
		if c.Category != cat {
			tw.Flush()
			fmt.Println(dim("── " + c.Category + " ──"))
			cat = c.Category
		}
		st := green("● up")
		lat := fmt.Sprintf("%.0f ms", c.LastMS)
		switch c.LastStatus {
		case 0:
			st, lat = red("● down"), "—"
		case -1:
			st, lat = dim("● ?"), "—"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", c.Name, st, lat, uptimeColor(c.Uptime))
	}
	tw.Flush()
	fmt.Printf("%s\n", dim(fmt.Sprintf("uptime over %d days", sum.WindowDays)))
	return nil
}

func cmdAlerts(rest []string) error {
	all := hasFlag(rest, "--all")
	data, err := apiGet("/api/v1/alerts", true)
	if err != nil {
		return err
	}
	if gJSON {
		return printJSON(data)
	}
	var env struct {
		Active  []alertEvent `json:"active"`
		History []alertEvent `json:"history"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	if len(env.Active) == 0 {
		fmt.Println(green("● no active alerts"))
	} else {
		fmt.Println(dim(fmt.Sprintf("── %d active ──", len(env.Active))))
		printAlertRows(env.Active)
	}
	if all && len(env.History) > 0 {
		fmt.Println(dim(fmt.Sprintf("── recent history (%d) ──", len(env.History))))
		printAlertRows(env.History)
	}
	return nil
}

func printAlertRows(rows []alertEvent) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	for _, a := range rows {
		mark := yellow("●")
		if a.State == "resolved" {
			mark = green("●")
		} else if a.Severity == "critical" {
			mark = red("●")
		}
		ack := ""
		if a.Acked {
			ack = dim("(ack " + a.AckedBy + ")")
		}
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\n",
			mark, sevColor(a.Severity), bold(orDash(a.NodeID)),
			a.Title, dim(relAge(a.FiredAt)), ack)
	}
	tw.Flush()
}

func cmdRPKI() error {
	data, err := apiGet("/api/v1/auth/rpki", true)
	if err != nil {
		return err
	}
	if gJSON {
		return printJSON(data)
	}
	var s struct {
		ASN      string `json:"asn"`
		Prefixes []struct {
			Prefix   string `json:"prefix"`
			Validity string `json:"validity"`
			ROAs     int    `json:"roas"`
		} `json:"prefixes"`
		Valid     int   `json:"valid"`
		Invalid   int   `json:"invalid"`
		Unknown   int   `json:"unknown"`
		CheckedAt int64 `json:"checked_at"`
		ROV       *struct {
			Node        string `json:"node"`
			Established bool   `json:"established"`
			VRPs        int    `json:"vrps"`
		} `json:"rov"`
		IntervalSecs int64  `json:"interval_secs"`
		Err          string `json:"error"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	fmt.Printf("%s  %s\n", bold(orDash(s.ASN)),
		dim(fmt.Sprintf("%s · poll %s · checked %s",
			validitySummary(s.Valid, s.Invalid, s.Unknown),
			humanDur(time.Duration(s.IntervalSecs)*time.Second), relAge(s.CheckedAt))))
	if s.Err != "" {
		fmt.Println("  " + red("error: "+s.Err))
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	for _, p := range s.Prefixes {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", p.Prefix, validityColor(p.Validity), dim(fmt.Sprintf("%d ROA", p.ROAs)))
	}
	tw.Flush()
	if s.ROV != nil {
		st := green("established")
		if !s.ROV.Established {
			st = red("down")
		}
		fmt.Printf("%s %s on %s, %d VRPs\n", dim("ROV:"), st, bold(s.ROV.Node), s.ROV.VRPs)
	}
	return nil
}

func cmdOncall() error {
	data, err := apiGet("/api/v1/auth/oncall", true)
	if err != nil {
		return err
	}
	if gJSON {
		return printJSON(data)
	}
	var d struct {
		Current string `json:"current"`
		Config  struct {
			Rotation   []string `json:"rotation"`
			StartDate  string   `json:"start_date"`
			PeriodDays int      `json:"period_days"`
		} `json:"config"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	if d.Current == "" {
		fmt.Println(dim("no rotation configured"))
		return nil
	}
	fmt.Printf("on call now: %s\n", bold(green(d.Current)))
	if len(d.Config.Rotation) > 0 {
		var parts []string
		for _, op := range d.Config.Rotation {
			if op == d.Current {
				parts = append(parts, bold(op))
			} else {
				parts = append(parts, dim(op))
			}
		}
		fmt.Printf("  rotation (%dd shifts): %s\n", d.Config.PeriodDays, strings.Join(parts, " → "))
	}
	return nil
}

func cmdPeering(rest []string) error {
	pendingOnly := !hasFlag(rest, "--all")
	if hasFlag(rest, "--pending") {
		pendingOnly = true
	}
	data, err := apiGet("/api/v1/auth/peering/applications", true)
	if err != nil {
		return err
	}
	if gJSON {
		return printJSON(data)
	}
	var apps []struct {
		ID          string    `json:"id"`
		Status      string    `json:"status"`
		ASN         uint32    `json:"asn"`
		NetworkName string    `json:"network_name"`
		NOCEmail    string    `json:"noc_email"`
		SubmittedAt time.Time `json:"submitted_at"`
	}
	if err := json.Unmarshal(data, &apps); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	shown := 0
	for _, a := range apps {
		if pendingOnly && a.Status != "pending" {
			continue
		}
		shown++
		st := yellow(a.Status)
		switch a.Status {
		case "approved":
			st = green(a.Status)
		case "rejected":
			st = red(a.Status)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			st, bold(fmt.Sprintf("AS%d", a.ASN)), orDash(a.NetworkName),
			dim(a.NOCEmail), dim(relAgeT(a.SubmittedAt)))
	}
	tw.Flush()
	if shown == 0 {
		if pendingOnly {
			fmt.Println(dim("no pending applications (try --all)"))
		} else {
			fmt.Println(dim("no applications"))
		}
	}
	return nil
}

func cmdOpFailures(rest []string) error {
	openOnly := !hasFlag(rest, "--all")
	path := "/api/v1/auth/op-failures"
	if openOnly {
		path += "?open=1"
	}
	data, err := apiGet(path, true)
	if err != nil {
		return err
	}
	if gJSON {
		return printJSON(data)
	}
	var d struct {
		Failures []struct {
			ID     string `json:"id"`
			Kind   string `json:"kind"`
			Target string `json:"target"`
			Actor  string `json:"actor"`
			Reason string `json:"reason"`
			At     int64  `json:"at"`
			Status string `json:"status"`
		} `json:"failures"`
		Open int `json:"open"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	if len(d.Failures) == 0 {
		fmt.Println(green("● no operation failures"))
		return nil
	}
	fmt.Println(dim(fmt.Sprintf("── %d open ──", d.Open)))
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	for _, f := range d.Failures {
		mark := red("●")
		if f.Status == "dismissed" {
			mark = dim("○")
		}
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\n",
			mark, dim(f.ID), bold(f.Kind), orDash(f.Target),
			dim(relAge(f.At)), truncate(f.Reason, 60))
	}
	tw.Flush()
	return nil
}

func cmdAudit(rest []string) error {
	limit := 20
	if v, ok := flagValue(rest, "--limit"); ok {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			limit = n
		}
	}
	data, err := apiGet(fmt.Sprintf("/api/v1/auth/audit?limit=%d", limit), true)
	if err != nil {
		return err
	}
	if gJSON {
		return printJSON(data)
	}
	var d struct {
		Events []struct {
			TS       time.Time `json:"ts"`
			Event    string    `json:"event"`
			Severity string    `json:"severity"`
			Actor    string    `json:"actor"`
			Target   string    `json:"target"`
			Outcome  string    `json:"outcome"`
		} `json:"events"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	if len(d.Events) == 0 {
		fmt.Println(dim("no audit events"))
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	for _, e := range d.Events {
		oc := green(e.Outcome)
		switch e.Outcome {
		case "fail":
			oc = red(e.Outcome)
		case "denied":
			oc = yellow(e.Outcome)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			dim(relAgeT(e.TS)), bold(e.Event), oc, orDash(e.Actor), dim(orDash(e.Target)))
	}
	tw.Flush()
	return nil
}

func cmdGet(rest []string) error {
	if len(rest) == 0 {
		return errors.New("usage: ncn-debug get <path>   e.g. ncn-debug get /api/v1/bird/protocol?node=pop-03")
	}
	path := rest[0]
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	// `get` is the escape hatch; always send the token if we have one (the
	// path may be admin-only). Public paths just ignore it.
	data, err := apiGet(path, false)
	if err != nil {
		return err
	}
	return printJSON(data)
}

func cmdToken(rest []string) error {
	if len(rest) == 0 {
		return errors.New("usage: ncn-debug token <ncntok_…>")
	}
	tok := strings.TrimSpace(rest[0])
	if !strings.HasPrefix(tok, "ncntok_") {
		return errors.New("that doesn't look like an API token (expected ncntok_… prefix)")
	}
	p, err := tokenPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Printf("saved token to %s (mode 0600)\n", p)
	return nil
}

// completionCommands is the command list offered by shell completion.
const completionCommands = "whoami fleet node bgp incidents alerts rpki oncall peering errors opfail audit status get token completion console welcome help"

func cmdCompletion(rest []string) error {
	shell := "bash"
	if len(rest) > 0 {
		shell = rest[0]
	}
	switch shell {
	case "bash":
		fmt.Printf(`# ncn-debug bash completion — add to ~/.bashrc:  source <(ncn-debug completion bash)
_ncn_debug() {
  local cur="${COMP_WORDS[COMP_CWORD]}"
  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=( $(compgen -W "%s" -- "$cur") )
  else
    COMPREPLY=( $(compgen -W "--json --host --token --timeout --all --pending --limit" -- "$cur") )
  fi
}
complete -F _ncn_debug ncn-debug
`, completionCommands)
	case "zsh":
		fmt.Printf(`#compdef ncn-debug
# ncn-debug zsh completion — add to ~/.zshrc:  source <(ncn-debug completion zsh)
_ncn_debug() {
  if (( CURRENT == 2 )); then
    compadd -- %s
  else
    compadd -- --json --host --token --timeout --all --pending --limit
  fi
}
compdef _ncn_debug ncn-debug
`, completionCommands)
	default:
		return fmt.Errorf("unsupported shell %q (use: bash | zsh)", shell)
	}
	return nil
}

// ─────────────────────────────── HTTP ───────────────────────────────

// apiGet performs a GET against base+path and returns the envelope's `data`.
// requireToken=true errors early if no token is resolvable; requireToken=false
// still sends the token when present (used by `get` and public endpoints).
func apiGet(path string, requireToken bool) (json.RawMessage, error) {
	tok, terr := resolveToken()
	if requireToken && terr != nil {
		return nil, terr
	}
	req, err := http.NewRequest(http.MethodGet, gHost+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ncn-debug/"+appVersion+" ("+runtime.GOOS+"-"+runtime.GOARCH+")")
	req.Header.Set("Accept", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	client := &http.Client{Timeout: gTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var env struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("non-JSON response (HTTP %d): %s", resp.StatusCode, truncate(string(body), 200))
	}
	if !env.OK {
		if env.Error == "" {
			env.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			env.Error += "  (token missing/expired? `ncn-debug token <ncntok_…>` or set $NCN_TOKEN)"
		}
		return nil, errors.New(env.Error)
	}
	return env.Data, nil
}

func fetchFleet() ([]fleetNodeStatus, error) {
	data, err := apiGet("/api/v1/fleet", true)
	if err != nil {
		return nil, err
	}
	var nodes []fleetNodeStatus
	if err := json.Unmarshal(data, &nodes); err != nil {
		return nil, err
	}
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].Node.ID < nodes[j].Node.ID })
	return nodes, nil
}

// ─────────────────────────────── Token resolution ───────────────────────────────

func resolveToken() (string, error) {
	if gToken != "" {
		return gToken, nil
	}
	if v := os.Getenv("NCN_TOKEN"); v != "" {
		return v, nil
	}
	if p, err := tokenPath(); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			if t := strings.TrimSpace(string(b)); t != "" {
				return t, nil
			}
		}
	}
	return "", errors.New("no API token — pass --token, set $NCN_TOKEN, or run `ncn-debug token <ncntok_…>` (mint one at admin.example.com → Security → API Tokens)")
}

func tokenPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ncn-cli", "token"), nil
}

// ─────────────────────────────── Rendering helpers ───────────────────────────────

// hasFlag reports whether name appears in args (order-independent boolean flag).
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// flagValue returns the value following `name` (or after `name=`), if present.
func flagValue(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1], true
		}
		if strings.HasPrefix(a, name+"=") {
			return strings.TrimPrefix(a, name+"="), true
		}
	}
	return "", false
}

func sevColor(s string) string {
	switch s {
	case "critical":
		return red(s)
	case "major", "warning", "warn":
		return yellow(s)
	}
	return s
}

func validityColor(v string) string {
	switch v {
	case "valid":
		return green(v)
	case "invalid":
		return red(v)
	}
	return dim(v)
}

func validitySummary(valid, invalid, unknown int) string {
	parts := []string{green(fmt.Sprintf("%d valid", valid))}
	if invalid > 0 {
		parts = append(parts, red(fmt.Sprintf("%d invalid", invalid)))
	} else {
		parts = append(parts, fmt.Sprintf("%d invalid", invalid))
	}
	parts = append(parts, dim(fmt.Sprintf("%d unknown", unknown)))
	return strings.Join(parts, " · ")
}

// relAge renders "<dur> ago" for a unix-seconds timestamp (— if zero).
func relAge(sec int64) string {
	if sec == 0 {
		return "—"
	}
	return humanDur(time.Since(time.Unix(sec, 0))) + " ago"
}

// relAgeT is relAge for a time.Time (— if zero).
func relAgeT(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return humanDur(time.Since(t)) + " ago"
}

func humanDur(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func bgpCounts(n fleetNodeStatus) (est, tot int) {
	for _, p := range n.Protocols {
		if strings.EqualFold(p.Proto, "BGP") {
			tot++
			if p.Healthy {
				est++
			}
		}
	}
	return
}

func printJSON(raw json.RawMessage) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		fmt.Println(string(raw)) // fall back to raw
		return nil
	}
	fmt.Println(buf.String())
	return nil
}

func printJSONv(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func pct(v float64) string {
	s := fmt.Sprintf("%.0f%%", v)
	switch {
	case v >= 90:
		return red(s)
	case v >= 75:
		return yellow(s)
	default:
		return s
	}
}
func uptimeColor(frac float64) string {
	p := frac * 100
	s := fmt.Sprintf("%.2f%%", p)
	switch {
	case frac == 0:
		return dim("— no data")
	case p >= 99.9:
		return green(s)
	case p >= 99:
		return s
	case p >= 95:
		return yellow(s)
	default:
		return red(s)
	}
}
func humanBytes(b uint64) string {
	const u = 1024
	if b < u {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(u), 0
	for n := b / u; n >= u; n /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(b)/float64(div), "KMGTPE"[exp])
}
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ─────────────────────────────── Color ───────────────────────────────

func colorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
func wrap(code, s string) string {
	if !gColor {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
func bold(s string) string   { return wrap("1", s) }
func dim(s string) string    { return wrap("2", s) }
func red(s string) string    { return wrap("31", s) }
func green(s string) string  { return wrap("32", s) }
func yellow(s string) string { return wrap("33", s) }
func cyan(s string) string   { return wrap("1;36", s) }

// ─────────────────────────────── Wire types ───────────────────────────────
// Subset of the backend shapes we render. Mirrors core-console/backend
// fleet.go / bird_scrape.go / tunnel.go — keep the JSON tags in sync.

type fleetNode struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Country string `json:"country"`
}

type alertEvent struct {
	ID         string `json:"id"`
	NodeID     string `json:"node_id"`
	Title      string `json:"title"`
	Severity   string `json:"severity"`
	Message    string `json:"message"`
	FiredAt    int64  `json:"fired_at"`
	ResolvedAt int64  `json:"resolved_at"`
	State      string `json:"state"`
	Acked      bool   `json:"acked"`
	AckedBy    string `json:"acked_by"`
}

type birdProtocol struct {
	Name    string `json:"name"`
	Proto   string `json:"proto"`
	Table   string `json:"table"`
	State   string `json:"state"`
	Since   string `json:"since"`
	Info    string `json:"info"`
	Healthy bool   `json:"healthy"`
}

type birdRouteCount struct {
	Table string `json:"table"`
	Count int    `json:"count"`
}

type probeOut struct {
	Name   string  `json:"name"`
	LastOK bool    `json:"last_ok"`
	LastMS float64 `json:"last_ms"`
}

type netTunnel struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Up     bool   `json:"up"`
	Local  string `json:"local,omitempty"`
	Remote string `json:"remote,omitempty"`
}

type wgIface struct {
	Name string `json:"name"`
}

type fleetNodeStatus struct {
	Node      fleetNode `json:"node"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	Hostname  string    `json:"hostname,omitempty"`
	Uptime    string    `json:"uptime,omitempty"`
	Load1     float64   `json:"load_1"`
	MemPct    float64   `json:"mem_pct"`
	CPUPct    float64   `json:"cpu_pct"`
	DiskPct   float64   `json:"disk_pct"`
	NetRxBps  float64   `json:"net_rx_bps"`
	NetTxBps  float64   `json:"net_tx_bps"`
	MemTotal  uint64    `json:"mem_total,omitempty"`
	MemUsed   uint64    `json:"mem_used,omitempty"`
	DiskTotal uint64    `json:"disk_total,omitempty"`
	DiskUsed  uint64    `json:"disk_used,omitempty"`

	BirdVer     string           `json:"bird_version,omitempty"`
	Protocols   []birdProtocol   `json:"protocols,omitempty"`
	RouteCounts []birdRouteCount `json:"route_counts,omitempty"`
	WG          []wgIface        `json:"wg,omitempty"`
	Tunnels     []netTunnel      `json:"tunnels,omitempty"`
	Probes      []probeOut       `json:"probes,omitempty"`

	AgentCertDaysLeft int `json:"agent_cert_days_left,omitempty"`

	FetchedAt int64  `json:"fetched_at"`
	Latency   string `json:"scrape_latency,omitempty"`
}
