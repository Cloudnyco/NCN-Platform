import { onBeforeUnmount, onMounted, ref } from 'vue'

/**
 * Reveal an element when it scrolls into view. Returns a ref to bind to the
 * target. The trigger fires as the element APPROACHES the viewport from
 * below (within ~30% of the bottom edge), not after it has fully entered —
 * this matters for anchor-click navigation: by the time the smooth-scroll
 * lands the user on a section, its 720ms reveal animation has already
 * finished, so it doesn't "pop up" under their eyes ("突兀").
 *
 * One-shot: we disconnect after the first reveal and don't re-hide on
 * scroll-out.
 *
 * Pair with a CSS transition (opacity + translate-y) keyed on the
 * `is-visible` class for a gentle entrance.
 *
 * Honors `prefers-reduced-motion` by revealing immediately.
 */
export function useReveal() {
  const el = ref<HTMLElement | null>(null)
  const revealed = ref(false)
  let io: IntersectionObserver | null = null

  onMounted(() => {
    const reduced = typeof window !== 'undefined'
      && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
    if (reduced) {
      revealed.value = true
      return
    }

    if (!el.value || typeof IntersectionObserver === 'undefined') {
      revealed.value = true
      return
    }
    // Positive bottom rootMargin extends the IO root 30% past the viewport's
    // bottom edge — the trigger fires while the element is still off-screen
    // but approaching. The previous setup used `-10%`, which DELAYED the
    // trigger until the element was already inside the viewport; combined
    // with a 720ms reveal that meant anchor-scrolls finished BEFORE the
    // animation started, so users saw the section "rise up" after arriving.
    io = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        if (entry.isIntersecting) {
          revealed.value = true
          io?.disconnect()
          io = null
          break
        }
      }
    }, { threshold: 0, rootMargin: '0px 0px 30% 0px' })
    io.observe(el.value)
  })

  onBeforeUnmount(() => { io?.disconnect() })

  return { el, revealed }
}
