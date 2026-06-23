<script setup lang="ts">
import { computed, ref } from 'vue'
import { api, type Envelope, type AlertsData, type ConnectivityData, type FleetNodeStatus, type RpkiState, type HijackState } from '@/api/client'
import { usePolling } from '@/composables/usePolling'
import Chart from '@/components/Chart.vue'

const alerts = usePolling<Envelope<AlertsData>>((s) => api.alerts(s), 5000)
const conn   = usePolling<Envelope<ConnectivityData>>((s) => api.connectivity(s), 6000)
const fleet  = usePolling<Envelope<FleetNodeStatus[]>>((s) => api.fleet(s), 8000)
const rpki   = usePolling<Envelope<RpkiState>>((s) => api.rpki(s), 60000)
const hijack = usePolling<Envelope<HijackState>>((s) => api.hijack(s), 60000)

const rpkiState = computed(() => rpki.data.value?.data ?? null)
const hijackState = computed(() => hijack.data.value?.data ?? null)

const rpkiBusy = ref(false)
async function refreshRpki() {
  rpkiBusy.value = true
  try { await api.rpkiRefresh(); await rpki.refresh() } finally { rpkiBusy.value = false }
}

// Operator-adjustable auto-poll interval. The selected value reflects the
// server's effective interval_secs; a non-preset value (e.g. set via env) is
// added so the dropdown can still show it.
function fmtDur(secs: number): string {
  if (secs % 86400 === 0) return (secs / 86400) + ' 天'
  if (secs % 3600 === 0) return (secs / 3600) + ' 小时'
  if (secs % 60 === 0) return (secs / 60) + ' 分钟'
  return secs + ' 秒'
}
const rpkiIvPresets = [3600, 21600, 43200, 86400, 172800]
const rpkiIvOptions = computed(() => {
  const cur = rpkiState.value?.interval_secs ?? 86400
  const set = new Set(rpkiIvPresets)
  if (cur > 0) set.add(cur)
  return [...set].sort((a, b) => a - b).map((s) => ({ secs: s, label: fmtDur(s) }))
})
const rpkiIvBusy = ref(false)
async function setRpkiInterval(e: Event) {
  const secs = Number((e.target as HTMLSelectElement).value)
  if (!secs) return
  rpkiIvBusy.value = true
  try { await api.rpkiSetInterval(secs); await rpki.refresh() } finally { rpkiIvBusy.value = false }
}
const rpkiBadge = (v: string) => v === 'valid'
  ? 'text-emerald-400 border-emerald-700'
  : v === 'invalid' ? 'text-red-400 border-red-700' : 'text-amber-400 border-amber-700'

const fleetNodes  = computed(() => fleet.data.value?.data ?? [])
const activeAlerts = computed(() => alerts.data.value?.data?.active ?? [])
const probes       = computed(() => conn.data.value?.data?.probes ?? [])

const sevColor  = (s: string) => s === 'crit' ? 'text-red-500' : s === 'warn' ? 'text-amber-400' : 'text-blue-400'

