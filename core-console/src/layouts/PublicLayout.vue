<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useSessionStore } from '@/stores/session'
import { useLocaleAware } from '@/i18n'
import LanguageSwitcher from '@/components/LanguageSwitcher.vue'
import ThemeSwitcher from '@/components/ThemeSwitcher.vue'

const session = useSessionStore()
const route   = useRoute()
const { t, caseClass } = useLocaleAware()

// Admin lives on its own subdomain so the operator cookie is scoped away
// from the public host. From here (example.com / www.example.com) the
// Operator + Console links cross hosts; on dev (localhost / IP) we stay
// on the same host.
const ADMIN_HOST = 'admin.example.com'
const hereHost = typeof window !== 'undefined' ? window.location.host : ''
const isPublicHost = hereHost === 'example.com' || hereHost === 'www.example.com'

const operatorHref = computed(() => isPublicHost ? `https://${ADMIN_HOST}/login` : '/login')
const consoleHref  = computed(() => isPublicHost ? `https://${ADMIN_HOST}/admin` : '/admin')

// Mobile nav drawer state
const mobileNavOpen = ref(false)
function toggleMobileNav() { mobileNavOpen.value = !mobileNavOpen.value }
function closeMobileNav()  { mobileNavOpen.value = false }
watch(() => route.fullPath, closeMobileNav)

// Spotlight: write CSS variables directly to <html> via rAF throttle so we
// don't go through Vue reactivity 100+ times per second. Skipped on touch
// devices (no useful pointer to track anyway).
let lastX = 0, lastY = 0, rafPending = false
function onMove(e: MouseEvent) {
  lastX = e.clientX
  lastY = e.clientY
  if (rafPending) return
  rafPending = true
  requestAnimationFrame(() => {
    document.documentElement.style.setProperty('--mx', lastX + 'px')
    document.documentElement.style.setProperty('--my', lastY + 'px')
    rafPending = false
  })
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') closeMobileNav()
}

const isTouch = typeof window !== 'undefined'
  && (window.matchMedia?.('(hover: none)').matches || 'ontouchstart' in window)

onMounted(() => {
  if (!isTouch) window.addEventListener('mousemove', onMove, { passive: true })
  window.addEventListener('keydown', onKey)
})
onBeforeUnmount(() => {
  window.removeEventListener('mousemove', onMove)
  window.removeEventListener('keydown', onKey)
})

// Top nav is page-level only — every item goes to another page. The old
// in-page section anchors (Mission / Network / Peering段落) were removed:
// mixing "scroll this page" and "go to a page" in one row read as
// inconsistent ("怪"), and this is a multi-page app, not a one-pager. The
// home page's mission/network sections are reached by scrolling; the hero's
// "VIEW NETWORK" CTA still does the in-page jump. Peering now points at the
// real application page (/peering-apply), a useful destination.
interface NavLink { to: string; labelKey: string }
const navLinks: NavLink[] = [
  { to: '/about',   labelKey: 'nav.mission' },
  { to: '/network', labelKey: 'nav.network' },
  { to: '/peering', labelKey: 'nav.peering' },
  { to: '/lg',      labelKey: 'lg.nav' },
  { to: '/status',  labelKey: 'nav.status' }
]
</script>

