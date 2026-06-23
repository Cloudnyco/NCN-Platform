import { onBeforeUnmount, ref, watch } from 'vue'

/**
 * Animate a numeric value from its previous shown value to the new target.
 * Useful for landing-page stats that "count up" as they're revealed and
 * smoothly transition when the live snapshot polls again.
 *
 * Returns a Ref<number> that drives the display. Pass a Ref<number> as
 * source — when source changes, display tweens to it over `durationMs`.
 *
 * Honors `prefers-reduced-motion` by jumping straight to the target.
 */
export function useCountUp(source: () => number, durationMs = 900) {
  const display = ref(0)
  let rafId: number | null = null
  let startedAt = 0
  let fromValue = 0
  let toValue = 0

  function reduced(): boolean {
    return typeof window !== 'undefined'
      && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
  }

  // Cubic ease-out — fast start, gentle landing on the final value.
  function ease(t: number): number {
    const u = 1 - t
    return 1 - u * u * u
  }

  function step(now: number) {
    const elapsed = now - startedAt
    const t = Math.min(1, elapsed / durationMs)
    display.value = Math.round(fromValue + (toValue - fromValue) * ease(t))
    if (t < 1) {
      rafId = requestAnimationFrame(step)
    } else {
      rafId = null
    }
  }

  watch(
    source,
    (next) => {
      const n = Number.isFinite(next) ? next : 0
      if (reduced()) {
        display.value = n
        return
      }
      fromValue = display.value
      toValue = n
      if (fromValue === toValue) return
      startedAt = performance.now()
      if (rafId !== null) cancelAnimationFrame(rafId)
      rafId = requestAnimationFrame(step)
    },
    { immediate: true }
  )

  onBeforeUnmount(() => { if (rafId !== null) cancelAnimationFrame(rafId) })
  return display
}
