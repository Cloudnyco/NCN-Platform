<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { api, type AlertsData, type Envelope, type AlertEvent } from '@/api/client'
import { usePolling } from '@/composables/usePolling'
import IncidentManager from '@/components/admin/IncidentManager.vue'
import AlertRules from '@/views/admin/AlertRules.vue'

// Two tabs: live alerts (active + history + incidents) and the rule editor —
// folded in here so there's no separate "Alert Rules" nav entry. ?tab=rules
// deep-links straight to the editor (the old /admin/alert-rules redirects here).
const route = useRoute()
const tab = ref<'alerts' | 'rules'>(route.query.tab === 'rules' ? 'rules' : 'alerts')

const { data, error, lastUpdatedAt, loading } = usePolling<Envelope<AlertsData>>(
  (s) => api.alerts(s), 5000
)

const active = computed(() => data.value?.data?.active ?? [])
const history = computed(() => data.value?.data?.history ?? [])

// Acknowledge an active alert — silences its repeat re-pings + auto-escalation
// until it resolves. The 5s poll picks up the acked state.
async function ack(a: AlertEvent) {
  try { await api.alertAck(a.id) } catch { /* poll will reflect or surface */ }
}

function sevTone(s: string) {
  if (s === 'crit') return { txt: 'text-red-400', border: 'border-red-500/60', bg: 'bg-red-950/30' }
  if (s === 'warn') return { txt: 'text-amber-400', border: 'border-amber-500/60', bg: 'bg-amber-950/30' }
  return { txt: 'text-blue-400', border: 'border-blue-500/60', bg: 'bg-blue-950/30' }
}

function fmtTime(epoch: number): string {
  if (!epoch) return '—'
  return new Date(epoch * 1000).toLocaleString('en-GB', { hour12: false })
}

function fmtDuration(a: AlertEvent): string {
  const end = a.state === 'resolved' && a.resolved_at ? a.resolved_at : Math.floor(Date.now() / 1000)
  const s = end - a.fired_at
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60)
  return `${h}h ${m}m`
}

function fmtClock(epoch: number): string {
  if (!epoch) return ''
  const d = new Date(epoch * 1000)
  return d.toLocaleTimeString('en-GB', { hour12: false })
}

// Relative offset of a sample vs the alert's fire time.
//   t+0s     fired-at sample
//   t+12s    seconds after
//   t+1m32s  minutes
function fmtOffset(sample: number, firedAt: number): string {
  const d = Math.max(0, sample - firedAt)
  if (d === 0) return 't+0s'
  if (d < 60) return `t+${d}s`
  if (d < 3600) return `t+${Math.floor(d / 60)}m${d % 60 ? (d % 60) + 's' : ''}`
  const h = Math.floor(d / 3600), m = Math.floor((d % 3600) / 60)
  return `t+${h}h${m ? m + 'm' : ''}`
}

const fmtAgo = computed(() =>
  lastUpdatedAt.value ? lastUpdatedAt.value.toLocaleTimeString('en-GB', { hour12: false }) : ''
)
</script>

