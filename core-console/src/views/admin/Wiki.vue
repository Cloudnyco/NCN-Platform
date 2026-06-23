<script setup lang="ts">
// Internal wiki (admin host). Any logged-in operator reads ALL pages; admins
// edit (markdown + live preview), with version history + revert. Reuses the
// console's dark design system + WikiMarkdown renderer.
import { computed, onMounted, ref } from 'vue'
import { api, type WikiMeta, type WikiPage, type WikiVersion, type WikiHit } from '@/api/client'
import { useSessionStore } from '@/stores/session'
import WikiMarkdown, { type TocItem } from '@/components/WikiMarkdown.vue'

const session = useSessionStore()
const isAdmin = computed(() => session.role === 'admin')

const tree = ref<WikiMeta[]>([])
const page = ref<WikiPage | null>(null)
const toc = ref<TocItem[]>([])
const mode = ref<'read' | 'edit'>('read')
const busy = ref(false)
const err = ref('')

// search
const q = ref('')
const hits = ref<WikiHit[] | null>(null)
const drawerOpen = ref(false) // mobile nav drawer

// edit form
const form = ref<{ path: string; title: string; content: string; is_public: boolean; sort: number }>(
  { path: '', title: '', content: '', is_public: false, sort: 0 })
const versions = ref<WikiVersion[]>([])
const showHistory = ref(false)

function depth(p: string): number { return p.split('/').length - 1 }

// Prev/next from the tree's reading order (sorted by sort, path).
const pager = computed(() => {
  const cur = page.value?.path
  const i = cur ? tree.value.findIndex((n) => n.path === cur) : -1
  if (i < 0) return { prev: null as WikiMeta | null, next: null as WikiMeta | null }
  return { prev: i > 0 ? tree.value[i - 1] : null, next: i < tree.value.length - 1 ? tree.value[i + 1] : null }
})

async function loadTree() {
  try { const e = await api.wikiTreeAuth(); if (e.ok) tree.value = e.data ?? [] } catch { /* ignore */ }
}
// Reset scroll to the top of whatever actually scrolls. AdminLayout puts the
// content in an inner overflow-auto <section>, so window.scrollTo alone is a
// no-op here — walk up from the article and zero the scrollable ancestor too.
// Called from open()'s finally (after the new page is set, before Vue paints)
// so the page appears already at the top instead of scrolling up.
function scrollContentTop() {
  window.scrollTo({ top: 0 })
  let el = document.querySelector('.wiki-md') as HTMLElement | null
  while (el && el !== document.body) {
    if (el.scrollHeight > el.clientHeight && /auto|scroll/.test(getComputedStyle(el).overflowY)) el.scrollTop = 0
    el = el.parentElement
  }
}
async function open(path: string) {
  busy.value = true; err.value = ''; mode.value = 'read'; hits.value = null; showHistory.value = false; drawerOpen.value = false
  try {
    const e = await api.wikiPageAuth(path)
    if (!e.ok) throw new Error(e.error || 'load failed')
    page.value = e.data ?? null
  } catch (e: unknown) { err.value = e instanceof Error ? e.message : String(e) }
  finally { busy.value = false; scrollContentTop() }
}
let searchTimer: ReturnType<typeof setTimeout> | undefined
function runSearch() { clearTimeout(searchTimer); searchTimer = setTimeout(doSearch, 220) }
async function doSearch() {
  if (!q.value.trim()) { hits.value = null; return }
  try { const e = await api.wikiSearchAuth(q.value.trim()); hits.value = e.ok ? (e.data ?? []) : [] } catch { hits.value = [] }
  drawerOpen.value = true // reveal results (no-op on desktop, where the nav is always visible)
}

