<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, type Envelope, type FleetNodeStatus, type TSSample, type WGIface, type NetTunnel, type SLATarget, type DriftState, type ConfigDecl } from '@/api/client'
import { usePolling } from '@/composables/usePolling'
import Chart from '@/components/Chart.vue'
import NodeTabs from '@/components/NodeTabs.vue'
import Bird from '@/views/admin/Bird.vue'

// Merged tab nav: /admin/bird folded in as the "bird" tab. Bird brings its
// own per-node NodeTabs, so we only render Connectivity's NodeTabs in the
// probes branch to avoid two stacked node-selectors.
type ConnTab = 'probes' | 'bird'
const activeTab = ref<ConnTab>('probes')

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
const probes = computed(() => node.value?.probes ?? [])

// ── SLA targets editor (sla.go) — every PoP probes these; status page shows SLO.
const slaList = ref<SLATarget[]>([])
const slaBusy = ref(false)
const slaMsg = ref('')
async function loadSLA() {
  try { const r = await api.slaTargets(); if (r.ok) slaList.value = r.data?.targets ?? [] } catch { /* ignore */ }
}
loadSLA()
function addSLA() {
  slaList.value.push({ name: '', target: '', type: 'ping6', slo_pct: 99.9, rtt_budget_ms: 0 })
}
function removeSLA(i: number) { slaList.value.splice(i, 1) }
async function saveSLA() {
  slaBusy.value = true; slaMsg.value = ''
  try {
    const r = await api.setSLATargets(slaList.value)
    if (r.ok) { slaList.value = r.data?.targets ?? []; slaMsg.value = '已保存 · 下次抓取生效' }
    else slaMsg.value = r.error || '保存失败'
  } catch (e: unknown) { slaMsg.value = e instanceof Error ? e.message : String(e) }
  finally { slaBusy.value = false }
}

// ── Config drift (configdrift.go) — actual vs declared baseline.
const drift = ref<DriftState[]>([])
const driftBusy = ref<Record<string, boolean>>({})
const driftMsg = ref<Record<string, string>>({})
const diffOpen = ref('')
const diffData = ref<{ declared: ConfigDecl; live: ConfigDecl } | null>(null)
const rbConfirm = ref<Record<string, string>>({})
async function loadDrift() { try { const r = await api.drift(); if (r.ok) drift.value = r.data?.nodes ?? [] } catch { /* ignore */ } }
loadDrift()
function driftLabel(d: DriftState): string {
  if (!d.has_baseline) return '未采纳基线'
  if (d.error) return '检查失败'
  if (!d.drift) return '✓ 与基线一致'
  const w: string[] = []
  if (d.bird_drift) w.push('bird'); if (d.filters_drift) w.push('filters'); if (d.nft_drift) w.push('nft')
  return '⚠ 漂移: ' + w.join(', ')
}
async function adoptBaseline(id: string) {
  driftBusy.value[id] = true; driftMsg.value[id] = ''
  try { const r = await api.configAdopt(id); driftMsg.value[id] = r.ok ? '已采纳为基线' : (r.error || '失败'); if (r.ok) await loadDrift() }
  catch (e: unknown) { driftMsg.value[id] = e instanceof Error ? e.message : String(e) }
  finally { driftBusy.value[id] = false }
}
async function showDiff(id: string) {
  if (diffOpen.value === id) { diffOpen.value = ''; diffData.value = null; return }
  driftBusy.value[id] = true; driftMsg.value[id] = ''
  try { const r = await api.configDiff(id); if (r.ok) { diffData.value = r.data || null; diffOpen.value = id } else driftMsg.value[id] = r.error || '失败' }
  catch (e: unknown) { driftMsg.value[id] = e instanceof Error ? e.message : String(e) }
  finally { driftBusy.value[id] = false }
}
async function doRollback(id: string) {
  driftBusy.value[id] = true; driftMsg.value[id] = ''
  try {
    const r = await api.configRollback(id, rbConfirm.value[id] || '')
    driftMsg.value[id] = r.ok ? '已回滚 BIRD 到基线' : (r.error || '失败')
    if (r.ok) { rbConfirm.value[id] = ''; diffOpen.value = ''; await loadDrift() }
  } catch (e: unknown) { driftMsg.value[id] = e instanceof Error ? e.message : String(e) }
  finally { driftBusy.value[id] = false }
}

