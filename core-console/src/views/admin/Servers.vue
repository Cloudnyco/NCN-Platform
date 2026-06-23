<script setup lang="ts">
// Servers — PoP / server lifecycle management.
//
// The node list used to be hardcoded in backend/fleet.go: adding or removing
// a PoP meant editing Go and redeploying. This page drives the persistent node
// registry instead, so the whole lifecycle is a few clicks:
//
//   * add a server  → it starts being scraped + shows on the map/status
//   * 配置 agent     → runs the provisioning script (mints key/cert, ships the
//                      agent, starts it) on a box that already has SSH set up
//   * 下架 (soft)    → stops monitoring + hides from public; reversible
//   * 永久删除        → removes the record + purges its HMAC key
//
// BGP/bird is intentionally out of scope — still managed by hand.

import { computed, onMounted, onBeforeUnmount, nextTick, ref } from 'vue'
import { useSessionStore } from '@/stores/session'
import {
  api,
  type NodeView, type NodeCreateReq, type NodePatchReq, type OnboardState,
  type MeshConfigBundle,
} from '@/api/client'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const session = useSessionStore()
const isAdmin = computed(() => session.role === 'admin')

const nodes = ref<NodeView[]>([])
const loading = ref(false)
const err = ref<string | null>(null)

async function refresh() {
  if (!isAdmin.value) return
  loading.value = true
  err.value = null
  try {
    const env = await api.nodesList()
    if (!env.ok) throw new Error(env.error || 'list failed')
    nodes.value = env.data ?? []
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

// ────────── new / edit form ──────────
interface NodeForm {
  id: string
  label: string
  country: string
  address: string
  lat: number
  lon: number
  ssh_user: string
  ssh_port: number
  arch: string
  notes: string
  ssh_password: string // one-time, for first-contact key bootstrap; never stored
  ssh_private_key: string // one-time, ONLY when password auth is unavailable; never stored
  ssh_key_passphrase: string // optional, for an encrypted private key
}
function emptyForm(): NodeForm {
  return { id: '', label: '', country: '', address: '', lat: 0, lon: 0, ssh_user: 'root', ssh_port: 22, arch: 'amd64', notes: '', ssh_password: '', ssh_private_key: '', ssh_key_passphrase: '' }
}
const formOpen = ref(false)
const editingId = ref<string | null>(null)
const form = ref<NodeForm>(emptyForm())
const formBusy = ref(false)
const showKeyAuth = ref(false) // reveals the "only when necessary" SSH private-key login option

function openNew() {
  editingId.value = null
  form.value = emptyForm()
  formOpen.value = true
}
function openEdit(n: NodeView) {
  editingId.value = n.id
  form.value = {
    id: n.id,
    label: n.label,
    country: n.country,
    address: n.address,
    lat: n.lat ?? 0,
    lon: n.lon ?? 0,
    ssh_user: n.ssh_user || 'root',
    ssh_port: n.ssh_port || 22,
    arch: n.arch || 'amd64',
    notes: n.notes ?? '',
    ssh_password: '',
    ssh_private_key: '',
    ssh_key_passphrase: '',
  }
  formOpen.value = true
}

// Geo autodetect — fill country/label/lat/lon from the address.
const geoBusy = ref(false)
const geoMsg = ref('')
async function autoGeo() {
  const addr = form.value.address.trim()
  if (!addr) { geoMsg.value = '先填地址'; return }
  geoBusy.value = true
  geoMsg.value = ''
  try {
    const env = await api.nodeGeo(addr)
    if (!env.ok || !env.data) throw new Error(env.error || 'lookup failed')
    const g = env.data
    if (g.source === 'none') { geoMsg.value = '识别不到，请手动填'; return }
    form.value.country = g.country || form.value.country
    if (g.label) form.value.label = g.label
    if (g.lat || g.lon) { form.value.lat = g.lat; form.value.lon = g.lon }
    geoMsg.value = `✓ ${g.source}`
  } catch (e: unknown) {
    geoMsg.value = '✗ ' + (e instanceof Error ? e.message : String(e))
  } finally {
    geoBusy.value = false
  }
}

async function submitForm() {
  if (formBusy.value) return
  if (!form.value.label.trim() || !form.value.address.trim()) {
    err.value = 'label 和 address 必填'
    return
  }
  if (!editingId.value && !/^[a-z0-9][a-z0-9-]{1,30}$/.test(form.value.id.trim())) {
    err.value = 'id 必须匹配 ^[a-z0-9][a-z0-9-]{1,30}$（如 lax-01）'
    return
  }
  formBusy.value = true
  err.value = null
  try {
    if (editingId.value) {
      const patch: NodePatchReq = {
        label: form.value.label, country: form.value.country, address: form.value.address,
        lat: form.value.lat, lon: form.value.lon, ssh_user: form.value.ssh_user,
        ssh_port: form.value.ssh_port, arch: form.value.arch, notes: form.value.notes,
      }
      const env = await api.nodePatch(editingId.value, patch)
      if (!env.ok) throw new Error(env.error || 'patch failed')
    } else {
      const req: NodeCreateReq = {
        id: form.value.id.trim().toLowerCase(),
        label: form.value.label, country: form.value.country, address: form.value.address,
        lat: form.value.lat, lon: form.value.lon, ssh_user: form.value.ssh_user,
        ssh_port: form.value.ssh_port, arch: form.value.arch, notes: form.value.notes,
      }
      const env = await api.nodeCreate(req)
      if (!env.ok) throw new Error(env.error || 'create failed')
      // Adding a server == bringing it online. Always kick off the live
      // onboard job: with a password it bootstraps the fleet key first, then
      // provisions + verifies; without one it assumes key auth already works
      // and just provisions + verifies.
      const created = req.id
      const user = form.value.ssh_user
      const pw = form.value.ssh_password
      const pk = form.value.ssh_private_key
      const pkPass = form.value.ssh_key_passphrase
      formOpen.value = false
      editingId.value = null
      await refresh()
      await beginOnboard(created, user, pw, pk, pkPass)
      return
    }
    formOpen.value = false
    editingId.value = null
    await refresh()
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  } finally {
    formBusy.value = false
    // never keep first-contact secrets around
    form.value.ssh_password = ''
    form.value.ssh_private_key = ''
    form.value.ssh_key_passphrase = ''
  }
}

// ────────── onboarding (live, polled) — driven by the add-server flow ──────────
// Onboarding is launched from "新增服务器" (beginOnboard, below); there is no
// per-row onboard button. The modal just shows live step progress.
const onboardOpen = ref(false)
const onboardNodeId = ref('')
const onboardState = ref<OnboardState | null>(null)
const onboardErr = ref('')
const onboardStarting = ref(false)
let onboardTimer: ReturnType<typeof setInterval> | null = null
const onboardLogEl = ref<HTMLElement | null>(null)
// Live wall-clock tick (ms) so the elapsed timer + the running step's duration
// update smoothly between status polls. Runs only while the modal is open.
const nowMs = ref(Date.now())
let clockTimer: ReturnType<typeof setInterval> | null = null
function startClock() {
  if (clockTimer) return
  clockTimer = setInterval(() => { nowMs.value = Date.now() }, 200)
}
function stopClock() {
  if (clockTimer) { clearInterval(clockTimer); clockTimer = null }
}

// Overall progress 0..1 — completed steps + a half-credit for the one running.
const onboardProgress = computed(() => {
  const steps = onboardState.value?.steps ?? []
  if (!steps.length) return 0
  let done = 0, running = 0
  for (const s of steps) {
    if (s.status === 'ok' || s.status === 'skip') done++
    else if (s.status === 'running') running++
  }
  return Math.min(1, (done + running * 0.5) / steps.length)
})
// Total elapsed since the first step started (live while running).
const onboardElapsedMs = computed(() => {
  const steps = onboardState.value?.steps ?? []
  const starts = steps.map(s => s.started ?? 0).filter(Boolean)
  if (!starts.length) return 0
  const first = Math.min(...starts)
  const ends = steps.map(s => s.ended ?? 0)
  const allDone = onboardState.value?.done
  const last = allDone && ends.every(Boolean) ? Math.max(...ends) : nowMs.value
  return Math.max(0, last - first)
})
function fmtDur(ms: number): string {
  if (ms <= 0) return '0.0s'
  if (ms < 60000) return (ms / 1000).toFixed(1) + 's'
  const m = Math.floor(ms / 60000)
  return m + 'm ' + Math.round((ms % 60000) / 1000) + 's'
}
// Per-step duration label (live for the running step, frozen once terminal).
function stepDur(s: { started?: number; ended?: number; status: string }): string {
  if (!s.started) return ''
  const end = s.ended ?? (s.status === 'running' ? nowMs.value : s.started)
  return fmtDur(end - s.started)
}

function scrollLogToEnd() {
  nextTick(() => { const el = onboardLogEl.value; if (el) el.scrollTop = el.scrollHeight })
}
function logLineClass(line: string): string {
  const l = line.toLowerCase()
  if (l.includes('fail') || l.includes('error') || line.includes('✗')) return 'text-red-400'
  if (line.includes('✓')) return 'text-emerald-400'
  return ''
}

function stopOnboardPoll() {
  if (onboardTimer) { clearInterval(onboardTimer); onboardTimer = null }
}

function closeOnboard() {
  onboardOpen.value = false
  stopOnboardPoll()
  stopClock()
}

function startOnboardPoll() {
  stopOnboardPoll()
  // 1s cadence for snappy live feedback while the provision script streams.
  onboardTimer = setInterval(async () => {
    try {
      const env = await api.nodeOnboardStatus(onboardNodeId.value)
      if (env.ok && env.data) {
        onboardState.value = env.data
        scrollLogToEnd()
        if (env.data.done) { stopOnboardPoll(); stopClock(); await refresh() }
      }
    } catch { /* keep polling */ }
  }, 1000)
}

// beginOnboard starts the onboard job for a just-added node and opens the live
// progress modal. password may be empty → key bootstrap is skipped (assumes
// key auth already works) and it just provisions + verifies.
async function beginOnboard(id: string, user: string, password: string, privateKey = '', keyPassphrase = '') {
  onboardNodeId.value = id
  onboardErr.value = ''
  onboardState.value = null
  onboardOpen.value = true
  onboardStarting.value = true
  nowMs.value = Date.now()
  startClock()
  try {
    const env = await api.nodeOnboardStart(id, {
      ssh_user: user,
      ssh_password: password || undefined,
      ssh_private_key: privateKey || undefined,
      ssh_key_passphrase: keyPassphrase || undefined,
    })
    if (!env.ok) throw new Error(env.error || 'onboard failed to start')
    onboardState.value = env.data ?? null
    scrollLogToEnd()
    startOnboardPoll()
  } catch (e: unknown) {
    onboardErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    onboardStarting.value = false
  }
}

onBeforeUnmount(() => { stopOnboardPoll(); stopClock(); stopApplyPoll() })

// ────────── mesh / BIRD config generator (review-only) ──────────
const meshOpen = ref(false)
const meshNode = ref<NodeView | null>(null)
const meshRegion = ref<number>(0)
const meshTransports = ref<Record<string, string>>({})  // peerId → 'gre' | 'wg'
const meshBusy = ref(false)
const meshErr = ref('')
const meshBundle = ref<MeshConfigBundle | null>(null)
type MeshTab = 'bird' | 'bringup' | 'filters' | 'peers'
const meshTabs: readonly MeshTab[] = ['bird', 'bringup', 'filters', 'peers']
const meshTab = ref<MeshTab>('bird')

// Active, non-self peers the new node will mesh with.
const meshNodeId = computed(() => meshNode.value?.id ?? '')
const meshPeers = computed(() =>
  meshNode.value ? nodes.value.filter(n => n.id !== meshNodeId.value && n.status === 'active') : [])

function openMesh(n: NodeView) {
  meshNode.value = n
  meshRegion.value = n.region || 0
  meshErr.value = ''
  meshBundle.value = null
  meshTab.value = 'bird'
  // default every link to GRE (the fleet's主流 transport)
  const t: Record<string, string> = {}
  for (const p of nodes.value) if (p.id !== n.id && p.status === 'active') t[p.id] = 'gre'
  meshTransports.value = t
  // reset auto-apply state
  applyTargets.value = {}
  applyConfirm.value = ''
  applyState.value = null
  applyErr.value = ''
  stopApplyPoll()
  meshOpen.value = true
}
function closeMesh() { meshOpen.value = false; meshBundle.value = null; stopApplyPoll() }

async function generateMesh() {
  if (!meshNode.value || meshBusy.value) return
  meshBusy.value = true
  meshErr.value = ''
  try {
    const body: { transports: Record<string, string>; region?: number } = { transports: meshTransports.value }
    if (meshRegion.value > 0) body.region = meshRegion.value
    const env = await api.nodeMeshConfig(meshNode.value.id, body)
    if (!env.ok || !env.data) throw new Error(env.error || 'generate failed')
    meshBundle.value = env.data
  } catch (e: unknown) {
    meshErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    meshBusy.value = false
  }
}

async function copyText(s: string) {
  try { await navigator.clipboard.writeText(s) } catch { /* clipboard blocked; ignore */ }
}

// ── mesh auto-apply (opt-in, per-target) ──
const applyTargets = ref<Record<string, boolean>>({})  // nodeId → selected
const applyConfirm = ref('')
const applyState = ref<OnboardState | null>(null)
const applyErr = ref('')
const applyStarting = ref(false)
let applyTimer: ReturnType<typeof setInterval> | null = null

// WG links are GRE-only for auto-apply (two-ended key exchange stays manual).
function canAutoApply(nodeId: string): boolean {
  if (meshNode.value && nodeId !== meshNode.value.id && meshTransports.value[nodeId] === 'wg') return false
  return true
}
const applyConfirmWord = computed(() => meshNode.value ? `APPLY MESH ${meshNode.value.id}` : '')
const applySelected = computed(() =>
  Object.entries(applyTargets.value).filter(([, v]) => v).map(([k]) => k))
const applyConfirmOK = computed(() => applyConfirmWord.value !== '' && applyConfirm.value === applyConfirmWord.value)
const applyReady = computed(() => applyConfirmOK.value && applySelected.value.length > 0)

function stopApplyPoll() { if (applyTimer) { clearInterval(applyTimer); applyTimer = null } }
function startApplyPoll() {
  stopApplyPoll()
  applyTimer = setInterval(async () => {
    try {
      const env = await api.nodeMeshApplyStatus(meshNode.value!.id)
      if (env.ok && env.data) {
        applyState.value = env.data
        if (env.data.done) { stopApplyPoll(); await refresh() }
      }
    } catch { /* keep polling */ }
  }, 1000)
}

async function runMeshApply() {
  if (!meshNode.value || applyStarting.value) return
  if (applyConfirm.value !== applyConfirmWord.value) {
    applyErr.value = '请输入确认词：' + applyConfirmWord.value
    return
  }
  const targets = applySelected.value
  if (!targets.length) { applyErr.value = '请至少勾选一台要自动应用的机器'; return }
  applyStarting.value = true
  applyErr.value = ''
  applyState.value = null
  try {
    const env = await api.nodeMeshApply(meshNode.value.id, {
      targets, transports: meshTransports.value,
      region: meshRegion.value > 0 ? meshRegion.value : undefined,
      confirm: applyConfirm.value,
    })
    if (!env.ok) throw new Error(env.error || 'apply failed to start')
    applyState.value = env.data ?? null
    startApplyPoll()
  } catch (e: unknown) {
    applyErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    applyStarting.value = false
  }
}

// ────────── confirm-gated actions (decommission / delete) ──────────
type ActionKind = 'decommission' | 'delete'
const confirmOpen = ref(false)
const confirmBusy = ref(false)
const confirmError = ref('')
const confirmKind = ref<ActionKind>('decommission')
const confirmTarget = ref<NodeView | null>(null)

const confirmMeta = computed(() => {
  const n = confirmTarget.value
  const id = n?.id ?? ''
  if (confirmKind.value === 'delete') {
    return {
      title: `永久删除 ${id}`,
      description: `彻底从注册表移除 ${id}，并删除它在 tyo 上的 HMAC 密钥。不可恢复（之后只能重新添加 + 重新上线）。这不会去退订你在服务商那边的 VPS。`,
      severity: 'high' as const,
      expected: `DELETE ${id}`,
    }
  }
  return {
    title: `下架 ${id}`,
    description: `停止抓取 ${id}、从公网地图 / Looking Glass / 状态页隐藏、并抑制它的告警。注册表记录保留，可一键恢复。`,
    severity: 'high' as const,
    expected: `DECOMMISSION ${id}`,
  }
})

function askDecommission(n: NodeView) { confirmKind.value = 'decommission'; openConfirm(n) }
function askDelete(n: NodeView)        { confirmKind.value = 'delete'; openConfirm(n) }
function openConfirm(n: NodeView) {
  confirmTarget.value = n
  confirmError.value = ''
  confirmOpen.value = true
}

async function onConfirm() {
  const n = confirmTarget.value
  if (!n) return
  confirmBusy.value = true
  confirmError.value = ''
  try {
    if (confirmKind.value === 'decommission') {
      const env = await api.nodeDecommission(n.id)
      if (!env.ok) throw new Error(env.error || 'decommission failed')
    } else {
      const env = await api.nodeDelete(n.id)
      if (!env.ok) throw new Error(env.error || 'delete failed')
    }
    confirmOpen.value = false
    await refresh()
  } catch (e: unknown) {
    confirmError.value = e instanceof Error ? e.message : String(e)
  } finally {
    confirmBusy.value = false
  }
}

// Recommission is low-risk (reversible re-add) — plain button, no type-to-confirm.
async function recommission(n: NodeView) {
  try {
    const env = await api.nodeRecommission(n.id)
    if (!env.ok) throw new Error(env.error || 'recommission failed')
    await refresh()
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  }
}

// Quick agent health probe — result shown inline per row.
const healthMsg = ref<Record<string, string>>({})
async function checkHealth(n: NodeView) {
  healthMsg.value = { ...healthMsg.value, [n.id]: '…' }
  try {
    const env = await api.nodeHealth(n.id)
    if (env.ok && env.data) {
      healthMsg.value = { ...healthMsg.value, [n.id]: `✓ ${env.data.status}` }
    } else {
      healthMsg.value = { ...healthMsg.value, [n.id]: `✗ ${env.error || (env.data ? env.data.status : 'unreachable')}` }
    }
  } catch (e: unknown) {
    healthMsg.value = { ...healthMsg.value, [n.id]: '✗ ' + (e instanceof Error ? e.message : String(e)) }
  }
}

// ────────── derived ──────────
function statusLabel(n: NodeView): { text: string; cls: string } {
  if (n.status === 'decommissioned') return { text: '已下架 · OFFLINE', cls: 'text-gray-500 border-gray-700' }
  if (!n.scraped) return { text: '等待 · PENDING', cls: 'text-gray-400 border-gray-700' }
  return n.ok
    ? { text: '在线 · ONLINE', cls: 'text-emerald-400 border-emerald-700' }
    : { text: '不可达 · DOWN', cls: 'text-red-400 border-red-700' }
}
function certLabel(n: NodeView): string {
  if (n.status === 'decommissioned' || !n.cert_days_left) return '—'
  return `${n.cert_days_left}d`
}
const activeCount = computed(() => nodes.value.filter(n => n.status !== 'decommissioned').length)

onMounted(refresh)
</script>

<template>
  <div class="space-y-4">
    <!-- Header -->
    <div class="border border-gray-800 bg-gray-900 p-4">
      <div class="flex items-center justify-between flex-wrap gap-2">
        <div class="flex items-center gap-3">
          <span class="w-1.5 h-1.5 bg-emerald-500"></span>
          <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">服务器 · Servers</h1>
          <span class="text-[10px] tracking-widest text-gray-600 uppercase">
            {{ activeCount }} 在册 / {{ nodes.length }} 总计
          </span>
        </div>
        <div class="flex items-center gap-2">
          <button @click="refresh" :disabled="loading"
                  class="text-[10px] tracking-widest text-gray-500 hover:text-emerald-400 disabled:opacity-30 normal-case">
            ↻ refresh
          </button>
          <button @click="openNew"
                  class="px-2 py-1 border border-emerald-700 text-emerald-400 hover:bg-emerald-900/30 text-[10px] tracking-widest uppercase">
            + 新增服务器
          </button>
        </div>
      </div>
      <p class="mt-2 text-[11px] text-gray-600 normal-case leading-relaxed">
        节点列表持久化在注册表里，增删改即时生效、无需改代码或重启。新增时可「自动识别」地区、填 root 密码一键上线（密码登录→装 fleet 公钥→部署 agent→校验，实时进度）。「下架」可恢复，「永久删除」清除记录与密钥。BGP/bird 仍手动管理。
      </p>
    </div>

    <!-- Error -->
    <div v-if="err" class="border border-red-500/40 bg-red-950/20 p-3 text-xs text-red-400 normal-case">
      ⨯ {{ err }}
    </div>

    <!-- Form -->
    <div v-if="formOpen" class="border border-emerald-700/60 bg-gray-900 p-4 space-y-3">
      <h2 class="text-[10px] tracking-widest uppercase text-emerald-400">
        {{ editingId ? '修改服务器 · edit' : '新增服务器 · add' }}
      </h2>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">id（创建后不可改）</label>
          <input v-model="form.id" :disabled="!!editingId" type="text" placeholder="lax-01"
                 class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none disabled:opacity-40" />
        </div>
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">名称 · label</label>
          <input v-model="form.label" type="text" placeholder="Los Angeles, US"
                 class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none" />
        </div>
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">公网地址 · address (IP)</label>
          <div class="flex gap-2">
            <input v-model="form.address" type="text" placeholder="203.0.113.10"
                   class="flex-1 min-w-0 bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none" />
            <button type="button" @click="autoGeo" :disabled="geoBusy" title="按 IP 自动识别国家/城市/经纬度"
                    class="px-2 border border-gray-700 text-gray-400 hover:border-emerald-500 hover:text-emerald-400 text-[10px] tracking-widest uppercase disabled:opacity-40 whitespace-nowrap">
              {{ geoBusy ? '◌' : '自动识别' }}
            </button>
          </div>
          <div v-if="geoMsg" class="mt-1 text-[10px] normal-case" :class="geoMsg.startsWith('✓') ? 'text-emerald-500' : 'text-amber-400'">{{ geoMsg }}</div>
        </div>
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">国家 ISO · country</label>
          <input v-model="form.country" type="text" placeholder="US" maxlength="2"
                 class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none" />
        </div>
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">纬度 · lat</label>
          <input v-model.number="form.lat" type="number" step="0.01"
                 class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none" />
        </div>
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">经度 · lon</label>
          <input v-model.number="form.lon" type="number" step="0.01"
                 class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none" />
        </div>
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">SSH 用户 · ssh_user</label>
          <input v-model="form.ssh_user" type="text" placeholder="root"
                 class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none" />
        </div>
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">SSH 端口 · ssh_port</label>
          <input v-model.number="form.ssh_port" type="number" min="1" max="65535" placeholder="22"
                 class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none" />
        </div>
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">架构 · arch</label>
          <input v-model="form.arch" type="text" placeholder="amd64"
                 class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none" />
        </div>
      </div>
      <div>
        <label class="block text-[10px] tracking-widest text-gray-500 mb-1">备注 · notes</label>
        <textarea v-model="form.notes" rows="2" placeholder="provider, plan, anything…"
                  class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-300 focus:border-emerald-700 focus:outline-none normal-case"></textarea>
      </div>
      <div v-if="!editingId">
        <label class="block text-[10px] tracking-widest text-gray-500 mb-1">首次登录密码 · root password（可选）</label>
        <input v-model="form.ssh_password" type="password" autocomplete="new-password"
               placeholder="留空 = 该机已配好 fleet 密钥；填了则自动装公钥并上线"
               class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none" />
        <div class="mt-1 text-[10px] text-gray-600 normal-case">仅用于首次连接安装 fleet 公钥，<b>不保存、不写日志</b>，用完即弃。填了就：密码登录 → 装公钥 → provision → 校验，全程实时进度。</div>
      </div>
      <!-- only-when-necessary: SSH private-key login (for boxes with password auth disabled) -->
      <div v-if="!editingId">
        <button type="button" @click="showKeyAuth = !showKeyAuth"
                class="text-[10px] tracking-widest text-gray-500 hover:text-gray-300 normal-case">
          {{ showKeyAuth ? '▾' : '▸' }} 仅必要时 · 用 SSH 私钥登录（该机已禁用密码登录时）
        </button>
        <div v-if="showKeyAuth" class="mt-2 space-y-2">
          <textarea v-model="form.ssh_private_key" rows="4" autocomplete="off" spellcheck="false"
                    placeholder="-----BEGIN OPENSSH PRIVATE KEY-----&#10;…粘贴一把已能登录该机的私钥（如服务商初始 root key）…&#10;-----END OPENSSH PRIVATE KEY-----"
                    class="w-full bg-black border border-gray-800 px-3 py-2 text-[11px] font-mono text-gray-100 focus:border-emerald-700 focus:outline-none normal-case"></textarea>
          <input v-model="form.ssh_key_passphrase" type="password" autocomplete="off"
                 placeholder="私钥密码短语（如私钥已加密）· 可选"
                 class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none" />
          <div class="text-[10px] text-gray-600 normal-case">填了私钥就<b>优先用私钥</b>登录（忽略上面的密码）。同样仅用于首次装公钥、<b>不保存、不写日志</b>，用完即弃。</div>
        </div>
      </div>
      <div class="flex gap-2">
        <button @click="submitForm" :disabled="formBusy"
                class="px-4 py-2 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-[10px] tracking-widest uppercase disabled:opacity-30">
          {{ formBusy ? '◌ 保存中…' : (editingId ? '保存修改' : ((form.ssh_password || form.ssh_private_key) ? '创建并上线' : '创建')) }}
        </button>
        <button @click="formOpen = false"
                class="px-4 py-2 border border-gray-700 text-gray-400 hover:border-gray-500 text-[10px] tracking-widest uppercase">
          取消
        </button>
      </div>
    </div>

    <!-- Empty -->
    <div v-if="!loading && nodes.length === 0" class="border border-gray-800 bg-gray-900 p-6 text-center text-sm text-gray-600 italic normal-case">
      注册表为空 · 点 "新增服务器" 开始
    </div>

    <!-- Table -->
    <div v-else class="border border-gray-800 bg-gray-900 overflow-x-auto">
      <table class="w-full text-xs">
        <thead class="border-b border-gray-800 bg-gray-950/60">
          <tr class="text-[10px] tracking-widest text-gray-500 uppercase">
            <th class="text-left px-3 py-2">节点</th>
            <th class="text-left px-3 py-2">地址</th>
            <th class="text-left px-3 py-2">状态</th>
            <th class="text-left px-3 py-2">证书</th>
            <th class="text-right px-3 py-2">操作</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-800">
          <tr v-for="n in nodes" :key="n.id" class="hover:bg-gray-950/40"
              :class="n.status === 'decommissioned' ? 'opacity-60' : ''">
            <td class="px-3 py-3">
              <div class="flex items-center gap-2">
                <span class="text-sm font-mono text-gray-100">{{ n.id }}</span>
                <span v-if="n.local" class="text-[9px] tracking-widest text-emerald-500 border border-emerald-800 px-1 uppercase">local</span>
              </div>
              <div class="text-[10px] tracking-widest text-gray-600 uppercase mt-0.5">{{ n.label }} · {{ n.country || '—' }}</div>
            </td>
            <td class="px-3 py-3 font-mono text-gray-400">{{ n.address }}<span v-if="n.ssh_port && n.ssh_port !== 22" class="text-gray-600">:{{ n.ssh_port }}</span></td>
            <td class="px-3 py-3">
              <span class="inline-block border px-1.5 py-0.5 text-[10px] tracking-widest uppercase"
                    :class="statusLabel(n).cls">{{ statusLabel(n).text }}</span>
              <div v-if="healthMsg[n.id]" class="text-[10px] mt-1 normal-case font-mono text-gray-400">{{ healthMsg[n.id] }}</div>
            </td>
            <td class="px-3 py-3 font-mono text-gray-400">{{ certLabel(n) }}</td>
            <td class="px-3 py-3 text-right space-x-1 whitespace-nowrap">
              <button @click="openEdit(n)"
                      class="px-2 py-1 border border-gray-700 text-gray-400 hover:border-gray-500 text-[10px] tracking-widest uppercase">edit</button>
              <button v-if="!n.local" @click="checkHealth(n)"
                      class="px-2 py-1 border border-gray-700 text-gray-400 hover:border-blue-500 hover:text-blue-400 text-[10px] tracking-widest uppercase">ping</button>
              <button @click="openMesh(n)" title="生成内部 mesh + BIRD 配置（仅供审阅）"
                      class="px-2 py-1 border border-gray-700 text-gray-400 hover:border-emerald-500 hover:text-emerald-400 text-[10px] tracking-widest uppercase">组网</button>
              <button v-if="n.status === 'decommissioned'" @click="recommission(n)"
                      class="px-2 py-1 border border-emerald-700 text-emerald-400 hover:bg-emerald-900/30 text-[10px] tracking-widest uppercase">恢复</button>
              <button v-else-if="!n.local" @click="askDecommission(n)"
                      class="px-2 py-1 border border-gray-700 text-gray-400 hover:border-amber-500 hover:text-amber-400 text-[10px] tracking-widest uppercase">下架</button>
              <button v-if="!n.local" @click="askDelete(n)"
                      class="px-2 py-1 border border-gray-700 text-gray-400 hover:border-red-500 hover:text-red-400 text-[10px] tracking-widest uppercase">删除</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <ConfirmDialog
      :open="confirmOpen"
      :title="confirmMeta.title"
      :description="confirmMeta.description"
      :severity="confirmMeta.severity"
      :expectedConfirmation="confirmMeta.expected"
      :busy="confirmBusy"
      :errorMsg="confirmError"
      @cancel="confirmOpen = false"
      @confirm="onConfirm"
    />

    <!-- Onboard modal — live step progress -->
    <div v-if="onboardOpen" class="fixed inset-0 z-[100] flex items-end sm:items-center justify-center sm:p-4 bg-black/75 backdrop-blur-sm"
         @click.self="closeOnboard">
      <div class="border-2 border-emerald-700/60 bg-gray-900 max-w-md w-full font-mono rounded-t-lg sm:rounded-none">
        <div class="px-4 py-2 border-b border-emerald-800 bg-emerald-950/30 text-emerald-400 text-xs tracking-widest uppercase flex items-center justify-between">
          <span>上线 · onboard {{ onboardNodeId }}</span>
          <button @click="closeOnboard" class="text-gray-500 hover:text-gray-300">✕</button>
        </div>
        <div class="p-4 space-y-3">
          <!-- Starting (POST in flight, no steps yet) -->
          <div v-if="!onboardState" class="text-[11px] tracking-widest uppercase"
               :class="onboardErr ? 'text-red-400' : 'text-amber-400'">
            {{ onboardErr ? ('✗ ' + onboardErr) : '◌ 启动中…' }}
          </div>

          <!-- Running / done: progress bar + elapsed, then live step list -->
          <template v-else>
            <!-- Overall progress -->
            <div>
              <div class="flex items-center justify-between text-[10px] tracking-widest uppercase mb-1">
                <span :class="onboardState.running ? 'text-amber-400' : (onboardState.ok ? 'text-emerald-400' : 'text-red-400')">
                  {{ Math.round(onboardProgress * 100) }}%
                </span>
                <span class="text-gray-500 font-mono">⏱ {{ fmtDur(onboardElapsedMs) }}</span>
              </div>
              <div class="h-1 bg-gray-800 overflow-hidden">
                <div class="h-full transition-[width] duration-500 ease-out"
                     :class="onboardState.ok && onboardState.done ? 'bg-emerald-500' : (!onboardState.running && !onboardState.ok ? 'bg-red-500' : 'bg-emerald-500')"
                     :style="{ width: (onboardProgress * 100).toFixed(1) + '%' }"></div>
              </div>
            </div>

            <ul class="space-y-2">
              <li v-for="(s, i) in onboardState.steps" :key="i" class="flex items-start gap-2">
                <span class="w-4 text-center text-sm leading-5"
                      :class="{
                        'text-gray-600': s.status === 'pending' || s.status === 'skip',
                        'text-amber-400': s.status === 'running',
                        'text-emerald-400': s.status === 'ok',
                        'text-red-400': s.status === 'fail',
                      }">
                  {{ s.status === 'ok' ? '✓' : s.status === 'fail' ? '✗' : s.status === 'running' ? '◌' : s.status === 'skip' ? '–' : '·' }}
                </span>
                <div class="min-w-0 flex-1">
                  <div class="flex items-baseline justify-between gap-2">
                    <span class="text-sm" :class="s.status === 'fail' ? 'text-red-300' : 'text-gray-200'">{{ s.name }}</span>
                    <span v-if="s.started" class="text-[10px] tabular-nums shrink-0"
                          :class="s.status === 'running' ? 'text-amber-400' : 'text-gray-600'">{{ stepDur(s) }}</span>
                  </div>
                  <div v-if="s.message" class="text-[10px] text-gray-500 normal-case break-all leading-relaxed">{{ s.message }}</div>
                </div>
              </li>
            </ul>
            <!-- Live streamed provision log -->
            <div v-if="onboardState.log && onboardState.log.length" ref="onboardLogEl"
                 class="bg-black border border-gray-800 p-2.5 max-h-44 overflow-y-auto text-[10px] leading-relaxed font-mono text-gray-400 normal-case whitespace-pre-wrap break-all">
              <div v-for="(line, i) in onboardState.log" :key="i" :class="logLineClass(line)">{{ line }}</div>
            </div>
            <div v-if="onboardErr" class="text-xs text-red-400 normal-case">⨯ {{ onboardErr }}</div>
            <div class="flex items-center justify-between pt-1">
              <span class="text-[10px] tracking-widest uppercase"
                    :class="onboardState.running ? 'text-amber-400' : (onboardState.ok ? 'text-emerald-400' : 'text-red-400')">
                {{ onboardState.running ? '◌ 进行中…' : (onboardState.ok ? '✓ 上线成功' : '✗ 上线失败') }}
              </span>
              <button @click="closeOnboard"
                      class="px-3 py-1.5 border border-gray-700 text-gray-400 hover:border-gray-500 text-[10px] tracking-widest uppercase">
                {{ onboardState.running ? '后台运行' : '关闭' }}
              </button>
            </div>
          </template>
        </div>
      </div>
    </div>

    <!-- Mesh / BIRD config generator (review-only) -->
    <div v-if="meshOpen" class="fixed inset-0 z-[100] flex items-stretch sm:items-center justify-center sm:p-4 bg-black/75 backdrop-blur-sm"
         @click.self="closeMesh">
      <div class="border-2 border-emerald-700/60 bg-gray-900 w-full sm:max-w-3xl sm:max-h-[90vh] flex flex-col font-mono">
        <div class="px-4 py-2 border-b border-emerald-800 bg-emerald-950/30 text-emerald-400 text-xs tracking-widest uppercase flex items-center justify-between shrink-0">
          <span>组网配置 · mesh {{ meshNode?.id }}</span>
          <button @click="closeMesh" class="text-gray-500 hover:text-gray-300">✕</button>
        </div>
        <div class="p-4 overflow-y-auto space-y-3">
          <p class="text-[11px] text-gray-500 normal-case leading-relaxed">
            按现有机器写法生成内部 mesh + BIRD 配置，<b class="text-gray-300">仅供审阅复制</b>，不会自动改任何机器。应用时用 <code class="text-emerald-400">birdc configure soft</code>（勿重启 bird）。
          </p>

          <!-- inputs -->
          <div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
            <div>
              <label class="block text-[10px] tracking-widest text-gray-500 mb-1">region（同城自动）</label>
              <input v-model.number="meshRegion" type="number" min="1" max="999" placeholder="如 53"
                     class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none" />
            </div>
            <div class="col-span-1 sm:col-span-3 flex items-end">
              <button @click="generateMesh" :disabled="meshBusy"
                      class="px-4 py-1.5 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-[10px] tracking-widest uppercase disabled:opacity-30">
                {{ meshBusy ? '◌ 生成中…' : '生成配置' }}
              </button>
            </div>
          </div>

          <!-- per-peer transport selectors -->
          <div>
            <div class="text-[10px] tracking-widest text-gray-500 uppercase mb-1">mesh 链路传输（逐链路）</div>
            <div class="grid grid-cols-2 sm:grid-cols-3 gap-2">
              <div v-for="p in meshPeers" :key="p.id" class="flex items-center justify-between border border-gray-800 bg-gray-950/50 px-2 py-1">
                <span class="text-xs font-mono text-gray-300">{{ p.id }}</span>
                <select v-model="meshTransports[p.id]"
                        class="bg-black border border-gray-800 text-[10px] text-gray-200 px-1 py-0.5 focus:border-emerald-700 focus:outline-none">
                  <option value="gre">GRE</option>
                  <option value="wg">WG</option>
                </select>
              </div>
            </div>
          </div>

          <div v-if="meshErr" class="text-xs text-red-400 normal-case">⨯ {{ meshErr }}</div>

          <!-- output -->
          <template v-if="meshBundle">
            <div v-if="meshBundle.warnings && meshBundle.warnings.length"
                 class="border border-amber-700/40 bg-amber-950/15 p-2 text-[11px] text-amber-300 normal-case leading-relaxed space-y-0.5">
              <div v-for="(wn, i) in meshBundle.warnings" :key="i">⚠ {{ wn }}</div>
            </div>

            <!-- tabs -->
            <div class="flex gap-1 border-b border-gray-800 text-[10px] tracking-widest uppercase">
              <button v-for="tab in meshTabs" :key="tab" @click="meshTab = tab"
                      class="px-3 py-1.5 -mb-px border-b-2"
                      :class="meshTab === tab ? 'border-emerald-500 text-emerald-400' : 'border-transparent text-gray-500 hover:text-gray-300'">
                {{ tab === 'bird' ? 'bird.conf' : tab === 'bringup' ? '隧道命令' : tab === 'filters' ? 'filters' : '对端片段' }}
              </button>
            </div>

            <!-- bird.conf -->
            <div v-show="meshTab === 'bird'">
              <div class="flex justify-between items-center mb-1">
                <span class="text-[10px] text-gray-500 normal-case">新节点 /etc/bird/bird.conf · anchor {{ meshBundle.anchor }}</span>
                <button @click="copyText(meshBundle.new_node_bird)" class="text-[10px] tracking-widest text-gray-500 hover:text-emerald-400 uppercase">复制</button>
              </div>
              <pre class="bg-black border border-gray-800 p-3 text-[11px] leading-relaxed text-gray-300 overflow-x-auto max-h-72 whitespace-pre">{{ meshBundle.new_node_bird }}</pre>
            </div>

            <!-- bringup -->
            <div v-show="meshTab === 'bringup'">
              <div class="flex justify-between items-center mb-1">
                <span class="text-[10px] text-gray-500 normal-case">新节点上执行（dummy0 anchor + 各链路）</span>
                <button @click="copyText((meshBundle.bringup || []).join('\n'))" class="text-[10px] tracking-widest text-gray-500 hover:text-emerald-400 uppercase">复制</button>
              </div>
              <pre class="bg-black border border-gray-800 p-3 text-[11px] leading-relaxed text-gray-300 overflow-x-auto max-h-72 whitespace-pre">{{ (meshBundle.bringup || []).join('\n') }}</pre>
            </div>

            <!-- filters -->
            <div v-show="meshTab === 'filters'">
              <div class="flex justify-between items-center mb-1">
                <span class="text-[10px] text-gray-500 normal-case">/etc/bird/filters_templates.conf（与全队一致）</span>
                <button @click="copyText(meshBundle.filters)" class="text-[10px] tracking-widest text-gray-500 hover:text-emerald-400 uppercase">复制</button>
              </div>
              <pre class="bg-black border border-gray-800 p-3 text-[11px] leading-relaxed text-gray-300 overflow-x-auto max-h-72 whitespace-pre">{{ meshBundle.filters }}</pre>
            </div>

            <!-- peer snippets -->
            <div v-show="meshTab === 'peers'" class="space-y-3">
              <p class="text-[10px] text-gray-500 normal-case">全 mesh：在每台现有节点贴下面对应片段，再执行 <code class="text-emerald-400">birdc configure soft</code>。</p>
              <div v-for="s in meshBundle.peer_snippets" :key="s.node_id"
                   class="border border-gray-800 bg-gray-950/40 p-2">
                <div class="flex items-center justify-between mb-1">
                  <span class="text-xs font-mono text-gray-200">{{ s.node_id }} <span class="text-[10px] text-gray-600 uppercase">· {{ s.transport }}</span></span>
                </div>
                <pre class="bg-black border border-gray-800 p-2 text-[10px] leading-relaxed text-gray-300 overflow-x-auto whitespace-pre">{{ s.tunnel }}

{{ s.bird }}</pre>
              </div>
            </div>

            <!-- ── auto-apply (opt-in, per-target) ── -->
            <div class="border-t border-gray-800 pt-3 space-y-2">
              <div class="text-[10px] tracking-widest text-amber-400 uppercase">⚡ 自动配置（改活路由器 · 加法 · configure soft · 失败自动回滚）</div>
              <p class="text-[11px] text-gray-500 normal-case leading-relaxed">
                勾选要<b class="text-gray-300">自动应用</b>的机器。每台先备份 bird.conf，只追加缺失的 iBGP 块 + 起 GRE 隧道，再 <code class="text-emerald-400">birdc configure soft</code>，失败自动恢复备份。WG 链路不可自动应用（走手动片段）。
              </p>
              <div class="grid grid-cols-2 sm:grid-cols-3 gap-2">
                <!-- new node itself -->
                <label class="flex items-center gap-2 border border-gray-800 bg-gray-950/50 px-2 py-1 text-xs"
                       :class="canAutoApply(meshNodeId) ? 'cursor-pointer' : 'opacity-40'">
                  <input type="checkbox" v-model="applyTargets[meshNodeId]" :disabled="!canAutoApply(meshNodeId)" />
                  <span class="font-mono text-emerald-300">{{ meshNodeId }} <span class="text-gray-600">(新)</span></span>
                </label>
                <label v-for="p in meshPeers" :key="p.id"
                       class="flex items-center gap-2 border border-gray-800 bg-gray-950/50 px-2 py-1 text-xs"
                       :class="canAutoApply(p.id) ? 'cursor-pointer' : 'opacity-40'">
                  <input type="checkbox" v-model="applyTargets[p.id]" :disabled="!canAutoApply(p.id)" />
                  <span class="font-mono text-gray-300">{{ p.id }}</span>
                  <span v-if="meshTransports[p.id] === 'wg'" class="text-[9px] text-gray-600 normal-case">WG·手动</span>
                </label>
              </div>
              <div class="flex flex-wrap items-end gap-2">
                <div class="flex-1 min-w-[180px]">
                  <label class="block text-[10px] tracking-widest text-gray-500 mb-1 normal-case">
                    输入确认词：<code class="text-amber-400 select-all">{{ applyConfirmWord }}</code>
                  </label>
                  <input v-model="applyConfirm" type="text" :placeholder="applyConfirmWord"
                         class="w-full bg-black border px-2 py-1.5 text-sm font-mono text-gray-100 focus:outline-none"
                         :class="applyConfirmOK ? 'border-emerald-600' : 'border-gray-800 focus:border-red-600'" />
                  <div class="mt-1 text-[10px] normal-case"
                       :class="applyConfirmOK ? 'text-emerald-400' : (applyConfirm ? 'text-amber-400' : 'text-gray-600')">
                    {{ applyConfirmOK ? '✓ 确认词正确' : (applyConfirm ? '确认词不符,需与上方完全一致' : '逐字输入上方确认词') }}
                    · 已选 {{ applySelected.length }} 台
                  </div>
                </div>
                <button @click="runMeshApply" :disabled="applyStarting"
                        class="px-4 py-1.5 border text-[10px] tracking-widest uppercase disabled:opacity-30"
                        :class="applyReady ? 'border-red-600 text-red-400 hover:bg-red-600 hover:text-white' : 'border-gray-700 text-gray-500'">
                  {{ applyStarting ? '◌ 启动中…' : '▶ 自动应用' }}
                </button>
              </div>
              <div v-if="applyErr" class="text-xs text-red-400 normal-case">⨯ {{ applyErr }}</div>

              <!-- live progress -->
              <div v-if="applyState" class="border border-gray-800 bg-gray-950/50 p-2 space-y-2">
                <ul class="space-y-1">
                  <li v-for="(s, i) in applyState.steps" :key="i" class="flex items-center gap-2 text-xs">
                    <span class="w-4 text-center"
                          :class="{ 'text-gray-600': s.status==='pending', 'text-amber-400': s.status==='running', 'text-emerald-400': s.status==='ok', 'text-red-400': s.status==='fail' }">
                      {{ s.status==='ok' ? '✓' : s.status==='fail' ? '✗' : s.status==='running' ? '◌' : '·' }}
                    </span>
                    <span class="text-gray-300">{{ s.name }}</span>
                    <span v-if="s.message" class="text-[10px] text-gray-500 normal-case">· {{ s.message }}</span>
                  </li>
                </ul>
                <pre v-if="applyState.log && applyState.log.length"
                     class="bg-black border border-gray-800 p-2 text-[10px] leading-relaxed text-gray-400 normal-case max-h-40 overflow-y-auto whitespace-pre-wrap break-all">{{ applyState.log.join('\n') }}</pre>
                <div class="text-[10px] tracking-widest uppercase"
                     :class="applyState.running ? 'text-amber-400' : (applyState.ok ? 'text-emerald-400' : 'text-red-400')">
                  {{ applyState.running ? '◌ 应用中…' : (applyState.ok ? '✓ 全部已应用' : '✗ 有目标失败（已对失败项回滚）') }}
                </div>
              </div>
            </div>
          </template>
        </div>
        <div class="px-4 py-2 border-t border-gray-800 shrink-0 flex justify-end">
          <button @click="closeMesh" class="px-3 py-1.5 border border-gray-700 text-gray-400 hover:border-gray-500 text-[10px] tracking-widest uppercase">关闭</button>
        </div>
      </div>
    </div>
  </div>
</template>
