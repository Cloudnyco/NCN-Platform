<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api, type BGPSession, type CmdResult, type LGNodeSessions, type LGTool } from '@/api/client'

// BGP labels stay English across locales (BGP terms don't translate).
type Tab = 'sessions' | 'route' | 'diag'
const activeTab = ref<Tab>('sessions')
const tabs: { key: Tab; label: string }[] = [
  { key: 'sessions', label: 'BGP Sessions' },
  { key: 'route',    label: 'Route Query' },
  { key: 'diag',     label: 'Ping & Trace' }
]

// ---------------------------------------------------------------------------
// BGP Sessions — GET /api/v1/lg/sessions returns every PoP at once; the node
// selector just switches which one is shown (no refetch).
// ---------------------------------------------------------------------------
const lgNodes = ref<LGNodeSessions[]>([])
const selectedNodeId = ref('ctrl-01')
const sessErr = ref('')
const sessLoaded = ref(false)
const search = ref('')
const statusKeys = ['established', 'connect', 'passive', 'down'] as const
const filters = ['all', ...statusKeys] as const
const filter = ref<typeof filters[number]>('all')
let sessTimer: ReturnType<typeof setInterval> | null = null

const statusMeta: Record<string, { label: string; text: string; border: string; dot: string; bar: string }> = {
  established: { label: 'Established', text: 'text-emerald-400', border: 'border-emerald-500/40', dot: 'bg-emerald-500', bar: 'border-l-emerald-500/60' },
  connect:     { label: 'Connect',     text: 'text-amber-400',   border: 'border-amber-500/40',   dot: 'bg-amber-500',   bar: 'border-l-amber-500/60' },
  passive:     { label: 'Passive',     text: 'text-blue-400',    border: 'border-blue-500/40',    dot: 'bg-blue-500',    bar: 'border-l-blue-500/60' },
  down:        { label: 'Down',        text: 'text-red-400',     border: 'border-red-500/40',     dot: 'bg-red-500',     bar: 'border-l-red-500/60' }
}

const selectedNode = computed<LGNodeSessions | null>(() =>
  lgNodes.value.find((n) => n.id === selectedNodeId.value) ?? lgNodes.value[0] ?? null
)
const sessions = computed<BGPSession[]>(() => selectedNode.value?.sessions ?? [])
const counts = computed<Record<string, number>>(() =>
  selectedNode.value?.counts ?? { established: 0, connect: 0, passive: 0, down: 0 }
)

async function loadSessions() {
  try {
    const env = await api.lgSessions()
    if (env.ok && env.data) {
      lgNodes.value = env.data.nodes ?? []
      if (!lgNodes.value.some((n) => n.id === selectedNodeId.value)) {
        selectedNodeId.value = env.data.default || lgNodes.value[0]?.id || 'ctrl-01'
      }
      sessErr.value = ''
    } else {
      sessErr.value = env.error ?? 'failed to load sessions'
    }
  } catch (e) {
    sessErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    sessLoaded.value = true
  }
}

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  return sessions.value.filter((s) => {
    if (filter.value !== 'all' && s.status !== filter.value) return false
    if (!q) return true
    return s.name.toLowerCase().includes(q)
      || String(s.neighbor_as).includes(q)
      || (s.neighbor_addr || '').toLowerCase().includes(q)
  })
})

function fmtRoutes(n: number): string {
  if (!n) return '0'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + 'M'
  if (n >= 1_000)     return (n / 1_000).toFixed(n >= 10_000 ? 0 : 1) + 'K'
  return String(n)
}

// ---------------------------------------------------------------------------
// Exec tools (Route Query + Ping & Trace) — always execute from ctrl-01.
// ---------------------------------------------------------------------------
interface Tool { key: LGTool; label: string; hint: string; example: string }
const diagTools: Tool[] = [
  { key: 'ping4',  label: 'PING4',  hint: 'ICMPv4 ×4',                example: '1.1.1.1' },
  { key: 'ping6',  label: 'PING6',  hint: 'ICMPv6 ×4',                example: '2606:4700:4700::1111' },
  { key: 'trace4', label: 'TRACE4', hint: 'traceroute -4 (≤20 hops)', example: '1.1.1.1' },
  { key: 'trace6', label: 'TRACE6', hint: 'traceroute -6 (≤20 hops)', example: '2606:4700:4700::1111' }
]
const diagTool = ref<LGTool>('ping4')
const target = ref('')
const loading = ref(false)
const result = ref<CmdResult | null>(null)
const errorMsg = ref<string | null>(null)
const startedAt = ref<Date | null>(null)

