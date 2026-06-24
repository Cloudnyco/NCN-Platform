<script setup lang="ts">
// CliLogin — dedicated browser → CLI login handoff page.
//
// `ncn-login` opens /cli-login?cli_port=PORT&cli_state=STATE and runs a loopback
// server on 127.0.0.1:PORT. This page, once the session is authenticated, mints
// an API token and POSTs it back to that loopback so the CLI is logged in.
//
// If we're NOT authenticated yet, we bounce to /login?next=<this page> and come
// straight back here after sign-in — so it works whether or not you're already
// logged in, and you always land on a clear "done" screen (never stranded on a
// login form).
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from '@/api/client'
import { useLocaleAware } from '@/i18n'

const route = useRoute()
const router = useRouter()
const { t } = useLocaleAware()

type Status = 'working' | 'done' | 'error'
const status = ref<Status>('working')
const detail = ref('')

onMounted(async () => {
  const port = String(route.query.cli_port ?? '')
  const state = String(route.query.cli_state ?? '')
  if (!port || !state) {
    status.value = 'error'
    detail.value = t('cli_login.bad_params')
    return
  }

  // Mint a token with the current session. Not authenticated (401 / !ok / throw)
  // → go sign in, then return here to finish.
  let token = ''
  try {
    const label = 'ncn-cli ' + new Date().toISOString().slice(0, 16).replace('T', ' ')
    const env = await api.apiTokenCreate(label)
    if (!env.ok || !env.data?.token) {
      router.replace('/login?next=' + encodeURIComponent(route.fullPath))
      return
    }
    token = env.data.token
  } catch {
    router.replace('/login?next=' + encodeURIComponent(route.fullPath))
    return
  }

  // Hand the token to the waiting loopback (localhost is a secure context, so
  // https → http://127.0.0.1 is exempt from mixed-content blocking).
  try {
    await fetch(`http://127.0.0.1:${encodeURIComponent(port)}/callback`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ state, token }),
    })
    status.value = 'done'
  } catch {
    status.value = 'error'
    detail.value = t('cli_login.loopback_fail')
  }
})
</script>

<template>
  <div class="min-h-screen flex items-center justify-center bg-[var(--bg)] text-[var(--fg)] p-6">
    <div class="w-full max-w-md rounded-xl border border-[var(--border)] bg-[var(--surface)] p-8 text-center shadow-lg">
      <div class="text-2xl font-mono font-bold tracking-wide mb-1">ncn-login</div>
      <div class="text-xs uppercase tracking-widest text-[var(--muted)] mb-6">CLI authentication</div>

      <template v-if="status === 'working'">
        <div class="text-[var(--muted)] animate-pulse">{{ t('cli_login.working') }}</div>
      </template>

      <template v-else-if="status === 'done'">
        <div class="text-3xl mb-2">✓</div>
        <div class="font-medium text-[var(--ok,#22c55e)]">{{ t('cli_login.done') }}</div>
        <div class="text-sm text-[var(--muted)] mt-2">{{ t('cli_login.close') }}</div>
      </template>

      <template v-else>
        <div class="text-3xl mb-2">⚠</div>
        <div class="font-medium text-[var(--err,#ef4444)]">{{ t('cli_login.error') }}</div>
        <div class="text-sm text-[var(--muted)] mt-2">{{ detail }}</div>
      </template>
    </div>
  </div>
</template>
