<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, type Envelope, type FleetNodeStatus, type AnycastState } from '@/api/client'
import { usePolling } from '@/composables/usePolling'
import { useSessionStore } from '@/stores/session'
import NodeTabs from '@/components/NodeTabs.vue'

const session = useSessionStore()
const isAdmin = computed(() => session.role === 'admin')

const { data, error, lastUpdatedAt, loading } = usePolling<Envelope<FleetNodeStatus[]>>(
  (s) => api.fleet(s), 6000
)

const nodes = computed(() => data.value?.data ?? [])
const selectedId = ref<string>('')
watch(nodes, (vs) => {
  if (vs.length && !vs.find(n => n.node.id === selectedId.value)) {
    selectedId.value = vs[0].node.id
  }
}, { immediate: true })

watch(selectedId, () => {
  // Reset any expanded panel when switching nodes — the detail belongs to
  // a specific (node, name) pair.
  expanded.value = null
  detail.value = ''
  detailError.value = ''
})

const node = computed(() => nodes.value.find(n => n.node.id === selectedId.value) || null)
const protocols = computed(() => node.value?.protocols ?? [])
const routeCounts = computed(() => node.value?.route_counts ?? [])
const birdReady = computed(() => !!(node.value?.bird_version))

const expanded = ref<string | null>(null)
const detail = ref<string>('')
const detailLoading = ref(false)
const detailError = ref('')

async function toggleProtocol(name: string) {
  if (expanded.value === name) {
    expanded.value = null
    detail.value = ''
    return
  }
  expanded.value = name
  detail.value = ''
  detailError.value = ''
  detailLoading.value = true
  try {
    const env = await api.birdProtocol(name, selectedId.value)
    if (env.ok && env.data) {
      detail.value = env.data.raw
    } else {
      detailError.value = env.error ?? 'unknown error'
    }
  } catch (e) {
    detailError.value = e instanceof Error ? e.message : String(e)
  } finally {
    detailLoading.value = false
  }
}

const fmtAgo = computed(() =>
  lastUpdatedAt.value ? lastUpdatedAt.value.toLocaleTimeString('en-GB', { hour12: false }) : ''
)

const proto2Color: Record<string, string> = {
  BGP:    'text-pink-400',
  Static: 'text-amber-300',
  Direct: 'text-blue-300',
  Kernel: 'text-violet-400',
  Device: 'text-gray-400'
}

const tabNodes = computed(() => nodes.value.map(n => ({
  id: n.node.id, ok: n.ok, country: n.node.country
})))

// ── anycast drain / undrain ──
const anycast = ref<AnycastState | null>(null)
const anycastBusy = ref(false)
const anycastMsg = ref('')
const anycastErr = ref('')
const drainConfirm = ref(false)

async function loadAnycast() {
  anycast.value = null; anycastMsg.value = ''; anycastErr.value = ''; drainConfirm.value = false
  if (!selectedId.value) return
  try {
    const env = await api.anycastState(selectedId.value)
    if (env.ok) anycast.value = normAnycast(env.data)
  } catch { /* non-fatal — control just stays hidden */ }
}

// Harden against nil slices serialized as JSON null (a node with no upstream_*
// sessions returns upstreams_up/down = null) — else `.length`/`.join` in the
// template throws and ErrorBoundary blanks the whole Connectivity route.
function normAnycast(a: AnycastState | undefined | null): AnycastState | null {
  if (!a) return null
  a.upstreams_up = a.upstreams_up ?? []
  a.upstreams_down = a.upstreams_down ?? []
  return a
}
watch(selectedId, loadAnycast, { immediate: true })

async function doDrain(enable: boolean) {
  if (!selectedId.value) return
  anycastBusy.value = true; anycastErr.value = ''; anycastMsg.value = ''
  try {
    const env = enable ? await api.anycastUndrain(selectedId.value) : await api.anycastDrain(selectedId.value)
    if (!env.ok) throw new Error(env.error || 'failed')
    anycast.value = normAnycast(env.data?.state) ?? anycast.value
    anycastMsg.value = (env.data?.output || '').trim() || (enable ? '已恢复宣告' : '已撤回宣告')
    drainConfirm.value = false
  } catch (e) { anycastErr.value = e instanceof Error ? e.message : String(e) }
  finally { anycastBusy.value = false }
}
</script>

