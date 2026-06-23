<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useSessionStore } from '@/stores/session'
import { useLocaleAware } from '@/i18n'
import { api } from '@/api/client'
import {
  decodeRequestOptions, encodeAssertionResponse, isWebAuthnSupported
} from '@/utils/webauthn'

const router = useRouter()
const route  = useRoute()
const session = useSessionStore()
const { t, caseClass, trackMed } = useLocaleAware()
const passkeySupported = computed(() => isWebAuthnSupported())

// Two-phase form state machine.
//   'credentials' — username + password only (no TOTP field on screen)
//   'totp'        — server requested step-2: shows TOTP input + "trust this
//                   device" checkbox. Username/password are now hidden.
//   'submitting'  — request in flight; controls block.
type LoginPhase = 'credentials' | 'totp'
const phase = ref<LoginPhase>('credentials')

const username = ref('')
const password = ref('')
const totp     = ref('')
const trustDevice = ref(true)   // default-on: most users want it
const submitting = ref(false)
const errorMsg = ref('')

// ---- Cloudflare Turnstile widget ----
//
// The public sitekey is provisioned via `POST .../challenges/widgets`
// against the example.com Cloudflare account. Burning it into the bundle
// is fine — it's the public identifier; the secret stays server-side
// at /etc/ncn-core-console/turnstile.secret.
//
// We render explicitly (data-callback / data-error-callback) so the
// completed token sits in `turnstileToken.value` and we can block the
// submit button until it's present.
const TURNSTILE_SITEKEY = '0x4AAAAAADWGuzZ_rM0PGeFJ'
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
// expose callbacks on window so the JSONP-style data-callback can find them
;(window as any).turnstileOnSuccess = turnstileOnSuccess
;(window as any).turnstileOnError   = turnstileOnError
;(window as any).turnstileOnExpire  = turnstileOnExpire

