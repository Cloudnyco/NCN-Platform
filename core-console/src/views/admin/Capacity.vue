<script setup lang="ts">
import { computed, ref } from 'vue'
import { api, type Envelope, type CapacityData, type CapNodeView, type CapDay } from '@/api/client'
import { usePolling } from '@/composables/usePolling'
import Chart from '@/components/Chart.vue'

const cap = usePolling<Envelope<CapacityData>>((s) => api.capacity(s), 60000)
const data = computed(() => cap.data.value?.data ?? null)
const nodes = computed(() => data.value?.nodes ?? [])

// Per-node capacity edit buffer (Mbps). Seeded from the server value on first render.
const edit = ref<Record<string, number | null>>({})
const busy = ref<Record<string, boolean>>({})
function capModel(n: CapNodeView): number | null {
  if (edit.value[n.node] !== undefined) return edit.value[n.node]
  return n.capacity_mbps > 0 ? n.capacity_mbps : null
}
async function saveCap(n: CapNodeView) {
  const v = edit.value[n.node]
  busy.value[n.node] = true
  try { await api.setLinkCapacity(n.node, Number(v) || 0); await cap.refresh() }
  finally { busy.value[n.node] = false }
}

// series helpers — turn daily p95 buckets into Chart {t,v} points.
function points(days: CapDay[] | undefined): { t: number; v: number }[] {
  if (!days) return []
  return days.map((d, i) => ({ t: i, v: Math.round(d.p95 * 10) / 10 }))
}
function latest(days: CapDay[] | undefined): number {
  if (!days || !days.length) return 0
  return Math.round(days[days.length - 1].p95 * 10) / 10
}
// busier direction's latest p95 → utilization vs capacity (for the headline bar).
function utilPct(n: CapNodeView): number | null {
  if (n.capacity_mbps <= 0) return null
  const rx = latest(n.series['net_rx_total_mbps'])
  const tx = latest(n.series['net_tx_total_mbps'])
  return Math.round((Math.max(rx, tx) / n.capacity_mbps) * 1000) / 10
}
function etaLabel(n: CapNodeView): string {
  if (n.eta_days < 0) return '未知'
  if (n.eta_days === 0) return '已达饱和阈值'
  if (n.eta_days > 3650) return '>10 年'
  return `≈ ${Math.round(n.eta_days)} 天`
}
function etaClass(n: CapNodeView): string {
  if (n.eta_days < 0) return 'text-gray-500'
  if (n.eta_days === 0 || n.eta_days < 30) return 'text-red-400'
  if (n.eta_days < 90) return 'text-amber-400'
  return 'text-emerald-400'
}

const metricLabels: Record<string, string> = {
  net_rx_total_mbps: '总入向 (Mbps)',
  net_tx_total_mbps: '总出向 (Mbps)',
  cpu_pct: 'CPU %',
  mem_pct: '内存 %',
  disk_pct: '磁盘 %',
}
const metricColors: Record<string, string> = {
  net_rx_total_mbps: 'rgb(139 92 246)',
  net_tx_total_mbps: 'rgb(34 211 238)',
  cpu_pct: 'rgb(16 185 129)',
  mem_pct: 'rgb(251 191 36)',
  disk_pct: 'rgb(244 63 94)',
}
const order = ['net_rx_total_mbps', 'net_tx_total_mbps', 'cpu_pct', 'mem_pct', 'disk_pct']
function metricsFor(n: CapNodeView): string[] {
  return order.filter((m) => (n.series[m]?.length ?? 0) > 0)
}
</script>

<template>
  <div class="space-y-4">
    <div class="border border-gray-800 bg-gray-900 p-4">
      <div class="flex items-center justify-between flex-wrap gap-2">
        <div class="flex items-center gap-3">
          <span class="w-1.5 h-1.5 bg-emerald-500"></span>
          <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">容量 · Capacity</h1>
        </div>
        <div class="text-[10px] tracking-widest text-gray-600 uppercase">每日 p95 趋势 · 链路饱和预测</div>
      </div>
    </div>

    <div v-if="!nodes.length" class="border border-gray-800 bg-gray-900 px-4 py-8 text-center text-sm text-gray-600">
      暂无容量数据 —— 趋势需要积累若干天的每日样本后才会显现。
    </div>

    <div v-for="n in nodes" :key="n.node" class="border border-gray-800 bg-gray-900">
      <!-- header -->
      <div class="px-4 py-2 border-b border-gray-800 flex items-center justify-between gap-3 flex-wrap">
        <div class="flex items-center gap-3">
          <span class="font-mono text-sm text-gray-200">{{ n.node }}</span>
          <span class="text-[11px] text-gray-500">距饱和:
            <span :class="etaClass(n)">{{ etaLabel(n) }}</span>
          </span>
          <span v-if="utilPct(n) !== null" class="text-[11px] text-gray-500">
            当前利用率: <span class="text-gray-300">{{ utilPct(n) }}%</span>
          </span>
        </div>
        <!-- link capacity editor -->
        <label class="flex items-center gap-1.5 text-[11px] text-gray-500">
          <span>链路容量</span>
          <input type="number" min="0" step="100" :value="capModel(n)"
                 @input="edit[n.node] = ($event.target as HTMLInputElement).valueAsNumber"
                 placeholder="未设置"
                 class="w-24 bg-black border border-gray-700 px-1.5 py-0.5 text-gray-200 text-right focus:border-emerald-700" />
          <span>Mbps</span>
          <button @click="saveCap(n)" :disabled="busy[n.node]"
                  class="border border-gray-700 px-2 py-0.5 text-gray-400 hover:border-emerald-600 hover:text-emerald-400 disabled:opacity-40">
            {{ busy[n.node] ? '保存中…' : '保存' }}</button>
        </label>
      </div>

      <!-- per-metric trend charts -->
      <div class="p-3 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <div v-for="m in metricsFor(n)" :key="m" class="space-y-1">
          <div class="flex items-baseline justify-between text-[11px]">
            <span class="text-gray-400">{{ metricLabels[m] || m }}</span>
            <span class="text-gray-300 font-mono">p95 {{ latest(n.series[m]) }}</span>
          </div>
          <Chart :series="points(n.series[m])" :color="metricColors[m]" :height="56" :width="260" :unit="''" show-axis />
          <div class="text-[10px] text-gray-700">{{ (n.series[m]?.length ?? 0) }} 天</div>
        </div>
      </div>
      <div v-if="!metricsFor(n).length" class="px-4 pb-3 text-[11px] text-gray-600 italic">该节点暂无足够每日样本。</div>
    </div>
  </div>
</template>
