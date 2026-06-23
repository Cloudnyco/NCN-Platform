<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { api, type OncallConfig, type EscalationTier } from '@/api/client'

const cfg = ref<OncallConfig>({ rotation: [], start_date: '', period_days: 7, tiers: [] })
const current = ref('')
const operators = ref<string[]>([])
const busy = ref(false)
const msg = ref('')

async function load() {
  const r = await api.oncall()
  if (r.ok && r.data) {
    cfg.value = r.data.config?.rotation ? r.data.config : { rotation: [], start_date: '', period_days: 7, tiers: [] }
    if (!cfg.value.period_days) cfg.value.period_days = 7
    if (!cfg.value.tiers) cfg.value.tiers = []
    if (!cfg.value.rotation) cfg.value.rotation = []
    current.value = r.data.current || ''
    operators.value = r.data.operators || []
  }
}
onMounted(load)

const available = computed(() => operators.value.filter(o => !cfg.value.rotation.includes(o)))
const addPick = ref('')
function addToRotation() { if (addPick.value && !cfg.value.rotation.includes(addPick.value)) { cfg.value.rotation.push(addPick.value); addPick.value = '' } }
function removeFromRotation(i: number) { cfg.value.rotation.splice(i, 1) }
function moveUp(i: number) { if (i > 0) { const a = cfg.value.rotation; [a[i - 1], a[i]] = [a[i], a[i - 1]] } }

function addTier() { cfg.value.tiers.push({ after_min: 10, target: 'oncall' }) }
function removeTier(i: number) { cfg.value.tiers.splice(i, 1) }

async function save() {
  busy.value = true; msg.value = ''
  try {
    const r = await api.setOncall(cfg.value)
    if (r.ok) { current.value = r.data?.current || ''; msg.value = '已保存' }
    else msg.value = r.error || '保存失败'
  } catch (e: unknown) { msg.value = e instanceof Error ? e.message : String(e) }
  finally { busy.value = false }
}
</script>

<template>
  <div class="space-y-4">
    <div class="border border-gray-800 bg-gray-900 p-4">
      <div class="flex items-center justify-between flex-wrap gap-2">
        <div class="flex items-center gap-3">
          <span class="w-1.5 h-1.5 bg-emerald-500"></span>
          <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">值班 · On-Call</h1>
          <span class="text-[10px] tracking-widest text-gray-500 uppercase">当前 <span class="text-emerald-400 font-mono normal-case">{{ current || '—' }}</span></span>
        </div>
        <div class="text-[10px] tracking-widest text-gray-600 uppercase">{{ msg }}</div>
      </div>
    </div>

    <!-- Rotation -->
    <div class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase">值班轮转</div>
      <div class="p-3 space-y-2">
        <div v-for="(op, i) in cfg.rotation" :key="op" class="flex items-center gap-2 text-[12px]">
          <span class="w-5 text-gray-600">{{ i + 1 }}</span>
          <span class="font-mono text-gray-200 flex-1">{{ op }}</span>
          <span v-if="op === current" class="text-[10px] text-emerald-400">值班中</span>
          <button @click="moveUp(i)" :disabled="i === 0" class="text-gray-500 hover:text-gray-300 disabled:opacity-30 px-1">▲</button>
          <button @click="removeFromRotation(i)" class="text-red-400 hover:text-red-300 px-1">✕</button>
        </div>
        <div v-if="!cfg.rotation.length" class="text-[11px] text-gray-600 italic">未添加值班人。</div>
        <div class="flex items-center gap-2 pt-1 text-[12px]">
          <select v-model="addPick" class="bg-black border border-gray-700 px-2 py-0.5 text-gray-300">
            <option value="">选择运维…</option>
            <option v-for="o in available" :key="o" :value="o">{{ o }}</option>
          </select>
          <button @click="addToRotation" :disabled="!addPick" class="border border-gray-700 px-2 py-0.5 text-gray-400 hover:border-emerald-600 hover:text-emerald-400 disabled:opacity-40">+ 加入轮转</button>
        </div>
        <div class="flex items-center gap-3 pt-2 text-[12px] text-gray-400">
          <label class="flex items-center gap-1">起始日期
            <input type="date" v-model="cfg.start_date" class="bg-black border border-gray-700 px-1.5 py-0.5 text-gray-200" />
          </label>
          <label class="flex items-center gap-1">每班
            <input type="number" min="1" v-model.number="cfg.period_days" class="w-16 bg-black border border-gray-700 px-1.5 py-0.5 text-gray-200 text-right" /> 天
          </label>
        </div>
      </div>
    </div>

    <!-- Escalation policy -->
    <div class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase">升级策略 · 告警未确认到点逐级呼叫</div>
      <div class="p-3 space-y-2">
        <div v-for="(t, i) in cfg.tiers" :key="i" class="flex items-center gap-2 text-[12px]">
          <span class="text-gray-500">触发后</span>
          <input type="number" min="1" v-model.number="t.after_min" class="w-16 bg-black border border-gray-700 px-1.5 py-0.5 text-gray-200 text-right" />
          <span class="text-gray-500">分钟未确认 → 呼叫</span>
          <select v-model="t.target" class="bg-black border border-gray-700 px-2 py-0.5 text-gray-300">
            <option value="oncall">当前值班(私聊)</option>
            <option value="admins">全体管理员(私聊)</option>
            <option value="group">运维群</option>
          </select>
          <button @click="removeTier(i)" class="text-red-400 hover:text-red-300 px-1">✕</button>
        </div>
        <div v-if="!cfg.tiers.length" class="text-[11px] text-gray-600 italic">未配置升级层级 —— 告警不会主动呼叫。</div>
        <button @click="addTier" class="border border-gray-700 px-2 py-0.5 text-[12px] text-gray-400 hover:border-emerald-600 hover:text-emerald-400">+ 添加层级</button>
      </div>
    </div>

    <div class="flex items-center gap-2">
      <button @click="save" :disabled="busy" class="border border-emerald-700 px-4 py-1 text-[12px] text-emerald-400 hover:bg-emerald-900/30 disabled:opacity-40">{{ busy ? '保存中…' : '保存' }}</button>
      <span class="text-[11px] text-gray-600">「接手」= 在告警页 ack 该告警即停止升级。机器人 /oncall 查询当前值班。</span>
    </div>
  </div>
</template>
