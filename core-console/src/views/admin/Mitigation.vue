<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { api, type FlowspecRule } from '@/api/client'

const rules = ref<FlowspecRule[]>([])
const msg = ref('')
async function load() { const r = await api.ddosList(); if (r.ok) rules.value = r.data?.rules ?? [] }
onMounted(load)

// create draft
const form = ref<Partial<FlowspecRule>>({ action: 'drop', ttl_secs: 3600, proto: '' })
const preview = ref('')
const draftId = ref('')
const busy = ref(false)
async function createDraft() {
  busy.value = true; msg.value = ''
  try {
    const r = await api.ddosCreate(form.value)
    if (r.ok && r.data) { preview.value = r.data.nft; draftId.value = r.data.rule.id; await load(); msg.value = '草稿已生成,确认后下发' }
    else msg.value = r.error || '生成失败'
  } catch (e: unknown) { msg.value = e instanceof Error ? e.message : String(e) }
  finally { busy.value = false }
}

// apply
const applyNodes = ref<Record<string, string>>({})
const applyConfirm = ref<Record<string, string>>({})
const applyLog = ref<Record<string, string>>({})
async function applyRule(rule: FlowspecRule) {
  const nodes = (applyNodes.value[rule.id] || '').split(',').map(s => s.trim()).filter(Boolean)
  busy.value = true
  try {
    const r = await api.ddosApply(rule.id, applyConfirm.value[rule.id] || '', nodes)
    if (r.ok && r.data) {
      const ok = r.data.applied.join(', '); const bad = Object.keys(r.data.failed || {}).join(', ')
      applyLog.value[rule.id] = `应用: ${ok || '无'}${bad ? ' · 失败: ' + bad : ''}`
      applyConfirm.value[rule.id] = ''; await load()
    } else applyLog.value[rule.id] = r.error || '失败'
  } catch (e: unknown) { applyLog.value[rule.id] = e instanceof Error ? e.message : String(e) }
  finally { busy.value = false }
}
async function revoke(rule: FlowspecRule) {
  busy.value = true
  try { await api.ddosRevoke(rule.id); await load() } finally { busy.value = false }
}

function ttlLeft(rule: FlowspecRule): string {
  if (rule.status !== 'active' || !rule.expires_at) return ''
  const s = rule.expires_at - Math.floor(Date.now() / 1000)
  if (s <= 0) return '到期'
  const m = Math.floor(s / 60)
  return m >= 60 ? `${Math.floor(m / 60)}h${m % 60}m` : `${m}m`
}
function summary(r: FlowspecRule): string {
  const p: string[] = []
  if (r.src) p.push('src ' + r.src)
  if (r.dst) p.push('dst ' + r.dst)
  if (r.proto) p.push(r.proto)
  if (r.dst_port) p.push('dport ' + r.dst_port)
  p.push('→ ' + (r.action === 'rate' ? `≤${r.rate_pps}pps` : 'drop'))
  return p.join(' ')
}
const statusColor = (s: string) => s === 'active' ? 'text-red-400' : s === 'draft' ? 'text-amber-400' : 'text-gray-500'
const liveRules = computed(() => rules.value.filter(r => r.status === 'active' || r.status === 'draft'))
const pastRules = computed(() => rules.value.filter(r => r.status !== 'active' && r.status !== 'draft').slice(0, 20))
</script>

