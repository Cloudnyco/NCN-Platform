<!--
  App.vue — top-level webmail UI for mail.example.com.
  Self-contained: no shared imports with the operator console.
-->
<script setup lang="ts">
import { ref, computed, onMounted, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from './components/Icon.vue'

const { t, locale } = useI18n()

type Phase = 'login' | 'mailbox'

interface Folder {
  name: string
  total?: number
  unseen?: number
  flags?: string[]
}

interface MessageHeader {
  uid: number
  seq: number
  size: number
  flags: string[]
  date: string
  subject?: string
  from?: string[]
  to?: string[]
  message_id?: string
}

interface MessageBody {
  uid: number
  flags: string[]
  date: string
  subject?: string
  from?: string[]
  to?: string[]
  cc?: string[]
  reply_to?: string[]
  message_id?: string
  text?: string
  html?: string
  attachments?: Array<{ filename: string; content_type: string; size: number }>
  parse_warning?: string
}

const phase = ref<Phase>('login')
const me = ref<{ mailbox: string; stashed: boolean; exp: number } | null>(null)
const loading = ref(false)
const errMsg = ref('')

// On mobile (<md) only one of the three panes is visible at a time.
// On md+ all three render side-by-side regardless of this value.
type MobileView = 'folders' | 'list' | 'reader'
const mobileView = ref<MobileView>('list')

// /invite/<token> mode — entire app pivots to the registration form.
interface InvitePreview {
  prefix?: string
  created_by: string
  expires_at: string
  note?: string
  domain: string
  operator?: boolean
  operator_op?: string
  operator_rol?: string
  suggested?: string
}
const registerToken = ref('')
const registerLoading = ref(true)
const registerPreview = ref<InvitePreview | null>(null)
const registerLocalPart = ref('')
const registerPassword = ref('')
const registerPasswordConfirm = ref('')
const registerSubmitting = ref(false)
const registerDone = ref<{ mailbox: string } | null>(null)

// /admin-recover/<token> — break-glass mailbox password reset minted by
// `ncn-mail admin mint-recover`. Separate from the registration flow:
// these tokens identify an EXISTING mailbox rather than minting a new one.
const recoverToken = ref('')
const recoverLoading = ref(true)
const recoverMailbox = ref('')
const recoverPassword = ref('')
const recoverPasswordConfirm = ref('')
const recoverSubmitting = ref(false)
const recoverErr = ref('')
const recoverDone = ref(false)

// Admin-only: token issuance panel, gated on the /api/v1/mail/invites endpoint
// returning 200 (vs 403 for ordinary mailboxes).
interface IssuedInvite {
  prefix: string
  created_by: string
  expires_at: string
  used_by?: string
  used_at?: string
  note?: string
}
const isAdmin = ref(false)
const invitesPanelOpen = ref(false)
const invitesList = ref<IssuedInvite[]>([])
const invitesBusy = ref(false)
const inviteNote = ref('')
const lastIssued = ref<{ token: string; url: string; expires_at: string } | null>(null)

// Admin password reset (inside the invites modal)
const resetMailbox = ref('')
const resetPassword = ref('')
const resetBusy = ref(false)
const resetDone = ref<{ mailbox: string } | null>(null)

// Settings panel (per-user) — currently only forwarding
const settingsOpen = ref(false)
const forwardAddresses = ref<string[]>([])
const forwardPending = ref<string[]>([])  // awaiting recipient click
const forwardVerifyJustSent = ref<string[]>([])  // surfaced after PUT
const forwardDraft = ref('')
const forwardBusy = ref(false)
const forwardSaved = ref(false)

// /verify-forward/<token> handler state
const verifyForwardToken = ref('')
const verifyForwardLoading = ref(true)
const verifyForwardOK = ref(false)
const verifyForwardRedirectIn = ref(0)  // seconds remaining before auto-redirect
const verifyForwardErr = ref('')
const verifyForwardAddress = ref('')

// Login: forgot-password info modal
const forgotOpen = ref(false)
const forgotMailbox = ref('')
const forgotBusy = ref(false)
const forgotDone = ref(false)
// Login: operator-only register info modal
const operatorRegisterOpen = ref(false)
const operatorBridgeURL = 'https://admin.example.com/admin/webmail-bridge'

// SSE: real-time new-mail push (IMAP IDLE on the server).
let sseClient: EventSource | null = null
const sseConnected = ref(false)
function startSSE() {
  if (sseClient) return
  const es = new EventSource('/api/v1/mail/events')
  sseClient = es
  es.addEventListener('ready', () => { sseConnected.value = true })
  es.addEventListener('mailbox', () => {
    // New mail arrived (or a flag changed). Refresh current view if it's
    // the INBOX (or whatever folder we're showing), plus folder counts.
    if (!searchActive.value) {
      loadMessages()
    }
    api<Folder[]>('/api/v1/mail/folders').then(f => { folders.value = f || [] }).catch(() => {})
  })
  es.onerror = () => {
    // EventSource auto-reconnects with backoff. Flag down for the UI.
    sseConnected.value = false
  }
}
function stopSSE() {
  sseClient?.close()
  sseClient = null
  sseConnected.value = false
}

// Health probe — drives the header pulse dot color.
type Health = 'unknown' | 'ok' | 'down'
const serverHealth = ref<Health>('unknown')
async function probeHealth() {
  try {
    const res = await fetch('/api/v1/mail/health', { credentials: 'omit', cache: 'no-store' })
    const env = await res.json()
    serverHealth.value = env.ok ? 'ok' : 'down'
  } catch {
    serverHealth.value = 'down'
  }
}

// Admin: pending forgot-password requests
interface ForgotRequestRow {
  id: string
  mailbox: string
  requested_at: string
  ip: string
  ua?: string
}
const forgotRequests = ref<ForgotRequestRow[]>([])

async function loadForgotRequests() {
  try {
    forgotRequests.value = (await api<ForgotRequestRow[]>('/api/v1/mail/forgot/requests')) || []
  } catch (e: any) {
    // Non-admin gets 403; silently leave empty.
    if (!String(e.message).includes('admin only')) {
      errMsg.value = e.message
    }
  }
}
async function dismissForgotRequest(id: string) {
  try {
    await api(`/api/v1/mail/forgot/requests/${id}`, { method: 'DELETE' })
    forgotRequests.value = forgotRequests.value.filter(x => x.id !== id)
  } catch (e: any) {
    errMsg.value = e.message
  }
}
function adoptForgotRequest(req: ForgotRequestRow) {
  // Pre-fill the admin-reset form's mailbox field; admin then types the
  // new password manually before clicking reset.
  resetMailbox.value = req.mailbox
  resetPassword.value = ''
  setTimeout(() => {
    const el = document.getElementById('admin-reset-form')
    el?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }, 80)
}

// Approve & send recovery link: closes the loop the original forgot-flow
// left open. Used to be that regular users only got a "we'll contact
// you" ack — this asks the backend to mint a single-use mailbox-recover
// URL and email it to the user. Same URL shape as role-mailbox auto-recovery.
const forgotApproveBusy = ref<Record<string, boolean>>({})
async function approveForgotRequest(req: ForgotRequestRow) {
  if (forgotApproveBusy.value[req.id]) return
  if (!confirm(t('admin.forgot.confirm_approve', { mailbox: req.mailbox }))) return
  forgotApproveBusy.value = { ...forgotApproveBusy.value, [req.id]: true }
  try {
    await api(`/api/v1/mail/forgot/requests/${req.id}/approve`, { method: 'POST', body: '{}' })
    forgotRequests.value = forgotRequests.value.filter(x => x.id !== req.id)
  } catch (e: any) {
    errMsg.value = e.message
  } finally {
    forgotApproveBusy.value = { ...forgotApproveBusy.value, [req.id]: false }
  }
}

// ─── passkey state ────────────────────────────────────────────────────────
interface PasskeyRecord {
  id: string
  name: string
  created_at: string
  last_used_at?: string
  transports?: string[]
}
const passkeyList = ref<PasskeyRecord[]>([])
const passkeyBusy = ref(false)
const passkeyName = ref('')
const passkeyLoginBusy = ref(false)
const passkeySupported = typeof window !== 'undefined' && !!(window as any).PublicKeyCredential

// Single-tenant deployment: every mailbox here lives under @example.com,
// so we lock the domain in the UI and let the user only type the
// local-part. `loginMailbox` (kept name for diff stability) now stores
// just the local-part; `fullMailbox()` reconstructs the address at
// submit/API call sites. Same convention for forgotMailbox.
const mailDomain = 'example.com'
function fullMailbox(local: string): string {
  const v = local.trim().toLowerCase()
  // Defensive: if user pasted a full email by mistake, take the part
  // before @ rather than mangling the suffix.
  const at = v.indexOf('@')
  return (at >= 0 ? v.slice(0, at) : v) + '@' + mailDomain
}

const loginMailbox = ref('')
const loginPassword = ref('')
const loginRemember = ref(true)
const loginPasswordVisible = ref(false)
const capsLockOn = ref(false)

// ─── Cloudflare Turnstile state ─────────────────────────────────────────────
// Public sitekey for mail.example.com (widget "ncn-webmail-login").
// Server-side secret lives at /etc/ncn-mail/turnstile.secret on pop-03;
// the backend verifies the token before bcrypt-comparing the password,
// so failed challenges can't probe whether a mailbox exists.
const TURNSTILE_SITEKEY = '0x4AAAAAADWJENeCxfJVXRxJ'
const turnstileToken = ref('')
const turnstileErr   = ref('')
let turnstileWidgetId: string | null = null

function turnstileOnSuccess(token: string) {
  turnstileToken.value = token
  turnstileErr.value = ''
}
function turnstileOnError() {
  turnstileToken.value = ''
  turnstileErr.value = t('login.turnstile.err')
}
function turnstileOnExpire() {
  turnstileToken.value = ''
}
;(window as any).turnstileOnSuccess = turnstileOnSuccess
;(window as any).turnstileOnError   = turnstileOnError
;(window as any).turnstileOnExpire  = turnstileOnExpire

function renderTurnstile() {
  const w = (window as any).turnstile
  if (!w) return
  const el = document.getElementById('cf-turnstile-mount')
  if (!el || turnstileWidgetId) return
  // Pass FUNCTION references directly, not string names. The string-name
  // form (data-callback) works for implicit class="cf-turnstile" render,
  // but the explicit `turnstile.render(...)` JS API resolves callbacks
  // via reference. With strings, Turnstile silently no-ops the callback
  // on some widget versions — widget shows the green check but our
  // `turnstileToken` ref never updates, so the submit button stays
  // disabled forever. Function refs are unambiguous.
  turnstileWidgetId = w.render('#cf-turnstile-mount', {
    sitekey:           TURNSTILE_SITEKEY,
    theme:             'dark',
    size:              'normal',
    callback:          turnstileOnSuccess,
    'error-callback':  turnstileOnError,
    'expired-callback':turnstileOnExpire,
  })
}
function resetTurnstile() {
  const w = (window as any).turnstile
  if (w && turnstileWidgetId) {
    w.reset(turnstileWidgetId)
    turnstileToken.value = ''
  }
}

// Re-render Turnstile every time we (re-)ENTER the login view. The widget is
// only mounted while phase==='login'. The onMounted injector renders it on
// first paint, but if the app then flips to mailbox (valid session) and later
// bounces BACK to login — logout, session expiry, or a 428 "stash lost" after
// a password rotation — the #cf-turnstile-mount node is brand new and the old
// `turnstileWidgetId` points at a destroyed widget, so renderTurnstile would
// early-return and the box stays empty ("人机验证又没弹出来"). Null the stale id
// and render into the fresh node. renderTurnstile guards on the CF script
// being loaded + the element existing, so this is safe to call eagerly.
watch(phase, (p) => {
  if (p === 'login') {
    turnstileWidgetId = null
    nextTick(renderTurnstile)
  }
})

function detectCapsLock(ev: KeyboardEvent) {
  // Browsers expose CapsLock state on any keyboard event. We hook keydown
  // AND keyup so toggling caps mid-input updates the indicator.
  if (typeof ev.getModifierState === 'function') {
    capsLockOn.value = ev.getModifierState('CapsLock')
  }
}

const folders = ref<Folder[]>([])
const activeFolder = ref('INBOX')
const messages = ref<MessageHeader[]>([])
const total = ref(0)
const offset = ref(0)
const limit = ref(50)
const activeMessage = ref<MessageBody | null>(null)
const refreshing = ref(false)

// IMAP SEARCH state
const searchQuery = ref('')
const searchActive = ref(false)   // when true, messages[] is search results
const searchBusy = ref(false)
let searchDebounce: ReturnType<typeof setTimeout> | null = null

const composeOpen = ref(false)
const composeTo = ref('')
const composeCc = ref('')
const composeBcc = ref('')
const composeSubject = ref('')
const composeBody = ref('')
const composeSending = ref(false)
const composeAttachments = ref<File[]>([])
const composeFileInput = ref<HTMLInputElement | null>(null)
const composeDraftUID = ref<number | null>(null)
const composeDraftSavedAt = ref<Date | null>(null)
const composeDraftBusy = ref(false)
let composeDraftTimer: ReturnType<typeof setTimeout> | null = null

// Rich text (HTML) compose state
const composeFormat = ref<'plain' | 'html'>('plain')
const composeEditor = ref<HTMLDivElement | null>(null)
function execFormat(cmd: string, arg?: string) {
  // contenteditable's old-but-still-functional formatting commands
  document.execCommand(cmd, false, arg)
  if (composeEditor.value) {
    composeBody.value = composeEditor.value.innerHTML
    scheduleDraftSave()
    composeEditor.value.focus()
  }
}
function execLink() {
  const url = prompt(t('compose.html.link_prompt'), 'https://')
  if (url && /^https?:\/\//i.test(url)) execFormat('createLink', url)
}
function onEditorInput(ev: Event) {
  composeBody.value = (ev.target as HTMLDivElement).innerHTML
  scheduleDraftSave()
}
function toggleFormat() {
  if (composeFormat.value === 'plain') {
    // Plain <Icon name="arrow-right" class="inline mx-1" /> HTML: escape current body + wrap each line in <br>
    composeBody.value = composeBody.value
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/\n/g, '<br>')
    composeFormat.value = 'html'
  } else {
    // HTML <Icon name="arrow-right" class="inline mx-1" /> Plain: strip tags
    const tmp = document.createElement('div')
    tmp.innerHTML = composeBody.value
    composeBody.value = (tmp.textContent || '').trim()
    composeFormat.value = 'plain'
  }
}

const totalUnseen = computed(() =>
  folders.value.reduce((acc, f) => acc + (f.unseen || 0), 0),
)

// Canonical folder order: INBOX first, then RFC 6154 special-use boxes in
// the order most clients show them (Sent / Drafts / Junk / Trash), then
// "Sent Messages" alias (we only deduplicate display, not the data), then
// everything else alphabetically. Names are case-insensitive matched
// against this list — gmail-style "Sent Items" or "Deleted Items"
// dovecot-aliased boxes still sort correctly.
const FOLDER_ORDER: Record<string, number> = {
  inbox:           0,
  sent:            1,
  'sent messages': 1.1,
  drafts:          2,
  junk:            3,
  spam:            3.1,
  trash:           4,
  'deleted items': 4.1,
}
const FOLDER_ICONS: Record<string, string> = {
  inbox:  'arrow-down',
  sent:   'send',
  drafts: 'save',
  junk:   'alert',
  spam:   'alert',
  trash:  'trash',
}
function folderRank(name: string): number {
  return FOLDER_ORDER[name.toLowerCase()] ?? 100
}
function folderIcon(name: string): string {
  return FOLDER_ICONS[name.toLowerCase()] ?? 'dot'
}
function folderLabel(name: string): string {
  const key = 'folder.' + name.toLowerCase().replace(/\s+/g, '_')
  // vue-i18n returns the key itself if missing — guard with te()-style
  // fallback.
  const lookup = t(key)
  return lookup === key ? name : lookup
}
const sortedFolders = computed(() => {
  return [...folders.value].sort((a, b) => {
    const ra = folderRank(a.name), rb = folderRank(b.name)
    if (ra !== rb) return ra - rb
    return a.name.localeCompare(b.name)
  })
})

const locales = [
  { code: 'en',    label: 'EN' },
  { code: 'zh-CN', label: '简' },
  { code: 'zh-TW', label: '繁' },
]

function setLocale(code: string) {
  locale.value = code as typeof locale.value
  localStorage.setItem('webmail.locale', code)
}

async function api<T = unknown>(path: string, opts: RequestInit = {}): Promise<T> {
  const res = await fetch(path, {
    ...opts,
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', ...(opts.headers || {}) },
  })
  const env = await res.json()
  if (!env.ok) throw new Error(env.error || `HTTP ${res.status}`)
  return env.data as T
}

onMounted(async () => {
  // Health probe kicks off immediately + every 30s.
  probeHealth()
  setInterval(probeHealth, 30_000)

  // Inject Cloudflare Turnstile script once. ORDERING MATTERS — see
  // admin/Login.vue for the full write-up. tl;dr: assign
  // window.ncnTurnstileReady BEFORE appending the <script>, otherwise
  // a warm HTTP cache on PC Chrome can fire the onload callback before
  // the assignment commits → widget never renders.
  ;(window as any).ncnTurnstileReady = renderTurnstile
  if (document.querySelector('script[data-cf-turnstile]')) {
    if ((window as any).turnstile) renderTurnstile()
  } else {
    const s = document.createElement('script')
    s.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?onload=ncnTurnstileReady'
    s.async = true
    s.dataset.cfTurnstile = '1'
    document.head.appendChild(s)
  }

  // Remember last successful mailbox (NOT password). Pre-fill login form.
  // Strip any legacy stored @example.com suffix — input only takes local-part now.
  const lastMbx = localStorage.getItem('webmail.last-mailbox')
  if (lastMbx) {
    const at = lastMbx.indexOf('@')
    loginMailbox.value = at >= 0 ? lastMbx.slice(0, at) : lastMbx
  }

  // /invite/<token> short-circuits everything else. Two token shapes:
  //   - random hex (admin-issued, stored in invites.json)
  //   - "op-<base64url>.<base64url>" (operator self-invite, HMAC-signed by ncn-api)
  const m = window.location.pathname.match(/^\/invite\/(op-[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+|[0-9a-f]+)/i)
  if (m) {
    registerToken.value = m[1]
    await loadRegisterPreview()
    return
  }

  // /admin-recover/<token> — break-glass mailbox password reset.
  const r = window.location.pathname.match(/^\/admin-recover\/(mb-[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+)/i)
  if (r) {
    recoverToken.value = r[1]
    await loadRecoverPreview()
    return
  }

  // /verify-forward/<token> — recipient consents to receive forwarded mail
  // from a Acme Cloud mailbox. No auth: trust = HMAC signature.
  const vf = window.location.pathname.match(/^\/verify-forward\/(vfwd-[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+)/i)
  if (vf) {
    verifyForwardToken.value = vf[1]
    await processForwardVerify()
    return
  }
  try {
    me.value = await api('/api/v1/mail/me')
    phase.value = 'mailbox'
    await refreshAll()
    // Probe admin permission silently.
    try {
      await api('/api/v1/mail/invites')
      isAdmin.value = true
    } catch {
      isAdmin.value = false
    }
    startSSE()
  } catch {
    phase.value = 'login'
  }
})

async function submitForgotRequest() {
  if (!forgotMailbox.value.trim()) return
  forgotBusy.value = true
  errMsg.value = ''
  // The forgot endpoint expects the full address — we hold just the
  // local-part in the input, so reconstruct here.
  const _forgotFull = fullMailbox(forgotMailbox.value)
  try {
    await api('/api/v1/mail/forgot/request', {
      method: 'POST',
      body: JSON.stringify({ mailbox: _forgotFull }),
    })
    forgotDone.value = true
  } catch (e: any) {
    errMsg.value = e.message
  } finally {
    forgotBusy.value = false
  }
}
function openForgot() {
  forgotOpen.value = true
  forgotDone.value = false
  forgotMailbox.value = loginMailbox.value
}

async function loadRegisterPreview() {
  registerLoading.value = true
  errMsg.value = ''
  try {
    registerPreview.value = await api<InvitePreview>(
      `/api/v1/mail/invite/preview?token=${encodeURIComponent(registerToken.value)}`,
    )
    // operator-bridge tokens come with a suggested alias (= operator username)
    if (registerPreview.value?.suggested && !registerLocalPart.value) {
      registerLocalPart.value = registerPreview.value.suggested
    }
  } catch (e: any) {
    errMsg.value = e.message
  } finally {
    registerLoading.value = false
  }
}

async function submitRegistration() {
  errMsg.value = ''
  if (registerPassword.value !== registerPasswordConfirm.value) {
    errMsg.value = t('register.err.password_mismatch')
    return
  }
  if (registerPassword.value.length < 8) {
    errMsg.value = t('register.err.password_short')
    return
  }
  registerSubmitting.value = true
  try {
    const data = await api<{ mailbox: string }>('/api/v1/mail/invite/complete', {
      method: 'POST',
      body: JSON.stringify({
        token: registerToken.value,
        local_part: registerLocalPart.value.trim().toLowerCase(),
        password: registerPassword.value,
      }),
    })
    registerDone.value = data
  } catch (e: any) {
    errMsg.value = e.message
  } finally {
    registerSubmitting.value = false
  }
}

async function processForwardVerify() {
  verifyForwardLoading.value = true
  verifyForwardErr.value = ''
  verifyForwardOK.value = false
  try {
    const r = await api<{ mailbox: string; address: string; already?: boolean }>(
      `/api/v1/mail/forward/verify?token=${encodeURIComponent(verifyForwardToken.value)}`,
    )
    verifyForwardAddress.value = r.address || ''
    verifyForwardOK.value = true
    // After verification succeeds the sieve is already rewritten. Bounce
    // the user back to the webmail root after a short countdown so they
    // land on their normal mailbox view (and if they're the owner, they
    // can open Settings → Forwarding and see the address as verified).
    verifyForwardRedirectIn.value = 3
    const tick = setInterval(() => {
      verifyForwardRedirectIn.value -= 1
      if (verifyForwardRedirectIn.value <= 0) {
        clearInterval(tick)
        window.location.replace('/')
      }
    }, 1000)
  } catch (e: any) {
    verifyForwardErr.value = e.message
  } finally {
    verifyForwardLoading.value = false
  }
}

async function loadRecoverPreview() {
  recoverLoading.value = true
  recoverErr.value = ''
  try {
    const data = await api<{ mailbox: string }>(
      `/api/v1/mail/admin/bootstrap-recover/preview?token=${encodeURIComponent(recoverToken.value)}`,
    )
    recoverMailbox.value = data.mailbox
  } catch (e: any) {
    recoverErr.value = e.message
  } finally {
    recoverLoading.value = false
  }
}

async function submitRecover() {
  recoverErr.value = ''
  if (recoverPassword.value.length < 8) {
    recoverErr.value = t('recover.err.too_short')
    return
  }
  if (recoverPassword.value !== recoverPasswordConfirm.value) {
    recoverErr.value = t('recover.err.mismatch')
    return
  }
  recoverSubmitting.value = true
  try {
    await api<{ mailbox: string }>('/api/v1/mail/admin/bootstrap-recover', {
      method: 'POST',
      body: JSON.stringify({
        token: recoverToken.value,
        new_password: recoverPassword.value,
      }),
    })
    recoverDone.value = true
  } catch (e: any) {
    recoverErr.value = e.message
  } finally {
    recoverSubmitting.value = false
  }
}

async function openInvitesPanel() {
  invitesPanelOpen.value = true
  await Promise.all([loadInvites(), loadForgotRequests()])
}

async function loadInvites() {
  invitesBusy.value = true
  try {
    invitesList.value = (await api<IssuedInvite[]>('/api/v1/mail/invites')) || []
  } catch (e: any) {
    errMsg.value = e.message
  } finally {
    invitesBusy.value = false
  }
}

async function createInvite() {
  invitesBusy.value = true
  try {
    lastIssued.value = await api('/api/v1/mail/invites', {
      method: 'POST',
      body: JSON.stringify({ note: inviteNote.value.trim() }),
    })
    inviteNote.value = ''
    await loadInvites()
  } catch (e: any) {
    errMsg.value = e.message
  } finally {
    invitesBusy.value = false
  }
}

async function revokeInvite(prefix: string) {
  if (!confirm(t('admin.confirm.revoke'))) return
  try {
    await api(`/api/v1/mail/invites/${prefix}`, { method: 'DELETE' })
    await loadInvites()
  } catch (e: any) {
    errMsg.value = e.message
  }
}

// ─── admin password reset ────────────────────────────────────────────────
async function adminResetPassword() {
  errMsg.value = ''
  resetDone.value = null
  if (!resetMailbox.value.trim() || resetPassword.value.length < 8) {
    errMsg.value = t('admin.reset.bad_input')
    return
  }
  resetBusy.value = true
  try {
    const r = await api<{ mailbox: string }>('/api/v1/mail/admin/reset-password', {
      method: 'POST',
      body: JSON.stringify({
        mailbox: resetMailbox.value.trim().toLowerCase(),
        new_password: resetPassword.value,
      }),
    })
    resetDone.value = r
    resetMailbox.value = ''
    resetPassword.value = ''
  } catch (e: any) {
    errMsg.value = e.message
  } finally {
    resetBusy.value = false
  }
}

// ─── settings panel (per-user) ───────────────────────────────────────────
async function openSettings() {
  settingsOpen.value = true
  forwardSaved.value = false
  await Promise.all([loadForward(), loadPasskeys()])
}

async function loadForward() {
  try {
    const r = await api<{ addresses: string[]; pending: string[] }>('/api/v1/mail/forward')
    forwardAddresses.value = r.addresses || []
    forwardPending.value = r.pending || []
  } catch (e: any) {
    errMsg.value = e.message
  }
}

// commitForward sends the current union (verified ∪ pending) to the
// backend, which is authoritative: it classifies each address into
// keep-verified / keep-pending / new-pending and triggers a verification
// email for any new pending entries. We refresh local state from the
// response — this is the single write path for forward changes.
async function commitForward() {
  forwardBusy.value = true
  forwardSaved.value = false
  errMsg.value = ''
  try {
    const submitted = [...forwardAddresses.value, ...forwardPending.value]
    const r = await api<{
      addresses: string[]
      pending: string[]
      verifications_sent: string[]
    }>('/api/v1/mail/forward', {
      method: 'PUT',
      body: JSON.stringify({ addresses: submitted }),
    })
    forwardAddresses.value = r.addresses || []
    forwardPending.value = r.pending || []
    // Accumulate the "verification sent" toast across multiple adds —
    // user might add two addresses in quick succession and both
    // notifications matter.
    for (const a of (r.verifications_sent || [])) {
      if (!forwardVerifyJustSent.value.includes(a)) {
        forwardVerifyJustSent.value = [...forwardVerifyJustSent.value, a]
      }
    }
    forwardSaved.value = true
    setTimeout(() => { forwardSaved.value = false }, 3000)
  } catch (e: any) {
    errMsg.value = e.message
    // Surface the error but also re-pull state so we don't drift if the
    // server processed partially.
    await loadForward()
  } finally {
    forwardBusy.value = false
  }
}

// Adding a new address auto-commits: backend gets the new union, decides
// it's pending, and sends a verification email immediately. The local UI
// shows it in the pending list right away.
async function addForwardAddr() {
  const a = forwardDraft.value.trim().toLowerCase()
  if (!a) return
  // Dedupe against BOTH verified and pending.
  if (forwardAddresses.value.includes(a) || forwardPending.value.includes(a)) {
    forwardDraft.value = ''
    return
  }
  const total = forwardAddresses.value.length + forwardPending.value.length
  if (total >= 8) {
    errMsg.value = t('settings.forward.too_many')
    return
  }
  // Optimistic: show in pending, clear the input. commitForward will
  // round-trip and the server response is authoritative.
  forwardPending.value = [...forwardPending.value, a]
  forwardDraft.value = ''
  await commitForward()
}

// Removing also auto-commits — one click, gone.
async function removeForwardAddr(addr: string) {
  forwardAddresses.value = forwardAddresses.value.filter(a => a !== addr)
  forwardPending.value = forwardPending.value.filter(a => a !== addr)
  forwardVerifyJustSent.value = forwardVerifyJustSent.value.filter(a => a !== addr)
  await commitForward()
}


// ─── WebAuthn helpers ─────────────────────────────────────────────────────
function b64urlToBuf(s: string): ArrayBuffer {
  s = s.replace(/-/g, '+').replace(/_/g, '/')
  while (s.length % 4) s += '='
  const bin = atob(s)
  const buf = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i)
  return buf.buffer
}
function bufToB64url(b: ArrayBuffer | Uint8Array): string {
  const bytes = b instanceof Uint8Array ? b : new Uint8Array(b)
  let s = ''
  for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i])
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

// The server returns CreationOptions / RequestOptions whose challenge +
// allowCredentials[].id + user.id are base64url strings. The WebAuthn API
// wants real ArrayBuffers. These deep-walkers swap them in place.
function decodeCreationOpts(o: any): CredentialCreationOptions {
  const pk = o.publicKey
  pk.challenge = b64urlToBuf(pk.challenge)
  pk.user.id = b64urlToBuf(pk.user.id)
  if (pk.excludeCredentials)
    pk.excludeCredentials = pk.excludeCredentials.map((c: any) => ({ ...c, id: b64urlToBuf(c.id) }))
  return o
}
function decodeRequestOpts(o: any): CredentialRequestOptions {
  const pk = o.publicKey
  pk.challenge = b64urlToBuf(pk.challenge)
  if (pk.allowCredentials)
    pk.allowCredentials = pk.allowCredentials.map((c: any) => ({ ...c, id: b64urlToBuf(c.id) }))
  return o
}
function encodeAttestation(cred: PublicKeyCredential): any {
  const r = cred.response as AuthenticatorAttestationResponse
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    response: {
      clientDataJSON:    bufToB64url(r.clientDataJSON),
      attestationObject: bufToB64url(r.attestationObject),
      transports: r.getTransports?.() || [],
    },
  }
}
function encodeAssertion(cred: PublicKeyCredential): any {
  const r = cred.response as AuthenticatorAssertionResponse
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    response: {
      clientDataJSON:    bufToB64url(r.clientDataJSON),
      authenticatorData: bufToB64url(r.authenticatorData),
      signature:         bufToB64url(r.signature),
      userHandle: r.userHandle ? bufToB64url(r.userHandle) : null,
    },
  }
}