function bgpProtocols(n: FleetNodeStatus) {
  return (n.protocols ?? []).filter(p => p.proto === 'BGP')
}
function loadColor(v: number): string {
  if (v >= 2) return 'text-red-500'
  if (v >= 1) return 'text-amber-400'
  return 'text-emerald-500'
}
function memColor(v: number): string {
  if (v >= 85) return 'text-red-500'
  if (v >= 70) return 'text-amber-400'
  return 'text-pink-400'
}
function cpuColor(v: number): string {
  if (v >= 80) return 'text-red-500'
  if (v >= 50) return 'text-amber-400'
  return 'text-emerald-500'
}
function fmtBytes(n: number): string {
  if (!n || n < 1) return '0 B/s'
  if (n < 1024) return `${n.toFixed(0)} B/s`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB/s`
  return `${(n / 1024 / 1024).toFixed(2)} MB/s`
}
function fmtAge(epoch: number): string {
  if (!epoch) return '—'
  const s = Math.floor(Date.now() / 1000) - epoch
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  return `${Math.floor(s / 3600)}h ago`
}
</script>

<template>
  <div class="space-y-4">
    <!-- Page header -->
    <div class="border border-gray-800 bg-gray-900 p-4 flex items-center justify-between flex-wrap gap-2">
      <div class="flex items-center gap-3">
        <span class="w-1.5 h-1.5 bg-emerald-500 animate-pulse"></span>
        <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">仪表盘 · Dashboard</h1>
        <span class="text-[10px] tracking-widest text-gray-700">{{ fleetNodes.length }} PoP</span>
      </div>
      <div class="text-[10px] tracking-widest text-gray-600 uppercase">
        <span v-if="fleet.error.value" class="text-red-500">ERR</span>
        <span v-else-if="fleet.loading.value" class="text-emerald-500">● sync</span>
        <span v-else class="text-gray-700">poll 8s · per-node sparkline 15s</span>
      </div>
    </div>

    <!-- Active alerts strip -->
    <div v-if="activeAlerts.length > 0" class="border border-red-500/60 bg-red-950/30 p-3">
      <div class="flex items-center gap-2 text-[10px] tracking-widest uppercase text-red-400 mb-2">
        <span class="w-1.5 h-1.5 bg-red-500 animate-pulse"></span>
        <span>{{ activeAlerts.length }} active alert{{ activeAlerts.length > 1 ? 's' : '' }}</span>
      </div>
      <ul class="space-y-1">
        <!-- Mobile: severity+title on one line, the (often long, monospaced)
             message wraps full-width below. sm+: single row, message right-
             aligned. min-w-0 + break lets the message shrink/wrap instead of
             pushing the row past the viewport (was causing horizontal scroll
             with messages like `down: cloudflare-v6(2606:4700:…)`). -->
        <li v-for="a in activeAlerts" :key="a.id"
            class="flex flex-col sm:flex-row sm:items-baseline gap-x-3 gap-y-0.5 text-xs">
          <span class="flex items-baseline gap-2 shrink-0">
            <span :class="['font-mono text-[10px] tracking-widest', sevColor(a.severity)]">[{{ a.severity.toUpperCase() }}]</span>
            <span class="text-gray-100">{{ a.title }}</span>
          </span>
          <span class="text-gray-500 min-w-0 break-all sm:ml-auto sm:text-right"
                style="overflow-wrap: anywhere;">{{ a.message }}</span>
        </li>
      </ul>
    </div>

    <!-- RPKI: ROA validity of our own announced prefixes (AS64500) -->
    <div v-if="rpkiState" class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex items-center justify-between gap-2 flex-wrap">
        <span>🛡️ RPKI · {{ rpkiState.asn }} 路由起源</span>
        <span class="flex items-center gap-2 normal-case">
          <span class="text-emerald-400">{{ rpkiState.valid }} valid</span>
          <span v-if="rpkiState.invalid" class="text-red-400">{{ rpkiState.invalid }} invalid</span>
          <span v-if="rpkiState.unknown" class="text-amber-400">{{ rpkiState.unknown }} unknown</span>
          <span class="text-gray-700">· {{ (rpkiState.prefixes ?? []).length }} 前缀</span>
          <label class="flex items-center gap-1 text-gray-500" title="自动轮询间隔（手动刷新随时可用）">
            <span>每</span>
            <select :value="rpkiState.interval_secs" @change="setRpkiInterval" :disabled="rpkiIvBusy"
              class="bg-gray-950 border border-gray-700 rounded px-1 py-0.5 text-gray-300 focus:border-emerald-600 disabled:opacity-40">
              <option v-for="o in rpkiIvOptions" :key="o.secs" :value="o.secs">{{ o.label }}</option>
            </select>
          </label>
          <button @click="refreshRpki" :disabled="rpkiBusy" title="立即刷新（不影响自动轮询间隔）"
            class="border border-gray-700 rounded px-2 py-0.5 text-gray-400 hover:border-emerald-600 hover:text-emerald-400 disabled:opacity-40">
            {{ rpkiBusy ? '刷新中…' : '↻ 刷新' }}</button>
        </span>
      </div>
      <div v-if="rpkiState.error" class="px-4 py-3 text-xs text-amber-400">查询失败:{{ rpkiState.error }}</div>
      <div v-else class="p-3 flex flex-wrap gap-1.5">
        <span v-for="p in (rpkiState.prefixes ?? [])" :key="p.prefix"
              :class="['inline-flex items-center gap-1.5 border rounded px-2 py-1 text-[11px] font-mono', rpkiBadge(p.validity)]"
              :title="p.validity + ' · ' + p.roas + ' ROA(s)'">
          {{ p.prefix }}
          <span class="opacity-70">{{ p.validity === 'valid' ? '✓' : p.validity === 'invalid' ? '⛔' : '❔' }}</span>
        </span>
        <span v-if="!(rpkiState.prefixes ?? []).length" class="text-[11px] text-gray-600 italic px-1">暂无已宣告前缀数据</span>
      </div>
      <!-- live route-origin-validation (soft-check) from a PoP -->
      <div v-if="rpkiState.rov" class="px-4 py-2 border-t border-gray-800 text-[11px] flex items-center gap-3 flex-wrap normal-case">
        <span class="text-gray-500">ROV · {{ rpkiState.rov.node }} (soft-check)</span>
        <span :class="rpkiState.rov.established ? 'text-emerald-400' : 'text-red-400'">
          {{ rpkiState.rov.established ? '● 验证器已连接' : '○ 验证器未连接' }}
        </span>
        <span class="text-gray-600">{{ rpkiState.rov.vrps.toLocaleString() }} VRPs</span>
        <span class="text-gray-700">收到路由:</span>
        <span class="text-emerald-400">{{ rpkiState.rov.valid.toLocaleString() }} valid</span>
        <span class="text-amber-400">{{ rpkiState.rov.unknown.toLocaleString() }} unknown</span>
        <span :class="rpkiState.rov.invalid ? 'text-red-400' : 'text-gray-500'">{{ rpkiState.rov.invalid.toLocaleString() }} invalid</span>
        <span class="text-gray-700 italic">· invalid 已拒绝(CF 验证器,enforce)</span>
      </div>
    </div>

    <!-- Prefix-hijack detection: live RIS Live stream watching our address space -->
    <div v-if="hijackState" class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex items-center justify-between gap-2 flex-wrap">
        <span>📡 前缀劫持监控 · RIS Live</span>
        <span class="flex items-center gap-2 normal-case">
          <span :class="hijackState.connected ? 'text-emerald-400' : 'text-red-400'">
            {{ hijackState.connected ? '● 已连接' : '○ 未连接' }}
          </span>
          <span class="text-gray-600">监视 {{ (hijackState.watching ?? []).length }} 前缀</span>
          <span :class="(hijackState.events ?? []).length ? 'text-red-400' : 'text-gray-700'">{{ (hijackState.events ?? []).length }} 可疑</span>
        </span>
      </div>
      <div v-if="hijackState.error && !hijackState.connected" class="px-4 py-3 text-xs text-amber-400">连接异常:{{ hijackState.error }}</div>
      <div v-else-if="!(hijackState.events ?? []).length" class="px-4 py-3 text-[11px] text-gray-600 italic">
        未发现他方宣告我方前缀(origin 非本 AS)。
      </div>
      <div v-else class="divide-y divide-gray-800/60">
        <div v-for="(ev, i) in (hijackState.events ?? []).slice(0, 8)" :key="i"
             class="px-4 py-2 text-[11px] flex flex-wrap items-center gap-x-3 gap-y-1">
          <span class="text-red-400">⛔</span>
          <span class="font-mono text-gray-200">{{ ev.prefix }}</span>
          <span class="text-gray-600">origin</span>
          <span class="font-mono text-red-400">{{ ev.origin }}</span>
          <span class="text-gray-700 font-mono truncate">path: {{ ev.as_path }}</span>
          <span class="text-gray-700 ml-auto">{{ new Date(ev.seen_at * 1000).toLocaleString() }}</span>
        </div>
      </div>
    </div>

    <!-- Per-node panels: every PoP gets the same treatment -->
    <div class="grid grid-cols-1 lg:grid-cols-3 gap-3">
      <div
        v-for="n in fleetNodes" :key="n.node.id"
        :class="[
          'border bg-gray-900 flex flex-col',
          n.ok ? 'border-gray-800' : 'border-red-500/60'
        ]"
      >
        <!-- Header strip -->
        <div :class="[
          'px-3 sm:px-4 py-2 border-b flex items-center justify-between gap-2 min-w-0 text-[10px] tracking-widest uppercase font-mono',
          n.ok ? 'border-gray-800 bg-gray-950/30' : 'border-red-500/60 bg-red-950/30'
        ]">
          <span class="flex items-center gap-2 min-w-0">
            <span class="shrink-0" :class="n.ok ? 'text-emerald-500 animate-pulse' : 'text-red-500'">●</span>
            <span class="text-gray-100 shrink-0">{{ n.node.id }}</span>
            <span class="text-gray-700 shrink-0">·</span>
            <span class="text-gray-500 truncate min-w-0">{{ n.node.country }} {{ n.node.label }}</span>
          </span>
          <span class="text-gray-600 hidden sm:inline shrink-0">{{ fmtAge(n.fetched_at) }}</span>
        </div>

        <!-- Failure body -->
        <div v-if="!n.ok" class="p-4 space-y-2">
          <div class="text-xs text-red-400 break-all">⨯ {{ n.error || 'unreachable' }}</div>
          <div class="text-[10px] tracking-widest text-gray-700">{{ n.node.address }} · {{ fmtAge(n.fetched_at) }}</div>
        </div>

        <!-- Healthy body -->
        <div v-else class="p-3 sm:p-4 space-y-3">
          <!-- Hostname / iface / uptime -->
          <div class="text-[10px] font-mono text-gray-600 flex items-center justify-between flex-wrap gap-x-2 gap-y-0.5">
            <span class="truncate text-gray-400">{{ n.hostname || n.node.address }}</span>
            <span class="text-gray-600">{{ n.iface || '—' }}</span>
          </div>

          <!-- 4 metric tiles, each with sparkline -->
          <div class="grid grid-cols-2 gap-2">
            <!-- CPU -->
            <div class="border border-gray-800 p-2">
              <div class="flex items-baseline justify-between">
                <span class="text-[9px] text-gray-600 tracking-widest uppercase">CPU</span>
                <span :class="['text-lg font-mono tabular-nums', cpuColor(n.cpu_pct)]">
                  {{ n.cpu_pct.toFixed(0) }}<span class="text-[10px] text-gray-600">%</span>
                </span>
              </div>
              <Chart
                v-if="n.cpu_series && n.cpu_series.length > 1"
                class="mt-1"
                :series="n.cpu_series"
                :height="28"
                :width="200"
                color="rgb(16 185 129)"
                :y-min="0"
                :y-max="100"
              />
              <div v-else class="mt-1 h-[28px] text-[9px] text-gray-700 tracking-widest flex items-center">awaiting samples…</div>
            </div>

            <!-- MEM -->
            <div class="border border-gray-800 p-2">
              <div class="flex items-baseline justify-between">
                <span class="text-[9px] text-gray-600 tracking-widest uppercase">MEM</span>
                <span :class="['text-lg font-mono tabular-nums', memColor(n.mem_pct)]">
                  {{ n.mem_pct.toFixed(0) }}<span class="text-[10px] text-gray-600">%</span>
                </span>
              </div>
              <Chart
                v-if="n.mem_series && n.mem_series.length > 1"
                class="mt-1"
                :series="n.mem_series"
                :height="28"
                :width="200"
                color="rgb(244 114 182)"
                :y-min="0"
                :y-max="100"
              />
              <div v-else class="mt-1 h-[28px] text-[9px] text-gray-700 tracking-widest flex items-center">awaiting samples…</div>
            </div>

            <!-- LOAD -->
            <div class="border border-gray-800 p-2">
              <div class="flex items-baseline justify-between">
                <span class="text-[9px] text-gray-600 tracking-widest uppercase">LOAD 1m</span>
                <span :class="['text-lg font-mono tabular-nums', loadColor(n.load_1)]">{{ n.load_1.toFixed(2) }}</span>
              </div>
              <Chart
                v-if="n.load_series && n.load_series.length > 1"
                class="mt-1"
                :series="n.load_series"
                :height="28"
                :width="200"
                color="rgb(96 165 250)"
              />
              <div v-else class="mt-1 h-[28px] text-[9px] text-gray-700 tracking-widest flex items-center">awaiting samples…</div>
            </div>

            <!-- NET RX -->
            <div class="border border-gray-800 p-2">
              <div class="flex items-baseline justify-between">
                <span class="text-[9px] text-gray-600 tracking-widest uppercase">NET RX</span>
                <span class="text-sm text-violet-400 font-mono tabular-nums truncate">{{ fmtBytes(n.net_rx_bps) }}</span>
              </div>
              <Chart
                v-if="n.net_rx_series && n.net_rx_series.length > 1"
                class="mt-1"
                :series="n.net_rx_series"
                :height="28"
                :width="200"
                color="rgb(167 139 250)"
              />
              <div v-else class="mt-1 h-[28px] text-[9px] text-gray-700 tracking-widest flex items-center">awaiting samples…</div>
            </div>
          </div>

          <!-- BGP peer compact list -->
          <div v-if="bgpProtocols(n).length" class="border-t border-gray-800 pt-3">
            <div class="flex items-center justify-between text-[10px] tracking-widest text-gray-600 uppercase mb-1.5">
              <span>BGP peers</span>
              <span class="text-gray-700">BIRD {{ n.bird_version || '?' }}</span>
            </div>
            <ul class="space-y-0.5 text-[11px] font-mono max-h-[180px] overflow-y-auto">
              <li v-for="p in bgpProtocols(n)" :key="p.name" class="flex items-center justify-between gap-2">
                <span class="flex items-center gap-2 truncate min-w-0">
                  <span :class="p.healthy ? 'text-emerald-500' : 'text-red-500'">●</span>
                  <span class="text-gray-200 truncate">{{ p.name }}</span>
                </span>
                <span :class="p.healthy ? 'text-gray-500' : 'text-red-400'" class="truncate text-[10px]">{{ p.info || p.state }}</span>
              </li>
            </ul>
          </div>

          <!-- Footer: uptime + scrape ssh latency -->
          <div class="text-[9px] tracking-widest text-gray-700 normal-case pt-2 border-t border-gray-800 flex items-center justify-between gap-2 flex-wrap">
            <span class="truncate">uptime: <span class="text-gray-500">{{ n.uptime || '—' }}</span></span>
            <span v-if="n.scrape_latency" class="text-gray-700">ssh {{ n.scrape_latency }}</span>
          </div>
        </div>
      </div>

      <div v-if="fleetNodes.length === 0" class="col-span-3 border border-gray-800 bg-gray-900 p-8 text-center text-[10px] tracking-widest text-gray-600 italic">
        // awaiting first fleet scrape
      </div>
    </div>

    <!-- Fleet-wide connectivity probes (this host's view) -->
    <div class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex items-center justify-between">
        <span>Connectivity probes</span>
        <span class="text-gray-700">view: ctrl-01</span>
      </div>
      <!-- Probes — overflow-x-auto so wide IPv6 / FQDN targets don't push
           the page wider than the viewport on phones. The target cell
           uses `break-all` instead of the previous hard 16ch truncate
           (which silently hid useful information even on desktop). -->
      <div class="overflow-x-auto">
        <table class="w-full text-xs font-mono">
          <thead class="text-[10px] text-gray-600 uppercase tracking-widest">
            <tr>
              <th class="text-left px-3 py-1.5">name</th>
              <th class="text-left px-3 py-1.5">target</th>
              <th class="text-right px-3 py-1.5">rtt</th>
              <th class="text-right px-3 py-1.5 hidden sm:table-cell">trend</th>
            </tr>
          </thead>
          <tbody class="text-gray-300">
            <tr v-for="p in probes" :key="p.name" class="border-t border-gray-800">
              <td class="px-3 py-1.5 whitespace-nowrap">
                <span :class="p.last_ok ? 'text-emerald-500' : 'text-red-500'">●</span>
                <span class="ml-2 text-gray-200">{{ p.name }}</span>
              </td>
              <td class="px-3 py-1.5 text-gray-500 break-all" style="overflow-wrap: anywhere;">{{ p.target }}</td>
              <td class="px-3 py-1.5 text-right tabular-nums whitespace-nowrap">
                <span v-if="p.last_ok" class="text-gray-200">{{ p.last_ms.toFixed(1) }}ms</span>
                <span v-else class="text-red-500">DOWN</span>
              </td>
              <td class="px-3 py-1.5 text-right hidden sm:table-cell">
                <div class="ml-auto w-[80px]">
                  <Chart :series="p.series" :height="20" :width="80" color="rgb(16 185 129)" :fill-below="false" />
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>
