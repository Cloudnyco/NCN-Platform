<!--
  Icon.vue — central inline-SVG library for webmail UI.
  All icons share a 24×24 viewBox + currentColor stroke; size via the `size`
  prop or just CSS (.h-4 etc) on the parent. No external deps.
-->
<script setup lang="ts">
import { computed } from 'vue'

interface Props {
  name: string
  size?: number | string
  strokeWidth?: number
}
const props = withDefaults(defineProps<Props>(), {
  size: '1em',
  strokeWidth: 2,
})
const sizeAttr = computed(() =>
  typeof props.size === 'number' ? `${props.size}px` : props.size,
)
</script>

<template>
  <svg
    :width="sizeAttr" :height="sizeAttr"
    viewBox="0 0 24 24" fill="none"
    :stroke-width="strokeWidth" stroke="currentColor"
    stroke-linecap="round" stroke-linejoin="round"
    class="inline-block align-[-0.125em] shrink-0"
    aria-hidden="true"
  >
    <!-- close ✕ -->
    <g v-if="name === 'x'">
      <path d="M6 6 18 18 M18 6 6 18" />
    </g>
    <!-- loader ◌ (spinner) -->
    <g v-else-if="name === 'loader'" class="animate-spin origin-center">
      <path d="M12 3 v3" />
      <path d="M12 18 v3" opacity="0.3" />
      <path d="M3 12 h3" opacity="0.5" />
      <path d="M18 12 h3" opacity="0.7" />
      <path d="m5.6 5.6 2.1 2.1" opacity="0.4" />
      <path d="m16.3 16.3 2.1 2.1" opacity="0.6" />
      <path d="m18.4 5.6-2.1 2.1" opacity="0.8" />
      <path d="m7.7 16.3-2.1 2.1" opacity="0.9" />
    </g>
    <!-- alert-triangle ⚠ -->
    <g v-else-if="name === 'alert'">
      <path d="M12 4 L22 20 L2 20 Z" />
      <path d="M12 10 v5" />
      <circle cx="12" cy="18" r="0.5" fill="currentColor" />
    </g>
    <!-- arrow-right → -->
    <g v-else-if="name === 'arrow-right'">
      <path d="M5 12 h14" />
      <path d="m13 6 6 6-6 6" />
    </g>
    <!-- arrow-left ← -->
    <g v-else-if="name === 'arrow-left'">
      <path d="M19 12 H5" />
      <path d="m11 18-6-6 6-6" />
    </g>
    <!-- arrow-down ↓ -->
    <g v-else-if="name === 'arrow-down'">
      <path d="M12 5 v14" />
      <path d="m6 13 6 6 6-6" />
    </g>
    <!-- dot · (separator) -->
    <g v-else-if="name === 'dot'">
      <circle cx="12" cy="12" r="1.5" fill="currentColor" stroke="none" />
    </g>
    <!-- paperclip 📎 -->
    <g v-else-if="name === 'paperclip'">
      <path d="M21 11.5 11.5 21a5 5 0 1 1-7-7L14 4.5a3.5 3.5 0 1 1 5 5L9.5 19a2 2 0 1 1-3-3L15 7.5" />
    </g>
    <!-- key 🔑 -->
    <g v-else-if="name === 'key'">
      <circle cx="8" cy="15" r="4" />
      <path d="m10.85 12.15 9.4-9.4" />
      <path d="m17 6 3 3" />
      <path d="m14 9 3 3" />
    </g>
    <!-- settings (gear) ⚙ -->
    <g v-else-if="name === 'settings'">
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.6 1.65 1.65 0 0 0 10 3.09V3a2 2 0 0 1 4 0v.09A1.65 1.65 0 0 0 15 4.6a1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09A1.65 1.65 0 0 0 19.4 15Z" />
    </g>
    <!-- shield (admin badge) ⚇ -->
    <g v-else-if="name === 'shield'">
      <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z" />
      <path d="m9 12 2 2 4-4" />
    </g>
    <!-- star ★ -->
    <g v-else-if="name === 'star'">
      <path d="m12 2 3.1 6.3L22 9.3l-5 4.9 1.2 6.9L12 17.8 5.8 21l1.2-6.9-5-4.9 6.9-1Z" />
    </g>
    <!-- eye 👁 -->
    <g v-else-if="name === 'eye'">
      <path d="M2 12s4-7 10-7 10 7 10 7-4 7-10 7-10-7-10-7Z" />
      <circle cx="12" cy="12" r="3" />
    </g>
    <!-- eye-off 🙈 -->
    <g v-else-if="name === 'eye-off'">
      <path d="M9.9 4.2A10.5 10.5 0 0 1 12 4c6 0 10 8 10 8a18 18 0 0 1-3.2 4.2" />
      <path d="M6.6 6.6A18 18 0 0 0 2 12s4 8 10 8a10 10 0 0 0 5.4-1.6" />
      <path d="M9 9a3 3 0 0 0 4.2 4.2" />
      <path d="m2 2 20 20" />
    </g>
    <!-- clipboard (copy) 📋 -->
    <g v-else-if="name === 'clipboard'">
      <rect x="8" y="3" width="8" height="4" rx="1" />
      <path d="M16 5h2a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2h2" />
    </g>
    <!-- save (floppy) 💾 -->
    <g v-else-if="name === 'save'">
      <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2Z" />
      <path d="M7 3v6h10V3" />
      <path d="M7 21v-7h10v7" />
    </g>
    <!-- power ⏻ -->
    <g v-else-if="name === 'power'">
      <path d="M18.4 6.6a9 9 0 1 1-12.8 0" />
      <path d="M12 3 v9" />
    </g>
    <!-- search ⌕ -->
    <g v-else-if="name === 'search'">
      <circle cx="11" cy="11" r="7" />
      <path d="m21 21-4.3-4.3" />
    </g>
    <!-- caps-lock ⇪ -->
    <g v-else-if="name === 'caps'">
      <path d="M5 11 12 4 19 11" />
      <path d="M9 11 v6 H15 v-6" />
      <path d="M9 20 H15" />
    </g>
    <!-- arrow-up-right ↗ (external link) -->
    <g v-else-if="name === 'external'">
      <path d="M7 17 17 7" />
      <path d="M8 7 H17 V16" />
    </g>
    <!-- refresh-cw ↻ -->
    <g v-else-if="name === 'refresh'">
      <path d="M21 12a9 9 0 0 1-9 9 9 9 0 0 1-7.1-3.5" />
      <path d="M3 12a9 9 0 0 1 9-9 9 9 0 0 1 7.1 3.5" />
      <path d="M21 3 v6 H15" />
      <path d="M3 21 v-6 H9" />
    </g>
    <!-- corner-up-left ↩ (reply) -->
    <g v-else-if="name === 'reply'">
      <path d="m9 14-5-5 5-5" />
      <path d="M4 9 H15 a5 5 0 0 1 5 5 v6" />
    </g>
    <!-- corner-down-right (move into) -->
    <g v-else-if="name === 'move'">
      <path d="M4 4 v6 a4 4 0 0 0 4 4 H20" />
      <path d="m15 9 5 5-5 5" />
    </g>
    <!-- download ↡ -->
    <g v-else-if="name === 'download'">
      <path d="M12 3 v12" />
      <path d="m6 9 6 6 6-6" />
      <path d="M4 20 H20" />
    </g>
    <!-- trash (delete) -->
    <g v-else-if="name === 'trash'">
      <path d="M4 7 H20" />
      <path d="M10 11 v6" />
      <path d="M14 11 v6" />
      <path d="M5 7 6 20 a1 1 0 0 0 1 1 H17 a1 1 0 0 0 1-1 L19 7" />
      <path d="M9 7 V4 a1 1 0 0 1 1-1 H14 a1 1 0 0 1 1 1 V7" />
    </g>
    <!-- check ✓ -->
    <g v-else-if="name === 'check'">
      <path d="m4 12 5 5 11-11" />
    </g>
    <!-- plus + -->
    <g v-else-if="name === 'plus'">
      <path d="M12 5 v14" />
      <path d="M5 12 H19" />
    </g>
    <!-- send (paper plane, replaces ▶ for primary submits) -->
    <g v-else-if="name === 'send'">
      <path d="M22 2 L11 13" />
      <path d="M22 2 15 22 11 13 2 9 Z" />
    </g>
    <!-- fallback: unknown name → small bullet so layout doesn't collapse -->
    <g v-else>
      <circle cx="12" cy="12" r="2" fill="currentColor" stroke="none" />
    </g>
  </svg>
</template>
