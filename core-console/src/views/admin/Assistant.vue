<script setup lang="ts">
// Console AI ops AGENT — DeepSeek drives the fleet through a tool-calling loop.
// STREAMS over SSE (live tool steps + answer text), keeps per-operator
// conversation history (sidebar), and a per-operator memory the agent reads +
// writes (remember/forget). Read-only tools auto-run; writes pause with an
// admin-only approval card. Markdown answers render safely (escape-then-tag).
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  api, aiStream,
  type AiMsg, type AiToolCall, type AgentPending, type AgentResult, type AiModelConfig,
  type ConvMeta, type MemoryItem, type AgentStreamHandlers,
} from '@/api/client'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()
const isAdmin = ref(session.role === 'admin')

const messages = ref<AiMsg[]>([])
const pending = ref<AgentPending | null>(null)
const draft = ref('')
const busy = ref(false)
const errorMsg = ref('')
const scroller = ref<HTMLElement | null>(null)

// inline edit of a previous user message (-1 = not editing)
const editingIdx = ref(-1)
const editDraft = ref('')

// mobile history/memory drawer (static column on sm+)
const drawerOpen = ref(false)

// live streaming state for the in-progress turn
const streamText = ref('')
const streamTools = ref<{ name: string; summary: string }[]>([])

// conversation history
const convList = ref<ConvMeta[]>([])
const convId = ref('')

// memory
const memory = ref<MemoryItem[]>([])
const memOpen = ref(false)
const memDraft = ref('')

// per-purpose model picker (admin)
const models = ref<AiModelConfig | null>(null)
const modelsOpen = ref(false)
const purposeLabel: Record<string, string> = {
  chat: '闲聊', ask: '问答 /ask', summary: '摘要', agent: 'Agent /agent', diagnose: '失败诊断',
}

// ── thinking animation (Claude-Code-style) ──
const spinFrame = ref(0)
const verbIdx = ref(0)
const elapsed = ref(0)
const startedAt = ref(0)
const spinFrames = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏']
const thinkVerbs = ['Thinking', 'Pondering', 'Reasoning', 'Inspecting', 'Crunching', 'Synthesizing', 'Working']
function thinkLine(): string {
  return `${spinFrames[spinFrame.value % spinFrames.length]} ${thinkVerbs[verbIdx.value % thinkVerbs.length]}… (${elapsed.value}s)`
}
let spinTimer: ReturnType<typeof setInterval> | null = null
watch(busy, (b) => { if (b) { startedAt.value = Date.now(); spinFrame.value = 0; elapsed.value = 0; verbIdx.value++ } })

onMounted(() => {
  loadModels(); loadConvList(); loadMemory()
  spinTimer = setInterval(() => {
    if (!busy.value) return
    spinFrame.value++
    elapsed.value = Math.floor((Date.now() - startedAt.value) / 1000)
    if (spinFrame.value % 25 === 0) verbIdx.value++
  }, 120)
})
onBeforeUnmount(() => { if (spinTimer) clearInterval(spinTimer) })

async function scrollDown() { await nextTick(); if (scroller.value) scroller.value.scrollTop = scroller.value.scrollHeight }

// ── loaders ──
async function loadModels() { try { const e = await api.aiModels(); if (e.ok && e.data) models.value = e.data } catch { /* */ } }
async function loadConvList() { try { const e = await api.aiConversations(); if (e.ok && e.data) convList.value = Array.isArray(e.data.conversations) ? e.data.conversations : [] } catch { /* */ } }
async function loadMemory() { try { const e = await api.aiMemory(); if (e.ok && e.data) memory.value = Array.isArray(e.data.memory) ? e.data.memory : [] } catch { /* */ } }

async function setModel(purpose: string, model: string) {
  try { const e = await api.aiModelSet(purpose, model); if (e.ok && e.data) models.value = e.data; else errorMsg.value = e.error || '设置失败' }
  catch (e: unknown) { errorMsg.value = e instanceof Error ? e.message : String(e) }
}
function onModelChange(purpose: string, e: Event) { setModel(purpose, (e.target as HTMLSelectElement).value) }

// ── conversation actions ──
function newChat() { messages.value = []; pending.value = null; convId.value = ''; streamText.value = ''; streamTools.value = []; errorMsg.value = ''; drawerOpen.value = false }
async function loadConv(id: string) {
  if (busy.value) return
  drawerOpen.value = false
  try {
    const e = await api.aiConversationGet(id)
    if (e.ok && e.data) { messages.value = Array.isArray(e.data.messages) ? e.data.messages : []; convId.value = id; pending.value = null; streamText.value = ''; streamTools.value = []; scrollDown() }
  } catch (e: unknown) { errorMsg.value = e instanceof Error ? e.message : String(e) }
}
async function delConv(id: string) {
  try { await api.aiConversationDelete(id); if (id === convId.value) newChat(); await loadConvList() } catch { /* */ }
}
async function autosave() {
  if (!messages.value.length) return
  try { const e = await api.aiConversationSave(convId.value, messages.value); if (e.ok && e.data) { convId.value = e.data.id; loadConvList() } } catch { /* */ }
}

