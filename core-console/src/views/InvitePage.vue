<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { api, type InvitePreview, type InviteCompleteResult } from '@/api/client'
import {
  decodeCreationOptions, encodeCreationResponse, isWebAuthnSupported
} from '@/utils/webauthn'

const route = useRoute()
const token = (route.params.token as string) || ''

// ---- Preview state ----
const previewLoading = ref(true)
const previewErr     = ref<string | null>(null)
const preview        = ref<InvitePreview | null>(null)
const expiresInSecs  = ref(0)
let expireTimer: ReturnType<typeof setInterval> | null = null

async function loadPreview() {
  if (!token) {
    previewErr.value = 'no token in URL'
    previewLoading.value = false
    return
  }
  try {
    const env = await api.invitePreview(token)
    if (!env.ok || !env.data) {
      previewErr.value = env.error ?? 'invalid invite'
      return
    }
    preview.value = env.data
    expiresInSecs.value = env.data.expires_in
    expireTimer = setInterval(() => {
      expiresInSecs.value = Math.max(0, expiresInSecs.value - 1)
      if (expiresInSecs.value === 0) {
        previewErr.value = 'invite expired'
        preview.value = null
      }
    }, 1000)
  } catch (e) {
    previewErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    previewLoading.value = false
  }
}

const expiresLabel = computed(() => {
  const s = expiresInSecs.value
  if (s <= 0) return '—'
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60
  return `${h}h ${m}m ${sec}s`
})

// ---- Registration form ----
const stage = ref<'identity' | 'mfa' | 'review' | 'submitting' | 'done'>('identity')
const username = ref('')
const password = ref('')
const passwordConfirm = ref('')
const formErr = ref<string | null>(null)

const passwordOk = computed(() => password.value.length >= 8 && password.value === passwordConfirm.value)
const usernameOk = computed(() => {
  const u = username.value.trim()
  return u.length >= 2 && u.length <= 32 && /^[A-Za-z0-9._-]+$/.test(u)
})
const canAdvanceIdentity = computed(() => usernameOk.value && passwordOk.value)

function goToMfa() {
  formErr.value = null
  if (!canAdvanceIdentity.value) {
    if (!usernameOk.value) formErr.value = 'username: 2-32 chars, [A-Za-z0-9._-]'
    else if (password.value.length < 8) formErr.value = 'password must be ≥ 8 chars'
    else if (password.value !== passwordConfirm.value) formErr.value = "passwords don't match"
    return
  }
  stage.value = 'mfa'
}

// ---- MFA: passkey OR TOTP ----
type MfaMethod = 'passkey' | 'totp'
const mfaMethod = ref<MfaMethod>('passkey')

const passkeyAvailable = computed(() => isWebAuthnSupported())
const passkeyBound = ref(false)
const passkeyChallengeId = ref<string>('')
const passkeyResponse = ref<unknown>(null)
const passkeyBusy = ref(false)
const passkeyName = ref('')

// Default the friendly label from a UA hint — operators almost never bother
// renaming, but "iPhone passkey" reads better than the generic fallback.
function defaultPasskeyName(): string {
  const ua = navigator.userAgent
  if (/iPhone|iPad|iPod/.test(ua))   return 'iOS passkey'
  if (/Android/.test(ua))            return 'Android passkey'
  if (/Mac OS X/.test(ua))           return 'macOS passkey'
  if (/Windows/.test(ua))            return 'Windows Hello passkey'
  return 'passkey · invite-bound'
}

