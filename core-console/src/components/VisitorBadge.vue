<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, type VisitorInfo } from '@/api/client'
import { useLocaleAware } from '@/i18n'

const { t, trackMed, caseClass } = useLocaleAware()

const info = ref<VisitorInfo | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)

onMounted(async () => {
  try {
    const env = await api.visitor()
    if (env.ok && env.data) {
      info.value = env.data
    } else {
      error.value = env.error ?? 'lookup failed'
    }
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
})

// ISO country code → regional-indicator flag emoji.
function flag(cc?: string) {
  if (!cc || cc.length !== 2) return ''
  const cleaned = cc.toUpperCase()
  if (!/^[A-Z]{2}$/.test(cleaned)) return ''
  const A = 0x1f1e6
  return String.fromCodePoint(A + cleaned.charCodeAt(0) - 65, A + cleaned.charCodeAt(1) - 65)
}

const hasOrigin = computed(() => !!info.value?.asn)
const flagDisplay = computed(() => flag(info.value?.country))
</script>

<template>
  <!-- Bg + border match the SYSTEM ONLINE pill next door so the two badges
       read as one row visually. Theming flips with the root mode; the inner
       BIRD-RIB-style emerald/pink/blue/violet accents are remapped to their
       Tailwind -700 cousins in light mode via :root.light overrides in
       style.css. The .ncn-visitor-badge scope additionally darkens the
       muted gray-600/gray-700 (labels + separators) in light mode so the
       small-text fields clear AA on the now-light pill background. -->
  <div
    class="ncn-visitor-badge inline-flex flex-col items-stretch border border-gray-800 bg-gray-900/50 backdrop-blur px-4 py-2 font-mono text-[10px] sm:text-[11px] leading-tight tabular-nums"
    aria-label="Visitor whois lookup"
  >
    <!-- Status line -->
    <div :class="['flex items-center gap-2 text-gray-500', trackMed, caseClass]">
      <span :class="['w-1.5 h-1.5', loading ? 'bg-amber-400 animate-pulse' : (error ? 'bg-red-500' : 'bg-emerald-500 animate-pulse')]"></span>
      <span>{{ t('visitor.inbound') }}</span>
      <span v-if="loading" class="text-amber-400">{{ t('visitor.resolving') }}</span>
      <span v-else-if="error" class="text-red-500 normal-case tracking-normal">· {{ error }}</span>
      <span v-else-if="!hasOrigin" class="text-gray-600">{{ t('visitor.no_asn') }}</span>
      <span v-else class="text-emerald-500">{{ t('visitor.identified') }}</span>
    </div>

    <!-- BIRD-RIB style entry -->
    <div v-if="info && !loading" class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-gray-400 normal-case">
      <span>
        <span class="text-gray-600">{{ t('visitor.fields.ip') }}</span>
        <span class="text-emerald-400 ml-1 break-all">{{ info.ip }}</span>
      </span>
      <span class="text-gray-700">·</span>
      <span>
        <span class="text-gray-600">{{ t('visitor.fields.asn') }}</span>
        <span class="text-pink-400 ml-1">{{ info.asn || t('visitor.unknown') }}</span>
      </span>
      <span class="text-gray-700">·</span>
      <span class="max-w-[28ch] sm:max-w-[40ch] truncate">
        <span class="text-gray-600">{{ t('visitor.fields.org') }}</span>
        <span class="text-blue-300 ml-1">{{ info.as_org || t('visitor.unknown_holder') }}</span>
      </span>
      <span class="text-gray-700">·</span>
      <span>
        <span class="text-gray-600">{{ t('visitor.fields.cc') }}</span>
        <span class="text-violet-400 ml-1">{{ info.country || '??' }}</span>
        <span class="ml-1">{{ flagDisplay }}</span>
      </span>
      <span v-if="info.prefix" class="text-gray-700">·</span>
      <span v-if="info.prefix" class="text-gray-500">{{ info.prefix }}</span>
    </div>

    <!-- Loading skeleton -->
    <div v-else-if="loading" class="mt-2 text-gray-700 italic normal-case tracking-normal">
      {{ t('visitor.querying') }}
    </div>
  </div>
</template>
