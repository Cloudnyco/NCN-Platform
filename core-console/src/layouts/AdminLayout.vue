<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useSessionStore } from '@/stores/session'
import { useLocaleAware } from '@/i18n'
import { api } from '@/api/client'
import LanguageSwitcher from '@/components/LanguageSwitcher.vue'
import ThemeSwitcher from '@/components/ThemeSwitcher.vue'

const session = useSessionStore()
const route   = useRoute()
const router  = useRouter()
const { t, caseClass } = useLocaleAware()

interface NavItem {
  routeName: string
  labelKey: string
}

const navItems: NavItem[] = [
  { routeName: 'admin.dashboard',    labelKey: 'admin.nav.dashboard' },
  { routeName: 'admin.fleet',        labelKey: 'admin.nav.fleet' },
  { routeName: 'admin.capacity',     labelKey: 'admin.nav.capacity' },
  { routeName: 'admin.traffic',      labelKey: 'admin.nav.traffic' },
  { routeName: 'admin.mitigation',   labelKey: 'admin.nav.mitigation' },
  { routeName: 'admin.servers',      labelKey: 'admin.nav.servers' },
  { routeName: 'admin.connectivity', labelKey: 'admin.nav.connectivity' },
  { routeName: 'admin.alerts',       labelKey: 'admin.nav.alerts' },
  { routeName: 'admin.oncall',       labelKey: 'admin.nav.oncall' },
  { routeName: 'admin.wiki',         labelKey: 'admin.nav.wiki' },
  { routeName: 'admin.assistant',    labelKey: 'admin.nav.assistant' },
  { routeName: 'admin.terminal',     labelKey: 'admin.nav.terminal' },
  { routeName: 'admin.security',     labelKey: 'admin.nav.security' },
  { routeName: 'admin.peering',      labelKey: 'admin.nav.peering' },
  { routeName: 'admin.billing',      labelKey: 'admin.nav.billing' }
]

const statusLabel  = computed(() => session.systemOnline ? t('admin.system_online') : t('admin.system_offline'))

// Operator avatar: <img> when an OAuth profile picture is available, else an
// initials circle. avatarBroken falls back to initials if the image 404s.
const avatarBroken = ref(false)
watch(() => session.avatarUrl, () => { avatarBroken.value = false })
const operatorInitials = computed(() => {
  const s = (session.operator || '').trim()
  if (!s) return '·'
  if (/[^\x00-\x7F]/.test(s[0])) return s[0] // CJK glyph fills the circle on its own
  return s.slice(0, 2).toUpperCase()
})
const currentTitle = computed(() => {
  const name = route.name as string | undefined
  if (!name || !name.startsWith('admin.')) return ''
  return t(`admin.nav.${name.slice('admin.'.length)}`)
})

const isSidebarOpen = ref(false)
const toggleSidebar = () => (isSidebarOpen.value = !isSidebarOpen.value)
const closeSidebar  = () => (isSidebarOpen.value = false)
watch(() => route.fullPath, () => closeSidebar())

const nowEpoch = ref(Math.floor(Date.now() / 1000))
let tick: ReturnType<typeof setInterval> | null = null
onMounted(() => { tick = setInterval(() => (nowEpoch.value = Math.floor(Date.now() / 1000)), 1000) })
onBeforeUnmount(() => { if (tick) clearInterval(tick) })

const ttlLabel = computed(() => {
  if (!session.authenticated || !session.expiresAt) return ''
  const s = Math.max(0, session.expiresAt - nowEpoch.value)
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60
  return `${String(h).padStart(2,'0')}:${String(m).padStart(2,'0')}:${String(sec).padStart(2,'0')}`
})

async function doLogout() {
  await session.logout()
  // Stay on admin.example.com after logout — operators should land back at
  // /login, NOT be bounced to the public marketing site. Strict host
  // separation: admin host serves admin routes only.
  router.replace({ name: 'login' })
}

