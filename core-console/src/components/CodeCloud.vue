<script setup lang="ts">
// Ambient code-cloud — 8 rows of network-themed terminal lines slowly
// drifting across the hero background. CSS-only animation; each row has
// its own duration + negative delay so the rows stay desynced forever.
//
// Color & blend-mode flip per theme:
//  - dark:  bright text + `mix-blend-mode: screen`  (glows over black bg)
//  - light: darker  text + `mix-blend-mode: multiply` (ink on paper)

interface Row {
  text: string
  color: string   // `text-<light-tone> dark:text-<bright-tone>` pair
  top: string     // CSS top %
  duration: number
  delay: number   // negative seconds
  size: string    // text size class
}

const rows: Row[] = [
  { top: '5%',  duration: 22, delay: -6,  size: 'text-[11px]',
    color: 'text-cyan-700    dark:text-cyan-300',
    text: '2a13:edc0:18::1  IDLE → ESTABLISHED  AS-PATH 64500 i  pref 100  med 100' },
  { top: '14%', duration: 26, delay: -12, size: 'text-xs',
    color: 'text-pink-700    dark:text-pink-400',
    text: 'wg0[pop04]  peer mxhLWWWX64...  828.74 KiB rx  5.12 MiB tx  handshake 23s ago' },
  { top: '23%', duration: 24, delay: -10, size: 'text-[11px]',
    color: 'text-emerald-700 dark:text-emerald-400',
    text: '2606:4700:4700::/48  unicast  via fe80::b6f9:5d03:e83f:cbc on eth0 → AS13335' },
  { top: '32%', duration: 28, delay: -16, size: 'text-[10px]',
    color: 'text-violet-700  dark:text-violet-400',
    text: 'traceroute 1.1.1.1 → 82.40.42.1 (1.5ms) → 103.22.201.127 (0.6ms) → 1.1.1.1 (0.5ms)' },
  { top: '42%', duration: 23, delay: -18, size: 'text-[11px]',
    color: 'text-amber-700   dark:text-amber-300',
    text: 'BIRD 2.17.1 ready · skyline_v6 BGP Established · master6 · 912k routes' },
  { top: '53%', duration: 27, delay: -20, size: 'text-[10px]',
    color: 'text-blue-700    dark:text-blue-300',
    text: 'RPKI cache @ rpki.cloudflare.com  vrps 542127 valid  +88 invalid  refresh 3m' },
  { top: '64%', duration: 25, delay: -8,  size: 'text-xs',
    color: 'text-emerald-700 dark:text-emerald-400',
    text: '[OK] bird table master6 ready · churn 12r/s · age 1h12m · convergence 0.42s' },
  { top: '76%', duration: 24, delay: -14, size: 'text-[10px]',
    color: 'text-fuchsia-700 dark:text-fuchsia-400',
    text: 'ip6 fe80::b6f9:5d03:e83f:cbc%eth0  next-hop · pref-life forever · valid forever' }
]
</script>

<template>
  <div
    class="ncn-code-cloud absolute inset-0 overflow-hidden pointer-events-none
           mix-blend-multiply dark:mix-blend-screen"
    style="
      mask-image: linear-gradient(to bottom, transparent 0%, black 20%, black 80%, transparent 100%);
      -webkit-mask-image: linear-gradient(to bottom, transparent 0%, black 20%, black 80%, transparent 100%);
    "
    aria-hidden="true"
  >
    <div
      v-for="(row, i) in rows"
      :key="i"
      :class="[
        'absolute left-0 right-0 whitespace-nowrap font-mono animate-code-flow',
        'opacity-60 dark:opacity-50',
        row.color,
        row.size
      ]"
      :style="{
        top: row.top,
        animationDuration: row.duration + 's',
        animationDelay: row.delay + 's',
        animationDirection: i % 2 === 0 ? 'normal' : 'reverse'
      }"
    >{{ row.text }}</div>
  </div>
</template>
