<script setup lang="ts">
// StatusPage — public availability for the NCN backbone.
//
// Renders INSIDE PublicLayout (App.vue wraps every public route), so the
// site nav, themed background, footer, and the global ThemeSwitcher all come
// for free — this page adds no chrome and NO theme toggle of its own. Its
// light/dark palette follows the global `:root.light` / `:root.dark` class
// that the shared theme store sets (see src/style.css).
//
// Three unauthenticated feeds, polled every 30s:
//   GET /api/v1/status/summary     — components + 90-day uptime history
//   GET /api/v1/fleet/public       — live PoP detail (BGP, routes, RTT)
//   GET /api/v1/incidents/public   — last 30 days of incidents
//
// Signature element: the 90-day uptime strip (one tick per UTC day, EKG
// style). No i18n (English, matching the original) and deliberately no Vue
// <Transition>: motion is CSS keyframes, transform-led, resting state visible
// (reduced-motion safe). No TS-only template syntax (blank-mount trap) — PoP
// detail uses fnode()?.x optional chaining.

import { ref, computed, onMounted, onUnmounted } from 'vue'
import {
  api,
  type FleetPublicNode,
  type IncidentPublic,
  type StatusComponent,
  type StatusDay,
  type SLATargetView,
} from '@/api/client'

const components = ref<StatusComponent[]>([])
const fleetNodes = ref<FleetPublicNode[]>([])
const incidents = ref<IncidentPublic[]>([])
const slaTargets = ref<SLATargetView[]>([])
const loading = ref(true)
const err = ref<string | null>(null)
const lastRefresh = ref<Date | null>(null)

