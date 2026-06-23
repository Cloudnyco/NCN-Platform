<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { api, type FleetNodeStatus, type Envelope } from '@/api/client'
import { usePolling } from '@/composables/usePolling'
import Performance from '@/views/admin/Performance.vue'
import Observability from '@/views/admin/Observability.vue'

// Merged tab nav: /admin/perf folded in as the "perf" tab + the Grafana monitor
// as the "monitoring" tab, so nav stays slim. ?tab= deep-links a tab (the
// /admin/observability + /admin/perf redirects land here).
type FleetTab = 'overview' | 'perf' | 'monitoring'
const route = useRoute()
const initialTab = (route.query.tab === 'perf' || route.query.tab === 'monitoring') ? route.query.tab as FleetTab : 'overview'
const activeTab = ref<FleetTab>(initialTab)

const { data, error, lastUpdatedAt, loading } = usePolling<Envelope<FleetNodeStatus[]>>(
  (s) => api.fleet(s),
  10000
)

const nodes = computed(() => data.value?.data ?? [])

function bgpEstablished(n: FleetNodeStatus): number {
  return (n.protocols ?? []).filter(p => p.proto === 'BGP' && p.healthy).length
}
function bgpTotal(n: FleetNodeStatus): number {
  return (n.protocols ?? []).filter(p => p.proto === 'BGP').length
}
function wgPeerCount(n: FleetNodeStatus): number {
  let total = 0
  for (const i of (n.wg ?? [])) total += i.peers.length
  return total
}
function fmtAge(epoch: number): string {
  if (!epoch) return '—'
  const s = Math.floor(Date.now() / 1000) - epoch
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m ago`
}
const fmtAgo = computed(() =>
  lastUpdatedAt.value ? lastUpdatedAt.value.toLocaleTimeString('en-GB', { hour12: false }) : ''
)
</script>

<template>
  <div class="space-y-4">
    <div class="border border-gray-800 bg-gray-900 p-4">
      <div class="flex items-center justify-between flex-wrap gap-2">
        <div class="flex items-center gap-3">
          <span class="w-1.5 h-1.5 bg-emerald-500 animate-pulse"></span>
          <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">舰队 · Fleet</h1>
          <span class="text-[10px] tracking-widest text-gray-700">{{ nodes.length }} PoP</span>
        </div>
        <div class="text-[10px] tracking-widest text-gray-600 uppercase">
          <span :class="loading ? 'text-emerald-500' : 'text-gray-700'">● POLL 10s</span>
          <span v-if="error" class="text-red-500 ml-2">ERR</span>
          <span v-else-if="fmtAgo" class="text-gray-500 ml-2">SYNC · {{ fmtAgo }}</span>
        </div>
      </div>
      <p class="mt-2 text-[10px] tracking-widest text-gray-700 normal-case uppercase">
        // remote nodes scraped via SSH every 30s · cached server-side
      </p>
    </div>

    <!-- Tab nav (overview / perf — perf folded in from /admin/perf) -->
    <div class="border border-gray-800 bg-gray-900 overflow-x-auto">
      <div class="flex divide-x divide-gray-800 min-w-max">
        <button type="button" @click="activeTab = 'overview'"
          :class="['px-4 py-2.5 text-[10px] tracking-widest uppercase whitespace-nowrap transition-colors',
            activeTab === 'overview' ? 'text-emerald-400 bg-emerald-950/30 border-b-2 border-emerald-500'
                                     : 'text-gray-500 hover:text-gray-300 hover:bg-gray-900/40 border-b-2 border-transparent']">
          舰队 · overview
        </button>
        <button type="button" @click="activeTab = 'perf'"
          :class="['px-4 py-2.5 text-[10px] tracking-widest uppercase whitespace-nowrap transition-colors',
            activeTab === 'perf' ? 'text-emerald-400 bg-emerald-950/30 border-b-2 border-emerald-500'
                                 : 'text-gray-500 hover:text-gray-300 hover:bg-gray-900/40 border-b-2 border-transparent']">
          性能 · perf
        </button>
        <button type="button" @click="activeTab = 'monitoring'"
          :class="['px-4 py-2.5 text-[10px] tracking-widest uppercase whitespace-nowrap transition-colors',
            activeTab === 'monitoring' ? 'text-emerald-400 bg-emerald-950/30 border-b-2 border-emerald-500'
                                       : 'text-gray-500 hover:text-gray-300 hover:bg-gray-900/40 border-b-2 border-transparent']">
          监控 · grafana
        </button>
      </div>
    </div>

    <template v-if="activeTab === 'overview'">
    <!-- Node cards grid -->
    <div class="grid grid-cols-1 lg:grid-cols-3 gap-3">
      <div
        v-for="n in nodes" :key="n.node.id"
        :class="[
          'border bg-gray-900 flex flex-col',
          n.ok ? 'border-gray-800' : 'border-red-500/60'
        ]"
      >
        <!-- Header strip -->
        <div :class="[
          'px-4 py-2 border-b flex items-center justify-between gap-2 min-w-0 text-[10px] tracking-widest uppercase font-mono',
          n.ok ? 'border-gray-800 bg-gray-950/30' : 'border-red-500/60 bg-red-950/30'
        ]">
          <span class="flex items-center gap-2 shrink-0">
            <span :class="n.ok ? 'text-emerald-500 animate-pulse' : 'text-red-500'">●</span>
            <span class="text-gray-100">{{ n.node.id }}</span>
            <span v-if="n.node.local" class="text-emerald-700 text-[9px]">LOCAL</span>
          </span>
          <!-- The country + address subtitle is the most overflow-prone
               element on this card (IPv6 addresses can run ~40 chars).
               min-w-0 + truncate lets it ellipsis-cut gracefully on
               phones instead of pushing the right edge off-screen. -->
          <span class="text-gray-600 min-w-0 truncate text-right">{{ n.node.country }} · {{ n.node.address }}</span>
        </div>

        <!-- Body -->
        <div v-if="n.ok" class="p-4 space-y-3">
          <div class="flex items-baseline justify-between">
            <div>
              <div class="text-xs text-gray-300 font-mono">{{ n.node.label }}</div>
              <div class="text-[10px] text-gray-600 font-mono break-all">{{ n.hostname }}</div>
            </div>
          </div>

          <!-- Quick metrics -->
          <div class="grid grid-cols-3 gap-2 text-center">
            <div class="border border-gray-800 px-2 py-1.5">
              <div class="text-[9px] text-gray-600 tracking-widest uppercase">LOAD</div>
              <div :class="['text-base font-mono tabular-nums', n.load_1 > 2 ? 'text-amber-400' : 'text-emerald-500']">
                {{ n.load_1.toFixed(2) }}
              </div>
            </div>
            <div class="border border-gray-800 px-2 py-1.5">
              <div class="text-[9px] text-gray-600 tracking-widest uppercase">MEM</div>
              <div :class="['text-base font-mono tabular-nums', n.mem_pct > 80 ? 'text-amber-400' : 'text-pink-400']">
                {{ n.mem_pct.toFixed(0) }}<span class="text-[10px] text-gray-600">%</span>
              </div>
            </div>
            <div class="border border-gray-800 px-2 py-1.5">
              <div class="text-[9px] text-gray-600 tracking-widest uppercase">BGP</div>
              <div :class="['text-base font-mono tabular-nums', bgpEstablished(n) === bgpTotal(n) ? 'text-emerald-500' : 'text-red-500']">
                {{ bgpEstablished(n) }}/{{ bgpTotal(n) }}
              </div>
            </div>
          </div>

          <!-- BIRD version + WG peer count -->
          <div class="text-[10px] tracking-widest uppercase text-gray-600 grid grid-cols-2 gap-2 font-mono">
            <div>
              <span class="text-gray-700">BIRD</span>
              <span class="text-gray-300 ml-1">{{ n.bird_version || '—' }}</span>
            </div>
            <div>
              <span class="text-gray-700">WG peers</span>
              <span class="text-gray-300 ml-1">{{ wgPeerCount(n) }}</span>
            </div>
          </div>

          <!-- Protocols summary -->
          <div v-if="n.protocols && n.protocols.length" class="border-t border-gray-800 pt-3">
            <div class="text-[10px] tracking-widest text-gray-600 uppercase mb-1.5">protocols</div>
            <ul class="space-y-0.5 text-[11px] font-mono">
              <li v-for="p in n.protocols" :key="p.name" class="flex items-center justify-between gap-2 min-w-0">
                <span class="flex items-center gap-2 min-w-0">
                  <span class="shrink-0" :class="p.healthy ? 'text-emerald-500' : 'text-red-500'">●</span>
                  <span class="text-gray-200 truncate min-w-0">{{ p.name }}</span>
                  <span class="text-gray-700 text-[9px] shrink-0">{{ p.proto }}</span>
                </span>
                <!-- Status text — was clamped at 14ch which silently
                     ate useful info like "Established AS65001" → "Establ…".
                     Plain `truncate` lets it use whatever space remains
                     after the name on the same row. -->
                <span :class="p.healthy ? 'text-gray-500' : 'text-red-400'" class="truncate min-w-0 text-right">{{ p.info || p.state }}</span>
              </li>
            </ul>
          </div>

          <div class="text-[9px] tracking-widest text-gray-700 normal-case pt-2 border-t border-gray-800">
            uptime: <span class="text-gray-500">{{ n.uptime || '—' }}</span>
          </div>
          <div class="text-[9px] tracking-widest text-gray-700 normal-case">
            scraped {{ fmtAge(n.fetched_at) }}<span v-if="n.scrape_latency"> · ssh {{ n.scrape_latency }}</span>
          </div>
        </div>

        <!-- Failure state -->
        <div v-else class="p-4">
          <div class="text-xs text-red-400 normal-case tracking-normal break-all">
            ⨯ {{ n.error || 'unreachable' }}
          </div>
          <div class="mt-2 text-[10px] text-gray-700 normal-case">
            attempt {{ fmtAge(n.fetched_at) }}
          </div>
        </div>
      </div>

      <!-- Empty state -->
      <div v-if="nodes.length === 0" class="col-span-3 border border-gray-800 bg-gray-900 p-8 text-center text-gray-600 italic text-sm">
        no fleet data yet (first scrape pending, retry in 30s)
      </div>
    </div>
    </template>

    <template v-if="activeTab === 'perf'">
      <Performance />
    </template>

    <template v-if="activeTab === 'monitoring'">
      <Observability />
    </template>
  </div>
</template>