// ── memory actions ──
async function addMem() {
  const t = memDraft.value.trim(); if (!t) return
  try { const e = await api.aiMemoryAdd(t); if (e.ok && e.data) { memory.value = e.data.memory; memDraft.value = '' } } catch { /* */ }
}
async function delMem(id: string) { try { await api.aiMemoryDelete(id); await loadMemory() } catch { /* */ } }

// ── the streaming turn driver ──
function applyResult(res: AgentResult) { messages.value = res.messages || []; pending.value = res.pending ?? null }
async function runStream(start: (h: AgentStreamHandlers) => Promise<void>) {
  busy.value = true; errorMsg.value = ''; streamText.value = ''; streamTools.value = []; pending.value = null
  scrollDown()
  try {
    await start({
      onTool: (name, summary) => { streamTools.value.push({ name, summary }); scrollDown() },
      onText: (d) => { streamText.value += d; scrollDown() },
      onDone: (res) => { applyResult(res); streamText.value = ''; streamTools.value = []; loadMemory(); autosave() },
      onError: (m) => { errorMsg.value = m },
    })
  } catch (e: unknown) { errorMsg.value = e instanceof Error ? e.message : String(e) }
  finally { busy.value = false; scrollDown() }
}
async function send() {
  const text = draft.value.trim()
  if (!text || busy.value || pending.value) return
  messages.value.push({ role: 'user', content: text })
  draft.value = ''
  await runStream((h) => aiStream.agent(messages.value, h))
}
async function decide(decision: 'approve' | 'deny') {
  if (!pending.value || busy.value) return
  const id = pending.value.tool_call_id
  await runStream((h) => aiStream.approve(messages.value, id, decision, h))
}
function onKeydown(e: KeyboardEvent) { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() } }

// ── retry / edit ──
// Retry: drop everything after the last user message and regenerate.
function retry() {
  if (busy.value || pending.value || editingIdx.value !== -1) return
  let i = messages.value.length - 1
  while (i >= 0 && messages.value[i].role !== 'user') i--
  if (i < 0) return
  messages.value = messages.value.slice(0, i + 1)
  runStream((h) => aiStream.agent(messages.value, h))
}
// Edit: revise a previous user message, drop everything after it, re-run.
function startEdit(i: number) {
  if (busy.value || pending.value) return
  editingIdx.value = i
  editDraft.value = String(messages.value[i]?.content ?? '')
}
function cancelEdit() { editingIdx.value = -1; editDraft.value = '' }
async function submitEdit(i: number) {
  const text = editDraft.value.trim()
  if (!text || busy.value || pending.value) return
  editingIdx.value = -1
  messages.value = messages.value.slice(0, i)
  messages.value.push({ role: 'user', content: text })
  await runStream((h) => aiStream.agent(messages.value, h))
}
function onEditKeydown(e: KeyboardEvent, i: number) { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); submitEdit(i) } else if (e.key === 'Escape') { cancelEdit() } }

