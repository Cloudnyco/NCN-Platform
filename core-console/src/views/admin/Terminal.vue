<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import { api, type Envelope, type FleetNodeStatus } from '@/api/client'
import { usePolling } from '@/composables/usePolling'
import NodeTabs from '@/components/NodeTabs.vue'
import { useSessionStore } from '@/stores/session'
import {
  decodeRequestOptions, encodeAssertionResponse, isWebAuthnSupported
} from '@/utils/webauthn'

const { data: fleetData } = usePolling<Envelope<FleetNodeStatus[]>>(
  (s) => api.fleet(s), 15000
)
const nodes = computed(() => fleetData.value?.data ?? [])
const tabNodes = computed(() =>
  nodes.value.map(n => ({ id: n.node.id, ok: n.ok, country: n.node.country }))
)

const selectedId = ref<string>('')
const status = ref<'idle' | 'connecting' | 'open' | 'closed' | 'error'>('idle')
const lastError = ref<string>('')

// Ref to the TOTP input — used for auto-focus when that tab is active.
const ackInputEl = ref<HTMLInputElement | null>(null)

// ---- MFA step-up risk-ack modal ----
// Note: we deliberately do NOT re-ask for the password here. The session
// cookie already proves possession (it was issued by /login or by a
// passkey discoverable assertion), so a second password type-in adds
// friction without adding security. The backend agrees.
const session = useSessionStore()
const confirmOpen = ref(false)
const ackTotpCode = ref('')
const ackBusy = ref(false)
const ackErr = ref<string>('')
// Which MFA factor to use this attempt. Defaults to passkey when the
// operator has one bound (better UX, no typing) — they can switch to TOTP
// if they prefer or if the browser can't do WebAuthn right now.
const ackMfaMethod = ref<'passkey' | 'totp'>('passkey')
const passkeyAvailable = computed(() => session.hasPasskey && isWebAuthnSupported())
const totpAvailable = computed(() => session.hasTotp)

function nodeAddrFor(id: string): string {
  return nodes.value.find(n => n.node.id === id)?.node.address ?? '?'
}

function requestConnect() {
  if (!selectedId.value) return
  if (status.value === 'open' || status.value === 'connecting') return
  lastError.value = ''
  ackTotpCode.value = ''
  ackErr.value = ''
  // Default MFA method: passkey when available, otherwise TOTP. If neither
  // is registered the backend refuses anyway — onboarding catches that case.
  ackMfaMethod.value = passkeyAvailable.value
    ? 'passkey'
    : (totpAvailable.value ? 'totp' : 'passkey')
  confirmOpen.value = true
}

// When the TOTP tab is selected, focus the code input. For the passkey tab
// there's no input to focus — the operator just clicks "verify with passkey"
// and the browser opens the OS prompt.
watch(confirmOpen, async (v) => {
  if (!v) return
  await new Promise(requestAnimationFrame)
  if (ackMfaMethod.value === 'totp') {
    ackInputEl.value?.focus()
    ackInputEl.value?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  }
})
watch(ackMfaMethod, async (m) => {
  if (m !== 'totp') return
  await new Promise(requestAnimationFrame)
  ackInputEl.value?.focus()
})

async function onAckSubmit() {
  if (ackBusy.value) return

  if (ackMfaMethod.value === 'passkey' && !passkeyAvailable.value) {
    ackErr.value = 'passkey not available; switch to TOTP'
    return
  }
  if (ackMfaMethod.value === 'totp') {
    if (!totpAvailable.value) {
      ackErr.value = 'no TOTP registered; switch to passkey'
      return
    }
    if (ackTotpCode.value.trim().length < 6) {
      ackErr.value = 'enter the 6-digit TOTP code'
      return
    }
  }

  ackBusy.value = true
  ackErr.value = ''
  try {
    const payload: {
      node: string
      totp_code?: string
      passkey?: { challenge_id: string; response: unknown }
    } = { node: selectedId.value }

    if (ackMfaMethod.value === 'passkey') {
      // Step-up WebAuthn assertion — operator-scoped (not discoverable).
      const begin = await api.termPasskeyBegin()
      if (!begin.ok || !begin.data) throw new Error(begin.error ?? 'passkey-begin failed')
      const options = decodeRequestOptions(begin.data.options)
      const cred = await navigator.credentials.get(options) as PublicKeyCredential | null
      if (!cred) throw new Error('passkey verification cancelled')
      payload.passkey = {
        challenge_id: begin.data.challenge_id,
        response: encodeAssertionResponse(cred)
      }
    } else {
      payload.totp_code = ackTotpCode.value.trim()
    }

    const env = await api.termTicket(payload)
    if (!env.ok || !env.data?.ticket) {
      ackErr.value = env.error ?? 'verify failed'
      return
    }
    const ticket = env.data.ticket
    confirmOpen.value = false
    ackTotpCode.value = ''
    connect(ticket)
  } catch (e) {
    ackErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    ackBusy.value = false
  }
}
function onAckCancel() {
  confirmOpen.value = false
  ackTotpCode.value = ''
  ackErr.value = ''
}
function switchMfaMethod(to: 'passkey' | 'totp') {
  ackMfaMethod.value = to
  ackErr.value = ''
  ackTotpCode.value = ''
}

watch(nodes, (vs) => {
  if (vs.length && !vs.find(n => n.node.id === selectedId.value)) {
    selectedId.value = vs[0].node.id
  }
}, { immediate: true })

const containerEl = ref<HTMLElement | null>(null)
let term: Terminal | null = null
let fit: FitAddon | null = null
let ws: WebSocket | null = null
let resizeObserver: ResizeObserver | null = null

// ---- Mobile quick-key bar ----------------------------------------------
// On touch devices the soft keyboard hides Esc, Tab, Ctrl, arrows, pipe,
// tilde, etc. We auto-mount a Termux-style horizontal key bar at the top
// edge of the soft keyboard whenever `pointer: coarse` matches.
//
// Positioning is the hard part. The naive "position: sticky; bottom: 0"
// anchors the bar to the LAYOUT viewport, which doesn't shrink when the
// keyboard opens — so the bar ends up hidden behind the keyboard. Worse,
// scrolling the page to expose the bar trips iOS Safari's "scroll → blur
// focus" heuristic, which dismisses the keyboard, which then exposes the
// bar … only after the user has lost their input focus.
//
// The fix is: track the keyboard's height via the visualViewport API and
// dynamically set the bar's `bottom: keyboardHeightPx` so it floats just
// above the keyboard's top edge at all times. Teleport-to-body escapes
// any ancestor containing-block that would break `position: fixed`.
//
// Each key sends the raw byte sequence the TTY expects through the same
// WebSocket the xterm `onData` callback uses, so the PTY can't tell them
// apart from a hardware keyboard. pointerdown.prevent on each button +
// term.focus() after each send keeps focus on xterm so the soft keyboard
// stays open between taps.
const isCoarsePointer = ref(false)
const quickKeysCollapsed = ref(false)
const keyboardOffsetPx = ref(0)
let visualViewportCleanup: (() => void) | null = null

