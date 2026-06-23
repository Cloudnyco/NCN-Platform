<script setup lang="ts">
// Peering.vue — admin review queue. One card per application; approve
// or reject inline with optional admin notes that get included in the
// decision email back to the applicant.
import { ref, computed, onMounted, watch, reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, type PeeringApplication, type PeerGeneration } from '@/api/client'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const session = useSessionStore()
const isAdmin = computed(() => session.role === 'admin')

// IMPORTANT: these constants live in the script so the template can
// reference them as plain identifiers. Earlier inline `v-for="f in
// (['pending','decided','all'] as const)"` blew up at render-time
// because `as const` is TypeScript syntax, not JS — Vue's template
// compiler emits the expression verbatim into the JS render function,
// which then throws a SyntaxError on `as`. Bumping the array out into
// a const in <script> avoids that entirely.
type FilterKey = 'pending' | 'decided' | 'all'
const FILTERS: readonly FilterKey[] = ['pending', 'decided', 'all']
const DECISIONS: readonly ('approved' | 'rejected')[] = ['approved', 'rejected']

const apps    = ref<PeeringApplication[]>([])
const loading = ref(false)
const err     = ref<string | null>(null)
const filter  = ref<FilterKey>('pending')
const notes   = ref<Record<string, string>>({})
const busy    = ref<Record<string, boolean>>({})

const visible = computed(() => {
  const all = apps.value
  if (filter.value === 'pending') return all.filter(a => a.status === 'pending')
  if (filter.value === 'decided') return all.filter(a => a.status !== 'pending')
  return all
})
const pendingCount = computed(() => apps.value.filter(a => a.status === 'pending').length)