<template>
  <div class="space-y-4">
    <div class="border border-gray-800 bg-gray-900 p-4">
      <div class="flex items-center justify-between flex-wrap gap-2">
        <div class="flex items-center gap-3">
          <span :class="['w-1.5 h-1.5', liveRules.length ? 'bg-red-500 animate-pulse' : 'bg-emerald-500']"></span>
          <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">缓解 · Mitigation</h1>
          <span v-if="liveRules.length" class="text-[10px] tracking-widest text-red-500 uppercase">{{ liveRules.length }} active</span>
        </div>
        <div class="text-[10px] tracking-widest text-gray-600 uppercase">内部 nft · 人工确认 · TTL 过期 · {{ msg }}</div>
      </div>
    </div>

    <!-- create -->
    <div class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase">生成规则(5 元组)</div>
      <div class="p-3 space-y-2 text-[12px]">
        <div class="flex flex-wrap items-center gap-2">
          <input v-model="form.dst" placeholder="目的 IP/前缀" class="w-44 bg-black border border-gray-700 px-1.5 py-0.5 text-gray-200 font-mono" />
          <input v-model="form.src" placeholder="源 IP/前缀(可空)" class="w-44 bg-black border border-gray-700 px-1.5 py-0.5 text-gray-200 font-mono" />
          <select v-model="form.proto" class="bg-black border border-gray-700 px-1 py-0.5 text-gray-300">
            <option value="">任意协议</option><option value="tcp">tcp</option><option value="udp">udp</option>
            <option value="icmp">icmp</option><option value="icmpv6">icmpv6</option>
          </select>
          <label class="text-gray-500">dport <input v-model.number="form.dst_port" type="number" class="w-20 bg-black border border-gray-700 px-1 py-0.5 text-gray-200 text-right" /></label>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <select v-model="form.action" class="bg-black border border-gray-700 px-1 py-0.5 text-gray-300">
            <option value="drop">drop</option><option value="rate">rate-limit</option>
          </select>
          <label v-if="form.action === 'rate'" class="text-gray-500">速率 <input v-model.number="form.rate_pps" type="number" class="w-24 bg-black border border-gray-700 px-1 py-0.5 text-gray-200 text-right" /> pps</label>
          <label class="text-gray-500">TTL <input v-model.number="form.ttl_secs" type="number" class="w-24 bg-black border border-gray-700 px-1 py-0.5 text-gray-200 text-right" /> 秒</label>
          <button @click="createDraft" :disabled="busy" class="border border-gray-700 px-3 py-0.5 text-gray-300 hover:border-emerald-600 hover:text-emerald-400 disabled:opacity-40">生成草稿</button>
        </div>
        <pre v-if="preview" class="text-[10px] text-emerald-300 bg-black border border-gray-800 p-2 overflow-auto">nft: {{ preview }}</pre>
      </div>
    </div>

    <!-- live rules -->
    <div class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase">活动 / 草稿规则</div>
      <div class="p-3 space-y-3">
        <div v-for="r in liveRules" :key="r.id" class="border-b border-gray-800/40 pb-3 last:border-0 text-[12px]">
          <div class="flex items-center gap-2 flex-wrap">
            <span class="font-mono text-gray-500">{{ r.id }}</span>
            <span :class="statusColor(r.status)">{{ r.status }}</span>
            <span class="text-gray-300">{{ summary(r) }}</span>
            <span v-if="ttlLeft(r)" class="text-gray-600">剩 {{ ttlLeft(r) }}</span>
            <span v-if="r.applied_pops?.length" class="text-gray-600">@ {{ r.applied_pops.join(',') }}</span>
            <span class="flex-1"></span>
            <button @click="revoke(r)" :disabled="busy" class="text-gray-400 hover:text-red-300 border border-gray-700 px-2 py-0.5">撤销</button>
          </div>
          <div v-if="r.status === 'draft'" class="flex items-center gap-2 mt-1.5 flex-wrap">
            <input v-model="applyNodes[r.id]" placeholder="目标 PoP(逗号分隔, 如 ctrl-01,pop-04)" class="w-72 bg-black border border-gray-700 px-1.5 py-0.5 text-gray-200 font-mono" />
            <input v-model="applyConfirm[r.id]" :placeholder="'APPLY DDOS ' + r.id" class="w-56 bg-black border border-gray-700 px-1.5 py-0.5 text-gray-200 font-mono" />
            <button @click="applyRule(r)" :disabled="busy" class="border border-red-800 px-3 py-0.5 text-red-400 hover:bg-red-900/30 disabled:opacity-40">下发</button>
          </div>
          <div v-if="applyLog[r.id]" class="text-[11px] text-gray-500 mt-1">{{ applyLog[r.id] }}</div>
        </div>
        <div v-if="!liveRules.length" class="text-[11px] text-gray-600 italic">无活动规则。异常流量会在此提议(仅文本,不自动下发)。</div>
      </div>
    </div>

    <!-- history -->
    <div v-if="pastRules.length" class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase">历史(已过期/撤销)</div>
      <div class="p-3 space-y-1">
        <div v-for="r in pastRules" :key="r.id" class="text-[11px] text-gray-600 flex items-center gap-2">
          <span class="font-mono">{{ r.id }}</span><span :class="statusColor(r.status)">{{ r.status }}</span><span>{{ summary(r) }}</span>
        </div>
      </div>
    </div>
  </div>
</template>
