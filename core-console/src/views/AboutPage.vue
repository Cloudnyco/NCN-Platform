<script setup lang="ts">
import { useLocaleAware } from '@/i18n'
import { useReveal } from '@/composables/useReveal'
import { useCardGlow } from '@/composables/useCardGlow'

const { t, trackWide, caseClass } = useLocaleAware()
const { onCardGlow } = useCardGlow()
const { el: revealEl, revealed } = useReveal()

interface Card {
  id: 'engineered' | 'peering' | 'resources'
  titleKey: string
  textKey: string
  borderColor: string
  iconColor: string
}
const missionCards: Card[] = [
  { id: 'engineered', titleKey: 'mission.cards.engineered.title', textKey: 'mission.cards.engineered.text', borderColor: 'hover:border-blue-500/40',    iconColor: 'text-blue-400' },
  { id: 'peering',    titleKey: 'mission.cards.peering.title',    textKey: 'mission.cards.peering.text',    borderColor: 'hover:border-pink-500/40',    iconColor: 'text-pink-400' },
  { id: 'resources',  titleKey: 'mission.cards.resources.title',  textKey: 'mission.cards.resources.text',  borderColor: 'hover:border-emerald-500/40', iconColor: 'text-emerald-400' }
]
</script>

<template>
  <!-- Page header -->
  <section class="relative px-4 sm:px-6 pt-12 sm:pt-20 pb-6 border-b border-gray-900">
    <div class="max-w-5xl mx-auto">
      <div :class="['text-[10px] sm:text-xs text-gray-500 mb-3', trackWide, caseClass]">{{ t('mission.eyebrow') }}</div>
      <h1 class="text-3xl sm:text-5xl font-mono font-bold text-gray-100 tracking-tight">{{ t('mission.heading') }}</h1>
    </div>
  </section>

  <!-- How-we-operate cards -->
  <section ref="revealEl" :class="['px-4 sm:px-6 py-12 sm:py-16 reveal', revealed && 'is-visible']">
    <div class="max-w-5xl mx-auto grid grid-cols-1 md:grid-cols-3 gap-4" @mousemove="onCardGlow">
      <div
        v-for="(c, i) in missionCards"
        :key="i"
        :class="['ncn-glow border border-gray-800 bg-gray-900/40 backdrop-blur p-6 transition-colors', c.borderColor]"
      >
        <div :class="['mb-4 inline-flex items-center justify-center w-10 h-10 border border-gray-800 bg-gray-950/50', c.iconColor]">
          <svg v-if="c.id === 'engineered'" viewBox="0 0 24 24" class="w-5 h-5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <rect x="4" y="4" width="16" height="16" rx="1"/><path d="M8 9l2.5 3L8 15"/><path d="M13 15h3"/>
          </svg>
          <svg v-else-if="c.id === 'peering'" viewBox="0 0 24 24" class="w-5 h-5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <circle cx="6" cy="6" r="2.5"/><circle cx="18" cy="18" r="2.5"/><path d="M8.5 6H15a3 3 0 0 1 3 3v6.5"/><path d="M15.5 18H9a3 3 0 0 1-3-3V8.5"/>
          </svg>
          <svg v-else viewBox="0 0 24 24" class="w-5 h-5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M4 5.5A1.5 1.5 0 0 1 5.5 4H12v15H5.5A1.5 1.5 0 0 0 4 20.5z"/><path d="M20 5.5A1.5 1.5 0 0 0 18.5 4H12v15h6.5a1.5 1.5 0 0 1 1.5 1.5z"/>
          </svg>
        </div>
        <h3 class="text-base text-gray-100 tracking-wide mb-2">{{ t(c.titleKey) }}</h3>
        <p class="text-sm text-gray-400 leading-relaxed">{{ t(c.textKey) }}</p>
      </div>
    </div>
  </section>
</template>