// WireGuard mesh — group all nodes' WG interfaces under their owning node
// so the full mesh topology is visible in one section regardless of which
// tab is selected.
interface WGGroup { nodeId: string; country: string; ok: boolean; error?: string; ifaces: WGIface[] }
const wgGroups = computed<WGGroup[]>(() => nodes.value.map(n => ({
  nodeId:  n.node.id,
  country: n.node.country,
  ok:      n.ok,
  error:   n.error,
  ifaces:  n.wg ?? []
})))
const wgTotalIfaces = computed(() => wgGroups.value.reduce((s, g) => s + g.ifaces.length, 0))
const wgTotalPeers  = computed(() =>
  wgGroups.value.reduce((s, g) => s + g.ifaces.reduce((a, i) => a + i.peers.length, 0), 0)
)

// GRE / VXLAN mesh — same grouping pattern as WG.
interface TunGroup { nodeId: string; country: string; ok: boolean; error?: string; tunnels: NetTunnel[] }
const tunGroups = computed<TunGroup[]>(() => nodes.value.map(n => ({
  nodeId:  n.node.id,
  country: n.node.country,
  ok:      n.ok,
  error:   n.error,
  tunnels: n.tunnels ?? []
})))
const tunTotal = computed(() => tunGroups.value.reduce((s, g) => s + g.tunnels.length, 0))

function kindColor(k: string): string {
  switch (k) {
    case 'gre':    return 'text-amber-400'
    case 'gretap': return 'text-amber-300'
    case 'ip6gre': return 'text-sky-400'
    case 'vxlan':  return 'text-pink-400'
    default:       return 'text-gray-400'
  }
}

