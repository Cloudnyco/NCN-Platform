<script setup lang="ts">
// Audit.vue — security audit panel. Reads /api/v1/auth/audit and renders
// a day-grouped timeline with click-to-expand detail panels. All sensitive
// operator-impacting actions land here.
//
// Readability priorities (per the brief):
//   - Day grouping. Each visible day gets a header rule so the eye can
//     navigate temporally without parsing timestamps.
//   - Severity color. info=neutral, warn=amber side-bar, critical=red
//     side-bar + faint background tint. Color is on the LEFT edge so
//     the data columns themselves aren't washed in tinted text.
//   - Human-readable sentences. "alice signed in via passkey"
//     beats `event=login.ok actor=alice path=passkey`. Templates
//     live in i18n so zh-CN / zh-TW translate naturally.
//   - Mono ONLY for IDs / IPs / UAs / raw JSON. Body text stays in
//     the system sans-serif so lines remain comfortable to scan.
//   - Sticky filter bar at the top so a long scroll never strands the
//     operator from controls.
//   - Click row to expand. Default density = 1 line per event with
//     time + icon + severity + sentence + outcome. Expand reveals
//     peer / UA / target / details JSON / raw event-id.
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api, type AuditEvent, type AuditFilter, type AuditStats } from '@/api/client'
import { useSessionStore } from '@/stores/session'

const { t, te } = useI18n()
const session = useSessionStore()
const isAdmin = computed(() => session.role === 'admin')

// ---------- Category tabs ----------
// Each category maps to a server-side `event=` filter; "all" means no
// filter. `event=` accepts either an exact event name or a `prefix.*`
// glob (see audit.go's auditFilter.match).
//
// IMPORTANT: declared as typed `readonly` arrays in <script>, NOT inline
// in the template — `as const` in a v-for crashes at runtime in Vue
// templates. (See feedback_vue_template_no_as_const memory.)
type CategoryKey = 'all' | 'login' | 'password' | 'recovery' | 'device' | 'invite' | 'operator' | 'peering' | 'mail' | 'sso' | 'breakglass'

interface CategoryDef {
  key: CategoryKey
  filter: string
}

const CATEGORIES: readonly CategoryDef[] = [
  { key: 'all',         filter: ''                  },
  { key: 'login',       filter: 'login.*'           },
  { key: 'password',    filter: 'password.*'        },
  { key: 'recovery',    filter: 'recovery.*'        },
  { key: 'device',      filter: 'device.*'          },
  { key: 'invite',      filter: 'invite.*'          },
  { key: 'operator',    filter: 'operator.*'        },
  { key: 'peering',     filter: 'peering.*'         },
  { key: 'mail',        filter: 'mail.*'            },
  { key: 'sso',         filter: 'sso.*'             },
  { key: 'breakglass',  filter: 'break-glass.*'     },
]

type Severity = 'info' | 'warn' | 'critical'
const SEVERITIES: readonly Severity[] = ['info', 'warn', 'critical']

// ---------- Reactive state ----------
const filter      = ref<AuditFilter>({})
const events      = ref<AuditEvent[]>([])
const stats       = ref<AuditStats | null>(null)
const nextCursor  = ref('')
const loading     = ref(false)
const err         = ref<string | null>(null)
const expanded    = ref<Record<string, boolean>>({})

