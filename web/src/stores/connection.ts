import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { useSessionsStore } from './sessions'

export interface ModelInfo {
  id: string
  name: string
  default: boolean
  thinking_levels: string[]
  thinking_style: string
}

const savedURL = localStorage.getItem('wisp_server_url') || 'http://127.0.0.1:7860'

export const useConnectionStore = defineStore('connection', () => {
  const serverUrl = ref(savedURL)
  const isConnected = ref(false)
  const isConnecting = ref(false)
  const models = ref<ModelInfo[]>([])
  const latencyMs = ref<number | null>(null)
  const lastError = ref('')
  const ttftMs = ref<number | null>(null)
  const displayURL = computed(() => serverUrl.value.replace(/^https?:\/\//, ''))

  function normalizeURL(value: string): string {
    let result = value.trim()
    if (!/^https?:\/\//i.test(result)) result = `http://${result}`
    return result.replace(/\/$/, '')
  }

  async function fetchWithTimeout(url: string, timeoutMs = 4500): Promise<Response> {
    const controller = new AbortController()
    const timer = window.setTimeout(() => controller.abort(), timeoutMs)
    try {
      return await fetch(url, { signal: controller.signal, cache: 'no-store' })
    } finally {
      window.clearTimeout(timer)
    }
  }

  async function connect(value = serverUrl.value): Promise<boolean> {
    const normalized = normalizeURL(value)
    isConnecting.value = true
    lastError.value = ''
    const started = performance.now()
    try {
      const health = await fetchWithTimeout(`${normalized}/api/health`)
      if (!health.ok) throw new Error(`健康检查失败 (${health.status})`)
      latencyMs.value = Math.max(0, Math.round(performance.now() - started))

      serverUrl.value = normalized
      localStorage.setItem('wisp_server_url', normalized)
      const modelsResponse = await fetchWithTimeout(`${normalized}/api/models`, 8000)
      if (!modelsResponse.ok) throw new Error(`读取模型失败 (${modelsResponse.status})`)
      models.value = (await modelsResponse.json()) as ModelInfo[]
      if (models.value.length === 0) throw new Error('服务端没有可用模型，请检查 config.toml')

      isConnected.value = true
      await useSessionsStore().loadSessions()
      return true
    } catch (error) {
      isConnected.value = false
      models.value = []
      latencyMs.value = null
      lastError.value = error instanceof Error ? error.message : String(error)
      return false
    } finally {
      isConnecting.value = false
    }
  }

  async function ping(): Promise<boolean> {
    if (!serverUrl.value) return false
    const started = performance.now()
    try {
      const response = await fetchWithTimeout(`${serverUrl.value}/api/health`, 3000)
      if (!response.ok) throw new Error('offline')
      latencyMs.value = Math.max(0, Math.round(performance.now() - started))
      isConnected.value = true
      return true
    } catch {
      isConnected.value = false
      latencyMs.value = null
      return false
    }
  }

  async function refreshModels(): Promise<void> {
    if (!isConnected.value) return
    const response = await fetch(`${serverUrl.value}/api/models`, { cache: 'no-store' })
    if (!response.ok) throw new Error(`刷新模型失败 (${response.status})`)
    models.value = (await response.json()) as ModelInfo[]
  }

  function disconnect(): void {
    isConnected.value = false
    models.value = []
    latencyMs.value = null
    ttftMs.value = null
    useSessionsStore().clear()
  }

  return {
    serverUrl,
    displayURL,
    isConnected,
    isConnecting,
    models,
    latencyMs,
    lastError,
    ttftMs,
    connect,
    ping,
    refreshModels,
    disconnect,
  }
})
