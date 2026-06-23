<script setup lang="ts">
import { computed } from 'vue'
import { api, type Envelope, type FlowTop, type FlowEntry } from '@/api/client'
import { usePolling } from '@/composables/usePolling'

const flow = usePolling<Envelope<FlowTop>>((s) => api.flowTop(s), 10000)
const d = computed(() => flow.data.value?.data ?? null)

function fmtBytes(b: number): string {
  if (b >= 1e12) return (b / 1e12).toFixed(2) + ' TB'
  if (b >= 1e9) return (b / 1e9).toFixed(2) + ' GB'
  if (b >= 1e6) return (b / 1e6).toFixed(1) + ' MB'
  if (b >= 1e3) return (b / 1e3).toFixed(1) + ' KB'
  return b + ' B'
}
function fmtRate(bytes: number): string {
  const secs = d.value?.window_secs || 600
  const bps = (bytes * 8) / secs
  if (bps >= 1e9) return (bps / 1e9).toFixed(2) + ' Gbps'
  if (bps >= 1e6) return (bps / 1e6).toFixed(1) + ' Mbps'
  if (bps >= 1e3) return (bps / 1e3).toFixed(1) + ' kbps'
  return bps.toFixed(0) + ' bps'
}
const dims: { key: keyof FlowTop; label: string }[] = [
  { key: 'src_ip', label: '源 IP' },
  { key: 'dst_ip', label: '目的 IP' },
  { key: 'dst_as', label: '目的 AS' },
  { key: 'src_as', label: '源 AS' },
  { key: 'port', label: '端口 (proto/dport)' },
  { key: 'proto', label: '协议' },
]
function rows(k: keyof FlowTop): FlowEntry[] { return (d.value?.[k] as FlowEntry[]) ?? [] }
const empty = computed(() => !d.value || d.value.flows === 0)
</script>

<template>
  <div class="space-y-4">
    <div class="border border-gray-800 bg-gray-900 p-4">
      <div class="flex items-center justify-between flex-wrap gap-2">
        <div class="flex items-center gap-3">
          <span class="w-1.5 h-1.5 bg-emerald-500"></span>
          <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">流量 · Traffic</h1>
        </div>
        <div class="text-[10px] tracking-widest text-gray-600 uppercase">窗口 {{ Math.round((d?.window_secs ?? 600) / 60) }}m · {{ d?.flows ?? 0 }} flows · sFlow</div>
      </div>
    </div>

    <div v-if="empty" class="border border-gray-800 bg-gray-900 px-4 py-8 text-center text-sm text-gray-600">
      暂无流量数据 —— 需要先部署 sFlow 收集器(goflow2)并在各 PoP 开启 hsflowd 导出(见 deploy/sflow/)。
    </div>

    <template v-else>
      <!-- direction totals -->
      <div class="grid grid-cols-3 gap-3">
        <div class="border border-gray-800 bg-gray-900 p-3">
          <div class="text-[10px] tracking-widest text-gray-600 uppercase">入向 (ingress)</div>
          <div class="text-lg text-violet-400 font-mono mt-1">{{ fmtRate(d!.in_bytes) }}</div>
          <div class="text-[10px] text-gray-600">{{ fmtBytes(d!.in_bytes) }}</div>
        </div>
        <div class="border border-gray-800 bg-gray-900 p-3">
          <div class="text-[10px] tracking-widest text-gray-600 uppercase">出向 (egress)</div>
          <div class="text-lg text-cyan-400 font-mono mt-1">{{ fmtRate(d!.out_bytes) }}</div>
          <div class="text-[10px] text-gray-600">{{ fmtBytes(d!.out_bytes) }}</div>
        </div>
        <div class="border border-gray-800 bg-gray-900 p-3">
          <div class="text-[10px] tracking-widest text-gray-600 uppercase">过境 (transit)</div>
          <div class="text-lg text-gray-400 font-mono mt-1">{{ fmtRate(d!.transit_bytes) }}</div>
          <div class="text-[10px] text-gray-600">{{ fmtBytes(d!.transit_bytes) }}</div>
        </div>
      </div>

      <!-- top-N tables per dimension -->
      <div class="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
        <div v-for="dim in dims" :key="dim.key" class="border border-gray-800 bg-gray-900">
          <div class="px-3 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase">{{ dim.label }}</div>
          <div class="p-2">
            <div v-if="!rows(dim.key).length" class="text-[11px] text-gray-700 italic px-1 py-2">无数据</div>
            <table v-else class="w-full text-[11px]">
              <tbody>
                <tr v-for="e in rows(dim.key)" :key="e.key" class="border-b border-gray-800/40 last:border-0">
                  <td class="py-0.5 pr-2 font-mono text-gray-300 truncate max-w-[160px]" :title="e.key">{{ e.key }}</td>
                  <td class="py-0.5 text-right text-gray-400 whitespace-nowrap">{{ fmtRate(e.bytes) }}</td>
                  <td class="py-0.5 pl-2 text-right text-gray-600 whitespace-nowrap">{{ fmtBytes(e.bytes) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
