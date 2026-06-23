<script setup lang="ts">
// Public wiki (wiki.example.com / example.com /docs) — anonymous, read-only.
// Uses the PUBLIC api (server forces is_public), so internal pages never show.
import { ref, watch, onMounted, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, type WikiMeta, type WikiPage, type WikiHit } from '@/api/client'
import WikiMarkdown, { type TocItem } from '@/components/WikiMarkdown.vue'

const route = useRoute()
const router = useRouter()
const tree = ref<WikiMeta[]>([])
const page = ref<WikiPage | null>(null)
const toc = ref<TocItem[]>([])
const err = ref('')
const busy = ref(false)
const q = ref('')
const hits = ref<WikiHit[] | null>(null)
const drawerOpen = ref(false) // mobile nav drawer

const curPath = computed(() => {
  const p = route.params.path
  const s = Array.isArray(p) ? p.join('/') : (p || '')
  return s || 'home'
})

function depth(p: string): number { return p.split('/').length - 1 }

// Reset scroll to the top of whatever actually scrolls. The window scrolls
// here, but inside AdminLayout it's an inner overflow-auto ancestor (so a plain
// window.scrollTo is a no-op there). Called from the loader's finally — i.e.
// after the new content is set but BEFORE Vue paints it — so the next page
// appears already at the top, never scrolling up from the previous position.
function scrollContentTop() {
  window.scrollTo({ top: 0 })
  let el = document.querySelector('.wiki-md') as HTMLElement | null
  while (el && el !== document.body) {
    if (el.scrollHeight > el.clientHeight && /auto|scroll/.test(getComputedStyle(el).overflowY)) el.scrollTop = 0
    el = el.parentElement
  }
}
function go(path: string) { drawerOpen.value = false; router.push('/docs/' + path.replace(/^\//, '')) }

// Prev/next from the tree's reading order (it's sorted by sort, path).
const pager = computed(() => {
  const i = tree.value.findIndex((n) => n.path === curPath.value)
  if (i < 0) return { prev: null as WikiMeta | null, next: null as WikiMeta | null }
  return { prev: i > 0 ? tree.value[i - 1] : null, next: i < tree.value.length - 1 ? tree.value[i + 1] : null }
})

async function load(path: string) {
  busy.value = true; err.value = ''; hits.value = null
  try {
    const e = await api.wikiPage(path)
    if (!e.ok) throw new Error(e.error || '页面不存在')
    page.value = e.data ?? null
  } catch (e: unknown) { err.value = e instanceof Error ? e.message : String(e); page.value = null }
  finally { busy.value = false; scrollContentTop() }
}
let searchTimer: ReturnType<typeof setTimeout> | undefined
function runSearch() { clearTimeout(searchTimer); searchTimer = setTimeout(doSearch, 220) }
async function doSearch() {
  if (!q.value.trim()) { hits.value = null; return }
  try { const e = await api.wikiSearch(q.value.trim()); hits.value = e.ok ? (e.data ?? []) : [] } catch { hits.value = [] }
  drawerOpen.value = true // reveal results (no-op on desktop, where the nav is always visible)
}

onMounted(async () => {
  try { const e = await api.wikiTree(); if (e.ok) tree.value = e.data ?? [] } catch { /* ignore */ }
  load(curPath.value)
})
watch(curPath, (p) => load(p))
</script>

<template>
  <div class="max-w-6xl mx-auto px-4 py-6">
    <div class="flex items-center gap-2 flex-wrap mb-4">
      <button type="button" @click="drawerOpen = true" aria-label="打开目录"
        class="md:hidden inline-flex items-center justify-center w-8 h-8 shrink-0 border border-gray-800 text-gray-300 hover:text-emerald-400 hover:border-emerald-700 rounded">
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><path d="M2 4h12M2 8h12M2 12h12"/></svg>
      </button>
      <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200 flex-1 min-w-0 truncate">Acme Net · 文档</h1>
      <input v-model="q" @input="runSearch" placeholder="搜索…"
        class="bg-black border border-gray-800 px-2 py-1 text-xs text-gray-100 focus:border-emerald-700 focus:outline-none w-32 sm:w-44 shrink-0" />
    </div>

    <div class="grid grid-cols-1 md:grid-cols-[200px_1fr] gap-5">
      <!-- mobile drawer backdrop -->
      <div v-if="drawerOpen" @click="drawerOpen = false" class="md:hidden fixed inset-0 z-40 bg-black/60"></div>
      <!-- nav: static sidebar (md+) / slide-in drawer (mobile) -->
      <nav :class="[
        'border-gray-800 bg-gray-900/95 md:bg-gray-900/60 p-2 overflow-y-auto text-sm',
        'fixed inset-y-0 left-0 z-50 w-72 max-w-[82vw] border-r shadow-2xl transition-transform duration-200 ease-out',
        'md:static md:z-auto md:w-auto md:max-w-none md:translate-x-0 md:border md:shadow-none md:self-start md:max-h-[calc(100vh-9rem)]',
        drawerOpen ? 'translate-x-0' : '-translate-x-full',
      ]">
        <div class="md:hidden flex items-center justify-between mb-2 px-1">
          <span class="text-[10px] tracking-widest text-gray-600 uppercase">目录</span>
          <button type="button" @click="drawerOpen = false" aria-label="关闭目录" class="text-gray-500 hover:text-gray-200 leading-none px-1">✕</button>
        </div>
        <template v-if="hits">
          <div class="text-[10px] tracking-widest text-gray-600 uppercase px-1 mb-1">搜索 ({{ hits.length }})</div>
          <button v-for="h in hits" :key="h.path" @click="go(h.path)" class="block w-full text-left px-2 py-1.5 hover:bg-gray-800/60 rounded">
            <div class="text-gray-200 text-xs">{{ h.title }}</div>
            <div class="text-[10px] text-gray-500 truncate">{{ h.snippet }}</div>
          </button>
          <div v-if="!hits.length" class="text-[10px] text-gray-600 italic px-2 py-1">无匹配</div>
        </template>
        <template v-else>
          <button v-for="n in tree" :key="n.path" @click="go(n.path)"
            :class="['block w-full text-left px-2 py-1 rounded text-xs truncate',
              curPath === n.path ? 'text-emerald-400 bg-emerald-950/30' : 'text-gray-400 hover:bg-gray-800/60 hover:text-gray-200']"
            :style="{ paddingLeft: (8 + depth(n.path) * 12) + 'px' }">{{ n.title }}</button>
          <div v-if="!tree.length" class="text-[10px] text-gray-600 italic px-2 py-1">暂无文档</div>
        </template>
      </nav>

      <section class="min-w-0">
        <div v-if="err" class="border border-gray-800 bg-gray-900 p-8 text-center text-[11px] tracking-widest text-gray-600 italic">{{ err }}</div>
        <template v-else-if="page">
          <div class="grid grid-cols-1 lg:grid-cols-[1fr_190px] gap-5">
            <article class="border border-gray-800 bg-gray-900/60 p-5 min-w-0">
              <WikiMarkdown :source="page.content" @toc="toc = $event" @navigate="go" />
              <nav v-if="pager.prev || pager.next" class="mt-8 pt-4 border-t border-gray-800 flex gap-3">
                <button v-if="pager.prev" @click="go(pager.prev.path)"
                  class="group flex-1 min-w-0 text-left border border-gray-800 rounded p-3 hover:border-emerald-700 transition-colors">
                  <div class="text-[10px] uppercase tracking-widest text-gray-600 mb-1">← 上一页</div>
                  <div class="text-sm text-gray-300 group-hover:text-emerald-400 truncate">{{ pager.prev.title }}</div>
                </button>
                <button v-if="pager.next" @click="go(pager.next.path)"
                  class="group flex-1 min-w-0 ml-auto text-right border border-gray-800 rounded p-3 hover:border-emerald-700 transition-colors">
                  <div class="text-[10px] uppercase tracking-widest text-gray-600 mb-1">下一页 →</div>
                  <div class="text-sm text-gray-300 group-hover:text-emerald-400 truncate">{{ pager.next.title }}</div>
                </button>
              </nav>
            </article>
            <aside v-if="toc.length" class="hidden lg:block text-xs border border-gray-800 bg-gray-900/60 p-3 self-start sticky top-4">
              <div class="text-[10px] tracking-widest text-gray-600 uppercase mb-2">目录</div>
              <a v-for="t in toc" :key="t.id" :href="'#' + t.id"
                :class="['block py-0.5 text-gray-400 hover:text-emerald-400 truncate', t.level >= 3 ? 'pl-3 text-gray-500' : '']">{{ t.text }}</a>
            </aside>
          </div>
        </template>
        <div v-else class="border border-gray-800 bg-gray-900 p-8 text-center text-[11px] tracking-widest text-gray-600 italic">加载中…</div>
      </section>
    </div>
  </div>
</template>
