import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useSessionStore } from '@/stores/session'

export interface NavMeta {
  title?: string
  public?: boolean
}

const routes: RouteRecordRaw[] = [
  // ---------- public ----------
  {
    path: '/',
    name: 'home',
    component: () => import('@/views/Landing.vue'),
    meta: { title: 'Acme Net', public: true } satisfies NavMeta
  },
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/Login.vue'),
    meta: { title: 'Sign In', public: true } satisfies NavMeta
  },
  {
    path: '/cli-login',
    name: 'cli-login',
    component: () => import('@/views/CliLogin.vue'),
    meta: { title: 'CLI Login', public: true } satisfies NavMeta
  },
  {
    path: '/lg',
    name: 'lg',
    component: () => import('@/views/LookingGlassPage.vue'),
    meta: { title: 'Looking Glass', public: true } satisfies NavMeta
  },
  {
    // Public wiki (self-hosted; wiki.example.com + example.com/docs). Anonymous,
    // read-only; the public API only returns is_public pages.
    path: '/docs',
    redirect: '/docs/home',
  },
  {
    path: '/docs/:path(.*)',
    name: 'docs',
    component: () => import('@/views/WikiPublic.vue'),
    meta: { title: '文档 · Acme Net', public: true } satisfies NavMeta
  },
  {
    // Public status page — fleet health + 30-day incident timeline.
    // No auth, polls /api/v1/{fleet,incidents}/public every 30s.
    // Both backing endpoints are whitelisted on the example.com host
    // (see deploy/nginx-ncn-core-console.conf).
    path: '/status',
    name: 'status',
    component: () => import('@/views/StatusPage.vue'),
    meta: { title: 'Status · Acme Net', public: true } satisfies NavMeta
  },
  {
    path: '/invite/:token',
    name: 'invite',
    component: () => import('@/views/InvitePage.vue'),
    meta: { title: 'Operator Invitation', public: true } satisfies NavMeta
  },
  {
    // Break-glass recovery URL — minted by `ncn-api admin mint-recover`
    // on tyo. Public route, no auth: trust comes from the HMAC signature
    // baked into the token. See backend/recover_bootstrap.go.
    path: '/recover/:token',
    name: 'recover',
    component: () => import('@/views/RecoverPage.vue'),
    meta: { title: 'Account Recovery', public: true } satisfies NavMeta
  },
  {
    path: '/privacy',
    name: 'privacy',
    component: () => import('@/views/PrivacyPolicy.vue'),
    meta: { title: 'Privacy Policy · Acme Net', public: true } satisfies NavMeta
  },
  {
    path: '/terms',
    name: 'terms',
    component: () => import('@/views/TermsOfService.vue'),
  },
  {
    path: '/peering-apply',
    name: 'peering_apply',
    component: () => import('@/views/PeeringApply.vue'),
    meta: { title: 'Request Peering · Acme Net', public: true } satisfies NavMeta
  },
  {
    path: '/about',
    name: 'about',
    component: () => import('@/views/AboutPage.vue'),
    meta: { title: 'About · Acme Net', public: true } satisfies NavMeta
  },
  {
    path: '/network',
    name: 'network',
    component: () => import('@/views/NetworkPage.vue'),
    meta: { title: 'Network · Acme Net', public: true } satisfies NavMeta
  },
  {
    path: '/peering',
    name: 'peering',
    component: () => import('@/views/PeeringInfoPage.vue'),
    meta: { title: 'Peering · Acme Net', public: true } satisfies NavMeta
  },

  // ---------- admin (protected) ----------
  { path: '/admin', redirect: { name: 'admin.dashboard' } },
  {
    path: '/admin/webmail-bridge',
    name: 'admin.webmail_bridge',
    component: () => import('@/views/admin/WebmailBridge.vue'),
    meta: { title: 'Webmail Bridge' } satisfies NavMeta
  },
  {
    path: '/admin/dashboard',
    name: 'admin.dashboard',
    component: () => import('@/views/admin/Dashboard.vue'),
    meta: { title: '仪表盘' } satisfies NavMeta
  },
  {
    path: '/admin/connectivity',
    name: 'admin.connectivity',
    component: () => import('@/views/admin/Connectivity.vue'),
    meta: { title: '连通性' } satisfies NavMeta
  },
  // /admin/bird is now a tab inside Connectivity; /admin/perf is a tab inside
  // Fleet. Keep the URLs working via redirect so old bookmarks/links don't 404.
  { path: '/admin/bird', redirect: '/admin/connectivity' },
  { path: '/admin/perf', redirect: '/admin/fleet' },
  {
    // On-call rotation + escalation policy (oncall.go). See OnCall.vue.
    path: '/admin/oncall',
    name: 'admin.oncall',
    component: () => import('@/views/admin/OnCall.vue'),
    meta: { title: '值班' } satisfies NavMeta
  },
  {
    path: '/admin/alerts',
    name: 'admin.alerts',
    component: () => import('@/views/admin/Alerts.vue'),
    meta: { title: '告警' } satisfies NavMeta
  },
  {
    // Internal wiki (self-hosted). Any operator reads all pages; admin edits.
    path: '/admin/wiki',
    name: 'admin.wiki',
    component: () => import('@/views/admin/Wiki.vue'),
    meta: { title: '文档' } satisfies NavMeta
  },
  // Monitoring (embedded Grafana) is now the "monitoring" tab inside Fleet;
  // keep the old URL working. See Fleet.vue + Observability.vue.
  { path: '/admin/observability', redirect: '/admin/fleet?tab=monitoring' },
  // Alert rules merged into the Alerts page (Rules tab); keep the old deep
  // link working. See AlertRules.vue (now embedded in Alerts.vue).
  { path: '/admin/alert-rules', redirect: '/admin/alerts?tab=rules' },
  // /admin/ops legacy URL — the quick-ops panel was removed. Redirect to
  // /admin/terminal so any bookmarked links land on the shell, which is
  // the canonical way to run ad-hoc commands now.
  { path: '/admin/ops', redirect: { name: 'admin.terminal' } },
  {
    path: '/admin/fleet',
    name: 'admin.fleet',
    component: () => import('@/views/admin/Fleet.vue'),
    meta: { title: '舰队' } satisfies NavMeta
  },
  {
    // Capacity planning — long-term traffic trends + link-saturation forecast
    // (capacity.go / capacity_series). See Capacity.vue.
    path: '/admin/capacity',
    name: 'admin.capacity',
    component: () => import('@/views/admin/Capacity.vue'),
    meta: { title: '容量' } satisfies NavMeta
  },
  {
    // Netflow/sFlow traffic analytics — top talkers + composition (netflow.go).
    path: '/admin/traffic',
    name: 'admin.traffic',
    component: () => import('@/views/admin/Traffic.vue'),
    meta: { title: '流量' } satisfies NavMeta
  },
  {
    // DDoS mitigation — nft drop/rate rules, confirm-gated (ddos.go). Mitigation.vue.
    path: '/admin/mitigation',
    name: 'admin.mitigation',
    component: () => import('@/views/admin/Mitigation.vue'),
    meta: { title: '缓解' } satisfies NavMeta
  },
  {
    // Server / PoP lifecycle — persistent node registry (add / edit /
    // decommission / delete / provision). See backend/noderegistry.go +
    // nodes_api.go + Servers.vue.
    path: '/admin/servers',
    name: 'admin.servers',
    component: () => import('@/views/admin/Servers.vue'),
    meta: { title: '服务器' } satisfies NavMeta
  },
  {
    path: '/admin/security',
    name: 'admin.security',
    component: () => import('@/views/admin/Security.vue'),
    meta: { title: '安全设置' } satisfies NavMeta
  },
  {
    // Telegram bind landing — reached from the bot's /bind one-time link.
    // Under /admin so the guard forces a logged-in operator first.
    path: '/admin/bind',
    name: 'admin.bind',
    component: () => import('@/views/admin/Bind.vue'),
    meta: { title: '绑定 Telegram' } satisfies NavMeta
  },
  {
    path: '/admin/assistant',
    name: 'admin.assistant',
    component: () => import('@/views/admin/Assistant.vue'),
    meta: { title: 'AI 助手' } satisfies NavMeta
  },
  {
    path: '/admin/peering',
    name: 'admin.peering',
    component: () => import('@/views/admin/Peering.vue'),
    meta: { title: '互联申请' } satisfies NavMeta
  },
  {
    // Internal VPS rent tracker — NOT customer-facing billing.
    // See backend/billing.go + Billing.vue for design notes.
    path: '/admin/billing',
    name: 'admin.billing',
    component: () => import('@/views/admin/Billing.vue'),
    meta: { title: '月费' } satisfies NavMeta
  },
  // /admin/audit is now a tab inside Security — keep the URL working via redirect
  { path: '/admin/audit', redirect: '/admin/security?tab=audit' },
  {
    path: '/admin/terminal',
    name: 'admin.terminal',
    component: () => import('@/views/admin/Terminal.vue'),
    meta: { title: '终端' } satisfies NavMeta
  },
  {
    path: '/admin/onboarding',
    name: 'admin.onboarding',
    component: () => import('@/views/admin/Onboarding.vue'),
    meta: { title: '安全引导' } satisfies NavMeta
  },

  // ---------- legacy redirects ----------
  { path: '/telemetry',           redirect: { name: 'admin.dashboard' } },
  { path: '/bgp',                 redirect: { name: 'admin.bird' } },
  { path: '/wireguard',           redirect: { name: 'admin.connectivity' } },
  { path: '/zero-trust',          redirect: { name: 'admin.dashboard' } },
  { path: '/looking-glass',       redirect: { name: 'lg' } },
  { path: '/admin/looking-glass', redirect: { name: 'lg' } },
  { path: '/admin/telemetry',     redirect: { name: 'admin.dashboard' } },
  { path: '/admin/bgp',           redirect: { name: 'admin.bird' } },
  { path: '/admin/wireguard',     redirect: { name: 'admin.connectivity' } },
  { path: '/admin/zero-trust',    redirect: { name: 'admin.dashboard' } },

  // catch-all — host-aware. On admin.example.com, unknown paths land at the
  // login screen (admin host should never render the public Landing). On
  // example.com, unknown paths fall back to the Landing root.
  {
    path: '/:pathMatch(.*)*',
    redirect: () => isAdminHost() ? { name: 'login' } : { path: '/' }
  }
]

