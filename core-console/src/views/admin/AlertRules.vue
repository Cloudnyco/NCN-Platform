<script setup lang="ts">
// Alert Rules — user-editable, data-driven alert engine.
//
// The alert rules used to be hardcoded Go closures in backend/alerts.go: tuning
// a threshold or silencing a noisy rule meant editing Go + redeploying. This
// page drives the persistent rule store instead: any admin can create/tune/
// disable rules, group them, scope a group to a subset of nodes, mute a whole
// group, toggle Telegram per rule, and PREVIEW a rule against the live fleet
// (blast radius) before saving. Built-in rules can be tuned/disabled but not
// deleted.

import { computed, onMounted, ref } from 'vue'
import { useSessionStore } from '@/stores/session'
import {
  api,
  type AlertRuleDef, type RuleGroup, type MetricMeta, type AlertOp,
  type AlertPreviewResult, type NodeView,
} from '@/api/client'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const session = useSessionStore()
const isAdmin = computed(() => session.role === 'admin')

const groups = ref<RuleGroup[]>([])
const rules = ref<AlertRuleDef[]>([])
const metrics = ref<MetricMeta[]>([])
const nodes = ref<NodeView[]>([])
const loading = ref(false)
const err = ref<string | null>(null)

const ops: { v: AlertOp; sym: string }[] = [
  { v: 'gt', sym: '>' }, { v: 'gte', sym: '≥' }, { v: 'lt', sym: '<' },
  { v: 'lte', sym: '≤' }, { v: 'eq', sym: '==' }, { v: 'ne', sym: '≠' },
]
function opSym(o: AlertOp): string { return (ops.find(x => x.v === o) || { sym: o }).sym }
function metricLabel(key: string): string { return (metrics.value.find(m => m.key === key) || { label: key }).label }
function metricUnit(key: string): string { return (metrics.value.find(m => m.key === key) || { unit: '' }).unit }

function nowSec(): number { return Math.floor(Date.now() / 1000) }
function groupMuted(g: RuleGroup): boolean { return !!g.mute_until && g.mute_until > nowSec() }
function rulesOf(groupId: string): AlertRuleDef[] { return rules.value.filter(r => r.group_id === groupId) }
function scopeLabel(g: RuleGroup): string {
  const parts: string[] = []
  if (g.node_ids && g.node_ids.length) parts.push(g.node_ids.join(','))
  if (g.regions && g.regions.length) parts.push('区域 ' + g.regions.join(','))
  return parts.length ? parts.join(' · ') : '全部节点'
}