// Computed: is the operator currently in "mobile shell" mode? True when
// the device is coarse-pointer, the PTY is connected, AND the soft
// keyboard is open. We use this to:
//   1. Lock <body> to overflow:hidden so the page can't rubber-band scroll
//      (iOS Safari's worst mobile-keyboard sin).
//   2. Hide non-essential chrome (banner, NodeTabs, audit notice) so the
//      tiny remaining visible viewport is dominated by the terminal.
// Without this guard the user perceives the layout as "stuck": the page
// scrolls under their finger, the cursor moves out of view, and the
// quick-key bar appears to lag behind the keyboard.
const mobileShellActive = computed(() =>
  isCoarsePointer.value && status.value === 'open' && keyboardOffsetPx.value > 0
)

// Sticky-Ctrl modifier. Tapping the "Ctrl" pill arms this flag; the very
// next character that arrives from the soft keyboard (a-z / A-Z) is
// transformed into its Ctrl+letter byte (0x01..0x1A) before being sent,
// then ctrlPending auto-disarms. Tapping Ctrl again while armed (or
// tapping any other quick-key) disarms it as a cancel.
//
// Why this UX: dedicated `^C / ^D / ^Z / ^L / ...` buttons crowded the
// bar and were easy to mis-tap — `^D` at a bare prompt exits the shell
// and ends the session, which most operators found surprising. A single
// sticky modifier means accidents take two deliberate taps (arm + send).
const ctrlPending = ref(false)

// Send a raw byte string to the PTY (or no-op if the socket is closed).
// Extracted so both onData (real keyboard) and the quick-key buttons go
// through the same single send path.
function sendBytes(data: string) {
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(new TextEncoder().encode(data))
  }
}

// The quick-key set, in display order. `seq` is the literal byte string
// the PTY receives. Arrow keys / Home / End / PgUp / PgDn use the standard
// xterm CSI sequences (bash / zsh / vim accept both CSI and SS3 forms).
//
// Note: `^X` control combos (^C / ^D / ^Z / ^L / ...) are NOT here. They
// were removed because they were easy to mis-tap on a crowded bar and
// `^D` in particular would silently exit the shell. Use the dedicated
// `Ctrl` toggle below instead — tap it, then tap the letter on the soft
// keyboard.
interface QuickKey { label: string; seq: string; title?: string }
const QUICK_KEYS: QuickKey[] = [
  { label: 'Esc',  seq: '\x1b',     title: 'Escape' },
  { label: 'Tab',  seq: '\t',       title: 'Tab' },
  { label: '↑',    seq: '\x1b[A',   title: 'Up' },
  { label: '↓',    seq: '\x1b[B',   title: 'Down' },
  { label: '←',    seq: '\x1b[D',   title: 'Left' },
  { label: '→',    seq: '\x1b[C',   title: 'Right' },
  { label: 'Home', seq: '\x1b[H',   title: 'Home' },
  { label: 'End',  seq: '\x1b[F',   title: 'End' },
  { label: 'PgUp', seq: '\x1b[5~',  title: 'Page Up' },
  { label: 'PgDn', seq: '\x1b[6~',  title: 'Page Down' },
  { label: '/',    seq: '/',        title: 'Slash' },
  { label: '|',    seq: '|',        title: 'Pipe' },
  { label: '~',    seq: '~',        title: 'Tilde' },
  { label: '-',    seq: '-',        title: 'Dash' },
]

// Dedup window for the dual @pointerdown / @click bindings — iOS Safari
// has had stretches where pointerdown silently fails to fire on
// position:fixed teleported elements, so we belt-and-braces both events.
// 250ms is well above the ~50-100ms gap between pointerup → synthetic
// click on a single tap; far below the human pace for a deliberate
// double-tap.
let lastQuickTapTs = 0
function isDuplicateTap(): boolean {
  const now = performance.now()
  if (now - lastQuickTapTs < 250) return true
  lastQuickTapTs = now
  return false
}

// reFocusTerm refocuses xterm's hidden helper textarea. On iOS the
// keyboard sometimes dismisses on a button tap despite preventDefault;
// scheduling the focus call one animation frame later lets the browser
// settle its focus state first, then we pull focus back inside what
// iOS still considers the active user gesture. requestAnimationFrame
// (vs setTimeout(0)) keeps it inside the same gesture window.
function reFocusTerm() {
  if (typeof requestAnimationFrame === 'function') {
    requestAnimationFrame(() => term?.focus())
  } else {
    term?.focus()
  }
}

function onQuickKey(seq: string) {
  if (isDuplicateTap()) return
  // Tapping any non-Ctrl quick-key cancels a pending Ctrl modifier (the
  // operator changed their mind). The key itself is sent verbatim — we
  // don't compose Ctrl + arrow / Tab / etc. here because the encoded
  // sequences differ per terminal mode and bash rarely uses them on
  // mobile-typical workflows.
  if (ctrlPending.value) ctrlPending.value = false
  sendBytes(seq)
  // Snap to the cursor row — matches the behavior of real keyboard
  // input below. Quick-key taps (especially arrow keys) are useless if
  // the user can't see what they're moving the cursor through.
  term?.scrollToBottom()
  reFocusTerm()
}

// Toggle the Ctrl modifier. Visual state is bound to the button via
// :class so the operator can see whether the next letter will be sent
// as Ctrl+letter or as a normal character.
function onToggleCtrl() {
  if (isDuplicateTap()) return
  ctrlPending.value = !ctrlPending.value
  reFocusTerm()
}

function onToggleQuickKeysCollapse() {
  if (isDuplicateTap()) return
  quickKeysCollapsed.value = !quickKeysCollapsed.value
  reFocusTerm()
}

function disposeSession() {
  try { ws?.close() } catch { /* */ }
  ws = null
  try { term?.dispose() } catch { /* */ }
  term = null
  fit = null
  resizeObserver?.disconnect()
  resizeObserver = null
  status.value = 'closed'
}

function buildWSURL(nodeId: string, ticket: string): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const q = new URLSearchParams({ node: nodeId, ticket })
  return `${proto}//${window.location.host}/api/v1/term?${q.toString()}`
}

