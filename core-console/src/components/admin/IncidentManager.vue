<script setup lang="ts">
// IncidentManager — admin interface for the public status page incidents.
// Lives at the bottom of /admin/alerts (close to where the live alert
// firings show). Admins:
//
//   * see all incidents (open + resolved), full timeline
//   * open new incidents
//   * append timeline updates (with optional status change)
//   * patch title / severity / affected_pops / status directly
//   * delete (for mistakes — separate from "resolved", which keeps the
//     entry visible on /status for 30 days)
//
// Public consumers see a sanitised view at /status, fed by the same
// data via /api/v1/incidents/public — no operator usernames, last 30
// days only.

import { ref, computed, onMounted } from 'vue'
import { useSessionStore } from '@/stores/session'
import {
  api,
  type Incident, type IncidentCreateReq, type IncidentStatus, type IncidentSeverity,
} from '@/api/client'

const session = useSessionStore()
const isAdmin = computed(() => session.role === 'admin')

const incidents = ref<Incident[]>([])
const loading = ref(false)
const err = ref<string | null>(null)

// Known PoPs — used to populate the "affected" picker. Hard-coded for
// now (matches backend/fleet.go node list); a future refactor could
// fetch from /api/v1/fleet/public to stay in sync.
const KNOWN_POPS = ['pop-03', 'pop-04', 'ctrl-01', 'pop-01', 'pop-08', 'pop-06', 'pop-05'] as const

const STATUSES: { v: IncidentStatus; label: string }[] = [
  { v: 'investigating', label: 'investigating' },
  { v: 'identified',    label: 'identified' },
  { v: 'monitoring',    label: 'monitoring' },
  { v: 'resolved',      label: 'resolved' },
]
const SEVERITIES: { v: IncidentSeverity; label: string }[] = [
  { v: 'minor',    label: 'minor' },
  { v: 'major',    label: 'major' },
  { v: 'critical', label: 'critical' },
]