async function refresh() {
  if (!isAdmin.value) return
  loading.value = true
  err.value = null
  try {
    const env = await api.peeringList()
    if (!env.ok) throw new Error(env.error || 'list failed')
    apps.value = env.data ?? []
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function decide(a: PeeringApplication, status: 'approved' | 'rejected') {
  const noteVal = (notes.value[a.id] || '').trim()
  if (busy.value[a.id]) return
  if (status === 'rejected' && !noteVal) {
    if (!confirm(t('admin_peering.confirm_reject_no_note'))) return
  }
  busy.value = { ...busy.value, [a.id]: true }
  try {
    const env = await api.peeringDecide(a.id, status, noteVal)
    if (!env.ok) throw new Error(env.error || 'decide failed')
    await refresh()
    notes.value = { ...notes.value, [a.id]: '' }
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = { ...busy.value, [a.id]: false }
  }
}

function fmtTime(s: string | undefined | null): string {
  if (!s) return ''
  try { return new Date(s).toLocaleString('en-GB', { hour12: false }) } catch { return s }
}
// --- Peering/IRR automation (per approved application) ---
const gen = reactive<Record<number, PeerGeneration>>({})       // asn → generated config
const neighborV6 = reactive<Record<string, string>>({})        // app.id → session v6
const applyTargets = reactive<Record<number, string>>({})      // asn → comma-sep node ids
const applyConfirm = reactive<Record<number, string>>({})      // asn → confirm word input
const applyLog = reactive<Record<number, string>>({})          // asn → last apply log
const peerBusy = reactive<Record<string, boolean>>({})
const peerErr = reactive<Record<string, string>>({})

async function genPeer(a: PeeringApplication) {
  peerErr[a.id] = ''; peerBusy[a.id] = true
  try {
    const env = await api.peerConfig({ app_id: a.id, neighbor_v6: (neighborV6[a.id] || '').trim() })
    if (!env.ok || !env.data) throw new Error(env.error || 'generate failed')
    gen[a.asn] = env.data
  } catch (e: unknown) { peerErr[a.id] = e instanceof Error ? e.message : String(e) }
  finally { peerBusy[a.id] = false }
}
async function applyPeer(a: PeeringApplication) {
  const nodes = (applyTargets[a.asn] || '').split(',').map(s => s.trim()).filter(Boolean)
  if (!nodes.length) { peerErr[a.id] = '填写目标节点（逗号分隔）'; return }
  peerErr[a.id] = ''; peerBusy[a.id] = true; applyLog[a.asn] = ''
  try {
    const env = await api.peerApply({ asn: a.asn, confirm: (applyConfirm[a.asn] || '').trim(), target_nodes: nodes })
    applyLog[a.asn] = env.data?.log || ''
    if (!env.ok) throw new Error('应用有失败：' + JSON.stringify(env.data?.failed || env.error))
    if (env.data) gen[a.asn] = { ...gen[a.asn], status: 'applied', applied_nodes: env.data.applied }
  } catch (e: unknown) { peerErr[a.id] = e instanceof Error ? e.message : String(e) }
  finally { peerBusy[a.id] = false }
}

function statusBadge(s: string): string {
  if (s === 'approved') return 'text-emerald-400 border-emerald-700/60'
  if (s === 'rejected') return 'text-red-400 border-red-700/60'
  return 'text-amber-400 border-amber-700/60'
}
function joinList(xs: string[] | undefined | null): string {
  if (!xs || xs.length === 0) return '—'
  return xs.join(', ')
}
function decisionClass(d: 'approved' | 'rejected'): string {
  return d === 'approved'
    ? 'border-emerald-500 text-emerald-400 hover:bg-emerald-500 hover:text-black'
    : 'border-red-500/60 text-red-400 hover:bg-red-500 hover:text-black'
}
function decisionLabel(d: 'approved' | 'rejected'): string {
  return d === 'approved'
    ? '✓ ' + t('admin_peering.approve')
    : '⨯ ' + t('admin_peering.reject')
}

onMounted(refresh)
watch(isAdmin, (now, prev) => { if (now && !prev) refresh() })
</script>

<template>
  <div class="space-y-4">
    <!-- Page header -->
    <div class="border border-gray-800 bg-gray-900 p-4 flex items-center justify-between flex-wrap gap-2">
      <div class="flex items-center gap-3">
        <span class="w-1.5 h-1.5 bg-emerald-500 animate-pulse"></span>
        <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">{{ t('admin_peering.title') }}</h1>
        <span class="text-[10px] text-gray-600">· {{ apps.length }} {{ t('admin_peering.total') }} · {{ pendingCount }} {{ t('admin_peering.pending') }}</span>
      </div>
      <button @click="refresh" :disabled="loading"
        class="text-[10px] tracking-widest uppercase text-gray-500 hover:text-emerald-400 disabled:opacity-30">
        ↻ {{ t('admin_peering.refresh') }}
      </button>
    </div>

    <!-- Admin-only gate -->
    <div v-if="!isAdmin" class="border border-amber-500/60 bg-amber-900/20 text-xs text-amber-300 p-4">
      {{ t('admin_peering.admin_only') }}
    </div>

    <template v-else>
      <!-- Filter tabs -->
      <div class="flex items-center gap-2">
        <button
          v-for="f in FILTERS"
          :key="f"
          @click="filter = f"
          :class="[
            'px-3 py-1.5 text-[10px] tracking-widest uppercase',
            filter === f
              ? 'border border-emerald-500 text-emerald-400 bg-emerald-950/30'
              : 'border border-gray-800 text-gray-500 hover:border-emerald-700/60 hover:text-gray-300'
          ]"
        >{{ t('admin_peering.filter.' + f) }}</button>
      </div>

      <!-- Loading / empty / list -->
      <div v-if="loading && apps.length === 0" class="text-xs text-gray-500 italic p-6 text-center">
        {{ t('admin_peering.loading') }}
      </div>
      <div v-else-if="visible.length === 0"
        class="border border-gray-800 bg-gray-900 text-xs text-gray-600 italic p-6 text-center">
        {{ t('admin_peering.empty') }}
      </div>

      <div v-else class="space-y-3">
        <article v-for="a in visible" :key="a.id" class="border border-gray-800 bg-gray-900">
          <!-- header strip -->
          <header class="px-4 py-3 flex items-center justify-between flex-wrap gap-3 border-b border-gray-800 bg-gray-900/60">
            <div class="flex items-center gap-3 min-w-0">
              <span :class="['border px-2 py-0.5 text-[10px] tracking-widest uppercase', statusBadge(a.status)]">
                {{ a.status }}
              </span>
              <span class="font-mono text-sm text-gray-100">AS{{ a.asn }}</span>
              <span class="text-sm text-gray-300 truncate">{{ a.network_name }}</span>
            </div>
            <div class="flex items-center gap-3 text-[10px] tracking-widest uppercase text-gray-600 shrink-0">
              <span>{{ fmtTime(a.submitted_at) }}</span>
              <span>id {{ a.id }}</span>
            </div>
          </header>

          <!-- body -->
          <div class="p-4 grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-2 text-xs">
            <div><span class="text-gray-600">noc-email </span><span class="text-gray-100 font-mono">{{ a.noc_email }}</span></div>
            <div><span class="text-gray-600">contact </span>  <span class="text-gray-300">{{ a.contact_name || '—' }}<span v-if="a.phone"> · {{ a.phone }}</span></span></div>
            <div><span class="text-gray-600">as-set </span>   <span class="text-gray-300 font-mono">{{ a.as_set || '—' }}</span></div>
            <div><span class="text-gray-600">irr-source </span><span class="text-gray-300">{{ a.irr_source || '—' }}</span></div>
            <div><span class="text-gray-600">max-pfx-v6 </span><span class="text-gray-300 tabular-nums">{{ a.max_prefix6 || '—' }}</span></div>
            <div><span class="text-gray-600">rpki </span>
              <span :class="a.has_rpki ? 'text-emerald-400' : 'text-amber-400'">{{ a.has_rpki ? 'yes' : 'no' }}</span>
            </div>
            <div><span class="text-gray-600">bfd-desired </span><span class="text-gray-300">{{ a.bfd_desired ? 'yes' : 'no' }}</span></div>
            <div><span class="text-gray-600">locations </span><span class="text-gray-300">{{ joinList(a.locations) }}</span></div>
            <div><span class="text-gray-600">sessions </span> <span class="text-gray-300">{{ joinList(a.session_types) }}</span></div>
            <div><span class="text-gray-600">ix-member </span><span class="text-gray-300">{{ joinList(a.ix_member) }}</span></div>

            <div class="sm:col-span-2">
              <div class="text-gray-600 mb-1">prefixes-v6</div>
              <pre class="font-mono text-emerald-300 break-all whitespace-pre-line m-0">{{ (a.prefixes6 || []).join('\n') || '—' }}</pre>
            </div>
            <div v-if="a.prefixes4 && a.prefixes4.length" class="sm:col-span-2">
              <div class="text-gray-600 mb-1">prefixes-v4</div>
              <pre class="font-mono text-gray-300 break-all whitespace-pre-line m-0">{{ a.prefixes4.join('\n') }}</pre>
            </div>
            <div v-if="a.notes" class="sm:col-span-2">
              <div class="text-gray-600 mb-1">applicant notes</div>
              <div class="text-gray-300 whitespace-pre-line border-l-2 border-gray-700 pl-3">{{ a.notes }}</div>
            </div>

            <div v-if="a.status !== 'pending'" class="sm:col-span-2 border-t border-gray-800 pt-2 mt-1">
              <div class="text-gray-600">decided by {{ a.decided_by }} · {{ fmtTime(a.decided_at) }}</div>
              <div v-if="a.admin_notes" class="mt-1 text-gray-300 whitespace-pre-line border-l-2 border-emerald-700/40 pl-3">{{ a.admin_notes }}</div>
            </div>
          </div>

          <!-- decision footer (pending only) -->
          <footer v-if="a.status === 'pending'" class="border-t border-gray-800 p-4 space-y-2">
            <label class="block">
              <span class="text-[10px] tracking-widest text-gray-600 uppercase">{{ t('admin_peering.admin_notes') }}</span>
              <textarea
                v-model="notes[a.id]"
                rows="2"
                :placeholder="t('admin_peering.notes_placeholder')"
                class="mt-1 w-full bg-black border border-gray-800 focus:border-emerald-500 px-3 py-2 text-xs font-mono focus:outline-none"
              ></textarea>
            </label>
            <div class="flex items-center gap-2 flex-wrap">
              <button
                v-for="d in DECISIONS"
                :key="d"
                @click="decide(a, d)"
                :disabled="busy[a.id]"
                :class="['px-4 py-2 border text-[10px] tracking-widest uppercase disabled:opacity-40 transition-colors', decisionClass(d)]"
              >{{ busy[a.id] ? '…' : decisionLabel(d) }}</button>
              <span class="text-[10px] text-gray-700 normal-case tracking-normal">
                {{ t('admin_peering.decision_hint') }}
              </span>
            </div>
          </footer>

          <!-- peer config automation (approved only): generate → review → confirm-gated apply -->
          <footer v-if="a.status === 'approved'" class="border-t border-gray-800 p-4 space-y-2 text-xs normal-case tracking-normal">
            <div class="flex items-center gap-2 flex-wrap">
              <span class="text-[10px] tracking-widest text-gray-600 uppercase">Peer 配置自动化 · IRR/bgpq4</span>
              <span v-if="gen[a.asn]" :class="['border px-2 py-0.5 text-[10px] uppercase', gen[a.asn].status==='drifted' ? 'text-amber-400 border-amber-700/60' : gen[a.asn].status==='applied' ? 'text-emerald-400 border-emerald-700/60' : 'text-gray-400 border-gray-700']">{{ gen[a.asn].status }}</span>
            </div>
            <div class="flex items-end gap-2 flex-wrap">
              <label class="block">
                <span class="text-gray-600">neighbor v6（会话地址）</span>
                <input v-model="neighborV6[a.id]" placeholder="2a0c:…::1" class="mt-1 w-56 bg-black border border-gray-800 px-2 py-1 font-mono focus:border-emerald-700 focus:outline-none" />
              </label>
              <button @click="genPeer(a)" :disabled="peerBusy[a.id]" class="px-3 py-1 border border-gray-700 text-[10px] uppercase tracking-widest text-gray-300 hover:border-emerald-600 hover:text-emerald-400 disabled:opacity-40">{{ peerBusy[a.id] ? '…' : '生成 / 重新生成' }}</button>
            </div>
            <template v-if="gen[a.asn]">
              <div class="text-gray-600">{{ gen[a.asn].prefix_count }} 前缀 · max {{ gen[a.asn].max_prefix }} · AS-SET {{ gen[a.asn].as_set || '—' }}</div>
              <ul v-if="gen[a.asn].warnings && gen[a.asn].warnings!.length" class="text-amber-400 list-disc pl-4">
                <li v-for="(wn,i) in gen[a.asn].warnings" :key="i">{{ wn }}</li>
              </ul>
              <pre class="max-h-64 overflow-auto bg-black border border-gray-800 p-2 font-mono text-[11px] text-gray-300 whitespace-pre m-0">{{ gen[a.asn].config }}</pre>
              <div class="border-t border-gray-800 pt-2 space-y-1">
                <div class="text-gray-600">应用到节点（会改路由器,需确认词;失败自动回滚）</div>
                <input v-model="applyTargets[a.asn]" placeholder="目标节点,逗号分隔 如 pop-01,pop-03" class="w-full bg-black border border-gray-800 px-2 py-1 font-mono focus:border-emerald-700 focus:outline-none" />
                <div class="flex items-center gap-2 flex-wrap">
                  <input v-model="applyConfirm[a.asn]" :placeholder="'APPLY PEER AS'+a.asn" class="w-56 bg-black border border-gray-800 px-2 py-1 font-mono focus:border-red-700 focus:outline-none" />
                  <button @click="applyPeer(a)" :disabled="peerBusy[a.id]" class="px-3 py-1 border border-red-800 text-[10px] uppercase tracking-widest text-red-300 hover:bg-red-950 disabled:opacity-40">应用</button>
                  <span class="text-gray-700">确认词 <code>APPLY PEER AS{{ a.asn }}</code></span>
                </div>
                <pre v-if="applyLog[a.asn]" class="max-h-48 overflow-auto bg-black border border-gray-800 p-2 font-mono text-[11px] text-gray-400 whitespace-pre m-0">{{ applyLog[a.asn] }}</pre>
              </div>
            </template>
            <div v-if="peerErr[a.id]" class="text-red-400">⨯ {{ peerErr[a.id] }}</div>
          </footer>
        </article>
      </div>

      <div v-if="err" class="border border-red-500 bg-red-950/30 text-xs text-red-400 normal-case tracking-normal p-3">
        ⨯ {{ err }}
      </div>
    </template>
  </div>
</template>