// ─── passkey operations ──────────────────────────────────────────────────
async function loadPasskeys() {
  try {
    passkeyList.value = (await api<PasskeyRecord[]>('/api/v1/mail/passkey')) || []
  } catch (e: any) {
    errMsg.value = e.message
  }
}

async function registerPasskey() {
  if (!passkeySupported) {
    errMsg.value = t('passkey.err.unsupported')
    return
  }
  if (!passkeyName.value.trim()) {
    errMsg.value = t('passkey.err.name_required')
    return
  }
  passkeyBusy.value = true
  errMsg.value = ''
  try {
    const begin = await api<{ challenge_id: string; options: any }>(
      '/api/v1/mail/passkey/register/begin',
      { method: 'POST', body: '{}' },
    )
    const opts = decodeCreationOpts(begin.options)
    const cred = (await navigator.credentials.create(opts)) as PublicKeyCredential | null
    if (!cred) throw new Error(t('passkey.err.aborted'))
    await api('/api/v1/mail/passkey/register/finish', {
      method: 'POST',
      body: JSON.stringify({
        challenge_id: begin.challenge_id,
        credential:   encodeAttestation(cred),
        name:         passkeyName.value.trim(),
      }),
    })
    passkeyName.value = ''
    await loadPasskeys()
  } catch (e: any) {
    errMsg.value = e.message
  } finally {
    passkeyBusy.value = false
  }
}