// Mobile gets a smaller default font so a typical phone shows ~70-80 cols
// instead of word-wrapping at every other word.
function pickFontSize(): number {
  if (typeof window === 'undefined') return 13
  return window.innerWidth < 640 ? 11 : 13
}

async function connect(ticket: string) {
  if (!selectedId.value || !containerEl.value) return
  disposeSession()
  lastError.value = ''
  status.value = 'connecting'

  term = new Terminal({
    cursorBlink: true,
    fontFamily: '"JetBrains Mono", ui-monospace, monospace',
    fontSize: pickFontSize(),
    lineHeight: 1.2,
    convertEol: false,
    scrollback: 5000,
    theme: {
      background: '#000000',
      foreground: '#d1d5db',
      cursor: '#10b981',
      cursorAccent: '#000000',
      selectionBackground: '#1f2937',
      black:     '#0f172a',
      red:       '#ef4444',
      green:     '#10b981',
      yellow:    '#f59e0b',
      blue:      '#60a5fa',
      magenta:   '#ec4899',
      cyan:      '#22d3ee',
      white:     '#d1d5db',
      brightBlack:   '#475569',
      brightRed:     '#f87171',
      brightGreen:   '#34d399',
      brightYellow:  '#fbbf24',
      brightBlue:    '#93c5fd',
      brightMagenta: '#f472b6',
      brightCyan:    '#67e8f9',
      brightWhite:   '#f3f4f6'
    }
  })
  fit = new FitAddon()
  term.loadAddon(fit)
  term.loadAddon(new WebLinksAddon())
  term.open(containerEl.value)
  try { fit.fit() } catch { /* */ }

  // Browser-shortcut interception. Without this, Ctrl+W closes the tab
  // instead of running bash's kill-word, Ctrl+R reloads instead of
  // reverse-history-search, Ctrl+T opens a new tab instead of bash's
  // transpose-chars, etc. — i.e. the "shortcut keys don't work" symptom.
  //
  // attachCustomKeyEventHandler runs BEFORE xterm's own key dispatch.
  // Returning true = let xterm process the key normally (which sends
  // the matching control byte to the PTY); we call preventDefault() to
  // stop the browser from also acting on it.
  //
  // Special case for Ctrl+C: if the user has a text selection in the
  // terminal, the conventional expectation is "copy to clipboard" (like
  // every other terminal emulator). Pass that through to the browser.
  // No selection = send SIGINT, our default.
  //
  // Cmd+* on macOS is left alone — that's the OS-level shortcut layer
  // (Cmd+C/V for clipboard, Cmd+W for tab) and users expect it to do
  // OS things, not be hijacked by the terminal.
  term.attachCustomKeyEventHandler((e: KeyboardEvent) => {
    if (e.type !== 'keydown') return true
    if (!e.ctrlKey || e.metaKey || e.altKey) return true

    // Ctrl+C with selection → let browser copy. Without selection →
    // through to xterm so the PTY gets SIGINT.
    if (e.key === 'c' && term?.hasSelection()) return false

    // Allow browser-default for Ctrl+V (paste) and Ctrl+Insert (rare
    // alt paste) — clipboard text goes through xterm's paste handler
    // automatically because the focused element receives the paste
    // event.
    if (e.key === 'v' || e.key === 'Insert') return true

    // Common conflicts: bash/zsh readline shortcuts that browsers
    // hijack. preventDefault stops the browser action; returning true
    // lets xterm send the corresponding control byte to the PTY.
    const hijacked = new Set([
      'w',  // kill word back (bash) vs close tab (browser)
      'r',  // reverse history search vs reload
      't',  // transpose chars vs new tab
      'n',  // next history vs new window
      'l',  // clear screen vs focus address bar (Firefox/Safari)
      'p',  // prev history (bash) vs print
      's',  // bash terminal scroll-lock vs save page
      'd',  // EOF / delete-char vs bookmark
      'k',  // kill-line vs browser search bar
      'j',  // accept-line literal vs downloads (Chrome)
      'h',  // backspace alt vs Firefox history
      'g',  // bell / cancel-search vs find-next
      'f',  // forward-char vs find
      'o',  // accept-line + next vs open file
      'q',  // quit/resume flow control vs nothing useful
      'a',  // beg-of-line vs select-all in page (we want it in terminal)
      'e',  // end-of-line vs nothing useful
      'b',  // back-char vs Firefox bookmarks
      'u',  // kill-line backward vs view source (some browsers)
      'y',  // yank vs Firefox downloads
    ])
    if (hijacked.has(e.key.toLowerCase())) {
      e.preventDefault()
      return true
    }

    return true
  })

  ws = new WebSocket(buildWSURL(selectedId.value, ticket))
  ws.binaryType = 'arraybuffer'

  ws.onopen = () => {
    status.value = 'open'
    // Send initial size right after connect.
    if (term && ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
    }
  }
  ws.onmessage = (ev) => {
    if (!term) return
    if (ev.data instanceof ArrayBuffer) {
      term.write(new Uint8Array(ev.data))
    } else if (typeof ev.data === 'string') {
      term.write(ev.data)
    }
  }
  ws.onerror = () => {
    status.value = 'error'
    lastError.value = 'WebSocket error'
  }
  ws.onclose = (ev) => {
    status.value = 'closed'
    // Codes worth surfacing to the operator:
    //   1000  normal close (no message needed)
    //   1005  no status code received (treated as clean)
    //   1006  abnormal closure — no Close frame. The most common cause on
    //         mobile is a network blip: WiFi↔cellular handoff, carrier NAT
    //         drop after backgrounding the tab, or the tab being suspended
    //         by the OS. Server-side WS keepalive (ping every 20s) prevents
    //         most idle-drop cases, but a real network change still trips
    //         this. Surface a useful hint instead of a bare code.
    //   1011  internal server error (we send a CloseMessage on cancel)
    //   any   anything else gets the bare code so we can debug
    if (ev.code !== 1000 && ev.code !== 1005) {
      if (ev.code === 1006) {
        lastError.value = navigator.onLine
          ? 'connection dropped (1006) — network or carrier NAT changed. tap "open shell" to reconnect.'
          : 'connection dropped (1006) — device is offline. reconnect once your network is back.'
      } else if (ev.code === 4001 || ev.code === 4003) {
        lastError.value = `unauthorized (${ev.code}) ${ev.reason || ''} — session expired or MFA required.`
      } else {
        lastError.value = `closed code=${ev.code} ${ev.reason || ''}`
      }
    }
    term?.writeln(`\r\n\x1b[33m[term]\x1b[0m disconnected.`)
  }

  // Forward keystrokes to PTY as raw bytes (real keyboard input).
  // Mobile quick-key buttons go through the same sendBytes path, so the
  // PTY can't distinguish them from a hardware keyboard.
  //
  // When the sticky Ctrl modifier is armed, the FIRST inbound character
  // that's a single ASCII letter is rewritten to its Ctrl+letter byte
  // (Ctrl+A → 0x01, …, Ctrl+Z → 0x1A) and the modifier auto-disarms.
  // Any other inbound data (multi-char paste, special key like Backspace,
  // a digit, punctuation) cancels the pending Ctrl WITHOUT being
  // rewritten — safer than guessing what `Ctrl+5` should mean on a
  // mobile keyboard, and lets the operator type around a misfire.
  term.onData((data) => {
    // Sticky Ctrl path: if the modifier is armed AND the first inbound
    // char is an ASCII letter, rewrite that first char to its Ctrl+letter
    // control byte and pass any trailing chars through verbatim.
    //
    // Previously this required `data.length === 1` strictly. That broke
    // on Android Gboard (and a few iOS keyboards in predictive mode)
    // where a single letter tap can deliver `letter + ' '` (autocomplete
    // glue) in one onData event — the strict check dropped the modifier
    // and the operator's Ctrl+letter became plain letter+space.
    if (ctrlPending.value && data.length >= 1) {
      const c = data.charCodeAt(0)
      const isLower = c >= 0x61 && c <= 0x7A   // 'a'..'z'
      const isUpper = c >= 0x41 && c <= 0x5A   // 'A'..'Z'
      if (isLower || isUpper) {
        // ASCII trick: 'a' (0x61) & 0x1F == 0x01 == Ctrl+A.
        const rewritten = String.fromCharCode(c & 0x1F) + data.slice(1)
        sendBytes(rewritten)
        ctrlPending.value = false
        // Snap the view to the cursor row. Without this, a user who
        // scrolled the scrollback up to read something would type
        // "blind" — input would commit but the view would stay parked
        // at the old position, hiding the cursor row.
        term?.scrollToBottom()
        return
      }
      // First char isn't a letter while armed — drop the modifier and
      // send the data unchanged. This covers digits, punctuation, and
      // multi-char IME composition (Chinese pinyin, emoji, etc.) where
      // Ctrl + char makes no sense at the PTY layer.
      ctrlPending.value = false
    }
    sendBytes(data)
    term?.scrollToBottom()
  })
  // Forward terminal resize events as JSON ctrl messages.
  term.onResize(({ cols, rows }) => {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'resize', cols, rows }))
    }
  })

  // Refit when the container's dimensions change — coalesced through one
  // rAF and guarded against re-firing on the sub-pixel deltas that fit()
  // itself produces. Without the guard, fit() resizes the canvas, which
  // the observer sees as a new size, which calls fit() again: on desktop
  // the classic scrollbar appearing/disappearing as output streams toggles
  // the content width by ~15px every frame, so the loop never settles and
  // the terminal visibly shudders. Mobile uses overlay scrollbars (zero
  // width) so it never showed. Only refit when the integer box actually
  // changed.
  let fitRaf = 0
  let lastW = 0
  let lastH = 0
  resizeObserver = new ResizeObserver((entries) => {
    const rect = entries[0]?.contentRect
    if (!rect) return
    const w = Math.round(rect.width)
    const h = Math.round(rect.height)
    if (w === lastW && h === lastH) return
    lastW = w
    lastH = h
    if (fitRaf) return
    fitRaf = requestAnimationFrame(() => { fitRaf = 0; try { fit?.fit() } catch { /* */ } })
  })
  resizeObserver.observe(containerEl.value)

  // Initial focus
  term.focus()
}

