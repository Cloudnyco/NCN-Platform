// clipboard.ts — small shim around navigator.clipboard.writeText with a
// fallback for older browsers / non-secure contexts (textarea select +
// execCommand). Extracted from src/views/admin/Security.vue when the
// page started to share this helper across multiple sections (role
// mailbox recovery + API token reveal); putting it in a util avoids
// duplication when more components grow a copy button.

export async function copyToClipboard(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
    return
  } catch {
    // Fallback path: works in HTTP-served (non-secure) admin contexts
    // and on Safari < 13.1 where navigator.clipboard is gated. The
    // textarea+execCommand pattern is broadly supported as a fallback.
    const ta = document.createElement('textarea')
    ta.value = text
    // Hide it off-screen so the select() doesn't blow away the user's
    // current viewport scroll position.
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.left = '-9999px'
    document.body.appendChild(ta)
    ta.select()
    try {
      document.execCommand('copy')
    } finally {
      document.body.removeChild(ta)
    }
  }
}
