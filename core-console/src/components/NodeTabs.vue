<script setup lang="ts">
import { computed } from 'vue'

interface NodeOpt { id: string; label?: string; ok?: boolean; country?: string }
const props = defineProps<{ modelValue: string; nodes: NodeOpt[] }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: string): void }>()

const tabs = computed(() => props.nodes)

function pick(id: string) {
  if (id !== props.modelValue) emit('update:modelValue', id)
}
</script>

<template>
  <div class="border border-gray-800 bg-gray-900 flex overflow-x-auto">
    <button
      v-for="n in tabs" :key="n.id"
      type="button"
      @click="pick(n.id)"
      :class="[
        'px-4 py-2 text-xs tracking-widest uppercase font-mono whitespace-nowrap border-r border-gray-800 transition-colors duration-75 flex items-center gap-2',
        n.id === modelValue
          ? 'bg-gray-800 text-emerald-500 border-b-2 border-b-emerald-500'
          : 'text-gray-400 hover:text-emerald-500 hover:bg-gray-800/50 border-b-2 border-b-transparent'
      ]"
    >
      <span :class="[
        'inline-block w-1.5 h-1.5',
        n.ok === false ? 'bg-red-500' : 'bg-emerald-500',
        n.id === modelValue && n.ok !== false ? 'animate-pulse' : ''
      ]"></span>
      <span>{{ n.id }}</span>
      <span v-if="n.country" class="text-gray-700 text-[10px]">·</span>
      <span v-if="n.country" class="text-gray-600 text-[10px]">{{ n.country }}</span>
    </button>
  </div>
</template>
