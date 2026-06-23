// Card hover glow — a delegated mousemove handler for a card grid. Finds the
// hovered `.ncn-glow` card and writes the pointer position (px, relative to
// that card) into --cx/--cy, which the card's ::before radial reads (see
// `.ncn-glow` in style.css). One listener per grid instead of one per card.
// Bind with `@mousemove="onCardGlow"` on the grid container.
export function useCardGlow() {
  function onCardGlow(e: MouseEvent) {
    const card = (e.target as HTMLElement | null)?.closest?.('.ncn-glow') as HTMLElement | null
    if (!card) return
    const r = card.getBoundingClientRect()
    card.style.setProperty('--cx', `${e.clientX - r.left}px`)
    card.style.setProperty('--cy', `${e.clientY - r.top}px`)
  }
  return { onCardGlow }
}