function disconnect() {
  if (ws && ws.readyState === WebSocket.OPEN) ws.close(1000, 'user')
}

watch(selectedId, () => { if (status.value === 'open' || status.value === 'connecting') disconnect() })

// Lock <body> to the visual viewport when the mobile shell layout is
// active, and restore the previous state when leaving. Setting overflow
// hidden + height 100dvh prevents the iOS Safari rubber-band + page-scroll
// behavior that pushes the terminal cursor offscreen mid-typing.
watch(mobileShellActive, (active) => {
  if (typeof document === 'undefined') return
  const root = document.documentElement
  const body = document.body
  if (active) {
    root.classList.add('ncn-term-mobile-lock')
    body.classList.add('ncn-term-mobile-lock')
  } else {
    root.classList.remove('ncn-term-mobile-lock')
    body.classList.remove('ncn-term-mobile-lock')
  }
})

onMounted(() => {
  // Detect coarse pointer (touchscreen primary input) once; we don't watch
  // it because plug-in/-out of an external pointer mid-session is rare and
  // a refresh resolves it cleanly. Cached so re-renders don't keep calling
  // matchMedia.
  if (typeof window !== 'undefined') {
    isCoarsePointer.value = window.matchMedia?.('(pointer: coarse)').matches ?? false
  }

  // Track soft-keyboard height via the visualViewport API. When the keyboard
  // opens, vv.height shrinks by ~250-350px (varies by device/keyboard);
  // the difference IS the keyboard height (modulo a tiny URL-bar wiggle on
  // iOS Safari). We expose this as keyboardOffsetPx so the quick-key bar
  // can sit at `bottom: keyboardOffsetPx` from the layout viewport bottom,
  // i.e. just above the keyboard's top edge.
  //
  // Why visualViewport: the layout viewport does NOT shrink when the soft
  // keyboard opens, so `bottom: 0` on a fixed-position element places it
  // BEHIND the keyboard. visualViewport is the standardized way to get the
  // currently-visible viewport excluding the keyboard.
  if (typeof window !== 'undefined' && window.visualViewport) {
    const vv = window.visualViewport
    const updateOffset = () => {
      // keyboard height = layout viewport height - visible viewport bottom edge
      // (offsetTop accounts for the URL bar on iOS — when it's expanded,
      // the visual viewport doesn't start at y=0)
      const offset = Math.max(0, Math.round(window.innerHeight - vv.height - vv.offsetTop))
      // Snap small values to 0 so the bar doesn't jitter from sub-pixel
      // URL-bar animations on iOS Safari.
      keyboardOffsetPx.value = offset < 24 ? 0 : offset

      // Publish the current visible-viewport height to :root as a CSS
      // custom property. Why: iOS Safari has a long-standing limitation
      // where `100dvh` does NOT shrink when the on-screen keyboard
      // opens — only when the URL bar collapses. Layout containers that
      // use `h-dvh` therefore stay full-height behind the keyboard, and
      // the terminal cursor row gets pushed offscreen. By driving the
      // AdminLayout root and the terminal-host height from this JS-set
      // pixel value, the entire admin shell shrinks with the keyboard
      // on iOS too. Android Chrome's dvh already tracks the keyboard,
      // but using the JS value uniformly keeps both platforms identical
      // and removes one source of layout drift.
      document.documentElement.style.setProperty(
        '--ncn-vvh', `${Math.round(vv.height)}px`,
      )
    }
    vv.addEventListener('resize', updateOffset)
    vv.addEventListener('scroll', updateOffset)
    updateOffset()
    visualViewportCleanup = () => {
      vv.removeEventListener('resize', updateOffset)
      vv.removeEventListener('scroll', updateOffset)
    }
  }

  // Don't auto-connect — user must click to open a shell (it's a privileged action).
})
onBeforeUnmount(() => {
  disposeSession()
  visualViewportCleanup?.()
  visualViewportCleanup = null
  // Always restore document/body classes + clear the --ncn-vvh custom
  // property. Otherwise navigating away mid-session would leave the rest
  // of the app stuck in scroll-lock mode or with a stale visible-viewport
  // height computed from a moment the user was on the terminal page.
  if (typeof document !== 'undefined') {
    document.documentElement.classList.remove('ncn-term-mobile-lock')
    document.body.classList.remove('ncn-term-mobile-lock')
    document.documentElement.style.removeProperty('--ncn-vvh')
  }
})

