<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import WorldMap from '@/components/WorldMap.vue'
import VisitorBadge from '@/components/VisitorBadge.vue'
import { useScramble } from '@/composables/useScramble'
import { useCountUp } from '@/composables/useCountUp'
import { useValueFlash } from '@/composables/useValueFlash'
import { useCardGlow } from '@/composables/useCardGlow'
import { usePublicNetwork } from '@/composables/usePublicNetwork'
import { useLocaleAware } from '@/i18n'

const { t, trackWide, trackMed, caseClass, isCJK } = useLocaleAware()
const { fleet, fmtRoutes } = usePublicNetwork()
const { onCardGlow } = useCardGlow()

// ---------------------------------------------------------------------------
// Live stat tiles — computed from the fleet snapshot. Until the first poll
// lands they render "·" placeholders.
// ---------------------------------------------------------------------------
interface Stat { valueGetter: () => string; labelKey: string; hintKey: string; accent: string }
const stats: Stat[] = [
  { valueGetter: () => fleet.value ? `${fleet.value.pops_online}/${fleet.value.pops_total}` : '·/·', labelKey: 'stats.pops',     hintKey: 'stats.pops_hint',     accent: 'text-blue-400' },
  { valueGetter: () => '64500',                                                                       labelKey: 'stats.asn',      hintKey: 'stats.asn_hint',      accent: 'text-pink-400' },
  { valueGetter: () => fleet.value ? fmtRoutes(fleet.value.routes_v6) : '·',                           labelKey: 'stats.routes',   hintKey: 'stats.routes_hint',   accent: 'text-emerald-400' },
  { valueGetter: () => fleet.value ? String(fleet.value.bgp_sessions) : '·',                           labelKey: 'stats.sessions', hintKey: 'stats.sessions_hint', accent: 'text-violet-400' }
]

// Only the static ASN tile scrambles; live values tween/flash instead.
const asnReveal = useScramble('64500', 900)
const routesV6CountUp = useCountUp(() => fleet.value?.routes_v6 ?? 0, 1400)
const flashPops     = useValueFlash(() => fleet.value?.pops_online)
const flashRoutes   = useValueFlash(() => fleet.value?.routes_v6)
const flashSessions = useValueFlash(() => fleet.value?.bgp_sessions)
function statFlashClass(i: number): string {
  if (i === 0 && flashPops.value)     return 'value-flash'
  if (i === 2 && flashRoutes.value)   return 'value-flash'
  if (i === 3 && flashSessions.value) return 'value-flash'
  return ''
}
function statDisplayLive(i: number): string {
  if (i === 1) return asnReveal.value
  if (i === 2) return fmtRoutes(routesV6CountUp.value)
  return stats[i].valueGetter()
}

// Subtle live UTC clock in the hero so it doesn't feel like a static brochure.
const now = ref(new Date())
let timer: ReturnType<typeof setInterval> | null = null
const utcLabel = computed(() => now.value.toISOString().replace('T', ' ').slice(0, 19) + 'Z')

// ---------------------------------------------------------------------------
// Hero headline — typewriter reveal (character-by-character, blinking caret).
// font-mono reserves equal advance width per glyph so growth doesn't reflow.
// ---------------------------------------------------------------------------
const headlineA = computed(() => t('hero.headline_a'))
const headlineB = computed(() => t('hero.headline_b'))
const typedA = ref('')
const typedB = ref('')
type HeadlinePhase = 'pending' | 'typingA' | 'pauseAfterA' | 'typingB' | 'done'
const headlinePhase = ref<HeadlinePhase>('pending')
let typeTimer: ReturnType<typeof setTimeout> | null = null
let typeEpoch = 0

const HEADLINE_LINE_A_TARGET_MS = 550
const HEADLINE_LINE_B_TARGET_MS = 650
const HEADLINE_CHAR_MIN_MS = 28
const HEADLINE_CHAR_MAX_MS = 180
const HEADLINE_JITTER_PCT  = 0.22
const HEADLINE_LINE_PAUSE  = 220

function headlineCharDelay(textLen: number, targetMs: number): number {
  if (textLen <= 0) return targetMs
  const base = Math.max(HEADLINE_CHAR_MIN_MS, Math.min(HEADLINE_CHAR_MAX_MS, targetMs / textLen))
  return base * (1 + (Math.random() * 2 - 1) * HEADLINE_JITTER_PCT)
}

