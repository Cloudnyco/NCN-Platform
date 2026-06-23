<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, type Envelope, type FleetNodeStatus } from '@/api/client'
import { usePolling } from '@/composables/usePolling'
import Chart from '@/components/Chart.vue'
import NodeTabs from '@/components/NodeTabs.vue'

const { data, error, lastUpdatedAt, loading } = usePolling<Envelope<FleetNodeStatus[]>>(
  (s) => api.fleet(s), 5000
)

const nodes = computed(() => data.value?.data ?? [])
const selectedId = ref<string>('')
watch(nodes, (vs) => {
  if (vs.length && !vs.find(n => n.node.id === selectedId.value)) {
    selectedId.value = vs[0].node.id
  }
}, { immediate: true })

const node = computed(() => nodes.value.find(n => n.node.id === selectedId.value) || null)

function fmtBytes(n?: number): string {
  if (!n || n < 0) return '—'
  if (n < 1024) return `${n.toFixed(0)} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 ** 3) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 ** 3).toFixed(2)} GB`
}
function fmtBytesPerSec(n: number): string {
  return fmtBytes(n) + '/s'
}

const fmtAgo = computed(() =>
  lastUpdatedAt.value ? lastUpdatedAt.value.toLocaleTimeString('en-GB', { hour12: false }) : ''
)

const tabNodes = computed(() => nodes.value.map(n => ({
  id: n.node.id, ok: n.ok, country: n.node.country
})))
</script>