async function revokePasskey(id: string) {
  if (!confirm(t('passkey.confirm.revoke'))) return
  try {
    await api(`/api/v1/mail/passkey/${encodeURIComponent(id)}`, { method: 'DELETE' })
    await loadPasskeys()
  } catch (e: any) {
    errMsg.value = e.message
  }
}

async function loginWithPasskey() {
  if (!passkeySupported) {
    errMsg.value = t('passkey.err.unsupported')
    return
  }
  if (!loginMailbox.value.trim()) {
    errMsg.value = t('passkey.err.mailbox_first')
    return
  }
  passkeyLoginBusy.value = true
  errMsg.value = ''
  try {
    const begin = await api<{ challenge_id: string; options: any }>(
      '/api/v1/mail/passkey/login/begin',
      { method: 'POST', body: JSON.stringify({ mailbox: fullMailbox(loginMailbox.value) }) },
    )
    const opts = decodeRequestOpts(begin.options)
    const cred = (await navigator.credentials.get(opts)) as PublicKeyCredential | null
    if (!cred) throw new Error(t('passkey.err.aborted'))
    const data = await api<{ mailbox: string }>('/api/v1/mail/passkey/login/finish', {
      method: 'POST',
      body: JSON.stringify({
        challenge_id: begin.challenge_id,
        credential:   encodeAssertion(cred),
      }),
    })
    me.value = { mailbox: data.mailbox, stashed: true, exp: 0 }
    localStorage.setItem('webmail.last-mailbox', data.mailbox)
    phase.value = 'mailbox'
    await refreshAll()
    try { await api('/api/v1/mail/invites'); isAdmin.value = true } catch { isAdmin.value = false }
    startSSE()
  } catch (e: any) {
    errMsg.value = e.message
  } finally {
    passkeyLoginBusy.value = false
  }
}

async function copyToClipboard(s: string) {
  try {
    await navigator.clipboard.writeText(s)
  } catch {
    // fallback for older / non-https — but mail.example.com is https, so OK
  }
}

async function submitLogin() {
  errMsg.value = ''
  if (!loginMailbox.value || !loginPassword.value) {
    errMsg.value = t('err.credentials_required')
    return
  }
  if (!turnstileToken.value) {
    turnstileErr.value = t('login.turnstile.needed')
    return
  }
  loading.value = true
  try {
    const data = await api<{ mailbox: string; remember: boolean }>('/api/v1/mail/auth', {
      method: 'POST',
      body: JSON.stringify({
        mailbox: fullMailbox(loginMailbox.value),
        password: loginPassword.value,
        remember: loginRemember.value,
        turnstile_token: turnstileToken.value,
      }),
    })
    me.value = { mailbox: data.mailbox, stashed: data.remember, exp: 0 }
    loginPassword.value = ''
    // Store just the local-part — login UI re-attaches @example.com.
    const at = data.mailbox.indexOf('@')
    localStorage.setItem('webmail.last-mailbox', at >= 0 ? data.mailbox.slice(0, at) : data.mailbox)
    phase.value = 'mailbox'
    await refreshAll()
    try { await api('/api/v1/mail/invites'); isAdmin.value = true } catch { isAdmin.value = false }
    startSSE()
  } catch (e: any) {
    errMsg.value = e.message || t('err.login_failed')
    // CF tokens are one-shot; failed submit consumed it. Hand the user a
    // fresh challenge so retry isn't blocked by "stale token".
    resetTurnstile()
  } finally {
    loading.value = false
  }
}

// SSO → admin console. Mint a 60s HMAC ticket via ncn-mail and redirect.
// admin verifies the ticket, looks up the operator record matching this
// mailbox's local-part, and issues an admin session. If no operator
// record exists, admin returns 404 and we surface a clear error.
const ssoBusy = ref(false)
async function openAdmin() {
  if (ssoBusy.value) return
  ssoBusy.value = true
  try {
    const r = await api<{ url: string; operator: string; expires_at: string }>('/api/v1/mail/sso/admin-ticket', { method: 'POST' })
    if (!r?.url) throw new Error('sso ticket failed')
    window.location.href = r.url
  } catch (e: any) {
    ssoBusy.value = false
    alert(e?.message || 'sso failed')
  }
}

async function logout() {
  stopSSE()
  try {
    await api('/api/v1/mail/logout', { method: 'POST' })
  } catch {}
  me.value = null
  folders.value = []
  messages.value = []
  activeMessage.value = null
  phase.value = 'login'
}

async function refreshAll() {
  refreshing.value = true
  try {
    folders.value = (await api<Folder[]>('/api/v1/mail/folders')) || []
    await loadMessages()
  } catch (e: any) {
    if (e.message?.includes('not authenticated')) {
      phase.value = 'login'
    } else {
      errMsg.value = e.message
    }
  } finally {
    refreshing.value = false
  }
}

async function loadMessages() {
  if (!activeFolder.value) return
  try {
    const data = await api<{ total: number; messages: MessageHeader[] }>(
      `/api/v1/mail/folders/${encodeURIComponent(activeFolder.value)}/messages?limit=${limit.value}&offset=${offset.value}`,
    )
    messages.value = data.messages || []
    total.value = data.total
  } catch (e: any) {
    errMsg.value = e.message
  }
}

async function selectFolder(name: string) {
  activeFolder.value = name
  offset.value = 0
  activeMessage.value = null
  searchQuery.value = ''
  searchActive.value = false
  mobileView.value = 'list'
  await loadMessages()
}

function onSearchInput() {
  if (searchDebounce) clearTimeout(searchDebounce)
  const q = searchQuery.value.trim()
  if (q.length === 0) {
    // Empty query <Icon name="arrow-right" class="inline mx-1" /> return to normal list
    searchActive.value = false
    loadMessages()
    return
  }
  if (q.length < 2) return  // wait for more chars
  searchDebounce = setTimeout(() => doSearch(q), 300)
}

async function doSearch(q: string) {
  searchBusy.value = true
  try {
    const data = await api<{ total: number; messages: MessageHeader[] }>(
      `/api/v1/mail/folders/${encodeURIComponent(activeFolder.value)}/search?q=${encodeURIComponent(q)}&limit=200`,
    )
    messages.value = data.messages || []
    total.value = data.total
    searchActive.value = true
  } catch (e: any) {
    errMsg.value = e.message
  } finally {
    searchBusy.value = false
  }
}

function clearSearch() {
  searchQuery.value = ''
  searchActive.value = false
  loadMessages()
}

async function openMessage(m: MessageHeader) {
  errMsg.value = ''
  try {
    activeMessage.value = await api<MessageBody>(
      `/api/v1/mail/messages/${m.uid}?folder=${encodeURIComponent(activeFolder.value)}`,
    )
    const row = messages.value.find((x) => x.uid === m.uid)
    if (row) {
      const flags = row.flags || []
      if (!flags.includes('\\Seen')) row.flags = [...flags, '\\Seen']
    }
    mobileView.value = 'reader'
  } catch (e: any) {
    errMsg.value = e.message
  }
}

async function deleteMessage(m: MessageHeader | MessageBody) {
  if (!confirm(t('confirm.delete'))) return
  try {
    await api(`/api/v1/mail/messages/${m.uid}?folder=${encodeURIComponent(activeFolder.value)}`, {
      method: 'DELETE',
    })
    messages.value = messages.value.filter((x) => x.uid !== m.uid)
    if (activeMessage.value?.uid === m.uid) activeMessage.value = null
  } catch (e: any) {
    errMsg.value = e.message
  }
}

// Move-to dropdown state for the reader pane
const moveMenuOpen = ref(false)
async function moveMessage(m: MessageHeader | MessageBody, target: string) {
  moveMenuOpen.value = false
  try {
    await api(`/api/v1/mail/messages/${m.uid}/move?folder=${encodeURIComponent(activeFolder.value)}`, {
      method: 'POST',
      body: JSON.stringify({ to: target }),
    })
    messages.value = messages.value.filter((x) => x.uid !== m.uid)
    if (activeMessage.value?.uid === m.uid) activeMessage.value = null
    // Refresh folder list so unseen counts update across folders.
    folders.value = (await api<Folder[]>('/api/v1/mail/folders')) || []
  } catch (e: any) {
    errMsg.value = e.message
  }
}

// Other folders the user can move INTO (everything except current)
const moveTargets = computed(() =>
  folders.value.map(f => f.name).filter(n => n !== activeFolder.value),
)

function attachmentURL(uid: number, index: number): string {
  return `/api/v1/mail/messages/${uid}/attachments/${index}?folder=${encodeURIComponent(activeFolder.value)}`
}

async function toggleFlag(uid: number, flag: string) {
  const row = messages.value.find((x) => x.uid === uid)
  const rowFlags = row?.flags || []
  const has = rowFlags.includes(flag)
  try {
    await api(`/api/v1/mail/messages/${uid}/flag?folder=${encodeURIComponent(activeFolder.value)}`, {
      method: 'POST',
      body: JSON.stringify({ op: has ? 'remove' : 'add', flag }),
    })
    if (row) {
      row.flags = has ? rowFlags.filter((f) => f !== flag) : [...rowFlags, flag]
    }
  } catch (e: any) {
    errMsg.value = e.message
  }
}

async function saveDraft() {
  if (!composeOpen.value) return
  // skip empty drafts
  if (!composeTo.value.trim() && !composeSubject.value.trim() && !composeBody.value.trim()) {
    return
  }
  composeDraftBusy.value = true
  try {
    const data = await api<{ uid: number; saved_at: string }>('/api/v1/mail/draft', {
      method: 'POST',
      body: JSON.stringify({
        to: composeTo.value,
        cc: composeCc.value,
        bcc: composeBcc.value,
        subject: composeSubject.value,
        body: composeBody.value,
        replace_uid: composeDraftUID.value || 0,
      }),
    })
    composeDraftUID.value = data.uid
    composeDraftSavedAt.value = new Date(data.saved_at)
  } catch (e: any) {
    // silent — don't disrupt the user, but log
    console.warn('draft save:', e.message)
  } finally {
    composeDraftBusy.value = false
  }
}

