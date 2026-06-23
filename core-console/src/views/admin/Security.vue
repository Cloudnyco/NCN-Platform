<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  api, type PasskeyRecord, type OperatorListItem, type OperatorCreateResult,
  type InviteRecord, type InviteCreateResult, type TrustedDevice,
  type SSHKeyRecord, type APITokenRecord, type APITokenCreateResult,
  type OAuthIdentity
} from '@/api/client'
import { useSessionStore } from '@/stores/session'
import {
  decodeCreationOptions, encodeCreationResponse, isWebAuthnSupported
} from '@/utils/webauthn'
import Audit from '@/views/admin/Audit.vue'
import RoleMailboxRecovery from '@/components/admin/security/RoleMailboxRecovery.vue'
import ForgotPasswordQueue from '@/components/admin/security/ForgotPasswordQueue.vue'
import { copyToClipboard } from '@/utils/clipboard'

const { t } = useI18n()
const session = useSessionStore()
const isAdmin = computed(() => session.role === 'admin')

// ─── Tab navigation (URL-synced) ─────────────────────────────────────────
// The Security page used to be a single tall column of 11 sections. We
// chunk it into three semantically distinct groups: personal account
// credentials, team management (admin-only — operators, invites, all the
// admin queues + role-mailbox recovery utility), and audit log
// (admin-only). The active tab lives in the URL (`?tab=…`) so back/forward
// and bookmarks work; if a non-admin operator URL-hacks their way onto a
// restricted tab we silently fall back to "account".
//
// Earlier versions had a separate 'recovery' tab for role-mailbox + the
// forgot-password queue. Those got folded into 'team' in #289 — the
// queues belong with the other admin queues, and the role-mailbox utility
// is admin-org-management same as operators/invites. `?tab=recovery` URLs
// from old bookmarks are silently rewritten to `?tab=team` in
// tabFromQuery() below.
type SecTab = 'account' | 'team' | 'audit'
const SECURE_TABS: readonly SecTab[] = ['account', 'team', 'audit']
const ADMIN_TABS: readonly SecTab[] = ['team', 'audit']
// Tab IDs visible to the current user. Template iterates this directly
// — keeping the `as const` literal out of the template avoids the
// "TS-syntax in v-for blows up render function" trap. See
// feedback_vue_template_no_as_const.md.
const VISIBLE_TABS = computed<readonly SecTab[]>(() =>
  isAdmin.value ? SECURE_TABS : (['account'] as const)
)
const route  = useRoute()
const router = useRouter()
function tabFromQuery(): SecTab {
  const raw = (route.query.tab as string) || 'account'
  // Back-compat: pre-#289 'recovery' tab is now part of 'team'.
  const t = raw === 'recovery' ? 'team' : raw
  const tab = (SECURE_TABS as string[]).includes(t) ? (t as SecTab) : 'account'
  if (!isAdmin.value && ADMIN_TABS.includes(tab)) return 'account'
  return tab
}
const activeTab = ref<SecTab>(tabFromQuery())
watch(() => route.query.tab, () => (activeTab.value = tabFromQuery()))
function setTab(t: SecTab) {
  if (activeTab.value === t) return
  activeTab.value = t
  router.replace({ query: { ...route.query, tab: t } })
}

const passkeys = ref<PasskeyRecord[]>([])
const loading = ref(true)
const err = ref<string | null>(null)
const passkeySupported = isWebAuthnSupported()

const recoveryRemaining = ref<number | null>(null)

const registering = ref(false)
const newName = ref('')
const lastAdded = ref<string | null>(null)

// ---- TOTP enrollment (for operators who never bound one or want to add it
// alongside an existing passkey). Mirrors the Onboarding flow but uses the
// current session's `hasTotp` to decide whether to even show the form.
const totpBusy   = ref(false)
const totpErr    = ref<string | null>(null)
const totpSecret  = ref<string>('')
const totpOtpauth = ref<string>('')
const totpCode    = ref<string>('')
const totpStage   = ref<'idle' | 'enroll' | 'done'>('idle')
const totpQrSrc = computed(() =>
  totpOtpauth.value
    ? `https://api.qrserver.com/v1/create-qr-code/?size=240x240&margin=8&data=${encodeURIComponent(totpOtpauth.value)}`
    : ''
)

async function totpBegin() {
  if (totpBusy.value) return
  totpBusy.value = true
  totpErr.value = null
  try {
    const env = await api.totpSetupBegin()
    if (!env.ok || !env.data) throw new Error(env.error ?? 'begin failed')
    totpSecret.value  = env.data.secret
    totpOtpauth.value = env.data.otpauth
    totpCode.value    = ''
    totpStage.value   = 'enroll'
  } catch (e) {
    totpErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    totpBusy.value = false
  }
}

async function totpConfirm() {
  if (totpBusy.value) return
  if (totpCode.value.trim().length < 6) {
    totpErr.value = 'enter the 6-digit code your authenticator shows'
    return
  }
  totpBusy.value = true
  totpErr.value = null
  try {
    const env = await api.totpSetupConfirm(totpSecret.value, totpCode.value.trim())
    if (!env.ok) throw new Error(env.error ?? 'verify failed')
    totpStage.value = 'done'
    // Refresh /me so the new has_totp lights up the badge in the operators
    // table + flips off any future MFA-required gate.
    await session.fetchMe()
    setTimeout(() => {
      totpStage.value = 'idle'
      totpSecret.value = ''
      totpOtpauth.value = ''
      totpCode.value = ''
    }, 2000)
  } catch (e) {
    totpErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    totpBusy.value = false
  }
}

function totpCancel() {
  totpStage.value = 'idle'
  totpSecret.value = ''
  totpOtpauth.value = ''
  totpCode.value = ''
  totpErr.value = null
}

// ---- Change password ----
const cpCurrent = ref('')
const cpNew = ref('')
const cpConfirm = ref('')
const cpSubmitting = ref(false)
const cpErr = ref<string | null>(null)
const cpOk = ref<string | null>(null)

const cpCanSubmit = computed(() =>
  cpCurrent.value.length > 0 &&
  cpNew.value.length >= 8 &&
  cpNew.value === cpConfirm.value &&
  cpNew.value !== cpCurrent.value &&
  !cpSubmitting.value
)

async function submitChangePassword() {
  if (!cpCanSubmit.value) return
  cpSubmitting.value = true
  cpErr.value = null
  cpOk.value = null
  try {
    const env = await api.authChangePassword(cpCurrent.value, cpNew.value)
    if (!env.ok || !env.data) {
      cpErr.value = env.error ?? 'change failed'
      return
    }
    cpOk.value = `✓ updated for ${env.data.operator}`
    cpCurrent.value = ''
    cpNew.value = ''
    cpConfirm.value = ''
  } catch (e) {
    cpErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    cpSubmitting.value = false
  }
}

// ---- Operator management ----
const operators = ref<OperatorListItem[]>([])
const opsLoading = ref(false)
const opsErr = ref<string | null>(null)
// Derived: pending approvals + approved
const pendingApprovals = computed(() => operators.value.filter(o => !o.approved))
const activeOperators  = computed(() => operators.value.filter(o => o.approved))

// ---- Invites ----
const invites = ref<InviteRecord[]>([])
const invitesLoading = ref(false)
const invitesErr = ref<string | null>(null)
const lastInvite = ref<InviteCreateResult | null>(null)
const inviteBusy = ref(false)
// New: admin types invitee's email + optional display name before clicking
// "Create + send invite". Backend dispatches the noreply email; admin no
// longer has to copy-paste a URL.
const inviteForm = ref({ email: '', name: '' })
const resendBusy = ref<Record<string, boolean>>({})

async function refreshInvites() {
  if (!isAdmin.value) return
  invitesLoading.value = true
  try {
    const env = await api.invitesList()
    if (env.ok && env.data) invites.value = env.data
    else invitesErr.value = env.error ?? 'failed to load invites'
  } catch (e) {
    invitesErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    invitesLoading.value = false
  }
}

async function generateInvite() {
  if (inviteBusy.value) return
  const email = inviteForm.value.email.trim()
  if (!email) {
    invitesErr.value = t('security.invite.error_email_required')
    return
  }
  inviteBusy.value = true
  invitesErr.value = null
  try {
    const env = await api.invitesCreate('operator', { invitee_email: email, invitee_name: inviteForm.value.name.trim() })
    if (!env.ok || !env.data) {
      invitesErr.value = env.error ?? 'create failed'
      return
    }
    lastInvite.value = env.data
    // Clear the form on success so the admin can immediately invite the next person.
    inviteForm.value = { email: '', name: '' }
    await refreshInvites()
  } catch (e) {
    invitesErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    inviteBusy.value = false
  }
}

