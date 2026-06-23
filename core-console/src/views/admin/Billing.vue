<script setup lang="ts">
// Billing — internal VPS rent tracker.
//
// Tracks our OWN outgoing VPS subscriptions so the team can:
//   * see total monthly spend across all providers / PoPs
//   * not forget to renew before a provider auto-suspends a PoP
//
// NOT a customer-facing billing system. No payment processing, no
// Stripe, no invoice PDFs. Just bookkeeping + a daily Telegram digest
// when something is due within 7 days (see backend/billing.go
// startBillingRenewalNotifier).

import { computed, onMounted, ref } from 'vue'
import { useSessionStore } from '@/stores/session'
import {
  api,
  type VPSSubscription, type BillingCreateReq, type BillingPatchReq,
  type BillingCycle, type FXRatesResponse, type NodeView,
} from '@/api/client'

const session = useSessionStore()
const isAdmin = computed(() => session.role === 'admin')

const subs = ref<VPSSubscription[]>([])
const loading = ref(false)
const err = ref<string | null>(null)

// FX rates from /api/v1/auth/fx/rates. Map is "1 X = N CNY", so we
// multiply a per-currency monthly total by rates[ccy] to get CNY.
// Fetched once on mount (the rates update server-side every 12h —
// no point pulling more often than the page reload cadence).
const fx = ref<FXRatesResponse | null>(null)

// Node registry rows — drive the "node_id" picker LIVE so a newly-added
// server is immediately linkable. Was a hardcoded 5-node list, which is why
// new servers (pop-01 / pop-08 / pop-05 and anything added via the Servers
// page) never showed up here.
const nodeRows = ref<NodeView[]>([])
const pops = computed(() => nodeRows.value.map(n => n.id))

const CYCLES: { v: BillingCycle; label: string }[] = [
  { v: 'monthly',   label: '每月 · monthly' },
  { v: 'quarterly', label: '每季 · quarterly' },
  { v: 'yearly',    label: '每年 · yearly' },
]

// Currencies we're likely to see based on where the PoPs live.
const CURRENCIES = ['USD', 'HKD', 'SGD', 'JPY', 'CNY', 'EUR', 'GBP'] as const