async function bindPasskey() {
  if (!passkeyAvailable.value) {
    formErr.value = 'this browser does not support WebAuthn / passkeys'
    return
  }
  if (!usernameOk.value) {
    formErr.value = 'go back and choose a username first'
    return
  }
  passkeyBusy.value = true
  formErr.value = null
  try {
    // 1. Ask the server for a creation challenge scoped to (invite, username).
    //    Server validates the invite is still good + username isn't taken.
    const beginEnv = await api.invitePasskeyBegin(token, username.value.trim())
    if (!beginEnv.ok || !beginEnv.data) {
      formErr.value = beginEnv.error ?? 'failed to start passkey registration'
      return
    }
    const { challenge_id, options } = beginEnv.data as {
      challenge_id: string
      options: unknown
    }

    // 2. Hand the options to the authenticator. decodeCreationOptions returns
    //    the full { publicKey } envelope with base64url-encoded fields
    //    (challenge, user.id, excludeCredentials.id) converted to ArrayBuffers.
    const credOpts = decodeCreationOptions(options)
    const cred = await navigator.credentials.create(credOpts)
    if (!cred) {
      formErr.value = 'authenticator returned no credential — try again'
      return
    }

    // 3. Stash the challenge_id + encoded response. We don't send to /complete
    //    yet — that happens at the final confirm step alongside username+password.
    passkeyChallengeId.value = challenge_id
    passkeyResponse.value    = encodeCreationResponse(cred as PublicKeyCredential)
    if (!passkeyName.value.trim()) passkeyName.value = defaultPasskeyName()
    passkeyBound.value = true
    stage.value = 'review'
  } catch (e) {
    // Common: user cancelled the Touch ID / passkey prompt.
    const msg = e instanceof Error ? e.message : String(e)
    if (msg.includes('NotAllowedError') || msg.includes('cancelled')) {
      formErr.value = 'passkey prompt cancelled'
    } else {
      formErr.value = msg
    }
  } finally {
    passkeyBusy.value = false
  }
}

// ---- TOTP setup ----
const totpStage = ref<'pristine' | 'enrolling' | 'verified'>('pristine')
const totpSecret = ref<string>('')
const totpOtpauth = ref<string>('')
const totpCode = ref<string>('')
const totpBound = ref(false)
const totpBusy = ref(false)

function generateBase32(length = 32): string {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  const buf = new Uint8Array(length)
  crypto.getRandomValues(buf)
  let s = ''
  for (let i = 0; i < length; i++) s += alphabet[buf[i] % 32]
  return s
}

function totpBegin() {
  // Generate secret client-side — the server doesn't store it until the
  // confirm step. Identical entropy to the server's setup-begin path.
  totpSecret.value = generateBase32(32)
  totpOtpauth.value = `otpauth://totp/AcmeNet:${encodeURIComponent(username.value.trim())}?secret=${totpSecret.value}&issuer=Acme%20Cloud%20Network&algorithm=SHA1&digits=6&period=30`
  totpCode.value = ''
  totpStage.value = 'enrolling'
}

const qrSrc = computed(() => {
  if (!totpOtpauth.value) return ''
  return `https://api.qrserver.com/v1/create-qr-code/?size=240x240&margin=8&data=${encodeURIComponent(totpOtpauth.value)}`
})

function confirmTotp() {
  if (totpCode.value.trim().length < 6) {
    formErr.value = 'enter the 6-digit code your authenticator shows'
    return
  }
  formErr.value = null
  totpBound.value = true
  totpStage.value = 'verified'
  stage.value = 'review'
}

watch(mfaMethod, () => {
  formErr.value = null
})

// ---- Submit ----
const submitBusy = ref(false)
const submitResult = ref<InviteCompleteResult | null>(null)

async function submitRegistration() {
  submitBusy.value = true
  formErr.value = null
  stage.value = 'submitting'
  try {
    const payload: Parameters<typeof api.inviteComplete>[0] = {
      token,
      username: username.value.trim(),
      password: password.value,
    }
    if (mfaMethod.value === 'totp') {
      payload.totp = { secret: totpSecret.value, code: totpCode.value.trim() }
    } else if (mfaMethod.value === 'passkey') {
      if (!passkeyBound.value || !passkeyChallengeId.value || !passkeyResponse.value) {
        formErr.value = 'please bind a passkey before confirming'
        stage.value = 'mfa'
        return
      }
      payload.passkey = {
        challenge_id: passkeyChallengeId.value,
        response:     passkeyResponse.value,
        name:         passkeyName.value.trim() || defaultPasskeyName(),
      }
    }
    const env = await api.inviteComplete(payload)
    if (!env.ok || !env.data) {
      formErr.value = env.error ?? 'registration failed'
      stage.value = 'review'
      return
    }
    submitResult.value = env.data
    stage.value = 'done'
  } catch (e) {
    formErr.value = e instanceof Error ? e.message : String(e)
    stage.value = 'review'
  } finally {
    submitBusy.value = false
  }
}