// SSO: mint a 60s ticket signed with the operator-mail-bridge.key and
// navigate the user straight to webmail. ncn-mail verifies the ticket,
// looks up the mapped mailbox (operator-username-lowercased@example.com),
// issues a 30-day webmail session, and 302's into the inbox.
const ssoBusy = ref(false)
async function openWebmail() {
  if (ssoBusy.value) return
  ssoBusy.value = true
  try {
    const env = await api.ssoMailTicket()
    if (!env.ok || !env.data?.url) {
      throw new Error(env.error || 'sso ticket failed')
    }
    window.location.href = env.data.url
  } catch (e) {
    ssoBusy.value = false
    alert((e instanceof Error ? e.message : String(e)) || 'sso failed')
  }
}
</script>

<template>
  <!-- ncn-vvh-shell: height is driven by the `--ncn-vvh` CSS variable
       (set from JS via the visualViewport API on pages that care — see
       Terminal.vue), with `100dvh` as a fallback for pages that don't
       publish a vvh. Why not just h-dvh? iOS Safari has a long-standing
       limitation: `100dvh` does NOT shrink when the on-screen keyboard
       opens — only when the URL bar collapses. Without the JS-driven
       value, the entire admin shell stays full-height behind the
       keyboard, pushing the terminal cursor offscreen. Android Chrome's
       dvh already tracks the keyboard, so it's not affected, but using
       the same JS-driven path on both platforms keeps the behavior
       identical and removes drift between mobile browsers. -->
  <div class="ncn-vvh-shell w-screen flex flex-col bg-gray-950 text-gray-300 font-mono select-none overflow-hidden">
    <!-- Header -->
    <header class="h-14 shrink-0 flex items-center justify-between border-b border-gray-800 bg-gray-900 px-3 sm:px-4 relative z-50">
      <div class="flex items-center gap-2 sm:gap-2.5 min-w-0">
        <!-- Hamburger (mobile only) -->
        <button
          type="button"
          @click="toggleSidebar"
          aria-label="Toggle navigation"
          class="md:hidden shrink-0 h-9 w-9 flex items-center justify-center border border-gray-800 hover:border-emerald-500 text-gray-300 hover:text-emerald-500 transition-colors duration-75"
        >
          <svg v-if="!isSidebarOpen" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="currentColor" class="w-4 h-4">
            <rect x="2" y="4"  width="16" height="1.5" />
            <rect x="2" y="9.25" width="16" height="1.5" />
            <rect x="2" y="14.5" width="16" height="1.5" />
          </svg>
          <svg v-else xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="currentColor" class="w-4 h-4">
            <path d="M4.7 3.3 10 8.6l5.3-5.3 1.4 1.4L11.4 10l5.3 5.3-1.4 1.4L10 11.4 4.7 16.7l-1.4-1.4L8.6 10 3.3 4.7z" />
          </svg>
        </button>

        <!-- Brand cluster — NCN logo + wordmark.
             The logo (served from /public/logo.svg) replaces the original
             green vertical-bar accent. Same dark-background-friendly SVG
             that PublicLayout uses; admin host serves it from the SPA
             dist, no extra wiring. -->
        <!-- Logo height is a touch smaller than on PublicLayout because the
             admin header has more competing UI elements (status dot, build
             rev, theme + lang switchers, logout). The viewBox crop in
             /public/logo.svg already gives ~30% visual lift over the old
             padded canvas, so we don't need to push the height too hard. -->
        <img src="/logo.svg" alt="NCN" class="h-8 sm:h-9 w-auto shrink-0 ml-0.5 select-none" draggable="false" />
        <span class="text-xs sm:text-sm tracking-[0.18em] text-gray-100 uppercase truncate min-w-0">
          {{ session.asn }} <span class="hidden lg:inline">Core</span> Console
        </span>
      </div>

      <div :class="['flex items-center gap-1.5 sm:gap-3 md:gap-4 text-[10px] sm:text-xs tracking-widest shrink-0', caseClass]">
        <!-- System status — dot always; text label only on sm+ -->
        <div class="flex items-center gap-2"
             :title="statusLabel">
          <span class="inline-block w-2.5 h-2.5 bg-emerald-500 animate-pulse-noc shrink-0" aria-hidden="true"></span>
          <span class="hidden sm:inline" :class="session.systemOnline ? 'text-emerald-500' : 'text-red-500'">{{ statusLabel }}</span>
        </div>
        <div class="hidden md:flex items-center gap-2 text-gray-400">
          <span class="text-gray-600">{{ t('admin.operator') }}</span>
          <span class="inline-flex items-center justify-center w-6 h-6 rounded-full overflow-hidden border border-gray-700 bg-gray-800 text-[10px] font-semibold text-emerald-400 shrink-0 normal-case">
            <img v-if="session.avatarUrl && !avatarBroken" :src="session.avatarUrl" alt="" referrerpolicy="no-referrer"
                 @error="avatarBroken = true" class="w-full h-full object-cover" />
            <template v-else>{{ operatorInitials }}</template>
          </span>
          <span class="text-emerald-500">{{ session.operator || '—' }}</span>
        </div>
        <div class="hidden lg:flex items-center gap-2 text-gray-500">
          <span class="text-gray-600">{{ t('admin.ttl') }}</span>
          <span class="text-gray-300 tabular-nums">{{ ttlLabel || '--:--:--' }}</span>
        </div>
        <ThemeSwitcher />
        <LanguageSwitcher />
        <button
          v-if="session.authenticated"
          @click="openWebmail"
          :disabled="ssoBusy"
          :title="t('admin.open_webmail')"
          :class="[
            'shrink-0 h-8 inline-flex items-center justify-center border border-gray-700 hover:border-emerald-500 text-[10px] tracking-widest text-gray-400 hover:text-emerald-400 transition-colors duration-75 disabled:opacity-40 disabled:cursor-not-allowed',
            'w-8 sm:w-auto sm:px-3 sm:gap-1.5',
            caseClass
          ]"
        >
          <svg viewBox="0 0 16 16" class="w-3.5 h-3.5 shrink-0" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
            <rect x="2" y="3" width="12" height="10" stroke-linejoin="round" />
            <path d="M2 5 L8 9 L14 5" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
          <span class="hidden sm:inline">{{ ssoBusy ? '…' : t('admin.open_webmail') }}</span>
        </button>
        <button
          v-if="session.authenticated"
          @click="doLogout"
          :title="t('admin.sign_out')"
          :class="[
            'shrink-0 h-8 inline-flex items-center justify-center border border-gray-700 hover:border-red-500 text-[10px] tracking-widest text-gray-400 hover:text-red-500 transition-colors duration-75',
            'w-8 sm:w-auto sm:px-3 sm:gap-1.5',
            caseClass
          ]"
          aria-label="Sign out"
        >
          <svg viewBox="0 0 16 16" class="w-3.5 h-3.5 shrink-0 sm:hidden" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
            <path d="M10 12 L14 8 L10 4" stroke-linecap="round" stroke-linejoin="round" />
            <path d="M14 8 H6" stroke-linecap="round" />
            <path d="M6 2 H3 V14 H6" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
          <span class="hidden sm:inline">{{ t('admin.sign_out') }}</span>
        </button>
      </div>
    </header>

    <!-- Body -->
    <div class="flex flex-1 min-h-0 relative">
      <!-- Mobile-only backdrop. Wrapped in <Transition name="fade-fast">
           so opening/closing the drawer fades the dim layer instead of
           snapping it on/off. -->
      <Transition name="fade-fast">
        <div v-if="isSidebarOpen" @click="closeSidebar" class="md:hidden fixed inset-0 top-14 bg-black/60 z-30"></div>
      </Transition>

      <!--
        Sidebar architecture (single element, always rendered):
          - Desktop (md+): `md:static md:translate-x-0` overrides keep it
            in-flow and visible regardless of isSidebarOpen.
          - Mobile (<md): fixed-position drawer at left, translateX-toggled
            between 0 (open) and -100% (closed). `transition-transform`
            gives the slide a 200ms ease-out feel instead of an instant
            display flip — the previous `hidden`/`flex` swap blocked any
            CSS transition from running.

        prefers-reduced-motion (global override in style.css) reduces the
        transition-duration to ~0 so users who opted out still get an
        instant snap.
      -->
      <aside
        :class="[
          'w-64 shrink-0 flex flex-col border-r border-gray-800 bg-gray-900',
          'transition-transform duration-200 ease-out will-change-transform',
          'fixed top-14 bottom-0 left-0 z-40',
          'md:static md:top-auto md:bottom-auto md:left-auto md:z-auto md:translate-x-0',
          isSidebarOpen ? 'translate-x-0' : '-translate-x-full md:translate-x-0',
        ]"
      >
        <!-- Top: back to public site -->
        <router-link to="/" v-slot="{ navigate, href }" custom>
          <a
            :href="href"
            @click="navigate"
            class="group flex items-center justify-between px-4 py-2.5 text-xs border-b border-gray-800 bg-gray-950/50 hover:bg-gray-800 transition-colors"
          >
            <span class="flex items-center gap-2 text-gray-400 group-hover:text-emerald-500">
              <svg viewBox="0 0 16 16" class="w-3 h-3" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true">
                <path d="M10 2 L4 8 L10 14" stroke-linecap="round" stroke-linejoin="round" />
                <path d="M4 8 H14" stroke-linecap="round" />
              </svg>
              <span :class="['tracking-wide', caseClass]">{{ t('admin.back_public') }}</span>
            </span>
            <span class="text-[9px] tracking-widest text-gray-700 group-hover:text-gray-400">ncn.public</span>
          </a>
        </router-link>

        <div :class="['px-4 py-3 text-[10px] tracking-[0.25em] text-gray-600 border-b border-gray-800', caseClass]">
          {{ t('admin.navigation') }}
        </div>
        <nav class="flex-1 overflow-y-auto py-2">
          <ul>
            <li v-for="item in navItems" :key="item.routeName">
              <router-link :to="{ name: item.routeName }" v-slot="{ isActive, navigate, href }" custom>
                <a
                  :href="href"
                  @click="navigate"
                  :class="[
                    'group flex items-center justify-between px-4 py-2.5 text-sm border-l-4 transition-colors duration-75',
                    isActive
                      ? 'border-emerald-500 bg-gray-800 text-emerald-500'
                      : 'border-transparent text-gray-300 hover:bg-gray-800 hover:text-emerald-500 hover:border-emerald-500'
                  ]"
                >
                  <span class="tracking-wide">{{ t(item.labelKey) }}</span>
                  <span :class="['text-xs', isActive ? 'text-emerald-500' : 'text-gray-700 group-hover:text-emerald-700']">{{ isActive ? '●' : '›' }}</span>
                </a>
              </router-link>
            </li>
          </ul>
        </nav>
        <div class="px-4 py-3 text-[10px] tracking-widest text-gray-700 border-t border-gray-800 leading-relaxed">
          <div>ACME NET</div>
          <div>L3 EDGE · v0.1.0</div>
        </div>
      </aside>

      <main class="flex-1 min-w-0 min-h-0 flex flex-col bg-gray-950">
        <div :class="['h-8 shrink-0 flex items-center px-3 sm:px-4 border-b border-gray-800 bg-gray-900 text-[10px] tracking-[0.2em] text-gray-600', caseClass]">
          <span class="text-emerald-700">~</span>
          <span class="mx-2 text-gray-700">/</span>
          <span class="text-gray-400">{{ t('admin.control_plane') }}</span>
          <template v-if="currentTitle">
            <span class="mx-2 text-gray-700">/</span>
            <span class="text-emerald-500 truncate">{{ currentTitle }}</span>
          </template>
        </div>
        <!-- Both `flex flex-col` and `min-h-0` matter: routes that need to
             fill the entire visible scroll area (e.g. admin/Terminal.vue's
             flex-column page) need a flex parent so their `h-full` /
             `flex-1` cascades correctly. min-h-0 lets the flex container
             shrink below content size so the child can do its own
             overflow handling — without it, a tall flex child would force
             the section taller than the viewport. -->
        <section class="flex-1 min-h-0 overflow-auto p-3 sm:p-6 flex flex-col">
          <slot />
        </section>
      </main>
    </div>
  </div>
</template>