function renderTurnstile() {
  const w = (window as any).turnstile
  if (!w) return
  const el = document.getElementById('cf-turnstile-mount')
  if (!el || turnstileWidgetId) return
  // Pass function references, NOT string names — explicit render() API
  // resolves callbacks by reference; string lookup is only documented for
  // implicit data-callback HTML mode. With strings the callback silently
  // no-ops on some widget versions and the submit button stays disabled.
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

interface BootLine {
  status: 'ok' | 'wait' | 'err'
  text: string
}

const boot = ref<BootLine[]>([])
let bootTimers: number[] = []

function pushBoot(line: BootLine, delay: number) {
  const t = window.setTimeout(() => boot.value.push(line), delay)
  bootTimers.push(t)
}

// Redirect after successful login. The `?next=` query param can be:
//   - a Vue-router path  (e.g. "/admin/security")          → router.replace
//   - a same-origin /api/ path that 302s further along     → window.location
//   - an absolute URL on a whitelisted host (admin/mail)   → window.location
// We resolve the form before deciding which navigator to use; arbitrary
// off-domain absolute URLs are rejected so a hostile /login?next=https://evil
// link can't redirect operators through us.
function navigateAfterLogin() {
  const raw = (route.query.next as string) || '/admin'
  if (raw.startsWith('/api/')) {
    window.location.replace(raw)
    return
  }
  if (raw.startsWith('http')) {
    try {
      const u = new URL(raw)
      if (u.host === 'mail.example.com' || u.host === 'admin.example.com') {
        window.location.replace(raw)
        return
      }
    } catch { /* fall through to default */ }
    router.replace('/admin')
    return
  }
  router.replace(raw)
}

const now = ref(new Date())
let clockTimer: ReturnType<typeof setInterval> | null = null

// ── OAuth / external login — our own consistent branded buttons ──
// Both GitHub and Telegram are standard OAuth/OIDC redirect flows now: one
// full-page redirect into the backend's /start, which 302s to the provider.
const oauthEnabled = ref<string[]>([])
function oauthLogin(provider: string) { window.location.href = '/api/v1/auth/oauth/' + encodeURIComponent(provider) + '/start' }
async function loadOAuthProviders() {
  try {
    const env = await api.oauthProviders()
    if (env.ok && env.data) {
      oauthEnabled.value = env.data.enabled || []
    }
  } catch { /* providers endpoint optional — buttons just don't render */ }
}

onMounted(() => {
  // If we landed here from a failed SSO ingest, surface the reason so
  // the user isn't staring at an unexplained login screen.
  const ssoErr = route.query.sso_err as string | undefined
  if (ssoErr) {
    const msg = ssoErr === 'no-operator'
      ? t('login.sso_err.no_operator')
      : ssoErr === 'pending'
      ? t('login.sso_err.pending')
      : t('login.sso_err.invalid')
    errorMsg.value = msg
    pushBoot({ status: 'err', text: 'SSO REJECTED · ' + msg }, 0)
  }

  // OAuth callback bounced back here on failure (?oauth_err=…).
  const oauthErr = route.query.oauth_err as string | undefined
  if (oauthErr) {
    errorMsg.value = oauthErr === 'not-bound'
      ? t('login.oauth_err_not_bound')
      : oauthErr === 'provider-disabled'
      ? t('login.oauth_err_disabled')
      : t('login.oauth_err_generic')
  }
  loadOAuthProviders()

  // Boot sequence — typewriter feel
  const seq: Array<[number, BootLine]> = [
    [0,    { status: 'ok',   text: t('login.boot.transport') }],
    [180,  { status: 'ok',   text: t('login.boot.totp') }],
    [340,  { status: 'ok',   text: t('login.boot.vault') }],
    [500,  { status: 'wait', text: t('login.boot.awaiting') }]
  ]
  for (const [d, l] of seq) pushBoot(l, d)

  clockTimer = setInterval(() => (now.value = new Date()), 1000)

  // Inject Turnstile JS once. ORDERING MATTERS:
  //   1. window.ncnTurnstileReady MUST be defined BEFORE the <script>
  //      tag is appended. CF api.js reads its own `?onload=NAME` query
  //      and calls window[NAME]() the moment Turnstile is initialised.
  //      If the network is hot (script already in HTTP cache on a
  //      desktop browser), the load event can fire before the
  //      following statement runs → callback is undefined → silent
  //      no-op → widget never renders. Mobile networks were slow
  //      enough to hide this race; PC Chrome with a warm cache loses
  //      every time.
  //   2. `async` is enough — don't also set `defer`; some browsers
  //      treat the combination inconsistently.
  ;(window as any).ncnTurnstileReady = renderTurnstile
  if (document.querySelector('script[data-cf-turnstile]')) {
    // Script tag already present from a prior mount (SPA re-entry into
    // /login without a full reload). If the runtime is already up,
    // render immediately; otherwise our callback above will fire when
    // CF finishes bootstrap.
    if ((window as any).turnstile) renderTurnstile()
  } else {
    const s = document.createElement('script')
    s.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?onload=ncnTurnstileReady'
    s.async = true
    s.dataset.cfTurnstile = '1'
    document.head.appendChild(s)
  }
})

onBeforeUnmount(() => {
  for (const t of bootTimers) clearTimeout(t)
  if (clockTimer) clearInterval(clockTimer)
})

// ============ Passkey login ============
async function loginWithPasskey() {
  if (submitting.value) return
  submitting.value = true
  errorMsg.value = ''
  boot.value.push({ status: 'wait', text: 'requesting WebAuthn challenge' })

  try {
    const begin = await api.passkeyLoginBegin()
    if (!begin.ok || !begin.data) {
      throw new Error(begin.error ?? 'challenge request failed')
    }
    const options = decodeRequestOptions(begin.data.options)
    boot.value.push({ status: 'wait', text: 'awaiting authenticator (Touch ID / passkey vault / YubiKey ...)' })
    const cred = await navigator.credentials.get(options) as PublicKeyCredential | null
    if (!cred) throw new Error('credential request aborted')

    const finish = await api.passkeyLoginFinish(
      begin.data.challenge_id,
      encodeAssertionResponse(cred)
    )
    if (!finish.ok || !finish.data) {
      throw new Error(finish.error ?? 'passkey verification failed')
    }
    boot.value.push({ status: 'ok', text: 'WebAuthn signature verified' })
    boot.value.push({ status: 'ok', text: `session established · operator=${finish.data.operator}` })

    await session.fetchMe()
    setTimeout(() => {
      navigateAfterLogin()
    }, 500)
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e)
    boot.value.push({ status: 'err', text: 'PASSKEY REJECTED · ' + msg })
    errorMsg.value = msg
    submitting.value = false
  }
}