function startHeadlineTyping() {
  if (typeTimer) { clearTimeout(typeTimer); typeTimer = null }
  typeEpoch++
  const myEpoch = typeEpoch
  const fullA = headlineA.value
  const fullB = headlineB.value
  if (typeof window !== 'undefined' && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) {
    typedA.value = fullA; typedB.value = fullB; headlinePhase.value = 'done'; return
  }
  typedA.value = ''; typedB.value = ''; headlinePhase.value = 'typingA'
  let idx = 0
  const tickA = () => {
    if (myEpoch !== typeEpoch) return
    idx++; typedA.value = fullA.slice(0, idx)
    if (idx >= fullA.length) {
      headlinePhase.value = 'pauseAfterA'
      typeTimer = setTimeout(() => { if (myEpoch !== typeEpoch) return; headlinePhase.value = 'typingB'; idx = 0; tickB() }, HEADLINE_LINE_PAUSE)
      return
    }
    typeTimer = setTimeout(tickA, headlineCharDelay(fullA.length, HEADLINE_LINE_A_TARGET_MS))
  }
  const tickB = () => {
    if (myEpoch !== typeEpoch) return
    idx++; typedB.value = fullB.slice(0, idx)
    if (idx >= fullB.length) { headlinePhase.value = 'done'; return }
    typeTimer = setTimeout(tickB, headlineCharDelay(fullB.length, HEADLINE_LINE_B_TARGET_MS))
  }
  tickA()
}
watch([headlineA, headlineB], () => startHeadlineTyping())
const caretOnA = computed(() => headlinePhase.value === 'pending' || headlinePhase.value === 'typingA' || headlinePhase.value === 'pauseAfterA')
const caretOnB = computed(() => headlinePhase.value === 'typingB' || headlinePhase.value === 'done')

// Hero cursor spotlight — mousemove sets two CSS vars on the hero element,
// rAF-throttled so bursts coalesce into one paint frame.
const heroEl = ref<HTMLElement | null>(null)
let mousePending = false
let lastMouseX = 0, lastMouseY = 0
function onHeroMouseMove(e: MouseEvent) {
  if (!heroEl.value) return
  lastMouseX = e.clientX; lastMouseY = e.clientY
  if (mousePending) return
  mousePending = true
  requestAnimationFrame(() => {
    mousePending = false
    if (!heroEl.value) return
    const r = heroEl.value.getBoundingClientRect()
    heroEl.value.style.setProperty('--mx', (((lastMouseX - r.left) / r.width) * 100).toFixed(1) + '%')
    heroEl.value.style.setProperty('--my', (((lastMouseY - r.top)  / r.height) * 100).toFixed(1) + '%')
  })
}

// The boot splash covers the page during mount, so first-paint animations
// would play (and finish) unseen behind it. Gate the hero typewriter on the
// splash's reveal signal: run once #app is actually visible. If the splash is
// already gone (client-side nav, or no splash) the flag is set → run now; a
// 2.5s fallback guarantees the headline never stays empty if the signal is
// missed (e.g. the splash failsafe path).
function whenRevealed(cb: () => void) {
  if (typeof window === 'undefined') { cb(); return }
  const w = window as unknown as { __ncnRevealed?: boolean }
  if (w.__ncnRevealed) { cb(); return }
  let ran = false
  const run = () => { if (ran) return; ran = true; cb() }
  window.addEventListener('ncn:revealed', run, { once: true })
  setTimeout(run, 2500)
}

onMounted(() => {
  timer = setInterval(() => (now.value = new Date()), 1000)
  heroEl.value?.addEventListener('mousemove', onHeroMouseMove, { passive: true })
  whenRevealed(startHeadlineTyping)
})
onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
  if (typeTimer) clearTimeout(typeTimer)
  heroEl.value?.removeEventListener('mousemove', onHeroMouseMove)
})
</script>