const statusLabel = computed(() => ({
  idle:        '[idle]',
  connecting:  '[connecting...]',
  open:        '[live]',
  closed:      '[closed]',
  error:       '[error]'
}[status.value]))

const statusColor = computed(() => ({
  idle:        'text-gray-500',
  connecting:  'text-amber-400',
  open:        'text-emerald-500',
  closed:      'text-gray-500',
  error:       'text-red-500'
}[status.value]))
</script>

<template>
  <!-- Flex column that fills the AdminLayout's scroll section. This
       replaces the previous space-y-4 stack + calc()-driven .ncn-term-host
       height. The calc approach was fragile on mobile because we had to
       reverse-engineer every chrome offset (header, banner, tabs, status,
       bar) and any drift produced a too-tall terminal whose bottom rows
       sat under the keyboard or off-screen.
       Now the panel grows via flex-1 to whatever space remains, and the
       inner .ncn-term-host fills the panel via flex-1 — xterm gets exactly
       the visible area on every browser / orientation / keyboard state.

       `ncn-term-page` exposes a stable hook for the mobile-shell layout
       rules below. When mobileShellActive is true (coarse pointer + live
       PTY + soft keyboard open), `mobile-shell-active` is added, which
       collapses the chrome and gives the terminal almost the full visible
       viewport. -->
  <div class="ncn-term-page flex flex-col h-full min-h-0 gap-4"
       :class="{ 'mobile-shell-active': mobileShellActive }">
    <!-- Meta banner: terminal title, status pill, open/disconnect button,
         audit notice. Hidden in mobile-shell-active mode — the operator
         already opened the shell; the still-visible chrome (compact
         status row, quick-key bar) carries the disconnect affordance. -->
    <div class="border border-gray-800 bg-gray-900 p-4 ncn-term-meta shrink-0">
      <div class="flex items-center justify-between flex-wrap gap-2">
        <div class="flex items-center gap-3">
          <span :class="['w-1.5 h-1.5', status === 'open' ? 'bg-emerald-500 animate-pulse' : 'bg-gray-600']"></span>
          <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">终端 · Terminal</h1>
          <span :class="['text-[10px] tracking-widest font-mono', statusColor]">{{ statusLabel }}</span>
        </div>
        <div class="flex items-center gap-2">
          <button
            v-if="status !== 'open' && status !== 'connecting'"
            type="button"
            @click="requestConnect"
            class="px-3 py-1.5 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-[10px] tracking-widest uppercase transition-colors"
          >▶ open shell</button>
          <button
            v-else
            type="button"
            @click="disconnect"
            class="px-3 py-1.5 border border-red-500 text-red-500 hover:bg-red-500 hover:text-black text-[10px] tracking-widest uppercase transition-colors"
          >⨯ disconnect</button>
        </div>
      </div>
      <p class="mt-2 text-[10px] tracking-widest text-gray-700 uppercase font-mono">
        // root shell · all keystrokes audited to journald · session ttl = your login (8h)
      </p>
    </div>

    <!-- NodeTabs hidden in mobile-shell-active mode. Switching tabs during
         a live session is destructive (forces disconnect — see watch on
         selectedId below), so removing the affordance until the keyboard
         is dismissed is the desired behavior. -->
    <div class="ncn-term-nodetabs shrink-0">
      <NodeTabs v-model="selectedId" :nodes="tabNodes" />
    </div>

    <!-- Terminal panel.
         `qk-active` triggers a CSS rule that subtracts ~50px from the
         .ncn-term-host height so the floating quick-key bar (which sits
         at the keyboard's top edge via visualViewport) doesn't overlap
         the last terminal row. The ResizeObserver inside connect() picks
         up the height change and refits xterm + ships a `resize` ctrl
         message to the PTY, so cols/rows stay accurate. -->
    <div class="border border-gray-800 bg-black relative flex flex-col flex-1 min-h-0"
         :class="{ 'qk-active': isCoarsePointer && status === 'open' }">
      <div class="border-b border-gray-800 bg-gray-950 font-mono shrink-0">
        <!-- Status row: always shows the active target. Errors get their
             own row below so they can wrap freely instead of fighting for
             space with the target label and getting `...`-truncated. -->
        <div class="px-3 py-1.5 flex items-center justify-between flex-wrap gap-2 text-[10px] tracking-widest uppercase">
          <span class="text-gray-500">
            target: <span class="text-emerald-500">{{ selectedId || '—' }}</span>
          </span>
          <span v-if="status === 'open' || status === 'connecting'"
                class="text-emerald-500 normal-case tracking-normal">● {{ status }}</span>
        </div>
        <!-- Error row — full-width, multi-line, monospace. Long errors
             (verbose WebSocket close reasons, server stack traces forwarded
             through, etc.) wrap into the cell instead of being clipped. -->
        <div v-if="lastError"
             class="px-3 py-1.5 border-t border-red-900/60 bg-red-950/30 text-xs text-red-400 normal-case tracking-normal leading-snug break-all"
             style="overflow-wrap: anywhere;"
        >
          <span class="text-[10px] tracking-widest uppercase text-red-500 mr-2 align-baseline">⨯ err</span>{{ lastError }}
        </div>
      </div>
      <div ref="containerEl" class="ncn-term-host flex-1 min-h-0" :class="{ 'ncn-term-idle': status !== 'open' && status !== 'connecting' }"></div>

      <!-- "No session" overlay. Anchored under the panel's status header
           (the v-if'd error row makes that header's height variable, so
           we use a flexbox row that pushes the card down by the header
           height naturally rather than a hard-coded `top-[34px]`).
           The card is constrained on mobile so it never overflows the
           panel; on desktop it stays the original max-md width. -->
      <div v-if="status === 'idle' || status === 'closed' || status === 'error'"
           class="absolute inset-0 flex items-center justify-center pointer-events-none p-4">
        <div class="border border-gray-800 bg-gray-900/85 px-4 sm:px-6 py-3 sm:py-4 text-center pointer-events-auto max-w-full sm:max-w-md w-full sm:w-auto">
          <div class="text-[10px] tracking-widest text-gray-600 uppercase mb-2">no session</div>
          <div class="text-[11px] sm:text-xs text-gray-400 leading-relaxed break-words">
            Click <span class="text-emerald-500">▶ open shell</span> to allocate a PTY on
            <span class="text-emerald-500 break-all">{{ selectedId || '—' }}</span>.
            A risk-acknowledgement prompt will appear before the session is created.
            Switching the node tab during an open session will disconnect.
          </div>
        </div>
      </div>
    </div>

    <!-- Risk-acknowledgement modal — operator confirms with a fresh MFA
         step-up (passkey OR TOTP). The session cookie already proves
         password possession, so we don't re-ask for the password here.
         Positioning:
           • mobile: bottom-sheet pinned ~16px above the screen bottom (with
             iOS safe-area inset), so the dialog rides just above the soft
             keyboard and doesn't merge with the system gesture bar.
           • tablet/desktop: centered with a slight upward bias (~35% from
             top via padding-top), placing it on the user's natural reading
             eye-line rather than mid-screen. -->
    <transition
      enter-active-class="transition duration-150 ease-out"
      enter-from-class="opacity-0"
      enter-to-class="opacity-100"
      leave-active-class="transition duration-100 ease-in"
      leave-from-class="opacity-100"
      leave-to-class="opacity-0"
    >
      <div
        v-if="confirmOpen"
        class="ncn-modal-backdrop"
        @click.self="onAckCancel"
      >
        <transition
          enter-active-class="transition duration-200 ease-out"
          enter-from-class="opacity-0 translate-y-4 sm:translate-y-2"
          enter-to-class="opacity-100 translate-y-0"
          appear
        >
          <div class="ncn-modal-card border-2 border-red-600 bg-gray-900 font-mono flex flex-col">
            <div class="px-3 sm:px-4 py-2 border-b border-red-600 bg-red-900/40 text-red-400 text-[10px] sm:text-xs tracking-widest uppercase shrink-0 flex items-center justify-between gap-2">
              <span>⚠ SENSITIVE OPERATION · severity=high</span>
              <button
                type="button"
                @click="onAckCancel"
                :disabled="ackBusy"
                aria-label="Close"
                class="text-red-400 hover:text-red-200 sm:hidden text-sm leading-none disabled:opacity-50"
              >✕</button>
            </div>
            <div class="p-3 sm:p-4 overflow-y-auto flex-1">
              <h3 class="text-sm sm:text-base text-gray-100 mb-2">
                Open root shell on <span class="text-red-400">{{ selectedId || '—' }}</span>
              </h3>
              <div class="text-xs text-gray-400 leading-relaxed normal-case tracking-normal space-y-2">
                <p>
                  About to allocate a privileged root PTY on
                  <span class="text-gray-200">{{ selectedId || '—' }}</span>
                  (<span class="text-gray-200">{{ nodeAddrFor(selectedId) }}</span>).
                  Every keystroke is forwarded as-is to the live system — no undo,
                  no per-command prompt, no rate limit.
                </p>
                <p>
                  <span class="text-gray-300">What this shell can do:</span>
                  edit /etc/bird/, restart routing daemons, change firewall, wipe data,
                  reboot, rotate keys.
                </p>
                <p>
                  <span class="text-gray-300">Audit:</span> session start + end + 5min
                  heartbeat logged to journald with operator id
                  (<span class="text-emerald-500">{{ session.operator || '?' }}</span>)
                  and source IP. Cannot be deleted from this UI.
                </p>
                <p class="text-amber-400">
                  Second factor (passkey or TOTP) required to open shell.
                </p>
              </div>

              <!-- MFA selector — operator picks which factor to verify with.
                   Defaults to passkey when bound (one tap on Touch ID), falls
                   back to TOTP if the browser can't do WebAuthn right now. -->
              <div class="mt-3 flex items-center gap-2 text-[10px] tracking-widest uppercase text-gray-600">
                <span>2nd factor:</span>
                <button
                  type="button"
                  @click="switchMfaMethod('passkey')"
                  :disabled="ackBusy || !passkeyAvailable"
                  :class="[
                    'px-2 py-1 border transition-colors touch-manipulation',
                    ackMfaMethod === 'passkey'
                      ? 'border-emerald-500 text-emerald-500 bg-emerald-500/10'
                      : 'border-gray-800 text-gray-500 hover:border-gray-600 hover:text-gray-300',
                    !passkeyAvailable && 'opacity-30 cursor-not-allowed'
                  ]"
                >🔑 passkey</button>
                <button
                  type="button"
                  @click="switchMfaMethod('totp')"
                  :disabled="ackBusy || !totpAvailable"
                  :class="[
                    'px-2 py-1 border transition-colors touch-manipulation',
                    ackMfaMethod === 'totp'
                      ? 'border-emerald-500 text-emerald-500 bg-emerald-500/10'
                      : 'border-gray-800 text-gray-500 hover:border-gray-600 hover:text-gray-300',
                    !totpAvailable && 'opacity-30 cursor-not-allowed'
                  ]"
                >⏱ totp</button>
              </div>

              <!-- TOTP code field — only when method=totp -->
              <input
                v-if="ackMfaMethod === 'totp'"
                ref="ackInputEl"
                v-model="ackTotpCode"
                type="text"
                inputmode="numeric"
                pattern="[0-9]*"
                maxlength="6"
                autocomplete="one-time-code"
                :disabled="ackBusy"
                placeholder="000000"
                @keyup.enter="onAckSubmit"
                class="mt-3 w-full bg-black border border-gray-800 px-3 py-3 sm:py-2 text-xl sm:text-lg font-mono tracking-[0.4em] tabular-nums text-gray-100 text-center placeholder:text-gray-700 focus:border-red-500 focus:outline-none disabled:opacity-50 rounded-none"
              />
              <p v-else class="mt-3 text-[11px] sm:text-[10px] text-gray-500 normal-case tracking-normal leading-relaxed">
                Browser will pop up a passkey verification dialog (Touch ID / Face ID / YubiKey / password manager) right after you click open shell.
              </p>

              <div v-if="ackErr" class="mt-2 text-xs text-red-400 normal-case tracking-normal">
                ⨯ {{ ackErr }}
              </div>
            </div>
            <div class="flex border-t border-gray-800 shrink-0">
              <button
                type="button"
                @click="onAckCancel"
                :disabled="ackBusy"
                class="flex-1 px-3 sm:px-4 py-3 text-[11px] sm:text-xs tracking-widest uppercase text-gray-400 hover:bg-gray-800 transition-colors disabled:opacity-50"
              >cancel</button>
              <button
                type="button"
                @click="onAckSubmit"
                :disabled="ackBusy
                  || (ackMfaMethod === 'totp' && ackTotpCode.trim().length < 6)
                  || (ackMfaMethod === 'passkey' && !passkeyAvailable)"
                class="flex-1 px-3 sm:px-4 py-3 text-[11px] sm:text-xs tracking-widest uppercase text-white bg-red-600 hover:bg-red-500 transition-colors disabled:opacity-30 disabled:cursor-not-allowed touch-manipulation"
              >{{ ackBusy ? '◌ verifying' : ackMfaMethod === 'passkey' ? '🔑 verify with passkey' : '▶ open shell' }}</button>
            </div>
          </div>
        </transition>
      </div>
    </transition>

    <!-- ===== Mobile quick-key bar (Termux-style, body-teleported) =====
         Floats just above the soft keyboard's top edge. Teleported to
         <body> so no ancestor's containing block (transform, filter, etc.)
         can break `position: fixed`. The `bottom` is bound to the visual-
         viewport-tracked keyboard height — when the keyboard is closed
         keyboardOffsetPx is 0 and the bar sits at the bottom of the
         viewport; when the keyboard opens, it slides up with it.
         Only renders on coarse-pointer devices with a live PTY session. -->
    <Teleport to="body">
      <div
        v-if="isCoarsePointer && status === 'open'"
        class="ncn-quickkeys"
        :style="{ bottom: keyboardOffsetPx + 'px' }"
        role="toolbar"
        aria-label="Terminal quick keys"
      >
        <div v-if="!quickKeysCollapsed" class="ncn-quickkeys-row">
          <!-- Sticky Ctrl modifier. When armed, the next letter typed on
               the soft keyboard is sent as its Ctrl+letter control byte
               (Ctrl+C, Ctrl+D, Ctrl+L, ...). Tap Ctrl again — or tap any
               other quick-key — to cancel. Visual `is-armed` highlight
               so the operator can see the modifier state at a glance. -->
          <!-- Both pointerdown AND click bound — iOS Safari (and a few
               Android WebViews) intermittently fail to fire pointerdown
               on position:fixed teleported elements. The handler dedups
               via isDuplicateTap() so the redundant binding is harmless
               when both events fire on the same tap. -->
          <button
            type="button"
            :title="ctrlPending ? 'Ctrl armed — next letter is Ctrl+X. tap again to cancel.' : 'Ctrl (sticky) — tap, then tap a letter on the soft keyboard'"
            @pointerdown.prevent="onToggleCtrl"
            @click.prevent="onToggleCtrl"
            :class="['ncn-qk-btn', 'ncn-qk-ctrl', ctrlPending && 'is-armed']"
          >Ctrl</button>
          <button
            v-for="k in QUICK_KEYS"
            :key="k.label"
            type="button"
            :title="k.title || k.label"
            @pointerdown.prevent="onQuickKey(k.seq)"
            @click.prevent="onQuickKey(k.seq)"
            class="ncn-qk-btn"
          >{{ k.label }}</button>
        </div>
        <button
          type="button"
          @pointerdown.prevent="onToggleQuickKeysCollapse"
          @click.prevent="onToggleQuickKeysCollapse"
          :title="quickKeysCollapsed ? 'Show quick keys' : 'Hide quick keys'"
          class="ncn-qk-toggle"
        >{{ quickKeysCollapsed ? '▴ keys' : '▾' }}</button>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