async function refresh(append = false): Promise<void> {
  if (!isAdmin.value) return
  loading.value = true
  err.value = null
  try {
    const f: AuditFilter = { ...filter.value, limit: 100 }
    if (append) f.cursor = nextCursor.value
    const env = await api.auditQuery(f)
    if (!env.ok) throw new Error(env.error || 'query failed')
    const incoming = env.data?.events ?? []
    events.value = append ? [...events.value, ...incoming] : incoming
    nextCursor.value = env.data?.next_cursor ?? ''
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function refreshStats(): Promise<void> {
  if (!isAdmin.value) return
  try {
    const env = await api.auditStats()
    if (env.ok) stats.value = env.data ?? null
  } catch { /* non-fatal — stats strip just won't render */ }
}

function applyFilters(): void {
  events.value = []
  nextCursor.value = ''
  refresh(false)
}

function resetFilters(): void {
  filter.value = {}
  applyFilters()
}

function exportJSONL(): void {
  // Use window.location so the browser handles the file download via the
  // Content-Disposition header sent by handleAuditExport.
  window.location.assign(api.auditExportURL(filter.value))
}

function toggleExpand(id: string): void {
  expanded.value = { ...expanded.value, [id]: !expanded.value[id] }
}

function setCategory(cat: CategoryKey): void {
  const c = CATEGORIES.find(x => x.key === cat)
  filter.value = { ...filter.value, event: c?.filter || undefined }
  applyFilters()
}

const currentCategory = computed<CategoryKey>(() => {
  const f = filter.value.event || ''
  return CATEGORIES.find(c => c.filter === f)?.key ?? 'all'
})

// ---------- Time / grouping helpers ----------
function fmtTime(s: string): string {
  if (!s) return ''
  try {
    const d = new Date(s)
    const hh = String(d.getHours()).padStart(2, '0')
    const mm = String(d.getMinutes()).padStart(2, '0')
    const ss = String(d.getSeconds()).padStart(2, '0')
    return `${hh}:${mm}:${ss}`
  } catch { return s }
}
function fmtDate(s: string): string {
  if (!s) return ''
  try {
    return new Date(s).toLocaleDateString(undefined, {
      weekday: 'long', year: 'numeric', month: 'long', day: 'numeric',
    })
  } catch { return s }
}
function dayKey(s: string): string {
  if (!s) return ''
  try {
    const d = new Date(s)
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
  } catch { return s }
}

interface DayGroup {
  day:    string       // YYYY-MM-DD for :key stability
  date:   string       // RFC3339 of first event in the group, for fmtDate
  events: AuditEvent[]
}

const grouped = computed<DayGroup[]>(() => {
  const out: DayGroup[] = []
  let curDay = ''
  let curBucket: AuditEvent[] = []
  for (const ev of events.value) {
    const k = dayKey(ev.ts)
    if (k !== curDay) {
      if (curBucket.length) out.push({ day: curDay, date: curBucket[0].ts, events: curBucket })
      curDay = k
      curBucket = [ev]
    } else {
      curBucket.push(ev)
    }
  }
  if (curBucket.length) out.push({ day: curDay, date: curBucket[0].ts, events: curBucket })
  return out
})

// ---------- Event sentence (i18n template) ----------
// vue-i18n traverses `.` so `mail.role-recover.mint` (which has dots
// AND a hyphen) can't be a key as-is. We normalize event ids to
// underscored slugs to match the keys in src/i18n/*.ts.
function eventKey(ev: string): string {
  return ev.replace(/[.\-]/g, '_')
}

function eventSentence(ev: AuditEvent): string {
  const vars: Record<string, unknown> = {
    actor: ev.actor || '—',
    target: ev.target || '',
  }
  if (ev.details) {
    for (const k of Object.keys(ev.details)) vars[k] = ev.details[k]
  }

  // login.ok with a `path` detail uses a more specific template
  // (`login_ok_passkey`, `login_ok_totp`, etc.) if defined.
  if (ev.event === 'login.ok' && (ev.details as Record<string, unknown> | undefined)?.path) {
    const p = String((ev.details as Record<string, unknown>).path)
    const specific = 'admin_audit.events.login_ok_' + p.replace(/-/g, '_')
    if (te(specific)) return t(specific, vars)
  }

  const key = 'admin_audit.events.' + eventKey(ev.event)
  if (te(key)) return t(key, vars)

  // Fallback for events not yet covered by i18n — still readable.
  return `${ev.event} · ${ev.actor || ''}` + (ev.target ? ` → ${ev.target}` : '')
}

// ---------- Visual mapping ----------
function eventIcon(ev: AuditEvent): string {
  if (ev.event.startsWith('login.'))       return ev.outcome === 'fail' ? '⨯' : '→'
  if (ev.event === 'logout')               return '←'
  if (ev.event.startsWith('password.'))    return '🔒'
  if (ev.event.startsWith('recovery.'))    return '🆘'
  if (ev.event.startsWith('passkey.'))     return '🔑'
  if (ev.event.startsWith('device.'))      return '💻'
  if (ev.event.startsWith('invite.'))      return '✉'
  if (ev.event.startsWith('operator.'))    return '👤'
  if (ev.event.startsWith('peering.'))     return '⌬'
  if (ev.event.startsWith('mail.'))        return '✉'
  if (ev.event.startsWith('sso.'))         return '⇄'
  if (ev.event.startsWith('break-glass.')) return '⚠'
  if (ev.event === 'service.start')        return '●'
  return '·'
}

function severityRowClass(s: Severity): string {
  if (s === 'critical') return 'border-l-2 border-red-500/80 bg-red-500/[0.03]'
  if (s === 'warn')     return 'border-l-2 border-amber-500/70 bg-amber-500/[0.03]'
  return 'border-l-2 border-transparent'
}
function severityBadge(s: Severity): string {
  if (s === 'critical') return 'text-red-400 bg-red-500/10'
  if (s === 'warn')     return 'text-amber-400 bg-amber-500/10'
  return 'text-gray-500 bg-gray-500/10'
}
function outcomeBadge(o: AuditEvent['outcome']): string {
  if (o === 'fail' || o === 'denied') return 'text-red-400'
  return 'text-emerald-500'
}

// ---------- 24h sparkline path ----------
const sparkPath = computed<string>(() => {
  const buckets = stats.value?.hourly_24h
  if (!buckets || buckets.length === 0) return ''
  const max = Math.max(1, ...buckets.map(b => b.count))
  const W = 240, H = 32
  const stepX = W / (buckets.length - 1 || 1)
  return buckets.map((b, i) => {
    const x = (i * stepX).toFixed(1)
    const y = (H - (b.count / max) * H).toFixed(1)
    return (i === 0 ? 'M' : 'L') + x + ',' + y
  }).join(' ')
})

// ---------- Lifecycle ----------
onMounted(async () => {
  await Promise.all([refresh(), refreshStats()])
})
watch(isAdmin, (now, prev) => {
  if (now && !prev) { refresh(); refreshStats() }
})
</script>

<template>
  <div class="space-y-4">
    <!-- Admin-only gate ------------------------------------------------- -->
    <div v-if="!isAdmin" class="border border-amber-500/60 bg-amber-900/20 text-xs text-amber-300 p-4">
      {{ t('admin_audit.admin_only') }}
    </div>

    <template v-else>
      <!-- Header strip: title + 24h sparkline + severity counts --------- -->
      <div class="border border-gray-800 bg-gray-900 p-4 flex flex-wrap items-center gap-x-6 gap-y-3">
        <div class="flex items-center gap-3 min-w-0">
          <span class="w-1.5 h-1.5 bg-emerald-500 animate-pulse"></span>
          <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200">{{ t('admin_audit.title') }}</h1>
          <span class="text-[10px] text-gray-600">· {{ t('admin_audit.subtitle') }}</span>
        </div>

        <div v-if="stats" class="flex flex-wrap items-center gap-4 ml-auto text-[10px]">
          <div class="flex items-center gap-2 text-gray-500">
            <span class="uppercase tracking-widest">24h</span>
            <svg viewBox="0 0 240 32" class="w-[240px] h-[32px]" preserveAspectRatio="none">
              <path :d="sparkPath" stroke="rgb(16 185 129 / 0.7)" stroke-width="1" fill="none" />
            </svg>
            <span class="text-emerald-400 font-medium tabular-nums">{{ stats.total_24h }}</span>
            <span>{{ t('admin_audit.total_24h') }}</span>
          </div>

          <div class="flex items-center gap-3">
            <span class="text-gray-500">
              <span class="text-gray-300 tabular-nums">{{ stats.by_severity.info ?? 0 }}</span>
              {{ t('admin_audit.severity.info') }}
            </span>
            <span class="text-amber-400 tabular-nums">
              {{ stats.by_severity.warn ?? 0 }} {{ t('admin_audit.severity.warn') }}
            </span>
            <span class="text-red-400 tabular-nums">
              {{ stats.by_severity.critical ?? 0 }} {{ t('admin_audit.severity.critical') }}
            </span>
          </div>
        </div>
      </div>

      <!-- Filter bar (sticky) ------------------------------------------- -->
      <div class="border border-gray-800 bg-gray-950 p-3 space-y-2 sticky top-0 z-10 backdrop-blur">
        <!-- Category tabs -->
        <div class="flex flex-wrap gap-1">
          <button v-for="cat in CATEGORIES" :key="cat.key"
            type="button"
            @click="setCategory(cat.key)"
            :class="[
              'px-2 py-1 text-[10px] tracking-widest uppercase border transition-colors',
              currentCategory === cat.key
                ? 'border-emerald-500 text-emerald-400 bg-emerald-500/5'
                : 'border-gray-800 text-gray-500 hover:text-gray-300 hover:border-gray-700'
            ]">
            {{ t('admin_audit.category.' + cat.key) }}
          </button>
        </div>

        <!-- Search / actor / severity / date inputs -->
        <div class="grid grid-cols-1 md:grid-cols-12 gap-2 text-xs">
          <input v-model="filter.q" @keyup.enter="applyFilters"
            :placeholder="t('admin_audit.filter.placeholder_search')"
            class="md:col-span-4 px-2 py-1.5 border border-gray-800 bg-gray-900 text-gray-200 placeholder-gray-600 focus:outline-none focus:border-emerald-700" />
          <input v-model="filter.actor" @keyup.enter="applyFilters"
            :placeholder="t('admin_audit.filter.placeholder_actor')"
            class="md:col-span-2 px-2 py-1.5 border border-gray-800 bg-gray-900 text-gray-200 placeholder-gray-600 focus:outline-none focus:border-emerald-700" />
          <select v-model="filter.severity" @change="applyFilters"
            class="md:col-span-2 px-2 py-1.5 border border-gray-800 bg-gray-900 text-gray-300 focus:outline-none">
            <option value="">{{ t('admin_audit.filter.any_severity') }}</option>
            <option v-for="s in SEVERITIES" :key="s" :value="s">{{ t('admin_audit.severity.' + s) }}</option>
          </select>
          <input v-model="filter.since" type="datetime-local" @change="applyFilters"
            :title="t('admin_audit.filter.since_label')"
            class="md:col-span-2 px-2 py-1.5 border border-gray-800 bg-gray-900 text-gray-400 text-[10px] focus:outline-none" />
          <input v-model="filter.until" type="datetime-local" @change="applyFilters"
            :title="t('admin_audit.filter.until_label')"
            class="md:col-span-2 px-2 py-1.5 border border-gray-800 bg-gray-900 text-gray-400 text-[10px] focus:outline-none" />
        </div>

        <!-- Action row -->
        <div class="flex items-center gap-2 text-[10px] tracking-widest uppercase">
          <button type="button" @click="applyFilters" :disabled="loading"
            class="px-2 py-1 border border-gray-800 text-gray-400 hover:text-emerald-400 hover:border-emerald-700 disabled:opacity-30">
            ↻ {{ t('admin_audit.filter.refresh') }}
          </button>
          <button type="button" @click="resetFilters" :disabled="loading"
            class="px-2 py-1 border border-gray-800 text-gray-500 hover:text-gray-300">
            × {{ t('admin_audit.filter.reset') }}
          </button>
          <button type="button" @click="exportJSONL"
            class="ml-auto px-2 py-1 border border-gray-800 text-gray-500 hover:text-gray-300">
            ⤓ {{ t('admin_audit.filter.export') }}
          </button>
        </div>
      </div>

      <!-- Error banner --------------------------------------------------- -->
      <div v-if="err" class="border border-red-500/60 bg-red-900/20 text-xs text-red-300 p-3">
        {{ err }}
      </div>

      <!-- Loading state -->
      <div v-if="loading && events.length === 0" class="text-xs text-gray-500 px-4 py-8 text-center">
        {{ t('admin_audit.loading') }}
      </div>

      <!-- Empty state -->
      <div v-else-if="!loading && events.length === 0"
        class="text-xs text-gray-500 px-4 py-12 text-center border border-gray-800 border-dashed">
        {{ t('admin_audit.empty') }}
      </div>

      <!-- Day-grouped timeline ------------------------------------------ -->
      <div v-else class="space-y-6">
        <div v-for="g in grouped" :key="g.day">
          <!-- Day header rule -->
          <div class="flex items-baseline gap-3 mb-2 px-1">
            <span class="text-[10px] tracking-widest uppercase text-gray-400">{{ fmtDate(g.date) }}</span>
            <span class="flex-1 h-px bg-gray-800 self-center"></span>
            <span class="text-[10px] text-gray-600 tabular-nums">{{ g.events.length }}</span>
          </div>

          <!-- Events for this day -->
          <div class="border border-gray-800 bg-gray-950 divide-y divide-gray-900">
            <div v-for="ev in g.events" :key="ev.id"
              :class="severityRowClass(ev.severity)">
              <!-- Compact row (button so it's keyboard-accessible) -->
              <button type="button" @click="toggleExpand(ev.id)"
                class="w-full text-left px-3 py-2 flex items-baseline gap-3 hover:bg-gray-900/60 focus:outline-none focus:bg-gray-900/80">
                <span class="text-gray-500 font-mono text-[11px] tabular-nums w-[7ch] flex-shrink-0">
                  {{ fmtTime(ev.ts) }}
                </span>
                <span class="text-sm leading-none w-5 text-center flex-shrink-0 text-gray-400" aria-hidden="true">
                  {{ eventIcon(ev) }}
                </span>
                <span :class="['text-[10px] uppercase tracking-wider px-1.5 py-0.5 flex-shrink-0', severityBadge(ev.severity)]">
                  {{ t('admin_audit.severity.' + ev.severity) }}
                </span>
                <span class="text-xs text-gray-200 flex-1 truncate">
                  {{ eventSentence(ev) }}
                </span>
                <span v-if="ev.outcome !== 'ok'"
                  :class="['text-[10px] tracking-widest uppercase flex-shrink-0', outcomeBadge(ev.outcome)]">
                  {{ t('admin_audit.outcome.' + ev.outcome) }}
                </span>
              </button>

              <!-- Expanded detail panel -->
              <div v-if="expanded[ev.id]"
                class="px-3 pb-3 pt-1 grid grid-cols-1 md:grid-cols-2 gap-x-6 gap-y-1.5 text-[11px] bg-gray-900/40">
                <div>
                  <span class="text-gray-600 tracking-widest uppercase mr-2">{{ t('admin_audit.detail.peer') }}</span>
                  <span class="text-gray-300 font-mono break-all">{{ ev.peer || '—' }}</span>
                </div>
                <div v-if="ev.target">
                  <span class="text-gray-600 tracking-widest uppercase mr-2">{{ t('admin_audit.detail.target') }}</span>
                  <span class="text-gray-300 font-mono break-all">{{ ev.target }}</span>
                </div>
                <div v-if="ev.ua" class="md:col-span-2">
                  <span class="text-gray-600 tracking-widest uppercase mr-2">{{ t('admin_audit.detail.ua') }}</span>
                  <span class="text-gray-500 font-mono break-all">{{ ev.ua }}</span>
                </div>
                <div v-if="ev.details && Object.keys(ev.details).length" class="md:col-span-2">
                  <div class="text-gray-600 tracking-widest uppercase mb-0.5">{{ t('admin_audit.detail.details') }}</div>
                  <pre class="text-gray-400 bg-black/30 p-2 overflow-x-auto font-mono text-[11px]">{{ JSON.stringify(ev.details, null, 2) }}</pre>
                </div>
                <div class="md:col-span-2 pt-1 border-t border-gray-900">
                  <span class="text-gray-700 font-mono text-[10px] break-all">{{ ev.id }} · {{ ev.event }}</span>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- Pagination: load older -->
        <div v-if="nextCursor" class="flex justify-center pt-2">
          <button type="button" @click="refresh(true)" :disabled="loading"
            class="text-[10px] tracking-widest uppercase px-3 py-1.5 border border-gray-800 text-gray-500 hover:text-emerald-400 hover:border-emerald-700 disabled:opacity-30">
            ↓ {{ t('admin_audit.load_more') }}
          </button>
        </div>
      </div>
    </template>
  </div>
</template>
