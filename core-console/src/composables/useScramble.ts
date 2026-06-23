import { onMounted, ref } from 'vue'

/**
 * Reveal a string by cycling through random characters first, then settling
 * left-to-right into the target. Honors `prefers-reduced-motion`.
 */
export function useScramble(target: string, durationMs = 900) {
  const display = ref(target)

  onMounted(() => {
    const reduced = typeof window !== 'undefined'
      && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
    if (reduced) {
      display.value = target
      return
    }

    const pool = '0123456789ABCDEFGHJKLMNPQRSTUVWXYZ-./<>?#@*+'
    const steps = 14
    const tickMs = Math.max(28, Math.floor(durationMs / steps))
    let i = 0
    display.value = ''

    const tick = () => {
      if (i >= steps) {
        display.value = target
        return
      }
      const progress = i / (steps - 1)
      const settled = Math.floor(target.length * progress)
      const randTail = target.length - settled
      let out = target.slice(0, settled)
      for (let j = 0; j < randTail; j++) {
        const src = target[settled + j]
        // Preserve spaces, dots, slashes — only scramble alphanumerics.
        out += /[A-Za-z0-9]/.test(src)
          ? pool[Math.floor(Math.random() * pool.length)]
          : src
      }
      display.value = out
      i++
      window.setTimeout(tick, tickMs)
    }
    tick()
  })

  return display
}