<template>
  <div class="space-y-4">
    <div class="border border-gray-800 bg-gray-900 p-4">
      <div class="flex items-center justify-between flex-wrap gap-2">
        <div class="flex items-center gap-3">
          <span :class="['w-1.5 h-1.5', birdReady ? 'bg-emerald-500 animate-pulse' : 'bg-red-500']"></span>
          <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">BIRD 路由</h1>
          <span class="text-[10px] tracking-widest text-gray-700">v{{ node?.bird_version || '?' }}</span>
        </div>
        <div class="text-[10px] tracking-widest text-gray-600 uppercase">
          <span :class="loading ? 'text-emerald-500' : 'text-gray-700'">● POLL 6s</span>
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
      <!-- Route counts -->
      <div v-if="routeCounts.length > 0" class="grid grid-cols-2 md:grid-cols-4 gap-3">
        <div v-for="rc in routeCounts" :key="rc.table" class="border border-gray-800 bg-gray-900 p-4">
          <div class="text-[10px] tracking-widest text-gray-600 uppercase">table · {{ rc.table }}</div>
          <div class="text-2xl text-emerald-500 font-mono tabular-nums mt-1">{{ rc.count.toLocaleString() }}</div>
          <div class="text-[10px] tracking-widest text-gray-500 uppercase mt-1">routes</div>
        </div>
      </div>

      <!-- Anycast drain / undrain (admin only; never the local control node) -->
      <div v-if="isAdmin && anycast && (anycast.upstreams_up.length || anycast.upstreams_down.length)"
           class="border p-4" :class="anycast.drained ? 'border-amber-800 bg-amber-950/20' : 'border-gray-800 bg-gray-900'">
        <div class="flex items-center justify-between flex-wrap gap-2">
          <div class="flex items-center gap-3 min-w-0">
            <span :class="['w-1.5 h-1.5', anycast.drained ? 'bg-amber-500' : 'bg-emerald-500']"></span>
            <span class="text-[11px] tracking-widest uppercase text-gray-300">Anycast 宣告</span>
            <span class="text-[10px] font-mono" :class="anycast.drained ? 'text-amber-400' : 'text-emerald-400'">
              {{ anycast.drained ? 'DRAINED · 已撤回' : anycast.upstreams_up.length + ' 个上游宣告中' }}
            </span>
          </div>
          <div v-if="!anycast.local" class="flex items-center gap-2">
            <template v-if="anycast.drained">
              <button @click="doDrain(true)" :disabled="anycastBusy"
                class="px-3 py-1 border border-emerald-700 text-[10px] uppercase text-emerald-400 hover:bg-emerald-950 disabled:opacity-40">恢复宣告 (undrain)</button>
            </template>
            <template v-else-if="!drainConfirm">
              <button @click="drainConfirm = true" :disabled="anycastBusy"
                class="px-3 py-1 border border-amber-700 text-[10px] uppercase text-amber-400 hover:bg-amber-950 disabled:opacity-40">撤回宣告 (drain)</button>
            </template>
            <template v-else>
              <span class="text-[10px] text-amber-300">确认撤回 {{ anycast.node_id }} 的 anycast?</span>
              <button @click="doDrain(false)" :disabled="anycastBusy" class="px-3 py-1 border border-red-700 text-[10px] uppercase text-red-400 hover:bg-red-950 disabled:opacity-40">确认</button>
              <button @click="drainConfirm = false" class="px-3 py-1 border border-gray-700 text-[10px] uppercase text-gray-400">取消</button>
            </template>
          </div>
          <span v-else class="text-[10px] text-gray-600 italic">控制节点 · 不可撤回</span>
        </div>
        <div class="mt-2 text-[10px] font-mono text-gray-500 break-all">
          <span v-if="anycast.upstreams_up.length">up: {{ anycast.upstreams_up.join(', ') }}</span>
          <span v-if="anycast.upstreams_down.length" class="text-amber-500/70 ml-2">down: {{ anycast.upstreams_down.join(', ') }}</span>
        </div>
        <pre v-if="anycastMsg" class="mt-2 text-[10px] text-gray-400 whitespace-pre-wrap">{{ anycastMsg }}</pre>
        <div v-if="anycastErr" class="mt-2 text-[10px] text-red-400">ERR · {{ anycastErr }}</div>
        <div class="mt-1 text-[9px] text-gray-600 italic">drain = birdc disable 各上游会话(撤回 anycast,流量回切健康 PoP;保留 mesh 与本机可达),可逆。</div>
      </div>

      <!-- Protocols table -->
      <div class="border border-gray-800 bg-gray-900">
        <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex items-center justify-between gap-2 min-w-0">
          <!-- min-w-0 + truncate on the title so the backticked command
               name doesn't push the right-side counter off the card on
               phones. The counter gets shrink-0 because losing the item
               count is worse than truncating the heading. -->
          <span class="min-w-0 truncate">Protocols · {{ node.node.id }} · `birdc show protocols`</span>
          <span class="text-gray-700 shrink-0">{{ protocols.length }} item{{ protocols.length === 1 ? '' : 's' }}</span>
        </div>
        <div class="overflow-x-auto">
          <table class="w-full text-xs font-mono min-w-[560px]">
            <thead class="text-[10px] text-gray-600 uppercase tracking-widest">
              <tr class="border-b border-gray-800">
                <th class="text-left px-3 py-2">name</th>
                <th class="text-left px-3 py-2">proto</th>
                <th class="text-left px-3 py-2">table</th>
                <th class="text-left px-3 py-2">state</th>
                <th class="text-left px-3 py-2 hidden sm:table-cell">since</th>
                <th class="text-left px-3 py-2 hidden sm:table-cell">info</th>
              </tr>
            </thead>
            <tbody>
              <template v-for="p in protocols" :key="p.name">
                <tr
                  class="border-b border-gray-800/50 cursor-pointer hover:bg-gray-800/40 transition-colors"
                  @click="toggleProtocol(p.name)"
                >
                  <td class="px-3 py-2 text-gray-100 whitespace-nowrap">
                    <span class="text-gray-700">{{ expanded === p.name ? '▼' : '›' }}</span>
                    <span class="ml-2">{{ p.name }}</span>
                  </td>
                  <td class="px-3 py-2 whitespace-nowrap" :class="proto2Color[p.proto] || 'text-gray-400'">{{ p.proto }}</td>
                  <td class="px-3 py-2 text-gray-500 whitespace-nowrap">{{ p.table }}</td>
                  <td class="px-3 py-2 whitespace-nowrap">
                    <span :class="p.healthy ? 'text-emerald-500' : 'text-red-500'">{{ p.state }}</span>
                  </td>
                  <td class="px-3 py-2 text-gray-500 hidden sm:table-cell whitespace-nowrap">{{ p.since }}</td>
                  <td class="px-3 py-2 hidden sm:table-cell">
                    <span :class="p.healthy ? 'text-gray-300' : 'text-red-400'">{{ p.info || '—' }}</span>
                  </td>
                </tr>
                <tr v-if="expanded === p.name" class="bg-black">
                  <td colspan="6" class="px-0">
                    <div class="px-4 py-2 text-[10px] tracking-widest text-gray-600 uppercase break-all">
                      $ ssh {{ node.node.id }} sudo birdc show protocols all {{ p.name }}
                    </div>
                    <pre v-if="detailLoading" class="font-mono text-amber-400 text-xs p-4">loading...</pre>
                    <pre v-else-if="detailError" class="font-mono text-red-400 text-xs p-4 whitespace-pre-wrap break-all">{{ detailError }}</pre>
                    <pre v-else class="font-mono text-emerald-400 text-xs p-4 overflow-x-auto whitespace-pre">{{ detail || '(no output)' }}</pre>
                  </td>
                </tr>
              </template>
              <tr v-if="protocols.length === 0">
                <td colspan="6" class="px-3 py-4 text-gray-600 italic text-center">
                  {{ birdReady ? 'no protocols' : 'BIRD unreachable' }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <div v-else class="border border-red-500/60 bg-red-950/30 p-4">
      <div class="text-xs text-red-400 break-all">⨯ {{ node.error || 'node unreachable' }}</div>
    </div>
  </div>
</template>
