<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from '@/api/client'
import { useSessionStore } from '@/stores/session'
import {
  decodeCreationOptions, encodeCreationResponse, isWebAuthnSupported
} from '@/utils/webauthn'

const route = useRoute()
const router = useRouter()
const session = useSessionStore()
const passkeySupported = isWebAuthnSupported()

// Decide where to send the operator once MFA is satisfied. Respect ?next=
// if present, otherwise default to the dashboard.
function continueAfterEnrollment() {
  const next = (route.query.next as string) || '/admin/dashboard'
  // refresh /me so the guard sees mfa_required=false
  session.fetchMe().then(() => router.replace(next))
}

// ---- Passkey path ----
const passkeyBusy = ref(false)
const passkeyErr  = ref<string | null>(null)
const passkeyName = ref('')

async function enrollPasskey() {
  if (passkeyBusy.value) return
  passkeyBusy.value = true
  passkeyErr.value = null
  try {
    const begin = await api.passkeyRegBegin()
    if (!begin.ok || !begin.data) throw new Error(begin.error ?? 'begin failed')
    const options = decodeCreationOptions(begin.data.options)
    const cred = await navigator.credentials.create(options) as PublicKeyCredential | null
    if (!cred) throw new Error('cancelled')
    const name = passkeyName.value.trim() || `passkey · ${new Date().toLocaleString('en-GB', { hour12: false })}`
    const finish = await api.passkeyRegFinish(begin.data.challenge_id, name, encodeCreationResponse(cred))
    if (!finish.ok) throw new Error(finish.error ?? 'verify failed')
    continueAfterEnrollment()
  } catch (e) {
    passkeyErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    passkeyBusy.value = false
  }
}

// ---- TOTP path ----
const totpBusy = ref(false)
const totpErr  = ref<string | null>(null)
const totpSecret  = ref<string>('')
const totpOtpauth = ref<string>('')
const totpCode    = ref<string>('')
const totpStage   = ref<'idle' | 'enroll'>('idle')

async function totpBegin() {
  if (totpBusy.value) return
  totpBusy.value = true
  totpErr.value = null
  try {
    const env = await api.totpSetupBegin()
    if (!env.ok || !env.data) throw new Error(env.error ?? 'begin failed')
    totpSecret.value = env.data.secret
    totpOtpauth.value = env.data.otpauth
    totpStage.value = 'enroll'
  } catch (e) {
    totpErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    totpBusy.value = false
  }
}

async function totpConfirm() {
  if (totpBusy.value) return
  if (totpCode.value.length < 6) { totpErr.value = 'enter the 6-digit code'; return }
  totpBusy.value = true
  totpErr.value = null
  try {
    const env = await api.totpSetupConfirm(totpSecret.value, totpCode.value.trim())
    if (!env.ok) throw new Error(env.error ?? 'verify failed')
    continueAfterEnrollment()
  } catch (e) {
    totpErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    totpBusy.value = false
  }
}

// Build a QR code URL using a tiny public service that takes the otpauth://
// URI and renders an SVG. We use api.qrserver.com (no auth, generous CORS,
// stable since 2010) and pass-through to <img>; the data itself stays
// client-side (browser → qrserver) — we don't put the secret through any
// of our servers.
const qrSrc = computed(() => {
  if (!totpOtpauth.value) return ''
  return `https://api.qrserver.com/v1/create-qr-code/?size=240x240&margin=8&data=${encodeURIComponent(totpOtpauth.value)}`
})

onMounted(() => {
  // If the operator landed here directly but actually has MFA already,
  // bounce them out — router guard normally handles this but a hard
  // reload can land us here transiently.
  if (!session.mfaRequired) {
    router.replace('/admin/dashboard')
  }
})
</script>