<template>
  <div class="min-h-screen w-full bg-gray-950 text-gray-300 font-mono relative overflow-x-hidden">
    <!-- Background layers -->
    <div class="pointer-events-none fixed inset-0 -z-10">
      <!-- Dotted grid with center vignette + slow drift.
           Alpha & color follow theme tokens. -->
      <div
        class="absolute inset-0 opacity-60 animate-grid-drift"
        style="
          background-image: radial-gradient(circle, rgb(var(--c-dot) / var(--c-dot-alpha)) 1px, transparent 1px);
          background-size: 26px 26px;
          mask-image: radial-gradient(ellipse at center, black 0%, transparent 75%);
          -webkit-mask-image: radial-gradient(ellipse at center, black 0%, transparent 75%);
        "
      ></div>

      <!-- Color blobs — denser tint in light mode so they actually show on paper.
           Use blur-2xl on mobile (cheaper) and blur-3xl on desktop. -->
      <div class="absolute -top-32 -left-24 w-[20rem] h-[20rem] sm:w-[28rem] sm:h-[28rem] rounded-full bg-blue-500/25    dark:bg-blue-500/10    blur-2xl sm:blur-3xl animate-float-a"></div>
      <div class="absolute top-1/3 -right-24 w-[22rem] h-[22rem] sm:w-[32rem] sm:h-[32rem] rounded-full bg-pink-500/25   dark:bg-pink-500/10    blur-2xl sm:blur-3xl animate-float-b"></div>
      <div class="absolute bottom-0 left-1/4 w-[18rem] h-[18rem] sm:w-[26rem] sm:h-[26rem] rounded-full bg-emerald-500/25 dark:bg-emerald-500/10 blur-2xl sm:blur-3xl animate-float-c"></div>

      <!-- Mouse-following spotlight — alpha is theme-token driven. -->
      <div
        class="absolute inset-0 transition-opacity duration-300"
        style="
          background: radial-gradient(600px circle at var(--mx, 50%) var(--my, 50%),
            rgb(16 185 129 / var(--c-spotlight-alpha)),
            transparent 40%);
        "
      ></div>

      <!-- Vertical scanline -->
      <div class="absolute inset-x-0 h-px bg-emerald-500/20 animate-scanline" style="box-shadow: 0 0 12px rgba(16,185,129,0.4);"></div>
    </div>

    <!-- Sticky top nav -->
    <header class="sticky top-0 z-40 backdrop-blur-md bg-gray-950/70 border-b border-gray-900">
      <div class="max-w-7xl mx-auto h-14 flex items-center justify-between px-3 sm:px-6 gap-2">
        <router-link to="/" class="flex items-center gap-2 sm:gap-3 group min-w-0">
          <!-- Logo SVG — /public/logo.svg. ViewBox cropped to 341 203 882 639
               so the brand mark fills its slot tightly (the original canvas
               had ~20% transparent padding all around). New aspect is
               ~1.38:1, so at h-9 mobile the logo is ~50px wide — that
               trade gives roughly 30% more visual presence than the
               pre-crop version while still leaving the wordmark room to
               breathe. Desktop bumps to h-12 (48 tall × 66 wide) where
               there's no horizontal-space competition. -->
          <img src="/logo.svg" alt="NCN" class="h-9 sm:h-12 w-auto shrink-0 select-none" draggable="false" />
          <!-- Wordmark: very tight tracking on mobile so "Acme Cloud
               Network" survives next to the larger logo + right-side
               controls (theme, language, hamburger). Subtitle "NCN ·
               AS64500" stays sm+ only. Truncate is a safety net for
               viewports under ~340px where the wordmark gracefully
               clips to "Acme Cloud Net…". -->
          <div class="leading-tight min-w-0">
            <div class="text-[10px] tracking-[0.04em] sm:text-sm sm:tracking-[0.2em] text-gray-100 uppercase animate-glitch-rgb truncate">
              Acme Net
            </div>
            <div class="hidden sm:block text-[9px] tracking-[0.3em] text-gray-600 uppercase">
              NCN<span class="mx-1">·</span>AS64500
            </div>
          </div>
        </router-link>

        <nav :class="['hidden md:flex items-center gap-5 text-[11px] text-gray-400', caseClass, 'tracking-widest']">
          <router-link
            v-for="link in navLinks"
            :key="link.to"
            :to="link.to"
            :class="['hover:text-emerald-500 transition-colors', $route.path === link.to ? 'text-emerald-500' : '']"
          >{{ t(link.labelKey) }}</router-link>
        </nav>

        <div class="flex items-center gap-1.5 sm:gap-2">
          <ThemeSwitcher />
          <LanguageSwitcher />
          <a
            v-if="!session.authenticated"
            :href="operatorHref"
            :class="['hidden sm:inline-block px-3 py-1.5 border border-gray-700 hover:border-emerald-500 text-[10px] text-gray-300 hover:text-emerald-500 transition-colors tracking-widest', caseClass]"
          >{{ t('nav.operator') }}</a>
          <a
            v-else
            :href="consoleHref"
            :class="['hidden sm:inline-block px-3 py-1.5 border border-emerald-500 bg-emerald-500/10 text-[10px] text-emerald-500 hover:bg-emerald-500 hover:text-black transition-colors tracking-widest', caseClass]"
          >{{ t('nav.console') }} ▸</a>

          <!-- Mobile hamburger — exposes the otherwise-hidden nav links.
               Single SVG with three <rect> lines that morph into an X
               on open: top & bottom rotate to ±45° while translating to
               vertical-center; middle fades + scales out. Avoids the
               jarring `v-if` SVG swap that snapped between two completely
               different shapes with no in-between frames. -->
          <button
            type="button"
            @click="toggleMobileNav"
            :aria-expanded="mobileNavOpen"
            aria-label="Toggle menu"
            class="md:hidden h-8 w-8 inline-flex items-center justify-center border border-gray-700 hover:border-emerald-500 text-gray-300 hover:text-emerald-500 transition-colors"
          >
            <svg viewBox="0 0 20 20" class="w-4 h-4" fill="currentColor" aria-hidden="true">
              <rect class="hb-line hb-top" :class="{ 'is-open': mobileNavOpen }"
                    x="2" y="4"     width="16" height="1.5" />
              <rect class="hb-line hb-mid" :class="{ 'is-open': mobileNavOpen }"
                    x="2" y="9.25"  width="16" height="1.5" />
              <rect class="hb-line hb-bot" :class="{ 'is-open': mobileNavOpen }"
                    x="2" y="14.5"  width="16" height="1.5" />
            </svg>
          </button>
        </div>
      </div>

      <!-- Mobile slide-down nav panel.
           Custom CSS classes (defined at the bottom of this file) give a
           260ms enter with a spring-decay cubic-bezier and a 200ms exit
           with a smoother ease curve. The old `duration-150 ease-out` /
           `duration-100 ease-in` pair felt abrupt on mobile (low-refresh-
           rate screens can only show 2-3 frames in 100ms, so the
           transition reads as a jump cut). The new curve uses ~16 frames
           at 60Hz which is enough for the eye to perceive smoothness.
           Translate moved from -8px to -12px so the slide is also
           visible at a glance — the longer travel makes the curve read. -->
      <transition name="ncn-drawer">
        <div
          v-show="mobileNavOpen"
          class="md:hidden absolute left-0 right-0 top-full border-t border-gray-800 bg-gray-950/95 backdrop-blur-md shadow-2xl shadow-black/40"
        >
          <nav :class="['flex flex-col text-xs', caseClass]">
            <router-link
              v-for="link in navLinks"
              :key="link.labelKey"
              :to="link.to"
              @click="closeMobileNav"
              :class="[
                'flex items-center justify-between px-4 py-3 border-l-2 border-b border-gray-900 transition-colors tracking-widest',
                $route.path.startsWith(link.to)
                  ? 'border-l-emerald-500 bg-gray-900 text-emerald-500'
                  : 'border-l-transparent text-gray-300 hover:bg-gray-900 hover:text-emerald-500'
              ]"
            >
              <span>{{ t(link.labelKey) }}</span>
              <span class="text-gray-700">›</span>
            </router-link>

            <!-- Operator / Console CTA at the bottom of the panel -->
            <a
              v-if="!session.authenticated"
              :href="operatorHref"
              @click="closeMobileNav"
              :class="['flex items-center justify-between px-4 py-3 border-l-2 border-l-transparent text-emerald-500 hover:bg-gray-900 hover:border-l-emerald-500 tracking-widest', caseClass]"
            >
              <span>▶ {{ t('nav.operator') }}</span>
              <span class="text-gray-700">{{ isPublicHost ? 'admin.example.com' : '/login' }}</span>
            </a>
            <a
              v-else
              :href="consoleHref"
              @click="closeMobileNav"
              :class="['flex items-center justify-between px-4 py-3 border-l-2 border-l-emerald-500 bg-emerald-500/10 text-emerald-500 tracking-widest', caseClass]"
            >
              <span>▶ {{ t('nav.console') }}</span>
              <span>{{ isPublicHost ? 'admin.example.com' : '/admin' }}</span>
            </a>
          </nav>
        </div>
      </transition>
    </header>

    <!-- Backdrop while mobile nav is open. Fades in/out on the same
         curve as the drawer so the two motions feel like one gesture. -->
    <transition name="ncn-backdrop">
      <div
        v-show="mobileNavOpen"
        @click="closeMobileNav"
        class="md:hidden fixed inset-x-0 top-14 bottom-0 bg-black/40 z-30"
        aria-hidden="true"
      ></div>
    </transition>

    <main>
      <slot />
    </main>

    <!-- Footer — minimalist, MoeDove-style.
         Three rows top→bottom: wordmark image, inline link row (legal +
         external AS profile links), copyright. No multi-column grid, no
         Description / Network / Contact blocks — those have been pulled
         up into the page body (peering whois has the AS info, the Hero
         "Request peering" CTA mailto handles outreach). Keeps the footer
         a true legal-receipt and doesn't bury info nobody scrolls for. -->
    <footer class="border-t border-gray-900 mt-24">
      <!-- Tight 3-row composition: wordmark, link row, copyright.
           Container gap-3 keeps everything close. The copyright row uses
           a negative top margin to sit closer to the link row above it
           (link + © are semantically the same "metadata" block, while
           the wordmark above gets its own room). -->
      <div class="max-w-7xl mx-auto px-4 sm:px-6 py-8 sm:py-10 flex flex-col items-center gap-3 sm:gap-4 text-center">
        <!-- 1. Wordmark — width-driven (not height-driven) so it always
             takes a healthy slice of the viewport and stays proportional.
             Painted with the same 5-stop animated gradient as the hero
             headline (slate → blue → pink → emerald → slate, looping
             horizontally) so the footer reads as a deliberate closer to
             the page rather than a separate brand mark.
             The PNG is just an alpha mask — the gradient is what's
             actually visible. -->
        <router-link
          to="/"
          class="inline-block group"
          aria-label="Acme Net"
          title="Acme Net"
        >
          <div
            class="ncn-wordmark transition-opacity group-hover:opacity-90"
            role="img"
            aria-hidden="true"
          ></div>
        </router-link>

        <!-- 2. Legal + external AS profile links — kept on one line
             across every viewport. ↗ external-link glyphs dropped; the
             `target="_blank"` rel still opens them in a new tab. Mobile
             uses text-xs + tracking-normal + tight gap so the row fits
             on a 320 px viewport without wrapping; desktop bumps both
             size and tracking back up. -->
        <div :class="['flex justify-center items-center gap-x-2 sm:gap-x-4 text-xs sm:text-base tracking-normal sm:tracking-widest text-gray-500 uppercase whitespace-nowrap', caseClass]">
          <router-link to="/privacy" class="hover:text-emerald-500 transition-colors">{{ t('footer.privacy') }}</router-link>
          <span class="text-gray-700" aria-hidden="true">|</span>
          <router-link to="/terms" class="hover:text-emerald-500 transition-colors">{{ t('footer.terms') }}</router-link>
          <span class="text-gray-700" aria-hidden="true">|</span>
          <a href="https://www.peeringdb.com/net/33599" target="_blank" rel="noopener"
             class="hover:text-emerald-500 transition-colors">PeeringDB</a>
          <span class="text-gray-700" aria-hidden="true">|</span>
          <a href="https://bgp.tools/as/64500" target="_blank" rel="noopener"
             class="hover:text-emerald-500 transition-colors">bgp.tools</a>
        </div>

        <!-- 3. Copyright — own row, centered, smaller + dimmer than the
             link row so the hierarchy reads as link-row → ©. Negative
             top margin pulls it close to the link row above.
             Mobile keeps tracking-normal to match the link row's tight
             cadence; desktop widens. -->
        <div :class="['-mt-1.5 sm:-mt-2 text-[11px] sm:text-sm tracking-normal sm:tracking-widest text-gray-600 normal-case', caseClass]">
          {{ t('footer.copyright') }}
        </div>
      </div>
    </footer>
  </div>