<template>
  <div class="space-y-4">
    <div class="border border-gray-800 bg-gray-900 p-4">
      <div class="flex items-center justify-between flex-wrap gap-2">
        <div class="flex items-center gap-3">
          <span class="w-1.5 h-1.5 bg-emerald-500 animate-pulse"></span>
          <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">性能 · Performance</h1>
        </div>
        <div class="text-[10px] tracking-widest text-gray-600 uppercase">
          <span :class="loading ? 'text-emerald-500' : 'text-gray-700'">● POLL 5s</span>
          <span v-if="error" class="text-red-500 ml-2">ERR · {{ error }}</span>
          <span v-else-if="fmtAgo" class="text-gray-500 ml-2">SYNC · {{ fmtAgo }}</span>
        </div>
      </div>
    </div>

    <NodeTabs v-model="selectedId" :nodes="tabNodes" />

    <div v-if="!node" class="border border-gray-800 bg-gray-900 p-6 text-center text-[10px] tracking-widest text-gray-600 italic">
      // awaiting first scrape
    </div>

    <template v-else-if="node.ok">
      <!-- Identity row -->
      <div class="border border-gray-800 bg-gray-900 p-3 text-[10px] tracking-widest text-gray-600 uppercase flex items-center justify-between flex-wrap gap-x-4 gap-y-1 font-mono">
        <span><span class="text-gray-700">host</span> <span class="text-gray-300 ml-1">{{ node.hostname || '—' }}</span></span>
        <span><span class="text-gray-700">iface</span> <span class="text-gray-300 ml-1">{{ node.iface || '—' }}</span></span>
        <span><span class="text-gray-700">uptime</span> <span class="text-gray-300 ml-1 normal-case">{{ node.uptime || '—' }}</span></span>
      </div>

      <div class="grid grid-cols-1 lg:grid-cols-2 gap-3">
        <!-- CPU -->
        <div class="border border-gray-800 bg-gray-900 p-4">
          <div class="flex justify-between items-baseline mb-2">
            <span class="text-[10px] tracking-widest text-gray-600 uppercase">CPU usage</span>
            <span class="text-3xl text-emerald-500 font-mono tabular-nums">
              {{ node.cpu_pct.toFixed(1) }}<span class="text-sm text-gray-600">%</span>
            </span>
          </div>
          <Chart :series="node.cpu_series ?? []" :height="120" :width="540" color="rgb(16 185 129)" :y-min="0" :y-max="100" :show-axis="true" />
        </div>

        <!-- Memory -->
        <div class="border border-gray-800 bg-gray-900 p-4">
          <div class="flex justify-between items-baseline mb-2">
            <span class="text-[10px] tracking-widest text-gray-600 uppercase">Memory used</span>
            <div class="text-right">
              <span class="text-3xl text-pink-400 font-mono tabular-nums">
                {{ node.mem_pct.toFixed(1) }}<span class="text-sm text-gray-600">%</span>
              </span>
              <div class="text-[10px] text-gray-600 tracking-widest uppercase">
                of {{ fmtBytes(node.mem_total) }}
              </div>
            </div>
          </div>
          <Chart :series="node.mem_series ?? []" :height="120" :width="540" color="rgb(244 114 182)" :y-min="0" :y-max="100" :show-axis="true" />
        </div>

        <!-- Load -->
        <div class="border border-gray-800 bg-gray-900 p-4">
          <div class="flex justify-between items-baseline mb-2">
            <span class="text-[10px] tracking-widest text-gray-600 uppercase">Load 1-minute</span>
            <span class="text-3xl text-blue-400 font-mono tabular-nums">
              {{ node.load_1.toFixed(2) }}
            </span>
          </div>
          <Chart :series="node.load_series ?? []" :height="120" :width="540" color="rgb(96 165 250)" :show-axis="true" />
        </div>

        <!-- Disk -->
        <div class="border border-gray-800 bg-gray-900 p-4">
          <div class="flex justify-between items-baseline mb-2">
            <span class="text-[10px] tracking-widest text-gray-600 uppercase">Disk · /</span>
            <div class="text-right">
              <span class="text-3xl text-amber-400 font-mono tabular-nums">
                {{ node.disk_pct.toFixed(1) }}<span class="text-sm text-gray-600">%</span>
              </span>
              <div class="text-[10px] text-gray-600 tracking-widest uppercase">
                of {{ fmtBytes(node.disk_total) }}
              </div>
            </div>
          </div>
          <Chart :series="node.disk_series ?? []" :height="120" :width="540" color="rgb(251 191 36)" :y-min="0" :y-max="100" :show-axis="true" />
        </div>

        <!-- Net RX -->
        <div class="border border-gray-800 bg-gray-900 p-4">
          <div class="flex justify-between items-baseline mb-2">
            <span class="text-[10px] tracking-widest text-gray-600 uppercase">Net RX · {{ node.iface || '—' }}</span>
            <span class="text-2xl text-violet-400 font-mono tabular-nums">
              {{ fmtBytesPerSec(node.net_rx_bps) }}
            </span>
          </div>
          <Chart :series="node.net_rx_series ?? []" :height="100" :width="540" color="rgb(167 139 250)" :show-axis="true" />
        </div>

        <!-- Net TX -->
        <div class="border border-gray-800 bg-gray-900 p-4">
          <div class="flex justify-between items-baseline mb-2">
            <span class="text-[10px] tracking-widest text-gray-600 uppercase">Net TX · {{ node.iface || '—' }}</span>
            <span class="text-2xl text-cyan-400 font-mono tabular-nums">
              {{ fmtBytesPerSec(node.net_tx_bps) }}
            </span>
          </div>
          <Chart :series="node.net_tx_series ?? []" :height="100" :width="540" color="rgb(34 211 238)" :show-axis="true" />
        </div>
      </div>

      <!-- Per-interface traffic Top-N (all non-lo ifaces incl. wg*/tun*) -->
      <div v-if="node.ifaces && node.ifaces.length" class="border border-gray-800 bg-gray-900 p-4">
        <div class="text-[10px] tracking-widest text-gray-600 uppercase mb-2">每接口流量 · Top {{ node.ifaces.length }}</div>
        <table class="w-full text-xs">
          <thead>
            <tr class="text-[10px] tracking-widest text-gray-600 uppercase border-b border-gray-800">
              <th class="text-left font-normal py-1">接口</th>
              <th class="text-right font-normal py-1">RX</th>
              <th class="text-right font-normal py-1">TX</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="i in node.ifaces" :key="i.name" class="border-b border-gray-800/40">
              <td class="py-1 font-mono text-gray-300">{{ i.name }}</td>
              <td class="py-1 text-right font-mono text-violet-400 tabular-nums">{{ fmtBytesPerSec(i.rx_bps) }}</td>
              <td class="py-1 text-right font-mono text-cyan-400 tabular-nums">{{ fmtBytesPerSec(i.tx_bps) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <div v-else class="border border-red-500/60 bg-red-950/30 p-4">
      <div class="text-xs text-red-400 break-all">⨯ {{ node.error || 'node unreachable' }}</div>
    </div>
  </div>
</template>