function scheduleDraftSave() {
  if (composeDraftTimer) clearTimeout(composeDraftTimer)
  composeDraftTimer = setTimeout(saveDraft, 5000)
}

const draftSavedAgo = computed(() => {
  if (!composeDraftSavedAt.value) return ''
  const s = Math.floor((Date.now() - composeDraftSavedAt.value.getTime()) / 1000)
  if (s < 5) return t('compose.draft_just_now')
  if (s < 60) return t('compose.draft_ago_sec', { n: s })
  const m = Math.floor(s / 60)
  return t('compose.draft_ago_min', { n: m })
})

function openCompose(reply?: MessageBody) {
  composeOpen.value = true
  composeAttachments.value = []
  composeFormat.value = 'plain'
  composeDraftUID.value = null
  composeDraftSavedAt.value = null
  if (composeDraftTimer) { clearTimeout(composeDraftTimer); composeDraftTimer = null }
  if (reply) {
    composeTo.value = (reply.reply_to?.[0] || reply.from?.[0] || '').replace(/.*<(.+)>/, '$1')
    composeSubject.value = reply.subject?.startsWith('Re: ') ? reply.subject : `Re: ${reply.subject || ''}`
    const quoted = (reply.text || '').split('\n').map((l) => `> ${l}`).join('\n')
    composeBody.value = `\n\n${t('compose.on_date_x_wrote', { date: reply.date, who: reply.from?.[0] || '' })}\n${quoted}`
  } else {
    composeTo.value = ''
    composeSubject.value = ''
    composeBody.value = ''
  }
  composeCc.value = ''
  composeBcc.value = ''
}

function pickAttachments() {
  composeFileInput.value?.click()
}
function onAttachmentsPicked(ev: Event) {
  const files = (ev.target as HTMLInputElement).files
  if (!files) return
  let total = composeAttachments.value.reduce((a, f) => a + f.size, 0)
  for (const f of Array.from(files)) {
    if (total + f.size > 25 * 1024 * 1024) {
      errMsg.value = t('compose.attach_too_large')
      break
    }
    composeAttachments.value.push(f)
    total += f.size
  }
  // Reset the input so picking the same file again re-fires change.
  ;(ev.target as HTMLInputElement).value = ''
}
function removeAttachment(i: number) {
  composeAttachments.value.splice(i, 1)
}
function fmtSize(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

async function sendCompose() {
  if (!composeTo.value.trim() || !composeBody.value.trim()) return
  composeSending.value = true
  errMsg.value = ''
  try {
    if (composeAttachments.value.length === 0) {
      await api('/api/v1/mail/send', {
        method: 'POST',
        body: JSON.stringify({
          to: composeTo.value.trim(),
          cc: composeCc.value.trim(),
          bcc: composeBcc.value.trim(),
          subject: composeSubject.value.trim(),
          body: composeBody.value,
          format: composeFormat.value,
        }),
      })
    } else {
      // multipart path — bypass api() helper because it forces Content-Type
      // to application/json.
      const fd = new FormData()
      fd.append('to', composeTo.value.trim())
      fd.append('cc', composeCc.value.trim())
      fd.append('bcc', composeBcc.value.trim())
      fd.append('subject', composeSubject.value.trim())
      fd.append('body', composeBody.value)
      fd.append('format', composeFormat.value)
      for (const f of composeAttachments.value) fd.append('attachments', f, f.name)
      const res = await fetch('/api/v1/mail/send', {
        method: 'POST',
        credentials: 'same-origin',
        body: fd,
      })
      const env = await res.json()
      if (!env.ok) throw new Error(env.error || `HTTP ${res.status}`)
    }
    // Delete the in-progress draft (best-effort; user is moving on regardless).
    if (composeDraftUID.value) {
      try { await api(`/api/v1/mail/draft/${composeDraftUID.value}`, { method: 'DELETE' }) }
      catch {}
      composeDraftUID.value = null
    }
    composeOpen.value = false
  } catch (e: any) {
    errMsg.value = e.message
  } finally {
    composeSending.value = false
  }
}

function fmtDate(s: string): string {
  if (!s) return ''
  const d = new Date(s)
  const now = new Date()
  const sameDay = d.toDateString() === now.toDateString()
  if (sameDay) return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  if (now.getTime() - d.getTime() < 7 * 86400_000)
    return d.toLocaleDateString([], { weekday: 'short' })
  return d.toLocaleDateString([], { month: 'short', day: '2-digit' })
}

function shortFrom(a?: string[]): string {
  if (!a?.length) return '—'
  const s = a[0]
  const m = s.match(/^"?([^"<]+?)"?\s*<.+>$/)
  return m ? m[1].trim() : s
}

function unseen(folder: Folder): number {
  return folder.unseen || 0
}
</script>

