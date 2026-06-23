<script setup lang="ts">
// RoleMailboxRecovery — admin utility for resetting the 5 fixed role
// mailbox passwords (postmaster/noc/hostmaster/abuse/security on
// dovecot at pop-03). Mints a one-shot URL via the operator-mail-bridge
// HMAC key; the URL opens a no-auth page on mail.example.com that the
// admin hands to whoever needs the reset. Burns on first redemption,
// 15-min TTL.
//
// Extracted from src/views/admin/Security.vue (#306 D3). State is
// fully owned by this component — no props, no shared parent state,
// no emits.

import { ref, onUnmounted } from 'vue'
import { api } from '@/api/client'
import { copyToClipboard } from '@/utils/clipboard'

const ROLE_MAILBOXES = ['postmaster', 'noc', 'hostmaster', 'abuse', 'security'] as const
type RoleMailbox = typeof ROLE_MAILBOXES[number]

const roleRecoverBusy = ref<RoleMailbox | null>(null)
const roleRecoverErr = ref<string | null>(null)

interface MintedRoleURL {
  mailbox: string
  url: string
  expiresAt: number  // unix seconds
}
const roleRecoverMinted = ref<Record<string, MintedRoleURL | null>>({})

async function mintRoleRecover(mb: RoleMailbox) {
  if (roleRecoverBusy.value) return
  roleRecoverBusy.value = mb
  roleRecoverErr.value = null
  try {
    const env = await api.mailRoleRecover(mb)
    if (!env.ok || !env.data) throw new Error(env.error || 'mint failed')
    roleRecoverMinted.value = {
      ...roleRecoverMinted.value,
      [mb]: {
        mailbox: env.data.mailbox,
        url: env.data.url,
        expiresAt: Math.floor(new Date(env.data.expires_at).getTime() / 1000),
      },
    }
  } catch (e: unknown) {
    roleRecoverErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    roleRecoverBusy.value = null
  }
}

// 1-second tick so the countdown text refreshes live. Cheap: 5 cards
// each compute a small subtraction + format. The interval is paused
// when the component unmounts.
const nowTick = ref(Date.now())
const tickInterval = window.setInterval(() => { nowTick.value = Date.now() }, 1000)
onUnmounted(() => window.clearInterval(tickInterval))

function roleRecoverCountdown(expiresAt: number): string {
  // Reading nowTick here registers the dependency so Vue re-evaluates
  // the binding once per second.
  const s = expiresAt - Math.floor(nowTick.value / 1000)
  if (s <= 0) return 'expired'
  const m = Math.floor(s / 60), sec = s % 60
  return `${m}m ${sec.toString().padStart(2, '0')}s`
}
</script>

<template>
  <div class="border border-gray-800 bg-gray-900">
    <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex justify-between flex-wrap gap-2">
      <span>邮箱角色账户恢复 · role mailbox recovery</span>
      <span class="text-gray-700">one-shot · 15-min TTL · 5 role mailboxes only</span>
    </div>

    <div class="p-4 text-xs text-gray-500 normal-case tracking-normal leading-relaxed border-b border-gray-800">
      点 "mint URL" 给指定 role 邮箱生成一条一次性密码重置链接。链接 15 分钟内有效,且只能用一次 — 设置完密码后链接立即失效。
      这条链路只接受
      <span class="text-emerald-400 font-mono">postmaster / noc / hostmaster / abuse / security</span> 这 5 个 role mailbox,
      普通用户邮箱不受影响。
    </div>

    <ul class="divide-y divide-gray-800">
      <li v-for="mb in ROLE_MAILBOXES" :key="mb" class="p-4 flex flex-col gap-2">
        <div class="flex items-center justify-between gap-3 flex-wrap">
          <div>
            <div class="text-sm text-gray-100 font-mono">{{ mb }}@example.com</div>
            <div v-if="roleRecoverMinted[mb]" class="text-[10px] text-amber-400 mt-0.5 normal-case tracking-normal">
              URL active · expires in {{ roleRecoverCountdown(roleRecoverMinted[mb]!.expiresAt) }}
            </div>
          </div>
          <button
            @click="mintRoleRecover(mb)"
            :disabled="roleRecoverBusy === mb"
            class="px-4 py-2 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-[10px] tracking-widest uppercase transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
          >{{ roleRecoverBusy === mb ? '◌ minting...' : (roleRecoverMinted[mb] ? '↻ mint new URL' : '▶ mint URL') }}</button>
        </div>

        <!-- Minted URL display: pinned warning + copy button -->
        <div v-if="roleRecoverMinted[mb]"
             class="border border-amber-700/60 bg-amber-950/20 p-3 space-y-2">
          <div class="text-[10px] tracking-widest text-amber-400 uppercase">
            ⚠ ONE-TIME LINK · copy and hand it off · burns on first use
          </div>
          <div class="font-mono text-[10px] break-all text-gray-200 select-all">{{ roleRecoverMinted[mb]!.url }}</div>
          <div class="flex flex-wrap gap-2">
            <button
              @click="copyToClipboard(roleRecoverMinted[mb]!.url)"
              class="px-3 py-1.5 border border-gray-700 hover:border-emerald-500 text-[10px] tracking-widest uppercase text-gray-400 hover:text-emerald-400"
            >📋 copy</button>
            <a :href="roleRecoverMinted[mb]!.url" target="_blank" rel="noopener"
               class="px-3 py-1.5 border border-gray-700 hover:border-emerald-500 text-[10px] tracking-widest uppercase text-gray-400 hover:text-emerald-400">
              ↗ open
            </a>
          </div>
        </div>
      </li>
    </ul>

    <div v-if="roleRecoverErr" class="p-4 border-t border-red-500/40 bg-red-950/20 text-xs text-red-400 normal-case tracking-normal break-all">
      ⨯ {{ roleRecoverErr }}
    </div>
  </div>
</template>
