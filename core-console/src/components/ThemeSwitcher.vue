<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useThemeStore, type ThemeMode } from '@/stores/theme'
import { useI18n } from 'vue-i18n'

const theme = useThemeStore()
const { t } = useI18n()

interface Opt {
  mode: ThemeMode
  labelKey: string
  hintKey: string
}

const options: Opt[] = [
  { mode: 'auto',  labelKey: 'theme.auto',  hintKey: 'theme.auto_hint'  },
  { mode: 'light', labelKey: 'theme.light', hintKey: 'theme.light_hint' },
  { mode: 'dark',  labelKey: 'theme.dark',  hintKey: 'theme.dark_hint'  }
]

const open = ref(false)
const root = ref<HTMLElement | null>(null)
let closeTimer: ReturnType<typeof setTimeout> | null = null

function clearCloseTimer() {
  if (closeTimer) { clearTimeout(closeTimer); closeTimer = null }
}
function show() { clearCloseTimer(); open.value = true }
function scheduleHide() {
  clearCloseTimer()
  closeTimer = setTimeout(() => { open.value = false }, 160)
}
function toggle() { open.value = !open.value; clearCloseTimer() }
function pick(m: ThemeMode) { theme.set(m); open.value = false; clearCloseTimer() }

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

const current = computed(() => options.find((o) => o.mode === theme.mode) ?? options[0])
const triggerLabel = computed(() => t(current.value.labelKey))
</script>

<template>
  <div
    ref="root"
    class="relative"
    @mouseenter="show"
    @mouseleave="scheduleHide"
  >
    <!-- Trigger -->
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
      aria-label="Change theme"
    >
      <!-- Icon swaps with resolved theme: sun / moon / auto-split -->
      <svg
        v-if="theme.resolved === 'light'"
        viewBox="0 0 16 16"
        class="w-3.5 h-3.5"
        fill="none" stroke="currentColor" stroke-width="1.3"
        aria-hidden="true"
      >
        <circle cx="8" cy="8" r="3.2" />
        <path d="M8 1v2M8 13v2M1 8h2M13 8h2M3 3l1.4 1.4M11.6 11.6L13 13M3 13l1.4-1.4M11.6 4.4L13 3" />
      </svg>
      <svg
        v-else
        viewBox="0 0 16 16"
        class="w-3.5 h-3.5"
        fill="none" stroke="currentColor" stroke-width="1.3"
        aria-hidden="true"
      >
        <path d="M13.5 9.5A5.5 5.5 0 1 1 6.5 2.5a4.5 4.5 0 0 0 7 7Z" />
      </svg>

      <span class="hidden sm:inline text-[10px] tracking-widest font-mono">{{ triggerLabel }}</span>

      <svg
        viewBox="0 0 10 6"
        class="w-2 h-1.5 transition-transform"
        :class="open ? 'rotate-180' : ''"
        fill="currentColor" aria-hidden="true"
      >
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
        class="absolute right-0 top-full mt-1 min-w-[14rem] z-50
               border border-gray-800 bg-gray-950/95 backdrop-blur-md
               shadow-[0_8px_32px_rgba(0,0,0,0.5)]"
        @mouseenter="show"
        @mouseleave="scheduleHide"
      >
        <div class="px-3 py-1.5 border-b border-gray-800 text-[9px] tracking-[0.3em] uppercase text-gray-600 font-mono flex items-center gap-2">
          <span class="inline-block w-1 h-1 bg-emerald-500"></span>
          <span>// {{ t('theme.heading') }}</span>
          <span class="ml-auto text-gray-700">{{ theme.resolved }}</span>
        </div>

        <ul class="py-1">
          <li v-for="o in options" :key="o.mode">
            <button
              type="button"
              @click="pick(o.mode)"
              role="option"
              :aria-selected="theme.mode === o.mode"
              :class="[
                'w-full flex items-center justify-between px-3 py-2 text-left text-xs font-mono',
                'border-l-2 transition-colors duration-75',
                theme.mode === o.mode
                  ? 'border-emerald-500 bg-gray-900 text-emerald-500'
                  : 'border-transparent text-gray-400 hover:bg-gray-900 hover:text-gray-100 hover:border-gray-700'
              ]"
            >
              <span class="flex flex-col leading-tight gap-0.5">
                <span class="tracking-wider">{{ t(o.labelKey) }}</span>
                <span class="text-[9px] tracking-widest text-gray-600">{{ t(o.hintKey) }}</span>
              </span>
              <span v-if="theme.mode === o.mode" class="text-emerald-500">●</span>
              <span v-else class="text-gray-700">›</span>
            </button>
          </li>
        </ul>

        <div class="px-3 py-1.5 border-t border-gray-800 text-[9px] tracking-widest uppercase text-gray-700 font-mono">
          persisted · localStorage
        </div>
      </div>
    </transition>
  </div>
</template>