<template>
  <div class="space-y-4 max-w-3xl mx-auto">
    <!-- Header -->
    <div class="border border-amber-500/60 bg-amber-900/15 p-4">
      <div class="flex items-center gap-3 mb-2">
        <span class="w-1.5 h-1.5 bg-amber-400 animate-pulse"></span>
        <h1 class="text-sm tracking-[0.2em] uppercase text-amber-300">第一次登录 · MFA 绑定</h1>
      </div>
      <p class="text-sm text-gray-300 normal-case tracking-normal leading-relaxed">
        欢迎 <span class="text-emerald-500 font-mono">{{ session.operator }}</span>。
        在进入控制台之前，必须先为账户绑定至少 <b>一种</b> 二次验证方式 —— 任选其一即可：
        <span class="text-emerald-400">Passkey</span>（推荐：浏览器 / 系统钥匙串 / YubiKey）
        或 <span class="text-emerald-400">TOTP</span>（任何 Authenticator app）。
      </p>
      <p class="mt-2 text-xs text-amber-200/80 normal-case tracking-normal">
        这一步是强制的，绑定完成后立即跳转到原本要去的页面。
        想登出从右上角"sign out"。
      </p>
    </div>

    <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
      <!-- =================== Passkey =================== -->
      <div class="border border-gray-800 bg-gray-900 p-4 flex flex-col">
        <div class="flex items-center gap-2 mb-1">
          <span class="text-emerald-500 text-lg">🔑</span>
          <h2 class="text-sm tracking-widest uppercase text-gray-100">Passkey (推荐)</h2>
        </div>
        <p class="text-xs text-gray-500 normal-case tracking-normal leading-relaxed mb-3">
          浏览器系统对话框引导,选 Google Password Manager / iCloud Keychain / Bitwarden / Touch ID / Face ID / YubiKey 等已知 authenticator。无需输入,凭设备本身验证。
        </p>

        <input
          v-model="passkeyName"
          :disabled="!passkeySupported || passkeyBusy"
          placeholder="optional · device label (e.g. 'MacBook Touch ID')"
          class="mb-2 w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-500 focus:outline-none disabled:opacity-50"
        />

        <button
          @click="enrollPasskey"
          :disabled="!passkeySupported || passkeyBusy"
          class="w-full px-4 py-2 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-xs tracking-widest uppercase transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
        >{{ passkeyBusy ? '◌ AWAITING DEVICE' : '▶ REGISTER PASSKEY' }}</button>

        <p v-if="!passkeySupported" class="mt-2 text-xs text-red-400 normal-case tracking-normal">
          ⨯ This browser doesn't support WebAuthn. Use the TOTP path on the right.
        </p>
        <p v-if="passkeyErr" class="mt-2 text-xs text-red-400 normal-case tracking-normal break-all">
          ⨯ {{ passkeyErr }}
        </p>
      </div>

      <!-- =================== TOTP =================== -->
      <div class="border border-gray-800 bg-gray-900 p-4 flex flex-col">
        <div class="flex items-center gap-2 mb-1">
          <span class="text-violet-400 text-lg">⏱</span>
          <h2 class="text-sm tracking-widest uppercase text-gray-100">TOTP</h2>
        </div>
        <p class="text-xs text-gray-500 normal-case tracking-normal leading-relaxed mb-3">
          任何 Authenticator app —— Authy, 1Password, Google Authenticator, Microsoft Authenticator, Bitwarden 等都可。
        </p>

        <!-- Stage 1: ask user to start enrollment -->
        <button
          v-if="totpStage === 'idle'"
          @click="totpBegin"
          :disabled="totpBusy"
          class="w-full px-4 py-2 border border-violet-400 text-violet-400 hover:bg-violet-400 hover:text-black text-xs tracking-widest uppercase transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
        >{{ totpBusy ? '◌ ...' : '▶ SET UP TOTP' }}</button>

        <!-- Stage 2: show QR + secret + ask for confirmation code -->
        <div v-else class="space-y-3">
          <!-- QR card caps at 256px on desktop, but on narrow phones
               (320px viewport, with ~3rem page padding) the fixed
               256px overflowed the safe area. Clamp via `calc` so the
               card shrinks to fit available width while keeping the
               desktop look. -->
          <div class="bg-white p-2 mx-auto" style="width: 100%; max-width: min(256px, calc(100vw - 3rem));">
            <img :src="qrSrc" alt="TOTP QR code" class="block w-full" />
          </div>

          <div class="text-[10px] text-gray-600 tracking-widest uppercase">manual entry</div>
          <code class="block px-3 py-2 bg-black text-emerald-400 text-xs select-all break-all border border-gray-800">{{ totpSecret }}</code>

          <p class="text-xs text-gray-500 normal-case tracking-normal leading-relaxed">
            扫描二维码 (或手动输入 secret) 添加到 authenticator,然后输入它显示的 6 位数字:
          </p>

          <div class="flex gap-2">
            <input
              v-model="totpCode"
              type="text"
              inputmode="numeric"
              maxlength="6"
              autocomplete="one-time-code"
              :disabled="totpBusy"
              placeholder="000000"
              @keyup.enter="totpConfirm"
              class="flex-1 bg-black border border-gray-800 px-3 py-2 text-lg font-mono tracking-[0.4em] tabular-nums text-gray-100 text-center focus:border-emerald-500 focus:outline-none disabled:opacity-50"
            />
            <button
              @click="totpConfirm"
              :disabled="totpBusy || totpCode.length < 6"
              class="px-4 py-2 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-xs tracking-widest uppercase transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
            >{{ totpBusy ? '◌' : '▶ verify' }}</button>
          </div>
        </div>

        <p v-if="totpErr" class="mt-2 text-xs text-red-400 normal-case tracking-normal break-all">
          ⨯ {{ totpErr }}
        </p>
      </div>
    </div>

    <!-- Footer hint -->
    <div class="border border-gray-800 bg-gray-900/40 p-3 text-[10px] tracking-widest text-gray-600 uppercase normal-case tracking-normal">
      绑定后此页不再出现。想之后管理或换设备,运维面板的"安全设置 · Security"里可以注册多个 passkey、查看恢复码。
    </div>
  </div>
</template>