async function refresh() {
  err.value = null
  try {
    const [s, f, i, sla] = await Promise.all([
      api.statusSummary(),
      api.fleetPublic(),
      api.incidentsPublic(),
      api.statusSLA(),
    ])
    if (s.ok) components.value = s.data?.components ?? []
    if (f.ok) fleetNodes.value = f.data?.nodes ?? []
    if (i.ok) incidents.value = i.data ?? []
    if (sla.ok) slaTargets.value = sla.data?.targets ?? []
    lastRefresh.value = new Date()
  } catch (e: unknown) {
    err.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

// Responsive bar window — fewer day-ticks on narrow screens.
const barWindow = ref(90)
function syncBarWindow() {
  const w = window.innerWidth
  barWindow.value = w < 480 ? 30 : w < 768 ? 60 : 90
}

let pollTimer: number | undefined
onMounted(() => {
  syncBarWindow()
  window.addEventListener('resize', syncBarWindow)
  refresh()
  pollTimer = window.setInterval(refresh, 30_000)
})
onUnmounted(() => {
  if (pollTimer !== undefined) window.clearInterval(pollTimer)
  window.removeEventListener('resize', syncBarWindow)
})

// ── Derived state ───────────────────────────────────────────────────────────
const openIncidents = computed(() => incidents.value.filter(i => i.status !== 'resolved'))
const downComponents = computed(() => components.value.filter(c => c.last_status === 0))

interface Banner { level: 'ok' | 'degraded' | 'incident'; headline: string; sub: string }
const banner = computed<Banner>(() => {
  const total = components.value.length
  const up = components.value.filter(c => c.last_status === 1).length
  const sub = total ? `${up} of ${total} components operational` : 'awaiting first sample'
  if (openIncidents.value.length > 0) {
    const n = openIncidents.value.length
    return { level: 'incident', headline: n === 1 ? 'Active incident' : `${n} active incidents`, sub }
  }
  if (downComponents.value.length > 0) {
    const n = downComponents.value.length
    return { level: 'degraded', headline: n === 1 ? 'Partial outage' : 'Degraded service', sub }
  }
  return { level: 'ok', headline: 'All systems operational', sub }
})

const overallUptime = computed(() => {
  const withData = components.value.filter(c => c.days.some(d => d.total > 0))
  if (!withData.length) return null
  return withData.reduce((a, c) => a + c.uptime, 0) / withData.length
})

interface Group { category: string; items: StatusComponent[] }
const groups = computed<Group[]>(() => {
  const seen: string[] = []
  const map = new Map<string, StatusComponent[]>()
  for (const c of components.value) {
    if (!map.has(c.category)) { map.set(c.category, []); seen.push(c.category) }
    map.get(c.category)!.push(c)
  }
  return seen.map(category => ({ category, items: map.get(category)! }))
})

const fleetById = computed(() => {
  const m = new Map<string, FleetPublicNode>()
  for (const n of fleetNodes.value) m.set(n.id, n)
  return m
})

const expanded = ref<Set<string>>(new Set())
function toggle(name: string) {
  const s = new Set(expanded.value)
  s.has(name) ? s.delete(name) : s.add(name)
  expanded.value = s
}

// ── Visual helpers ──────────────────────────────────────────────────────────
type BarState = 'up' | 'down' | 'partial' | 'nodata'
function barState(d: StatusDay): BarState {
  if (d.total === 0) return 'nodata'
  if (d.down === 0) return 'up'
  if (d.up === 0) return 'down'
  return 'partial'
}
function visibleDays(c: StatusComponent): StatusDay[] {
  return c.days.slice(-barWindow.value)
}
function barTitle(d: StatusDay): string {
  if (d.total === 0) return `${d.day} · no data`
  const pct = ((d.up / d.total) * 100).toFixed(d.down ? 2 : 0)
  return `${d.day} · ${pct}% · ${d.up} up / ${d.down} down`
}
function uptimePct(frac: number): string {
  const p = frac * 100
  return (p >= 99.995 ? 100 : p).toFixed(2)
}
function uptimeTone(frac: number): string {
  const p = frac * 100
  if (p >= 99.9) return 't-up'
  if (p >= 99)   return 't-teal'
  if (p >= 95)   return 't-warn'
  return 't-down'
}
function statusWord(c: StatusComponent): string {
  if (c.last_status === 1) return 'Operational'
  if (c.last_status === 0) return 'Down'
  return 'Unknown'
}
function dotClass(c: StatusComponent): string {
  if (c.type === 'pop') {
    for (const inc of openIncidents.value) {
      if (inc.affected_pops?.includes(c.name)) return 'd-warn'
    }
  }
  if (c.last_status === 1) return 'd-up'
  if (c.last_status === 0) return 'd-down'
  return 'd-idle'
}
function fmtLatency(c: StatusComponent): string {
  if (c.last_status !== 1 || c.last_latency_ms <= 0) return '—'
  return c.last_latency_ms < 10 ? `${c.last_latency_ms.toFixed(1)} ms` : `${Math.round(c.last_latency_ms)} ms`
}
function popLabel(name: string): string {
  return fleetById.value.get(name)?.label ?? ''
}
// fnode — rich fleet record for a PoP; a real method so the template uses ?.
// on a runtime call, never a TS non-null assertion (blank-mount trap).
function fnode(name: string): FleetPublicNode | undefined {
  return fleetById.value.get(name)
}

// ── Time formatting ─────────────────────────────────────────────────────────
function fmtTime(iso: string): string {
  return new Date(iso).toLocaleString('en-GB', {
    year: 'numeric', month: 'short', day: 'numeric',
    hour: '2-digit', minute: '2-digit', hour12: false,
    timeZone: 'UTC', timeZoneName: 'short',
  })
}
function fmtAgo(d: Date | null): string {
  if (!d) return '—'
  const s = Math.floor((Date.now() - d.getTime()) / 1000)
  if (s < 60) return `${s}s ago`
  return `${Math.floor(s / 60)}m ago`
}
function sevClass(s: string): string {
  if (s === 'critical') return 't-down'
  if (s === 'major') return 't-warn'
  return 't-dim'
}
function incStatusClass(s: string): string {
  switch (s) {
    case 'investigating': return 't-down'
    case 'identified':    return 't-warn'
    case 'monitoring':    return 't-teal'
    case 'resolved':      return 't-up'
    default:              return 't-dim'
  }
}
</script>

<template>
  <div class="status-page">
    <!-- slim page heading (no brand / no theme toggle — PublicLayout owns those) -->
    <div class="page-head">
      <div>
        <h1 class="ph-title">Service Status</h1>
        <p class="ph-sub">backbone availability · {{ components.length || '—' }} components · 90-day history</p>
      </div>
      <div class="ph-meta">updated {{ fmtAgo(lastRefresh) }}<span class="faint"> · auto 30s</span></div>
    </div>

    <!-- Hero banner -->
    <section class="hero rise" :class="`hero-${banner.level}`">
      <div class="hero-pulse"><span class="ring"></span><span class="core"></span></div>
      <div class="hero-text">
        <div class="hero-headline">{{ banner.headline }}</div>
        <div class="hero-sub">{{ banner.sub }}</div>
      </div>
      <div v-if="overallUptime !== null" class="hero-uptime">
        <div class="hu-val" :class="uptimeTone(overallUptime)">{{ uptimePct(overallUptime) }}<span class="hu-pct">%</span></div>
        <div class="hu-cap">90-day uptime</div>
      </div>
    </section>

    <div v-if="loading" class="muted center pad">loading status…</div>
    <div v-if="err" class="errbox">⨯ {{ err }}</div>

    <!-- Component groups -->
    <section v-for="(g, gi) in groups" :key="g.category" class="group rise" :style="{ animationDelay: `${60 + gi * 50}ms` }">
      <h2 class="group-title">{{ g.category }}</h2>
      <div class="cards">
        <div v-for="c in g.items" :key="c.name" class="card"
             :class="{ clickable: c.type === 'pop', open: expanded.has(c.name) }"
             @click="c.type === 'pop' && toggle(c.name)">
          <div class="card-top">
            <div class="ident">
              <span class="dot" :class="dotClass(c)"></span>
              <span class="name">{{ c.name }}</span>
              <span v-if="c.type === 'pop' && popLabel(c.name)" class="loc">{{ popLabel(c.name) }}</span>
              <span v-else-if="c.url" class="loc">{{ c.url.replace(/^https?:\/\//, '').replace(/\/$/, '') }}</span>
            </div>
            <div class="metrics">
              <span class="lat">{{ fmtLatency(c) }}</span>
              <span class="status-word" :class="dotClass(c).replace('d-', 't-')">{{ statusWord(c) }}</span>
            </div>
          </div>

          <div class="strip">
            <span v-for="(d, i) in visibleDays(c)" :key="i"
                  class="bar" :class="`b-${barState(d)}`"
                  :style="{ animationDelay: `${i * 6}ms` }"
                  :title="barTitle(d)"></span>
          </div>
          <div class="strip-meta">
            <span class="faint">{{ barWindow }} days ago</span>
            <span class="up-pct" :class="uptimeTone(c.uptime)">{{ uptimePct(c.uptime) }}% uptime</span>
            <span class="faint">today</span>
          </div>

          <div v-if="c.type === 'pop' && expanded.has(c.name) && fnode(c.name)" class="detail">
            <div class="d-cell">
              <div class="d-k">BGP sessions</div>
              <div class="d-v" :class="fnode(c.name)?.bgp_sessions === fnode(c.name)?.bgp_total ? 't-up' : 't-warn'">
                {{ fnode(c.name)?.bgp_sessions }}/{{ fnode(c.name)?.bgp_total }}
              </div>
            </div>
            <div class="d-cell">
              <div class="d-k">routes v6</div>
              <div class="d-v">{{ fnode(c.name)?.routes_v6.toLocaleString() }}</div>
            </div>
            <div class="d-cell">
              <div class="d-k">anchor RTT</div>
              <div class="d-v">{{ fnode(c.name)?.anchor_ms.toFixed(2) }} ms</div>
            </div>
            <div class="d-cell">
              <div class="d-k">tunnels · wg</div>
              <div class="d-v">{{ fnode(c.name)?.tunnel_count }} · {{ fnode(c.name)?.wg_count }}</div>
            </div>
          </div>
          <div v-if="c.type === 'pop'" class="expand-hint faint">{{ expanded.has(c.name) ? '▲ hide detail' : '▾ detail' }}</div>
        </div>
      </div>
    </section>

    <!-- SLA — per-target availability/loss/latency from each PoP -->
    <section v-if="slaTargets.length" class="group rise" :style="{ animationDelay: '240ms' }">
      <h2 class="group-title">SLA · last 30 days</h2>
      <div v-for="t in slaTargets" :key="t.name" class="sla-target">
        <div class="sla-head">
          <span class="sla-name">{{ t.name }}</span>
          <span class="faint">{{ t.target }} · SLO {{ t.slo_pct }}%<template v-if="t.rtt_budget_ms"> · ≤{{ t.rtt_budget_ms }}ms</template></span>
        </div>
        <div v-if="!t.pops.length" class="empty">No samples yet.</div>
        <div v-else class="sla-grid">
          <div v-for="p in t.pops" :key="p.pop" class="sla-cell" :class="p.meets_slo ? 'sla-ok' : 'sla-bad'">
            <div class="sla-pop">{{ p.pop }}</div>
            <div class="sla-avail">{{ p.avail_pct }}%</div>
            <div class="sla-sub faint">loss {{ p.loss_pct }}% · {{ p.mean_rtt_ms }}ms</div>
          </div>
        </div>
      </div>
    </section>

    <!-- Incidents -->
    <section class="group rise" :style="{ animationDelay: '260ms' }">
      <h2 class="group-title">Incidents · last 30 days</h2>
      <div v-if="!incidents.length" class="empty">No incidents reported in the last 30 days.</div>
      <ul v-else class="inc-list">
        <li v-for="inc in incidents" :key="inc.id" class="inc">
          <div class="inc-head">
            <span class="inc-title">{{ inc.title }}</span>
            <span class="chip" :class="sevClass(inc.severity)">{{ inc.severity }}</span>
            <span class="inc-status" :class="incStatusClass(inc.status)">{{ inc.status }}</span>
          </div>
          <div class="inc-meta faint">
            opened {{ fmtTime(inc.created_at) }}
            <span v-if="inc.resolved_at"> · resolved {{ fmtTime(inc.resolved_at) }}</span>
            <span v-if="inc.affected_pops?.length" class="t-warn"> · affects {{ inc.affected_pops.join(', ') }}</span>
          </div>
          <p class="inc-body">{{ inc.body }}</p>
          <div v-if="inc.updates && inc.updates.length" class="inc-updates">
            <div v-for="(u, i) in inc.updates" :key="i" class="inc-update">
              <span class="faint">{{ fmtTime(u.ts) }}<span v-if="u.status" :class="incStatusClass(u.status)"> · {{ u.status }}</span></span>
              <div class="iu-msg">{{ u.message }}</div>
            </div>
          </div>
        </li>
      </ul>
    </section>

    <div class="utc-note faint">all times UTC</div>
  </div>
</template>

<style scoped>
/* Status palette. Defaults are the DARK values (site's primary look); the
   light overrides key off the global `:root.light` class the shared theme
   store sets — so this page follows the nav's ThemeSwitcher, no local toggle. */
.status-page {
  /* Reuse the site's documented gray scale (style.css :root.dark/.light) so
     text + surfaces inherit the same per-theme AAA contrast as the rest of
     the admin UI, and auto-flip with the global theme. Semantic mapping per
     the style.css comments: g-200 primary text, g-400 secondary, g-500
     muted/labels, g-700 markers, g-800 borders, g-900 card surface. */
  --panel: rgb(var(--g-900));
  --panel-2: rgb(var(--g-800));
  --border: rgb(var(--g-800));
  --border-strong: rgb(var(--g-700));
  --text: rgb(var(--g-200));
  --dim: rgb(var(--g-400));
  --faint: rgb(var(--g-500));
  --nodata: rgb(var(--g-700));
  /* status accents — dark defaults; light variants overridden below */
  --up: #34d399;
  --teal: #2dd4bf;
  --down: #f87171;
  --warn: #fbbf24;
  --glow-ok: 52,211,153;
  --glow-degraded: 251,191,36;
  --glow-incident: 248,113,113;

  max-width: 880px;
  margin: 0 auto;
  padding: 30px 20px 8px;
  font-family: ui-monospace, 'SFMono-Regular', 'JetBrains Mono', Menlo, Consolas, monospace;
  color: var(--text);
}
:global(:root.light) .status-page {
  /* Only the status accents need a light variant (darker = legible on the
     near-white card surface). Text/surfaces/borders all come from --g-* and
     flip automatically, so there's nothing else to override here. */
  --up: #047857;   /* emerald-700 */
  --teal: #0f766e;
  --down: #b91c1c; /* red-700 */
  --warn: #b45309; /* amber-700 */
}
/* Light cards are pure white on an off-white page (g-900 on g-950); the
   design system separates them via shadow, not borders/translucency. */
:global(:root.light) .status-page .card,
:global(:root.light) .status-page .hero,
:global(:root.light) .status-page .inc {
  box-shadow: 0 1px 2px rgba(24,24,27,0.05), 0 4px 18px rgba(24,24,27,0.06);
  backdrop-filter: none;
}

/* slim heading */
.page-head { display: flex; align-items: flex-end; justify-content: space-between; gap: 14px; flex-wrap: wrap; margin-bottom: 18px; }
.ph-title { font-family: 'Outfit', ui-sans-serif, system-ui, sans-serif; font-weight: 700; font-size: 22px; letter-spacing: -0.01em; margin: 0; color: var(--text); }
.ph-sub { margin: 4px 0 0; font-size: 10.5px; letter-spacing: 0.14em; text-transform: uppercase; color: var(--faint); }
.ph-meta { font-size: 10px; letter-spacing: 0.13em; text-transform: uppercase; color: var(--dim); text-align: right; }

/* Hero */
.hero {
  padding: 20px 22px; border-radius: 14px; display: flex; align-items: center; gap: 18px;
  background: linear-gradient(180deg, var(--panel-2), var(--panel));
  border: 1px solid var(--border); position: relative; overflow: hidden;
}
.hero::after { content: ''; position: absolute; left: 0; top: 0; bottom: 0; width: 3px; }
.hero-ok::after { background: var(--up); } .hero-degraded::after { background: var(--warn); } .hero-incident::after { background: var(--down); }
.hero-pulse { position: relative; width: 16px; height: 16px; flex: none; }
.hero-pulse .core { position: absolute; inset: 4px; border-radius: 50%; }
.hero-pulse .ring { position: absolute; inset: 0; border-radius: 50%; opacity: 0.5; }
.hero-ok .core { background: var(--up); } .hero-ok .ring { background: var(--up); animation: ping 2.4s cubic-bezier(0,0,0.2,1) infinite; }
.hero-degraded .core { background: var(--warn); } .hero-degraded .ring { background: var(--warn); animation: ping 1.6s cubic-bezier(0,0,0.2,1) infinite; }
.hero-incident .core { background: var(--down); } .hero-incident .ring { background: var(--down); animation: ping 1.2s cubic-bezier(0,0,0.2,1) infinite; }
.hero-text { flex: 1; min-width: 0; }
.hero-headline { font-family: 'Outfit', ui-sans-serif, system-ui, sans-serif; font-weight: 600; font-size: 22px; letter-spacing: -0.01em; }
.hero-sub { margin-top: 3px; font-size: 12px; color: var(--dim); letter-spacing: 0.03em; }
.hero-uptime { text-align: right; flex: none; }
.hu-val { font-family: 'Outfit', sans-serif; font-weight: 700; font-size: 26px; line-height: 1; }
.hu-pct { font-size: 15px; margin-left: 1px; opacity: 0.7; }
.hu-cap { margin-top: 4px; font-size: 9.5px; letter-spacing: 0.16em; text-transform: uppercase; color: var(--faint); }

/* Groups & cards */
.group { margin-top: 28px; }
.group-title { font-size: 10.5px; letter-spacing: 0.22em; text-transform: uppercase; color: var(--faint); margin: 0 0 12px; }
.cards { display: flex; flex-direction: column; gap: 10px; }
.card {
  background: var(--panel); border: 1px solid var(--border); border-radius: 12px;
  padding: 15px 17px; transition: border-color 0.18s ease, background 0.18s ease;
}
.card.clickable { cursor: pointer; }
.card.clickable:hover { border-color: var(--border-strong); background: var(--panel-2); }
.card.open { border-color: var(--border-strong); }
.card-top { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; }
.ident { display: flex; align-items: baseline; gap: 9px; min-width: 0; flex-wrap: wrap; }
.dot { width: 8px; height: 8px; border-radius: 50%; flex: none; align-self: center; }
.name { font-weight: 600; font-size: 14px; color: var(--text); }
.loc { font-size: 10.5px; letter-spacing: 0.1em; text-transform: uppercase; color: var(--faint); }
.metrics { display: flex; align-items: baseline; gap: 12px; flex: none; }
.lat { font-size: 11.5px; color: var(--dim); }
.status-word { font-size: 10px; letter-spacing: 0.14em; text-transform: uppercase; }

.strip { display: flex; gap: 2px; height: 34px; margin-top: 13px; align-items: stretch; }
.bar { flex: 1 1 0; min-width: 0; border-radius: 1.5px; background: var(--nodata); transform-origin: bottom; animation: grow 0.4s ease both; transition: filter 0.12s ease, transform 0.12s ease; }
.bar:hover { filter: brightness(1.35); transform: scaleY(1.06); }
.b-up { background: var(--up); } .b-down { background: var(--down); } .b-partial { background: linear-gradient(180deg, var(--warn) 60%, var(--down)); } .b-nodata { background: var(--nodata); }
.strip-meta { display: flex; align-items: center; justify-content: space-between; margin-top: 7px; font-size: 9.5px; letter-spacing: 0.1em; text-transform: uppercase; }
.up-pct { font-weight: 600; letter-spacing: 0.08em; }

.detail { margin-top: 14px; padding-top: 13px; border-top: 1px dashed var(--border); display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; }
.d-k { font-size: 9px; letter-spacing: 0.13em; text-transform: uppercase; color: var(--faint); }
.d-v { font-size: 13px; margin-top: 3px; color: var(--text); }
.expand-hint { margin-top: 10px; font-size: 9px; letter-spacing: 0.16em; text-transform: uppercase; }

/* SLA */
.sla-target { background: var(--panel); border: 1px solid var(--border); border-radius: 12px; padding: 14px 16px; margin-bottom: 12px; }
.sla-head { display: flex; align-items: baseline; gap: 10px; flex-wrap: wrap; margin-bottom: 11px; }
.sla-name { font-weight: 600; font-size: 14px; letter-spacing: 0.02em; }
.sla-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(120px, 1fr)); gap: 10px; }
.sla-cell { border: 1px solid var(--border); border-left-width: 3px; border-radius: 9px; padding: 9px 11px; }
.sla-cell.sla-ok { border-left-color: #10b981; }
.sla-cell.sla-bad { border-left-color: #f43f5e; }
.sla-pop { font-size: 11px; letter-spacing: 0.04em; color: var(--dim); }
.sla-avail { font-size: 19px; font-weight: 600; letter-spacing: 0.02em; margin-top: 2px; }
.sla-sub { font-size: 10px; margin-top: 2px; }

/* Incidents */
.empty { padding: 26px; text-align: center; font-size: 13px; color: var(--faint); border: 1px dashed var(--border); border-radius: 12px; }
.inc-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 10px; }
.inc { background: var(--panel); border: 1px solid var(--border); border-radius: 12px; padding: 15px 17px; }
.inc-head { display: flex; align-items: center; gap: 9px; flex-wrap: wrap; }
.inc-title { font-weight: 600; font-size: 14px; }
.chip { border: 1px solid currentColor; border-radius: 5px; padding: 1px 7px; font-size: 9.5px; letter-spacing: 0.12em; text-transform: uppercase; }
.inc-status { font-size: 10px; letter-spacing: 0.14em; text-transform: uppercase; }
.inc-meta { margin-top: 7px; font-size: 10px; letter-spacing: 0.06em; }
.inc-body { margin: 9px 0 0; font-size: 13px; color: var(--dim); line-height: 1.6; white-space: pre-wrap; }
.inc-updates { margin-top: 12px; border-left: 2px solid var(--border); padding-left: 13px; display: flex; flex-direction: column; gap: 9px; }
.inc-update { font-size: 9.5px; letter-spacing: 0.06em; }
.iu-msg { margin-top: 3px; font-size: 12.5px; color: var(--dim); line-height: 1.5; white-space: pre-wrap; }

.utc-note { margin-top: 26px; text-align: center; font-size: 9.5px; letter-spacing: 0.16em; text-transform: uppercase; }

/* Tones */
.muted { color: var(--dim); } .center { text-align: center; } .pad { padding: 30px 0; }
.faint { color: var(--faint); }
.t-up { color: var(--up); } .t-teal { color: var(--teal); } .t-down { color: var(--down); }
.t-warn { color: var(--warn); } .t-dim { color: var(--dim); } .t-idle { color: var(--faint); }
.errbox { margin-top: 18px; padding: 13px 16px; border: 1px solid var(--down); border-radius: 10px; color: var(--down); background: rgba(248,113,113,0.08); font-size: 13px; }

/* dots */
.d-up { background: var(--up); box-shadow: 0 0 8px rgba(var(--glow-ok),0.7); }
.d-down { background: var(--down); box-shadow: 0 0 8px rgba(var(--glow-incident),0.7); animation: blink 1.4s ease-in-out infinite; }
.d-warn { background: var(--warn); box-shadow: 0 0 8px rgba(var(--glow-degraded),0.7); animation: blink 1.8s ease-in-out infinite; }
.d-idle { background: var(--faint); }

/* Motion (reduced-motion safe: resting state is the visible state) */
@keyframes ping { 0% { transform: scale(1); opacity: 0.5; } 80%,100% { transform: scale(2.6); opacity: 0; } }
@keyframes blink { 0%,100% { opacity: 1; } 50% { opacity: 0.45; } }
@keyframes grow { from { transform: scaleY(0.12); } to { transform: scaleY(1); } }
.rise { animation: rise 0.5s cubic-bezier(0.22,1,0.36,1) both; }
@keyframes rise { from { transform: translateY(10px); opacity: 0; } to { transform: translateY(0); opacity: 1; } }
@media (prefers-reduced-motion: reduce) {
  .rise, .bar, .hero-pulse .ring, .d-down, .d-warn { animation: none; }
}
@media (max-width: 560px) {
  .detail { grid-template-columns: repeat(2, 1fr); }
  .hero-headline { font-size: 19px; }
}
</style>
