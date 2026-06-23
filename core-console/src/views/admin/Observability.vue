<script setup lang="ts">
// Embedded Grafana — the "NCN Control Plane" dashboard, iframed same-origin from
// admin.example.com/grafana (ncn-api reverse-proxies it behind an admin session;
// Grafana itself is localhost-only + anonymous Viewer, reached only through that
// gated proxy + the tyo→pop-03 tunnel). Kiosk mode hides Grafana's own chrome.
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const session = useSessionStore()
const isAdmin = computed(() => session.role === 'admin')

// Cache-bust to force a fresh load on manual refresh.
const nonce = ref(0)
const base = '/grafana/d/ncn-overview/ncn-control-plane'
const src = computed(() => `${base}?kiosk&theme=dark&refresh=30s&_=${nonce.value}`)
function reload() { nonce.value++ }
</script>

<template>
  <div class="flex flex-col h-[calc(100vh-13rem)] min-h-[460px]">
    <div class="shrink-0 border border-gray-800 bg-gray-900 px-4 py-2 flex items-center justify-between gap-2">
      <div class="flex items-center gap-3 min-w-0">
        <span class="w-1.5 h-1.5 bg-emerald-500 animate-pulse"></span>
        <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">{{ t('admin.nav.observability') }}</h1>
        <span class="text-[10px] tracking-widest text-gray-600 hidden sm:inline">Grafana · NCN Control Plane</span>
      </div>
      <div class="flex items-center gap-2 shrink-0">
        <button v-if="isAdmin" @click="reload"
          class="px-2 py-1 border border-gray-700 text-[10px] uppercase tracking-widest text-gray-400 hover:border-emerald-600 hover:text-emerald-400 transition-colors">刷新</button>
        <a v-if="isAdmin" :href="base + '?theme=dark'" target="_blank" rel="noopener"
          class="px-2 py-1 border border-gray-700 text-[10px] uppercase tracking-widest text-gray-400 hover:border-emerald-600 hover:text-emerald-400 transition-colors">新标签打开 ↗</a>
      </div>
    </div>

    <div v-if="!isAdmin" class="flex-1 border border-x border-b border-gray-800 bg-gray-900 flex items-center justify-center">
      <p class="text-[11px] tracking-widest text-gray-600 italic">// 监控仪表盘需要管理员权限</p>
    </div>
    <iframe v-else :key="nonce" :src="src" title="Grafana"
      class="flex-1 w-full border border-x border-b border-gray-800 bg-[#111217]"></iframe>
  </div>
</template>