/* ---------------------------------------------------------------------------
 * Risk-ack modal positioning
 *
 * Mobile (< 640px): bottom-sheet feel. Pinned ~16px above the screen
 *   bottom (with iOS safe-area inset), centered horizontally with a small
 *   left/right gap so the card doesn't touch the screen edge.
 *
 * Tablet+ (≥ 640px): centered, but biased ~10vh toward the top so the
 *   reading eye-line lands on the headline naturally (Z-pattern reading)
 *   instead of having to skim the whole screen.
 * ------------------------------------------------------------------------- */
.ncn-modal-backdrop {
  position: fixed;
  inset: 0;
  z-index: 100;
  display: flex;
  align-items: flex-end;          /* bottom-sheet on mobile */
  justify-content: center;
  padding: 12px 12px calc(12px + env(safe-area-inset-bottom)) 12px;
  background: rgba(0, 0, 0, 0.7);
  backdrop-filter: blur(4px);
  -webkit-backdrop-filter: blur(4px);
}
.ncn-modal-card {
  width: 100%;
  max-width: 28rem;               /* ~448px — tablet-friendly */
  max-height: min(88dvh, 720px);
}

@media (min-width: 640px) {
  .ncn-modal-backdrop {
    align-items: flex-start;      /* centered with upward bias */
    padding: 14vh 16px 16px 16px;
  }
}
@media (min-width: 1024px) {
  .ncn-modal-backdrop {
    padding-top: 18vh;
  }
}

