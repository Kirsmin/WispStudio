import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { useSessionsStore } from './sessions'

export interface ModelConfig {
  id: string
  name: string
  default: boolean
  thinking_levels: string[]
  thinking_style: string
  provider_name?: string
}

export const useConnectionStore = defineStore('connection', () => {
  const isConnected = ref(false)
  const serverUrl = ref(localStorage.getItem('serverUrl') || 'http://127.0.0.1:7860')
  const latency = ref(0)
  const ttft = ref(0)
  const pingOk = ref(false)
  const models = ref<ModelConfig[]>([])
  const showConnectDialog = ref(false)
  let pingTimer: ReturnType<typeof setInterval> | null = null

  const defaultModel = computed(() => models.value.find(model => model.default)?.id || models.value[0]?.id || '')
  const displayLatency = computed(() => ttft.value > 0 ? ttft.value : latency.value)
  const latencyLabel = computed(() => ttft.value > 0 ? '首字' : '')

  function normalizeURL(url: string): string { return url.trim().replace(/\/+$/, '') }
  async function refreshModels(): Promise<void> {
    const response = await fetch(`${serverUrl.value}/api/models`, { cache: 'no-store' })
    if (!response.ok) throw new Error(`获取模型失败 (${response.status})`)
    const raw = await response.json() as Array<Record<string, unknown>>
    models.value = raw.map(item => ({
      id: String(item.id ?? item.ID ?? ''),
      name: String(item.name ?? item.Name ?? item.id ?? item.ID ?? ''),
      default: Boolean(item.default ?? item.Default ?? false),
      thinking_levels: Array.isArray(item.thinking_levels ?? item.ThinkingLevels) ? (item.thinking_levels ?? item.ThinkingLevels) as string[] : ['off'],
      thinking_style: String(item.thinking_style ?? item.ThinkingStyle ?? 'none'),
      provider_name: String(item.provider_name ?? item.ProviderName ?? ''),
    })).filter(model => model.id)
  }
  async function connect(url: string): Promise<boolean> {
    const normalized = normalizeURL(url)
    try {
      const controller = new AbortController()
      const timer = window.setTimeout(() => controller.abort(), 4000)
      const start = performance.now()
      const response = await fetch(`${normalized}/api/health`, { signal: controller.signal, cache: 'no-store' })
      window.clearTimeout(timer)
      if (!response.ok) return false
      latency.value = Math.round(performance.now() - start)
      serverUrl.value = normalized
      localStorage.setItem('serverUrl', normalized)
      isConnected.value = true
      pingOk.value = true
      await refreshModels()
      await useSessionsStore().loadSessions()
      startPing()
      return true
    } catch (error) {
      console.error(error)
      isConnected.value = false
      pingOk.value = false
      return false
    }
  }
  function disconnect(): void {
    isConnected.value = false; pingOk.value = false; latency.value = 0; ttft.value = 0; models.value = []; stopPing()
  }
  function startPing(): void {
    stopPing()
    pingTimer = window.setInterval(async () => {
      try {
        const controller = new AbortController(); const timer = window.setTimeout(() => controller.abort(), 3000); const start = performance.now()
        const response = await fetch(`${serverUrl.value}/api/health`, { signal: controller.signal, cache: 'no-store' }); window.clearTimeout(timer)
        pingOk.value = response.ok
        if (response.ok) latency.value = Math.round(performance.now() - start)
      } catch { pingOk.value = false }
    }, 3000)
  }
  function stopPing(): void { if (pingTimer) { window.clearInterval(pingTimer); pingTimer = null } }
  return { isConnected, serverUrl, latency, ttft, pingOk, models, showConnectDialog, defaultModel, displayLatency, latencyLabel, refreshModels, connect, disconnect }
})
