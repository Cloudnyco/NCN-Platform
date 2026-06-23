<script setup lang="ts">
import { computed } from 'vue'

interface Sample { t: number; v: number }

interface Props {
  series: Sample[]
  height?: number
  width?: number
  color?: string
  yMin?: number
  yMax?: number
  fillBelow?: boolean
  showAxis?: boolean
  unit?: string
}

const props = withDefaults(defineProps<Props>(), {
  height: 60,
  width: 240,
  color: 'rgb(16 185 129)',
  fillBelow: true,
  showAxis: false
})

const PAD = 4

const range = computed(() => {
  const vals = props.series.map((s) => s.v).filter((v) => v >= 0)
  if (vals.length === 0) return { min: 0, max: 1 }
  let min = props.yMin ?? Math.min(...vals)
  let max = props.yMax ?? Math.max(...vals)
  if (min === max) max = min + 1
  const r = max - min
  return { min: min - r * 0.08, max: max + r * 0.12 }
})

const xStep = computed(() => {
  const n = Math.max(1, props.series.length - 1)
  return (props.width - PAD * 2) / n
})

const yScale = computed(() => (props.height - PAD * 2) / (range.value.max - range.value.min))

function project(i: number, v: number): { x: number; y: number } {
  return {
    x: PAD + i * xStep.value,
    y: PAD + (range.value.max - v) * yScale.value
  }
}

const linePath = computed(() => {
  if (props.series.length === 0) return ''
  let d = ''
  let started = false
  for (let i = 0; i < props.series.length; i++) {
    const v = props.series[i].v
    if (v < 0) {
      started = false
      continue
    }
    const { x, y } = project(i, v)
    d += (started ? 'L' : 'M') + x.toFixed(1) + ' ' + y.toFixed(1) + ' '
    started = true
  }
  return d.trim()
})

const fillPath = computed(() => {
  if (!props.fillBelow || !linePath.value) return ''
  const lastIdx = props.series.length - 1
  const baseY = props.height - PAD
  // Build segment-aware fill: each contiguous non-failure run gets its own poly.
  let d = ''
  let inSeg = false
  let segStart = 0
  for (let i = 0; i < props.series.length; i++) {
    const v = props.series[i].v
    if (v < 0) {
      if (inSeg) {
        const last = project(i - 1, props.series[i - 1].v)
        d += `L ${last.x.toFixed(1)} ${baseY} L ${project(segStart, props.series[segStart].v).x.toFixed(1)} ${baseY} Z `
        inSeg = false
      }
      continue
    }
    const { x, y } = project(i, v)
    if (!inSeg) {
      d += `M ${x.toFixed(1)} ${baseY} L ${x.toFixed(1)} ${y.toFixed(1)} `
      segStart = i
      inSeg = true
    } else {
      d += `L ${x.toFixed(1)} ${y.toFixed(1)} `
    }
    if (i === lastIdx && inSeg) {
      d += `L ${x.toFixed(1)} ${baseY} L ${project(segStart, props.series[segStart].v).x.toFixed(1)} ${baseY} Z `
    }
  }
  return d.trim()
})

const lastSample = computed(() => {
  for (let i = props.series.length - 1; i >= 0; i--) {
    if (props.series[i].v >= 0) return props.series[i]
  }
  return null
})

const lastDot = computed(() => {
  if (!lastSample.value) return null
  return project(props.series.indexOf(lastSample.value), lastSample.value.v)
})

const axisTicks = computed(() => {
  if (!props.showAxis) return []
  const { min, max } = range.value
  const n = 4
  const ticks: { y: number; label: string }[] = []
  for (let i = 0; i <= n; i++) {
    const v = min + ((max - min) * i) / n
    ticks.push({
      y: PAD + (range.value.max - v) * yScale.value,
      label: v < 10 ? v.toFixed(1) : Math.round(v).toString()
    })
  }
  return ticks
})
</script>

<template>
  <svg
    :viewBox="`0 0 ${width} ${height}`"
    :style="{ height: height + 'px' }"
    class="block w-full overflow-visible"
    preserveAspectRatio="none"
  >
    <!-- Axis ticks -->
    <g v-if="showAxis" stroke="rgb(var(--g-800))" stroke-width="0.5" fill="none">
      <line v-for="t in axisTicks" :key="t.y" x1="0" :y1="t.y" :x2="width" :y2="t.y" stroke-dasharray="2 2" opacity="0.5" />
    </g>
    <g v-if="showAxis" fill="rgb(var(--g-500))" font-family="JetBrains Mono, monospace" font-size="7">
      <text v-for="t in axisTicks" :key="`l${t.y}`" :x="2" :y="t.y - 1">{{ t.label }}</text>
    </g>

    <!-- Fill below -->
    <path v-if="fillPath" :d="fillPath" :fill="color" opacity="0.18" stroke="none" />

    <!-- Line -->
    <path :d="linePath" :stroke="color" stroke-width="1.4" fill="none" stroke-linejoin="round" stroke-linecap="round" />

    <!-- Last sample dot -->
    <circle v-if="lastDot" :cx="lastDot.x" :cy="lastDot.y" r="2" :fill="color" />
    <circle v-if="lastDot" :cx="lastDot.x" :cy="lastDot.y" r="4" :fill="color" opacity="0.3">
      <animate attributeName="r" values="3;7;3" dur="2s" repeatCount="indefinite" />
      <animate attributeName="opacity" values="0.4;0;0.4" dur="2s" repeatCount="indefinite" />
    </circle>
  </svg>
</template>
