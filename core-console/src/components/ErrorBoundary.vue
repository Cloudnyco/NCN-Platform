<script setup lang="ts">
// View-level error boundary. Renders the routed view ITSELF (passed as the
// `view` prop) so the view is a real DESCENDANT in Vue's component tree —
// onErrorCaptured only fires for descendants, and slot content is owned by
// the slot PROVIDER (App.vue), not by this boundary. So we must NOT take the
// view via <slot>; we render <component :is="view"> in our own scope.
//
// Why this exists: a render/lifecycle throw in any view (a message with a
// malformed shape, an i18n `@` parse error, …) otherwise tears down the
// subtree and the operator sees a black/blank section with zero info. This
// repo has been bitten by "page just goes black" repeatedly. Now the error
// is shown in place and the app shell stays alive.
import { onErrorCaptured, ref, type Component } from 'vue'

defineProps<{ view: Component | null }>()

const err = ref<Error | null>(null)
const info = ref('')

onErrorCaptured((e, _inst, hookInfo) => {
  // eslint-disable-next-line no-console
  console.error('[EB CAUGHT]', hookInfo, e)
  err.value = e instanceof Error ? e : new Error(String(e))
  info.value = hookInfo || ''
  return false // handled — don't propagate, keep the shell alive
})

function reload() { window.location.reload() }
</script>

<template>
  <div v-if="err" class="m-4 max-w-3xl border border-red-700 bg-red-950/30 rounded p-4 font-mono text-sm">
    <div class="text-red-400 tracking-widest mb-2">[VIEW CRASHED] 此页面渲染出错（已捕获，未变黑）</div>
    <div class="text-red-200 mb-2 break-words">{{ err.message || '(no message)' }}</div>
    <div v-if="info" class="text-[11px] text-red-400/70 mb-2">during: {{ info }}</div>
    <pre class="whitespace-pre-wrap text-[11px] text-red-300/80 max-h-72 overflow-auto bg-black/40 rounded p-2">{{ err.stack }}</pre>
    <button @click="reload" class="mt-3 px-3 py-1 border border-red-600 text-red-300 hover:bg-red-600 hover:text-black text-xs tracking-widest uppercase">重新加载</button>
  </div>
  <component v-else :is="view" class="route-anim" />
</template>
