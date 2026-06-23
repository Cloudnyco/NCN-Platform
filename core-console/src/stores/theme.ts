import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'

export type ThemeMode = 'auto' | 'light' | 'dark'
export type ResolvedTheme = 'light' | 'dark'

const STORAGE_KEY = 'ncn:theme'

function readStoredMode(): ThemeMode {
  if (typeof window === 'undefined') return 'auto'
  const v = window.localStorage.getItem(STORAGE_KEY)
  if (v === 'auto' || v === 'light' || v === 'dark') return v
  return 'auto'
}

function systemPrefersDark(): boolean {
  return typeof window !== 'undefined'
    && window.matchMedia?.('(prefers-color-scheme: dark)').matches
}

function applyClass(theme: ResolvedTheme) {
  if (typeof document === 'undefined') return
  const root = document.documentElement
  root.classList.toggle('dark', theme === 'dark')
  root.classList.toggle('light', theme === 'light')
}

export const useThemeStore = defineStore('theme', () => {
  const mode = ref<ThemeMode>(readStoredMode())
  const systemDark = ref(systemPrefersDark())

  // Live-listen for OS theme changes so `auto` keeps reflecting current pref.
  if (typeof window !== 'undefined' && window.matchMedia) {
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const handler = (e: MediaQueryListEvent) => (systemDark.value = e.matches)
    if (mq.addEventListener) mq.addEventListener('change', handler)
    else mq.addListener(handler) // Safari < 14
  }

  const resolved = computed<ResolvedTheme>(() => {
    if (mode.value === 'auto') return systemDark.value ? 'dark' : 'light'
    return mode.value
  })

  function set(next: ThemeMode) {
    mode.value = next
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(STORAGE_KEY, next)
    }
  }

  // Apply on creation + on any change.
  applyClass(resolved.value)
  watch(resolved, applyClass)

  return { mode, resolved, set }
})

/* ------------------------------------------------------------------------- *
 * Synchronous bootstrap — call this BEFORE Vue mounts so the very first
 * paint is already in the correct theme (avoids a flash of dark/light).
 * Used from main.ts after the inline script in index.html.
 * ------------------------------------------------------------------------- */
export function bootstrapTheme(): ResolvedTheme {
  const m = readStoredMode()
  const r: ResolvedTheme = m === 'auto' ? (systemPrefersDark() ? 'dark' : 'light') : m
  applyClass(r)
  return r
}