function startEdit() {
  if (!page.value) return
  form.value = { path: page.value.path, title: page.value.title, content: page.value.content,
    is_public: page.value.is_public, sort: page.value.sort }
  mode.value = 'edit'
}
function startNew() {
  form.value = { path: '', title: '', content: '# 新页面\n', is_public: false, sort: 0 }
  page.value = null; mode.value = 'edit'
}
async function save() {
  busy.value = true; err.value = ''
  try {
    const e = await api.wikiSave({ path: form.value.path.trim(), title: form.value.title.trim(),
      content: form.value.content, is_public: form.value.is_public, sort: form.value.sort })
    if (!e.ok) throw new Error(e.error || 'save failed')
    await loadTree(); await open(form.value.path.trim())
  } catch (e: unknown) { err.value = e instanceof Error ? e.message : String(e) }
  finally { busy.value = false }
}
async function del() {
  if (!page.value || !confirm('删除页面 ' + page.value.path + ' ?')) return
  busy.value = true
  try { await api.wikiDelete(page.value.path); page.value = null; await loadTree() }
  finally { busy.value = false }
}
async function loadVersions() {
  if (!page.value) return
  showHistory.value = !showHistory.value
  if (showHistory.value) { const e = await api.wikiVersions(page.value.path); versions.value = e.ok ? (e.data ?? []) : [] }
}
async function revert(v: WikiVersion) {
  if (!page.value || !confirm('回滚到版本 v' + v.version + ' ?')) return
  busy.value = true
  try { await api.wikiRevert(page.value.path, v.id); await open(page.value.path) }
  finally { busy.value = false }
}

onMounted(async () => { await loadTree(); if (tree.value.length) open(tree.value[0].path) })
</script>