// ============ Forgot-password modal ============
const showRecover = ref(false)
const recoverUser = ref('')
const recoverCode = ref('')
const recoverNewPw = ref('')
const recoverBusy = ref(false)
const recoverErr = ref('')
const recoverDone = ref('')

function openRecover() {
  showRecover.value = true
  recoverUser.value = username.value || 'NOC'
  recoverCode.value = ''
  recoverNewPw.value = ''
  recoverErr.value = ''
  recoverDone.value = ''
}

async function submitRecover() {
  if (recoverBusy.value) return
  recoverBusy.value = true
  recoverErr.value = ''
  try {
    const env = await api.authRecover({
      username: recoverUser.value.trim(),
      recovery_code: recoverCode.value.trim().toUpperCase(),
      new_password: recoverNewPw.value
    })
    if (env.ok && env.data) {
      recoverDone.value = `密码已重置 · 剩余 ${env.data.remaining_codes} 个恢复码`
      // Auto-fill back into the main form
      username.value = env.data.operator
      password.value = recoverNewPw.value
      setTimeout(() => { showRecover.value = false }, 1500)
    } else {
      recoverErr.value = env.error ?? 'unknown error'
    }
  } catch (e: unknown) {
    recoverErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    recoverBusy.value = false
  }
}

// Step 1 — credentials submit. If the server says totp_required we
// flip to phase='totp' and show the second-step UI. If the server
// recognized a trusted-device cookie, the session is already alive
// and we jump straight to the dashboard.
async function submitCredentials() {
  if (submitting.value) return
  errorMsg.value = ''
  // Turnstile is required (server enforces too). Block submit + nudge
  // the user if the widget hasn't produced a token yet.
  if (!turnstileToken.value) {
    turnstileErr.value = t('login.turnstile.needed')
    return
  }
  submitting.value = true

  boot.value.push({ status: 'wait', text: t('login.boot.validating') })

  try {
    const { needsTotp } = await session.login(
      username.value.trim(),
      password.value,
      turnstileToken.value,
    )
    if (needsTotp) {
      // Smooth UX transition into step 2.
      boot.value.push({ status: 'ok', text: t('login.boot.signature') })
      boot.value.push({ status: 'wait', text: t('login.boot.verifying') })
      phase.value = 'totp'
      // Focus the TOTP input after Vue renders it.
      setTimeout(() => {
        const el = document.querySelector('input[name=totp]') as HTMLInputElement | null
        el?.focus()
      }, 30)
    } else {
      // Trusted-device shortcut OR first-login bootstrap — session already live.
      boot.value.push({ status: 'ok', text: t('login.boot.signature') })
      boot.value.push({ status: 'ok', text: t('login.boot.established', { op: session.operator }) })
      boot.value.push({ status: 'ok', text: t('login.boot.redirecting') })
      setTimeout(() => {
        navigateAfterLogin()
      }, 600)
    }
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e)
    boot.value.push({ status: 'err', text: t('login.boot.rejected', { reason: msg }) })
    errorMsg.value = msg
    // Turnstile tokens are one-shot — refresh after every failed submit
    // so a retry doesn't 401 with "expired token".
    resetTurnstile()
  } finally {
    submitting.value = false
  }
}

