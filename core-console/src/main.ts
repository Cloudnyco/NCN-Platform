import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { i18n } from './i18n'
import { bootstrapTheme, useThemeStore } from './stores/theme'
import './style.css'

// Sync theme to <html> BEFORE Vue mounts → no flash.
bootstrapTheme()

// Set initial <html lang> based on detected locale.
if (typeof document !== 'undefined') {
  const loc = i18n.global.locale as unknown as { value: string }
  document.documentElement.setAttribute('lang', loc.value)

  // Pause all ambient animations when the tab is hidden. Saves a lot of
  // battery on mobile and stops the GPU compositing background frames.
  document.addEventListener('visibilitychange', () => {
    document.documentElement.classList.toggle('ncn-tab-hidden', document.hidden)
  })
}

const pinia = createPinia()
const app = createApp(App)
// Last-resort global error logger. The per-view <ErrorBoundary> shows a panel
// in place of a crashed view; this just makes sure nothing is swallowed.
app.config.errorHandler = (err, _inst, info) => {
  // eslint-disable-next-line no-console
  console.error('[GLOBAL ERROR]', info, err)
}
app.use(pinia)
app.use(router)
app.use(i18n)

// Ensure the store exists & wires its <html>-class watcher immediately.
useThemeStore()

app.mount('#app')

// Dismiss the boot splash (index.html): its inline rAF driver eases progress to
// 100% and fades out, handing off to a painted view rather than a blank frame.
// Trigger it once the first route resolves — but NEVER hang on that, since a
// boot-time redirect (e.g. the ncn:unauthorized bounce) can keep
// router.isReady() pending. A short post-mount safety timer guarantees we
// always hand off; a pure-CSS failsafe in index.html is the last backstop.
let splashDismissed = false
function dismissSplash() {
  if (splashDismissed) return
  splashDismissed = true
  const w = window as unknown as { __ncnSplashDone?: () => void }
  if (w.__ncnSplashDone) { w.__ncnSplashDone(); return }
  const splash = document.getElementById('ncn-splash')
  if (splash) {
    splash.classList.add('ncn-splash--done')
    setTimeout(() => splash.remove(), 650)
  }
}
router.isReady().finally(dismissSplash)
setTimeout(dismissSplash, 1200)