async function refresh() {
  if (!isAdmin.value) return
  loading.value = true
  err.value = null
  try {
    const env = await api.incidentsList()
    if (!env.ok) throw new Error(env.error || 'list failed')
    incidents.value = env.data ?? []
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

// ───────────── new incident form state ─────────────
const newOpen = ref(false)
const newForm = ref<IncidentCreateReq>({
  title: '', status: 'investigating', severity: 'minor',
  affected_pops: [], body: '',
})
const newBusy = ref(false)

function togglePoP(p: string) {
  if (!newForm.value.affected_pops) newForm.value.affected_pops = []
  const i = newForm.value.affected_pops.indexOf(p)
  if (i >= 0) newForm.value.affected_pops.splice(i, 1)
  else newForm.value.affected_pops.push(p)
}

async function createIncident() {
  if (newBusy.value) return
  if (!newForm.value.title.trim() || !newForm.value.body.trim()) {
    err.value = 'title + body required'
    return
  }
  newBusy.value = true
  err.value = null
  try {
    const env = await api.incidentsCreate(newForm.value)
    if (!env.ok) throw new Error(env.error || 'create failed')
    // Reset form, refresh list
    newForm.value = { title: '', status: 'investigating', severity: 'minor', affected_pops: [], body: '' }
    newOpen.value = false
    await refresh()
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  } finally {
    newBusy.value = false
  }
}

// ───────────── per-incident update form state ─────────────
// updateDraft[id] = { message, status } for the inline update form on
// each open incident card.
const updateDraft = ref<Record<string, { message: string; status: IncidentStatus | '' }>>({})
const updateBusy = ref<string | null>(null)

function draftFor(id: string) {
  if (!updateDraft.value[id]) updateDraft.value[id] = { message: '', status: '' }
  return updateDraft.value[id]
}

async function postUpdate(inc: Incident) {
  const d = draftFor(inc.id)
  if (!d.message.trim()) {
    err.value = 'message required'
    return
  }
  updateBusy.value = inc.id
  err.value = null
  try {
    const req = { message: d.message, ...(d.status ? { status: d.status as IncidentStatus } : {}) }
    const env = await api.incidentsAddUpdate(inc.id, req)
    if (!env.ok) throw new Error(env.error || 'update failed')
    d.message = ''
    d.status = ''
    await refresh()
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  } finally {
    updateBusy.value = null
  }
}

async function quickResolve(inc: Incident) {
  if (!confirm(`Mark "${inc.title}" as resolved?`)) return
  try {
    const env = await api.incidentsPatch(inc.id, { status: 'resolved' })
    if (!env.ok) throw new Error(env.error || 'patch failed')
    await refresh()
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  }
}

async function deleteIncident(inc: Incident) {
  if (!confirm(`Delete "${inc.title}" entirely? (Distinct from resolving — this removes it from history.)`)) return
  try {
    const env = await api.incidentsDelete(inc.id)
    if (!env.ok) throw new Error(env.error || 'delete failed')
    await refresh()
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  }
}

// ───────────── formatting ─────────────
function fmtTime(iso: string): string {
  return new Date(iso).toLocaleString('en-GB', { hour12: false, timeZone: 'UTC' }) + 'Z'
}
function statusTone(s: string): string {
  switch (s) {
    case 'investigating': return 'text-red-400 border-red-500/60'
    case 'identified':    return 'text-amber-400 border-amber-500/60'
    case 'monitoring':    return 'text-yellow-300 border-yellow-500/60'
    case 'resolved':      return 'text-emerald-400 border-emerald-500/60'
    default:              return 'text-gray-400 border-gray-600'
  }
}
function severityTone(s: string): string {
  switch (s) {
    case 'critical': return 'text-red-400'
    case 'major':    return 'text-amber-400'
    default:         return 'text-gray-400'
  }
}

const openIncidents = computed(() => incidents.value.filter(i => i.status !== 'resolved'))
const resolvedIncidents = computed(() => incidents.value.filter(i => i.status === 'resolved'))

onMounted(refresh)
</script>

<template>
  <div v-if="isAdmin" class="border border-gray-800 bg-gray-900">
    <!-- Header -->
    <div class="px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex justify-between flex-wrap gap-2 items-center">
      <span class="flex items-center gap-2">
        <span :class="['w-1.5 h-1.5', openIncidents.length > 0 ? 'bg-red-500 animate-pulse' : 'bg-emerald-500']"></span>
        事件通告 · status-page incidents · {{ openIncidents.length }} open
      </span>
      <div class="flex items-center gap-2">
        <button
          type="button"
          @click="refresh"
          :disabled="loading"
          class="text-gray-500 hover:text-emerald-400 normal-case tracking-normal disabled:opacity-30"
          title="refresh"
        >↻ refresh</button>
        <button
          type="button"
          @click="newOpen = !newOpen"
          class="px-2 py-1 border border-emerald-700 text-emerald-400 hover:bg-emerald-900/30 normal-case tracking-normal"
        >{{ newOpen ? '− cancel' : '+ open incident' }}</button>
      </div>
    </div>

    <!-- Inline new-incident form -->
    <div v-if="newOpen" class="p-4 border-b border-gray-800 bg-gray-950/40 space-y-3">
      <div>
        <label class="block text-[10px] tracking-widest text-gray-500 mb-1">title</label>
        <input v-model="newForm.title" type="text" maxlength="120"
               placeholder="e.g. pop-04 BGP session to AS1299 down"
               class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none" />
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">status</label>
          <select v-model="newForm.status"
                  class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none">
            <option v-for="s in STATUSES" :key="s.v" :value="s.v">{{ s.label }}</option>
          </select>
        </div>
        <div>
          <label class="block text-[10px] tracking-widest text-gray-500 mb-1">severity</label>
          <select v-model="newForm.severity"
                  class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 focus:border-emerald-700 focus:outline-none">
            <option v-for="s in SEVERITIES" :key="s.v" :value="s.v">{{ s.label }}</option>
          </select>
        </div>
      </div>

      <div>
        <label class="block text-[10px] tracking-widest text-gray-500 mb-1">affected PoPs</label>
        <div class="flex flex-wrap gap-1.5">
          <button v-for="p in KNOWN_POPS" :key="p" type="button"
                  @click="togglePoP(p)"
                  :class="['px-2 py-1 text-[10px] tracking-widest uppercase border transition-colors',
                           newForm.affected_pops?.includes(p)
                             ? 'border-amber-500 text-amber-400 bg-amber-950/30'
                             : 'border-gray-700 text-gray-400 hover:border-gray-500']">
            {{ p }}
          </button>
        </div>
      </div>

      <div>
        <label class="block text-[10px] tracking-widest text-gray-500 mb-1">body (initial message)</label>
        <textarea v-model="newForm.body" rows="3"
                  placeholder="What's happening? What's known so far?"
                  class="w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-300 focus:border-emerald-700 focus:outline-none normal-case tracking-normal"></textarea>
      </div>

      <button type="button" @click="createIncident" :disabled="newBusy"
              class="px-4 py-2 border border-emerald-500 text-emerald-500 hover:bg-emerald-500 hover:text-black text-[10px] tracking-widest uppercase transition-colors disabled:opacity-30 disabled:cursor-not-allowed">
        {{ newBusy ? '◌ creating…' : '▶ open incident' }}
      </button>
    </div>

    <!-- Error -->
    <div v-if="err" class="p-3 border-b border-red-500/40 bg-red-950/20 text-xs text-red-400 normal-case tracking-normal">
      ⨯ {{ err }}
    </div>

    <!-- Empty state -->
    <div v-if="!loading && incidents.length === 0" class="p-6 text-center text-sm text-gray-600 italic normal-case tracking-normal">
      no incidents on file · /status page will show "all systems operational"
    </div>

    <!-- Active incidents (top) -->
    <ul v-if="openIncidents.length" class="divide-y divide-gray-800">
      <li v-for="inc in openIncidents" :key="inc.id" class="p-4 space-y-3 bg-red-950/10">
        <div class="flex items-start justify-between gap-2 flex-wrap">
          <div class="min-w-0 flex-1">
            <div class="text-sm text-gray-100 font-semibold">{{ inc.title }}</div>
            <div class="mt-1 text-[10px] tracking-widest uppercase flex items-center gap-2 flex-wrap">
              <span :class="['border px-1.5 py-0.5', statusTone(inc.status)]">{{ inc.status }}</span>
              <span :class="severityTone(inc.severity)">{{ inc.severity }}</span>
              <span class="text-gray-600">opened {{ fmtTime(inc.created_at) }}</span>
              <span class="text-gray-700 normal-case tracking-normal">by {{ inc.created_by }}</span>
              <span v-if="inc.affected_pops?.length" class="text-amber-400 normal-case tracking-normal">
                · {{ inc.affected_pops.join(', ') }}
              </span>
            </div>
          </div>
          <div class="flex gap-1.5">
            <button @click="quickResolve(inc)"
                    class="px-2 py-1 border border-emerald-700 text-emerald-400 hover:bg-emerald-900/30 text-[10px] tracking-widest uppercase">
              ✓ resolve
            </button>
            <button @click="deleteIncident(inc)"
                    class="px-2 py-1 border border-gray-700 text-gray-400 hover:border-red-500 hover:text-red-400 text-[10px] tracking-widest uppercase"
                    title="delete (removes from history)">
              ⨯
            </button>
          </div>
        </div>

        <p class="text-xs text-gray-400 whitespace-pre-wrap normal-case tracking-normal leading-relaxed">
          {{ inc.body }}
        </p>

        <!-- Existing updates timeline -->
        <div v-if="inc.updates?.length" class="border-l-2 border-gray-800 pl-3 space-y-1.5">
          <div v-for="(u, i) in inc.updates" :key="i" class="text-xs text-gray-400 normal-case tracking-normal">
            <span class="text-[10px] tracking-widest text-gray-600 uppercase">
              {{ fmtTime(u.ts) }} · {{ u.author }}
              <span v-if="u.status" :class="['ml-1', statusTone(u.status).split(' ')[0]]">→ {{ u.status }}</span>
            </span>
            <div class="mt-0.5 whitespace-pre-wrap leading-relaxed">{{ u.message }}</div>
          </div>
        </div>

        <!-- Add-update inline form -->
        <div class="flex flex-col sm:flex-row gap-2 pt-2">
          <input v-model="draftFor(inc.id).message" type="text"
                 placeholder="add an update for the public status page…"
                 @keyup.enter="postUpdate(inc)"
                 class="flex-1 bg-black border border-gray-800 px-3 py-1.5 text-xs font-mono text-gray-200 focus:border-emerald-700 focus:outline-none normal-case tracking-normal" />
          <select v-model="draftFor(inc.id).status"
                  class="bg-black border border-gray-800 px-2 py-1.5 text-xs font-mono text-gray-300 focus:border-emerald-700 focus:outline-none normal-case tracking-normal">
            <option value="">— status unchanged —</option>
            <option v-for="s in STATUSES" :key="s.v" :value="s.v">{{ s.label }}</option>
          </select>
          <button type="button" @click="postUpdate(inc)"
                  :disabled="updateBusy === inc.id"
                  class="px-3 py-1.5 border border-emerald-700 text-emerald-400 hover:bg-emerald-900/30 text-[10px] tracking-widest uppercase disabled:opacity-30">
            {{ updateBusy === inc.id ? '…' : '↑ post' }}
          </button>
        </div>
      </li>
    </ul>

    <!-- Resolved (collapsed by default — just show the count + a button to expand) -->
    <div v-if="resolvedIncidents.length" class="px-4 py-3 border-t border-gray-800 bg-gray-950/40">
      <details>
        <summary class="text-[10px] tracking-widest text-gray-600 uppercase cursor-pointer hover:text-gray-400">
          ▸ resolved · {{ resolvedIncidents.length }}
        </summary>
        <ul class="mt-3 divide-y divide-gray-800/50">
          <li v-for="inc in resolvedIncidents" :key="inc.id" class="py-3 flex items-start justify-between gap-3 flex-wrap">
            <div class="min-w-0 flex-1">
              <div class="text-xs text-gray-300">{{ inc.title }}</div>
              <div class="mt-0.5 text-[10px] tracking-widest text-gray-600 uppercase">
                <span :class="severityTone(inc.severity)">{{ inc.severity }}</span>
                · resolved {{ inc.resolved_at ? fmtTime(inc.resolved_at) : '?' }}
                <span v-if="inc.affected_pops?.length" class="text-gray-700 normal-case tracking-normal ml-1">
                  · {{ inc.affected_pops.join(', ') }}
                </span>
              </div>
            </div>
            <button @click="deleteIncident(inc)"
                    class="text-[10px] tracking-widest text-gray-600 hover:text-red-400"
                    title="delete entirely">⨯</button>
          </li>
        </ul>
      </details>
    </div>
  </div>
</template>