// Returns true when the current page is loaded from the admin subdomain.
// Strict separation between example.com (public marketing + LG) and
// admin.example.com (operator console + auth) is enforced here in JS as
// defense-in-depth on top of nginx-level redirects — SPA-internal
// navigation (vue-router push/replace) doesn't hit nginx so the rules
// must also live here.
function isAdminHost(): boolean {
  return typeof window !== 'undefined' &&
    window.location.hostname.startsWith('admin.')
}

export const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior(to) {
    if (to.hash) return { el: to.hash, behavior: 'smooth' }
    return { top: 0 }
  }
})

// ----------------------------------------------------------------------------
// Auth guard — only /admin/* is protected.
// ----------------------------------------------------------------------------

router.beforeEach(async (to) => {
  const session = useSessionStore()
  const requiresAuth = to.path.startsWith('/admin')

  // === Strict host separation (defense-in-depth on top of nginx) ===
  //
  // Admin host:
  //   - The public Landing (`home`) and the Looking Glass (`lg`) belong
  //     on example.com. If an in-SPA navigation lands here, bounce.
  //   - `/` itself becomes either login (unauth'd) or dashboard (auth'd)
  //     — admin operators don't need a marketing page.
  //   - `/invite/:token` stays — invite URLs are minted with the admin
  //     host so recipients reach the registration flow directly.
  //
  // Public host:
  //   - `/admin/*` and `/login` belong on admin.example.com. Hard-redirect
  //     since vue-router can't cross hostnames in-app. nginx also 301s
  //     these — this is just for SPA-internal pushes.
  if (typeof window !== 'undefined') {
    if (isAdminHost()) {
      if (to.name === 'home') {
        if (!session.checked) await session.fetchMe()
        return session.authenticated
          ? { name: 'admin.dashboard' }
          : { name: 'login' }
      }
      if (to.name === 'lg' || to.name === 'status'
          || to.name === 'about' || to.name === 'network' || to.name === 'peering') {
        // Public-host-only pages: bounce off admin.example.com to the
        // canonical example.com URL. The router-internal SPA push goes
        // through here too; the hard window.location swap is needed
        // because vue-router can't cross hostnames in-app.
        window.location.replace('https://example.com' + to.fullPath)
        return false
      }
    } else {
      if (to.path.startsWith('/admin') || to.name === 'login') {
        window.location.replace('https://admin.example.com' + to.fullPath)
        return false
      }
    }
  }

  if (!session.checked) {
    await session.fetchMe()
  }

  if (requiresAuth && !session.authenticated) {
    return { name: 'login', query: { next: to.fullPath } }
  }
  if (session.authenticated && to.name === 'login') {
    return { path: '/admin' }
  }

  // First-login MFA gate. Once a user logs in with no passkey AND no TOTP
  // secret, the backend flags them mfa_required. Until they bind one, we
  // hold them at /admin/onboarding. Logout is the only escape.
  if (
    session.authenticated &&
    session.mfaRequired &&
    requiresAuth &&
    to.name !== 'admin.onboarding'
  ) {
    return { name: 'admin.onboarding', query: { next: to.fullPath } }
  }
  // Conversely: if MFA is already satisfied, don't trap them at onboarding.
  if (
    session.authenticated &&
    !session.mfaRequired &&
    to.name === 'admin.onboarding'
  ) {
    return { path: '/admin' }
  }

  return true
})

// Global 401 broadcaster — bounce to /login if we were in admin shell.
if (typeof window !== 'undefined') {
  window.addEventListener('ncn:unauthorized', () => {
    const session = useSessionStore()
    if (session.authenticated) session.clear()
    const here = router.currentRoute.value
    if (here.path.startsWith('/admin')) {
      router.replace({ name: 'login', query: { next: here.fullPath } })
    }
  })
}