.ncn-term-host {
  /* Sizing is now handled by the parent flex chain (ncn-term-page →
     panel → host, all with flex-1 + min-h-0). The host takes whatever
     vertical space the chain has left, no calc() involved. Padding
     stays for visual breathing room inside the xterm viewport.
     A floor of 160px keeps the terminal at least 10-ish lines tall on
     pathological viewports (very small phones in landscape with the
     keyboard open). */
  min-height: 160px;
  padding: 4px;
}
/* When the mobile quick-key bar is active, the bar floats at the bottom
   of the visual viewport via position:fixed (see .ncn-quickkeys below).
   Reserve that space at the bottom of the terminal panel so the bar
   doesn't overlap the cursor row. padding-bottom is on the PANEL (flex
   parent), so the .ncn-term-host (a flex-1 child) shrinks accordingly. */
.qk-active {
  padding-bottom: 50px;
}
.ncn-term-idle {
  filter: brightness(0.5);
}
:deep(.xterm) {
  padding: 4px 6px;
}
@media (min-width: 640px) {
  :deep(.xterm) { padding: 6px 8px; }
}
:deep(.xterm-viewport) {
  background-color: #000 !important;
  /* Always reserve the scrollbar gutter. On desktop the classic scrollbar
     would otherwise appear the instant output overflows and vanish when it
     fits, changing the viewport's content width by the scrollbar's ~15px
     each time — the trigger for the ResizeObserver→fit→resize shudder. With
     the gutter reserved the width is constant whether or not the bar shows.
     Pairs with the size-guarded observer in connect(). */
  scrollbar-gutter: stable;
}