<template>
  <div class="space-y-4">
    <div class="border border-gray-800 bg-gray-900 p-4">
      <div class="flex items-center justify-between flex-wrap gap-2">
        <div class="flex items-center gap-3">
          <span :class="['w-1.5 h-1.5', active.length > 0 ? 'bg-red-500 animate-pulse' : 'bg-emerald-500']"></span>
          <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">告警 · Alerts</h1>
          <span v-if="active.length > 0" class="text-[10px] tracking-widest text-red-500 uppercase">
            {{ active.length }} active
          </span>
        </div>
        <div class="text-[10px] tracking-widest text-gray-600 uppercase">
          <span :class="loading ? 'text-emerald-500' : 'text-gray-700'">● POLL 5s</span>
          <span v-if="error" class="text-red-500 ml-2">ERR</span>
          <span v-else-if="fmtAgo" class="text-gray-500 ml-2">SYNC · {{ fmtAgo }}</span>
        </div>
      </div>
    </div>

    <!-- Tab nav (alerts / rules — rule editor folded in from /admin/alert-rules) -->
    <div class="border border-gray-800 bg-gray-900 overflow-x-auto">
      <div class="flex divide-x divide-gray-800 min-w-max">
        <button type="button" @click="tab = 'alerts'"
          :class="['px-4 py-2.5 text-[10px] tracking-widest uppercase whitespace-nowrap transition-colors',
            tab === 'alerts' ? 'text-emerald-400 bg-emerald-950/30 border-b-2 border-emerald-500'
                             : 'text-gray-500 hover:text-gray-300 hover:bg-gray-900/40 border-b-2 border-transparent']">
          告警 · alerts
        </button>
        <button type="button" @click="tab = 'rules'"
          :class="['px-4 py-2.5 text-[10px] tracking-widest uppercase whitespace-nowrap transition-colors',
            tab === 'rules' ? 'text-emerald-400 bg-emerald-950/30 border-b-2 border-emerald-500'
                            : 'text-gray-500 hover:text-gray-300 hover:bg-gray-900/40 border-b-2 border-transparent']">
          规则 · rules
        </button>
      </div>
    </div>

    <template v-if="tab === 'alerts'">
    <!-- Active alerts -->
    <div v-if="active.length === 0" class="border border-emerald-500/30 bg-emerald-950/20 px-4 py-6 text-center">
      <div class="text-[10px] tracking-widest text-emerald-500 uppercase">✓ all systems within thresholds</div>
    </div>

    <div v-else class="space-y-3">
      <div
        v-for="a in active" :key="a.id"
        :class="['border-2 bg-gray-900', sevTone(a.severity).border]"
      >
        <!-- Header strip — severity + node tag left, id/rule right. Each
             alert is scoped to ONE node (ctrl-01 / pop-05 / pop-04), so the
             node id rides next to severity for immediate "where" context. -->
        <div :class="['px-3 sm:px-4 py-2 border-b flex flex-col sm:flex-row sm:flex-wrap sm:items-baseline sm:justify-between gap-1 sm:gap-x-3 min-w-0 text-[10px] tracking-widest uppercase', sevTone(a.severity).border, sevTone(a.severity).bg]">
          <div class="flex flex-wrap items-baseline gap-x-2 gap-y-1 min-w-0">
            <span :class="['shrink-0', sevTone(a.severity).txt]">[{{ a.severity.toUpperCase() }}] · firing</span>
            <span class="shrink-0 px-1.5 py-0.5 bg-gray-800 text-gray-200 font-mono">@{{ a.node_id || '—' }}</span>
            <span v-if="a.acked" class="shrink-0 px-1.5 py-0.5 bg-emerald-900/50 text-emerald-300 normal-case tracking-normal">✓ acked{{ a.acked_by ? ' · ' + a.acked_by : '' }}</span>
            <button v-else type="button" @click="ack(a)"
              class="shrink-0 px-1.5 py-0.5 border border-gray-600 text-gray-300 hover:border-emerald-500 hover:text-emerald-300 normal-case tracking-normal">确认 ack</button>
          </div>
          <!-- id/rule metadata: own full-width line on mobile so it never
               competes for width with the severity/node tags above. -->
          <span class="text-gray-500 normal-case tracking-normal break-all min-w-0 w-full sm:w-auto sm:text-right"
                style="overflow-wrap: anywhere;">id={{ a.id }} · rule={{ a.rule_id }}</span>
        </div>
        <div class="p-3 sm:p-4 min-w-0">
          <div class="text-base text-gray-100 break-words" style="overflow-wrap: anywhere;">{{ a.title }}</div>
          <p class="text-xs text-gray-500 mt-1 normal-case tracking-normal break-words leading-relaxed"
             style="overflow-wrap: anywhere;">{{ a.description }}</p>
          <div class="mt-3 grid grid-cols-1 sm:grid-cols-3 gap-3 text-xs">
            <!-- Latest message + threshold side-by-side so the operator
                 immediately sees "current reading vs. the line that was
                 crossed". Message wraps freely; threshold is short. -->
            <div class="sm:col-span-2 min-w-0">
              <div class="text-[10px] text-gray-600 tracking-widest uppercase">latest sample</div>
              <div class="font-mono text-emerald-400 mt-1 break-all leading-relaxed"
                   style="overflow-wrap: anywhere;">{{ a.message }}</div>
            </div>
            <div class="min-w-0">
              <div class="text-[10px] text-gray-600 tracking-widest uppercase">threshold</div>
              <div class="font-mono text-amber-400 mt-1 break-all" style="overflow-wrap: anywhere;">{{ a.threshold || '—' }}</div>
            </div>
            <div class="min-w-0">
              <div class="text-[10px] text-gray-600 tracking-widest uppercase">since</div>
              <div class="font-mono text-gray-300 mt-1 tabular-nums">{{ fmtTime(a.fired_at) }}</div>
            </div>
            <div class="min-w-0">
              <div class="text-[10px] text-gray-600 tracking-widest uppercase">duration</div>
              <div class="font-mono text-gray-300 mt-1 tabular-nums">{{ fmtDuration(a) }}</div>
            </div>
            <div class="min-w-0">
              <div class="text-[10px] text-gray-600 tracking-widest uppercase">samples</div>
              <div class="font-mono text-gray-300 mt-1 tabular-nums">{{ (a.trail?.length ?? 0) }}</div>
            </div>
          </div>

          <!-- Sample trail — chronological message changes since fire.
               Each row: relative offset (t+Ns), wall-clock, message.
               Collapsed by default to keep the card compact; an alert
               can rack up dozens of samples on a long-running incident
               and we don't want to flood the viewport. -->
          <details v-if="(a.trail?.length ?? 0) > 1" class="mt-3 group">
            <summary class="cursor-pointer text-[10px] tracking-widest text-gray-500 uppercase hover:text-gray-200 select-none flex items-center gap-2">
              <span class="inline-block transition-transform group-open:rotate-90">▸</span>
              <span>trail · {{ a.trail?.length }} samples</span>
              <span class="text-gray-700 normal-case tracking-normal">(click to expand)</span>
            </summary>
            <ol class="mt-2 border-l border-gray-800 pl-3 space-y-1 text-[11px] font-mono">
              <li v-for="(s, i) in a.trail" :key="i"
                  class="grid grid-cols-[auto_auto_minmax(0,1fr)] gap-x-3 items-baseline leading-relaxed">
                <span class="text-gray-600 tabular-nums shrink-0">{{ fmtOffset(s.at, a.fired_at) }}</span>
                <span class="text-gray-700 tabular-nums shrink-0">{{ fmtClock(s.at) }}</span>
                <span class="text-gray-400 break-all" style="overflow-wrap: anywhere;">{{ s.message }}</span>
              </li>
            </ol>
          </details>
        </div>
      </div>
    </div>

    <!-- History
         Desktop ≥ sm: dense table. Each row is one event with the message
         column flex-growing to fill remaining width; long messages wrap
         freely instead of being clipped or pushing horizontal scroll.
         Mobile < sm: each event becomes a stacked card — table layout
         doesn't work below ~500px without horizontal scrolling, which
         hides exactly the error message the operator is trying to read. -->
    <div class="border border-gray-800 bg-gray-900">
      <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase">
        history · last {{ history.length }} events
      </div>

      <!-- DESKTOP table -->
      <table class="hidden sm:table w-full text-xs font-mono table-fixed">
        <colgroup>
          <col class="w-[22ch]"><!-- when -->
          <col class="w-[8ch]"><!--  node -->
          <col class="w-[6ch]"><!--  sev  -->
          <col class="w-[20ch]"><!-- title -->
          <col><!-- message — flex remainder -->
          <col class="w-[8ch]"><!--  dur  -->
          <col class="w-[8ch]"><!-- state -->
        </colgroup>
        <thead class="text-[10px] text-gray-600 uppercase tracking-widest">
          <tr class="border-b border-gray-800">
            <th class="text-left  px-3 py-2">when</th>
            <th class="text-left  px-3 py-2">node</th>
            <th class="text-left  px-3 py-2">sev</th>
            <th class="text-left  px-3 py-2">title</th>
            <th class="text-left  px-3 py-2">message</th>
            <th class="text-right px-3 py-2">dur</th>
            <th class="text-right px-3 py-2">state</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="a in history" :key="a.id" class="border-b border-gray-800/50 align-top">
            <td class="px-3 py-2 text-gray-500 whitespace-nowrap tabular-nums">{{ fmtTime(a.fired_at) }}</td>
            <td class="px-3 py-2 text-gray-200 whitespace-nowrap">{{ a.node_id || '—' }}</td>
            <td :class="['px-3 py-2 whitespace-nowrap', sevTone(a.severity).txt]">{{ a.severity.toUpperCase() }}</td>
            <td class="px-3 py-2 text-gray-200 break-words" style="overflow-wrap: anywhere;">{{ a.title }}</td>
            <td class="px-3 py-2 text-gray-500 break-all leading-relaxed"
                style="overflow-wrap: anywhere; word-break: break-word;">{{ a.message }}</td>
            <td class="px-3 py-2 text-right text-gray-400 tabular-nums whitespace-nowrap">{{ fmtDuration(a) }}</td>
            <td class="px-3 py-2 text-right whitespace-nowrap">
              <span :class="a.state === 'firing' ? 'text-red-400' : 'text-gray-500'">{{ a.state }}</span>
            </td>
          </tr>
          <tr v-if="history.length === 0">
            <td colspan="7" class="px-3 py-4 text-gray-600 italic text-center">no events</td>
          </tr>
        </tbody>
      </table>

      <!-- MOBILE card list -->
      <ul class="sm:hidden divide-y divide-gray-800">
        <li v-if="history.length === 0" class="px-4 py-6 text-center text-gray-600 italic text-xs">
          no events
        </li>
        <li v-for="a in history" :key="a.id" class="px-4 py-3 font-mono text-xs">
          <div class="flex items-baseline justify-between gap-2 flex-wrap">
            <div class="flex items-baseline gap-2">
              <span :class="['text-[10px] tracking-widest uppercase shrink-0', sevTone(a.severity).txt]">[{{ a.severity.toUpperCase() }}]</span>
              <span class="text-[10px] tracking-widest uppercase text-gray-300 shrink-0">@{{ a.node_id || '—' }}</span>
            </div>
            <span :class="['text-[10px] tracking-widest uppercase shrink-0', a.state === 'firing' ? 'text-red-400' : 'text-gray-500']">{{ a.state }}</span>
          </div>
          <div class="mt-1 text-sm text-gray-200 break-words leading-snug" style="overflow-wrap: anywhere;">{{ a.title }}</div>
          <div v-if="a.message"
               class="mt-1 text-[11px] text-gray-500 break-all leading-relaxed"
               style="overflow-wrap: anywhere; word-break: break-word;">{{ a.message }}</div>
          <div class="mt-1.5 flex flex-wrap gap-x-3 gap-y-0.5 text-[10px] text-gray-600 tracking-widest uppercase">
            <span class="tabular-nums">{{ fmtTime(a.fired_at) }}</span>
            <span>dur <span class="text-gray-400 tabular-nums">{{ fmtDuration(a) }}</span></span>
          </div>
        </li>
      </ul>
    </div>

    <!-- Public status-page incidents (admin CRUD, see /status for the
         sanitised public view). Lives here next to alerts because
         "open an incident" usually follows "saw an alert fire"; keeps
         the response loop tight. -->
    <IncidentManager />
    </template>

    <!-- Rules tab: the full data-driven rule editor, folded in from the old
         standalone /admin/alert-rules page. -->
    <template v-if="tab === 'rules'">
      <AlertRules />
    </template>
  </div>
</template>