<template>
  <!-- ============ HERO ============
       The landing page is now a pure entrance: headline + the live anycast
       map + the live stat band. The former Mission / Network / Peering
       sections were promoted to their own pages (/about, /network, /peering)
       reachable from the top nav. -->
  <section
    ref="heroEl"
    class="relative px-4 sm:px-6 pt-16 sm:pt-24 pb-16 overflow-hidden"
  >
    <div class="ncn-spotlight" aria-hidden="true"></div>

    <div class="relative max-w-3xl mx-auto text-center">
      <!-- Status pill + visitor whois badge -->
      <div class="flex flex-wrap items-stretch justify-center gap-3 mb-8">
        <div :class="['inline-flex items-center gap-2 px-3 py-2 border border-gray-800 bg-gray-900/50 backdrop-blur text-[10px] text-gray-400', trackMed, caseClass]">
          <span class="w-1.5 h-1.5 bg-emerald-500 animate-pulse"></span>
          <span>{{ t('hero.status_online') }}</span>
          <span class="text-gray-600">·</span>
          <span class="text-gray-500 tabular-nums">{{ utcLabel }}</span>
        </div>
        <VisitorBadge />
      </div>

      <div :class="['text-xs sm:text-sm text-gray-500 mb-5', trackWide, caseClass]">{{ t('hero.eyebrow') }}</div>

      <h1
        :aria-label="`${headlineA} ${headlineB}`"
        :class="[
          'font-mono font-bold leading-[1.06] tracking-tight animate-glow-pulse',
          isCJK ? 'text-4xl sm:text-6xl lg:text-7xl' : 'text-4xl sm:text-5xl lg:text-6xl'
        ]"
      >
        <span class="block text-gray-100" aria-hidden="true"
        >{{ typedA }}<span v-show="caretOnA" class="ncn-caret"></span></span>
        <span class="block" aria-hidden="true"><span
          class="bg-gradient-to-r from-gray-100 via-blue-500 via-pink-500 via-emerald-500 to-gray-100 bg-clip-text text-transparent animate-gradient-x"
          style="background-size: 200% 100%;"
        >{{ typedB }}</span><span v-show="caretOnB" class="ncn-caret"></span></span>
      </h1>

      <p class="mt-6 text-sm sm:text-base text-gray-400 max-w-2xl mx-auto leading-relaxed" v-html="t('hero.sub')"></p>

      <div class="mt-8 flex flex-wrap gap-3 justify-center">
        <router-link
          to="/peering-apply"
          :class="['group relative px-6 py-3 text-xs border border-emerald-500 text-emerald-500 hover:text-black overflow-hidden transition-colors micro-lift', trackMed, caseClass]"
        >
          <span class="absolute inset-0 bg-emerald-500 -translate-x-full group-hover:translate-x-0 transition-transform"></span>
          <span class="relative">{{ t('hero.cta_primary') }}</span>
        </router-link>
        <router-link
          to="/network"
          :class="['px-6 py-3 text-xs border border-gray-700 text-gray-300 hover:border-white hover:text-white transition-colors', trackMed, caseClass]"
        >{{ t('hero.cta_secondary') }}</router-link>
      </div>
    </div>

    <!-- Live anycast map + connected stat band -->
    <div class="relative max-w-5xl mx-auto mt-10 sm:mt-14">
      <div class="ncn-map-frame relative border border-gray-800/70 overflow-hidden">
        <WorldMap :nodes="fleet?.nodes ?? []" />
      </div>

      <div class="relative grid grid-cols-2 md:grid-cols-4 border-x border-b border-gray-800/70 bg-gray-900/50 backdrop-blur" @mousemove="onCardGlow">
        <div
          v-for="(s, i) in stats"
          :key="i"
          class="ncn-glow group relative px-4 py-4 sm:px-5 sm:py-5 border-gray-800/70
                 [&:nth-child(even)]:border-l md:[&:not(:first-child)]:border-l
                 [&:nth-child(n+3)]:border-t md:[&:nth-child(n+3)]:border-t-0"
        >
          <div class="absolute inset-x-0 -top-px h-px bg-gradient-to-r from-transparent via-emerald-500/50 to-transparent opacity-0 group-hover:opacity-100 transition-opacity"></div>
          <div :class="['text-[10px] text-gray-500 mb-1.5', trackMed, caseClass]">{{ t(s.labelKey) }}</div>
          <div :class="['text-2xl sm:text-3xl font-mono font-bold tabular-nums', s.accent, statFlashClass(i)]">{{ statDisplayLive(i) }}</div>
          <div :class="['text-[10px] text-gray-600 mt-1.5 tracking-widest truncate', caseClass]">{{ t(s.hintKey) }}</div>
        </div>
      </div>
    </div>
  </section>
</template>
