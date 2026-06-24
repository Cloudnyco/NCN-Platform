<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import AdminLayout from '@/layouts/AdminLayout.vue'
import PublicLayout from '@/layouts/PublicLayout.vue'
import ErrorBoundary from '@/components/ErrorBoundary.vue'

const route = useRoute()

const isLogin = computed(() => route.name === 'login' || route.name === 'cli-login')
const isAdmin = computed(() => route.path.startsWith('/admin'))
</script>

<template>
  <!-- Login: bare card, no shell -->
  <router-view v-if="isLogin" />

  <!-- Admin: console shell (header + sidebar).
       The <Transition name="route" mode="out-in"> wraps the keyed
       <component>, so navigating between admin sub-routes plays a
       soft fade+lift instead of snapping the new view in. Key by
       route.name to guarantee even same-component routes play the
       transition. -->
  <!-- Admin + Public: route polish via CSS @keyframes (NOT Vue Transition).
       `:key="route.name"` forces re-mount of the inner component on
       every navigation, which restarts the .route-anim CSS animation.
       Unlike Vue Transition (which depends on transitionend firing,
       unreliable on tall pages), `@keyframes` has a finite committed
       duration: even if the browser skips paint frames mid-animation,
       the element ends at the natural unanimated state (opacity:1)
       because we don't use fill-mode forwards. Worst case = instant
       snap-in; never a stuck-at-opacity:0 page. See style.css
       `.route-anim` + memory feedback_vue_transition_tall_content. -->
  <AdminLayout v-else-if="isAdmin">
    <router-view v-slot="{ Component }">
      <ErrorBoundary :view="Component" :key="route.name" />
    </router-view>
  </AdminLayout>

  <PublicLayout v-else>
    <router-view v-slot="{ Component }">
      <ErrorBoundary :view="Component" :key="route.name" />
    </router-view>
  </PublicLayout>
</template>
