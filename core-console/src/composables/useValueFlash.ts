import { onBeforeUnmount, ref, watch } from 'vue'

/**
 * Returns a Ref<boolean> that flips to `true` for `flashMs` ms whenever
 * `source()` changes. Bind it to a CSS class to trigger a "value just
 * updated" highlight pulse on a stat tile / numeric cell.
 *
 * Skips the first invocation so we don't flash everything on mount.
 */
export function useValueFlash(source: () => unknown, flashMs = 700) {
  const flashing = ref(false)
  let timer: ReturnType<typeof setTimeout> | null = null
  let primed = false

  watch(source, () => {
    if (!primed) { primed = true; return }
    flashing.value = true
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => { flashing.value = false }, flashMs)
  })

  onBeforeUnmount(() => { if (timer) clearTimeout(timer) })
  return flashing
}
