<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import WorldMap from '@/components/WorldMap.vue'
import LookingGlass from '@/views/LookingGlass.vue'
import { useLocaleAware } from '@/i18n'
import { api, type FleetPublic } from '@/api/client'

const { t, trackWide, caseClass } = useLocaleAware()

// Live PoP feed — passed straight into WorldMap so the map's node list,
// dot positions, arcs, and online/offline colors all derive from the
// public fleet API in one place. Adding a new PoP in backend/fleet.go
// makes it appear here without any template edits.
const nodes = ref<{ id: string; label: string; lat: number; lon: number; ok: boolean }[]>([])
let pollTimer: ReturnType<typeof setInterval> | null = null

// Cache the last-applied shape so we can skip reactive updates when a
// poll returns logically-identical data. Why this matters: every time
// `nodes.value` is assigned a new array reference, the chain of computeds
// downstream of it (popPoints → popMarkers → popClusters → arcs) all
// re-run and produce new array references too. Vue's :key reconciliation
// keeps the same DOM nodes when keys match, BUT mobile browsers still
// take a frame or two to re-bind SMIL <animateMotion> targets after any
// upstream re-render, manifesting as occasional missed animation frames
// or briefly-blank markers. Skipping the no-op assignment removes the
// recompute storm and keeps SMIL animations smooth.
let lastFleetSig = ''
async function loadFleet() {
  try {
    const env = await api.fleetPublic()
    if (env.ok && env.data) {
      const data: FleetPublic = env.data
      const next = data.nodes.map(n => ({
        id:    n.id,
        label: n.label,
        lat:   n.lat,
        lon:   n.lon,
        ok:    n.ok,
      }))
      // Cheap, stable signature — id/lat/lon/label are static per node,
      // only `ok` flips, so concatenating ok-flags by sorted id catches
      // every state-change worth re-rendering for.
      const sig = next
        .slice()
        .sort((a, b) => a.id.localeCompare(b.id))
        .map(n => `${n.id}:${n.ok ? 1 : 0}`)
        .join('|')
      if (sig !== lastFleetSig) {
        lastFleetSig = sig
        nodes.value = next
      }
    }
  } catch { /* ignore — map shows last-known status until next poll */ }
}

onMounted(() => {
  loadFleet()
  pollTimer = setInterval(loadFleet, 15000)
})
onBeforeUnmount(() => { if (pollTimer) clearInterval(pollTimer) })
</script>

<template>
  <!-- Hero strip -->
  <section class="relative px-4 sm:px-6 pt-12 sm:pt-20 pb-6 border-b border-gray-900">
    <div class="max-w-6xl mx-auto">
      <div :class="['text-[10px] sm:text-xs text-gray-500 mb-3', trackWide, caseClass]">
        {{ t('lg.eyebrow') }}
      </div>
      <h1 class="text-3xl sm:text-5xl font-mono font-bold text-gray-100 tracking-tight">
        Looking Glass
      </h1>
      <p class="mt-4 text-sm sm:text-base text-gray-400 max-w-2xl">
        {{ t('lg.intro') }}
      </p>
    </div>
  </section>

  <!-- World map panel -->
  <section class="px-4 sm:px-6 py-6 sm:py-10">
    <div class="max-w-6xl mx-auto">
      <div class="border border-gray-800 bg-gray-900/40 backdrop-blur p-3 sm:p-6">
        <div class="flex items-center justify-between mb-3 sm:mb-4 flex-wrap gap-2">
          <div :class="['text-[10px] tracking-widest text-gray-500', caseClass]">
            <span class="text-emerald-500">●</span> {{ t('lg.map.live') }}
          </div>
          <div class="text-[10px] tracking-widest text-gray-700 font-mono">
            AS64500 · global backbone · v6
          </div>
        </div>
        <WorldMap :nodes="nodes" />
      </div>
    </div>
  </section>

  <!-- The Looking Glass tool itself -->
  <section class="px-4 sm:px-6 pb-16 sm:pb-24">
    <div class="max-w-6xl mx-auto">
      <LookingGlass />
    </div>
  </section>
</template>