async function refresh() {
  if (!isAdmin.value) return
  loading.value = true
  err.value = null
  try {
    const [subsEnv, fxEnv, nodesEnv] = await Promise.all([
      api.billingList(),
      api.fxRates(),
      api.nodesList(),
    ])
    if (!subsEnv.ok) throw new Error(subsEnv.error || 'list failed')
    subs.value = subsEnv.data ?? []
    if (fxEnv.ok) fx.value = fxEnv.data ?? null
    // FX failure is non-fatal — the CNY rollup just won't render.
    // Node list drives the PoP picker + the "unbilled servers" hint; non-fatal
    // if it fails (existing links still show via the fallback option).
    if (nodesEnv.ok && nodesEnv.data) nodeRows.value = nodesEnv.data
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

// ────────── New / edit form ──────────
const formOpen = ref(false)
const editingId = ref<string | null>(null)
const form = ref<BillingCreateReq>({
  label: '', provider: '', node_id: '',
  monthly_cost: 0, currency: 'USD',
  billing_cycle: 'monthly',
  next_due: nextMonthIso(),
  portal_url: '', notes: '',
})
const formBusy = ref(false)

function nextMonthIso(): string {
  // Default: 1 month from today, time 00:00 UTC, formatted YYYY-MM-DD.
  const d = new Date()
  d.setMonth(d.getMonth() + 1)
  return d.toISOString().slice(0, 10)
}

function openNew() {
  editingId.value = null
  form.value = {
    label: '', provider: '', node_id: '',
    monthly_cost: 0, currency: 'USD',
    billing_cycle: 'monthly',
    next_due: nextMonthIso(),
    portal_url: '', notes: '',
  }
  formOpen.value = true
}

function openEdit(sub: VPSSubscription) {
  editingId.value = sub.id
  form.value = {
    label: sub.label,
    provider: sub.provider,
    node_id: sub.node_id ?? '',
    monthly_cost: sub.monthly_cost,
    currency: sub.currency,
    billing_cycle: sub.billing_cycle,
    next_due: sub.next_due.slice(0, 10),
    portal_url: sub.portal_url ?? '',
    notes: sub.notes ?? '',
  }
  formOpen.value = true
}

async function submitForm() {
  if (formBusy.value) return
  if (!form.value.label.trim() || !form.value.provider.trim()) {
    err.value = '名称和服务商必填'
    return
  }
  if (form.value.monthly_cost <= 0) {
    err.value = '月费必须大于 0'
    return
  }
  formBusy.value = true
  err.value = null
  try {
    // Server expects RFC3339; <input type="date"> gives YYYY-MM-DD.
    // Append T00:00:00Z so the backend's time.Time parser is happy.
    const payload = { ...form.value, next_due: form.value.next_due + 'T00:00:00Z' }
    let envOk = false
    if (editingId.value) {
      const env = await api.billingPatch(editingId.value, payload as BillingPatchReq)
      envOk = env.ok
      if (!env.ok) throw new Error(env.error || 'patch failed')
    } else {
      const env = await api.billingCreate(payload)
      envOk = env.ok
      if (!env.ok) throw new Error(env.error || 'create failed')
    }
    if (envOk) {
      formOpen.value = false
      editingId.value = null
      await refresh()
    }
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  } finally {
    formBusy.value = false
  }
}

async function markPaid(sub: VPSSubscription) {
  if (!confirm(`确认 "${sub.label}" 已付款 ${sub.monthly_cost} ${sub.currency}?\n下次到期日会自动推到下一个 ${sub.billing_cycle} 周期。`)) return
  try {
    const env = await api.billingMarkPaid(sub.id, {})
    if (!env.ok) throw new Error(env.error || 'mark-paid failed')
    await refresh()
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  }
}

async function deleteSubscription(sub: VPSSubscription) {
  if (!confirm(`删除 "${sub.label}"? 付款历史也会一起消失。`)) return
  try {
    const env = await api.billingDelete(sub.id)
    if (!env.ok) throw new Error(env.error || 'delete failed')
    await refresh()
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  }
}

// ────────── derived state ──────────

// Monthly-equivalent helper: yearly/12, quarterly/3, monthly as-is.
function monthlyEquiv(sub: VPSSubscription): number {
  switch (sub.billing_cycle) {
    case 'yearly':    return sub.monthly_cost / 12
    case 'quarterly': return sub.monthly_cost / 3
    default:          return sub.monthly_cost
  }
}

// Active, non-local servers that have NO subscription linked yet — surfaces
// exactly the "I added a server but billing doesn't know about it" gap.
const unbilledServers = computed(() => {
  const linked = new Set(subs.value.map(s => s.node_id).filter(Boolean))
  return nodeRows.value
    .filter(n => n.status === 'active' && !n.local && !linked.has(n.id))
    .map(n => n.id)
})

// Pre-fill the new-subscription form already linked to a given node.
function newForNode(id: string) {
  openNew()
  form.value.node_id = id
}

// Per-currency monthly totals — UI rollup card at the top of the page.
const totalsByCurrency = computed(() => {
  const m: Record<string, number> = {}
  for (const sub of subs.value) {
    m[sub.currency] = (m[sub.currency] ?? 0) + monthlyEquiv(sub)
  }
  return Object.entries(m)
    .map(([ccy, total]) => ({ ccy, total }))
    .sort((a, b) => b.total - a.total)
})

// CNY-equivalent grand total: sum each per-currency monthly figure
// × fx.rates[ccy]. Only meaningful if every currency present in the
// totals has a rate; otherwise the figure would be misleading
// (partial roll-up that looks like a full one).
const cnyRollup = computed<{ total: number; complete: boolean; fetchedAt: string | null }>(() => {
  if (!fx.value || !fx.value.rates) return { total: 0, complete: false, fetchedAt: null }
  let total = 0
  let complete = true
  for (const { ccy, total: amt } of totalsByCurrency.value) {
    const rate = fx.value.rates[ccy]
    if (rate && rate > 0) total += amt * rate
    else complete = false
  }
  return { total, complete, fetchedAt: fx.value.fetched_at }
})

function fxAge(iso: string | null): string {
  if (!iso) return '—'
  const ms = Date.now() - new Date(iso).getTime()
  const m = Math.floor(ms / 60_000)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
}

// Days until next renewal — negative if overdue.
function daysUntil(iso: string): number {
  const due = new Date(iso).getTime()
  return Math.floor((due - Date.now()) / (24 * 3600 * 1000))
}

// Visual urgency band for the countdown column.
function urgencyClass(iso: string): string {
  const d = daysUntil(iso)
  if (d < 0)  return 'text-red-400 font-semibold'      // overdue
  if (d <= 1) return 'text-red-400 font-semibold'      // critical
  if (d <= 7) return 'text-amber-400'                  // warn
  if (d <= 30) return 'text-yellow-300'                // upcoming
  return 'text-gray-300'                                // far away
}

function daysLabel(iso: string): string {
  const d = daysUntil(iso)
  if (d < 0)  return `逾期 ${-d} 天`
  if (d === 0) return '今天到期'
  if (d === 1) return '明天到期'
  return `还剩 ${d} 天`
}

function fmtDate(iso: string): string {
  return new Date(iso).toLocaleDateString('en-GB', { year: 'numeric', month: 'short', day: 'numeric' })
}

function fmtCost(n: number): string {
  return n.toFixed(2)
}

onMounted(refresh)
</script>

<template>
  <div class="space-y-4">
    <!-- Header + summary -->
    <div class="border border-gray-800 bg-gray-900 p-4">
      <div class="flex items-center justify-between flex-wrap gap-2">
        <div class="flex items-center gap-3">
          <span class="w-1.5 h-1.5 bg-emerald-500"></span>
          <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">月费 · VPS Billing</h1>
          <span class="text-[10px] tracking-widest text-gray-600 uppercase">
            {{ subs.length }} 订阅
          </span>
        </div>
        <div class="flex items-center gap-2">
          <button @click="refresh" :disabled="loading"
                  class="text-[10px] tracking-widest text-gray-500 hover:text-emerald-400 disabled:opacity-30 normal-case tracking-normal">
            ↻ refresh
          </button>
          <button @click="openNew"
                  class="px-2 py-1 border border-emerald-700 text-emerald-400 hover:bg-emerald-900/30 text-[10px] tracking-widest uppercase">
            + 新增订阅
          </button>
        </div>
      </div>

      <!-- Per-currency monthly rollup. Yearly entries get amortized to per-month. -->
      <div v-if="totalsByCurrency.length" class="mt-3 grid grid-cols-2 sm:grid-cols-4 gap-3">
        <div v-for="t in totalsByCurrency" :key="t.ccy"
             class="border border-gray-800 bg-gray-950/60 px-3 py-2">
          <div class="text-[10px] tracking-widest text-gray-600 uppercase">{{ t.ccy }} · 月折算</div>
          <div class="mt-1 text-lg font-mono text-gray-100">{{ fmtCost(t.total) }}</div>
        </div>
      </div>

      <!-- CNY-equivalent grand total — pulled from the FX rate cache.
           Only rendered when EVERY currency present has a rate, so the
           "grand total" doesn't lie when one currency is missing from
           the FX feed. fetched-age label so the operator can tell how
           stale the conversion is (FX provider refreshes daily). -->
      <div v-if="totalsByCurrency.length && cnyRollup.complete" class="mt-3 border border-emerald-700/50 bg-emerald-950/20 px-4 py-3 flex items-center justify-between flex-wrap gap-2">
        <div>
          <div class="text-[10px] tracking-widest text-emerald-400 uppercase">折算人民币 · monthly equivalent</div>
          <div class="mt-1 text-2xl font-mono text-emerald-300">¥{{ fmtCost(cnyRollup.total) }}</div>
        </div>
        <div class="text-[10px] tracking-widest text-gray-600 uppercase text-right">
          <div>汇率 · FX</div>
          <div class="mt-0.5 normal-case tracking-normal text-gray-500">updated {{ fxAge(cnyRollup.fetchedAt) }}</div>
        </div>
      </div>
      <div v-else-if="totalsByCurrency.length && !cnyRollup.complete && fx" class="mt-3 border border-amber-700/40 bg-amber-950/15 px-3 py-2 text-[10px] tracking-widest text-amber-400 uppercase">
        FX 数据不完整 · 部分币种缺率,CNY 折算暂不显示
      </div>
    </div>

    <!-- Error -->
    <div v-if="err" class="border border-red-500/40 bg-red-950/20 p-3 text-xs text-red-400 normal-case tracking-normal">
      ⨯ {{ err }}
    </div>

    <!-- Unbilled servers — active PoPs with no subscription linked yet -->
    <div v-if="unbilledServers.length" class="border border-amber-700/40 bg-amber-950/15 p-3">
      <div class="text-[10px] tracking-widest text-amber-400 uppercase mb-2">
        {{ unbilledServers.length }} 台在册服务器还没关联月费订阅
      </div>
      <div class="flex flex-wrap gap-2">
        <button v-for="id in unbilledServers" :key="id" @click="newForNode(id)"
                class="px-2 py-1 border border-amber-700/60 text-amber-300 hover:bg-amber-900/30 text-[10px] tracking-widest uppercase font-mono">
          + {{ id }}
        </button>
      </div>
    </div>

    <!-- Form -->
    <div v-if="formOpen" class="border border-emerald-700/60 bg-gray-900 p-4 space-y-3">
      <h2 class="text-[10px] tracking-widest uppercase text-emerald-400">
        {{ editingId ? '修改订阅' : '新增订阅' }}
      </h2>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">名称 ·  label</label>
          <input v-model="form.label" type="text" placeholder="e.g. Region D VPS at Cyberjet"
                 class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none" />
        </div>
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">服务商 · provider</label>
          <input v-model="form.provider" type="text" placeholder="Cyberjet / DataSphere / OVH ..."
                 class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none" />
        </div>
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">PoP 关联(可选)</label>
          <select v-model="form.node_id"
                  class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none">
            <option value="">— 无 / 未关联 —</option>
            <option v-for="p in pops" :key="p" :value="p">{{ p }}</option>
            <!-- Keep an existing link selectable even if that node was since removed. -->
            <option v-if="form.node_id && !pops.includes(form.node_id)" :value="form.node_id">{{ form.node_id }}（已移除）</option>
          </select>
        </div>
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">下次到期 · next due</label>
          <input v-model="form.next_due" type="date"
                 class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none" />
        </div>
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">金额(每个周期) · cost / cycle</label>
          <input v-model.number="form.monthly_cost" type="number" min="0" step="0.01"
                 class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none" />
        </div>
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">币种 · currency</label>
          <select v-model="form.currency"
                  class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none">
            <option v-for="c in CURRENCIES" :key="c" :value="c">{{ c }}</option>
          </select>
        </div>
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">周期 · cycle</label>
          <select v-model="form.billing_cycle"
                  class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none">
            <option v-for="c in CYCLES" :key="c.v" :value="c.v">{{ c.label }}</option>
          </select>
        </div>
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">控制台 URL(可选)</label>
          <input v-model="form.portal_url" type="url" placeholder="https://billing.provider.com/..."
                 class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none" />
        </div>
      </div>
      <div>
        <label class="block text-[10px] tracking-widest text-gray-500 mb-1">备注 · notes</label>
        <textarea v-model="form.notes" rows="2"
                  placeholder="e.g. 自动续费,信用卡尾号 4242"
                  class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-300 focus:border-emerald-700 focus:outline-none normal-case tracking-normal"></textarea>
      </div>
      <div class="flex gap-2">
        <button @click="submitForm" :disabled="formBusy"
                class="px-4 py-2 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-[10px] tracking-widest uppercase disabled:opacity-30">
          {{ formBusy ? '◌ 保存中…' : (editingId ? '保存修改' : '创建') }}
        </button>
        <button @click="formOpen = false"
                class="px-4 py-2 border border-gray-700 text-gray-400 hover:border-gray-500 text-[10px] tracking-widest uppercase">
          取消
        </button>
      </div>
    </div>

    <!-- List -->
    <div v-if="!loading && subs.length === 0" class="border border-gray-800 bg-gray-900 p-6 text-center text-sm text-gray-600 italic normal-case tracking-normal">
      还没有订阅 · 点 "新增订阅" 开始记录
    </div>

    <div v-else class="border border-gray-800 bg-gray-900 overflow-x-auto">
      <table class="w-full text-xs">
        <thead class="border-b border-gray-800 bg-gray-950/60">
          <tr class="text-[10px] tracking-widest text-gray-500 uppercase">
            <th class="text-left px-3 py-2">订阅</th>
            <th class="text-left px-3 py-2">PoP</th>
            <th class="text-right px-3 py-2">金额</th>
            <th class="text-left px-3 py-2">周期</th>
            <th class="text-left px-3 py-2">下次到期</th>
            <th class="text-left px-3 py-2">倒计时</th>
            <th class="text-right px-3 py-2">操作</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-800">
          <tr v-for="sub in subs" :key="sub.id" class="hover:bg-gray-950/40">
            <td class="px-3 py-3">
              <div class="text-sm text-gray-100">{{ sub.label }}</div>
              <div class="text-[10px] tracking-widest text-gray-600 uppercase mt-0.5">{{ sub.provider }}</div>
              <a v-if="sub.portal_url" :href="sub.portal_url" target="_blank" rel="noopener"
                 class="text-[10px] text-blue-400 hover:text-blue-300 normal-case tracking-normal break-all">
                ↗ portal
              </a>
            </td>
            <td class="px-3 py-3 text-[10px] tracking-widest uppercase text-gray-500">
              {{ sub.node_id || '—' }}
            </td>
            <td class="px-3 py-3 text-right font-mono">
              <div class="text-gray-100">{{ fmtCost(sub.monthly_cost) }} {{ sub.currency }}</div>
              <div v-if="sub.billing_cycle !== 'monthly'"
                   class="text-[10px] text-gray-600 normal-case tracking-normal">
                ≈ {{ fmtCost(monthlyEquiv(sub)) }} {{ sub.currency }}/mo
              </div>
            </td>
            <td class="px-3 py-3 text-[10px] tracking-widest uppercase text-gray-400">
              {{ sub.billing_cycle }}
            </td>
            <td class="px-3 py-3 text-xs font-mono text-gray-300">{{ fmtDate(sub.next_due) }}</td>
            <td class="px-3 py-3 text-xs" :class="urgencyClass(sub.next_due)">
              {{ daysLabel(sub.next_due) }}
            </td>
            <td class="px-3 py-3 text-right space-x-1 whitespace-nowrap">
              <button @click="markPaid(sub)" title="标记已付款 · 自动推下次到期"
                      class="px-2 py-1 border border-emerald-700 text-emerald-400 hover:bg-emerald-900/30 text-[10px] tracking-widest uppercase">
                ✓ paid
              </button>
              <button @click="openEdit(sub)"
                      class="px-2 py-1 border border-gray-700 text-gray-400 hover:border-gray-500 text-[10px] tracking-widest uppercase">
                edit
              </button>
              <button @click="deleteSubscription(sub)"
                      class="px-2 py-1 border border-gray-700 text-gray-400 hover:border-red-500 hover:text-red-400 text-[10px] tracking-widest uppercase">
                ⨯
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Payment history (collapsed per subscription) -->
    <details v-for="sub in subs.filter(s => (s.payments?.length ?? 0) > 0)" :key="sub.id"
             class="border border-gray-800 bg-gray-900/40 p-3">
      <summary class="cursor-pointer text-[10px] tracking-widest text-gray-500 uppercase hover:text-gray-300">
        {{ sub.label }} · 付款历史 · {{ sub.payments?.length }}
      </summary>
      <ul class="mt-2 divide-y divide-gray-800/50">
        <li v-for="(p, i) in sub.payments" :key="i"
            class="py-2 flex items-center justify-between text-xs">
          <div>
            <span class="font-mono text-gray-300">{{ fmtCost(p.amount) }} {{ p.currency }}</span>
            <span class="ml-2 text-[10px] tracking-widest text-gray-600 uppercase">
              {{ fmtDate(p.paid_at) }} · by {{ p.by }}
            </span>
            <span v-if="p.note" class="ml-2 text-gray-500 normal-case tracking-normal">{{ p.note }}</span>
          </div>
        </li>
      </ul>
    </details>
  </div>
</template>
