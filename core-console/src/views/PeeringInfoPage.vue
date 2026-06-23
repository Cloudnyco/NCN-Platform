<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useLocaleAware } from '@/i18n'
import { useReveal } from '@/composables/useReveal'
import { usePublicNetwork } from '@/composables/usePublicNetwork'

const { t, trackWide, trackMed, caseClass } = useLocaleAware()
const { tm } = useI18n()
const { pops, ixMemberships } = usePublicNetwork()
const { el: revealEl, revealed } = useReveal()

const peeringPolicy = computed(() => tm('peering.policy') as { k: string; v: string }[])
const peerYes = computed(() => tm('peering.peer_yes') as string[])
const peerNo  = computed(() => tm('peering.peer_no')  as string[])
const routingAudit = computed(() => tm('peering.audit') as { label: string; value: string }[])

// Direct peers observed in BIRD, cross-verified against bgp.tools + PeeringDB.
interface PeerRow { asn: number; name: string; cc: string }
const peerASes: PeerRow[] = [
  { asn: 216211, name: 'CYBERVERSE',                        cc: 'US' },
  { asn:  44324, name: 'MoeDove Global',                    cc: 'US' },
  { asn:  14447, name: 'Infinitron Global Communications',  cc: 'US' },
  { asn: 207529, name: 'NyanLoli Network',                  cc: 'GB' },
  { asn: 211575, name: 'RuiNetwork',                        cc: 'CN' },
  { asn: 216299, name: 'LCNetwork',                         cc: 'CN' }
]
</script>

