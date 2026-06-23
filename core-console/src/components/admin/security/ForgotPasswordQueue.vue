<script setup lang="ts">
// ForgotPasswordQueue — admin-side mirror of the password-reset request
// queue that ncn-mail (on pop-03) maintains. Each entry is a non-operator
// mailbox user asking to reset their password; the admin either approves
// (mint + email recovery URL) or dismisses (reject).
//
// Dual-track display: this panel + the webmail-side admin panel. Either
// can dismiss/approve and the other reflects on next refresh.
//
// Extracted from src/views/admin/Security.vue (#306 D3). State is fully
// owned by this component. Mount-time auto-load is fired from
// onMounted; parent doesn't need to plumb it.

import { ref, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, type MailForgotEntry } from '@/api/client'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const session = useSessionStore()

const forgotQueue = ref<MailForgotEntry[]>([])
const forgotLoading = ref(false)
const forgotErr = ref<string | null>(null)
const forgotDismissBusy = ref<string | null>(null)
const forgotApproveBusy = ref<string | null>(null)

async function loadForgotQueue() {
  if (session.role !== 'admin') return
  forgotLoading.value = true
  forgotErr.value = null
  try {
    const env = await api.mailForgotList()
    if (!env.ok) throw new Error(env.error || 'list failed')
    forgotQueue.value = env.data ?? []
  } catch (e: unknown) {
    forgotErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    forgotLoading.value = false
  }
}

async function dismissForgot(id: string) {
  if (forgotDismissBusy.value) return
  forgotDismissBusy.value = id
  try {
    const env = await api.mailForgotDismiss(id)
    if (!env.ok) throw new Error(env.error || 'dismiss failed')
    forgotQueue.value = forgotQueue.value.filter(e => e.id !== id)
  } catch (e: unknown) {
    forgotErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    forgotDismissBusy.value = null
  }
}

async function approveForgot(entry: MailForgotEntry) {
  if (forgotApproveBusy.value) return
  if (!confirm(t('security.forgot.confirm_approve', { mailbox: entry.mailbox }))) return
  forgotApproveBusy.value = entry.id
  forgotErr.value = null
  try {
    const env = await api.mailForgotApprove(entry.id)
    if (!env.ok) throw new Error(env.error || 'approve failed')
    forgotQueue.value = forgotQueue.value.filter(e => e.id !== entry.id)
  } catch (e: unknown) {
    forgotErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    forgotApproveBusy.value = null
  }
}

// 1-second tick so the "5s ago / 3m ago / 2h ago" labels refresh live.
const nowTick = ref(Date.now())
const tickInterval = window.setInterval(() => { nowTick.value = Date.now() }, 1000)
onUnmounted(() => window.clearInterval(tickInterval))

function forgotAgo(iso: string): string {
  const t = Date.parse(iso)
  if (!Number.isFinite(t)) return iso
  const s = Math.floor((nowTick.value - t) / 1000)
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ${m % 60}m ago`
  const d = Math.floor(h / 24)
  return `${d}d ${h % 24}h ago`
}

onMounted(loadForgotQueue)
</script>

<template>
  <!-- Mirror of the same queue ncn-mail's admin panel shows. Operator
       self-recovery entries are filtered out by the bridge handler, so
       everything here genuinely needs an admin's attention. Dual-track
       display: either panel can dismiss + the other reflects on next
       refresh. -->
  <div class="border border-gray-800 bg-gray-900">
    <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex justify-between flex-wrap gap-2">
      <span>忘记密码请求 · forgot-password queue</span>
      <div class="flex items-center gap-3">
        <span class="text-gray-700">non-operator mailboxes only · 7d TTL</span>
        <button
          type="button"
          @click="loadForgotQueue"
          :disabled="forgotLoading"
          class="text-gray-500 hover:text-emerald-400 disabled:opacity-30 normal-case tracking-normal"
          title="refresh"
        >↻ refresh</button>
      </div>
    </div>

    <div v-if="forgotLoading && !forgotQueue.length" class="p-4 text-xs text-gray-500 italic">
      loading queue…
    </div>

    <div v-else-if="!forgotQueue.length" class="p-4 text-xs text-gray-600 italic normal-case tracking-normal">
      no pending forgot-password requests · operators auto-recover via the email link
    </div>

    <ul v-else class="divide-y divide-gray-800">
      <li v-for="e in forgotQueue" :key="e.id" class="p-4 flex flex-col sm:flex-row sm:items-center justify-between gap-3">
        <div class="min-w-0 flex-1">
          <div class="text-sm text-gray-200 font-mono break-all">{{ e.mailbox }}</div>
          <div class="mt-1 text-[10px] tracking-widest text-gray-600 uppercase flex flex-wrap gap-x-3 gap-y-1">
            <span>{{ forgotAgo(e.requested_at) }}</span>
            <span class="normal-case tracking-normal text-gray-500">ip: {{ e.ip }}</span>
            <span v-if="e.ua" class="normal-case tracking-normal text-gray-700 truncate max-w-[24em]" :title="e.ua">ua: {{ e.ua }}</span>
          </div>
        </div>
        <div class="flex items-center gap-2 shrink-0 flex-wrap">
          <span class="text-[10px] tracking-widest text-gray-700 uppercase">id {{ e.id }}</span>
          <button
            type="button"
            @click="approveForgot(e)"
            :disabled="forgotApproveBusy === e.id || forgotDismissBusy === e.id"
            class="px-3 py-1.5 border border-emerald-700 hover:border-emerald-500 hover:text-emerald-300 disabled:opacity-30 text-[10px] tracking-widest uppercase text-emerald-400">
            {{ forgotApproveBusy === e.id ? '…' : t('security.forgot.approve') }}
          </button>
          <button
            type="button"
            @click="dismissForgot(e.id)"
            :disabled="forgotDismissBusy === e.id || forgotApproveBusy === e.id"
            class="px-3 py-1.5 border border-gray-700 hover:border-red-500 hover:text-red-400 disabled:opacity-30 text-[10px] tracking-widest uppercase text-gray-400">
            {{ forgotDismissBusy === e.id ? '…' : '⨯ ' + t('security.forgot.dismiss') }}
          </button>
        </div>
      </li>
    </ul>

    <div v-if="forgotErr" class="p-4 border-t border-red-500/40 bg-red-950/20 text-xs text-red-400 normal-case tracking-normal break-all">
      ⨯ {{ forgotErr }}
    </div>
  </div>
</template>
