<script setup lang="ts">
import { ref, watch } from 'vue'

interface Props {
  open: boolean
  title: string
  description: string
  severity: 'low' | 'medium' | 'high' | 'info'
  expectedConfirmation: string
  busy?: boolean
  errorMsg?: string
}
const props = defineProps<Props>()
const emit = defineEmits<{
  cancel: []
  confirm: [confirmation: string]
}>()

const typed = ref('')
const localErr = ref('')

watch(
  () => props.open,
  (v) => {
    if (v) {
      typed.value = ''
      localErr.value = ''
    }
  }
)

function attempt() {
  if (typed.value !== props.expectedConfirmation) {
    localErr.value = `must type EXACTLY: ${props.expectedConfirmation}`
    return
  }
  localErr.value = ''
  emit('confirm', typed.value)
}

const severityClasses = {
  info:   { border: 'border-emerald-700/60', bg: 'bg-emerald-950/30', text: 'text-emerald-400', btn: 'bg-emerald-600 hover:bg-emerald-500' },
  low:    { border: 'border-amber-500',      bg: 'bg-amber-900/30',   text: 'text-amber-400',   btn: 'bg-amber-600 hover:bg-amber-500' },
  medium: { border: 'border-orange-500',     bg: 'bg-orange-900/30',  text: 'text-orange-400',  btn: 'bg-orange-600 hover:bg-orange-500' },
  high:   { border: 'border-red-600',        bg: 'bg-red-900/40',     text: 'text-red-400',     btn: 'bg-red-600 hover:bg-red-500' }
} as const
</script>

<template>
  <!-- Two coordinated transitions:
       1. `.ncn-confirm-fade`   — backdrop opacity 0→1 (240ms ease-out)
       2. `.ncn-confirm-card`   — card itself: mobile slides up from
          translateY(100%); desktop scales 0.96→1 with subtle lift.
          Spring-out easing (cubic-bezier 0.16, 1, 0.3, 1) for a settled
          arrival.
       Mobile is detected via `items-end sm:items-center` — the card
       anchors to the bottom edge below the sm: breakpoint, recreating
       the iOS bottom-sheet feel. safe-area-inset-bottom keeps the
       action row above the iOS home pill. -->
  <Transition name="ncn-confirm-fade">
    <div v-if="open"
         class="ncn-confirm-backdrop fixed inset-0 z-[100] flex items-end sm:items-center justify-center sm:p-4 bg-black/75 backdrop-blur-sm"
         @click.self="emit('cancel')">
      <div
        :class="[
          'ncn-confirm-card border-2 bg-gray-900 max-w-md w-full font-mono rounded-t-lg sm:rounded-none',
          severityClasses[severity].border,
        ]"
        style="padding-bottom: env(safe-area-inset-bottom);"
      >
        <div :class="['px-4 py-2 border-b text-xs tracking-widest uppercase', severityClasses[severity].border, severityClasses[severity].bg, severityClasses[severity].text]">
          ⚠ SENSITIVE OPERATION · severity={{ severity }}
        </div>
        <div class="p-4">
          <h3 class="text-base text-gray-100 mb-2">{{ title }}</h3>
          <p class="text-sm text-gray-400 leading-relaxed normal-case tracking-normal">{{ description }}</p>
          <div class="mt-4 text-[10px] tracking-widest uppercase text-gray-600">
            Type EXACTLY to confirm:
          </div>
          <code class="block mt-1 px-3 py-2 bg-black text-emerald-400 text-sm select-all border border-gray-800">{{ expectedConfirmation }}</code>
          <input
            v-model="typed"
            @keyup.enter="attempt"
            autocomplete="off"
            autocorrect="off"
            spellcheck="false"
            placeholder="(retype above)"
            class="mt-2 w-full bg-black border border-gray-800 px-3 py-2 text-sm font-mono text-gray-100 placeholder:text-gray-700 focus:border-red-500 focus:outline-none"
          />
          <div v-if="localErr || errorMsg" class="mt-2 text-xs text-red-400 normal-case tracking-normal">
            ⨯ {{ localErr || errorMsg }}
          </div>
        </div>
        <div class="flex border-t border-gray-800">
          <button
            @click="emit('cancel')"
            :disabled="busy"
            class="flex-1 px-4 py-3 text-xs tracking-widest uppercase text-gray-400 hover:bg-gray-800 transition-colors disabled:opacity-50"
          >cancel</button>
          <button
            @click="attempt"
            :disabled="busy || typed !== expectedConfirmation"
            :class="['flex-1 px-4 py-3 text-xs tracking-widest uppercase text-white transition-colors disabled:opacity-30 disabled:cursor-not-allowed', severityClasses[severity].btn]"
          >{{ busy ? '◌ EXECUTING...' : '▶ EXECUTE' }}</button>
        </div>
      </div>
    </div>
  </Transition>
</template>

<style scoped>
/* Backdrop fade — covers the whole dialog wrapper so both backdrop AND
   card fade together initially. The card then runs its own transform
   transition (defined below) for the slide/scale arrival. */
.ncn-confirm-fade-enter-active,
.ncn-confirm-fade-leave-active {
  transition: opacity 240ms ease-out;
}
.ncn-confirm-fade-enter-from,
.ncn-confirm-fade-leave-to {
  opacity: 0;
}

/* Card arrival. The `.ncn-confirm-fade-enter-active .ncn-confirm-card`
   selector applies during the same window as the backdrop fade — Vue
   doesn't have separate transitions per child, so we drive the card's
   transform off the wrapper's enter/leave classes. */
/* Resting (no transition classes applied) state MUST be the visible one.
   Vue strips the -enter-to class the instant the enter transition ends, so
   if the base style were opacity:0 the card would fade in and then vanish —
   exactly the "dialog dropped" bug. So: base = fully visible; the hidden
   offset lives ONLY on the enter-start / leave-end edges. */
.ncn-confirm-card {
  transition: transform 260ms cubic-bezier(0.16, 1, 0.3, 1),
              opacity 260ms ease-out;
  transform: scale(1) translateY(0);
  opacity: 1;
}
.ncn-confirm-fade-enter-from .ncn-confirm-card,
.ncn-confirm-fade-leave-to .ncn-confirm-card {
  transform: scale(0.96) translateY(8px);
  opacity: 0;
}

/* Mobile: bottom-sheet — card slides up from off-screen. Overrides the
   scale animation above. The selector targets the wrapper-enter state
   so initial render and exit both use this transform. */
@media (max-width: 640px) {
  /* Same inversion for the mobile bottom-sheet: rest on-screen, slide in
     from translateY(100%) only at the enter/leave edges. */
  .ncn-confirm-card {
    transform: translateY(0);
    opacity: 1;
    transition: transform 280ms cubic-bezier(0.16, 1, 0.3, 1);
  }
  .ncn-confirm-fade-enter-from .ncn-confirm-card,
  .ncn-confirm-fade-leave-to .ncn-confirm-card {
    transform: translateY(100%);
  }
  .ncn-confirm-fade-leave-active .ncn-confirm-card {
    transition: transform 200ms cubic-bezier(0.4, 0, 0.6, 1);
  }
}
</style>