// Step 2 — verify TOTP + optionally trust this browser. The server
// reads the short-lived intent cookie set by step 1; we just send the
// 6-digit code + the checkbox state.
async function submitTOTP() {
  if (submitting.value) return
  errorMsg.value = ''
  submitting.value = true

  try {
    await session.verifyTOTP(totp.value.trim(), trustDevice.value)
    boot.value.push({ status: 'ok', text: t('login.boot.established', { op: session.operator }) })
    boot.value.push({ status: 'ok', text: t('login.boot.redirecting') })
    setTimeout(() => {
      navigateAfterLogin()
    }, 600)
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : String(e)
    boot.value.push({ status: 'err', text: t('login.boot.rejected', { reason: msg }) })
    errorMsg.value = msg
    totp.value = ''  // TOTP rotates every 30s; clear so user types fresh
  } finally {
    submitting.value = false
  }
}

// Single entry point bound to the form @submit so we can route to step
// 1 or step 2 from the same Enter key.
function submit() {
  if (phase.value === 'credentials') {
    return submitCredentials()
  }
  return submitTOTP()
}

// Back arrow on step 2 — abandons the intent ticket on the client side
// (server side it just expires in 5 min). Returns user to credentials
// form so they can try a different username, or use passkey instead.
function cancelTOTPStep() {
  if (submitting.value) return
  phase.value = 'credentials'
  totp.value = ''
  errorMsg.value = ''
  // The widget token from the first attempt was already consumed by
  // the credentials POST that put us in TOTP phase. Hand the user a
  // fresh challenge so the next submit isn't gated by a stale token.
  resetTurnstile()
}

const ts = () => now.value.toISOString().replace('T', ' ').slice(0, 19) + 'Z'
</script>