function rtt(ms: number): string { return ms.toFixed(1) + ' ms' }
function lossPct(samples: TSSample[]): number {
  if (!samples.length) return 0
  return samples.filter(s => s.v < 0).length / samples.length * 100
}
function avgRTT(samples: TSSample[]): number {
  const ok = samples.filter(s => s.v >= 0).map(s => s.v)
  if (!ok.length) return 0
  return ok.reduce((a, b) => a + b, 0) / ok.length
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
      <div class="flex justify-between items-center flex-wrap gap-2">
        <div class="flex items-center gap-3">
          <span class="w-1.5 h-1.5 bg-emerald-500"></span>
          <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">连通性 · Connectivity</h1>
        </div>
        <div class="text-[10px] tracking-widest text-gray-600 uppercase">
          <span :class="loading ? 'text-emerald-500' : 'text-gray-700'">● POLL 5s</span>
          <span v-if="error" class="text-red-500 ml-2">ERR · {{ error }}</span>
          <span v-else-if="fmtAgo" class="text-gray-500 ml-2">SYNC · {{ fmtAgo }}</span>
        </div>
      </div>
    </div>

    <!-- Tab nav (probes / bird — bird folded in from /admin/bird) -->
    <div class="border border-gray-800 bg-gray-900 overflow-x-auto">
      <div class="flex divide-x divide-gray-800 min-w-max">
        <button type="button" @click="activeTab = 'probes'"
          :class="['px-4 py-2.5 text-[10px] tracking-widest uppercase whitespace-nowrap transition-colors',
            activeTab === 'probes' ? 'text-emerald-400 bg-emerald-950/30 border-b-2 border-emerald-500'
                                   : 'text-gray-500 hover:text-gray-300 hover:bg-gray-900/40 border-b-2 border-transparent']">
          连通性 · probes
        </button>
        <button type="button" @click="activeTab = 'bird'"
          :class="['px-4 py-2.5 text-[10px] tracking-widest uppercase whitespace-nowrap transition-colors',
            activeTab === 'bird' ? 'text-emerald-400 bg-emerald-950/30 border-b-2 border-emerald-500'
                                 : 'text-gray-500 hover:text-gray-300 hover:bg-gray-900/40 border-b-2 border-transparent']">
          BIRD 路由 · bird
        </button>
      </div>
    </div>

    <template v-if="activeTab === 'probes'">
    <!-- Config drift — live config vs adopted baseline (configdrift.go) -->
    <div class="border border-gray-800 bg-gray-900 mb-3">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase">
        配置漂移 · 实际 bird/filters/nft vs 声明基线
      </div>
      <div class="p-3 space-y-2">
        <div v-for="d in drift" :key="d.node_id" class="border-b border-gray-800/40 pb-2 last:border-0">
          <div class="flex items-center gap-2 flex-wrap text-[11px]">
            <span class="font-mono text-gray-300 w-16">{{ d.node_id }}</span>
            <span :class="d.drift ? 'text-red-400' : d.has_baseline ? 'text-emerald-400' : 'text-gray-500'">{{ driftLabel(d) }}</span>
            <span class="text-gray-600">{{ driftMsg[d.node_id] }}</span>
            <span class="flex-1"></span>
            <button @click="adoptBaseline(d.node_id)" :disabled="driftBusy[d.node_id]"
              class="border border-gray-700 px-2 py-0.5 text-gray-400 hover:border-emerald-600 hover:text-emerald-400 disabled:opacity-40">采纳基线</button>
            <button v-if="d.has_baseline" @click="showDiff(d.node_id)" :disabled="driftBusy[d.node_id]"
              class="border border-gray-700 px-2 py-0.5 text-gray-400 hover:border-sky-600 hover:text-sky-400 disabled:opacity-40">{{ diffOpen === d.node_id ? '收起' : 'diff' }}</button>
          </div>
          <div v-if="d.drift && d.has_baseline" class="flex items-center gap-2 mt-1.5 text-[11px]">
            <input v-model="rbConfirm[d.node_id]" :placeholder="'ROLLBACK CONFIG ' + d.node_id"
              class="w-56 bg-black border border-gray-700 px-1.5 py-0.5 text-gray-200 font-mono" />
            <button @click="doRollback(d.node_id)" :disabled="driftBusy[d.node_id]"
              class="border border-red-800 px-2 py-0.5 text-red-400 hover:bg-red-900/30 disabled:opacity-40">回滚 BIRD</button>
            <span class="text-gray-600">nft 漂移需人工核对,不自动回滚</span>
          </div>
          <div v-if="diffOpen === d.node_id && diffData" class="mt-2 grid sm:grid-cols-2 gap-2">
            <div>
              <div class="text-[10px] text-gray-500 mb-0.5">声明 bird.conf</div>
              <pre class="text-[10px] text-gray-400 bg-black border border-gray-800 p-2 max-h-60 overflow-auto whitespace-pre">{{ diffData.declared.bird_conf }}</pre>
            </div>
            <div>
              <div class="text-[10px] text-gray-500 mb-0.5">实际 bird.conf</div>
              <pre class="text-[10px] text-gray-300 bg-black border border-gray-800 p-2 max-h-60 overflow-auto whitespace-pre">{{ diffData.live.bird_conf }}</pre>
            </div>
          </div>
        </div>
        <div v-if="!drift.length" class="text-[11px] text-gray-600 italic">暂无漂移数据 —— 先对各节点「采纳基线」后开始监测。</div>
      </div>
    </div>

    <!-- SLA targets editor — folded into every PoP's probe set (sla.go) -->
    <div class="border border-gray-800 bg-gray-900 mb-3">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex items-center justify-between gap-2">
        <span>SLA 目标 · 每个 PoP 拨测,状态页展示 SLO</span>
        <span class="normal-case text-gray-500">{{ slaMsg }}</span>
      </div>
      <div class="p-3 space-y-2">
        <div v-for="(t, i) in slaList" :key="i" class="flex items-center gap-2 flex-wrap text-[11px]">
          <input v-model="t.name" placeholder="名称(a-z0-9-)" class="w-28 bg-black border border-gray-700 px-1.5 py-0.5 text-gray-200 font-mono" />
          <input v-model="t.target" placeholder="IP / 主机" class="w-44 bg-black border border-gray-700 px-1.5 py-0.5 text-gray-200 font-mono" />
          <select v-model="t.type" class="bg-black border border-gray-700 px-1 py-0.5 text-gray-300">
            <option value="ping6">ping6</option>
            <option value="ping4">ping4</option>
          </select>
          <label class="text-gray-500">SLO
            <input v-model.number="t.slo_pct" type="number" step="0.01" class="w-16 bg-black border border-gray-700 px-1 py-0.5 text-gray-200 text-right" />%
          </label>
          <label class="text-gray-500">RTT≤
            <input v-model.number="t.rtt_budget_ms" type="number" step="1" class="w-16 bg-black border border-gray-700 px-1 py-0.5 text-gray-200 text-right" />ms
          </label>
          <button @click="removeSLA(i)" class="text-red-400 hover:text-red-300 px-1">✕</button>
        </div>
        <div class="flex items-center gap-2 pt-1">
          <button @click="addSLA" class="border border-gray-700 px-2 py-0.5 text-[11px] text-gray-400 hover:border-emerald-600 hover:text-emerald-400">+ 添加目标</button>
          <button @click="saveSLA" :disabled="slaBusy" class="border border-emerald-700 px-3 py-0.5 text-[11px] text-emerald-400 hover:bg-emerald-900/30 disabled:opacity-40">{{ slaBusy ? '保存中…' : '保存' }}</button>
          <span v-if="!slaList.length" class="text-[11px] text-gray-600 italic">暂无 SLA 目标 —— 添加后每个 PoP 会开始拨测。</span>
        </div>
      </div>
    </div>

    <NodeTabs v-model="selectedId" :nodes="tabNodes" />

    <div v-if="!node" class="border border-gray-800 bg-gray-900 p-6 text-center text-[10px] tracking-widest text-gray-600 italic">
      // awaiting first scrape
    </div>

    <template v-else-if="node.ok">
      <!-- Probes table -->
      <div class="border border-gray-800 bg-gray-900">
        <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex items-center justify-between">
          <span>ICMP probes · from {{ node.node.id }}</span>
          <span class="text-gray-700">{{ probes.length }} target{{ probes.length === 1 ? '' : 's' }}</span>
        </div>
        <div class="overflow-x-auto">
          <table class="w-full text-xs font-mono min-w-[640px]">
            <thead class="text-[10px] text-gray-600 uppercase tracking-widest">
              <tr class="border-b border-gray-800">
                <th class="text-left  px-3 py-2">name</th>
                <th class="text-left  px-3 py-2">target</th>
                <th class="text-left  px-3 py-2 hidden md:table-cell">type</th>
                <th class="text-right px-3 py-2">last rtt</th>
                <th class="text-right px-3 py-2 hidden md:table-cell">avg</th>
                <th class="text-right px-3 py-2">loss%</th>
                <th class="text-right px-3 py-2 hidden sm:table-cell">trend</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="p in probes" :key="p.name" class="border-b border-gray-800/50">
                <td class="px-3 py-2.5 whitespace-nowrap">
                  <span :class="p.last_ok ? 'text-emerald-500' : 'text-red-500'">●</span>
                  <span class="ml-2 text-gray-100">{{ p.name }}</span>
                </td>
                <td class="px-3 py-2.5 text-gray-500 break-all max-w-[16ch] sm:max-w-none">{{ p.target }}</td>
                <td class="px-3 py-2.5 text-gray-400 hidden md:table-cell">{{ p.type }}</td>
                <td class="px-3 py-2.5 text-right tabular-nums whitespace-nowrap">
                  <span v-if="p.last_ok" class="text-emerald-400">{{ rtt(p.last_ms) }}</span>
                  <span v-else class="text-red-500">DOWN</span>
                </td>
                <td class="px-3 py-2.5 text-right text-gray-300 tabular-nums hidden md:table-cell">
                  {{ avgRTT(p.series).toFixed(1) }}ms
                </td>
                <td class="px-3 py-2.5 text-right tabular-nums">
                  <span :class="lossPct(p.series) > 0 ? 'text-red-400' : 'text-gray-500'">
                    {{ lossPct(p.series).toFixed(0) }}%
                  </span>
                </td>
                <td class="px-3 py-2.5 text-right hidden sm:table-cell">
                  <div class="ml-auto w-[120px]">
                    <Chart :series="p.series" :height="28" :width="120" color="rgb(16 185 129)" />
                  </div>
                </td>
              </tr>
              <tr v-if="probes.length === 0">
                <td colspan="7" class="px-3 py-4 text-gray-600 italic text-center">no probes configured</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

    </template>

    <div v-else class="border border-red-500/60 bg-red-950/30 p-4">
      <div class="text-xs text-red-400 break-all">⨯ {{ node.error || 'node unreachable' }}</div>
    </div>

    <!-- WireGuard mesh — fleet-wide, all 3 nodes' interfaces grouped under
         their owning node. This section ignores the tab selection because
         the WG topology is a single shared property of the network. -->
    <div class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex items-center justify-between flex-wrap gap-2">
        <span>WireGuard mesh · fleet-wide</span>
        <span class="text-gray-700">{{ wgTotalIfaces }} iface{{ wgTotalIfaces === 1 ? '' : 's' }} · {{ wgTotalPeers }} peer{{ wgTotalPeers === 1 ? '' : 's' }}</span>
      </div>
      <div class="divide-y divide-gray-800">
        <div v-for="g in wgGroups" :key="g.nodeId">
          <div class="px-4 py-2 border-b border-gray-800 bg-gray-950/40 text-[10px] tracking-widest font-mono uppercase flex items-center gap-2">
            <span :class="g.ok ? 'text-emerald-500' : 'text-red-500'">●</span>
            <span class="text-gray-100">{{ g.nodeId }}</span>
            <span class="text-gray-700">·</span>
            <span class="text-gray-500">{{ g.country }}</span>
            <span v-if="g.ifaces.length === 0" class="ml-auto text-gray-700 normal-case">no WG interfaces</span>
            <span v-else class="ml-auto text-gray-700">{{ g.ifaces.length }} iface · {{ g.ifaces.reduce((s, i) => s + i.peers.length, 0) }} peer</span>
          </div>
          <div v-if="!g.ok" class="p-4 text-xs text-red-400 break-all">⨯ {{ g.error || 'node unreachable' }}</div>
          <div v-else>
            <div v-for="i in g.ifaces" :key="g.nodeId + '/' + i.name" class="p-4">
              <div class="flex items-baseline justify-between mb-2 flex-wrap gap-x-2 gap-y-0.5">
                <div>
                  <span class="text-base text-emerald-500 font-mono">{{ i.name }}</span>
                  <span class="ml-2 text-[10px] tracking-widest uppercase text-gray-600">port {{ i.listening_port || '—' }}</span>
                </div>
                <div class="text-[10px] tracking-widest uppercase text-gray-700 font-mono">{{ g.nodeId }} → {{ i.name }}</div>
              </div>
              <div class="text-[10px] tracking-widest text-gray-700 uppercase break-all">
                pub: <span class="text-gray-500 normal-case">{{ i.public_key }}</span>
              </div>
              <div v-for="(p, idx) in i.peers" :key="idx" class="mt-3 border-l-2 border-gray-800 pl-3">
                <div class="text-[10px] text-gray-600 tracking-widest uppercase mb-1">peer · {{ idx + 1 }}</div>
                <div class="grid grid-cols-1 sm:grid-cols-2 gap-x-4 gap-y-1 text-xs">
                  <div class="min-w-0"><span class="text-gray-600">endpoint</span> <span class="text-gray-300 ml-1 break-all">{{ p.endpoint || '—' }}</span></div>
                  <div class="min-w-0"><span class="text-gray-600">allowed</span>  <span class="text-gray-300 ml-1 break-all">{{ p.allowed_ips }}</span></div>
                  <div class="min-w-0"><span class="text-gray-600">handshake</span> <span class="text-gray-300 ml-1">{{ p.last_handshake || '—' }}</span></div>
                  <div class="min-w-0"><span class="text-gray-600">transfer</span>  <span class="text-gray-300 ml-1">{{ p.transfer || '—' }}</span></div>
                  <div class="min-w-0"><span class="text-gray-600">keepalive</span> <span class="text-gray-300 ml-1">{{ p.keepalive || '—' }}</span></div>
                  <div class="min-w-0 sm:col-span-2"><span class="text-gray-600">pub-key</span>   <span class="text-gray-500 ml-1 normal-case break-all text-[10px]">{{ p.public_key }}</span></div>
                </div>
              </div>
              <div v-if="i.peers.length === 0" class="mt-2 text-xs text-gray-600 italic">no peers connected</div>
            </div>
          </div>
        </div>
        <div v-if="wgGroups.length === 0" class="p-4 text-gray-600 italic text-xs">no fleet data yet</div>
      </div>
    </div>

    <!-- GRE / VXLAN mesh — fleet-wide, every kernel tunnel from every node. -->
    <div class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex items-center justify-between flex-wrap gap-2">
        <span>GRE / VXLAN tunnels · fleet-wide</span>
        <span class="text-gray-700">{{ tunTotal }} tunnel{{ tunTotal === 1 ? '' : 's' }}</span>
      </div>
      <div class="divide-y divide-gray-800">
        <div v-for="g in tunGroups" :key="g.nodeId">
          <div class="px-4 py-2 border-b border-gray-800 bg-gray-950/40 text-[10px] tracking-widest font-mono uppercase flex items-center gap-2">
            <span :class="g.ok ? 'text-emerald-500' : 'text-red-500'">●</span>
            <span class="text-gray-100">{{ g.nodeId }}</span>
            <span class="text-gray-700">·</span>
            <span class="text-gray-500">{{ g.country }}</span>
            <span v-if="g.tunnels.length === 0" class="ml-auto text-gray-700 normal-case">no GRE/VXLAN</span>
            <span v-else class="ml-auto text-gray-700">{{ g.tunnels.length }} tunnel{{ g.tunnels.length === 1 ? '' : 's' }}</span>
          </div>
          <div v-if="!g.ok" class="p-4 text-xs text-red-400 break-all">⨯ {{ g.error || 'node unreachable' }}</div>
          <div v-else-if="g.tunnels.length > 0" class="overflow-x-auto">
            <table class="w-full text-xs font-mono min-w-[640px]">
              <thead class="text-[10px] text-gray-600 uppercase tracking-widest">
                <tr class="border-b border-gray-800">
                  <th class="text-left  px-3 py-2">name</th>
                  <th class="text-left  px-3 py-2">kind</th>
                  <th class="text-left  px-3 py-2">local</th>
                  <th class="text-left  px-3 py-2">remote</th>
                  <th class="text-right px-3 py-2 hidden sm:table-cell">mtu</th>
                  <th class="text-right px-3 py-2 hidden sm:table-cell">ttl</th>
                  <th class="text-left  px-3 py-2 hidden md:table-cell">extra</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="t in g.tunnels" :key="g.nodeId + '/' + t.name" class="border-b border-gray-800/50">
                  <td class="px-3 py-2 whitespace-nowrap">
                    <span :class="t.up ? 'text-emerald-500' : 'text-red-500'">●</span>
                    <span class="ml-2 text-gray-100">{{ t.name }}</span>
                  </td>
                  <td class="px-3 py-2 whitespace-nowrap" :class="kindColor(t.kind)">{{ t.kind }}</td>
                  <td class="px-3 py-2 text-gray-300 break-all">{{ t.local || '—' }}</td>
                  <td class="px-3 py-2 text-gray-300 break-all">{{ t.remote || '—' }}</td>
                  <td class="px-3 py-2 text-right text-gray-500 hidden sm:table-cell tabular-nums">{{ t.mtu || '—' }}</td>
                  <td class="px-3 py-2 text-right text-gray-500 hidden sm:table-cell tabular-nums">{{ t.ttl || '—' }}</td>
                  <td class="px-3 py-2 text-gray-500 hidden md:table-cell">
                    <template v-if="t.kind === 'vxlan'">
                      <span class="text-pink-300">vni {{ t.vxlan_id }}</span>
                      <span class="text-gray-700 mx-1">·</span>
                      <span>port {{ t.dst_port }}</span>
                      <span v-if="t.underlay_dev" class="text-gray-700 mx-1">·</span>
                      <span v-if="t.underlay_dev">dev {{ t.underlay_dev }}</span>
                    </template>
                    <span v-else-if="t.state" class="text-gray-600">{{ t.state }}</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
        <div v-if="tunGroups.length === 0" class="p-4 text-gray-600 italic text-xs">no fleet data yet</div>
      </div>
    </div>
    </template>

    <template v-if="activeTab === 'bird'">
      <Bird />
    </template>
  </div>
</template>
