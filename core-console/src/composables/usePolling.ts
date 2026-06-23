import { onBeforeUnmount, onMounted, ref, type Ref } from 'vue'

export interface PollingHandle<T> {
  data: Ref<T | null>
  error: Ref<string | null>
  loading: Ref<boolean>
  lastUpdatedAt: Ref<Date | null>
  refresh: () => Promise<void>
}

export function usePolling<T>(
  fetcher: (signal: AbortSignal) => Promise<T>,
  intervalMs = 3000
): PollingHandle<T> {
  const data = ref<T | null>(null) as Ref<T | null>
  const error = ref<string | null>(null)
  const loading = ref(false)
  const lastUpdatedAt = ref<Date | null>(null)

  let timer: ReturnType<typeof setInterval> | null = null
  let inflight: AbortController | null = null

  const tick = async () => {
    if (inflight) inflight.abort()
    inflight = new AbortController()
    loading.value = true
    try {
      const result = await fetcher(inflight.signal)
      data.value = result
      error.value = null
      lastUpdatedAt.value = new Date()
    } catch (e: unknown) {
      if ((e as { name?: string })?.name === 'AbortError') return
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  }

  onMounted(() => {
    void tick()
    timer = setInterval(() => void tick(), intervalMs)
  })

  onBeforeUnmount(() => {
    if (timer) clearInterval(timer)
    if (inflight) inflight.abort()
  })

  return { data, error, loading, lastUpdatedAt, refresh: tick }
}