async function resendInvite(prefix: string) {
  const clean = prefix.replace(/…$/, '')
  if (resendBusy.value[clean]) return
  resendBusy.value = { ...resendBusy.value, [clean]: true }
  try {
    const env = await api.invitesResend(clean)
    if (!env.ok) {
      invitesErr.value = env.error ?? 'resend failed'
      return
    }
    await refreshInvites()
  } catch (e) {
    invitesErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    resendBusy.value = { ...resendBusy.value, [clean]: false }
  }
}

async function revokeInvite(prefix: string) {
  const clean = prefix.replace(/…$/, '')
  if (!confirm(t('security.invite.confirm_revoke', { prefix: clean }))) return
  try {
    const env = await api.invitesRevoke(clean)
    if (!env.ok) {
      invitesErr.value = env.error ?? 'revoke failed'
      return
    }
    await refreshInvites()
  } catch (e) {
    invitesErr.value = e instanceof Error ? e.message : String(e)
  }
}

async function copyInviteUrl(url: string) {
  try {
    await navigator.clipboard.writeText(url)
  } catch {
    // older browsers fallback
    const ta = document.createElement('textarea')
    ta.value = url
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    document.body.removeChild(ta)
  }
}

function dismissLastInvite() { lastInvite.value = null }

// ---- Operator-self webmail mailbox ----
const mailSelfBusy   = ref(false)
const mailSelfErr    = ref<string | null>(null)
const lastMailInvite = ref<{ url: string; expires_at: string; operator: string } | null>(null)

async function requestMyMailbox() {
  if (mailSelfBusy.value) return
  mailSelfBusy.value = true
  mailSelfErr.value = null
  try {
    const env = await api.mailSelfInvite()
    if (!env.ok || !env.data) {
      mailSelfErr.value = env.error ?? 'request failed'
      return
    }
    lastMailInvite.value = env.data
  } catch (e) {
    mailSelfErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    mailSelfBusy.value = false
  }
}

function dismissMailInvite() { lastMailInvite.value = null }

// ---- Approvals ----
async function approveOperator(o: OperatorListItem) {
  if (!confirm(`Approve operator "${o.username}"? They'll be able to log in immediately.`)) return
  try {
    const env = await api.operatorsApprove(o.username)
    if (!env.ok) {
      opsErr.value = env.error ?? 'approve failed'
      return
    }
    await refreshOperators()
  } catch (e) {
    opsErr.value = e instanceof Error ? e.message : String(e)
  }
}

async function rejectOperator(o: OperatorListItem) {
  if (!confirm(`Reject + DELETE "${o.username}"? They'll have to be re-invited.`)) return
  await deleteOperator(o)
}
// Create form
const newOpUsername = ref('')
const newOpRole = ref<'operator' | 'admin'>('operator')
const newOpBusy = ref(false)
// One-time display of created credentials
const newOpCredentials = ref<OperatorCreateResult | null>(null)

async function refreshOperators() {
  opsLoading.value = true
  opsErr.value = null
  try {
    const env = await api.operatorsList()
    if (env.ok && env.data) operators.value = env.data
    else opsErr.value = env.error ?? 'failed to load operators'
  } catch (e) {
    opsErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    opsLoading.value = false
  }
}

async function createOperator() {
  if (newOpBusy.value) return
  const username = newOpUsername.value.trim()
  if (!username) return
  newOpBusy.value = true
  opsErr.value = null
  newOpCredentials.value = null
  try {
    const env = await api.operatorsCreate({ username, role: newOpRole.value })
    if (!env.ok || !env.data) {
      opsErr.value = env.error ?? 'create failed'
      return
    }
    newOpCredentials.value = env.data
    newOpUsername.value = ''
    newOpRole.value = 'operator'
    await refreshOperators()
  } catch (e) {
    opsErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    newOpBusy.value = false
  }
}

async function deleteOperator(o: OperatorListItem) {
  if (o.username === session.operator) {
    alert("You can't delete your own account.")
    return
  }
  if (!confirm(`Delete operator "${o.username}"? This is irreversible.`)) return
  try {
    const env = await api.operatorsDelete(o.username)
    if (!env.ok) {
      opsErr.value = env.error ?? 'delete failed'
      return
    }
    await refreshOperators()
  } catch (e) {
    opsErr.value = e instanceof Error ? e.message : String(e)
  }
}

async function updateOperatorRole(o: OperatorListItem, role: 'admin' | 'operator') {
  if (role === o.role) return
  if (o.username === session.operator && o.role === 'admin' && role !== 'admin') {
    if (!confirm("You're demoting yourself from admin. You'll lose the operator-management section. Proceed?")) return
  }
  try {
    const env = await api.operatorsUpdate({ username: o.username, role })
    if (!env.ok) {
      opsErr.value = env.error ?? 'update failed'
      return
    }
    await refreshOperators()
    if (o.username === session.operator) await session.fetchMe()
  } catch (e) {
    opsErr.value = e instanceof Error ? e.message : String(e)
  }
}

function dismissCredentials() { newOpCredentials.value = null }