</template>

<style scoped>
/* ============================================================================
   Mobile hamburger icon — single-SVG morph between ≡ and ✕.
   ============================================================================
   transform-box: fill-box + transform-origin: center together let each
   <rect> rotate around its own visual center (not the SVG's origin),
   which is what you want for a hamburger → X morph. Without these two
   declarations, transform-origin: center would mean the center of the
   <svg>, and the lines would swing wildly across the canvas. */
.hb-line {
  transform-box: fill-box;
  transform-origin: center;
  transition:
    transform 280ms cubic-bezier(0.16, 1, 0.3, 1),
    opacity   200ms ease;
  will-change: transform, opacity;
}
/* Lines at y=4, y=9.25, y=14.5 with the viewBox center at y=10 →
   the top and bottom lines need to translate ±5.25 px to meet at the
   center before rotating ±45°. The middle line just fades + scales
   horizontally to nothing. */
.hb-top.is-open { transform: translateY(5.25px)  rotate(45deg);  }
.hb-mid.is-open { opacity: 0; transform: scaleX(0); }
.hb-bot.is-open { transform: translateY(-5.25px) rotate(-45deg); }

/* ============================================================================
   Mobile drawer slide-down — `ncn-drawer` Vue transition.
   ============================================================================
   Spring-decay cubic-bezier (0.16, 1, 0.3, 1) is the same curve the
   Material You / iOS 16 system drawers use: fast initial movement that
   eases into rest, reading as "deliberate but graceful" rather than the
   default ease-out's mechanical feel.

   Enter is slightly longer than leave (260 vs 200ms) because openings
   need to feel intentional while closings should feel quick. Translate
   is -12px (was -8px) — small enough to stay subtle, large enough to
   carry the eye through the curve so the motion is legible. */
.ncn-drawer-enter-active,
.ncn-drawer-leave-active {
  transition-property: opacity, transform;
  will-change: opacity, transform;
}
.ncn-drawer-enter-active {
  transition-duration: 260ms;
  transition-timing-function: cubic-bezier(0.16, 1, 0.3, 1);
}
.ncn-drawer-leave-active {
  transition-duration: 200ms;
  transition-timing-function: cubic-bezier(0.4, 0, 0.6, 1);
}
.ncn-drawer-enter-from,
.ncn-drawer-leave-to {
  opacity: 0;
  transform: translateY(-12px);
}

/* ============================================================================
   Backdrop fade — paired with drawer so the two motions read as one.
   ============================================================================
   Slightly slower than the drawer's enter/leave so the backdrop's tail
   end lingers a beat after the drawer settles, giving the menu visual
   weight against the page below. */
.ncn-backdrop-enter-active,
.ncn-backdrop-leave-active {
  transition: opacity 220ms ease;
  will-change: opacity;
}
.ncn-backdrop-enter-from,
.ncn-backdrop-leave-to {
  opacity: 0;
}
</style>
