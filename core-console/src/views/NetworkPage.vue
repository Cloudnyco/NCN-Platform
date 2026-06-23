<script setup lang="ts">
import { useLocaleAware } from '@/i18n'
import { useReveal } from '@/composables/useReveal'
import { useCardGlow } from '@/composables/useCardGlow'
import { usePublicNetwork } from '@/composables/usePublicNetwork'

const { t, trackWide, caseClass } = useLocaleAware()
const { onCardGlow } = useCardGlow()
const { el: revealEl, revealed } = useReveal()
const { fleetReady, nodeFor, fmtRoutes, popCardBorder, pops } = usePublicNetwork()
</script>

<template>
  <!-- Page header -->
  <section class="relative px-4 sm:px-6 pt-12 sm:pt-20 pb-6 border-b border-gray-900">
    <div class="max-w-6xl mx-auto">
      <div :class="['text-[10px] sm:text-xs text-gray-500 mb-3', trackWide, caseClass]">{{ t('network.eyebrow') }}</div>
      <h1 class="text-3xl sm:text-5xl font-mono font-bold text-gray-100 tracking-tight">{{ t('network.heading') }}</h1>
    </div>
  </section>

  <!-- PoP grid. (No world map here — it's already the hero centerpiece on /
       and the core of /lg; a third copy was redundant.) -->
  <section ref="revealEl" :class="['px-4 sm:px-6 pt-10 pb-16 sm:pt-12 sm:pb-24 reveal', revealed && 'is-visible']">
    <div class="max-w-6xl mx-auto grid grid-cols-1 md:grid-cols-3 gap-4" @mousemove="onCardGlow">
      <div
        v-for="(pop, i) in pops"
        :key="i"
        :class="['ncn-glow border bg-gray-900/40 backdrop-blur p-6 group transition-colors', popCardBorder(pop.code)]"
      >
        <div class="flex items-start justify-between gap-3">
          <div class="min-w-0">
            <div class="inline-block font-mono text-[11px] tracking-[0.2em] uppercase text-emerald-400/90 border border-gray-800 bg-gray-950/40 px-2 py-0.5 mb-2">{{ pop.code }}</div>
            <div class="text-xl text-gray-100 leading-tight">{{ pop.label }}</div>
            <div class="text-sm text-gray-500 mt-1 break-words" style="overflow-wrap: anywhere;">{{ pop.region }}</div>
          </div>
          <div :class="['flex items-center gap-2 text-[10px] tracking-widest', caseClass]">
            <template v-if="nodeFor(pop.code)?.ok">
              <span class="w-2 h-2 bg-emerald-500 animate-pulse"></span>
              <span class="text-emerald-500">{{ t('network.online') }}</span>
            </template>
            <template v-else-if="fleetReady">
              <span class="w-2 h-2 bg-red-500"></span>
              <span class="text-red-500">{{ t('network.offline') }}</span>
            </template>
            <template v-else>
              <span class="w-2 h-2 bg-gray-600 animate-pulse"></span>
              <span class="text-gray-600">···</span>
            </template>
          </div>
        </div>

        <div v-if="nodeFor(pop.code)?.ok" class="mt-4 pt-4 border-t border-gray-800 grid grid-cols-2 gap-x-4 gap-y-2 text-xs font-mono">
          <div>
            <div :class="['text-[10px] text-gray-600 tracking-widest', caseClass]">{{ t('network.bgp') }}</div>
            <div class="text-gray-200 tabular-nums mt-0.5">
              <span class="text-emerald-400">{{ nodeFor(pop.code)!.bgp_sessions }}</span><span class="text-gray-700">/{{ nodeFor(pop.code)!.bgp_total }}</span>
            </div>
          </div>
          <div>
            <div :class="['text-[10px] text-gray-600 tracking-widest', caseClass]">{{ t('network.routes') }} v6</div>
            <div class="text-emerald-400 tabular-nums mt-0.5">{{ fmtRoutes(nodeFor(pop.code)!.routes_v6) }}</div>
          </div>
          <div>
            <div :class="['text-[10px] text-gray-600 tracking-widest', caseClass]">{{ t('network.tunnels') }}</div>
            <div class="text-gray-300 tabular-nums mt-0.5">
              {{ nodeFor(pop.code)!.wg_count + nodeFor(pop.code)!.tunnel_count }}
              <span class="text-[10px] text-gray-700 ml-1">({{ nodeFor(pop.code)!.wg_count }} wg · {{ nodeFor(pop.code)!.tunnel_count }} gre/vxlan)</span>
            </div>
          </div>
          <div>
            <div :class="['text-[10px] text-gray-600 tracking-widest', caseClass]">{{ t('network.latency') }}</div>
            <div class="text-gray-300 tabular-nums mt-0.5">
              {{ nodeFor(pop.code)!.anchor_ms > 0 ? nodeFor(pop.code)!.anchor_ms.toFixed(1) + ' ms' : '—' }}
            </div>
          </div>
        </div>

        <div v-if="nodeFor(pop.code)" class="mt-3 text-[9px] tracking-widest text-gray-700 flex items-center gap-1.5">
          <span class="inline-block w-1 h-1 bg-emerald-500"></span>
          <span :class="caseClass">{{ t('network.live') }}</span>
        </div>
      </div>
    </div>
  </section>
</template>
