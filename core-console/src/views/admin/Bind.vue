<script setup lang="ts">
// Telegram bind landing page. Reached from the bot's /bind link
// (/admin/bind?t=<token>). The route is under /admin so the router guard has
// already forced an authenticated operator session by the time we mount; here
// we peek the one-time ticket, show which Telegram account it carries, and on
// confirm bind it to the logged-in operator. No Telegram Login Widget — the
// identity comes from the /bind message the bot minted.
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { api } from '@/api/client'
import { useSessionStore } from '@/stores/session'

const route = useRoute()
const session = useSessionStore()

type Phase = 'loading' | 'ready' | 'binding' | 'done' | 'error'
const phase = ref<Phase>('loading')
const errorMsg = ref('')
const tgUsername = ref('')
const tgId = ref('')

const token = ref('')

function tgLabel() {
  if (tgUsername.value) return '@' + tgUsername.value
  if (tgId.value) return 'Telegram ID ' + tgId.value
  return 'this Telegram account'
}

async function peek() {
  phase.value = 'loading'
  errorMsg.value = ''
  const env = await api.tgBindPeek(token.value)
  if (!env.ok || !env.data) {
    phase.value = 'error'
    errorMsg.value = env.error || '链接已失效或过期'
    return
  }
  tgUsername.value = env.data.telegram_username || ''
  tgId.value = env.data.telegram_id || ''
  phase.value = 'ready'
}

async function confirmBind() {
  phase.value = 'binding'
  errorMsg.value = ''
  const env = await api.tgBindConfirm(token.value)
  if (!env.ok || !env.data) {
    phase.value = 'error'
    errorMsg.value = env.error || '绑定失败'
    return
  }
  tgUsername.value = env.data.telegram_username || tgUsername.value
  phase.value = 'done'
}

onMounted(() => {
  token.value = String(route.query.t || '')
  if (!token.value) {
    phase.value = 'error'
    errorMsg.value = '缺少绑定令牌 — 请在机器人里重新发送 /bind'
    return
  }
  peek()
})
</script>

<template>
  <div class="min-h-screen flex flex-col items-center justify-center bg-gray-950 text-gray-300 px-4">
    <div class="w-full max-w-md border border-gray-800 bg-gray-900 px-5 sm:px-8 py-6 space-y-4 rounded">
      <div class="flex items-center gap-2 text-emerald-400">
        <span class="text-lg">🔐</span>
        <h1 class="text-sm sm:text-base font-semibold tracking-wide">绑定 Telegram 运维身份</h1>
      </div>

      <p class="text-xs text-gray-500">
        当前登录运维账户:
        <span class="text-gray-300 font-mono">{{ session.operator || '—' }}</span>
      </p>

      <!-- loading -->
      <div v-if="phase === 'loading'" class="text-xs text-gray-400 py-4">
        正在校验绑定链接…
      </div>

      <!-- ready: confirm -->
      <div v-else-if="phase === 'ready'" class="space-y-4">
        <p class="text-sm">
          把 <span class="text-emerald-400 font-mono">{{ tgLabel() }}</span>
          绑定到运维账户 <span class="text-gray-100 font-mono">{{ session.operator }}</span> ?
        </p>
        <p class="text-[11px] text-gray-500">
          绑定后,该 Telegram 账户即可在机器人里使用 <code>/netadmin</code>、<code>/manage</code> 等运维命令。
        </p>
        <button
          class="w-full bg-emerald-700 hover:bg-emerald-600 text-white text-sm py-2 rounded transition-colors"
          @click="confirmBind"
        >
          确认绑定
        </button>
      </div>

      <!-- binding -->
      <div v-else-if="phase === 'binding'" class="text-xs text-gray-400 py-4">
        正在绑定…
      </div>

      <!-- done -->
      <div v-else-if="phase === 'done'" class="space-y-3">
        <p class="text-sm text-emerald-400">
          ✓ 已绑定 <span class="font-mono">{{ tgLabel() }}</span> → <span class="font-mono">{{ session.operator }}</span>
        </p>
        <p class="text-[11px] text-gray-500">
          回到 Telegram,发送 <code>/whoami</code> 验证,或直接使用 <code>/netadmin</code>。
        </p>
        <router-link
          to="/admin/security"
          class="block w-full text-center border border-gray-700 hover:border-gray-500 text-sm py-2 rounded transition-colors"
        >
          打开安全设置
        </router-link>
      </div>

      <!-- error -->
      <div v-else class="space-y-3">
        <div class="border border-red-900 bg-black px-3 py-2 text-xs text-red-400">
          <span class="text-red-500 tracking-widest">[FAIL]</span> {{ errorMsg }}
        </div>
        <button
          v-if="token"
          class="w-full border border-gray-700 hover:border-gray-500 text-sm py-2 rounded transition-colors"
          @click="peek"
        >
          重试
        </button>
        <p class="text-[11px] text-gray-500">
          若链接已过期,请回到 Telegram 私聊机器人重新发送 <code>/bind</code>。
        </p>
      </div>
    </div>
  </div>
</template>
