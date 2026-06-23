<!--
  WebmailBridge.vue — operator → webmail self-register handoff.

  The webmail (mail.example.com) "Register" button redirects here. By the
  time we mount, the global router guard has ensured an authenticated
  operator session. We immediately call /api/v1/auth/mail-self-invite to
  mint an HMAC-signed op-token, then redirect the browser to
  https://mail.example.com/invite/op-<token>.

  No UI to interact with — pure transit page. The user sees one second of
  "preparing..." then the webmail registration form. Errors land here.
-->
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/api/client'

const { t } = useI18n()
const status = ref<'loading' | 'redirecting' | 'error'>('loading')
const errMsg = ref('')
const targetUrl = ref('')

onMounted(async () => {
  try {
    const env = await api.mailSelfInvite()
    if (!env.ok || !env.data) {
      throw new Error(env.error || 'mint failed')
    }
    targetUrl.value = env.data.url
    status.value = 'redirecting'
    // Tiny delay so the user can see what's happening if the network is fast.
    setTimeout(() => { window.location.replace(env.data!.url) }, 400)
  } catch (e: unknown) {
    status.value = 'error'
    errMsg.value = e instanceof Error ? e.message : String(e)
  }
})
</script>

<template>
  <div class="min-h-[60vh] flex items-center justify-center p-6">
    <div class="w-full max-w-md border border-gray-800 bg-gray-900 p-6">
      <h1 class="text-sm tracking-[0.25em] uppercase text-gray-200 mb-4">
        // {{ t('webmail_bridge.title') }}
      </h1>

      <div v-if="status === 'loading'" class="space-y-2 text-xs text-gray-400">
        <div class="flex items-center gap-2">
          <span class="w-1.5 h-1.5 bg-emerald-500 animate-pulse"></span>
          <span>{{ t('webmail_bridge.loading') }}</span>
        </div>
      </div>

      <div v-else-if="status === 'redirecting'" class="space-y-3 text-xs">
        <div class="flex items-center gap-2 text-emerald-400">
          <span class="w-1.5 h-1.5 bg-emerald-500"></span>
          <span>{{ t('webmail_bridge.redirecting') }}</span>
        </div>
        <p class="text-gray-500 leading-relaxed">
          {{ t('webmail_bridge.hint') }}
        </p>
        <a :href="targetUrl"
           class="block text-center px-3 py-2 border border-emerald-700 text-emerald-300 hover:bg-emerald-900/30 text-[10px] tracking-widest uppercase">
          {{ t('webmail_bridge.manual') }}
        </a>
      </div>

      <div v-else class="space-y-3 text-xs">
        <div class="flex items-center gap-2 text-red-400">
          <span class="w-1.5 h-1.5 bg-red-500"></span>
          <span>{{ t('webmail_bridge.error') }}</span>
        </div>
        <p class="text-gray-500 break-all">⨯ {{ errMsg }}</p>
        <router-link to="/admin/security"
           class="block text-center px-3 py-2 border border-gray-700 hover:border-gray-500 text-[10px] tracking-widest uppercase">
          {{ t('webmail_bridge.back_security') }}
        </router-link>
      </div>
    </div>
  </div>
</template>