<template>
  <div class="min-h-screen w-full bg-gray-950 text-gray-300 font-mono flex flex-col items-center justify-center px-4 py-8 gap-4 select-none">

    <!-- ncn-dark-island: re-defines --g-* palette vars to the dark Tailwind
         gray scale so the entire login interior stays terminal-dark in
         light mode while the surrounding page bg flips with the theme.
         See src/style.css → "Login form — dark island override".
         Width: max-w-xl (was max-w-2xl) — matches webmail's login card so
         the form feels intentional rather than sparse inside a wide box. -->
    <div class="ncn-dark-island w-full max-w-xl border border-gray-800 bg-gray-900 shadow-2xl">

      <!-- Hero — logo carries the brand ("Acme Net" is in the
           wordmark itself), the H1 below names what THIS page is for.
           Logo has a transparent bg (chroma-keyed pure #000 → alpha) so it
           sits flush on the card's bg-gray-900 with no visible rectangle.
           Sized to match webmail's hero (h-14/sm:h-20/md:h-24) so the two
           consoles' login facades have the same brand weight.
           A small emerald-dot status line under the H1 carries the
           operator-console subtitle from i18n (kept so the i18n key stays
           used, and so the transition into the boot log below is softened
           rather than abrupt). -->
      <div class="flex flex-col items-center px-5 sm:px-8 pt-8 sm:pt-10 pb-6">
        <!-- Logo sized by width (max-w-*) not height. The webp is 3646×601
             after the trim+padding pass — aspect ≈ 6.07:1 — so width-based
             sizing gives a predictable visible wordmark height that fills
             the image bounds. The previous h-based sizing on the
             untrimmed 4096×1407 image rendered the wordmark at only ~40%
             of the image's height, with ~30% transparent padding above
             and below — which is why the gap to the title below looked
             huge no matter how small mb-* got. -->
        <img src="/download/ncnlogo_dark.webp" alt="Acme Net"
             class="w-full max-w-[15rem] sm:max-w-[24rem] md:max-w-[28rem] mb-3 sm:mb-4 select-none"
             draggable="false" loading="eager" decoding="async" />
        <!-- Title: "Network Operations Center" — the standard industry
             term (NOC) for a centralised facility that monitors and
             manages network infrastructure (BGP, PoPs, fleet health,
             alerts, audit). Accurate to what this console actually is
             and instantly recognisable to network engineers. -->
        <h1 class="font-display font-bold text-lg sm:text-2xl md:text-3xl text-gray-100 tracking-tight text-center px-2">
          Network Operations Center
        </h1>
        <div :class="['mt-2 flex items-center gap-2 text-[10px] text-gray-500', trackMed, caseClass]">
          <span class="w-1.5 h-1.5 bg-emerald-500 animate-pulse"></span>
          {{ t('login.title_console') }}
        </div>
      </div>

      <!-- Boot log — unified into the card surface (no separate bg, no
           heavy padding), so it reads as the terminal-style detail line
           BELOW the hero rather than a competing band.
           Each row is a 2-column CSS grid:
             col 1 = status pill (auto width, content-sized)
             col 2 = log text (1fr, fills the rest, wraps inside its column)
           This pins wrapped continuation lines flush with the first
           character of text — they never crash back to the left margin
           or push the pill off the edge on narrow viewports. -->
      <div class="px-5 sm:px-8 pb-5 border-b border-gray-800/70 min-h-[5rem]">
        <ul class="text-[11px] space-y-0.5">
          <li v-for="(l, i) in boot" :key="i"
              class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-2 items-baseline leading-snug">
            <!-- Status pill — brackets static, label in fixed 4ch centered
                 box so OK / FAIL / .. render at identical pixel width.
                 No tracking-widest (it broke per-glyph kerning). -->
            <span
              :class="[
                'font-mono tabular-nums whitespace-nowrap',
                l.status === 'ok'   ? 'text-emerald-500/80' :
                l.status === 'err'  ? 'text-red-500'        :
                                      'text-amber-400/80 animate-pulse'
              ]"
            >[<span class="inline-block w-[4ch] text-center">{{ l.status === 'ok' ? 'OK' : l.status === 'err' ? 'FAIL' : '..' }}</span>]</span>
            <span
              :class="[
                'block min-w-0 break-words',
                l.status === 'err' ? 'text-red-400' : 'text-gray-500'
              ]"
              style="overflow-wrap: anywhere;"
            >{{ l.text }}</span>
          </li>
        </ul>
      </div>

      <!-- Form. Two phases:
           - `credentials`: username + password (clean two-field UI).
           - `totp`: TOTP code + "trust this device" checkbox. Username/
             password are hidden because the server already validated
             them; the intent ticket cookie holds the state.
           Padding matches hero + boot log (px-5 sm:px-8) so the card
           has one unified horizontal rhythm.
           Inputs use a focus-within bordered wrapper (no $/§/⌖ prefix
           cells — those were the only place that idiom existed in the
           whole product, and the cell/input seam never looked clean).
           The same pattern is used in webmail's login. -->
      <form @submit.prevent="submit" class="px-5 sm:px-8 py-6 space-y-4">
        <!-- ====== PHASE 1: credentials ====== -->
        <template v-if="phase === 'credentials'">
          <div>
            <label :class="['block text-[10px] tracking-widest text-gray-500 mb-1.5', caseClass]">
              {{ t('login.operator_label') }}
            </label>
            <div class="flex items-stretch bg-black border border-gray-800 focus-within:border-emerald-700">
              <input
                v-model="username"
                type="text"
                autocomplete="username"
                autocapitalize="off"
                autocorrect="off"
                spellcheck="false"
                :disabled="submitting"
                placeholder=""
                class="flex-1 min-w-0 bg-transparent px-3 py-2.5 text-sm font-mono text-gray-100 focus:outline-none disabled:opacity-50"
              />
            </div>
          </div>

          <div>
            <label :class="['block text-[10px] tracking-widest text-gray-500 mb-1.5', caseClass]">
              {{ t('login.password_label') }}
            </label>
            <div class="flex items-stretch bg-black border border-gray-800 focus-within:border-emerald-700">
              <input
                v-model="password"
                type="password"
                autocomplete="current-password"
                :disabled="submitting"
                placeholder=""
                class="flex-1 min-w-0 bg-transparent px-3 py-2.5 text-sm font-mono text-gray-100 focus:outline-none disabled:opacity-50"
              />
            </div>
          </div>

          <button
            type="submit"
            :disabled="submitting || !username || !password || !turnstileToken"
            class="w-full mt-1 py-2.5 border border-emerald-700 bg-emerald-950/30 text-emerald-300
                   hover:bg-emerald-900/40 text-xs tracking-widest uppercase
                   disabled:opacity-30 disabled:cursor-not-allowed
                   transition-colors"
          >{{ submitting ? t('login.submitting') : t('login.submit') }}</button>
        </template>

        <!-- ====== PHASE 2: TOTP + trust device ====== -->
        <template v-else>
          <!-- Mini header recapping which account we're verifying for.
               Helps avoid "wait, am I logging into the right account?"
               in shared-browser cases. -->
          <div class="flex items-center justify-between text-[10px] tracking-widest text-gray-500 uppercase">
            <span><span class="text-emerald-500">$</span> {{ username }}</span>
            <button type="button" @click="cancelTOTPStep" :disabled="submitting"
              class="text-gray-600 hover:text-gray-300 disabled:opacity-40">
              {{ t('login.totp_back') }}
            </button>
          </div>

          <div>
            <label :class="['block text-[10px] tracking-widest text-gray-500 mb-1.5', caseClass]">
              {{ t('login.totp_label') }}
              <span class="text-gray-700 normal-case tracking-normal">{{ t('login.totp_hint') }}</span>
            </label>
            <div class="flex items-stretch bg-black border border-gray-800 focus-within:border-emerald-700">
              <input
                v-model="totp"
                name="totp"
                type="text"
                inputmode="numeric"
                pattern="[0-9]*"
                maxlength="6"
                autocomplete="one-time-code"
                :disabled="submitting"
                placeholder=""
                class="flex-1 min-w-0 bg-transparent px-3 py-2.5 text-base font-mono tracking-[0.5em] text-emerald-400 focus:outline-none disabled:opacity-50"
              />
            </div>
          </div>

          <!-- "Trust this device" — default checked. When ticked, the
               server registers this browser; future password logins
               here will skip the TOTP step (cookie-driven, 90-day TTL). -->
          <label class="flex items-start gap-2 text-[11px] text-gray-300 cursor-pointer select-none">
            <input v-model="trustDevice" type="checkbox" :disabled="submitting"
              class="mt-0.5 accent-emerald-500" />
            <span class="leading-snug">
              {{ t('login.trust_device_label') }}
              <span class="block text-gray-600 normal-case tracking-normal text-[10px] mt-0.5">
                {{ t('login.trust_device_hint') }}
              </span>
            </span>
          </label>

          <button
            type="submit"
            :disabled="submitting || totp.length !== 6"
            class="w-full mt-1 py-2.5 border border-emerald-700 bg-emerald-950/30 text-emerald-300
                   hover:bg-emerald-900/40 text-xs tracking-widest uppercase
                   disabled:opacity-30 disabled:cursor-not-allowed
                   transition-colors"
          >{{ submitting ? t('login.submitting') : t('login.totp_submit') }}</button>
        </template>

        <!-- Passkey + Forgot password row — phase 1 only. In phase 2
             the user is already mid-flow with a valid intent ticket;
             the alternatives are: back arrow above, or just type the code. -->
        <div v-if="phase === 'credentials'" class="flex flex-col sm:flex-row gap-2 items-stretch sm:items-center pt-1">
          <button
            type="button"
            v-if="passkeySupported"
            @click="loginWithPasskey"
            :disabled="submitting"
            class="flex-1 inline-flex items-center justify-center gap-2 py-2.5 border border-gray-700 hover:border-blue-500 text-[10px] tracking-widest uppercase text-gray-300 hover:text-blue-400 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          >
            <svg viewBox="0 0 16 16" class="w-3.5 h-3.5" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
              <path d="M5 7 V5 a3 3 0 0 1 6 0 V7" stroke-linecap="round" stroke-linejoin="round" />
              <rect x="3" y="7" width="10" height="7" stroke-linecap="round" stroke-linejoin="round" />
              <circle cx="8" cy="10.5" r="1.2" fill="currentColor" />
            </svg>
            {{ t('login.passkey') }}
          </button>
          <button
            type="button"
            @click="openRecover"
            :disabled="submitting"
            class="text-[10px] tracking-widest uppercase text-gray-500 hover:text-amber-400 transition-colors py-2 px-2 sm:px-3"
          >
            {{ t('login.forgot') }}
          </button>
        </div>

        <!-- External login (only bound operators can sign in this way).
             Our own buttons → consistent with the passkey button + each carries
             its brand logo. -->
        <div v-if="phase === 'credentials' && oauthEnabled.length" class="pt-1 space-y-2">
          <div class="text-[10px] tracking-widest uppercase text-gray-600 text-center">{{ t('login.oauth_title') }}</div>
          <div class="flex flex-col sm:flex-row gap-2">
            <button
              v-if="oauthEnabled.includes('github')"
              type="button" @click="oauthLogin('github')"
              class="flex-1 inline-flex items-center justify-center gap-2 py-2.5 border border-gray-700 hover:border-blue-500 text-[10px] tracking-widest uppercase text-gray-300 hover:text-blue-400 transition-colors"
            >
              <svg viewBox="0 0 16 16" class="w-3.5 h-3.5" fill="currentColor" aria-hidden="true"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82a7.6 7.6 0 0 1 2-.27c.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z"/></svg>
              GitHub
            </button>
            <button
              v-if="oauthEnabled.includes('telegram')"
              type="button" @click="oauthLogin('telegram')"
              class="flex-1 inline-flex items-center justify-center gap-2 py-2.5 border border-gray-700 hover:border-blue-500 text-[10px] tracking-widest uppercase text-gray-300 hover:text-blue-400 transition-colors"
            >
              <svg viewBox="0 0 24 24" class="w-4 h-4" fill="currentColor" aria-hidden="true"><path d="M9.78 18.65l.28-4.23 7.68-6.92c.34-.31-.07-.46-.52-.19L7.74 13.3 3.64 12c-.88-.25-.89-.86.2-1.3l15.97-6.16c.73-.33 1.43.18 1.15 1.3l-2.72 12.81c-.19.91-.74 1.13-1.5.71L12.6 16.3l-1.99 1.93c-.23.23-.42.42-.83.42z"/></svg>
              Telegram
            </button>
          </div>
        </div>

        <div v-if="errorMsg" class="mt-2 border border-red-900 bg-black px-3 py-2 text-xs text-red-400">
          <span class="text-red-500 tracking-widest">[FAIL]</span> {{ errorMsg }}
        </div>
      </form>

      <!-- ============ Forgot-password modal ============ -->
      <transition
        enter-active-class="transition-opacity duration-100"
        enter-from-class="opacity-0" enter-to-class="opacity-100"
        leave-active-class="transition-opacity duration-75"
        leave-from-class="opacity-100" leave-to-class="opacity-0"
      >
        <div v-if="showRecover" class="fixed inset-0 z-[100] flex items-center justify-center p-4 bg-black/80 backdrop-blur-sm" @click.self="showRecover = false">
          <div class="border-2 border-amber-500 bg-gray-900 max-w-md w-full font-mono">
            <div class="px-4 py-2 border-b border-amber-500 bg-amber-900/30 text-amber-400 text-xs tracking-widest uppercase flex justify-between">
              <span>{{ t('login.recover.title') }}</span>
              <button @click="showRecover = false" class="text-gray-500 hover:text-gray-200">✕</button>
            </div>
            <div class="p-4 space-y-3">
              <p class="text-xs text-gray-400 normal-case tracking-normal leading-relaxed">{{ t('login.recover.intro') }}</p>

              <div>
                <label class="block text-[10px] tracking-widest text-gray-600 uppercase mb-1">{{ t('login.operator_label') }}</label>
                <input v-model="recoverUser" autocomplete="username" :disabled="recoverBusy"
                  class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-amber-500 focus:outline-none" />
              </div>
              <div>
                <label class="block text-[10px] tracking-widest text-gray-600 uppercase mb-1">{{ t('login.recover.code') }}</label>
                <input v-model="recoverCode" :disabled="recoverBusy"
                  placeholder=""
                  autocomplete="off" autocorrect="off" spellcheck="false"
                  class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-emerald-400 tracking-widest focus:border-amber-500 focus:outline-none" />
              </div>
              <div>
                <label class="block text-[10px] tracking-widest text-gray-600 uppercase mb-1">{{ t('login.recover.new_password') }}</label>
                <input v-model="recoverNewPw" type="password" autocomplete="new-password" :disabled="recoverBusy"
                  class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-amber-500 focus:outline-none" />
                <div class="text-[10px] text-gray-600 mt-1 normal-case tracking-normal">{{ t('login.recover.password_hint') }}</div>
              </div>

              <div v-if="recoverErr" class="text-xs text-red-400 normal-case tracking-normal">⨯ {{ recoverErr }}</div>
              <div v-if="recoverDone" class="text-xs text-emerald-400 normal-case tracking-normal">✓ {{ recoverDone }}</div>
            </div>
            <div class="flex border-t border-gray-800">
              <button @click="showRecover = false" :disabled="recoverBusy"
                class="flex-1 px-4 py-3 text-xs tracking-widest uppercase text-gray-400 hover:bg-gray-800 transition-colors disabled:opacity-50">{{ t('login.recover.cancel') }}</button>
              <button @click="submitRecover"
                :disabled="recoverBusy || !recoverUser || !recoverCode || recoverNewPw.length < 8"
                class="flex-1 px-4 py-3 text-xs tracking-widest uppercase bg-amber-600 text-white hover:bg-amber-500 transition-colors disabled:opacity-30 disabled:cursor-not-allowed">{{ recoverBusy ? '◌ RESETTING...' : t('login.recover.submit') }}</button>
            </div>
          </div>
        </div>
      </transition>

      <!-- Footer — Back link + meta info on ONE row. Was previously two
           stacked bands (back-bar + 3-cell meta grid) that doubled the
           visual weight at the bottom for very similar purposes. Same
           card bg as everything else — no bg-black band. -->
      <div :class="['px-5 sm:px-8 py-3 border-t border-gray-800 flex flex-wrap items-center justify-between gap-x-4 gap-y-1 text-[10px] tracking-widest text-gray-600', caseClass]">
        <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
          <span>
            <span class="text-gray-700">{{ t('login.protocol_label') }}</span>
            <span class="text-gray-400 ml-1">{{ t('login.protocol_value') }}</span>
          </span>
          <span class="hidden sm:inline">·</span>
          <span class="hidden sm:inline">
            <span class="text-gray-700">{{ t('login.ttl_label') }}</span>
            <span class="text-gray-400 ml-1">{{ t('login.ttl_value') }}</span>
          </span>
          <span class="hidden md:inline">·</span>
          <span class="hidden md:inline">
            <span class="text-gray-700">{{ t('login.clock_label') }}</span>
            <span class="text-emerald-500 ml-1 tabular-nums normal-case tracking-normal">{{ ts() }}</span>
          </span>
        </div>
        <router-link to="/" class="hover:text-emerald-500 transition-colors">
          {{ t('login.back') }}
        </router-link>
      </div>
    </div>

    <!-- Turnstile · floats below the login card as a separate verification
         step rather than fighting for space with the operator/password
         inputs. v-show (not v-if) so the widget stays mounted across
         credential ↔ TOTP phase swaps; phase 2 just hides it.
         Hidden during the passkey flow too — passkey is its own
         human-presence proof and doesn't need Turnstile. -->
    <div v-show="phase === 'credentials'" class="ncn-turnstile-shell">
      <div id="cf-turnstile-mount" class="ncn-turnstile"></div>
      <p v-if="turnstileErr" class="mt-2 text-[10px] text-red-400 normal-case tracking-normal text-center">
        ⨯ {{ turnstileErr }}
      </p>
    </div>
  </div>
</template>