onMounted(loadPreview)
</script>

<template>
  <div
    class="flex items-start justify-center px-3 sm:px-6 relative overflow-hidden"
    style="
      min-height: 100dvh;
      padding-top:    max(1.5rem, env(safe-area-inset-top));
      padding-bottom: max(1.5rem, env(safe-area-inset-bottom));
    "
  >
    <!-- Ambient backdrop -->
    <div class="absolute inset-0 pointer-events-none -z-10" aria-hidden="true">
      <div class="absolute -top-32 -left-32 w-[320px] h-[320px] sm:w-[480px] sm:h-[480px] rounded-full"
           style="background: radial-gradient(circle, rgba(16,185,129,0.18), transparent 60%); filter: blur(40px);"></div>
      <div class="absolute -bottom-32 -right-32 w-[360px] h-[360px] sm:w-[520px] sm:h-[520px] rounded-full"
           style="background: radial-gradient(circle, rgba(125,211,252,0.15), transparent 60%); filter: blur(40px);"></div>
    </div>

    <div class="w-full max-w-2xl font-mono sm:mt-10">

      <!-- ============= Loading ============= -->
      <div v-if="previewLoading" class="border border-gray-800 bg-gray-900 p-6 sm:p-8 text-center">
        <div class="inline-block w-6 h-6 border-2 border-emerald-500 border-t-transparent rounded-full animate-spin"></div>
        <div class="mt-3 text-xs tracking-widest text-gray-500 uppercase">verifying invite link</div>
      </div>

      <!-- ============= Invalid / Expired ============= -->
      <div v-else-if="previewErr" class="border border-red-500/60 bg-red-950/30 p-5 sm:p-8">
        <div class="text-red-400 text-3xl leading-none">⨯</div>
        <h1 class="text-lg sm:text-xl text-gray-100 mt-2 leading-snug">Invite link is no longer valid</h1>
        <p class="text-[13px] sm:text-sm text-gray-400 mt-3 leading-relaxed normal-case tracking-normal break-words">
          {{ previewErr }}
        </p>
        <p class="text-[11px] sm:text-xs text-gray-600 mt-4 normal-case tracking-normal leading-relaxed">
          Ask the admin who sent this link to issue a fresh one. Each invite is single-use and expires after 24 hours.
        </p>
      </div>

      <!-- ============= Active invite ============= -->
      <template v-else-if="preview">
        <!-- Header banner -->
        <div class="border border-emerald-500/60 bg-emerald-900/15 p-4 sm:p-6 mb-3 sm:mb-4 relative overflow-hidden">
          <div class="flex items-center gap-2 text-[10px] tracking-widest text-emerald-400 uppercase">
            <span class="w-1.5 h-1.5 bg-emerald-500 animate-pulse shrink-0"></span>
            <span class="truncate">operator invitation · AS64500</span>
          </div>
          <h1 class="mt-2 text-xl sm:text-3xl text-gray-100 leading-snug break-words">
            欢迎加入 <span class="text-emerald-500 whitespace-nowrap">Acme Net</span>
          </h1>
          <p class="mt-3 text-[13px] sm:text-sm text-gray-400 normal-case tracking-normal leading-relaxed">
            <span class="text-emerald-500">{{ preview.invited_by }}</span> 邀请你以
            <span class="text-emerald-500">{{ preview.role }}</span>
            身份加入运维团队。完成下方表单后,你的注册请求会发送给 admin 审批。
          </p>
          <div class="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-[10px] tracking-widest text-gray-600 uppercase font-mono">
            <span>expires in <span class="text-amber-400 tabular-nums">{{ expiresLabel }}</span></span>
            <span class="text-gray-700 hidden sm:inline">·</span>
            <span>single-use link</span>
          </div>
        </div>

        <!-- Stepper -->
        <ol class="flex flex-wrap items-center gap-x-2 gap-y-1 text-[10px] tracking-widest text-gray-600 uppercase mb-3">
          <li :class="stage === 'identity' ? 'text-emerald-500' : 'text-gray-500'">① identity</li>
          <li class="text-gray-700" aria-hidden="true">›</li>
          <li :class="stage === 'mfa' ? 'text-emerald-500' : stage === 'review' || stage === 'submitting' || stage === 'done' ? 'text-gray-500' : 'text-gray-700'">② 2-factor</li>
          <li class="text-gray-700" aria-hidden="true">›</li>
          <li :class="stage === 'review' || stage === 'submitting' ? 'text-emerald-500' : stage === 'done' ? 'text-gray-500' : 'text-gray-700'">③ review</li>
          <li class="text-gray-700" aria-hidden="true">›</li>
          <li :class="stage === 'done' ? 'text-emerald-500' : 'text-gray-700'">✓ done</li>
        </ol>

        <!-- ============= STAGE 1: identity ============= -->
        <div v-if="stage === 'identity'" class="border border-gray-800 bg-gray-900 p-4 sm:p-6 space-y-3.5">
          <div class="text-[10px] tracking-widest text-gray-600 uppercase">① choose your operator identity</div>

          <div>
            <label class="text-[10px] tracking-widest text-gray-600 uppercase">username</label>
            <input
              v-model="username"
              type="text"
              autocomplete="username"
              placeholder="alice"
              spellcheck="false"
              autocapitalize="none"
              autocorrect="off"
              class="mt-1 w-full bg-black border border-gray-800 px-3 py-3 sm:py-2 text-base font-mono text-gray-100 focus:border-emerald-500 focus:outline-none rounded-none"
            />
            <p class="text-[10px] text-gray-600 mt-1 normal-case tracking-normal">2-32 chars · letters / digits / . _ -</p>
          </div>

          <div>
            <label class="text-[10px] tracking-widest text-gray-600 uppercase">password</label>
            <input
              v-model="password"
              type="password"
              autocomplete="new-password"
              class="mt-1 w-full bg-black border border-gray-800 px-3 py-3 sm:py-2 text-base font-mono text-gray-100 focus:border-emerald-500 focus:outline-none rounded-none"
            />
            <p class="text-[10px] text-gray-600 mt-1 normal-case tracking-normal">≥ 8 chars · use a password manager</p>
          </div>

          <div>
            <label class="text-[10px] tracking-widest text-gray-600 uppercase">confirm password</label>
            <input
              v-model="passwordConfirm"
              type="password"
              autocomplete="new-password"
              @keyup.enter="goToMfa"
              class="mt-1 w-full bg-black border border-gray-800 px-3 py-3 sm:py-2 text-base font-mono text-gray-100 focus:border-emerald-500 focus:outline-none rounded-none"
            />
          </div>

          <p v-if="formErr" class="text-xs text-red-400 normal-case tracking-normal">⨯ {{ formErr }}</p>

          <button
            type="button"
            @click="goToMfa"
            :disabled="!canAdvanceIdentity"
            class="w-full mt-2 px-4 py-3 sm:py-2.5 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black active:bg-emerald-500 active:text-black text-xs tracking-widest uppercase transition-colors disabled:opacity-30 disabled:cursor-not-allowed touch-manipulation"
          >continue to 2-factor ›</button>
        </div>

        <!-- ============= STAGE 2: mfa ============= -->
        <div v-else-if="stage === 'mfa'" class="border border-gray-800 bg-gray-900 p-4 sm:p-6 space-y-4">
          <div class="text-[10px] tracking-widest text-gray-600 uppercase">② bind a second factor</div>

          <!-- Method tabs -->
          <div class="flex gap-2 text-[10px] sm:text-xs tracking-widest uppercase">
            <button
              type="button"
              @click="mfaMethod = 'passkey'"
              :disabled="!passkeyAvailable"
              :class="[
                'flex-1 px-3 py-3 sm:py-2 border transition-colors touch-manipulation',
                mfaMethod === 'passkey'
                  ? 'border-emerald-500 text-emerald-500 bg-emerald-500/10'
                  : 'border-gray-800 text-gray-500 hover:border-gray-600 hover:text-gray-300',
                !passkeyAvailable && 'opacity-30 cursor-not-allowed'
              ]"
            >🔑 Passkey</button>
            <button
              type="button"
              @click="mfaMethod = 'totp'"
              :class="[
                'flex-1 px-3 py-3 sm:py-2 border transition-colors touch-manipulation',
                mfaMethod === 'totp'
                  ? 'border-emerald-500 text-emerald-500 bg-emerald-500/10'
                  : 'border-gray-800 text-gray-500 hover:border-gray-600 hover:text-gray-300'
              ]"
            >⏱ TOTP</button>
          </div>

          <!-- TOTP path -->
          <div v-if="mfaMethod === 'totp'" class="space-y-3">
            <div v-if="totpStage === 'pristine'">
              <p class="text-[13px] sm:text-xs text-gray-500 normal-case tracking-normal leading-relaxed">
                任何 Authenticator app —— Authy, 1Password, Bitwarden, Google Authenticator, Microsoft Authenticator 都可以。
              </p>
              <button
                type="button"
                @click="totpBegin"
                class="mt-3 w-full px-4 py-3 sm:py-2 border border-violet-400 text-violet-400 hover:bg-violet-400 hover:text-black active:bg-violet-400 active:text-black text-xs tracking-widest uppercase transition-colors touch-manipulation"
              >▶ generate TOTP secret</button>
            </div>

            <div v-else-if="totpStage === 'enrolling'" class="space-y-3">
              <!-- QR — responsive: scales to viewport on narrow phones, capped at 256px on wider screens -->
              <div class="bg-white p-2 mx-auto w-full" style="max-width: min(256px, calc(100vw - 3rem));">
                <img :src="qrSrc" alt="TOTP QR code" class="block w-full h-auto" />
              </div>
              <div class="text-[10px] text-gray-600 tracking-widest uppercase">manual entry</div>
              <code class="block px-3 py-2 bg-black text-emerald-400 text-[11px] sm:text-xs leading-relaxed select-all break-all border border-gray-800">{{ totpSecret }}</code>
              <p class="text-[13px] sm:text-xs text-gray-500 normal-case tracking-normal">
                扫码后输入当前 6 位数字:
              </p>
              <!-- Code + verify: stack on phones, row on tablets+ -->
              <div class="flex flex-col sm:flex-row gap-2">
                <input
                  v-model="totpCode"
                  type="text"
                  inputmode="numeric"
                  pattern="[0-9]*"
                  maxlength="6"
                  autocomplete="one-time-code"
                  placeholder="000000"
                  @keyup.enter="confirmTotp"
                  class="flex-1 bg-black border border-gray-800 px-3 py-3 sm:py-2 text-2xl sm:text-xl font-mono tracking-[0.4em] tabular-nums text-gray-100 text-center focus:border-emerald-500 focus:outline-none rounded-none"
                />
                <button
                  type="button"
                  @click="confirmTotp"
                  :disabled="totpCode.trim().length < 6"
                  class="px-4 py-3 sm:py-2 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black active:bg-emerald-500 active:text-black text-xs tracking-widest uppercase transition-colors disabled:opacity-30 disabled:cursor-not-allowed touch-manipulation"
                >▶ verify</button>
              </div>
            </div>

            <div v-else-if="totpStage === 'verified'" class="text-xs text-emerald-400 tracking-normal normal-case">
              ✓ TOTP bound · continue to review
            </div>
          </div>

          <!-- Passkey path -->
          <div v-else class="space-y-3">
            <div v-if="!passkeyBound">
              <p class="text-[13px] sm:text-xs text-gray-500 normal-case tracking-normal leading-relaxed">
                绑定 passkey 后,以后从任何设备登录 <span class="text-emerald-400">admin.example.com</span> 都不再需要输入密码 ——
                Touch ID / Face ID / Windows Hello 一下即可。比 TOTP 更安全也更快。
              </p>
              <p v-if="!passkeyAvailable" class="text-[11px] text-amber-400 normal-case tracking-normal mt-2">
                ⚠ 当前浏览器不支持 WebAuthn,请改用 TOTP。
              </p>
              <div class="mt-3 grid grid-cols-1 sm:grid-cols-[1fr_auto] gap-2 items-center">
                <input
                  v-model="passkeyName"
                  type="text"
                  spellcheck="false"
                  autocapitalize="none"
                  :placeholder="defaultPasskeyName()"
                  class="bg-black border border-gray-800 px-3 py-3 sm:py-2 text-sm font-mono text-gray-100 focus:border-emerald-500 focus:outline-none rounded-none"
                />
                <button
                  type="button"
                  @click="bindPasskey"
                  :disabled="passkeyBusy || !passkeyAvailable"
                  class="px-4 py-3 sm:py-2 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black active:bg-emerald-500 active:text-black text-xs tracking-widest uppercase transition-colors disabled:opacity-30 disabled:cursor-not-allowed touch-manipulation whitespace-nowrap"
                >{{ passkeyBusy ? '◌ waiting on authenticator…' : '🔑 register passkey' }}</button>
              </div>
              <p class="text-[10px] text-gray-600 mt-1 normal-case tracking-normal">
                label 用来在 <span class="text-gray-400">/admin/security</span> 区分多个设备 · 留空使用系统默认名
              </p>
            </div>

            <div v-else class="border border-emerald-500/40 bg-emerald-900/10 p-3">
              <div class="flex items-center gap-2 text-sm text-emerald-400 normal-case tracking-normal">
                <span class="text-base">✓</span>
                <span>Passkey 已生成 · <span class="font-mono text-emerald-300">{{ passkeyName || defaultPasskeyName() }}</span></span>
              </div>
              <p class="text-[11px] text-gray-500 mt-1 normal-case tracking-normal">
                凭证保存在你的设备 (或同步到 iCloud / Google / 1Password)。点击下方 review 完成注册。
              </p>
              <div class="flex gap-2 mt-2">
                <button
                  type="button"
                  @click="passkeyBound = false; passkeyChallengeId = ''; passkeyResponse = null"
                  class="px-3 py-1.5 border border-gray-700 hover:border-gray-500 text-gray-400 text-[10px] tracking-widest uppercase transition-colors touch-manipulation"
                >↻ re-register</button>
                <button
                  type="button"
                  @click="stage = 'review'"
                  class="px-3 py-1.5 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black active:bg-emerald-500 active:text-black text-[10px] tracking-widest uppercase transition-colors touch-manipulation"
                >▶ continue to review</button>
              </div>
            </div>
          </div>

          <p v-if="formErr" class="text-xs text-red-400 normal-case tracking-normal">⨯ {{ formErr }}</p>

          <div class="flex gap-2 pt-2">
            <button
              type="button"
              @click="stage = 'identity'"
              class="flex-1 px-4 py-3 sm:py-2 border border-gray-700 hover:border-gray-500 text-gray-400 text-xs tracking-widest uppercase transition-colors touch-manipulation"
            >‹ back</button>
          </div>
        </div>

        <!-- ============= STAGE 3: review ============= -->
        <div v-else-if="stage === 'review' || stage === 'submitting'" class="border border-gray-800 bg-gray-900 p-4 sm:p-6 space-y-4">
          <div class="text-[10px] tracking-widest text-gray-600 uppercase">③ review &amp; send approval request</div>

          <div class="text-[13px] sm:text-sm space-y-2 font-mono">
            <div class="flex justify-between items-baseline gap-3">
              <span class="text-gray-500 shrink-0">username</span>
              <span class="text-gray-100 break-all text-right">{{ username.trim() }}</span>
            </div>
            <div class="flex justify-between items-baseline gap-3">
              <span class="text-gray-500 shrink-0">role</span>
              <span class="text-emerald-500 text-right">{{ preview.role }}</span>
            </div>
            <div class="flex justify-between items-baseline gap-3">
              <span class="text-gray-500 shrink-0">invited by</span>
              <span class="text-gray-100 break-all text-right">{{ preview.invited_by }}</span>
            </div>
            <div class="flex justify-between items-baseline gap-3">
              <span class="text-gray-500 shrink-0">2-factor</span>
              <span class="text-emerald-500 text-right">{{ mfaMethod === 'totp' ? '⏱ TOTP bound' : '🔑 Passkey' }}</span>
            </div>
          </div>

          <p class="text-[13px] sm:text-xs text-gray-500 normal-case tracking-normal leading-relaxed">
            点 ▶ confirm 后,你的账户会以 <span class="text-amber-400">pending</span> 状态创建,
            <span class="text-emerald-500">{{ preview.invited_by }}</span> 会在 admin 后台收到批准请求。批准后你才能登录 <span class="text-emerald-400">admin.example.com</span>。
          </p>

          <p v-if="formErr" class="text-xs text-red-400 normal-case tracking-normal">⨯ {{ formErr }}</p>

          <!-- Action row — confirm is the primary action so it stacks on top on mobile (thumb-friendly) -->
          <div class="flex flex-col-reverse sm:flex-row gap-2">
            <button
              type="button"
              @click="stage = 'mfa'"
              :disabled="submitBusy"
              class="sm:flex-1 px-4 py-3 sm:py-2 border border-gray-700 hover:border-gray-500 text-gray-400 text-xs tracking-widest uppercase transition-colors disabled:opacity-50 touch-manipulation"
            >‹ back</button>
            <button
              type="button"
              @click="submitRegistration"
              :disabled="submitBusy"
              class="sm:flex-1 px-4 py-3 sm:py-2 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black active:bg-emerald-500 active:text-black text-xs tracking-widest uppercase transition-colors disabled:opacity-30 disabled:cursor-not-allowed touch-manipulation"
            >{{ submitBusy ? '◌ submitting' : '▶ confirm · send approval' }}</button>
          </div>
        </div>

        <!-- ============= STAGE 4: done ============= -->
        <div v-else-if="stage === 'done' && submitResult" class="space-y-3 sm:space-y-4">
          <div class="border border-emerald-500/60 bg-emerald-900/15 p-4 sm:p-6">
            <div class="text-emerald-500 text-3xl leading-none">✓</div>
            <h2 class="text-lg sm:text-xl text-gray-100 mt-2 leading-snug break-words">
              注册请求已送达 <span class="text-emerald-500 break-all">{{ submitResult.invited_by }}</span>
            </h2>
            <p class="text-[13px] sm:text-sm text-gray-400 mt-3 normal-case tracking-normal leading-relaxed">
              你的账户 <span class="text-emerald-500 font-mono break-all">{{ submitResult.username }}</span>
              已经创建,目前处于 <span class="text-amber-400">pending</span> 状态。
              admin 批准后,你就可以从
              <a class="text-emerald-400 hover:text-emerald-300 underline break-all" href="https://admin.example.com/login">admin.example.com/login</a>
              登录。
            </p>
          </div>

          <!-- Recovery codes — one-time display -->
          <div class="border border-red-500/60 bg-red-950/20 p-4 sm:p-6">
            <div class="flex items-center justify-between flex-wrap gap-2 mb-2">
              <span class="text-[10px] tracking-widest text-red-400 uppercase font-mono leading-snug">
                ⚠ ONE-TIME DISPLAY · save these codes now
              </span>
            </div>
            <p class="text-[13px] sm:text-xs text-gray-500 normal-case tracking-normal leading-relaxed mb-3">
              如果你将来忘了密码,这些恢复码各能用一次。<strong class="text-gray-300">离开这个页面后不会再显示</strong>。
              复制到密码管理器或离线纸条:
            </p>
            <div class="grid grid-cols-2 sm:grid-cols-5 gap-x-4 gap-y-2 text-[13px] sm:text-xs font-mono text-emerald-400 select-all tabular-nums">
              <span v-for="c in submitResult.recovery_codes" :key="c" class="break-all">{{ c }}</span>
            </div>
          </div>

          <div class="text-[11px] sm:text-xs text-gray-600 normal-case tracking-normal text-center leading-relaxed px-2">
            你可以关闭这个页面。我们不会再发邮件提醒;请等 admin 在内部告诉你已批准。
          </div>
        </div>

      </template>

    </div>
  </div>
</template>