<template>
  <div class="space-y-4">
    <div class="border border-gray-800 bg-gray-900 p-4 flex items-center justify-between flex-wrap gap-2">
      <div class="flex items-center gap-3 min-w-0">
        <button type="button" @click="drawerOpen = true" aria-label="打开目录"
          class="md:hidden inline-flex items-center justify-center w-7 h-7 shrink-0 border border-gray-700 text-gray-300 hover:text-emerald-400 hover:border-emerald-700 rounded">
          <svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><path d="M2 4h12M2 8h12M2 12h12"/></svg>
        </button>
        <span class="w-1.5 h-1.5 bg-emerald-500 animate-pulse shrink-0"></span>
        <h1 class="text-sm tracking-[0.2em] uppercase text-gray-200 truncate">文档 · Wiki</h1>
        <span class="text-[10px] tracking-widest text-gray-700 shrink-0">{{ tree.length }} 页</span>
      </div>
      <div class="flex items-center gap-2">
        <input v-model="q" @input="runSearch" placeholder="搜索…"
          class="bg-black border border-gray-800 px-2 py-1 text-xs text-gray-100 focus:border-emerald-700 focus:outline-none w-32 sm:w-40" />
        <button v-if="isAdmin" @click="startNew"
          class="px-2 py-1 border border-gray-700 text-[10px] uppercase tracking-widest text-gray-400 hover:border-emerald-600 hover:text-emerald-400">+ 新建</button>
      </div>
    </div>

    <div class="grid grid-cols-1 md:grid-cols-[210px_1fr] gap-4">
      <!-- mobile drawer backdrop -->
      <div v-if="drawerOpen" @click="drawerOpen = false" class="md:hidden fixed inset-0 z-40 bg-black/60"></div>
      <!-- page tree / search results: static sidebar (md+) / slide-in drawer (mobile) -->
      <nav :class="[
        'border-gray-800 bg-gray-900 p-2 overflow-y-auto text-sm',
        'fixed inset-y-0 left-0 z-50 w-72 max-w-[82vw] border-r shadow-2xl transition-transform duration-200 ease-out',
        'md:static md:z-auto md:w-auto md:max-w-none md:translate-x-0 md:border md:shadow-none md:max-h-[calc(100vh-10rem)]',
        drawerOpen ? 'translate-x-0' : '-translate-x-full',
      ]">
        <div class="md:hidden flex items-center justify-between mb-2 px-1">
          <span class="text-[10px] tracking-widest text-gray-600 uppercase">目录</span>
          <button type="button" @click="drawerOpen = false" aria-label="关闭目录" class="text-gray-500 hover:text-gray-200 leading-none px-1">✕</button>
        </div>
        <template v-if="hits">
          <div class="text-[10px] tracking-widest text-gray-600 uppercase px-1 mb-1">搜索结果 ({{ hits.length }})</div>
          <button v-for="h in hits" :key="h.path" @click="open(h.path)"
            class="block w-full text-left px-2 py-1.5 hover:bg-gray-800/60 rounded">
            <div class="text-gray-200 text-xs">{{ h.title }}</div>
            <div class="text-[10px] text-gray-500 truncate">{{ h.snippet }}</div>
          </button>
          <div v-if="!hits.length" class="text-[10px] text-gray-600 italic px-2 py-1">无匹配</div>
        </template>
        <template v-else>
          <button v-for="n in tree" :key="n.path" @click="open(n.path)"
            :class="['block w-full text-left px-2 py-1 rounded text-xs truncate',
              page && page.path === n.path ? 'text-emerald-400 bg-emerald-950/30' : 'text-gray-400 hover:bg-gray-800/60 hover:text-gray-200']"
            :style="{ paddingLeft: (8 + depth(n.path) * 12) + 'px' }">
            {{ n.title }}<span v-if="!n.is_public" class="text-[9px] text-gray-600 ml-1">·内部</span>
          </button>
        </template>
      </nav>

      <!-- content -->
      <section class="min-w-0">
        <div v-if="err" class="border border-red-900 bg-red-950/30 text-red-300 text-xs p-2 mb-2">ERR · {{ err }}</div>

        <!-- READ -->
        <template v-if="mode === 'read' && page">
          <div class="flex items-center justify-end gap-2 mb-2" v-if="isAdmin">
            <button @click="startEdit" class="px-2 py-1 border border-gray-700 text-[10px] uppercase tracking-widest text-gray-400 hover:border-emerald-600 hover:text-emerald-400">编辑</button>
            <button @click="loadVersions" class="px-2 py-1 border border-gray-700 text-[10px] uppercase tracking-widest text-gray-400 hover:border-sky-600 hover:text-sky-400">历史</button>
            <button @click="del" class="px-2 py-1 border border-gray-700 text-[10px] uppercase tracking-widest text-gray-400 hover:border-red-700 hover:text-red-400">删除</button>
          </div>
          <div v-if="showHistory" class="border border-gray-800 bg-gray-900 p-2 mb-2 text-xs">
            <div class="text-[10px] tracking-widest text-gray-600 uppercase mb-1">版本历史</div>
            <div v-for="v in versions" :key="v.id" class="flex items-center justify-between py-1 border-b border-gray-800/50">
              <span class="text-gray-400">v{{ v.version }} · {{ v.edited_by }} · {{ new Date(v.edited_at).toLocaleString() }}</span>
              <button @click="revert(v)" class="text-[10px] text-amber-400 hover:text-amber-300 uppercase">回滚</button>
            </div>
            <div v-if="!versions.length" class="text-[10px] text-gray-600 italic">无历史</div>
          </div>
          <div class="grid grid-cols-1 lg:grid-cols-[1fr_200px] gap-4">
            <article class="border border-gray-800 bg-gray-900 p-5 min-w-0">
              <WikiMarkdown :source="page.content" @toc="toc = $event" @navigate="open" />
              <nav v-if="pager.prev || pager.next" class="mt-8 pt-4 border-t border-gray-800 flex gap-3">
                <button v-if="pager.prev" @click="open(pager.prev.path)"
                  class="group flex-1 min-w-0 text-left border border-gray-800 rounded p-3 hover:border-emerald-700 transition-colors">
                  <div class="text-[10px] uppercase tracking-widest text-gray-600 mb-1">← 上一页</div>
                  <div class="text-sm text-gray-300 group-hover:text-emerald-400 truncate">{{ pager.prev.title }}</div>
                </button>
                <button v-if="pager.next" @click="open(pager.next.path)"
                  class="group flex-1 min-w-0 ml-auto text-right border border-gray-800 rounded p-3 hover:border-emerald-700 transition-colors">
                  <div class="text-[10px] uppercase tracking-widest text-gray-600 mb-1">下一页 →</div>
                  <div class="text-sm text-gray-300 group-hover:text-emerald-400 truncate">{{ pager.next.title }}</div>
                </button>
              </nav>
            </article>
            <aside v-if="toc.length" class="hidden lg:block text-xs border border-gray-800 bg-gray-900 p-3 self-start sticky top-4">
              <div class="text-[10px] tracking-widest text-gray-600 uppercase mb-2">本页目录</div>
              <a v-for="t in toc" :key="t.id" :href="'#' + t.id"
                :class="['block py-0.5 text-gray-400 hover:text-emerald-400 truncate', t.level >= 3 ? 'pl-3 text-gray-500' : '']">{{ t.text }}</a>
            </aside>
          </div>
          <div class="text-[10px] text-gray-600 mt-2">{{ page.path }} · v{{ page.version }} · {{ page.updated_by }} · {{ new Date(page.updated_at).toLocaleString() }}</div>
        </template>

        <!-- EDIT (admin) -->
        <template v-else-if="mode === 'edit' && isAdmin">
          <div class="border border-gray-800 bg-gray-900 p-3 space-y-2">
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-2">
              <label class="block"><span class="text-[10px] tracking-widest text-gray-500 uppercase">路径</span>
                <input v-model="form.path" placeholder="ops/systems/foo" class="w-full bg-black border border-gray-800 px-2 py-1 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none" /></label>
              <label class="block"><span class="text-[10px] tracking-widest text-gray-500 uppercase">标题</span>
                <input v-model="form.title" class="w-full bg-black border border-gray-800 px-2 py-1 text-sm text-gray-100 focus:border-emerald-700 focus:outline-none" /></label>
            </div>
            <div class="flex items-center gap-4 text-[11px] text-gray-300">
              <label class="flex items-center gap-1"><input type="checkbox" v-model="form.is_public" /> 公开页(匿名可见)</label>
              <label class="flex items-center gap-1">排序 <input v-model.number="form.sort" type="number" class="w-16 bg-black border border-gray-800 px-1 py-0.5" /></label>
            </div>
            <div class="grid grid-cols-1 lg:grid-cols-2 gap-3">
              <textarea v-model="form.content" spellcheck="false"
                class="w-full h-[60vh] bg-black border border-gray-800 px-2 py-2 text-[12.5px] font-mono text-gray-100 focus:border-emerald-700 focus:outline-none resize-none"></textarea>
              <div class="border border-gray-800 bg-gray-900 p-4 h-[60vh] overflow-y-auto min-w-0">
                <WikiMarkdown :source="form.content" />
              </div>
            </div>
            <div class="flex items-center gap-2">
              <button @click="save" :disabled="busy || !form.path || !form.title"
                class="px-3 py-1 border border-emerald-700 text-[11px] uppercase tracking-widest text-emerald-400 hover:bg-emerald-950 disabled:opacity-40">保存</button>
              <button @click="page ? (mode = 'read') : (mode = 'read')" class="px-3 py-1 border border-gray-700 text-[11px] uppercase tracking-widest text-gray-400">取消</button>
            </div>
          </div>
        </template>

        <div v-else class="border border-gray-800 bg-gray-900 p-8 text-center text-[11px] tracking-widest text-gray-600 italic">
          {{ busy ? '加载中…' : '选择左侧页面' }}
        </div>
      </section>
    </div>
  </div>
</template>
