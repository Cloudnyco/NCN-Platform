import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api } from '@/api/client'

export interface MeData {
  operator: string
  role: string
  issued_at: number
  expires_at: number
  session_id: string
  ttl_seconds: number
  mfa_required: boolean
  has_passkey: boolean
  has_totp: boolean
  avatar_url?: string
  external_identities?: Array<{ provider: string; email?: string; bound_at?: string }>
}

export const useSessionStore = defineStore('session', () => {
  // Identity
  const operator = ref<string>('')
  const role = ref<string>('')
  const sessionId = ref<string>('')
  const issuedAt = ref<number>(0)
  const expiresAt = ref<number>(0)
  const authenticated = ref<boolean>(false)
  const checked = ref<boolean>(false)
  // MFA state — when true, router guard forces user to /admin/onboarding
  const mfaRequired = ref<boolean>(false)
  const hasPasskey = ref<boolean>(false)
  const hasTotp = ref<boolean>(false)
  const avatarUrl = ref<string>('') // operator profile picture (from OAuth), '' → initials fallback

  // Operational metadata (kept stable for header chrome)
  const asn = ref<string>('AS64500')
  const systemOnline = ref<boolean>(true)

  // Derived
  const ttlSeconds = computed(() => Math.max(0, expiresAt.value - Math.floor(Date.now() / 1000)))

  function applyMe(m: MeData) {
    operator.value = m.operator
    role.value = m.role || ''
    sessionId.value = m.session_id
    issuedAt.value = m.issued_at
    expiresAt.value = m.expires_at
    mfaRequired.value = !!m.mfa_required
    hasPasskey.value = !!m.has_passkey
    hasTotp.value = !!m.has_totp
    avatarUrl.value = m.avatar_url || ''
    authenticated.value = true
  }

  function clear() {
    operator.value = ''
    role.value = ''
    sessionId.value = ''
    issuedAt.value = 0
    expiresAt.value = 0
    mfaRequired.value = false
    hasPasskey.value = false
    hasTotp.value = false
    avatarUrl.value = ''
    authenticated.value = false
  }

  async function fetchMe(): Promise<boolean> {
    try {
      const env = await api.authMe()
      if (env.ok && env.data) {
        applyMe(env.data)
      } else {
        clear()
      }
    } catch {
      clear()
    } finally {
      checked.value = true
    }
    return authenticated.value
  }

  // Step 1 — submit username + password. Returns whether the server
  // needs a TOTP step next ({ needsTotp: true }) or already issued a
  // session ({ needsTotp: false }). The TOTP-needed branch is handled
  // by calling verifyTOTP() below.
  async function login(username: string, password: string, turnstileToken?: string): Promise<{ needsTotp: boolean }> {
    const env = await api.authLogin({ username, password, turnstile_token: turnstileToken })
    if (!env.ok) {
      throw new Error(env.error ?? 'authentication failed')
    }
    if (env.data?.totp_required) {
      return { needsTotp: true }
    }
    // Session already issued — pull /me.
    await fetchMe()
    return { needsTotp: false }
  }

  // Step 2 — finish the password-path login by submitting the TOTP code
  // and a "trust this device" preference. The server consumes the
  // intent ticket cookie and issues the session.
  async function verifyTOTP(totpCode: string, trustDevice: boolean) {
    const env = await api.authLoginVerifyTOTP({ totp_code: totpCode, trust_device: trustDevice })
    if (!env.ok) {
      throw new Error(env.error ?? 'authentication failed')
    }
    await fetchMe()
  }

  async function logout() {
    try { await api.authLogout() } catch { /* swallow */ }
    clear()
  }

  return {
    // identity
    operator, role, sessionId, issuedAt, expiresAt, authenticated, checked,
    mfaRequired, hasPasskey, hasTotp, avatarUrl,
    // chrome
    asn, systemOnline,
    // derived
    ttlSeconds,
    // ops
    fetchMe, login, verifyTOTP, logout, clear
  }
})