// ── render helpers ──
function isWrite(name: string): boolean { return ['decommission', 'recommission', 'delete_node', 'mesh_apply', 'run_command'].includes(name) }
// Defensive accessors — never assume tc.function exists (a malformed message
// from anywhere must not crash the whole view to a black screen).
function toolName(tc: AiToolCall): string { return tc?.function?.name ?? '(unknown)' }
function toolArgs(tc: AiToolCall): string { return tc?.function?.arguments ?? '' }
function toolResultFor(id: string): string { return messages.value.find(x => x.role === 'tool' && x.tool_call_id === id)?.content ?? '' }
function argsPretty(argstr: string): string { try { return JSON.stringify(JSON.parse(argstr)) } catch { return argstr } }
function esc(s: string): string { return String(s ?? '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;') }
// Safe Markdown → HTML for v-html. ESCAPE FIRST, then only insert known tags,
// so model output can never smuggle HTML/scripts. Covers the subset an LLM
// actually emits: fenced + inline code, headings, bold, italic, links,
// ordered/unordered lists. Code spans are pulled out to placeholders first so
// their contents aren't re-processed (e.g. `**` inside code stays literal).
function renderMd(input: unknown): string {
  let h = esc(String(input ?? ''))

  // 1) Pull code spans out behind unique placeholders so their contents aren't
  // re-processed (e.g. `**` inside code stays literal). Placeholder form
  // \x00<idx>\x00 uses NUL, which never appears in content or markdown syntax,
  // so restoration is collision-free (bare digits would clash with content).
  const stash: string[] = []
  const keep = (html: string) => { stash.push(html); return `\x00${stash.length - 1}\x00` }
  h = h.replace(/```[a-zA-Z0-9_-]*\n?([\s\S]*?)```/g, (_m, code) =>
    keep(`<pre class="whitespace-pre-wrap bg-black/40 rounded p-2 my-1 text-[11px] text-gray-300">${code}</pre>`))
  h = h.replace(/`([^`\n]+)`/g, (_m, code) =>
    keep(`<code class="bg-black/40 px-1 rounded text-emerald-300">${code}</code>`))

  // 2) Block-level: headings -> styled lines; bullets -> bullet glyph
  h = h.replace(/^[ \t]*#{1,6}[ \t]+(.+)$/gm, '<div class="font-semibold text-gray-100 mt-2 mb-0.5">$1</div>')
  h = h.replace(/^([ \t]*)[-*][ \t]+/gm, '$1• ')

  // 3) Inline: bold (before italic so ** wins), italic, links
  h = h.replace(/\*\*([^*\n]+)\*\*/g, '<b>$1</b>')
  h = h.replace(/__([^_\n]+)__/g, '<b>$1</b>')
  h = h.replace(/(^|[^*\w])\*(?!\s)([^*\n]+?)\*(?!\*)/g, '$1<i>$2</i>')
  h = h.replace(/(^|[^_\w])_(?!\s)([^_\n]+?)_(?!_)/g, '$1<i>$2</i>')
  h = h.replace(/\[([^\]\n]+)\]\((https?:\/\/[^\s)"']+)\)/g,
    '<a href="$2" target="_blank" rel="noopener noreferrer" class="text-emerald-400 underline">$1</a>')

  // 4) Restore protected code spans.
  h = h.replace(/\x00(\d+)\x00/g, (_m, i) => stash[+i] ?? '')
  return h
}
</script>

<template>
  <div class="flex h-[calc(100vh-8rem)] gap-3 max-w-6xl mx-auto px-3">
    <!-- mobile-only backdrop behind the drawer -->
    <div v-if="drawerOpen" @click="drawerOpen = false"
      class="sm:hidden fixed inset-0 bg-black/60 z-40 ncn-fade-in"></div>

    <!-- sidebar: history + memory.
         sm+ : static in-flow column. <sm : fixed slide-in drawer
         (translateX-toggled; sm:translate-x-0 keeps it pinned on desktop). -->
    <aside :class="[
        'flex flex-col border-r border-gray-800 min-h-0 py-3',
        'transition-transform duration-200 ease-out will-change-transform',
        'fixed inset-y-0 left-0 z-50 w-64 bg-gray-950 px-3',
        'sm:static sm:z-auto sm:w-52 sm:bg-transparent sm:px-0 sm:pr-2',
        drawerOpen ? 'translate-x-0' : '-translate-x-full sm:translate-x-0']">
      <button @click="newChat" class="w-full text-xs border border-gray-700 hover:border-emerald-600 text-gray-300 hover:text-emerald-400 rounded py-1.5">+ 新对话</button>
      <div class="flex-1 overflow-y-auto mt-2 space-y-0.5 min-h-0">
        <div v-for="c in convList" :key="c.id" @click="loadConv(c.id)"
          class="group flex items-center justify-between px-2 py-1.5 rounded text-xs cursor-pointer"
          :class="c.id === convId ? 'bg-emerald-900/30 text-emerald-300' : 'text-gray-400 hover:bg-gray-800/40'">
          <span class="truncate">{{ c.title }}</span>
          <button @click.stop="delConv(c.id)" class="ml-1 opacity-0 group-hover:opacity-100 text-gray-600 hover:text-red-400">✕</button>
        </div>
        <p v-if="!convList.length" class="text-[11px] text-gray-700 px-2 py-2">还没有历史对话</p>
      </div>
      <!-- memory -->
      <button @click="memOpen = !memOpen" class="mt-2 text-[11px] text-gray-500 hover:text-gray-300 text-left">🧠 记忆 ({{ memory.length }}) {{ memOpen ? '▾' : '▸' }}</button>
      <div v-if="memOpen" class="mt-1 border-t border-gray-800 pt-2 space-y-1 max-h-48 overflow-y-auto">
        <div v-for="m in memory" :key="m.id" class="group flex items-start justify-between gap-1 text-[11px] text-gray-400">
          <span class="break-words">{{ m.text }}</span>
          <button @click="delMem(m.id)" class="opacity-0 group-hover:opacity-100 text-gray-600 hover:text-red-400 shrink-0">✕</button>
        </div>
        <p v-if="!memory.length" class="text-[11px] text-gray-700">（空 — agent 会自动记，也可手动加）</p>
        <div class="flex gap-1 pt-1">
          <input v-model="memDraft" placeholder="手动记一条…" @keydown.enter="addMem"
            class="flex-1 bg-black border border-gray-800 rounded px-2 py-1 text-[11px] text-gray-200 outline-none focus:border-emerald-700" />
          <button @click="addMem" class="text-[11px] text-gray-500 hover:text-emerald-400">+</button>
        </div>
      </div>
    </aside>

    <!-- main chat -->
    <div class="flex-1 flex flex-col min-w-0">
      <div class="flex items-center justify-between py-3">
        <div class="flex items-center gap-2 min-w-0">
          <button class="sm:hidden shrink-0 text-gray-400 hover:text-emerald-400 border border-gray-800 hover:border-emerald-600 rounded px-2 py-1 text-xs transition-colors duration-75"
            @click="drawerOpen = true" title="历史 / 记忆" aria-label="Open history">☰</button>
          <h1 class="text-sm font-semibold tracking-wide text-emerald-400 truncate">🤖 AI 运维 Agent</h1>
        </div>
        <div class="flex items-center gap-3">
          <button v-if="isAdmin && models" class="text-xs text-gray-500 hover:text-gray-300" @click="modelsOpen = !modelsOpen">🧠 模型</button>
          <button class="text-xs text-gray-500 hover:text-gray-300 sm:hidden" @click="newChat">+ 新对话</button>
        </div>
      </div>

      <div v-if="isAdmin && models && modelsOpen" class="mb-2 border border-gray-800 bg-gray-900 rounded p-3 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-2 items-center">
        <template v-for="p in models.order" :key="p">
          <label class="text-[11px] text-gray-400">{{ purposeLabel[p] || p }}</label>
          <select :value="models.purposes[p]" @change="onModelChange(p, $event)"
            class="bg-black border border-gray-800 rounded px-2 py-1 text-xs text-gray-200 focus:border-emerald-700 outline-none">
            <option v-for="mod in models.available" :key="mod" :value="mod">{{ mod }}</option>
          </select>
        </template>
      </div>

      <div ref="scroller" class="flex-1 overflow-y-auto space-y-3 pr-1 min-h-0">
        <p v-if="!messages.length && !busy" class="text-xs text-gray-600 py-8 text-center">
          让它诊断或运维 — 例:“现在哪些节点有问题?”“看看 pop-05 的 BIRD 状态”“重启 pop-05 的 bird”。<br>
          只读查询自动执行;改动 / 命令会先弹确认卡片(admin 批准)。它会记住你告诉它的事。
        </p>

        <template v-for="(m, i) in messages" :key="i">
          <div v-if="m.role === 'user'" class="flex justify-end ncn-fade-in">
            <!-- edit mode -->
            <div v-if="editingIdx === i" class="w-full max-w-[85%] ncn-fade-in">
              <textarea v-model="editDraft" rows="2" @keydown="onEditKeydown($event, i)"
                class="w-full resize-none bg-gray-950 border border-emerald-700 rounded px-3 py-2 text-sm text-gray-100 outline-none"></textarea>
              <div class="flex justify-end gap-2 mt-1">
                <button @click="cancelEdit" class="text-[11px] text-gray-500 hover:text-gray-300 px-2 py-0.5">取消</button>
                <button @click="submitEdit(i)" class="text-[11px] text-emerald-400 hover:text-emerald-300 border border-emerald-700 rounded px-2 py-0.5">保存并重发</button>
              </div>
            </div>
            <!-- normal: bubble + hover edit button -->
            <div v-else class="group flex items-start gap-1.5">
              <button v-if="!busy && !pending" @click="startEdit(i)" title="编辑"
                class="opacity-0 group-hover:opacity-100 mt-2 shrink-0 text-gray-600 hover:text-emerald-400 text-xs">✎</button>
              <div class="max-w-full whitespace-pre-wrap rounded px-3 py-2 text-sm bg-emerald-800/40 border border-emerald-900 text-gray-100">{{ m.content }}</div>
            </div>
          </div>
          <div v-else-if="m.role === 'assistant' && m.content" class="flex justify-start ncn-fade-in">
            <div class="max-w-[85%] whitespace-pre-wrap rounded px-3 py-2 text-sm bg-gray-900 border border-gray-800 text-gray-200 leading-relaxed" v-html="renderMd(m.content || '')"></div>
          </div>
          <div v-else-if="m.role === 'assistant' && Array.isArray(m.tool_calls) && m.tool_calls.length" class="space-y-1.5 ncn-fade-in">
            <div v-for="tc in m.tool_calls" :key="tc.id"
                 class="text-xs border rounded px-3 py-2 font-mono"
                 :class="isWrite(toolName(tc)) ? 'border-amber-800 bg-amber-950/20 text-amber-300' : 'border-gray-800 bg-gray-950/60 text-gray-400'">
              <div>{{ isWrite(toolName(tc)) ? '⚠️' : '🔧' }} {{ toolName(tc) }}
                <span class="text-gray-600">{{ argsPretty(toolArgs(tc)) }}</span></div>
              <pre v-if="toolResultFor(tc.id)" class="mt-1 whitespace-pre-wrap text-[11px] text-gray-500 max-h-48 overflow-y-auto">{{ toolResultFor(tc.id) }}</pre>
            </div>
          </div>
        </template>

        <!-- retry the last turn (regenerate) -->
        <div v-if="messages.length && !busy && !pending && editingIdx === -1" class="flex justify-start ncn-fade-in">
          <button @click="retry" title="重新生成上一条回复"
            class="group text-xs text-gray-500 hover:text-emerald-400 border border-gray-800 hover:border-emerald-700 rounded px-2 py-1 transition-colors duration-100">
            <span class="inline-block transition-transform duration-300 group-hover:rotate-180">↻</span> 重试</button>
        </div>

        <!-- live streaming turn -->
        <div v-if="busy" class="space-y-1.5">
          <div v-for="(t, i) in streamTools" :key="i" class="text-xs border border-gray-800 bg-gray-950/60 text-gray-400 rounded px-3 py-1.5 font-mono ncn-fade-in">🔧 {{ t.name }} <span class="text-gray-600">{{ t.summary }}</span></div>
          <div v-if="streamText" class="flex justify-start">
            <div class="max-w-[85%] whitespace-pre-wrap rounded px-3 py-2 text-sm bg-gray-900 border border-gray-800 text-gray-200 leading-relaxed" v-html="renderMd(streamText)"></div>
          </div>
          <div v-else class="text-xs text-emerald-400 font-mono tabular-nums">{{ thinkLine() }}</div>
        </div>

        <!-- approval card -->
        <div v-if="pending && !busy" class="border-2 border-amber-600/70 bg-amber-950/30 rounded p-3 space-y-2 ncn-rise">
          <div class="text-[10px] tracking-widest text-amber-400 uppercase">待批准 · {{ pending.name }}</div>
          <pre class="whitespace-pre-wrap text-xs font-mono text-amber-100 bg-black/40 border border-amber-900 rounded p-2">{{ pending.summary }}</pre>
          <div v-if="!isAdmin" class="text-[11px] text-red-400">需要 admin 权限才能批准写操作。</div>
          <div v-else class="flex gap-2">
            <button @click="decide('approve')" class="px-3 py-1.5 border border-emerald-600 text-emerald-400 hover:bg-emerald-600 hover:text-black text-[11px] tracking-widest uppercase">✅ 批准执行</button>
            <button @click="decide('deny')" class="px-3 py-1.5 border border-gray-700 text-gray-400 hover:border-red-600 hover:text-red-400 text-[11px] tracking-widest uppercase">✖ 拒绝</button>
          </div>
        </div>
      </div>

      <div v-if="errorMsg" class="border border-red-900 bg-black px-3 py-2 my-2 text-xs text-red-400">
        <span class="text-red-500 tracking-widest">[FAIL]</span> {{ errorMsg }}
      </div>

      <div class="flex items-end gap-2 py-3 border-t border-gray-800">
        <textarea v-model="draft" rows="2" :disabled="!!pending || busy"
          placeholder="让 agent 诊断或运维,Enter 发送 / Shift+Enter 换行(有待批准项时先处理)"
          class="flex-1 resize-none bg-gray-950 border border-gray-800 focus:border-emerald-700 rounded px-3 py-2 text-sm text-gray-200 outline-none disabled:opacity-40"
          @keydown="onKeydown"></textarea>
        <button class="bg-emerald-700 hover:bg-emerald-600 disabled:opacity-30 disabled:cursor-not-allowed text-white text-sm px-4 py-2 rounded"
          :disabled="busy || !draft.trim() || !!pending" @click="send">发送</button>
      </div>
    </div>
  </div>
</template>