<template>
  <div class="h-full bg-black text-gray-300 font-mono text-sm">
    <!-- top bar -->
    <header class="border-b border-gray-800 bg-gray-950 px-3 sm:px-4 py-2 flex items-center gap-2 sm:gap-4 h-10 shrink-0">
      <div class="flex items-center gap-2 min-w-0">
        <span class="w-1.5 h-1.5 shrink-0"
              :class="{
                'bg-emerald-500 animate-pulse': serverHealth === 'ok',
                'bg-red-500':                   serverHealth === 'down',
                'bg-gray-500 animate-pulse':    serverHealth === 'unknown',
              }"
              :title="serverHealth === 'ok' ? t('app.health_ok') : serverHealth === 'down' ? t('app.health_down') : t('app.health_unknown')"></span>
        <span class="text-[10px] sm:text-xs tracking-[0.2em] uppercase text-gray-200 truncate">{{ t('app.title') }}</span>
      </div>

      <div v-if="me" class="flex items-center gap-1.5 sm:gap-3 text-[10px] tracking-widest text-gray-500 uppercase ml-auto min-w-0">
        <!-- mailbox: hide label on mobile, keep email address -->
        <span class="truncate max-w-[120px] sm:max-w-none">
          <span class="hidden sm:inline">{{ t('header.mailbox') }} </span>
          <span class="text-emerald-400 normal-case tracking-normal">{{ me.mailbox }}</span>
        </span>
        <!-- unread count: number always shown, label only on sm+ -->
        <span v-if="totalUnseen > 0" class="text-amber-400 shrink-0">
          {{ totalUnseen }}<span class="hidden sm:inline"> {{ t('header.unseen') }}</span>
        </span>
        <!-- refresh: icon-only on mobile -->
        <button @click="refreshAll" class="px-1.5 sm:px-2 py-0.5 border border-gray-800 hover:border-gray-600 hover:text-gray-200 shrink-0" :disabled="refreshing">
          <Icon :name="refreshing ? 'loader' : 'refresh'" /><span class="hidden sm:inline ml-1">{{ t('header.refresh') }}</span>
        </button>
        <!-- per-user settings (forwarding etc.) -->
        <button @click="openSettings" class="px-1.5 sm:px-2 py-0.5 border border-gray-800 hover:border-emerald-700 hover:text-emerald-300 shrink-0" :title="t('header.settings')">
          <span class="sm:hidden"><Icon name="settings" /></span>
          <span class="hidden sm:inline"><Icon name="settings" /> {{ t('header.settings') }}</span>
        </button>
        <!-- admin: invite management -->
        <button v-if="isAdmin" @click="openInvitesPanel" class="px-1.5 sm:px-2 py-0.5 border border-gray-800 hover:border-emerald-700 hover:text-emerald-300 shrink-0" :title="t('admin.invites')">
          <span class="sm:hidden"><Icon name="shield" /></span>
          <span class="hidden sm:inline">{{ t('admin.invites') }}</span>
        </button>
        <!-- SSO → admin console: visible to everyone (the admin side
             will 404 if the mailbox's local-part has no matching
             operator account). The button just kicks off the redirect. -->
        <button @click="openAdmin" :disabled="ssoBusy" class="px-1.5 sm:px-2 py-0.5 border border-gray-800 hover:border-emerald-700 hover:text-emerald-300 disabled:opacity-40 shrink-0" :title="t('header.open_admin')">
          <span class="sm:hidden"><Icon name="key" /></span>
          <span class="hidden sm:inline">{{ ssoBusy ? '…' : t('header.open_admin') }}</span>
        </button>
        <!-- sign out: icon-only on mobile -->
        <button @click="logout" class="px-1.5 sm:px-2 py-0.5 border border-gray-800 hover:border-amber-700 hover:text-amber-400 shrink-0" :title="t('header.sign_out')">
          <span class="sm:hidden"><Icon name="power" /></span>
          <span class="hidden sm:inline">{{ t('header.sign_out') }}</span>
        </button>
      </div>
      <div v-else-if="!registerToken && !recoverToken && !verifyForwardToken" class="text-[10px] tracking-widest text-gray-600 uppercase ml-auto truncate">
        {{ t('header.not_signed_in') }}
      </div>

      <div class="flex items-center gap-0.5 sm:gap-1 sm:ml-2 shrink-0">
        <button
          v-for="l in locales"
          :key="l.code"
          @click="setLocale(l.code)"
          class="px-1 sm:px-1.5 text-[10px] tracking-widest uppercase border border-transparent hover:border-gray-700"
          :class="locale === l.code ? 'text-emerald-400 border-emerald-800' : 'text-gray-600'"
        >{{ l.label }}</button>
      </div>
    </header>

    <!-- error bar -->
    <Transition name="banner">
      <div v-if="errMsg" class="border-b border-red-900 bg-red-950/30 px-4 py-1.5 text-xs text-red-400">
        <Icon name="alert" class="inline mr-1 align-[-0.125em]" /> {{ errMsg }}
        <button @click="errMsg = ''" class="ml-2 text-red-700 hover:text-red-300"><Icon name="x" /></button>
      </div>
    </Transition>

    <!-- register (/invite/<token>) -->
    <section v-if="registerToken" class="min-h-[calc(100dvh-40px)] flex items-center justify-center px-3 sm:px-4 py-6">
      <div class="w-full max-w-xl bg-gray-950 border border-gray-800 px-5 py-8 sm:px-10 sm:py-12">
        <div class="flex flex-col items-center mb-6 sm:mb-8">
          <img src="/logo.svg" alt="" class="h-14 sm:h-20 w-auto mb-4 select-none max-w-full" draggable="false" />
          <h1 class="font-display font-bold text-lg sm:text-2xl text-gray-100 tracking-tight text-center">
            {{ t('register.title') }}
          </h1>
        </div>

        <!-- registration complete -->
        <div v-if="registerDone" class="space-y-4">
          <div class="border border-emerald-800 bg-emerald-950/30 p-4 text-emerald-300 text-sm">
            ✓ {{ t('register.done.title') }}
            <div class="mt-2 text-xs text-emerald-400">
              <span class="text-emerald-300">{{ registerDone.mailbox }}</span>
            </div>
          </div>
          <p class="text-[11px] sm:text-xs text-gray-500 leading-relaxed">
            {{ t('register.done.next') }}
          </p>
          <a href="/" class="block w-full text-center py-2.5 border border-emerald-800 bg-emerald-950/30 text-emerald-300 hover:bg-emerald-900/40 tracking-widest text-xs uppercase">
            {{ t('register.done.go_login') }}
          </a>
        </div>

        <!-- loading preview -->
        <div v-else-if="registerLoading" class="text-center text-xs text-gray-600 py-6">
          {{ t('register.loading') }}
        </div>

        <!-- preview failed -->
        <div v-else-if="!registerPreview" class="text-center text-xs text-red-400 py-6">
          {{ errMsg || t('register.invalid') }}
        </div>

        <!-- registration form -->
        <form v-else @submit.prevent="submitRegistration" class="space-y-4 sm:space-y-5">
          <div class="border bg-black p-3 text-[11px] text-gray-500 space-y-1"
               :class="registerPreview.operator ? 'border-emerald-800/60' : 'border-gray-800'">
            <div v-if="registerPreview.operator" class="text-emerald-400 text-[10px] tracking-widest uppercase pb-1 mb-1 border-b border-gray-800">
              <Icon name="shield" class="inline mr-1" /> operator self-register · {{ registerPreview.operator_op }} ({{ registerPreview.operator_rol }})
            </div>
            <div v-else><span class="text-gray-600 uppercase tracking-widest text-[10px]">{{ t('register.invited_by') }}</span> <span class="text-emerald-400 normal-case">{{ registerPreview.created_by }}</span></div>
            <div><span class="text-gray-600 uppercase tracking-widest text-[10px]">{{ t('register.expires') }}</span> <span class="text-gray-300">{{ new Date(registerPreview.expires_at).toLocaleString() }}</span></div>
            <div v-if="registerPreview.note"><span class="text-gray-600 uppercase tracking-widest text-[10px]">{{ t('register.note') }}</span> <span class="text-gray-300">{{ registerPreview.note }}</span></div>
          </div>

          <div>
            <label class="block text-[10px] tracking-widest uppercase text-gray-500 mb-1.5">{{ t('register.alias') }}</label>
            <div class="flex items-center bg-black border border-gray-800 focus-within:border-emerald-700">
              <input v-model="registerLocalPart" type="text"
                     class="flex-1 bg-transparent text-gray-200 px-3 sm:px-4 py-2.5 focus:outline-none text-sm"
                     placeholder="alice" required pattern="[a-z0-9][a-z0-9.\-]{0,30}[a-z0-9]" />
              <span class="px-3 text-gray-600 text-sm border-l border-gray-800">@{{ registerPreview.domain }}</span>
            </div>
            <p class="text-[10px] text-gray-700 mt-1">{{ t('register.alias_hint') }}</p>
          </div>

          <div>
            <label class="block text-[10px] tracking-widest uppercase text-gray-500 mb-1.5">{{ t('register.password') }}</label>
            <input v-model="registerPassword" type="password" autocomplete="new-password"
                   class="w-full bg-black border border-gray-800 text-gray-200 px-3 sm:px-4 py-2.5 focus:border-emerald-700 focus:outline-none text-sm"
                   minlength="8" required />
          </div>
          <div>
            <label class="block text-[10px] tracking-widest uppercase text-gray-500 mb-1.5">{{ t('register.password_confirm') }}</label>
            <input v-model="registerPasswordConfirm" type="password" autocomplete="new-password"
                   class="w-full bg-black border border-gray-800 text-gray-200 px-3 sm:px-4 py-2.5 focus:border-emerald-700 focus:outline-none text-sm"
                   minlength="8" required />
          </div>

          <button type="submit" :disabled="registerSubmitting"
                  class="w-full py-2.5 border border-emerald-800 bg-emerald-950/30 text-emerald-300 hover:bg-emerald-900/40 disabled:opacity-50 tracking-widest text-xs uppercase">
            <Icon v-if="registerSubmitting" name="loader" /><Icon v-else name="send" class="inline mr-1" /><span>{{ registerSubmitting ? t('register.submitting') : t('register.submit') }}</span>
          </button>
        </form>
      </div>
    </section>

    <!-- /admin-recover/<token> — break-glass mailbox password reset -->
    <section v-else-if="recoverToken" class="min-h-[calc(100dvh-40px)] flex items-center justify-center px-3 sm:px-4 py-6">
      <div class="w-full max-w-xl bg-gray-950 border border-gray-800 px-5 py-8 sm:px-10 sm:py-12">
        <div class="flex flex-col items-center mb-6 sm:mb-8">
          <img src="/logo.svg" alt="" class="h-14 sm:h-20 w-auto mb-4 select-none max-w-full" draggable="false" />
          <h1 class="font-display font-bold text-lg sm:text-2xl text-gray-100 tracking-tight text-center">
            {{ t('recover.title') }}
          </h1>
        </div>

        <div v-if="recoverLoading" class="text-xs text-gray-400 text-center">
          <Icon name="loader" class="inline mr-1" /> {{ t('recover.verifying') }}
        </div>

        <div v-else-if="recoverDone" class="space-y-3 text-center">
          <div class="text-emerald-400 text-xs">
            <Icon name="check" class="inline mr-1" /> {{ t('recover.done') }}
          </div>
          <p class="text-[11px] text-gray-500 leading-relaxed">{{ t('recover.done_hint') }}</p>
          <a href="/" class="block text-center px-3 py-2 border border-emerald-700 text-emerald-300 hover:bg-emerald-900/30 text-[10px] tracking-widest uppercase">
            {{ t('recover.back_login') }}
          </a>
        </div>

        <div v-else-if="!recoverMailbox" class="space-y-3">
          <div class="text-red-400 text-xs">
            <Icon name="alert" class="inline mr-1" /> {{ t('recover.err.title') }}
          </div>
          <p class="text-[11px] text-gray-500 break-all">⨯ {{ recoverErr || t('recover.err.invalid') }}</p>
          <a href="/" class="block text-center px-3 py-2 border border-gray-700 hover:border-gray-500 text-[10px] tracking-widest uppercase">
            {{ t('recover.back_login') }}
          </a>
        </div>

        <form v-else @submit.prevent="submitRecover" class="space-y-4">
          <p class="text-[11px] text-gray-500 leading-relaxed text-center">
            {{ t('recover.intro') }}
            <span class="text-emerald-300">{{ recoverMailbox }}</span>
          </p>

          <div>
            <label class="block text-[10px] tracking-widest uppercase text-gray-500 mb-1.5">{{ t('recover.new_password') }}</label>
            <input v-model="recoverPassword" type="password" minlength="8" required autocomplete="new-password"
                   class="w-full bg-black border border-gray-800 text-gray-200 px-3 py-2 focus:border-emerald-700 focus:outline-none text-sm" />
          </div>

          <div>
            <label class="block text-[10px] tracking-widest uppercase text-gray-500 mb-1.5">{{ t('recover.confirm') }}</label>
            <input v-model="recoverPasswordConfirm" type="password" minlength="8" required autocomplete="new-password"
                   class="w-full bg-black border border-gray-800 text-gray-200 px-3 py-2 focus:border-emerald-700 focus:outline-none text-sm" />
          </div>

          <p v-if="recoverErr" class="text-[11px] text-red-400 break-all">⨯ {{ recoverErr }}</p>

          <button type="submit" :disabled="recoverSubmitting"
                  class="w-full px-3 py-2 border border-emerald-700 text-emerald-300 hover:bg-emerald-900/30 text-[11px] tracking-widest uppercase disabled:opacity-50 flex items-center justify-center gap-1">
            <Icon v-if="recoverSubmitting" name="loader" /><Icon v-else name="send" class="inline" />
            <span>{{ recoverSubmitting ? t('recover.submitting') : t('recover.submit') }}</span>
          </button>

          <p class="text-[10px] text-gray-600 leading-relaxed text-center">
            {{ t('recover.warn_one_shot') }}
          </p>
        </form>
      </div>
    </section>

    <!-- /verify-forward/<token> — recipient clicked the link in the
         verification email. No auth; result is server-driven. -->
    <section v-else-if="verifyForwardToken" class="min-h-[calc(100dvh-40px)] flex items-center justify-center px-3 sm:px-4 py-6">
      <div class="w-full max-w-xl bg-gray-950 border border-gray-800 px-5 py-8 sm:px-10 sm:py-12">
        <div class="flex flex-col items-center mb-6 sm:mb-8">
          <img src="/logo.svg" alt="" class="h-14 sm:h-20 w-auto mb-4 select-none max-w-full" draggable="false" />
          <h1 class="font-display font-bold text-lg sm:text-2xl text-gray-100 tracking-tight text-center">
            {{ t('verify_forward.title') }}
          </h1>
        </div>

        <div v-if="verifyForwardLoading" class="text-xs text-gray-400 text-center">
          <Icon name="loader" class="inline mr-1" /> {{ t('verify_forward.verifying') }}
        </div>

        <div v-else-if="verifyForwardOK" class="space-y-3 text-center">
          <div class="text-emerald-400 text-xs">
            <Icon name="check" class="inline mr-1" /> {{ t('verify_forward.done') }}
          </div>
          <p class="text-[11px] text-gray-500 leading-relaxed">
            {{ t('verify_forward.done_hint') }}
            <span class="text-emerald-300">{{ verifyForwardAddress }}</span>
          </p>
          <p v-if="verifyForwardRedirectIn > 0" class="text-[11px] text-gray-600 mt-3">
            {{ t('verify_forward.redirecting_in', { n: verifyForwardRedirectIn }) }}
          </p>
          <a href="/" class="block text-center px-3 py-2 border border-emerald-700 text-emerald-300 hover:bg-emerald-900/30 text-[10px] tracking-widest uppercase">
            {{ t('verify_forward.go_home_now') }}
          </a>
        </div>

        <div v-else class="space-y-3">
          <div class="text-red-400 text-xs">
            <Icon name="alert" class="inline mr-1" /> {{ t('verify_forward.err_title') }}
          </div>
          <p class="text-[11px] text-gray-500 break-all">⨯ {{ verifyForwardErr || t('verify_forward.err_invalid') }}</p>
        </div>
      </div>
    </section>

    <!-- login -->
    <section v-else-if="phase === 'login'" class="min-h-[calc(100dvh-40px)] flex flex-col items-center justify-center gap-4 px-3 sm:px-4 py-6">
      <div class="w-full max-w-xl bg-gray-950 border border-gray-800 px-5 py-8 sm:px-10 sm:py-12">
        <!-- logo + tagline -->
        <div class="flex flex-col items-center mb-7 sm:mb-10">
          <img src="/logo.svg" alt="Acme Net"
               class="h-14 sm:h-20 md:h-24 w-auto mb-4 sm:mb-5 select-none max-w-full"
               draggable="false" />
          <h1 class="font-display font-bold text-lg sm:text-2xl md:text-3xl text-gray-100 tracking-tight text-center px-2">
            Acme Cloud Webmail System
          </h1>
        </div>

        <p class="text-[11px] sm:text-xs text-gray-500 mb-5 sm:mb-6 text-center max-w-md mx-auto leading-relaxed">
          {{ t('login.intro') }}
        </p>

        <form @submit.prevent="submitLogin" class="space-y-4 sm:space-y-5">
          <div>
            <label class="block text-[10px] tracking-widest uppercase text-gray-500 mb-1.5">{{ t('login.mailbox') }}</label>
            <div class="flex items-stretch bg-black border border-gray-800 focus-within:border-emerald-700">
              <input v-model="loginMailbox" type="text" autocomplete="username" inputmode="email"
                     class="flex-1 min-w-0 bg-transparent text-gray-200 px-3 sm:px-4 py-2.5 focus:outline-none text-sm"
                     :placeholder="t('login.local_placeholder')"
                     pattern="[a-z0-9][a-z0-9.\-]{0,30}[a-z0-9]"
                     required />
              <span class="px-3 sm:px-4 py-2.5 text-gray-500 text-sm bg-gray-950/60 border-l border-gray-800 select-none whitespace-nowrap">
                {{ '@' + mailDomain }}
              </span>
            </div>
          </div>
          <div>
            <label class="block text-[10px] tracking-widest uppercase text-gray-500 mb-1.5 flex items-center justify-between">
              <span>{{ t('login.password') }}</span>
              <span v-if="capsLockOn" class="text-amber-400 normal-case tracking-normal text-[10px]">
                <Icon name="caps" class="inline" /> {{ t('login.caps_lock_on') }}
              </span>
            </label>
            <div class="flex items-stretch bg-black border border-gray-800 focus-within:border-emerald-700">
              <input v-model="loginPassword"
                     :type="loginPasswordVisible ? 'text' : 'password'"
                     autocomplete="current-password"
                     @keydown="detectCapsLock"
                     @keyup="detectCapsLock"
                     class="flex-1 bg-transparent text-gray-200 px-3 sm:px-4 py-2.5 focus:outline-none text-sm"
                     required />
              <button type="button" @click="loginPasswordVisible = !loginPasswordVisible"
                      :title="loginPasswordVisible ? t('login.password_hide') : t('login.password_show')"
                      class="px-3 text-gray-600 hover:text-emerald-400 border-l border-gray-800">
                <Icon :name="loginPasswordVisible ? 'eye-off' : 'eye'" />
              </button>
            </div>
          </div>
          <label class="flex items-start gap-2 text-xs text-gray-400 cursor-pointer select-none">
            <input v-model="loginRemember" type="checkbox" class="accent-emerald-600 mt-0.5 shrink-0" />
            <span>
              {{ t('login.remember') }}
              <span class="block text-[10px] text-gray-600 mt-0.5 leading-snug">{{ t('login.remember_hint') }}</span>
            </span>
          </label>
          <button type="submit" :disabled="loading || !turnstileToken"
                  class="w-full py-2.5 border border-emerald-800 bg-emerald-950/30 text-emerald-300 hover:bg-emerald-900/40 disabled:opacity-50 tracking-widest text-xs uppercase">
            <Icon v-if="loading" name="loader" /><Icon v-else name="send" class="inline mr-1" /><span>{{ loading ? t('login.signing_in') : t('login.sign_in') }}</span>
          </button>

          <!-- passkey alternative -->
          <div class="relative pt-1">
            <div class="absolute inset-x-0 top-4 h-px bg-gray-800"></div>
            <div class="relative flex justify-center">
              <span class="bg-gray-950 px-3 text-[10px] tracking-widest uppercase text-gray-600">{{ t('login.or') }}</span>
            </div>
          </div>
          <button type="button" @click="loginWithPasskey"
                  :disabled="passkeyLoginBusy || !passkeySupported || !loginMailbox.trim()"
                  :title="!passkeySupported ? t('passkey.err.unsupported')
                          : !loginMailbox.trim() ? t('passkey.err.mailbox_first') : ''"
                  class="w-full py-2.5 border border-gray-700 hover:border-emerald-600 hover:text-emerald-300 disabled:opacity-30 disabled:cursor-not-allowed tracking-widest text-xs uppercase flex items-center justify-center gap-2">
            <span><Icon name="key" /></span>
            <span>{{ passkeyLoginBusy ? t('passkey.signing_in') : t('passkey.sign_in') }}</span>
          </button>
          <p v-if="passkeySupported && !loginMailbox.trim()"
             class="text-[10px] text-gray-700 text-center -mt-2">
            {{ t('passkey.err.mailbox_first') }}
          </p>

          <!-- SSO sign-in via admin.example.com. Browser hits the admin
               sso-out endpoint directly; the redirect chain (login if
               needed → mint mail ticket → ingest here → 30d cookie) is
               handled entirely server-side. No JS needed on this side. -->
          <a :href="'https://admin.example.com/api/v1/auth/sso-out?target=mail'"
             class="w-full py-2.5 border border-gray-700 hover:border-blue-500 hover:text-blue-300 tracking-widest text-xs uppercase flex items-center justify-center gap-2 no-underline">
            <Icon name="shield" />
            <span>{{ t('login.sign_in_with_admin') }}</span>
          </a>

          <!-- register CTA — boxed so it's not confused with the forgot-password
               footer link, since they have very different consequences -->
          <button type="button" @click="operatorRegisterOpen = true"
                  class="w-full flex items-center justify-between gap-2 px-3 py-2 border border-gray-800 bg-gray-900/40 hover:border-emerald-700 hover:bg-emerald-950/30 group">
            <span class="text-left">
              <span class="block text-[10px] tracking-widest uppercase text-gray-500 group-hover:text-emerald-500">
                {{ t('login.register_kicker') }}
              </span>
              <span class="block text-xs text-gray-300 group-hover:text-emerald-300">
                {{ t('login.register_cta') }}
              </span>
            </span>
            <span class="text-emerald-500 group-hover:translate-x-0.5 transition-transform">→</span>
          </button>

          <div class="flex items-center justify-center pt-1">
            <button type="button" @click="openForgot"
                    class="text-[11px] text-gray-600 hover:text-amber-400 underline-offset-4 hover:underline">
              {{ t('login.forgot') }}
            </button>
          </div>
        </form>
      </div>

      <!-- Turnstile · floats below the login card as a separate verification
           step. Server (/api/v1/mail/auth) rejects submits without a valid
           token, so passkey-on-this-page sign-in still works only because
           passkey itself is human-presence proof — Turnstile only gates
           the password path. -->
      <div class="ncn-turnstile-shell">
        <div id="cf-turnstile-mount" class="ncn-turnstile"></div>
        <p v-if="turnstileErr" class="mt-2 text-[10px] text-red-400 tracking-normal text-center">
          ⨯ {{ turnstileErr }}
        </p>
      </div>
    </section>

    <!-- mailbox layout
         desktop (md+):  3-pane grid, all visible
         mobile:         single pane based on mobileView -->
    <section v-else class="h-[calc(100dvh-40px)] overflow-x-hidden md:overflow-x-visible md:grid md:[grid-template-columns:200px_340px_1fr]">
      <!-- folder list -->
      <aside
        class="bg-gray-950 overflow-y-auto md:border-r md:border-gray-800 h-full mob-pane mob-pane-folders"
        :class="{ 'hidden md:block': mobileView !== 'folders' }"
      >
        <div class="px-3 py-2 border-b border-gray-800 flex items-center justify-between sticky top-0 bg-gray-950 z-10">
          <span class="text-[10px] tracking-widest uppercase text-gray-500">{{ t('sidebar.folders') }}</span>
          <button @click="openCompose()" class="text-xs px-2 py-0.5 border border-emerald-800 bg-emerald-950/30 text-emerald-300 hover:bg-emerald-900/40">
            + {{ t('sidebar.compose') }}
          </button>
        </div>
        <ul class="py-1 relative">
          <li
            v-for="f in sortedFolders" :key="f.name"
            @click="selectFolder(f.name)"
            class="px-3 py-2 cursor-pointer text-xs flex items-center justify-between gap-2 transition-colors"
            :class="activeFolder === f.name ? 'bg-gray-900 text-emerald-300 border-l-2 border-emerald-700' : 'text-gray-400 hover:bg-gray-900 border-l-2 border-transparent'"
          >
            <span class="flex items-center gap-2 min-w-0">
              <Icon :name="folderIcon(f.name)" class="shrink-0 opacity-70" />
              <span class="truncate">{{ folderLabel(f.name) }}</span>
            </span>
            <span v-if="unseen(f) > 0" class="text-[10px] text-amber-400 shrink-0">{{ unseen(f) }}</span>
            <span v-else-if="f.total" class="text-[10px] text-gray-700 shrink-0">{{ f.total }}</span>
          </li>
        </ul>
      </aside>

      <!-- message list -->
      <main
        class="bg-gray-950 overflow-y-auto md:border-r md:border-gray-800 h-full mob-pane mob-pane-list"
        :class="{ 'hidden md:block': mobileView !== 'list' }"
      >
        <div class="sticky top-0 bg-gray-950 z-10 border-b border-gray-800">
          <div class="px-3 py-2 flex items-center justify-between gap-2">
            <button
              @click="mobileView = 'folders'"
              class="md:hidden text-gray-500 hover:text-gray-200 px-1.5 -ml-1.5 shrink-0"
              :title="t('sidebar.folders')"
            ><Icon name="arrow-left" /></button>
            <span class="text-[10px] tracking-widest uppercase text-gray-300 truncate">{{ activeFolder }}</span>
            <span class="text-[10px] text-gray-600 shrink-0">
              <span v-if="searchActive">{{ t('list.found') }} {{ total }}</span>
              <span v-else>{{ total }} {{ t('list.total') }}</span>
            </span>
          </div>
          <!-- Search input -->
          <div class="px-3 pb-2 flex items-center gap-2">
            <div class="flex-1 flex items-center bg-black border border-gray-800 focus-within:border-emerald-700">
              <span class="px-2 text-gray-700"><Icon name="search" /></span>
              <input v-model="searchQuery" @input="onSearchInput"
                     type="search" inputmode="search"
                     :placeholder="t('list.search_ph')"
                     class="flex-1 bg-transparent text-gray-200 text-xs py-1.5 focus:outline-none" />
              <button v-if="searchQuery" @click="clearSearch" class="px-2 text-gray-600 hover:text-gray-300"><Icon name="x" /></button>
              <Icon v-if="searchBusy" name="loader" class="mx-2 text-emerald-500" />
            </div>
          </div>
        </div>
        <div v-if="messages.length === 0" class="p-8 text-center text-xs text-gray-600">
          {{ t('list.empty') }}
        </div>
        <ul class="relative">
          <li
            v-for="m in messages" :key="m.uid"
            @click="openMessage(m)"
            class="px-3 py-2.5 border-b border-gray-900 cursor-pointer hover:bg-gray-900 active:bg-gray-900 text-xs"
            :class="[
              activeMessage?.uid === m.uid ? 'bg-gray-900' : '',
              (m.flags || []).includes('\\Seen') ? 'text-gray-500' : 'text-gray-200',
            ]"
          >
            <div class="flex items-center justify-between gap-2">
              <span class="truncate font-semibold">{{ shortFrom(m.from) }}</span>
              <span class="text-[10px] text-gray-600 shrink-0">{{ fmtDate(m.date) }}</span>
            </div>
            <div class="truncate mt-0.5" :class="(m.flags || []).includes('\\Seen') ? '' : 'font-semibold'">
              {{ m.subject || '(no subject)' }}
            </div>
            <div class="flex items-center gap-2 mt-1">
              <span v-if="(m.flags || []).includes('\\Flagged')" class="text-amber-400 text-[10px]"><Icon name="star" /></span>
              <span v-if="!(m.flags || []).includes('\\Seen')" class="text-emerald-500 text-[10px]">●</span>
              <span class="text-[10px] text-gray-700">UID {{ m.uid }}</span>
            </div>
          </li>
        </ul>
      </main>

      <!-- reader -->
      <section
        class="bg-black overflow-y-auto h-full mob-pane mob-pane-reader"
        :class="{ 'hidden md:block': mobileView !== 'reader' }"
      >
        <!-- mobile back bar -->
        <div class="md:hidden sticky top-0 bg-gray-950 border-b border-gray-800 px-3 py-2 flex items-center gap-2 z-10">
          <button @click="mobileView = 'list'" class="text-gray-500 hover:text-gray-200 px-1.5 -ml-1.5 shrink-0"><Icon name="arrow-left" /></button>
          <span class="text-[10px] tracking-widest uppercase text-gray-500 truncate">{{ activeFolder }}</span>
        </div>

        <div v-if="!activeMessage" class="p-16 text-center text-xs text-gray-600 hidden md:block">
          {{ t('reader.empty') }}
        </div>
        <article v-else class="p-4 sm:p-6 max-w-3xl">
          <header class="border-b border-gray-800 pb-4 mb-4">
            <h2 class="text-base sm:text-lg text-gray-100 mb-3 break-words">{{ activeMessage.subject || '(no subject)' }}</h2>
            <dl class="grid grid-cols-[48px_1fr] sm:grid-cols-[60px_1fr] gap-x-2 gap-y-1 text-xs">
              <dt class="text-gray-600 uppercase tracking-widest text-[10px]">{{ t('reader.from') }}</dt>
              <dd class="text-emerald-400 break-all">{{ activeMessage.from?.join(', ') }}</dd>
              <dt class="text-gray-600 uppercase tracking-widest text-[10px]">{{ t('reader.to') }}</dt>
              <dd class="break-all">{{ activeMessage.to?.join(', ') }}</dd>
              <template v-if="activeMessage.cc?.length">
                <dt class="text-gray-600 uppercase tracking-widest text-[10px]">{{ t('reader.cc') }}</dt>
                <dd class="break-all">{{ activeMessage.cc.join(', ') }}</dd>
              </template>
              <dt class="text-gray-600 uppercase tracking-widest text-[10px]">{{ t('reader.date') }}</dt>
              <dd>{{ new Date(activeMessage.date).toLocaleString() }}</dd>
            </dl>
            <div class="mt-3 flex flex-wrap items-center gap-2">
              <button @click="openCompose(activeMessage)" class="px-3 py-1.5 text-xs border border-gray-700 hover:border-emerald-700 hover:text-emerald-300">
                <Icon name="reply" class="inline" /> {{ t('reader.reply') }}
              </button>
              <button
                @click="toggleFlag(activeMessage.uid, '\\Flagged')"
                class="px-3 py-1.5 text-xs border border-gray-700 hover:border-amber-600"
                :class="(activeMessage.flags || []).includes('\\Flagged') ? 'text-amber-400 border-amber-700' : 'text-gray-400'"
              >
                <Icon name="star" class="inline" /> {{ t('reader.flag') }}
              </button>

              <!-- move-to dropdown -->
              <div class="relative">
                <button @click="moveMenuOpen = !moveMenuOpen"
                        class="px-3 py-1.5 text-xs border border-gray-700 hover:border-emerald-700 hover:text-emerald-300">
                  <Icon name="move" class="inline" /> {{ t('reader.move_to') }}
                </button>
                <Transition name="dropdown">
                  <div v-if="moveMenuOpen"
                       class="absolute z-20 left-0 mt-1 min-w-[160px] bg-gray-950 border border-gray-800 max-h-[260px] overflow-y-auto shadow-2xl">
                    <div v-if="!moveTargets.length" class="px-3 py-2 text-[10px] text-gray-600 italic">
                      {{ t('reader.no_targets') }}
                    </div>
                    <button v-for="name in moveTargets" :key="name"
                            @click="moveMessage(activeMessage!, name)"
                            class="block w-full text-left px-3 py-1.5 text-xs text-gray-300 hover:bg-gray-900 hover:text-emerald-300 truncate">
                      {{ name }}
                    </button>
                  </div>
                </Transition>
              </div>

              <button @click="deleteMessage(activeMessage)" class="px-3 py-1.5 text-xs border border-gray-700 hover:border-red-700 hover:text-red-400">
                <Icon name="trash" class="inline" /> {{ t('reader.delete') }}
              </button>
            </div>
          </header>

          <div v-if="activeMessage.attachments?.length" class="mb-4 border border-gray-800 bg-gray-950 p-3">
            <div class="text-[10px] tracking-widest uppercase text-gray-500 mb-2">
              {{ t('reader.attachments') }} ({{ activeMessage.attachments.length }})
            </div>
            <ul class="text-xs space-y-1">
              <li v-for="(a, i) in activeMessage.attachments" :key="i">
                <a :href="attachmentURL(activeMessage.uid, i)"
                   :download="a.filename || `attachment-${i}`"
                   class="flex items-start gap-2 px-2 py-1.5 -mx-2 hover:bg-gray-900 text-gray-400 hover:text-emerald-300 group">
                  <span class="text-gray-600 group-hover:text-emerald-400 shrink-0"><Icon name="paperclip" /></span>
                  <span class="min-w-0 flex-1">
                    <span class="break-all">{{ a.filename || `attachment-${i}` }}</span>
                    <span class="block text-gray-700 text-[10px]">{{ a.content_type }} · {{ Math.round(a.size / 1024) }} KB</span>
                  </span>
                  <span class="text-[10px] tracking-widest uppercase text-gray-700 group-hover:text-emerald-500 shrink-0"><Icon name="arrow-down" /> {{ t('reader.download') }}</span>
                </a>
              </li>
            </ul>
          </div>

          <!-- sandbox MUST include allow-same-origin (but NEVER allow-scripts):
               the message body references external images via our same-origin
               /api/v1/mail/img-proxy. With an empty sandbox the iframe gets an
               opaque origin, so CSP `img-src 'self'` treats the proxy as cross-
               origin (blocked) AND the auth cookie isn't sent (401). With
               allow-same-origin (and no scripts) the proxy URLs load and the
               cookie rides along, while email HTML still cannot execute any JS.
               CID inline images (data: URLs) work either way. -->
          <iframe
            v-if="activeMessage.html"
            :srcdoc="activeMessage.html"
            sandbox="allow-same-origin"
            class="w-full min-h-[60vh] bg-white text-black border border-gray-800"
          />
          <pre v-else-if="activeMessage.text" class="whitespace-pre-wrap break-words text-gray-300 text-xs leading-relaxed">{{ activeMessage.text }}</pre>
          <div v-else class="text-xs text-gray-600 italic">{{ t('reader.empty_body') }}</div>

          <div v-if="activeMessage.parse_warning" class="mt-4 text-[10px] text-amber-500/70">
            <Icon name="alert" class="inline mr-1 align-[-0.125em]" /> {{ t('reader.parse_warning') }}: {{ activeMessage.parse_warning }}
          </div>
        </article>
      </section>
    </section>

    <!-- admin invites + reset modal -->
    <Teleport to="body">
      <Transition name="modal">
      <div v-if="invitesPanelOpen" class="fixed inset-0 bg-black/80 flex items-end sm:items-center justify-center z-50 sm:p-4">
        <div class="modal-card w-full max-w-3xl bg-gray-950 border-t sm:border border-gray-800 max-h-[100dvh] sm:max-h-[90dvh] flex flex-col rounded-t-lg sm:rounded-none"
             style="padding-bottom: env(safe-area-inset-bottom);">
          <header class="px-4 py-2 border-b border-gray-800 flex items-center justify-between shrink-0">
            <span class="text-xs tracking-[0.2em] uppercase text-gray-200">// {{ t('admin.invites_title') }}</span>
            <button @click="invitesPanelOpen = false" class="text-gray-600 hover:text-gray-300 text-lg px-2 -mr-2"><Icon name="x" /></button>
          </header>
          <div class="p-4 space-y-5 overflow-y-auto flex-1 min-h-0">

            <!-- pending forgot-password requests -->
            <section v-if="forgotRequests.length" class="border border-amber-900/60 bg-amber-950/10">
              <div class="px-3 py-2 border-b border-gray-800 text-[10px] tracking-widest uppercase text-amber-400 flex justify-between">
                <span><Icon name="alert" class="inline mr-1 align-[-0.125em]" /> {{ t('admin.forgot.title') }}</span>
                <span class="text-gray-700">{{ forgotRequests.length }}</span>
              </div>
              <ul class="divide-y divide-gray-900">
                <li v-for="req in forgotRequests" :key="req.id"
                    class="px-3 py-2 text-xs flex flex-col sm:flex-row sm:items-center gap-2">
                  <div class="flex-1 min-w-0">
                    <div class="text-emerald-400 break-all">{{ req.mailbox }}</div>
                    <div class="text-[10px] text-gray-600 mt-0.5 flex flex-wrap gap-x-3">
                      <span>{{ t('admin.forgot.requested') }} {{ new Date(req.requested_at).toLocaleString() }}</span>
                      <span class="font-mono">{{ req.ip }}</span>
                    </div>
                  </div>
                  <div class="flex gap-1 shrink-0 flex-wrap">
                    <button @click="approveForgotRequest(req)"
                            :disabled="forgotApproveBusy[req.id]"
                            class="text-[10px] px-2 py-1 border border-emerald-700 text-emerald-300 hover:bg-emerald-900/40 disabled:opacity-30 tracking-widest uppercase">
                      <Icon name="check" class="inline" />
                      {{ forgotApproveBusy[req.id] ? t('admin.forgot.sending') : t('admin.forgot.approve') }}
                    </button>
                    <button @click="adoptForgotRequest(req)"
                            class="text-[10px] px-2 py-1 border border-amber-700 text-amber-300 hover:bg-amber-900/40 tracking-widest uppercase">
                      <Icon name="download" class="inline" /> {{ t('admin.forgot.load') }}
                    </button>
                    <button @click="dismissForgotRequest(req.id)"
                            class="text-[10px] px-2 py-1 border border-gray-700 hover:border-red-500 hover:text-red-400 tracking-widest uppercase">
                      <Icon name="x" class="inline" /> {{ t('admin.forgot.dismiss') }}
                    </button>
                  </div>
                </li>
              </ul>
            </section>

            <!-- admin password reset -->
            <section id="admin-reset-form" class="border border-amber-900/60 bg-amber-950/10">
              <div class="px-3 py-2 border-b border-gray-800 text-[10px] tracking-widest uppercase text-amber-400">
                {{ t('admin.reset.title') }}
              </div>
              <div class="p-3 space-y-2">
                <p class="text-[11px] text-gray-500 leading-relaxed">{{ t('admin.reset.intro') }}</p>
                <form @submit.prevent="adminResetPassword" class="flex flex-col sm:flex-row gap-2">
                  <input v-model="resetMailbox" type="email" :placeholder="t('admin.reset.mailbox_ph')"
                         class="flex-1 bg-black border border-gray-800 text-gray-200 px-3 py-1.5 focus:border-amber-700 focus:outline-none text-xs" />
                  <input v-model="resetPassword" type="text" :placeholder="t('admin.reset.password_ph')"
                         class="flex-1 bg-black border border-gray-800 text-gray-200 px-3 py-1.5 focus:border-amber-700 focus:outline-none text-xs" />
                  <button type="submit" :disabled="resetBusy"
                          class="px-4 py-1.5 border border-amber-800 bg-amber-950/30 text-amber-300 hover:bg-amber-900/40 disabled:opacity-50 text-xs tracking-widest uppercase shrink-0">
                    <Icon v-if="resetBusy" name="loader" /><span v-else>{{ t('admin.reset.button') }}</span>
                  </button>
                </form>
                <div v-if="resetDone" class="text-[11px] text-emerald-400 mt-1">
                  ✓ {{ t('admin.reset.done', { mailbox: resetDone.mailbox }) }}
                </div>
              </div>
            </section>

            <!-- create form -->
            <form @submit.prevent="createInvite" class="flex flex-col sm:flex-row gap-2">
              <input v-model="inviteNote" type="text" :placeholder="t('admin.note_placeholder')"
                     class="flex-1 bg-black border border-gray-800 text-gray-200 px-3 py-1.5 focus:border-emerald-700 focus:outline-none text-xs" />
              <button type="submit" :disabled="invitesBusy"
                      class="px-4 py-1.5 border border-emerald-800 bg-emerald-950/30 text-emerald-300 hover:bg-emerald-900/40 disabled:opacity-50 text-xs tracking-widest uppercase shrink-0">
                + {{ t('admin.create') }}
              </button>
            </form>

            <!-- last-issued banner — token shown ONCE -->
            <div v-if="lastIssued" class="border border-amber-800 bg-amber-950/20 p-3 space-y-2">
              <div class="text-[10px] tracking-widest uppercase text-amber-400"><Icon name="alert" class="inline mr-1 align-[-0.125em]" /> {{ t('admin.copy_now') }}</div>
              <div class="flex items-center gap-2">
                <code class="flex-1 text-[11px] text-gray-300 bg-black px-2 py-1.5 border border-gray-800 break-all">{{ lastIssued.url }}</code>
                <button @click="copyToClipboard(lastIssued.url)" class="px-3 py-1.5 text-xs border border-gray-700 hover:border-emerald-700 hover:text-emerald-300 shrink-0">
                  <Icon name="clipboard" />
                </button>
              </div>
              <div class="text-[10px] text-gray-600">
                {{ t('admin.expires') }} {{ new Date(lastIssued.expires_at).toLocaleString() }}
              </div>
              <button @click="lastIssued = null" class="text-[10px] text-gray-600 hover:text-gray-300 underline">
                {{ t('admin.dismiss') }}
              </button>
            </div>

            <!-- list -->
            <div class="border border-gray-800">
              <div class="px-3 py-2 border-b border-gray-800 text-[10px] tracking-widest uppercase text-gray-500 flex justify-between">
                <span>{{ t('admin.outstanding') }}</span>
                <span class="text-gray-700">{{ invitesList.length }}</span>
              </div>
              <div v-if="invitesList.length === 0" class="p-6 text-center text-xs text-gray-600">
                {{ t('admin.none') }}
              </div>
              <ul v-else class="divide-y divide-gray-900">
                <li v-for="i in invitesList" :key="i.prefix" class="px-3 py-2 text-xs flex flex-col sm:flex-row sm:items-center gap-2">
                  <div class="flex-1 min-w-0">
                    <div class="flex items-center gap-2">
                      <code class="text-emerald-400">{{ i.prefix }}…</code>
                      <span v-if="i.used_by" class="text-[10px] text-gray-600">→ {{ i.used_by }}@{{ registerPreview?.domain || 'example.com' }}</span>
                    </div>
                    <div class="text-[10px] text-gray-600 mt-0.5 flex flex-wrap gap-x-3">
                      <span>{{ t('admin.by') }} <span class="text-gray-400">{{ i.created_by }}</span></span>
                      <span>{{ t('admin.expires') }} <span class="text-gray-400">{{ new Date(i.expires_at).toLocaleDateString() }}</span></span>
                      <span v-if="i.note" class="italic text-gray-500">"{{ i.note }}"</span>
                    </div>
                  </div>
                  <button @click="revokeInvite(i.prefix)" :disabled="!!i.used_by"
                          class="text-[10px] px-2 py-1 border border-gray-700 hover:border-red-700 hover:text-red-400 shrink-0 disabled:opacity-30 disabled:cursor-not-allowed">
                    {{ i.used_by ? t('admin.used') : t('admin.revoke') }}
                  </button>
                </li>
              </ul>
            </div>
          </div>
        </div>
      </div>
      </Transition>
    </Teleport>

    <!-- settings modal (per-user) -->
    <Teleport to="body">
      <Transition name="modal">
      <div v-if="settingsOpen" class="fixed inset-0 bg-black/80 flex items-end sm:items-center justify-center z-50 sm:p-4">
        <div class="modal-card w-full max-w-2xl bg-gray-950 border-t sm:border border-gray-800 max-h-[100dvh] sm:max-h-[90dvh] flex flex-col rounded-t-lg sm:rounded-none"
             style="padding-bottom: env(safe-area-inset-bottom);">
          <header class="px-4 py-2 border-b border-gray-800 flex items-center justify-between shrink-0">
            <span class="text-xs tracking-[0.2em] uppercase text-gray-200">// {{ t('settings.title') }}</span>
            <button @click="settingsOpen = false" class="text-gray-600 hover:text-gray-300 text-lg px-2 -mr-2"><Icon name="x" /></button>
          </header>
          <div class="p-4 space-y-5 overflow-y-auto flex-1 min-h-0">
            <!-- passkeys -->
            <section class="border border-gray-800">
              <div class="px-3 py-2 border-b border-gray-800 text-[10px] tracking-widest uppercase text-gray-500 flex justify-between">
                <span>{{ t('passkey.section') }}</span>
                <span class="text-gray-700">{{ passkeyList.length }}</span>
              </div>
              <div class="p-3 space-y-3">
                <p class="text-[11px] text-gray-500 leading-relaxed">{{ t('passkey.intro') }}</p>

                <ul v-if="passkeyList.length" class="space-y-1">
                  <li v-for="p in passkeyList" :key="p.id"
                      class="flex items-center justify-between gap-2 text-xs px-2 py-1.5 border border-gray-800 bg-black">
                    <span class="min-w-0 flex-1">
                      <span class="text-emerald-400 truncate block"><Icon name="key" /> {{ p.name }}</span>
                      <span class="text-[10px] text-gray-600">
                        {{ t('passkey.added') }} {{ new Date(p.created_at).toLocaleDateString() }}
                        <template v-if="p.last_used_at"> · {{ t('passkey.used') }} {{ new Date(p.last_used_at).toLocaleDateString() }}</template>
                      </span>
                    </span>
                    <button @click="revokePasskey(p.id)" class="text-gray-600 hover:text-red-400 shrink-0 text-[10px] uppercase tracking-widest">{{ t('passkey.revoke') }}</button>
                  </li>
                </ul>

                <form @submit.prevent="registerPasskey" class="flex flex-col sm:flex-row gap-2">
                  <input v-model="passkeyName" type="text" :placeholder="t('passkey.name_ph')" maxlength="64"
                         class="flex-1 bg-black border border-gray-800 text-gray-200 px-3 py-1.5 focus:border-emerald-700 focus:outline-none text-xs" />
                  <button type="submit" :disabled="passkeyBusy || !passkeySupported"
                          :title="passkeySupported ? '' : t('passkey.err.unsupported')"
                          class="px-4 py-1.5 border border-emerald-800 bg-emerald-950/30 text-emerald-300 hover:bg-emerald-900/40 disabled:opacity-50 text-xs tracking-widest uppercase shrink-0">
                    <Icon v-if="passkeyBusy" name="loader" /><span v-else><Icon name="plus" class="inline" /> {{ t('passkey.add_btn') }}</span>
                  </button>
                </form>
                <p v-if="!passkeySupported" class="text-[10px] text-amber-500/70"><Icon name="alert" class="inline mr-1 align-[-0.125em]" /> {{ t('passkey.err.unsupported') }}</p>
              </div>
            </section>

            <!-- forwarding -->
            <section class="border border-gray-800">
              <div class="px-3 py-2 border-b border-gray-800 text-[10px] tracking-widest uppercase text-gray-500 flex justify-between">
                <span>{{ t('settings.forward.title') }}</span>
                <span class="text-gray-700">{{ forwardAddresses.length + forwardPending.length }}/8</span>
              </div>
              <div class="p-3 space-y-3">
                <p class="text-[11px] text-gray-500 leading-relaxed">
                  {{ t('settings.forward.intro') }}
                </p>

                <!-- VERIFIED addresses (active in sieve) -->
                <ul v-if="forwardAddresses.length" class="space-y-1">
                  <li v-for="a in forwardAddresses" :key="a"
                      class="flex items-center justify-between gap-2 text-xs px-2 py-1.5 border border-gray-800 bg-black">
                    <span class="flex items-center gap-2 min-w-0">
                      <Icon name="check" class="text-emerald-500 shrink-0" />
                      <span class="truncate text-emerald-400 break-all">{{ a }}</span>
                    </span>
                    <button @click="removeForwardAddr(a)" class="text-gray-600 hover:text-red-400 shrink-0"><Icon name="x" /></button>
                  </li>
                </ul>

                <!-- PENDING addresses (awaiting recipient click) -->
                <ul v-if="forwardPending.length" class="space-y-1">
                  <li v-for="a in forwardPending" :key="a"
                      class="flex items-center justify-between gap-2 text-xs px-2 py-1.5 border border-amber-900/60 bg-amber-950/10">
                    <span class="flex items-center gap-2 min-w-0">
                      <Icon name="loader" class="text-amber-400 shrink-0" />
                      <span class="truncate text-amber-300 break-all">{{ a }}</span>
                      <span class="text-[10px] text-amber-500/70 shrink-0 uppercase tracking-widest">{{ t('settings.forward.pending_badge') }}</span>
                    </span>
                    <button @click="removeForwardAddr(a)" class="text-gray-600 hover:text-red-400 shrink-0"><Icon name="x" /></button>
                  </li>
                </ul>

                <form @submit.prevent="addForwardAddr" class="flex gap-2">
                  <input v-model="forwardDraft" type="email" inputmode="email" :placeholder="t('settings.forward.placeholder')"
                         class="flex-1 bg-black border border-gray-800 text-gray-200 px-3 py-1.5 focus:border-emerald-700 focus:outline-none text-xs" />
                  <button type="submit"
                          class="px-3 py-1.5 border border-gray-700 hover:border-emerald-700 hover:text-emerald-300 text-xs tracking-widest uppercase shrink-0">
                    + {{ t('settings.forward.add') }}
                  </button>
                </form>

                <!-- "verification email sent" toast — sticky until the user
                     adds another address or the page refreshes -->
                <div v-if="forwardVerifyJustSent.length" class="text-[10px] text-amber-300 border border-amber-900/60 bg-amber-950/20 px-2 py-1.5 leading-relaxed">
                  <Icon name="send" class="inline mr-1" />
                  {{ t('settings.forward.verify_sent_prefix') }}
                  <span class="font-mono">{{ forwardVerifyJustSent.join(', ') }}</span>
                  — {{ t('settings.forward.verify_sent_suffix') }}
                </div>

                <!-- Status line: auto-save indicator + autosave hint -->
                <div class="flex items-center gap-2 pt-2 border-t border-gray-900 text-[10px]">
                  <Icon v-if="forwardBusy" name="loader" class="text-emerald-500" />
                  <Icon v-else-if="forwardSaved" name="check" class="text-emerald-500" />
                  <span v-if="forwardBusy" class="text-emerald-400">{{ t('settings.forward.saving') }}</span>
                  <span v-else-if="forwardSaved" class="text-emerald-400">{{ t('settings.forward.saved') }}</span>
                  <span v-else class="text-gray-700">{{ t('settings.forward.autosave_hint') }}</span>
                </div>
              </div>
            </section>
          </div>
        </div>
      </div>
      </Transition>
    </Teleport>

    <!-- operator-only register info modal -->
    <Teleport to="body">
      <Transition name="modal">
      <div v-if="operatorRegisterOpen" class="fixed inset-0 bg-black/80 flex items-center justify-center z-50 p-4">
        <div class="modal-card w-full max-w-md bg-gray-950 border border-gray-800">
          <header class="px-4 py-2 border-b border-gray-800 flex items-center justify-between">
            <span class="text-xs tracking-[0.2em] uppercase text-gray-200">// {{ t('login.register_title') }}</span>
            <button @click="operatorRegisterOpen = false" class="text-gray-600 hover:text-gray-300 text-lg px-2 -mr-2"><Icon name="x" /></button>
          </header>
          <div class="p-5 space-y-4 text-xs text-gray-400 leading-relaxed">
            <div class="border border-amber-900/60 bg-amber-950/20 px-3 py-2 text-amber-400 text-[11px]">
              <Icon name="alert" class="inline mr-1 align-[-0.125em]" /> {{ t('login.register_operator_only') }}
            </div>
            <p>{{ t('login.register_intro') }}</p>
            <ol class="list-decimal list-inside space-y-1 text-[11px] text-gray-500">
              <li>{{ t('login.register_step1') }}</li>
              <li>{{ t('login.register_step2') }}</li>
              <li>{{ t('login.register_step3') }}</li>
            </ol>
            <a :href="operatorBridgeURL"
               class="block text-center px-4 py-2.5 border border-emerald-800 bg-emerald-950/30 text-emerald-300 hover:bg-emerald-900/40 tracking-widest text-xs uppercase">
              {{ t('login.register_continue') }} <Icon name="external" class="inline" />
            </a>
            <p class="text-[10px] text-gray-700 pt-2 border-t border-gray-900">
              {{ t('login.register_non_operator_note') }}
            </p>
          </div>
        </div>
      </div>
      </Transition>
    </Teleport>

    <!-- forgot password modal -->
    <Teleport to="body">
      <Transition name="modal">
      <div v-if="forgotOpen" class="fixed inset-0 bg-black/80 flex items-center justify-center z-50 p-4">
        <div class="modal-card w-full max-w-md bg-gray-950 border border-gray-800">
          <header class="px-4 py-2 border-b border-gray-800 flex items-center justify-between">
            <span class="text-xs tracking-[0.2em] uppercase text-gray-200">// {{ t('login.forgot_title') }}</span>
            <button @click="forgotOpen = false" class="text-gray-600 hover:text-gray-300 text-lg px-2 -mr-2"><Icon name="x" /></button>
          </header>

          <!-- success -->
          <div v-if="forgotDone" class="p-5 space-y-3">
            <div class="border border-emerald-800 bg-emerald-950/30 p-3 text-emerald-300 text-xs">
              ✓ {{ t('login.forgot_done') }}
            </div>
            <p class="text-[11px] text-gray-500 leading-relaxed">{{ t('login.forgot_done_hint') }}</p>
            <button @click="forgotOpen = false"
                    class="block w-full text-center px-4 py-2.5 border border-gray-700 hover:border-gray-500 text-xs tracking-widest uppercase">
              {{ t('login.forgot_close') }}
            </button>
          </div>

          <!-- form -->
          <form v-else @submit.prevent="submitForgotRequest" class="p-5 space-y-3">
            <p class="text-xs text-gray-400 leading-relaxed">{{ t('login.forgot_intro') }}</p>
            <div>
              <label class="block text-[10px] tracking-widest uppercase text-gray-500 mb-1.5">{{ t('login.forgot_mailbox') }}</label>
              <div class="flex items-stretch bg-black border border-gray-800 focus-within:border-amber-700">
                <input v-model="forgotMailbox" type="text" inputmode="email"
                       class="flex-1 min-w-0 bg-transparent text-gray-200 px-3 py-2 focus:outline-none text-sm"
                       :placeholder="t('login.local_placeholder')"
                       pattern="[a-z0-9][a-z0-9.\-]{0,30}[a-z0-9]"
                       required />
                <span class="px-3 py-2 text-gray-500 text-sm bg-gray-950/60 border-l border-gray-800 select-none whitespace-nowrap">
                  {{ '@' + mailDomain }}
                </span>
              </div>
            </div>
            <button type="submit" :disabled="forgotBusy"
                    class="w-full py-2.5 border border-amber-800 bg-amber-950/30 text-amber-300 hover:bg-amber-900/40 disabled:opacity-50 tracking-widest text-xs uppercase">
              <Icon v-if="forgotBusy" name="loader" /><span v-else><Icon name="send" class="inline mr-1" />{{ t('login.forgot_submit') }}</span>
            </button>
            <p class="text-[10px] text-gray-600 pt-2 border-t border-gray-900 leading-relaxed">
              {{ t('login.forgot_rate_note') }}
            </p>
          </form>
        </div>
      </div>
      </Transition>
    </Teleport>

    <!-- compose modal -->
    <Teleport to="body">
      <Transition name="modal">
      <div v-if="composeOpen" class="fixed inset-0 bg-black/80 flex items-end sm:items-center justify-center z-50 sm:p-4">
        <!--
          Mobile bottom-sheet: max-h:100dvh handles iOS keyboard via the
          dynamic viewport unit. flex-col + form flex-1 lets the form
          scroll inside the sheet while header/footer stay pinned. Safe-
          area-inset-bottom keeps the action row above iOS's home pill.
        -->
        <div class="modal-card w-full max-w-2xl bg-gray-950 border-t sm:border border-gray-800 max-h-[100dvh] sm:max-h-[90dvh] flex flex-col rounded-t-lg sm:rounded-none"
             style="padding-bottom: env(safe-area-inset-bottom);">
          <header class="px-4 py-2 border-b border-gray-800 flex items-center justify-between shrink-0">
            <span class="text-xs tracking-[0.2em] uppercase text-gray-200">// {{ t('compose.title') }}</span>
            <button @click="composeOpen = false" class="text-gray-600 hover:text-gray-300 text-lg px-2 -mr-2"><Icon name="x" /></button>
          </header>
          <form @submit.prevent="sendCompose" class="p-4 space-y-3 overflow-y-auto flex-1 min-h-0">
            <div>
              <label class="block text-[10px] tracking-widest uppercase text-gray-500 mb-1">{{ t('compose.to') }}</label>
              <input v-model="composeTo" @input="scheduleDraftSave" type="text" class="w-full bg-black border border-gray-800 text-gray-200 px-3 py-1.5 focus:border-emerald-700 focus:outline-none text-xs" placeholder="someone@example.com" required />
            </div>
            <div>
              <label class="block text-[10px] tracking-widest uppercase text-gray-500 mb-1">{{ t('compose.cc') }}</label>
              <input v-model="composeCc" @input="scheduleDraftSave" type="text" class="w-full bg-black border border-gray-800 text-gray-200 px-3 py-1.5 focus:border-emerald-700 focus:outline-none text-xs" />
            </div>
            <div>
              <label class="block text-[10px] tracking-widest uppercase text-gray-500 mb-1">{{ t('compose.bcc') }}</label>
              <input v-model="composeBcc" @input="scheduleDraftSave" type="text"
                     class="w-full bg-black border border-gray-800 text-gray-200 px-3 py-1.5 focus:border-emerald-700 focus:outline-none text-xs" />
              <p v-if="composeBcc" class="text-[10px] text-amber-500/70 mt-1">{{ t('compose.bcc_hint') }}</p>
            </div>
            <div>
              <label class="block text-[10px] tracking-widest uppercase text-gray-500 mb-1">{{ t('compose.subject') }}</label>
              <input v-model="composeSubject" @input="scheduleDraftSave" type="text" class="w-full bg-black border border-gray-800 text-gray-200 px-3 py-1.5 focus:border-emerald-700 focus:outline-none text-xs" required />
            </div>
            <div>
              <div class="flex items-center justify-between mb-1">
                <label class="text-[10px] tracking-widest uppercase text-gray-500">{{ t('compose.body') }}</label>
                <button type="button" @click="toggleFormat"
                        class="text-[10px] tracking-widest uppercase px-2 py-0.5 border border-gray-800 hover:border-emerald-700 hover:text-emerald-400">
                  {{ composeFormat === 'html' ? t('compose.html.switch_plain') : t('compose.html.switch_html') }}
                </button>
              </div>

              <!-- HTML toolbar -->
              <div v-if="composeFormat === 'html'"
                   class="flex flex-wrap items-center gap-1 px-2 py-1 border border-b-0 border-gray-800 bg-black">
                <button type="button" @click="execFormat('bold')"             class="px-2 py-0.5 text-xs text-gray-300 hover:text-emerald-300 hover:bg-gray-900 font-bold"     :title="t('compose.html.bold')">B</button>
                <button type="button" @click="execFormat('italic')"           class="px-2 py-0.5 text-xs text-gray-300 hover:text-emerald-300 hover:bg-gray-900 italic"        :title="t('compose.html.italic')">I</button>
                <button type="button" @click="execFormat('underline')"        class="px-2 py-0.5 text-xs text-gray-300 hover:text-emerald-300 hover:bg-gray-900 underline"     :title="t('compose.html.underline')">U</button>
                <span class="text-gray-700 mx-1">·</span>
                <button type="button" @click="execLink"                      class="px-2 py-0.5 text-xs text-gray-300 hover:text-emerald-300 hover:bg-gray-900" :title="t('compose.html.link')">🔗</button>
                <button type="button" @click="execFormat('insertUnorderedList')" class="px-2 py-0.5 text-xs text-gray-300 hover:text-emerald-300 hover:bg-gray-900" :title="t('compose.html.bullet')">•</button>
                <button type="button" @click="execFormat('insertOrderedList')"   class="px-2 py-0.5 text-xs text-gray-300 hover:text-emerald-300 hover:bg-gray-900" :title="t('compose.html.numbered')">1.</button>
                <span class="text-gray-700 mx-1">·</span>
                <button type="button" @click="execFormat('removeFormat')"    class="px-2 py-0.5 text-xs text-gray-500 hover:text-amber-400 hover:bg-gray-900" :title="t('compose.html.clear')">∅</button>
              </div>

              <!-- HTML editor -->
              <div v-if="composeFormat === 'html'"
                   ref="composeEditor"
                   contenteditable="true"
                   v-html="composeBody"
                   @input="onEditorInput"
                   class="w-full bg-black border border-gray-800 text-gray-200 px-3 py-2 focus:border-emerald-700 focus:outline-none text-xs leading-relaxed min-h-[180px] max-h-[400px] overflow-y-auto"
              ></div>
              <!-- Plain text fallback -->
              <textarea v-else v-model="composeBody" @input="scheduleDraftSave" rows="10"
                        class="w-full bg-black border border-gray-800 text-gray-200 px-3 py-2 focus:border-emerald-700 focus:outline-none text-xs leading-relaxed font-mono resize-y" required></textarea>
            </div>

            <!-- attachments -->
            <div>
              <div class="flex items-center justify-between mb-1">
                <label class="text-[10px] tracking-widest uppercase text-gray-500">
                  {{ t('compose.attachments') }} ({{ composeAttachments.length }})
                </label>
                <button type="button" @click="pickAttachments"
                        class="text-[10px] tracking-widest uppercase text-gray-500 hover:text-emerald-400 px-2 py-0.5 border border-gray-800 hover:border-emerald-700">
                  <Icon name="paperclip" class="inline" /> {{ t('compose.attach') }}
                </button>
                <input ref="composeFileInput" type="file" multiple class="hidden" @change="onAttachmentsPicked" />
              </div>
              <ul v-if="composeAttachments.length" class="space-y-1">
                <li v-for="(f, i) in composeAttachments" :key="i"
                    class="flex items-center justify-between gap-2 text-xs px-2 py-1 border border-gray-800 bg-black">
                  <span class="min-w-0 flex-1 truncate text-gray-300"><Icon name="paperclip" /> {{ f.name }}</span>
                  <span class="text-[10px] text-gray-600 shrink-0">{{ fmtSize(f.size) }}</span>
                  <button type="button" @click="removeAttachment(i)" class="text-gray-600 hover:text-red-400 shrink-0"><Icon name="x" /></button>
                </li>
              </ul>
              <p v-else class="text-[10px] text-gray-700 italic">{{ t('compose.attach_hint') }}</p>
            </div>

            <div class="flex items-center justify-between gap-2 pt-2 border-t border-gray-900 flex-wrap">
              <span class="text-[10px] text-gray-600">
                <span v-if="composeDraftBusy" class="text-emerald-500 animate-pulse"><Icon name="loader" class="inline" /> {{ t('compose.draft_saving') }}</span>
                <span v-else-if="composeDraftSavedAt"><Icon name="save" /> {{ draftSavedAgo }}</span>
                <span v-else>{{ t('compose.draft_hint') }}</span>
              </span>
              <div class="flex items-center gap-2">
                <button type="button" @click="composeOpen = false" class="px-3 py-1.5 text-xs border border-gray-700 hover:border-gray-500">
                  {{ t('compose.cancel') }}
                </button>
                <button type="submit" :disabled="composeSending" class="px-4 py-1.5 text-xs border border-emerald-800 bg-emerald-950/30 text-emerald-300 hover:bg-emerald-900/40 disabled:opacity-50">
                  <Icon v-if="composeSending" name="loader" /><Icon v-else name="send" class="inline mr-1" /><span>{{ composeSending ? t('compose.sending') : t('compose.send') }}</span>
                </button>
              </div>
            </div>
          </form>
        </div>
      </div>
      </Transition>
    </Teleport>
  </div>
</template>