const activeDiag = computed(() => diagTools.find((t) => t.key === diagTool.value) ?? diagTools[0])
const placeholder = computed(() =>
  activeTab.value === 'route' ? 'e.g. 8.8.8.8  /  1.1.1.0/24' : `e.g. ${activeDiag.value.example}`
)
const exitColor = computed(() => (result.value?.exit_code === 0 ? 'text-emerald-500' : 'text-red-500'))

async function runTool(toolKey: LGTool) {
  if (!target.value.trim() || loading.value) return
  loading.value = true
  errorMsg.value = null
  startedAt.value = new Date()
  try {
    const env = await api.lgExec({ tool: toolKey, target: target.value.trim() })
    if (env.ok && env.data) {
      result.value = env.data
      errorMsg.value = null
    } else {
      result.value = env.data ?? null
      errorMsg.value = env.error ?? 'unknown error'
    }
  } catch (e) {
    errorMsg.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}
function submitExec() {
  runTool(activeTab.value === 'route' ? 'bgp_route' : diagTool.value)
}

watch(activeTab, () => { result.value = null; errorMsg.value = null })

onMounted(() => {
  loadSessions()
  sessTimer = setInterval(loadSessions, 20000)
})
onBeforeUnmount(() => { if (sessTimer) clearInterval(sessTimer) })
</script>

<template>
  <div class="space-y-4">
    <!-- Tabs -->
    <div class="flex flex-wrap gap-2">
      <button
        v-for="tb in tabs"
        :key="tb.key"
        type="button"
        @click="activeTab = tb.key"
        :class="[
          'px-4 py-2 text-[11px] tracking-widest uppercase border transition-colors duration-75',
          activeTab === tb.key
            ? 'border-emerald-500 bg-emerald-500 text-black font-semibold'
            : 'border-gray-700 text-gray-300 hover:border-emerald-500 hover:text-emerald-500'
        ]"
      >{{ tb.label }}</button>
    </div>

    <!-- ============ BGP SESSIONS ============ -->
    <div v-if="activeTab === 'sessions'" class="space-y-4 ncn-rise">
      <!-- PoP selector -->
      <div class="flex gap-1.5 overflow-x-auto pb-1 -mb-1">
        <button
          v-for="n in lgNodes"
          :key="n.id"
          type="button"
          @click="selectedNodeId = n.id"
          :class="[
            'shrink-0 inline-flex items-center gap-1.5 px-3 py-1.5 text-[11px] tracking-widest uppercase border font-mono transition-colors duration-75',
            selectedNodeId === n.id
              ? 'border-emerald-500 bg-emerald-500 text-black font-semibold'
              : 'border-gray-800 text-gray-400 hover:border-emerald-500 hover:text-emerald-500'
          ]"
        >
          {{ n.id }}
          <span v-if="!n.ready" class="opacity-50" title="no session detail from this PoP">∅</span>
        </button>
      </div>

      <!-- Selected node identity (re-keyed so it re-animates on PoP switch) -->
      <div v-if="selectedNode" :key="selectedNode.id" class="ncn-rise flex items-center gap-3 border border-gray-800 bg-gray-900 px-4 py-3">
        <div class="shrink-0 w-9 h-9 border border-gray-800 bg-gray-950 flex items-center justify-center font-mono text-[11px] tracking-widest text-gray-300">{{ selectedNode.country }}</div>
        <div class="min-w-0">
          <span class="font-mono text-sm text-gray-100">{{ selectedNode.id }}</span>
          <span class="text-sm text-gray-500 ml-2">{{ selectedNode.label }}</span>
        </div>
        <span class="ml-auto shrink-0 text-[10px] tracking-widest uppercase font-mono">
          <span v-if="selectedNode.ready" class="inline-flex items-center gap-1.5 text-emerald-500"><span class="w-1.5 h-1.5 bg-emerald-500 animate-pulse"></span>live</span>
          <span v-else class="text-gray-600">no detail</span>
        </span>
      </div>

      <!-- Status count pills -->
      <div :key="'pills-' + selectedNodeId" class="flex flex-wrap gap-2 ncn-rise">
        <span
          v-for="k in statusKeys"
          :key="k"
          class="inline-flex items-center gap-1.5 text-[10px] tracking-widest uppercase border px-2.5 py-1 font-mono"
          :class="[statusMeta[k].text, statusMeta[k].border]"
        >
          <span class="w-1.5 h-1.5" :class="statusMeta[k].dot"></span>
          {{ counts[k] ?? 0 }} {{ statusMeta[k].label }}
        </span>
      </div>

      <!-- Search + filter -->
      <div class="space-y-2">
        <input
          v-model="search"
          placeholder="search by name, neighbor AS, or IP…"
          spellcheck="false" autocomplete="off"
          class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-200 placeholder:text-gray-700 focus:border-emerald-500 focus:outline-none"
        />
        <div class="flex flex-wrap gap-1.5">
          <button
            v-for="f in filters"
            :key="f"
            type="button"
            @click="filter = f"
            :class="[
              'px-2.5 py-1 text-[10px] tracking-widest uppercase border transition-colors duration-75',
              filter === f
                ? 'border-emerald-500 text-emerald-400 bg-emerald-500/10'
                : 'border-gray-800 text-gray-500 hover:text-gray-300 hover:border-gray-700'
            ]"
          >{{ f }}</button>
        </div>
      </div>

      <!-- Session cards -->
      <div v-if="sessErr" class="border border-red-900 bg-gray-900 px-4 py-3 text-sm text-red-400 font-mono">{{ sessErr }}</div>
      <div v-else-if="!sessLoaded" class="border border-gray-800 bg-gray-900 px-4 py-6 text-center text-gray-600 text-xs tracking-widest uppercase animate-pulse">syncing sessions…</div>
      <div v-else-if="selectedNode && !selectedNode.ready" class="border border-gray-800 bg-gray-900 px-4 py-6 text-center text-gray-600 text-xs tracking-widest uppercase">no session detail available from this PoP yet</div>
      <div v-else-if="!filtered.length" class="border border-gray-800 bg-gray-900 px-4 py-6 text-center text-gray-600 text-xs tracking-widest uppercase">no sessions match</div>
      <div v-else class="space-y-2">
        <div
          v-for="(s, i) in filtered"
          :key="s.name"
          :style="{ animationDelay: Math.min(i, 14) * 35 + 'ms' }"
          class="ncn-rise border border-l-2 border-gray-800 bg-gray-900/40 p-3 sm:p-4 hover:border-gray-700 transition-colors"
          :class="statusMeta[s.status].bar"
        >
          <div class="flex items-center justify-between gap-3 flex-wrap">
            <div class="flex items-center gap-2 min-w-0">
              <span class="w-1.5 h-1.5 shrink-0" :class="statusMeta[s.status].dot"></span>
              <span class="font-mono text-sm text-gray-100 truncate">{{ s.name }}</span>
              <span
                class="text-[10px] tracking-widest uppercase border px-1.5 py-0.5 shrink-0"
                :class="[statusMeta[s.status].text, statusMeta[s.status].border]"
              >{{ statusMeta[s.status].label }}</span>
            </div>
            <span class="text-[10px] tracking-widest text-gray-600 font-mono uppercase">{{ s.proto }}</span>
          </div>
          <div class="mt-3 grid grid-cols-2 sm:grid-cols-4 gap-x-4 gap-y-2 text-xs font-mono">
            <div>
              <div class="text-[10px] tracking-widest text-gray-600 uppercase">Neighbor</div>
              <div class="text-gray-300 break-all mt-0.5">{{ s.neighbor_addr || '—' }}</div>
            </div>
            <div>
              <div class="text-[10px] tracking-widest text-gray-600 uppercase">Neighbor AS</div>
              <div class="text-gray-300 mt-0.5">{{ s.neighbor_as ? 'AS' + s.neighbor_as : '—' }}</div>
            </div>
            <div>
              <div class="text-[10px] tracking-widest text-gray-600 uppercase">Imported</div>
              <div class="text-emerald-400 tabular-nums mt-0.5">{{ fmtRoutes(s.routes_imported) }}</div>
            </div>
            <div>
              <div class="text-[10px] tracking-widest text-gray-600 uppercase">Exported</div>
              <div class="text-gray-300 tabular-nums mt-0.5">{{ fmtRoutes(s.routes_exported) }}</div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- ============ ROUTE QUERY / PING & TRACE ============ -->
    <div v-else class="space-y-4 ncn-rise">
      <div class="border border-gray-800 bg-gray-900 p-4 space-y-4">
        <div class="text-[10px] tracking-widest text-gray-600 uppercase font-mono">executed from TYO-01 · AS64500 · Region A, JP</div>
        <div v-if="activeTab === 'diag'" class="flex flex-wrap gap-2">
          <button
            v-for="t in diagTools"
            :key="t.key"
            type="button"
            @click="diagTool = t.key"
            :disabled="loading"
            :class="[
              'px-3 py-1.5 text-[11px] tracking-widest uppercase border transition-colors duration-75 disabled:opacity-50',
              diagTool === t.key
                ? 'border-emerald-500 bg-emerald-500 text-black font-semibold'
                : 'border-gray-700 text-gray-300 hover:border-emerald-500 hover:text-emerald-500'
            ]"
          >{{ t.label }}</button>
        </div>
        <div class="text-[10px] tracking-widest text-gray-600 uppercase">
          // {{ activeTab === 'route' ? 'birdc show route for' : activeDiag.hint }}
        </div>

        <form @submit.prevent="submitExec" class="flex flex-wrap gap-2 items-stretch">
          <span class="flex items-center text-emerald-500 text-sm font-mono pl-1">&gt;</span>
          <input
            v-model="target"
            :placeholder="placeholder"
            spellcheck="false" autocomplete="off" autocapitalize="off" autocorrect="off"
            :disabled="loading"
            class="flex-1 min-w-0 bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-200 placeholder:text-gray-700 focus:border-emerald-500 focus:outline-none disabled:opacity-50"
          />
          <button
            type="submit"
            :disabled="loading || !target.trim()"
            class="px-4 py-2 border border-emerald-500 text-emerald-500 text-xs tracking-widest uppercase hover:bg-emerald-500 hover:text-black disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
          >{{ loading ? 'EXEC...' : 'EXECUTE' }}</button>
        </form>
      </div>

      <div
        v-if="loading || result || errorMsg"
        class="border border-gray-800 bg-gray-900 px-4 py-2 text-[10px] tracking-widest uppercase flex justify-between flex-wrap gap-2"
      >
        <span>
          <span v-if="loading" class="text-emerald-500 animate-pulse">▌ EXECUTING</span>
          <span v-else-if="result" :class="exitColor">EXIT={{ result.exit_code }}</span>
          <span v-else-if="errorMsg" class="text-red-500">REJECTED</span>
        </span>
        <span class="text-gray-600">
          <span v-if="result">{{ result.duration }}</span>
          <span v-else-if="startedAt">{{ startedAt.toLocaleTimeString('en-GB', { hour12: false }) }}</span>
        </span>
      </div>

      <div v-if="errorMsg && !result" class="border border-red-900 bg-gray-900">
        <div class="px-4 py-2 border-b border-red-900 text-[10px] tracking-widest text-red-500 uppercase">$ ERROR</div>
        <pre class="font-mono text-red-400 text-sm bg-black p-4 whitespace-pre-wrap">{{ errorMsg }}</pre>
      </div>

      <div v-if="result" class="ncn-rise border border-gray-800 bg-gray-900">
        <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex justify-between flex-wrap gap-2">
          <span class="truncate">$ {{ result.cmd }}</span>
          <span :class="exitColor">exit={{ result.exit_code }} · {{ result.duration }}</span>
        </div>
        <pre class="font-mono text-emerald-400 text-sm bg-black p-4 overflow-x-auto whitespace-pre">{{ result.raw || '(no stdout)' }}</pre>
        <pre v-if="result.stderr" class="font-mono text-red-400 text-xs bg-black border-t border-gray-800 p-4 overflow-x-auto whitespace-pre">{{ result.stderr }}</pre>
      </div>
    </div>
  </div>
</template>