<template>
  <!-- Page header -->
  <section class="relative px-4 sm:px-6 pt-12 sm:pt-20 pb-6 border-b border-gray-900">
    <div class="max-w-5xl mx-auto flex flex-wrap items-end justify-between gap-4">
      <div>
        <div :class="['text-[10px] sm:text-xs text-gray-500 mb-3', trackWide, caseClass]">{{ t('peering.eyebrow') }}</div>
        <h1 class="text-3xl sm:text-5xl font-mono font-bold text-gray-100 tracking-tight text-gradient-flow">{{ t('peering.heading') }}</h1>
      </div>
      <router-link
        to="/peering-apply"
        :class="['group relative px-6 py-3 text-xs border border-emerald-500 text-emerald-500 hover:text-black overflow-hidden transition-colors micro-lift', trackMed, caseClass]"
      >
        <span class="absolute inset-0 bg-emerald-500 -translate-x-full group-hover:translate-x-0 transition-transform"></span>
        <span class="relative">{{ t('hero.cta_primary') }}</span>
      </router-link>
    </div>
  </section>

  <section ref="revealEl" :class="['px-4 sm:px-6 py-12 sm:py-16 reveal', revealed && 'is-visible']">
    <div class="max-w-5xl mx-auto">
      <!-- Interconnects panel -->
      <div class="border border-gray-800 bg-gray-900/40 backdrop-blur">
        <div :class="['px-3 sm:px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest text-gray-600 uppercase flex flex-wrap justify-between items-baseline gap-x-3 gap-y-1', caseClass]">
          <span class="font-mono text-gray-500">$ peering.interconnects</span>
          <span class="text-gray-700 normal-case tracking-normal">{{ pops.length }} PoP · {{ t('peering.pi_meta') }}</span>
        </div>
        <div class="p-3 sm:p-5 font-mono text-[13px] leading-relaxed space-y-6 sm:space-y-7">
          <!-- 1. Policy -->
          <div>
            <div :class="['text-[10px] tracking-widest text-gray-400 uppercase mb-3', caseClass]">{{ t('peering.sub_policy') }}</div>
            <table class="text-xs">
              <tbody>
                <tr v-for="row in peeringPolicy" :key="row.k" class="align-baseline">
                  <td class="text-gray-500 pr-4 py-0.5 whitespace-nowrap">{{ row.k }}</td>
                  <td class="text-emerald-400 py-0.5 break-all">{{ row.v }}</td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- 2. Facilities -->
          <div>
            <div :class="['text-[10px] tracking-widest text-gray-400 uppercase mb-3', caseClass]">{{ t('peering.sub_facilities') }}</div>
            <table class="hidden sm:table text-xs tabular-nums min-w-full">
              <thead :class="['text-[10px] tracking-widest text-gray-500 uppercase', caseClass]">
                <tr class="border-b border-gray-800">
                  <th class="text-left py-1 pr-4">{{ t('peering.fac_th_id') }}</th>
                  <th class="text-left py-1 pr-4">{{ t('peering.fac_th_region') }}</th>
                  <th class="text-left py-1 pr-4">{{ t('peering.fac_th_facility') }}</th>
                  <th class="text-left py-1">{{ t('peering.fac_th_peering') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="p in pops" :key="p.code" class="border-b border-gray-800/40 align-baseline">
                  <td class="py-1.5 pr-4 text-gray-200">{{ p.code }}</td>
                  <td class="py-1.5 pr-4 text-gray-400">{{ p.label }}</td>
                  <td class="py-1.5 pr-4 text-gray-300">{{ p.region }}</td>
                  <td class="py-1.5 text-emerald-500">{{ t('peering.fac_open') }}</td>
                </tr>
              </tbody>
            </table>
            <ul class="sm:hidden divide-y divide-gray-800/40">
              <li v-for="p in pops" :key="p.code" class="py-2 first:pt-1 last:pb-0">
                <div class="flex items-baseline justify-between gap-2">
                  <span class="text-gray-200 font-mono">{{ p.code }}</span>
                  <span class="text-emerald-500 text-[10px] tracking-widest uppercase">{{ t('peering.fac_open') }}</span>
                </div>
                <div class="mt-0.5 text-gray-400">{{ p.label }}</div>
                <div class="text-gray-500 text-[11px] break-words leading-snug" style="overflow-wrap: anywhere;">{{ p.region }}</div>
              </li>
            </ul>
          </div>

          <!-- 3. IX memberships -->
          <div>
            <div :class="['text-[10px] tracking-widest text-gray-400 uppercase mb-3 flex flex-wrap gap-x-3', caseClass]">
              <span>{{ t('peering.sub_ix') }}</span>
              <a class="text-emerald-500 hover:text-emerald-400 normal-case tracking-normal" href="https://www.peeringdb.com/asn/64500" target="_blank" rel="noopener">peeringdb ↗</a>
            </div>
            <div class="overflow-x-auto">
              <table class="text-xs tabular-nums min-w-full">
                <thead :class="['text-[10px] tracking-widest text-gray-500 uppercase', caseClass]">
                  <tr class="border-b border-gray-800">
                    <th class="text-left py-1 pr-4">{{ t('peering.ix_th_exchange') }}</th>
                    <th class="text-left py-1">{{ t('peering.ix_th_speed') }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="ix in ixMemberships" :key="ix.name" class="border-b border-gray-800/40 align-baseline">
                    <td class="py-1.5 pr-4 text-gray-200 break-words" style="overflow-wrap: anywhere;">{{ ix.name }}</td>
                    <td class="py-1.5 text-gray-300">{{ ix.speed }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>

          <!-- 4. Direct peers -->
          <div>
            <div :class="['text-[10px] tracking-widest text-gray-400 uppercase mb-3 flex flex-wrap gap-x-3', caseClass]">
              <span>{{ t('peering.sub_peers') }}</span>
              <a class="text-emerald-500 hover:text-emerald-400 normal-case tracking-normal" href="https://bgp.tools/as/64500" target="_blank" rel="noopener">bgp.tools ↗</a>
            </div>
            <div class="overflow-x-auto">
              <table class="text-xs tabular-nums min-w-full">
                <thead :class="['text-[10px] tracking-widest text-gray-500 uppercase', caseClass]">
                  <tr class="border-b border-gray-800">
                    <th class="text-left py-1 pr-4">{{ t('peering.peer_th_asn') }}</th>
                    <th class="text-left py-1 pr-4">{{ t('peering.peer_th_network') }}</th>
                    <th class="text-left py-1">{{ t('peering.peer_th_reg') }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="p in peerASes" :key="p.asn" class="border-b border-gray-800/40 align-baseline">
                    <td class="py-1.5 pr-4 text-emerald-400">AS{{ p.asn }}</td>
                    <td class="py-1.5 pr-4 text-gray-200 break-words" style="overflow-wrap: anywhere;">{{ p.name }}</td>
                    <td class="py-1.5 text-gray-400">{{ p.cc }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>

          <!-- 5. Yes / No -->
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4 sm:gap-6 pt-2 border-t border-gray-800">
            <div>
              <div :class="['text-[10px] tracking-widest text-emerald-500 uppercase mb-2', caseClass]">{{ t('peering.sub_yes') }}</div>
              <ul class="space-y-0.5">
                <li v-for="(line, i) in peerYes" :key="i" class="text-gray-300">
                  <span class="text-emerald-500 select-none">+</span><span class="ml-2">{{ line }}</span>
                </li>
              </ul>
            </div>
            <div>
              <div :class="['text-[10px] tracking-widest text-red-400 uppercase mb-2', caseClass]">{{ t('peering.sub_no') }}</div>
              <ul class="space-y-0.5">
                <li v-for="(line, i) in peerNo" :key="i" class="text-gray-300">
                  <span class="text-red-400 select-none">-</span><span class="ml-2">{{ line }}</span>
                </li>
              </ul>
            </div>
          </div>
        </div>
      </div>

      <!-- Routing-security audit panel -->
      <div class="mt-6 border border-gray-800 bg-gray-900/40 backdrop-blur">
        <div class="px-3 sm:px-4 py-2 border-b border-gray-800 text-[10px] tracking-widest uppercase flex flex-wrap justify-between items-baseline gap-x-3 gap-y-1">
          <span class="text-gray-500 font-mono">$ ncn-routing --audit</span>
          <span class="text-emerald-500">EXIT=0</span>
        </div>
        <div class="p-3 sm:p-5 font-mono text-[13px] leading-relaxed">
          <ul class="space-y-1.5">
            <li v-for="item in routingAudit" :key="item.label" class="grid grid-cols-[auto_auto_minmax(0,1fr)] gap-x-2 sm:gap-x-4 items-baseline leading-snug">
              <span class="text-emerald-500 shrink-0">[ok]</span>
              <span class="text-gray-200 shrink-0 sm:min-w-[15ch]">{{ item.label }}</span>
              <span class="text-gray-500 break-words" style="overflow-wrap: anywhere;">{{ item.value }}</span>
            </li>
          </ul>
        </div>
      </div>
    </div>
  </section>
</template>