async function refresh() {
  if (!isAdmin.value) return
  loading.value = true
  err.value = null
  try {
    const [cfg, mx, nd] = await Promise.all([api.alertRulesList(), api.alertMetrics(), api.nodesList()])
    if (!cfg.ok) throw new Error(cfg.error || 'list failed')
    groups.value = cfg.data?.groups ?? []
    rules.value = cfg.data?.rules ?? []
    if (mx.ok) metrics.value = mx.data ?? []
    if (nd.ok) nodes.value = nd.data ?? []
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}
onMounted(refresh)

// ── inline toggles (patch) ──
async function toggleRule(r: AlertRuleDef, field: 'enabled' | 'notify_tg') {
  try {
    const env = await api.alertRulePatch(r.id, { [field]: !r[field] })
    if (!env.ok) throw new Error(env.error || 'patch failed')
    await refresh()
  } catch (e: unknown) { err.value = e instanceof Error ? e.message : String(e) }
}
async function toggleGroupEnabled(g: RuleGroup) {
  try {
    const env = await api.alertGroupPatch(g.id, { enabled: !g.enabled })
    if (!env.ok) throw new Error(env.error || 'patch failed')
    await refresh()
  } catch (e: unknown) { err.value = e instanceof Error ? e.message : String(e) }
}
function ruleMuted(r: AlertRuleDef): boolean { return !!r.mute_until && r.mute_until > nowSec() }
async function muteRule(r: AlertRuleDef, mins: number) {
  try {
    const until = mins > 0 ? nowSec() + mins * 60 : 0
    const env = await api.alertRulePatch(r.id, { mute_until: until })
    if (!env.ok) throw new Error(env.error || 'patch failed')
    await refresh()
  } catch (e: unknown) { err.value = e instanceof Error ? e.message : String(e) }
}

async function muteGroup(g: RuleGroup, mins: number) {
  try {
    const until = mins > 0 ? nowSec() + mins * 60 : 0
    const env = await api.alertGroupPatch(g.id, { mute_until: until })
    if (!env.ok) throw new Error(env.error || 'patch failed')
    await refresh()
  } catch (e: unknown) { err.value = e instanceof Error ? e.message : String(e) }
}

// Durations are edited in MINUTES (friendlier than raw seconds); the API
// stores seconds. Whole-minute granularity.
function secsToMin(s?: number): number { return Math.round((s ?? 0) / 60) }
function minToSecs(m: number): number { return Math.max(0, Math.round(m || 0)) * 60 }
function mins(s?: number): string { return Math.round((s ?? 0) / 60) + 'm' }

// ── rule form (create / edit) ──
interface RuleForm {
  id: string; group_id: string; name: string; description: string
  metric: string; op: AlertOp; threshold: number
  anomaly: boolean; anomaly_sigma: number; anomaly_window: number; anomaly_min_delta: number
  sustain_min: number; resolve_min: number; escalate_min: number; repeat_min: number
  severity: 'info' | 'warn' | 'crit'; enabled: boolean; notify_tg: boolean
}
function emptyRuleForm(): RuleForm {
  return { id: '', group_id: 'all', name: '', description: '', metric: 'cpu_pct', op: 'gt',
    threshold: 0, anomaly: false, anomaly_sigma: 5, anomaly_window: 120, anomaly_min_delta: 0,
    sustain_min: 0, resolve_min: 0, escalate_min: 0, repeat_min: 0,
    severity: 'warn', enabled: true, notify_tg: true }
}
const ruleFormOpen = ref(false)
const ruleEditingId = ref<string | null>(null)
const ruleForm = ref<RuleForm>(emptyRuleForm())
const ruleBusy = ref(false)
const sustainPresets = [0, 1, 2, 5, 10]

function openNewRule() { ruleEditingId.value = null; ruleForm.value = emptyRuleForm(); ruleFormOpen.value = true }
function openEditRule(r: AlertRuleDef) {
  ruleEditingId.value = r.id
  ruleForm.value = {
    id: r.id, group_id: r.group_id, name: r.name, description: r.description ?? '',
    metric: r.metric, op: r.op, threshold: r.threshold,
    anomaly: !!r.anomaly_sigma, anomaly_sigma: r.anomaly_sigma || 5,
    anomaly_window: r.anomaly_window || 120, anomaly_min_delta: r.anomaly_min_delta || 0,
    sustain_min: secsToMin(r.sustain_secs), resolve_min: secsToMin(r.resolve_secs),
    escalate_min: secsToMin(r.escalate_secs), repeat_min: secsToMin(r.repeat_secs),
    severity: r.severity, enabled: r.enabled, notify_tg: r.notify_tg,
  }
  ruleFormOpen.value = true
}
async function submitRule() {
  if (!ruleForm.value.name.trim()) { err.value = 'name required'; return }
  ruleBusy.value = true
  err.value = null
  const durs = {
    sustain_secs: minToSecs(ruleForm.value.sustain_min),
    resolve_secs: minToSecs(ruleForm.value.resolve_min),
    escalate_secs: minToSecs(ruleForm.value.escalate_min),
    repeat_secs: minToSecs(ruleForm.value.repeat_min),
  }
  // Anomaly mode: send the σ/window/floor when enabled, else 0 to clear it.
  const anom = ruleForm.value.anomaly
    ? { anomaly_sigma: ruleForm.value.anomaly_sigma, anomaly_window: ruleForm.value.anomaly_window, anomaly_min_delta: ruleForm.value.anomaly_min_delta }
    : { anomaly_sigma: 0, anomaly_window: 0, anomaly_min_delta: 0 }
  try {
    if (ruleEditingId.value) {
      const env = await api.alertRulePatch(ruleEditingId.value, {
        name: ruleForm.value.name, description: ruleForm.value.description, group_id: ruleForm.value.group_id,
        metric: ruleForm.value.metric, op: ruleForm.value.op, threshold: ruleForm.value.threshold,
        severity: ruleForm.value.severity, notify_tg: ruleForm.value.notify_tg, ...durs, ...anom,
      })
      if (!env.ok) throw new Error(env.error || 'patch failed')
    } else {
      const env = await api.alertRuleCreate({
        id: ruleForm.value.id.trim().toLowerCase(), group_id: ruleForm.value.group_id, name: ruleForm.value.name,
        description: ruleForm.value.description, metric: ruleForm.value.metric, op: ruleForm.value.op,
        threshold: ruleForm.value.threshold, severity: ruleForm.value.severity,
        enabled: ruleForm.value.enabled, notify_tg: ruleForm.value.notify_tg, ...durs, ...anom,
      })
      if (!env.ok) throw new Error(env.error || 'create failed')
    }
    ruleFormOpen.value = false
    await refresh()
  } catch (e: unknown) { err.value = e instanceof Error ? e.message : String(e) }
  finally { ruleBusy.value = false }
}

// ── group form (create / edit) ──
interface GroupForm {
  id: string; name: string; description: string; node_ids: string[]; regions_csv: string
  suppress_tg: boolean; min_severity: '' | 'info' | 'warn' | 'crit'; default_sustain_min: number
}
function emptyGroupForm(): GroupForm {
  return { id: '', name: '', description: '', node_ids: [], regions_csv: '',
    suppress_tg: false, min_severity: '', default_sustain_min: 0 }
}
const groupFormOpen = ref(false)
const groupEditingId = ref<string | null>(null)
const groupForm = ref<GroupForm>(emptyGroupForm())
const groupBusy = ref(false)
function openNewGroup() { groupEditingId.value = null; groupForm.value = emptyGroupForm(); groupFormOpen.value = true }
function openEditGroup(g: RuleGroup) {
  groupEditingId.value = g.id
  groupForm.value = {
    id: g.id, name: g.name, description: g.description ?? '',
    node_ids: [...(g.node_ids ?? [])], regions_csv: (g.regions ?? []).join(','),
    suppress_tg: g.suppress_tg ?? false, min_severity: g.min_severity ?? '',
    default_sustain_min: secsToMin(g.default_sustain_secs),
  }
  groupFormOpen.value = true
}
async function submitGroup() {
  if (!groupForm.value.name.trim()) { err.value = 'name required'; return }
  groupBusy.value = true
  err.value = null
  try {
    const regions = groupForm.value.regions_csv.split(',').map(s => parseInt(s.trim(), 10)).filter(n => !isNaN(n))
    const fields = {
      name: groupForm.value.name, description: groupForm.value.description,
      node_ids: groupForm.value.node_ids, regions,
      suppress_tg: groupForm.value.suppress_tg, min_severity: groupForm.value.min_severity,
      default_sustain_secs: minToSecs(groupForm.value.default_sustain_min),
    }
    if (groupEditingId.value) {
      const env = await api.alertGroupPatch(groupEditingId.value, fields)
      if (!env.ok) throw new Error(env.error || 'patch failed')
    } else {
      const env = await api.alertGroupCreate({ id: groupForm.value.id.trim().toLowerCase(), enabled: true, ...fields })
      if (!env.ok) throw new Error(env.error || 'create failed')
    }
    groupFormOpen.value = false
    await refresh()
  } catch (e: unknown) { err.value = e instanceof Error ? e.message : String(e) }
  finally { groupBusy.value = false }
}

// ── preview (blast radius) ──
const previewOpen = ref(false)
const previewRule = ref<AlertRuleDef | null>(null)
const previewResults = ref<AlertPreviewResult[]>([])
const previewBusy = ref(false)
async function openPreview(r: AlertRuleDef) {
  previewRule.value = r
  previewResults.value = []
  previewOpen.value = true
  previewBusy.value = true
  try {
    const g = groups.value.find(x => x.id === r.group_id)
    const env = await api.alertPreview({
      metric: r.metric, op: r.op, threshold: r.threshold,
      node_ids: g?.node_ids, regions: g?.regions,
    })
    if (!env.ok) throw new Error(env.error || 'preview failed')
    previewResults.value = (env.data?.results ?? []).sort((a, b) => Number(b.firing) - Number(a.firing))
  } catch (e: unknown) { err.value = e instanceof Error ? e.message : String(e) }
  finally { previewBusy.value = false }
}
const previewFiring = computed(() => previewResults.value.filter(x => x.firing).length)

// ── delete (ConfirmDialog) ──
const confirmOpen = ref(false)
const confirmBusy = ref(false)
const confirmError = ref('')
const pendingDelete = ref<{ kind: 'rule' | 'group'; id: string } | null>(null)
const confirmMeta = computed(() => {
  const p = pendingDelete.value
  if (!p) return { title: '', description: '', expected: '' }
  return { title: '删除' + (p.kind === 'rule' ? '规则' : '规则组') + ' ' + p.id, description: '此操作不可撤销。', expected: 'DELETE ' + p.id }
})
function askDeleteRule(r: AlertRuleDef) { pendingDelete.value = { kind: 'rule', id: r.id }; confirmError.value = ''; confirmOpen.value = true }
function askDeleteGroup(g: RuleGroup) { pendingDelete.value = { kind: 'group', id: g.id }; confirmError.value = ''; confirmOpen.value = true }
async function onConfirmDelete() {
  const p = pendingDelete.value
  if (!p) return
  confirmBusy.value = true
  confirmError.value = ''
  try {
    const env = p.kind === 'rule' ? await api.alertRuleDelete(p.id) : await api.alertGroupDelete(p.id)
    if (!env.ok) throw new Error(env.error || 'delete failed')
    confirmOpen.value = false
    await refresh()
  } catch (e: unknown) { confirmError.value = e instanceof Error ? e.message : String(e) }
  finally { confirmBusy.value = false }
}

function sevClass(s: string): string {
  if (s === 'crit') return 'text-red-400 border-red-700'
  if (s === 'warn') return 'text-amber-400 border-amber-700'
  return 'text-gray-400 border-gray-700'
}
</script>

<template>
  <div class="space-y-4">
    <div class="border border-gray-800 bg-gray-900 p-4">
      <div class="flex items-center justify-between flex-wrap gap-2">
        <div class="flex items-center gap-3">
          <span class="w-1.5 h-1.5 bg-emerald-500"></span>
          <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">告警规则 · Alert Rules</h1>
          <span class="text-[10px] tracking-widest text-gray-600 uppercase">{{ rules.length }} 规则 / {{ groups.length }} 组</span>
        </div>
        <div class="flex items-center gap-2">
          <button @click="refresh" :disabled="loading" class="px-2 py-1 border border-gray-700 text-gray-400 hover:border-gray-500 text-[10px] tracking-widest uppercase">↻ refresh</button>
          <button @click="openNewGroup" class="px-2 py-1 border border-gray-700 text-gray-300 hover:border-emerald-600 text-[10px] tracking-widest uppercase">+ 规则组</button>
          <button @click="openNewRule" class="px-3 py-1 border border-emerald-600 text-emerald-400 hover:bg-emerald-600 hover:text-black text-[10px] tracking-widest uppercase">+ 新增规则</button>
        </div>
      </div>
      <p class="mt-2 text-[11px] text-gray-500 normal-case">规则 = 指标 + 比较符 + 阈值。可分组、给组绑节点、整组静音、每条单独开关 Telegram。保存前可「试算」看当前会在哪些节点告警。内置规则可调可禁,不可删。</p>
    </div>

    <div v-if="err" class="border border-red-500/40 bg-red-950/20 p-3 text-xs text-red-400 normal-case">⨯ {{ err }}</div>
    <div v-if="!isAdmin" class="border border-amber-700/40 bg-amber-950/20 p-3 text-xs text-amber-400">需要 admin 权限。</div>

    <!-- groups -->
    <div v-for="g in groups" :key="g.id" class="border border-gray-800 bg-gray-900">
      <div class="flex items-center justify-between flex-wrap gap-2 px-4 py-2 border-b border-gray-800 bg-gray-950/40">
        <div class="flex items-center gap-2 min-w-0">
          <span class="text-sm text-gray-200 font-mono">{{ g.name }}</span>
          <span class="text-[10px] tracking-widest text-gray-600 uppercase">{{ g.id }}</span>
          <span class="text-[10px] text-emerald-400/80 border border-emerald-800 px-1 normal-case">{{ scopeLabel(g) }}</span>
          <span v-if="!g.enabled" class="text-[9px] text-gray-500 border border-gray-700 px-1 uppercase">已停用</span>
          <span v-else-if="groupMuted(g)" class="text-[9px] text-amber-400 border border-amber-700 px-1 uppercase">静音中</span>
          <span v-if="g.suppress_tg" class="text-[9px] text-gray-400 border border-gray-700 px-1 uppercase">TG 关</span>
          <span v-else-if="g.min_severity" class="text-[9px] text-amber-400/80 border border-amber-800 px-1 uppercase">TG≥{{ g.min_severity }}</span>
          <span v-if="g.default_sustain_secs" class="text-[9px] text-sky-400/80 border border-sky-900 px-1">默认持续 {{ mins(g.default_sustain_secs) }}</span>
        </div>
        <div class="flex items-center gap-1">
          <button @click="openEditGroup(g)" class="px-2 py-0.5 border border-gray-700 text-gray-400 hover:border-emerald-500 hover:text-emerald-400 text-[10px] uppercase">编辑组</button>
          <button @click="toggleGroupEnabled(g)" class="px-2 py-0.5 border border-gray-700 text-gray-400 hover:border-gray-500 text-[10px] uppercase">{{ g.enabled ? '停用组' : '启用组' }}</button>
          <button @click="muteGroup(g, 60)" class="px-2 py-0.5 border border-gray-700 text-gray-400 hover:border-amber-600 text-[10px] uppercase">静音1h</button>
          <button v-if="groupMuted(g)" @click="muteGroup(g, 0)" class="px-2 py-0.5 border border-gray-700 text-gray-400 hover:border-emerald-600 text-[10px] uppercase">取消静音</button>
          <button v-if="g.id !== 'all'" @click="askDeleteGroup(g)" class="px-2 py-0.5 border border-gray-700 text-gray-500 hover:border-red-600 hover:text-red-400 text-[10px] uppercase">删组</button>
        </div>
      </div>
      <p v-if="g.description" class="px-4 py-1.5 text-[11px] text-gray-500 normal-case border-b border-gray-800/60">{{ g.description }}</p>
      <div class="overflow-x-auto">
        <table class="w-full text-xs">
          <thead>
            <tr class="text-[10px] tracking-widest text-gray-600 uppercase border-b border-gray-800">
              <th class="text-left px-3 py-2">规则</th>
              <th class="text-left px-3 py-2">条件</th>
              <th class="text-left px-3 py-2">级别</th>
              <th class="text-center px-3 py-2">TG</th>
              <th class="text-center px-3 py-2">启用</th>
              <th class="text-right px-3 py-2">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in rulesOf(g.id)" :key="r.id" class="border-b border-gray-800/60 hover:bg-gray-800/20">
              <td class="px-3 py-2">
                <div class="text-gray-200">{{ r.name }}
                  <span v-if="r.builtin" class="ml-1 text-[9px] text-gray-600 border border-gray-700 px-1 uppercase">内置</span>
                </div>
                <div class="text-[10px] text-gray-600 font-mono">{{ r.id }}</div>
              </td>
              <td class="px-3 py-2 font-mono text-gray-300">
                <template v-if="r.anomaly_sigma">{{ metricLabel(r.metric) }} 偏离基线 {{ opSym(r.op) }}{{ r.anomaly_sigma }}σ</template>
                <template v-else>{{ metricLabel(r.metric) }} {{ opSym(r.op) }} {{ r.threshold }}<span class="text-gray-600">{{ metricUnit(r.metric) }}</span></template>
                <div class="mt-0.5 flex flex-wrap gap-1">
                  <span v-if="r.anomaly_sigma" class="text-[9px] text-fuchsia-400/80 border border-fuchsia-900 px-1">异常检测 · 窗口 {{ Math.round((r.anomaly_window||120)/2) }}m<template v-if="r.anomaly_min_delta">· ≥{{ r.anomaly_min_delta }}{{ metricUnit(r.metric) }}</template></span>
                  <span v-if="r.sustain_secs" class="text-[9px] text-sky-400/80 border border-sky-900 px-1">持续 {{ mins(r.sustain_secs) }}</span>
                  <span v-if="r.resolve_secs" class="text-[9px] text-emerald-400/80 border border-emerald-900 px-1">恢复 {{ mins(r.resolve_secs) }}</span>
                  <span v-if="r.escalate_secs" class="text-[9px] text-red-400/80 border border-red-900 px-1">升级 {{ mins(r.escalate_secs) }}↑crit</span>
                  <span v-if="r.repeat_secs" class="text-[9px] text-amber-400/80 border border-amber-900 px-1">重提醒 {{ mins(r.repeat_secs) }}</span>
                </div>
              </td>
              <td class="px-3 py-2"><span class="text-[10px] px-1 border uppercase" :class="sevClass(r.severity)">{{ r.severity }}</span></td>
              <td class="px-3 py-2 text-center">
                <button @click="toggleRule(r, 'notify_tg')" class="text-[10px] px-1.5 py-0.5 border" :class="r.notify_tg ? 'border-emerald-700 text-emerald-400' : 'border-gray-700 text-gray-600'">{{ r.notify_tg ? 'ON' : 'off' }}</button>
              </td>
              <td class="px-3 py-2 text-center">
                <button @click="toggleRule(r, 'enabled')" class="text-[10px] px-1.5 py-0.5 border" :class="r.enabled ? 'border-emerald-700 text-emerald-400' : 'border-gray-700 text-gray-600'">{{ r.enabled ? 'ON' : 'off' }}</button>
              </td>
              <td class="px-3 py-2 text-right whitespace-nowrap">
                <button @click="openPreview(r)" class="px-2 py-0.5 border border-gray-700 text-gray-400 hover:border-blue-500 hover:text-blue-400 text-[10px] uppercase">试算</button>
                <button @click="openEditRule(r)" class="px-2 py-0.5 border border-gray-700 text-gray-400 hover:border-emerald-500 hover:text-emerald-400 text-[10px] uppercase">编辑</button>
                <button @click="muteRule(r, ruleMuted(r) ? 0 : 60)" class="px-2 py-0.5 border border-gray-700 text-[10px] uppercase" :class="ruleMuted(r) ? 'text-amber-400 border-amber-700 hover:border-emerald-600' : 'text-gray-400 hover:border-amber-600'">{{ ruleMuted(r) ? '取消静音' : '静音1h' }}</button>
                <button @click="askDeleteRule(r)" :disabled="r.builtin" class="px-2 py-0.5 border border-gray-700 text-gray-500 hover:border-red-600 hover:text-red-400 text-[10px] uppercase disabled:opacity-30" :title="r.builtin ? '内置规则不可删,只能禁用' : ''">删</button>
              </td>
            </tr>
            <tr v-if="rulesOf(g.id).length === 0"><td colspan="6" class="px-3 py-3 text-[11px] text-gray-600 normal-case">（此组暂无规则）</td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- rule form modal -->
    <div v-if="ruleFormOpen" class="fixed inset-0 z-[100] flex items-center justify-center p-4 bg-black/75 backdrop-blur-sm" @click.self="ruleFormOpen = false">
      <div class="border-2 border-emerald-700/60 bg-gray-900 w-full max-w-lg font-mono">
        <div class="px-4 py-2 border-b border-emerald-800 bg-emerald-950/30 text-emerald-400 text-xs tracking-widest uppercase">{{ ruleEditingId ? '编辑规则' : '新增规则' }}</div>
        <div class="p-4 space-y-3">
          <div v-if="!ruleEditingId" class="grid grid-cols-2 gap-3">
            <div><label class="block text-[10px] tracking-widest text-gray-500 mb-1">规则 ID</label><input v-model="ruleForm.id" placeholder="my-rule" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none" /></div>
            <div><label class="block text-[10px] tracking-widest text-gray-500 mb-1">组</label>
              <select v-model="ruleForm.group_id" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none"><option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }}</option></select>
            </div>
          </div>
          <div><label class="block text-[10px] tracking-widest text-gray-500 mb-1">名称</label><input v-model="ruleForm.name" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none" /></div>
          <div class="grid grid-cols-3 gap-3">
            <div><label class="block text-[10px] tracking-widest text-gray-500 mb-1">指标</label>
              <select v-model="ruleForm.metric" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none"><option v-for="m in metrics" :key="m.key" :value="m.key">{{ m.label }}</option></select>
            </div>
            <div><label class="block text-[10px] tracking-widest text-gray-500 mb-1">比较</label>
              <select v-model="ruleForm.op" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none"><option v-for="o in ops" :key="o.v" :value="o.v">{{ o.sym }}</option></select>
            </div>
            <div><label class="block text-[10px] tracking-widest text-gray-500 mb-1">{{ ruleForm.anomaly ? '阈值 (异常模式忽略)' : '阈值' }}</label><input v-model.number="ruleForm.threshold" type="number" step="any" :disabled="ruleForm.anomaly" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none disabled:opacity-40" /></div>
          </div>
          <!-- Anomaly detection: fire on deviation from the metric's learned baseline, no fixed threshold -->
          <div class="border border-fuchsia-900/50 p-2">
            <label class="flex items-center gap-2 text-[11px] text-fuchsia-300"><input type="checkbox" v-model="ruleForm.anomaly" /> 异常检测(偏离自身基线,而非固定阈值)</label>
            <div v-if="ruleForm.anomaly" class="grid grid-cols-3 gap-3 mt-2">
              <div><label class="block text-[10px] tracking-widest text-gray-500 mb-1">灵敏度 (σ)</label><input v-model.number="ruleForm.anomaly_sigma" type="number" step="0.5" min="1" max="10" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-fuchsia-700 focus:outline-none" /></div>
              <div><label class="block text-[10px] tracking-widest text-gray-500 mb-1">窗口 (ticks)</label><input v-model.number="ruleForm.anomaly_window" type="number" step="10" min="4" max="5000" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-fuchsia-700 focus:outline-none" /></div>
              <div><label class="block text-[10px] tracking-widest text-gray-500 mb-1">最小偏移 ({{ metricUnit(ruleForm.metric) || '单位' }})</label><input v-model.number="ruleForm.anomaly_min_delta" type="number" step="any" min="0" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-fuchsia-700 focus:outline-none" /></div>
            </div>
            <div v-if="ruleForm.anomaly" class="text-[10px] text-gray-500 mt-1">方向取自上面的「比较」:&gt; 高侧 · &lt; 低侧 · ≠ 双侧。窗口 120 ticks ≈ 1 小时;重启后需预热 ~窗口/4 才开始判定。</div>
          </div>
          <div class="grid grid-cols-2 gap-3">
            <div><label class="block text-[10px] tracking-widest text-gray-500 mb-1">级别</label>
              <select v-model="ruleForm.severity" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none"><option value="info">info</option><option value="warn">warn</option><option value="crit">crit</option></select>
            </div>
            <div class="flex items-end gap-3 pb-1.5">
              <label class="flex items-center gap-1 text-[11px] text-gray-300"><input type="checkbox" v-model="ruleForm.notify_tg" /> TG 推送</label>
              <label v-if="!ruleEditingId" class="flex items-center gap-1 text-[11px] text-gray-300"><input type="checkbox" v-model="ruleForm.enabled" /> 启用</label>
            </div>
          </div>
          <!-- durations, all in MINUTES (0 = off / instant) -->
          <div class="border border-gray-800 bg-black/40 p-3 space-y-3">
            <div class="text-[10px] tracking-widest text-gray-500 uppercase">触发 / 恢复 / 升级 (分钟, 0=关)</div>
            <div>
              <label class="block text-[10px] text-gray-500 mb-1">持续多久才报警 (sustain)</label>
              <div class="flex items-center gap-2">
                <input v-model.number="ruleForm.sustain_min" type="number" min="0" max="60" class="w-20 bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none" />
                <span class="text-[10px] text-gray-600">分钟</span>
                <button v-for="p in sustainPresets" :key="p" type="button" @click="ruleForm.sustain_min = p"
                  class="text-[10px] px-1.5 py-0.5 border" :class="ruleForm.sustain_min === p ? 'border-emerald-600 text-emerald-400' : 'border-gray-700 text-gray-500 hover:border-gray-500'">{{ p === 0 ? '即时' : p + 'm' }}</button>
              </div>
            </div>
            <div class="grid grid-cols-3 gap-3">
              <div><label class="block text-[10px] text-gray-500 mb-1">恢复需持续</label><input v-model.number="ruleForm.resolve_min" type="number" min="0" max="60" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none" /></div>
              <div><label class="block text-[10px] text-gray-500 mb-1">超时升级crit</label><input v-model.number="ruleForm.escalate_min" type="number" min="0" max="1440" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none" /></div>
              <div><label class="block text-[10px] text-gray-500 mb-1">重复提醒间隔</label><input v-model.number="ruleForm.repeat_min" type="number" min="0" max="1440" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none" /></div>
            </div>
          </div>
          <div v-if="err" class="text-xs text-red-400 normal-case">⨯ {{ err }}</div>
        </div>
        <div class="px-4 py-2 border-t border-gray-800 flex justify-end gap-2">
          <button @click="ruleFormOpen = false" class="px-3 py-1.5 border border-gray-700 text-gray-400 text-[10px] tracking-widest uppercase">取消</button>
          <button @click="submitRule" :disabled="ruleBusy" class="px-4 py-1.5 border border-emerald-600 text-emerald-400 hover:bg-emerald-600 hover:text-black text-[10px] tracking-widest uppercase disabled:opacity-30">{{ ruleBusy ? '…' : '保存' }}</button>
        </div>
      </div>
    </div>

    <!-- group form modal -->
    <div v-if="groupFormOpen" class="fixed inset-0 z-[100] flex items-center justify-center p-4 bg-black/75 backdrop-blur-sm" @click.self="groupFormOpen = false">
      <div class="border-2 border-emerald-700/60 bg-gray-900 w-full max-w-md font-mono">
        <div class="px-4 py-2 border-b border-emerald-800 bg-emerald-950/30 text-emerald-400 text-xs tracking-widest uppercase">{{ groupEditingId ? '编辑规则组' : '新增规则组' }}</div>
        <div class="p-4 space-y-3">
          <div class="grid grid-cols-2 gap-3">
            <div v-if="!groupEditingId"><label class="block text-[10px] tracking-widest text-gray-500 mb-1">组 ID</label><input v-model="groupForm.id" placeholder="edge-pops" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none" /></div>
            <div :class="groupEditingId ? 'col-span-2' : ''"><label class="block text-[10px] tracking-widest text-gray-500 mb-1">名称</label><input v-model="groupForm.name" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none" /></div>
          </div>
          <div><label class="block text-[10px] tracking-widest text-gray-500 mb-1">描述 / 备注</label><textarea v-model="groupForm.description" rows="2" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none normal-case"></textarea></div>
          <div>
            <label class="block text-[10px] tracking-widest text-gray-500 mb-1">绑定节点（不选=全部）</label>
            <div class="max-h-32 overflow-y-auto border border-gray-800 bg-black p-2 grid grid-cols-2 gap-1">
              <label v-for="n in nodes" :key="n.id" class="flex items-center gap-1 text-[11px] text-gray-300"><input type="checkbox" :value="n.id" v-model="groupForm.node_ids" /> {{ n.id }}</label>
            </div>
          </div>
          <div><label class="block text-[10px] tracking-widest text-gray-500 mb-1">或按区域码 (逗号分隔, 如 53,55)</label><input v-model="groupForm.regions_csv" placeholder="53,55" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none" /></div>
          <!-- group-level TG policy + default sustain -->
          <div class="border border-gray-800 bg-black/40 p-3 grid grid-cols-3 gap-3 items-end">
            <div class="col-span-3 text-[10px] tracking-widest text-gray-500 uppercase">组级 Telegram 策略 + 默认持续</div>
            <label class="flex items-center gap-1 text-[11px] text-gray-300 pb-1.5"><input type="checkbox" v-model="groupForm.suppress_tg" /> 关闭本组 TG</label>
            <div><label class="block text-[10px] text-gray-500 mb-1">TG 严重度下限</label>
              <select v-model="groupForm.min_severity" :disabled="groupForm.suppress_tg" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none disabled:opacity-40">
                <option value="">默认 (仅 crit)</option><option value="warn">warn 起</option><option value="info">info 起</option><option value="crit">仅 crit</option>
              </select>
            </div>
            <div><label class="block text-[10px] text-gray-500 mb-1">默认持续(分钟)</label><input v-model.number="groupForm.default_sustain_min" type="number" min="0" max="60" class="w-full bg-black border border-gray-800 px-2 py-1.5 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none" /></div>
          </div>
          <div v-if="err" class="text-xs text-red-400 normal-case">⨯ {{ err }}</div>
        </div>
        <div class="px-4 py-2 border-t border-gray-800 flex justify-end gap-2">
          <button @click="groupFormOpen = false" class="px-3 py-1.5 border border-gray-700 text-gray-400 text-[10px] tracking-widest uppercase">取消</button>
          <button @click="submitGroup" :disabled="groupBusy" class="px-4 py-1.5 border border-emerald-600 text-emerald-400 hover:bg-emerald-600 hover:text-black text-[10px] tracking-widest uppercase disabled:opacity-30">{{ groupBusy ? '…' : (groupEditingId ? '保存' : '创建') }}</button>
        </div>
      </div>
    </div>

    <!-- preview modal -->
    <div v-if="previewOpen" class="fixed inset-0 z-[100] flex items-center justify-center p-4 bg-black/75 backdrop-blur-sm" @click.self="previewOpen = false">
      <div class="border-2 border-blue-700/60 bg-gray-900 w-full max-w-md font-mono">
        <div class="px-4 py-2 border-b border-blue-800 bg-blue-950/30 text-blue-400 text-xs tracking-widest uppercase">试算 · {{ previewRule?.name }}</div>
        <div class="p-4 space-y-2">
          <p class="text-[11px] text-gray-500 normal-case">按当前 fleet 快照,这条规则现在会让 <b class="text-blue-300">{{ previewFiring }}</b> 个节点告警:</p>
          <div v-if="previewBusy" class="text-xs text-gray-500">◌ 计算中…</div>
          <div v-else class="max-h-72 overflow-y-auto space-y-1">
            <div v-for="r in previewResults" :key="r.node_id" class="flex items-center justify-between text-xs border-b border-gray-800/50 py-1">
              <span class="font-mono" :class="r.firing ? 'text-red-400' : 'text-gray-400'">{{ r.firing ? '🔴' : '·' }} {{ r.node_id }}</span>
              <span class="font-mono text-gray-500">{{ r.ok ? r.value.toFixed(2) : 'n/a' }}</span>
            </div>
          </div>
        </div>
        <div class="px-4 py-2 border-t border-gray-800 flex justify-end">
          <button @click="previewOpen = false" class="px-3 py-1.5 border border-gray-700 text-gray-400 text-[10px] tracking-widest uppercase">关闭</button>
        </div>
      </div>
    </div>

    <ConfirmDialog :open="confirmOpen" :title="confirmMeta.title" :description="confirmMeta.description"
      severity="high" :expectedConfirmation="confirmMeta.expected" :busy="confirmBusy" :errorMsg="confirmError"
      @cancel="confirmOpen = false" @confirm="onConfirmDelete" />
  </div>
</template>