/* ============================================================================
   Mobile shell mode — keyboard open + live PTY + coarse pointer.
   ============================================================================
   When the operator's soft keyboard is up during a live session, we want
   the terminal to dominate the tiny visible viewport. The page-level
   class `mobile-shell-active` (toggled by Terminal.vue's computed) is
   the hook for those rules.

   The body / html lock (added via watcher on mobileShellActive, see the
   <script>) prevents iOS Safari from rubber-band scrolling under the
   user's finger — the most disorienting mobile-keyboard symptom. */
:global(html.ncn-term-mobile-lock),
:global(body.ncn-term-mobile-lock) {
  overflow: hidden;
  /* JS-driven visible-viewport height — see updateOffset() and the
     comment block on .ncn-vvh-shell in src/style.css. 100dvh fallback
     for the brief moment between mount and the first visualViewport
     event (and for desktop where the keyboard-shrink case never fires). */
  height: var(--ncn-vvh, 100dvh);
  overscroll-behavior: none;
}

/* Page-level chrome that's irrelevant once a session is live + keyboard
   is up: the meta banner (title / open-shell button / audit notice) and
   the NodeTabs row. The terminal panel itself (with its status row) +
   the floating quick-key bar carry everything the operator needs. */
.mobile-shell-active .ncn-term-meta,
.mobile-shell-active .ncn-term-nodetabs {
  display: none;
}
/* Drop the inter-section gap when chrome is collapsed — every pixel
   counts on a 480-ish-tall keyboard-open viewport. The page is now a
   flex column with `gap-4` (16px), so we zero it out in this mode. */
.mobile-shell-active.ncn-term-page {
  gap: 0 !important;
}

/* ============================================================================
   Mobile quick-key bar — Termux-inspired.
   ============================================================================
   Teleport-mounted on <body>, so style rules need `:global()` to escape
   the scoped-CSS attribute selector (Vue would otherwise rewrite them to
   `[data-v-…] .ncn-quickkeys`, which never matches an element outside the
   component's DOM subtree).

   Auto-shown on coarse-pointer devices when a session is live (see
   template v-if guard). The bar is `position: fixed` with `bottom` set
   inline from the visualViewport-tracked keyboardOffsetPx, which is the
   pixel-perfect height of the soft keyboard. Result: the bar sits exactly
   on top of the keyboard's top edge, never hidden behind it; the user
   doesn't need to scroll to see it, so iOS Safari's scroll-blur-input
   heuristic doesn't fire and the keyboard stays up between taps.

   The row is horizontally scrollable so 21 keys survive on 360px phones;
   each button is ~44×34, the iOS HIG minimum tap target. A separate
   sticky ▾ pill on the right collapses the row when the operator wants
   more vertical room. */
:global(.ncn-quickkeys) {
  position: fixed;
  left: 0;
  right: 0;
  /* bottom set inline from keyboardOffsetPx */
  z-index: 50;
  display: flex;
  align-items: stretch;
  border-top: 1px solid rgb(31 41 55);          /* gray-800 */
  background: rgb(3 7 18);                       /* gray-950 */
  /* iOS home-indicator inset — only applies when keyboard is closed
     (keyboardOffsetPx===0), otherwise the keyboard already covers the
     home-indicator area. */
  padding-bottom: env(safe-area-inset-bottom, 0px);
  /* Don't capture any extra scroll/zoom gestures from the user — they
     belong to the terminal or page underneath. */
  touch-action: pan-x;
}
:global(.ncn-quickkeys-row) {
  flex: 1 1 auto;
  display: flex;
  gap: 4px;
  padding: 6px 8px;
  overflow-x: auto;
  overflow-y: hidden;
  scrollbar-width: none;                          /* Firefox */
  -webkit-overflow-scrolling: touch;              /* iOS momentum */
}
:global(.ncn-quickkeys-row::-webkit-scrollbar) {
  display: none;
}
:global(.ncn-qk-btn) {
  flex: 0 0 auto;
  min-width: 44px;
  height: 34px;
  padding: 0 10px;
  border: 1px solid rgb(31 41 55);                /* gray-800 */
  background: rgb(17 24 39);                      /* gray-900 */
  color: rgb(209 213 219);                        /* gray-300 */
  font-family: "JetBrains Mono", ui-monospace, monospace;
  font-size: 12px;
  letter-spacing: 0.04em;
  border-radius: 0;
  user-select: none;
  -webkit-user-select: none;
  -webkit-tap-highlight-color: transparent;
  transition: background 100ms, color 100ms, border-color 100ms;
}
:global(.ncn-qk-btn:active) {
  background: rgb(16 185 129);                    /* emerald-500 */
  color: #000;
  border-color: rgb(16 185 129);
}
/* Armed-Ctrl visual state: amber outline + amber text, distinct from the
   transient `:active` emerald press so the operator can tell the
   modifier is held armed waiting for the next letter. */
:global(.ncn-qk-ctrl.is-armed) {
  background: rgba(245, 158, 11, 0.15);            /* amber-500/15 */
  color: rgb(245 158 11);                          /* amber-500 */
  border-color: rgb(245 158 11);
}
:global(.ncn-qk-ctrl.is-armed:active) {
  background: rgb(245 158 11);
  color: #000;
}
:global(.ncn-qk-toggle) {
  flex: 0 0 auto;
  width: 42px;
  border: none;
  border-left: 1px solid rgb(31 41 55);
  background: rgb(3 7 18);
  color: rgb(107 114 128);                        /* gray-500 */
  font-family: "JetBrains Mono", ui-monospace, monospace;
  font-size: 11px;
  letter-spacing: 0.1em;
  user-select: none;
  -webkit-tap-highlight-color: transparent;
}
:global(.ncn-qk-toggle:active) {
  color: rgb(16 185 129);
}
</style>
