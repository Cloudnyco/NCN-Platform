<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { LOCALES, persistLocale, type Locale } from '@/i18n'

const { locale } = useI18n()

const open = ref(false)
const root = ref<HTMLElement | null>(null)
let closeTimer: ReturnType<typeof setTimeout> | null = null

function clearCloseTimer() {
  if (closeTimer) { clearTimeout(closeTimer); closeTimer = null }
}

function show() {
  clearCloseTimer()
  open.value = true
}

// Slight close-delay so brief mouse trips between trigger and panel don't kill it.
function scheduleHide() {
  clearCloseTimer()
  closeTimer = setTimeout(() => { open.value = false }, 160)
}

function toggle() {
  open.value = !open.value
  clearCloseTimer()
}

function pick(code: Locale) {
  if (locale.value !== code) persistLocale(code)
  open.value = false
  clearCloseTimer()
}

function onDocClick(e: MouseEvent) {
  if (!root.value) return
  if (!root.value.contains(e.target as Node)) open.value = false
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') open.value = false
}

onMounted(() => {
  document.addEventListener('click', onDocClick)
  document.addEventListener('keydown', onKeydown)
})
onBeforeUnmount(() => {
  document.removeEventListener('click', onDocClick)
  document.removeEventListener('keydown', onKeydown)
  clearCloseTimer()
})

const current = computed(() => LOCALES.find((l) => l.code === locale.value) ?? LOCALES[0])
</script>

<template>
  <div
    ref="root"
    class="relative"
    @mouseenter="show"
    @mouseleave="scheduleHide"
  >
    <!-- Trigger: globe icon + current locale short code -->
    <button
      type="button"
      @click="toggle"
      :class="[
        'h-8 px-2.5 flex items-center gap-1.5 border transition-colors',
        open
          ? 'border-emerald-500 text-emerald-500'
          : 'border-gray-700 text-gray-400 hover:border-emerald-500 hover:text-emerald-500'
      ]"
      aria-haspopup="listbox"
      :aria-expanded="open"
      aria-label="Change language"
    >
      <svg viewBox="0 0 16 16" class="w-3.5 h-3.5" fill="none" stroke="currentColor" stroke-width="1.25" aria-hidden="true">
        <circle cx="8" cy="8" r="6.5"></circle>
        <ellipse cx="8" cy="8" rx="3" ry="6.5"></ellipse>
        <path d="M1.5 8h13"></path>
        <path d="M2.7 4.5h10.6M2.7 11.5h10.6"></path>
      </svg>
      <span class="hidden sm:inline text-[10px] tracking-widest font-mono">{{ current.label }}</span>
      <svg viewBox="0 0 10 6" class="w-2 h-1.5 transition-transform" :class="open ? 'rotate-180' : ''" fill="currentColor" aria-hidden="true">
        <path d="M0 0 L5 6 L10 0 Z" />
      </svg>
    </button>

    <!-- Dropdown panel -->
    <transition
      enter-active-class="transition duration-100 ease-out"
      enter-from-class="opacity-0 -translate-y-1"
      enter-to-class="opacity-100 translate-y-0"
      leave-active-class="transition duration-75 ease-in"
      leave-from-class="opacity-100 translate-y-0"
      leave-to-class="opacity-0 -translate-y-1"
    >
      <div
        v-show="open"
        role="listbox"
        class="absolute right-0 top-full mt-1 min-w-[12rem] z-50
               border border-gray-800 bg-gray-950/95 backdrop-blur-md
               shadow-[0_8px_32px_rgba(0,0,0,0.5)]"
        @mouseenter="show"
        @mouseleave="scheduleHide"
      >
        <!-- Header -->
        <div class="px-3 py-1.5 border-b border-gray-800 text-[9px] tracking-[0.3em] uppercase text-gray-600 font-mono flex items-center gap-2">
          <span class="inline-block w-1 h-1 bg-emerald-500"></span>
          <span>// LANGUAGE</span>
        </div>

        <!-- Options -->
        <ul class="py-1">
          <li v-for="l in LOCALES" :key="l.code">
            <button
              type="button"
              @click="pick(l.code)"
              role="option"
              :aria-selected="locale === l.code"
              :class="[
                'w-full flex items-center justify-between px-3 py-2 text-left text-xs font-mono',
                'border-l-2 transition-colors duration-75',
                locale === l.code
                  ? 'border-emerald-500 bg-gray-900 text-emerald-500'
                  : 'border-transparent text-gray-400 hover:bg-gray-900 hover:text-gray-100 hover:border-gray-700'
              ]"
            >
              <span class="flex flex-col leading-tight gap-0.5">
                <span class="tracking-wider">{{ l.label }}</span>
                <span class="text-[9px] tracking-widest text-gray-600">{{ l.code }}</span>
              </span>
              <span v-if="locale === l.code" class="text-emerald-500">●</span>
              <span v-else class="text-gray-700">›</span>
            </button>
          </li>
        </ul>

        <!-- Footer hint -->
        <div class="px-3 py-1.5 border-t border-gray-800 text-[9px] tracking-widest uppercase text-gray-700 font-mono">
          persisted · localStorage
        </div>
      </div>
    </transition>
  </div>
</template>