async function refresh() {
  loading.value = true
  err.value = null
  try {
    const [pkEnv, rcEnv, opsEnv] = await Promise.all([
      api.passkeyList(),
      api.authRecoveryStatus(),
      api.operatorsList()
    ])
    if (pkEnv.ok && pkEnv.data) passkeys.value = pkEnv.data
    else err.value = pkEnv.error ?? 'failed to load passkeys'
    if (rcEnv.ok && rcEnv.data) recoveryRemaining.value = rcEnv.data.remaining
    if (opsEnv.ok && opsEnv.data) operators.value = opsEnv.data
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}
// ============ Trusted devices ============
// List of browsers the operator has marked "trust this device" on. The
// current browser is flagged via the `current` boolean from the server,
// which compares the device-trust cookie against each entry's bcrypt
// hash. Revoking the current row also clears the cookie locally.
const trustedDevices = ref<TrustedDevice[]>([])
const devicesLoading = ref(false)
const devicesErr = ref<string | null>(null)
const revoking = ref<string | null>(null)

async function loadTrustedDevices() {
  devicesLoading.value = true
  devicesErr.value = null
  try {
    const env = await api.devicesList()
    if (!env.ok) throw new Error(env.error ?? 'load failed')
    trustedDevices.value = env.data?.devices ?? []
  } catch (e: unknown) {
    devicesErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    devicesLoading.value = false
  }
}

async function revokeTrustedDevice(id: string) {
  if (revoking.value) return
  revoking.value = id
  devicesErr.value = null
  try {
    const env = await api.devicesRevoke(id)
    if (!env.ok) throw new Error(env.error ?? 'revoke failed')
    await loadTrustedDevices()
  } catch (e: unknown) {
    devicesErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    revoking.value = null
  }
}

function fmtAgo(epoch: number): string {
  if (!epoch) return '—'
  const s = Math.floor(Date.now() / 1000 - epoch)
  if (s < 60)        return `${s}s ago`
  if (s < 3600)      return `${Math.floor(s / 60)}m ago`
  if (s < 86_400)    return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}

// ── SSH keys (per-operator, any role) ──────────────────────────────────────
const sshKeys = ref<SSHKeyRecord[]>([])
const sshKeyForm = ref({ label: '', public_key: '' })
const sshKeyBusy = ref(false)
const sshKeyErr = ref<string | null>(null)
async function loadSSHKeys() {
  try {
    const env = await api.sshKeysList()
    if (env.ok && env.data) sshKeys.value = env.data
  } catch (e) {
    sshKeyErr.value = e instanceof Error ? e.message : String(e)
  }
}
async function addSSHKey() {
  if (sshKeyBusy.value) return
  const label = sshKeyForm.value.label.trim()
  const pub = sshKeyForm.value.public_key.trim()
  if (!label || !pub) {
    sshKeyErr.value = t('security.ssh_keys.error_required')
    return
  }
  sshKeyBusy.value = true
  sshKeyErr.value = null
  try {
    const env = await api.sshKeyAdd(label, pub)
    if (!env.ok) {
      sshKeyErr.value = env.error ?? 'add failed'
      return
    }
    sshKeyForm.value = { label: '', public_key: '' }
    await loadSSHKeys()
  } catch (e) {
    sshKeyErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    sshKeyBusy.value = false
  }
}
async function removeSSHKey(k: SSHKeyRecord) {
  if (!confirm(t('security.ssh_keys.confirm_remove', { label: k.label }))) return
  try {
    const env = await api.sshKeyDelete(k.id)
    if (!env.ok) {
      sshKeyErr.value = env.error ?? 'remove failed'
      return
    }
    await loadSSHKeys()
  } catch (e) {
    sshKeyErr.value = e instanceof Error ? e.message : String(e)
  }
}

// ── API tokens (per-operator, any role) ────────────────────────────────────
const apiTokens = ref<APITokenRecord[]>([])
const apiTokenForm = ref({ label: '', expires_in_days: 0 })
const apiTokenBusy = ref(false)
const apiTokenErr = ref<string | null>(null)
// `lastNewToken` is the just-created plaintext shown ONCE — the server
// never returns it again, so dismissing this banner is irreversible.
const lastNewToken = ref<APITokenCreateResult | null>(null)
async function loadAPITokens() {
  try {
    const env = await api.apiTokensList()
    if (env.ok && env.data) apiTokens.value = env.data
  } catch (e) {
    apiTokenErr.value = e instanceof Error ? e.message : String(e)
  }
}
async function createAPIToken() {
  if (apiTokenBusy.value) return
  const label = apiTokenForm.value.label.trim()
  if (!label) {
    apiTokenErr.value = t('security.api_tokens.error_required')
    return
  }
  const days = apiTokenForm.value.expires_in_days
  const expIn = days > 0 ? days * 86400 : 0
  apiTokenBusy.value = true
  apiTokenErr.value = null
  try {
    const env = await api.apiTokenCreate(label, expIn)
    if (!env.ok || !env.data) {
      apiTokenErr.value = env.error ?? 'create failed'
      return
    }
    lastNewToken.value = env.data
    apiTokenForm.value = { label: '', expires_in_days: 0 }
    await loadAPITokens()
  } catch (e) {
    apiTokenErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    apiTokenBusy.value = false
  }
}
async function revokeAPIToken(tok: APITokenRecord) {
  if (!confirm(t('security.api_tokens.confirm_revoke', { label: tok.label }))) return
  try {
    const env = await api.apiTokenDelete(tok.id)
    if (!env.ok) {
      apiTokenErr.value = env.error ?? 'revoke failed'
      return
    }
    await loadAPITokens()
  } catch (e) {
    apiTokenErr.value = e instanceof Error ? e.message : String(e)
  }
}
function dismissLastNewToken() { lastNewToken.value = null }

// ── OAuth / connected accounts (per-operator self-service) ──────────────────
const oauthIdentities = ref<OAuthIdentity[]>([])
const oauthProvidersEnabled = ref<string[]>([])
const oauthErr = ref('')
const oauthBindBusy = ref('')
const oauthProviderLabel: Record<string, string> = { github: 'GitHub', telegram: 'Telegram' }
const oauthUnbound = computed(() =>
  oauthProvidersEnabled.value.filter(p => !oauthIdentities.value.some(i => i.provider === p)))

async function loadOAuthIdentities() {
  try {
    const env = await api.oauthIdentities()
    if (env.ok && env.data) {
      oauthIdentities.value = env.data.identities ?? []
      oauthProvidersEnabled.value = env.data.enabled_providers ?? []
    }
  } catch { /* optional */ }
}
// Both providers are standard OAuth/OIDC redirect flows now: bind-start returns
// an auth_url we full-page redirect to; the provider 302s back to the console.
async function bindOAuth(provider: string) {
  oauthErr.value = ''
  oauthBindBusy.value = provider
  try {
    const env = await api.oauthBindStart(provider)
    if (!env.ok || !env.data) throw new Error(env.error || 'bind failed')
    window.location.href = env.data.auth_url // 302 flow → returns to /admin/security?bound=…
  } catch (e: unknown) {
    oauthErr.value = e instanceof Error ? e.message : String(e)
    oauthBindBusy.value = ''
  }
}
async function unbindOAuth(provider: string) {
  oauthErr.value = ''
  try {
    const env = await api.oauthUnbind(provider)
    if (!env.ok) throw new Error(env.error || 'unbind failed')
    await loadOAuthIdentities()
  } catch (e: unknown) { oauthErr.value = e instanceof Error ? e.message : String(e) }
}

onMounted(async () => {
  await refresh()
  await loadTrustedDevices()
  await loadSSHKeys()
  await loadAPITokens()
  await loadOAuthIdentities()
  // Surface the OAuth bind result that the backend redirected back with.
  const bound = route.query.bound as string | undefined
  const bindErr = route.query.bind_err as string | undefined
  if (bound) oauthErr.value = ''
  if (bindErr) oauthErr.value = decodeURIComponent(bindErr)
  if (isAdmin.value) {
    await refreshInvites()
    // ForgotPasswordQueue now self-loads on mount; nothing to call here.
  }
})

// RoleMailboxRecovery and ForgotPasswordQueue were extracted to
// src/components/admin/security/*.vue (#306 D3, 2026-05-28). They own
// their own state, fetch on mount, and live entirely inside the
// `team` tab template below. Security.vue still has 1300+ lines for
// the `account` tab + operators/invites — those share state and need
// composable extraction in a follow-up.

async function registerPasskey() {
  if (registering.value) return
  registering.value = true
  err.value = null
  lastAdded.value = null
  try {
    const begin = await api.passkeyRegBegin()
    if (!begin.ok || !begin.data) throw new Error(begin.error ?? 'begin failed')

    const options = decodeCreationOptions(begin.data.options)
    const cred = await navigator.credentials.create(options) as PublicKeyCredential | null
    if (!cred) throw new Error('credential creation aborted')

    const name = newName.value.trim() || `passkey · ${new Date().toLocaleString('en-GB', { hour12: false })}`
    const finish = await api.passkeyRegFinish(
      begin.data.challenge_id,
      name,
      encodeCreationResponse(cred)
    )
    if (!finish.ok || !finish.data) throw new Error(finish.error ?? 'verify failed')
    lastAdded.value = finish.data.name
    newName.value = ''
    await refresh()
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  } finally {
    registering.value = false
  }
}

async function removePasskey(p: PasskeyRecord) {
  if (!confirm(`Delete passkey "${p.name}"? You will lose this credential.`)) return
  try {
    const env = await api.passkeyDelete(p.id)
    if (!env.ok) {
      err.value = env.error ?? 'delete failed'
      return
    }
    await refresh()
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  }
}

function fmtDate(s: string): string {
  try { return new Date(s).toLocaleString('en-GB', { hour12: false }) } catch { return s }
}
</script>

<template>
  <div class="space-y-4">
    <div class="border border-gray-800 bg-gray-900 p-4">
      <div class="flex items-center justify-between flex-wrap gap-2">
        <div class="flex items-center gap-3">
          <span class="w-1.5 h-1.5 bg-emerald-500 animate-pulse"></span>
          <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">安全设置 · Security</h1>
        </div>
      </div>
    </div>

    <!-- ===== TAB NAVIGATION =====
         Splits the 11-section page into three semantically distinct
         groups so the admin doesn't scroll through unrelated chrome.
         `activeTab` is URL-synced via ?tab= so back/forward + bookmarks
         work, and non-admins are kicked off the recovery tab silently
         (see tabFromQuery() guard in script). -->
    <div class="border border-gray-800 bg-gray-900 overflow-x-auto">
      <div class="flex divide-x divide-gray-800 min-w-max">
        <button
          v-for="t in VISIBLE_TABS"
          :key="t"
          type="button"
          @click="setTab(t)"
          :class="[
            'px-4 py-2.5 text-[10px] tracking-widest uppercase whitespace-nowrap transition-colors',
            activeTab === t
              ? 'text-emerald-400 bg-emerald-950/30 border-b-2 border-emerald-500'
              : 'text-gray-500 hover:text-gray-300 hover:bg-gray-900/40 border-b-2 border-transparent'
          ]"
        >
          <span v-if="t === 'account'">我的账号 · account</span>
          <span v-else-if="t === 'team'">团队管理 · team</span>
          <span v-else>审计 · audit</span>
        </button>
      </div>
    </div>

    <template v-if="activeTab === 'account'">

    <!-- Change password -->
    <div class="border border-gray-800 bg-gray-900 p-4">
      <div class="text-[10px] tracking-widest text-gray-600 uppercase mb-3">更改密码 · change password</div>
      <form @submit.prevent="submitChangePassword" class="grid grid-cols-1 sm:grid-cols-3 gap-2">
        <input
          v-model="cpCurrent"
          type="password"
          autocomplete="current-password"
          :disabled="cpSubmitting"
          placeholder="current password"
          class="bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-500 focus:outline-none disabled:opacity-50"
        />
        <input
          v-model="cpNew"
          type="password"
          autocomplete="new-password"
          :disabled="cpSubmitting"
          placeholder="new password (min 8)"
          class="bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-500 focus:outline-none disabled:opacity-50"
        />
        <input
          v-model="cpConfirm"
          type="password"
          autocomplete="new-password"
          :disabled="cpSubmitting"
          placeholder="confirm new password"
          class="bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-500 focus:outline-none disabled:opacity-50"
        />

        <!-- Inline guidance / errors / success -->
        <div class="sm:col-span-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
          <span v-if="cpNew && cpNew.length < 8" class="text-amber-400">new password must be ≥ 8 chars</span>
          <span v-else-if="cpConfirm && cpNew !== cpConfirm" class="text-amber-400">passwords don't match</span>
          <span v-else-if="cpNew && cpNew === cpCurrent" class="text-amber-400">new must differ from current</span>
          <span v-if="cpErr" class="text-red-500">⨯ {{ cpErr }}</span>
          <span v-if="cpOk" class="text-emerald-500">{{ cpOk }}</span>
          <button
            type="submit"
            :disabled="!cpCanSubmit"
            class="ml-auto px-4 py-1.5 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-[10px] tracking-widest uppercase transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
          >{{ cpSubmitting ? '◌ rotating' : '▶ change password' }}</button>
        </div>
      </form>
      <p class="mt-3 text-xs text-gray-500 normal-case tracking-normal leading-relaxed">
        修改后立即生效。当前会话保留，但下次登录需要新密码。Passkey 和恢复码不受影响。
      </p>
    </div>

    <!-- Recovery codes status -->
    <div class="border border-gray-800 bg-gray-900 p-4">
      <div class="text-[10px] tracking-widest text-gray-600 uppercase mb-2">recovery codes</div>
      <div class="flex items-baseline gap-3">
        <span class="text-3xl font-mono tabular-nums"
          :class="recoveryRemaining === null ? 'text-gray-600' :
                  recoveryRemaining > 3 ? 'text-emerald-500' :
                  recoveryRemaining > 0 ? 'text-amber-400' : 'text-red-500'">
          {{ recoveryRemaining === null ? '?' : recoveryRemaining }}
        </span>
        <span class="text-xs text-gray-500">未使用 · 用于 "忘记密码" 流</span>
      </div>
      <p class="mt-3 text-xs text-gray-500 leading-relaxed normal-case tracking-normal">
        恢复码在首次部署 / 迁移时一次性打印到服务器日志
        (<code class="text-gray-400">journalctl -u ncn-api | grep RECOVERY</code>)。
        若全部用完且忘记密码，需要从服务器 SSH 重置 <code class="text-gray-400">/etc/ncn-core-console/operators.json</code>。
      </p>
    </div>

    <!-- Passkey list + register -->
    <div class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex justify-between flex-wrap gap-2">
        <span>passkeys · {{ passkeys.length }} registered</span>
        <span v-if="!passkeySupported" class="text-red-500 normal-case tracking-normal">browser does not support WebAuthn</span>
      </div>

      <ul v-if="passkeys.length" class="divide-y divide-gray-800">
        <li v-for="p in passkeys" :key="p.id" class="p-4 flex items-center justify-between gap-3 flex-wrap">
          <div class="flex-1 min-w-0">
            <div class="text-sm text-gray-100 font-mono break-all">{{ p.name }}</div>
            <div class="text-[10px] text-gray-600 mt-0.5 tracking-widest uppercase">
              created {{ fmtDate(p.created_at) }} · sign-count {{ p.sign_count }}
              <span v-if="p.transport && p.transport.length"> · {{ p.transport.join(', ') }}</span>
            </div>
            <div class="text-[10px] text-gray-700 mt-1 normal-case tracking-normal break-all">id={{ p.id }}</div>
          </div>
          <button
            @click="removePasskey(p)"
            class="px-3 py-1.5 border border-gray-700 hover:border-red-500 text-[10px] tracking-widest uppercase text-gray-400 hover:text-red-500 transition-colors"
          >remove</button>
        </li>
      </ul>
      <div v-else class="p-4 text-xs text-gray-600 italic">no passkeys registered yet</div>

      <!-- Register form -->
      <div v-if="passkeySupported" class="p-4 border-t border-gray-800 bg-gray-950/50">
        <div class="text-[10px] tracking-widest text-gray-600 uppercase mb-2">add new passkey</div>
        <div class="flex flex-col sm:flex-row gap-2">
          <input
            v-model="newName"
            :disabled="registering"
            placeholder="optional name (e.g. 'Bitwarden vault')"
            class="flex-1 bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-500 focus:outline-none disabled:opacity-50"
          />
          <button
            @click="registerPasskey"
            :disabled="registering"
            class="px-4 py-2 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-xs tracking-widest uppercase transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
          >{{ registering ? '◌ AWAITING DEVICE...' : '▶ REGISTER' }}</button>
        </div>
        <p class="mt-2 text-xs text-gray-500 normal-case tracking-normal leading-relaxed">
          点 REGISTER 后浏览器会弹出系统对话框 · 选 Google Password Manager / Bitwarden / iCloud Keychain / Touch ID / Face ID / YubiKey 等任一已知 authenticator · 完成绑定。下次登录可直接用 "🔑 Sign in with passkey"。
        </p>
        <div v-if="lastAdded" class="mt-2 text-xs text-emerald-400 normal-case tracking-normal">
          ✓ added: <span class="font-mono">{{ lastAdded }}</span>
        </div>
      </div>
    </div>

    <!-- ============ CONNECTED ACCOUNTS (OAuth) ============ -->
    <div class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex justify-between flex-wrap gap-2">
        <span>{{ t('security.oauth.title') }} · {{ oauthIdentities.length }}</span>
      </div>
      <div v-if="oauthErr" class="px-4 py-2 text-xs text-red-400 normal-case border-b border-gray-800">⨯ {{ oauthErr }}</div>
      <ul v-if="oauthIdentities.length" class="divide-y divide-gray-800">
        <li v-for="i in oauthIdentities" :key="i.provider" class="px-4 py-3 flex items-center justify-between gap-2">
          <div class="min-w-0">
            <div class="text-sm text-gray-200">{{ oauthProviderLabel[i.provider] || i.provider }}</div>
            <div class="text-[10px] text-gray-500 truncate">{{ i.email || '—' }}<span v-if="i.bound_at" class="text-gray-600"> · {{ t('security.oauth.bound') }} {{ i.bound_at.slice(0,10) }}</span></div>
          </div>
          <button @click="unbindOAuth(i.provider)"
            class="px-3 py-1.5 border border-gray-700 hover:border-red-500 text-[10px] tracking-widest uppercase text-gray-400 hover:text-red-500 shrink-0">{{ t('security.oauth.unbind') }}</button>
        </li>
      </ul>
      <div v-else class="p-4 text-xs text-gray-600 italic">{{ t('security.oauth.empty') }}</div>
      <div class="p-4 space-y-2 border-t border-gray-800">
        <div class="text-[10px] tracking-widest text-gray-600 uppercase">{{ t('security.oauth.link_title') }}</div>
        <div v-if="oauthUnbound.length" class="flex flex-wrap gap-2">
          <button v-for="p in oauthUnbound" :key="p" @click="bindOAuth(p)" :disabled="oauthBindBusy === p"
            class="inline-flex items-center gap-2 py-2 px-3 border border-gray-700 hover:border-emerald-500 text-[10px] tracking-widest uppercase text-gray-300 hover:text-emerald-400 disabled:opacity-40">
            + {{ oauthProviderLabel[p] || p }}
          </button>
        </div>
        <div v-else class="text-[10px] text-gray-600">{{ t('security.oauth.all_linked') }}</div>
      </div>
    </div>

    <!-- ============ SSH KEYS ============ -->
    <div class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex justify-between flex-wrap gap-2">
        <span>ssh keys · {{ sshKeys.length }} registered</span>
        <span class="text-gray-700 normal-case tracking-normal">CLI 登录用 · ncn-login --user {{ session.operator }}</span>
      </div>
      <ul v-if="sshKeys.length" class="divide-y divide-gray-800">
        <li v-for="k in sshKeys" :key="k.id" class="p-4 flex items-start justify-between gap-3 flex-wrap">
          <div class="flex-1 min-w-0">
            <div class="text-sm text-gray-100">{{ k.label }}</div>
            <div class="text-[10px] text-gray-500 mt-0.5 font-mono break-all">{{ k.fingerprint }}</div>
            <div class="text-[10px] text-gray-600 mt-0.5 tracking-widest uppercase">
              {{ k.type }} · added {{ fmtDate(new Date(k.created_at * 1000).toISOString()) }}
              <span v-if="k.last_used_at"> · last used {{ fmtAgo(k.last_used_at) }}</span>
              <span v-else class="text-gray-700"> · never used</span>
            </div>
          </div>
          <button @click="removeSSHKey(k)"
            class="px-3 py-1.5 border border-gray-700 hover:border-red-500 text-[10px] tracking-widest uppercase text-gray-400 hover:text-red-500">
            {{ t('security.ssh_keys.remove') }}
          </button>
        </li>
      </ul>
      <div v-else class="p-4 text-xs text-gray-600 italic">{{ t('security.ssh_keys.empty') }}</div>

      <!-- Add form -->
      <div class="p-4 border-t border-gray-800 bg-gray-950/50 space-y-2">
        <div class="text-[10px] tracking-widest text-gray-600 uppercase">{{ t('security.ssh_keys.add_title') }}</div>
        <input v-model="sshKeyForm.label"
          :disabled="sshKeyBusy"
          :placeholder="t('security.ssh_keys.placeholder_label')"
          class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-500 focus:outline-none disabled:opacity-50" />
        <textarea v-model="sshKeyForm.public_key"
          :disabled="sshKeyBusy"
          rows="3"
          :placeholder="t('security.ssh_keys.placeholder_key')"
          class="w-full bg-black border border-gray-800 px-3 py-2 text-xs font-mono text-gray-100 focus:border-emerald-500 focus:outline-none disabled:opacity-50 resize-y"></textarea>
        <div class="flex items-center justify-between flex-wrap gap-2">
          <p class="text-xs text-gray-500 normal-case tracking-normal flex-1 min-w-0">
            {{ t('security.ssh_keys.hint') }}
          </p>
          <button @click="addSSHKey" :disabled="sshKeyBusy"
            class="px-4 py-2 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-xs tracking-widest uppercase disabled:opacity-30">
            {{ sshKeyBusy ? '◌' : '▶ ' + t('security.ssh_keys.add') }}
          </button>
        </div>
        <div v-if="sshKeyErr" class="text-xs text-red-400 normal-case tracking-normal">⨯ {{ sshKeyErr }}</div>
      </div>
    </div>

    <!-- ============ API TOKENS ============ -->
    <div class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex justify-between flex-wrap gap-2">
        <span>api tokens · {{ apiTokens.length }} active</span>
        <span class="text-gray-700 normal-case tracking-normal">Authorization: Bearer ncntok_…</span>
      </div>

      <!-- One-time plaintext banner — copy now, can't see it again -->
      <div v-if="lastNewToken" class="p-4 border-b border-red-500/60 bg-red-950/20">
        <div class="flex items-center justify-between flex-wrap gap-2 mb-2">
          <span class="text-[10px] tracking-widest text-red-400 uppercase">
            ⚠ {{ t('security.api_tokens.last_warning') }}
          </span>
          <button @click="dismissLastNewToken" class="text-xs text-gray-400 hover:text-emerald-400">
            ✕ {{ t('security.api_tokens.last_dismiss') }}
          </button>
        </div>
        <div class="flex items-stretch gap-0 font-mono">
          <input :value="lastNewToken.token" readonly
            @focus="($event.target as HTMLInputElement).select()"
            class="flex-1 bg-black border border-gray-800 px-3 py-2 text-xs text-emerald-400 select-all break-all" />
          <button @click="copyToClipboard(lastNewToken.token)"
            class="px-3 py-2 border-l-0 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-[10px] tracking-widest uppercase">
            copy
          </button>
        </div>
        <p class="mt-2 text-xs text-gray-500 normal-case tracking-normal">
          {{ t('security.api_tokens.last_hint', { label: lastNewToken.label }) }}
        </p>
      </div>

      <ul v-if="apiTokens.length" class="divide-y divide-gray-800">
        <li v-for="tok in apiTokens" :key="tok.id" class="p-4 flex items-start justify-between gap-3 flex-wrap">
          <div class="flex-1 min-w-0">
            <div class="text-sm text-gray-100">{{ tok.label }}</div>
            <div class="text-[10px] text-gray-500 mt-0.5 font-mono">{{ tok.prefix_hint }}</div>
            <div class="text-[10px] text-gray-600 mt-0.5 tracking-widest uppercase">
              created {{ fmtDate(new Date(tok.created_at * 1000).toISOString()) }}
              <span v-if="tok.last_used_at"> · last used {{ fmtAgo(tok.last_used_at) }}</span>
              <span v-else class="text-gray-700"> · never used</span>
              <span v-if="tok.expires_at" class="text-amber-500"> · expires {{ fmtDate(new Date(tok.expires_at * 1000).toISOString()) }}</span>
              <span v-else class="text-gray-700"> · no expiry</span>
            </div>
          </div>
          <button @click="revokeAPIToken(tok)"
            class="px-3 py-1.5 border border-gray-700 hover:border-red-500 text-[10px] tracking-widest uppercase text-gray-400 hover:text-red-500">
            {{ t('security.api_tokens.revoke') }}
          </button>
        </li>
      </ul>
      <div v-else class="p-4 text-xs text-gray-600 italic">{{ t('security.api_tokens.empty') }}</div>

      <!-- Create form -->
      <div class="p-4 border-t border-gray-800 bg-gray-950/50 space-y-2">
        <div class="text-[10px] tracking-widest text-gray-600 uppercase">{{ t('security.api_tokens.add_title') }}</div>
        <div class="grid grid-cols-1 sm:grid-cols-12 gap-2">
          <input v-model="apiTokenForm.label"
            :disabled="apiTokenBusy"
            :placeholder="t('security.api_tokens.placeholder_label')"
            class="sm:col-span-8 bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-500 focus:outline-none disabled:opacity-50" />
          <input v-model.number="apiTokenForm.expires_in_days"
            type="number" min="0" max="365"
            :disabled="apiTokenBusy"
            :placeholder="t('security.api_tokens.placeholder_days')"
            class="sm:col-span-2 bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-500 focus:outline-none disabled:opacity-50" />
          <button @click="createAPIToken" :disabled="apiTokenBusy"
            class="sm:col-span-2 px-4 py-2 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-xs tracking-widest uppercase disabled:opacity-30">
            {{ apiTokenBusy ? '◌' : '▶ ' + t('security.api_tokens.create') }}
          </button>
        </div>
        <p class="text-xs text-gray-500 normal-case tracking-normal">
          {{ t('security.api_tokens.hint') }}
        </p>
        <div v-if="apiTokenErr" class="text-xs text-red-400 normal-case tracking-normal">⨯ {{ apiTokenErr }}</div>
      </div>
    </div>

    <!-- ============ TOTP (Authenticator app) ============ -->
    <div class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex justify-between flex-wrap gap-2">
        <span>TOTP authenticator</span>
        <span v-if="session.hasTotp" class="text-emerald-400 normal-case tracking-normal">⏱ 已绑定 · bound</span>
        <span v-else class="text-amber-400 normal-case tracking-normal">未绑定 · not configured</span>
      </div>

      <!-- Already bound — show steady-state info -->
      <div v-if="session.hasTotp && totpStage === 'idle'" class="p-4 text-xs text-gray-500 normal-case tracking-normal leading-relaxed">
        你的账户已绑定 TOTP secret。登录或进入终端时可输入 6 位验证码作为第二因素。
        <span class="text-gray-600">若要替换(例如换了 authenticator app),点下方"重新绑定"会用新 secret 覆盖当前绑定 — 旧 secret 立即失效。</span>
        <div class="mt-3">
          <button
            type="button"
            @click="totpBegin"
            :disabled="totpBusy"
            class="px-4 py-2 border border-gray-700 hover:border-amber-500 text-[10px] tracking-widest uppercase text-gray-400 hover:text-amber-400 transition-colors disabled:opacity-30"
          >{{ totpBusy ? '◌ rotating...' : '↻ 重新绑定 · re-bind' }}</button>
        </div>
      </div>

      <!-- Not bound, idle — offer setup -->
      <div v-else-if="!session.hasTotp && totpStage === 'idle'" class="p-4 space-y-3">
        <p class="text-xs text-gray-500 normal-case tracking-normal leading-relaxed">
          除了 passkey,你也可以绑定一个 TOTP secret 作为备份/替代二次因素。
          任意 Authenticator app 都行: <span class="text-gray-400">Authy / 1Password / Bitwarden / Google Authenticator / Microsoft Authenticator</span>。
          推荐和 passkey 同时启用 —— 一个设备丢了还能用另一个登录。
        </p>
        <button
          type="button"
          @click="totpBegin"
          :disabled="totpBusy"
          class="w-full sm:w-auto px-4 py-2 border border-violet-400 text-violet-400 hover:bg-violet-400 hover:text-black text-xs tracking-widest uppercase transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
        >{{ totpBusy ? '◌ generating...' : '▶ generate TOTP secret' }}</button>
      </div>

      <!-- Enrollment in progress — QR + manual entry + 6-digit code -->
      <div v-else-if="totpStage === 'enroll'" class="p-4 space-y-3">
        <p class="text-xs text-gray-500 normal-case tracking-normal leading-relaxed">
          扫描下方二维码到你的 Authenticator app,或手动输入 base32 secret。然后填入 app 当前显示的 6 位数字。
        </p>

        <!-- QR (responsive: 256 cap, never overflows on phones) -->
        <div class="bg-white p-2 mx-auto" style="max-width: min(256px, calc(100vw - 3rem));">
          <img :src="totpQrSrc" alt="TOTP QR code" class="block w-full h-auto" />
        </div>

        <div class="text-[10px] tracking-widest text-gray-600 uppercase">manual entry</div>
        <code class="block px-3 py-2 bg-black text-emerald-400 text-[11px] sm:text-xs leading-relaxed select-all break-all border border-gray-800">{{ totpSecret }}</code>

        <div class="text-[10px] tracking-widest text-gray-600 uppercase mt-2">verification code</div>
        <div class="flex flex-col sm:flex-row gap-2">
          <input
            v-model="totpCode"
            type="text"
            inputmode="numeric"
            pattern="[0-9]*"
            maxlength="6"
            autocomplete="one-time-code"
            placeholder="000000"
            @keyup.enter="totpConfirm"
            class="flex-1 bg-black border border-gray-800 px-3 py-3 sm:py-2 text-2xl sm:text-xl font-mono tracking-[0.4em] tabular-nums text-gray-100 text-center focus:border-emerald-500 focus:outline-none rounded-none"
          />
          <button
            type="button"
            @click="totpConfirm"
            :disabled="totpBusy || totpCode.trim().length < 6"
            class="px-4 py-3 sm:py-2 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-xs tracking-widest uppercase transition-colors disabled:opacity-30 disabled:cursor-not-allowed touch-manipulation"
          >{{ totpBusy ? '◌ verifying' : '▶ verify' }}</button>
        </div>

        <div class="flex justify-end">
          <button
            type="button"
            @click="totpCancel"
            :disabled="totpBusy"
            class="px-3 py-1.5 border border-gray-700 hover:border-gray-500 text-[10px] tracking-widest uppercase text-gray-400 transition-colors"
          >cancel</button>
        </div>

        <p v-if="totpErr" class="text-xs text-red-400 normal-case tracking-normal">⨯ {{ totpErr }}</p>
      </div>

      <!-- Just successfully bound — flash for 2s then collapse back to idle -->
      <div v-else-if="totpStage === 'done'" class="p-4 text-sm text-emerald-400 normal-case tracking-normal">
        ✓ TOTP bound · 下次登录或进终端可使用 6 位验证码
      </div>
    </div>

    <!-- ============ TRUSTED DEVICES ============
         Browsers the operator has marked "trust this device" on at login.
         Each one skips the TOTP step on future password sign-ins for 90
         days. Revoking a row deletes the server-side bcrypt hash so the
         cookie on that browser becomes a dead token. -->
    <div class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex justify-between flex-wrap gap-2">
        <span>信任设备 · trusted devices</span>
        <span class="text-gray-700 normal-case tracking-normal">
          {{ trustedDevices.length }} · ttl 90d
        </span>
      </div>

      <div v-if="devicesErr" class="px-4 py-2 text-xs text-red-400 border-b border-red-900">
        ⨯ {{ devicesErr }}
      </div>

      <div v-if="devicesLoading && trustedDevices.length === 0" class="px-4 py-6 text-center text-gray-600 text-xs italic">
        loading...
      </div>

      <div v-else-if="trustedDevices.length === 0" class="px-4 py-6 text-center text-gray-600 text-xs italic">
        没有信任设备 · no devices marked as trusted
        <div class="mt-1 text-gray-700 normal-case tracking-normal">
          下次密码登录时勾选"信任此设备",该浏览器将出现在这里
        </div>
      </div>

      <ul v-else class="divide-y divide-gray-800">
        <li v-for="d in trustedDevices" :key="d.id" class="p-4 flex items-start gap-3 flex-wrap sm:flex-nowrap">
          <div class="flex-1 min-w-0">
            <div class="flex items-baseline gap-2 flex-wrap">
              <span class="font-mono text-sm text-gray-100">{{ d.label }}</span>
              <span v-if="d.current" class="text-[10px] tracking-widest text-emerald-400 uppercase">● 当前 · this device</span>
            </div>
            <div class="mt-1 text-[10px] tracking-widest text-gray-600 uppercase flex flex-wrap gap-x-3 gap-y-0.5">
              <span>id <span class="font-mono text-gray-500 normal-case tracking-normal">{{ d.id }}</span></span>
              <span>added <span class="text-gray-500 normal-case tracking-normal tabular-nums">{{ fmtAgo(d.created_at) }}</span></span>
              <span>last seen <span class="text-gray-500 normal-case tracking-normal tabular-nums">{{ fmtAgo(d.last_seen_at) }}</span></span>
              <span v-if="d.last_seen_ip">from <span class="font-mono text-gray-500 normal-case tracking-normal">{{ d.last_seen_ip }}</span></span>
            </div>
            <div v-if="d.user_agent" class="mt-1 text-[10px] text-gray-700 normal-case tracking-normal break-all leading-snug"
                 style="overflow-wrap: anywhere;">{{ d.user_agent }}</div>
          </div>
          <button
            type="button"
            @click="revokeTrustedDevice(d.id)"
            :disabled="revoking === d.id"
            class="shrink-0 px-3 py-1.5 border border-red-700 text-red-400 hover:bg-red-700 hover:text-white text-[10px] tracking-widest uppercase transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {{ revoking === d.id ? '◌ revoking' : 'revoke' }}
          </button>
        </li>
      </ul>
    </div>

    </template>
    <template v-if="activeTab === 'team'">

    <!-- ============ PENDING APPROVALS — visible to everyone but only admin can act ============ -->
    <div v-if="pendingApprovals.length > 0" class="border-2 border-amber-500/60 bg-amber-900/15">
      <div class="px-4 py-2 border-b border-amber-500/60 bg-amber-900/30 text-[10px] tracking-widest text-amber-300 uppercase flex justify-between flex-wrap gap-2">
        <span class="flex items-center gap-2">
          <span class="w-1.5 h-1.5 bg-amber-400 animate-pulse"></span>
          <span>待批准 · pending approvals · {{ pendingApprovals.length }}</span>
        </span>
      </div>
      <ul class="divide-y divide-amber-500/30">
        <li v-for="o in pendingApprovals" :key="o.username" class="p-4 flex items-center justify-between flex-wrap gap-3">
          <div class="flex-1 min-w-0">
            <div class="text-sm font-mono text-gray-100">
              {{ o.username }}
              <span class="ml-2 text-[10px] tracking-widest uppercase text-amber-400">{{ o.role }}</span>
            </div>
            <div class="text-[10px] tracking-widest text-gray-500 mt-1 normal-case uppercase">
              invited by <span class="text-emerald-400">{{ o.invited_by || '?' }}</span>
              <span class="text-gray-700 mx-1">·</span>
              <span v-if="o.invited_at">{{ new Date(o.invited_at).toLocaleString('en-GB', { hour12: false }) }}</span>
            </div>
            <div class="text-[10px] tracking-widest text-gray-500 mt-1 uppercase">
              2FA bound: <span :class="o.has_totp || o.passkeys_count > 0 ? 'text-emerald-400' : 'text-red-400'">
                {{ o.passkeys_count > 0 ? `🔑 passkey×${o.passkeys_count}` : '' }}{{ o.passkeys_count > 0 && o.has_totp ? ' + ' : '' }}{{ o.has_totp ? '⏱ TOTP' : '' }}{{ !o.has_totp && o.passkeys_count === 0 ? 'none' : '' }}
              </span>
            </div>
          </div>
          <div v-if="isAdmin" class="flex gap-2">
            <button
              @click="rejectOperator(o)"
              class="px-3 py-1.5 border border-gray-700 hover:border-red-500 text-[10px] tracking-widest uppercase text-gray-400 hover:text-red-500 transition-colors"
            >reject</button>
            <button
              @click="approveOperator(o)"
              class="px-3 py-1.5 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-[10px] tracking-widest uppercase transition-colors"
            >✓ approve</button>
          </div>
          <div v-else class="text-[10px] tracking-widest text-gray-600 uppercase">awaiting admin review</div>
        </li>
      </ul>
    </div>

    <!-- ============ FORGOT-PASSWORD QUEUE — admin only ============
         Extracted to its own component in #306. Self-fetches on mount,
         self-gates on session.role === 'admin'. The v-if here is the
         tab-level guard so the component isn't mounted when not on
         the team tab (avoids the on-mount fetch when the user is
         elsewhere). -->
    <ForgotPasswordQueue v-if="isAdmin" />

    <!-- ============ INVITES — admin only ============ -->
    <div v-if="isAdmin" class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex justify-between flex-wrap gap-2">
        <span>邀请链接 · invites</span>
        <span class="text-gray-700">single-use · 24h TTL · operator role only</span>
      </div>

      <!-- Invite-by-email form -->
      <div class="p-4 border-b border-gray-800 space-y-3">
        <div class="text-sm text-gray-300 normal-case tracking-normal max-w-2xl leading-relaxed">
          {{ t('security.invite.hint') }}
        </div>
        <div class="grid grid-cols-1 md:grid-cols-12 gap-2">
          <input
            v-model="inviteForm.email"
            type="email"
            :placeholder="t('security.invite.placeholder_email')"
            @keyup.enter="generateInvite"
            class="md:col-span-6 px-3 py-2 border border-gray-800 bg-gray-950 text-gray-200 placeholder-gray-600 text-sm focus:outline-none focus:border-emerald-700" />
          <input
            v-model="inviteForm.name"
            type="text"
            :placeholder="t('security.invite.placeholder_name')"
            @keyup.enter="generateInvite"
            class="md:col-span-4 px-3 py-2 border border-gray-800 bg-gray-950 text-gray-200 placeholder-gray-600 text-sm focus:outline-none focus:border-emerald-700" />
          <button
            @click="generateInvite"
            :disabled="inviteBusy || !inviteForm.email.trim()"
            class="md:col-span-2 px-3 py-2 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-[11px] tracking-widest uppercase transition-colors disabled:opacity-30 disabled:cursor-not-allowed">
            {{ inviteBusy ? '◌ ' + t('security.invite.sending') : '▶ ' + t('security.invite.send') }}
          </button>
        </div>
      </div>

      <!-- Latest send confirmation -->
      <div v-if="lastInvite" class="p-4 border-b border-gray-800 bg-gray-950/40">
        <div class="flex items-center justify-between flex-wrap gap-2 mb-2">
          <span :class="lastInvite.mail_status === 'sent'
              ? 'text-[10px] tracking-widest text-emerald-400 uppercase'
              : 'text-[10px] tracking-widest text-red-400 uppercase'">
            <template v-if="lastInvite.mail_status === 'sent'">
              ✓ {{ t('security.invite.last_sent', { email: lastInvite.invitee_email }) }}
            </template>
            <template v-else>
              ⚠ {{ t('security.invite.last_failed') }} — {{ lastInvite.mail_status }}
            </template>
          </span>
          <button @click="dismissLastInvite" class="text-xs text-gray-500 hover:text-gray-300">✕</button>
        </div>
        <!-- Show URL too — useful if mail failed (copy + send out-of-band)
             or just for the admin's records. NOT marked as "one-time" any
             more because the email itself is the primary distribution. -->
        <div class="flex items-stretch gap-0 font-mono">
          <input
            :value="lastInvite.url"
            readonly
            @focus="($event.target as HTMLInputElement).select()"
            class="flex-1 bg-black border border-gray-800 px-3 py-2 text-xs text-emerald-400/80 select-all break-all" />
          <button
            @click="copyInviteUrl(lastInvite.url)"
            class="px-3 py-2 border-l-0 border border-gray-700 text-gray-400 hover:bg-gray-700 hover:text-emerald-400 text-[10px] tracking-widest uppercase transition-colors">
            copy
          </button>
        </div>
        <div class="mt-2 text-[10px] text-gray-500 tracking-widest uppercase">
          {{ t('security.invite.expires') }} {{ new Date(lastInvite.expires_at).toLocaleString('en-GB', { hour12: false }) }} ·
          {{ Math.floor(lastInvite.expires_in / 3600) }}h
        </div>
      </div>

      <!-- Issued invites table -->
      <div v-if="invites.length" class="overflow-x-auto">
        <table class="w-full text-xs min-w-[640px]">
          <thead class="text-[10px] text-gray-600 uppercase tracking-widest">
            <tr class="border-b border-gray-800">
              <th class="text-left px-3 py-2">{{ t('security.invite.col_invitee') }}</th>
              <th class="text-left px-3 py-2 font-mono">token</th>
              <th class="text-left px-3 py-2 hidden sm:table-cell">{{ t('security.invite.col_issued') }}</th>
              <th class="text-left px-3 py-2">{{ t('security.invite.col_mail') }}</th>
              <th class="text-left px-3 py-2">{{ t('security.invite.col_status') }}</th>
              <th class="text-right px-3 py-2">{{ t('security.invite.col_actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="iv in invites" :key="iv.token" class="border-b border-gray-800/50">
              <!-- Invitee identity. Show the human name (if known) above the
                   email; for legacy tokens that have no email we render a
                   single "—" placeholder so the layout stays balanced. -->
              <td class="px-3 py-2 align-top">
                <div v-if="iv.invitee_name" class="text-gray-200">{{ iv.invitee_name }}</div>
                <div :class="iv.invitee_name ? 'text-gray-500 font-mono text-[11px]' : 'text-gray-300 font-mono'">
                  {{ iv.invitee_email || '—' }}
                </div>
              </td>
              <td class="px-3 py-2 text-gray-500 font-mono align-top">{{ iv.token }}</td>
              <td class="px-3 py-2 text-gray-500 hidden sm:table-cell whitespace-nowrap align-top">
                {{ new Date(iv.created_at).toLocaleDateString('en-CA') }}
              </td>
              <!-- Mail delivery state -->
              <td class="px-3 py-2 align-top">
                <span v-if="iv.mail_status === 'sent'" class="text-emerald-400">
                  ✓ {{ t('security.invite.mail_sent') }}
                </span>
                <span v-else-if="iv.mail_status && iv.mail_status.startsWith('failed')" class="text-red-400" :title="iv.mail_status">
                  ⨯ {{ t('security.invite.mail_failed') }}
                </span>
                <span v-else class="text-gray-600">—</span>
              </td>
              <!-- Acceptance state -->
              <td class="px-3 py-2 align-top">
                <span v-if="iv.used" class="text-emerald-400">{{ t('security.invite.status_used', { who: iv.used_by ?? '?' }) }}</span>
                <span v-else-if="new Date(iv.expires_at).getTime() < Date.now()" class="text-gray-600">{{ t('security.invite.status_expired') }}</span>
                <span v-else class="text-amber-400">
                  {{ t('security.invite.status_pending') }} · {{ new Date(iv.expires_at).toLocaleDateString('en-CA') }}
                </span>
              </td>
              <td class="px-3 py-2 text-right align-top whitespace-nowrap">
                <button
                  v-if="!iv.used && iv.invitee_email"
                  @click="resendInvite(iv.token)"
                  :disabled="resendBusy[iv.token.replace(/…$/, '')]"
                  class="mr-1 px-2 py-1 border border-gray-700 hover:border-emerald-500 text-[10px] tracking-widest uppercase text-gray-400 hover:text-emerald-400 disabled:opacity-30 transition-colors">
                  {{ resendBusy[iv.token.replace(/…$/, '')] ? '◌' : '↻ ' + t('security.invite.action_resend') }}
                </button>
                <button
                  v-if="!iv.used"
                  @click="revokeInvite(iv.token)"
                  class="px-2 py-1 border border-gray-700 hover:border-red-500 text-[10px] tracking-widest uppercase text-gray-400 hover:text-red-500 transition-colors">
                  {{ t('security.invite.action_revoke') }}
                </button>
                <span v-if="iv.used" class="text-gray-700 text-[10px] uppercase tracking-widest">—</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-else class="p-4 text-xs text-gray-600 italic">{{ t('security.invite.empty') }}</div>

      <div v-if="invitesErr" class="px-4 py-2 border-t border-red-500/60 text-xs text-red-400 normal-case tracking-normal">
        ⨯ {{ invitesErr }}
      </div>
    </div>

    </template>
    <template v-if="activeTab === 'account'">

    <!-- ============ My Webmail Mailbox · self-register on mail.example.com ============ -->
    <div class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex justify-between flex-wrap gap-2">
        <span>我的邮箱 · webmail self-register</span>
        <span class="text-gray-700">HMAC-signed bridge · 10min TTL · single-use</span>
      </div>
      <div class="p-4 space-y-3">
        <p class="text-sm text-gray-300 normal-case tracking-normal leading-relaxed">
          点下方按钮生成一个 10 分钟有效的预签邀请链接,跳转到 mail.example.com 上注册一个属于你的 @example.com 邮箱。
          ncn-mail 通过 HMAC 验签你是合法运维 — 无需重新输运维密码。
        </p>
        <div class="flex items-center gap-3 flex-wrap">
          <button
            @click="requestMyMailbox"
            :disabled="mailSelfBusy"
            class="px-4 py-2 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-xs tracking-widest uppercase transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
          >
            {{ mailSelfBusy ? '◌ minting' : '▶ get my webmail mailbox' }}
          </button>
        </div>

        <!-- one-time URL display -->
        <div v-if="lastMailInvite" class="border border-emerald-500/60 bg-emerald-950/20 p-3">
          <div class="flex items-center justify-between flex-wrap gap-2 mb-2">
            <span class="text-[10px] tracking-widest text-emerald-400 uppercase">
              ⏱ {{ Math.max(0, Math.floor((new Date(lastMailInvite.expires_at).getTime() - Date.now()) / 60000)) }}min · single-use
            </span>
            <button @click="dismissMailInvite" class="text-xs text-gray-400 hover:text-emerald-500">[ ✕ ]</button>
          </div>
          <div class="flex items-stretch gap-0 font-mono">
            <input
              :value="lastMailInvite.url"
              readonly
              @focus="($event.target as HTMLInputElement).select()"
              class="flex-1 bg-black border border-gray-800 px-3 py-2 text-xs text-emerald-400 select-all break-all"
            />
            <a
              :href="lastMailInvite.url"
              target="_blank"
              rel="noopener"
              class="px-3 py-2 border-l-0 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-[10px] tracking-widest uppercase transition-colors flex items-center"
            >open ↗</a>
          </div>
          <p class="mt-2 text-xs text-gray-500 normal-case tracking-normal leading-relaxed">
            点 "open ↗" 跳到 webmail,挑一个别名 + 设密码就好。别名默认填的是你的运维用户名 (<code class="text-gray-300">{{ lastMailInvite.operator }}</code>),你可以改成任何别的小写字母+数字+连字符。
          </p>
        </div>

        <div v-if="mailSelfErr" class="text-xs text-red-400 normal-case tracking-normal">
          ⨯ {{ mailSelfErr }}
        </div>
      </div>
    </div>

    </template>
    <template v-if="activeTab === 'team'">

    <!-- ============ OPERATORS ============ -->
    <div class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex justify-between flex-wrap gap-2">
        <span>运维账户 · operators · {{ operators.length }} total</span>
        <span v-if="!isAdmin" class="normal-case tracking-normal text-gray-700">
          read-only · current role: <span class="text-gray-400">{{ session.role || '?' }}</span>
        </span>
      </div>

      <!-- Table -->
      <div class="overflow-x-auto">
        <table class="w-full text-xs font-mono min-w-[640px]">
          <thead class="text-[10px] text-gray-600 uppercase tracking-widest">
            <tr class="border-b border-gray-800">
              <th class="text-left px-3 py-2">username</th>
              <th class="text-left px-3 py-2">role</th>
              <th class="text-left px-3 py-2 hidden sm:table-cell">created</th>
              <th class="text-right px-3 py-2 hidden md:table-cell">recovery codes</th>
              <th class="text-right px-3 py-2 hidden md:table-cell">passkeys</th>
              <th class="text-right px-3 py-2">actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="o in operators" :key="o.username" class="border-b border-gray-800/50">
              <td class="px-3 py-2.5 whitespace-nowrap text-gray-100">
                {{ o.username }}
                <span v-if="o.username === session.operator" class="ml-2 text-[9px] tracking-widest text-emerald-500 uppercase">you</span>
              </td>
              <td class="px-3 py-2.5">
                <select
                  v-if="isAdmin"
                  :value="o.role"
                  @change="updateOperatorRole(o, ($event.target as HTMLSelectElement).value as 'admin'|'operator')"
                  class="bg-black border border-gray-800 px-2 py-1 text-xs font-mono text-gray-100"
                >
                  <option value="operator">operator</option>
                  <option value="admin">admin</option>
                </select>
                <span v-else :class="o.role === 'admin' ? 'text-pink-400' : 'text-gray-300'">
                  {{ o.role }}
                </span>
              </td>
              <td class="px-3 py-2.5 text-gray-500 hidden sm:table-cell whitespace-nowrap">
                {{ o.created_at ? new Date(o.created_at).toLocaleDateString('en-CA') : '—' }}
              </td>
              <td class="px-3 py-2.5 text-right tabular-nums hidden md:table-cell">
                <span :class="o.recovery_remaining > 3 ? 'text-emerald-500' :
                              o.recovery_remaining > 0 ? 'text-amber-400' : 'text-red-500'">
                  {{ o.recovery_remaining }}
                </span>
              </td>
              <td class="px-3 py-2.5 text-right tabular-nums hidden md:table-cell">
                {{ o.passkeys_count }}
              </td>
              <td class="px-3 py-2.5 text-right">
                <button
                  v-if="isAdmin && o.username !== session.operator"
                  @click="deleteOperator(o)"
                  class="px-2 py-1 border border-gray-700 hover:border-red-500 text-[10px] tracking-widest uppercase text-gray-400 hover:text-red-500 transition-colors"
                >remove</button>
                <span v-else class="text-gray-700">—</span>
              </td>
            </tr>
            <tr v-if="operators.length === 0">
              <td colspan="6" class="px-3 py-4 text-gray-600 italic text-center">no operators</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Add form — admin only -->
      <div v-if="isAdmin" class="p-4 border-t border-gray-800 bg-gray-950/40">
        <div class="text-[10px] tracking-widest text-gray-600 uppercase mb-2">add operator</div>
        <form @submit.prevent="createOperator" class="flex flex-col sm:flex-row gap-2 items-stretch">
          <input
            v-model="newOpUsername"
            :disabled="newOpBusy"
            placeholder="username (2-32 chars, [A-Za-z0-9._-])"
            class="flex-1 bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-500 focus:outline-none disabled:opacity-50"
          />
          <select
            v-model="newOpRole"
            :disabled="newOpBusy"
            class="bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-500 focus:outline-none disabled:opacity-50"
          >
            <option value="operator">operator</option>
            <option value="admin">admin</option>
          </select>
          <button
            type="submit"
            :disabled="newOpBusy || !newOpUsername.trim()"
            class="px-4 py-2 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-xs tracking-widest uppercase transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
          >{{ newOpBusy ? '◌ creating' : '▶ create' }}</button>
        </form>
        <p class="mt-2 text-xs text-gray-500 normal-case tracking-normal leading-relaxed">
          创建会一次性显示初始密码 + 10 个恢复码。<span class="text-amber-400">这是唯一一次显示</span> — 复制出来后通过加密信道转交。新运维登录后立刻去"更改密码"重置。
        </p>
      </div>

      <!-- One-time credentials display -->
      <div v-if="newOpCredentials" class="p-4 border-t border-red-500/60 bg-red-950/20">
        <div class="flex items-center justify-between mb-2">
          <span class="text-[10px] tracking-widest text-red-400 uppercase">
            ⚠ ONE-TIME DISPLAY · save these before dismissing
          </span>
          <button @click="dismissCredentials" class="text-xs text-gray-400 hover:text-emerald-500">[ I have saved them ✕ ]</button>
        </div>
        <div class="font-mono text-xs space-y-2 break-all">
          <div>
            <span class="text-gray-500">username  </span>
            <span class="text-emerald-400">{{ newOpCredentials.username }}</span>
            <span class="text-gray-700 ml-2">·</span>
            <span class="text-gray-500 ml-2">role  </span>
            <span class="text-emerald-400">{{ newOpCredentials.role }}</span>
          </div>
          <div>
            <span class="text-gray-500">password  </span>
            <span class="text-emerald-400 select-all">{{ newOpCredentials.password }}</span>
          </div>
          <div>
            <div class="text-gray-500 mb-1">recovery codes ({{ newOpCredentials.recovery_codes.length }})</div>
            <div class="grid grid-cols-2 sm:grid-cols-5 gap-x-4 gap-y-1 text-emerald-400 select-all">
              <span v-for="c in newOpCredentials.recovery_codes" :key="c">{{ c }}</span>
            </div>
          </div>
        </div>
      </div>

      <div v-if="opsErr" class="px-4 py-2 border-t border-red-500/60 text-xs text-red-400 normal-case tracking-normal">
        ⨯ {{ opsErr }}
      </div>
    </div>

    <!-- ============ ROLE MAILBOX RECOVERY — admin only ============
         Extracted to its own component in #306. Self-owned state. -->
    <RoleMailboxRecovery v-if="isAdmin" />

    </template>

    <!-- ============ AUDIT (merged from /admin/audit) ============
         Mounts the standalone Audit view as a tab inside Security so the
         admin nav stays slim. The component self-fetches + admin-gates;
         we also gate the tab via ADMIN_TABS. -->
    <template v-if="activeTab === 'audit'">
      <Audit />
    </template>

    <div v-if="err" class="border border-red-500 bg-red-950/30 p-3 text-xs text-red-400 normal-case tracking-normal">
      ⨯ {{ err }}
    </div>
  </div>
</template>
