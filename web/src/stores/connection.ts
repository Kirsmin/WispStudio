import { computed, ref } from 'vue'
import { defineStore } from 'pinia'

export interface ProviderInfo {
  id: string
  name: string
  default: boolean
  available: boolean
  models_count: number
  error?: string
}

export interface ModelInfo {
  id: string
  name: string
  provider_id: string
  provider_name: string
  default: boolean
  thinking_levels: string[]
  thinking_style: string
}

interface CatalogResponse {
  providers: ProviderInfo[]
  models: ModelInfo[]
  refreshed_at?: string
}

function defaultServerURL(): string {
  return localStorage.getItem('serverUrl') || window.location.origin
}

function normalizeURL(url: string): string {
  const trimmed = url.trim()
  if (!trimmed) return window.location.origin
  return trimmed.replace(/\/+$/, '')
}

export const useConnectionStore = defineStore('connection', () => {
  const isConnected = ref(false)
  const serverUrl = ref(defaultServerURL())
  const latency = ref(0)
  const lastOkTime = ref(0)
  const pingOk = ref(false)
  const providers = ref<ProviderInfo[]>([])
  const models = ref<ModelInfo[]>([])
  const catalogLoading = ref(false)
  const catalogError = ref('')
  const showConnectDialog = ref(false)
  let pingTimer: ReturnType<typeof window.setInterval> | null = null
  let catalogTimer: ReturnType<typeof window.setInterval> | null = null

  const defaultProvider = computed(() =>
    providers.value.find(provider => provider.default)?.id || providers.value[0]?.id || '',
  )

  function api(path: string): string {
    return `${serverUrl.value}${path}`
  }

  async function refreshCatalog(force = false): Promise<void> {
    if (!isConnected.value) return
    catalogLoading.value = true
    catalogError.value = ''
    try {
      const suffix = force ? '?refresh=true' : ''
      const response = await fetch(api(`/api/catalog${suffix}`), { cache: 'no-store' })
      if (!response.ok) {
        throw new Error(await readHTTPError(response, `获取模型目录失败 (${response.status})`))
      }
      const catalog = await response.json() as CatalogResponse
      providers.value = Array.isArray(catalog.providers) ? catalog.providers : []
      models.value = Array.isArray(catalog.models) ? catalog.models : []
      const failures = providers.value.filter(provider => !provider.available && provider.error)
      if (failures.length > 0) {
        catalogError.value = failures.map(provider => `${provider.name}: ${provider.error}`).join('；')
      }
    } catch (error) {
      catalogError.value = error instanceof Error ? error.message : String(error)
      throw error
    } finally {
      catalogLoading.value = false
    }
  }

  async function connect(url: string): Promise<boolean> {
    const normalized = normalizeURL(url)
    try {
      const controller = new AbortController()
      const timeout = window.setTimeout(() => controller.abort(), 4000)
      const start = performance.now()
      const response = await fetch(`${normalized}/api/health`, {
        signal: controller.signal,
        cache: 'no-store',
      })
      window.clearTimeout(timeout)
      if (!response.ok) return false

      serverUrl.value = normalized
      localStorage.setItem('serverUrl', normalized)
      latency.value = Math.max(1, Math.round(performance.now() - start))
      lastOkTime.value = Date.now()
      pingOk.value = true
      isConnected.value = true
      startPing()
      startCatalogRefresh()

      try {
        await refreshCatalog(true)
      } catch {
        // 后端连接本身已成功；单个 Provider 不可用时仍允许进入界面并查看会话。
      }
      return true
    } catch {
      isConnected.value = false
      pingOk.value = false
      return false
    }
  }

  function disconnect(): void {
    isConnected.value = false
    pingOk.value = false
    latency.value = 0
    lastOkTime.value = 0
    providers.value = []
    models.value = []
    catalogError.value = ''
    stopPing()
    stopCatalogRefresh()
  }

  function startPing(): void {
    stopPing()
    pingTimer = window.setInterval(async () => {
      try {
        const controller = new AbortController()
        const timeout = window.setTimeout(() => controller.abort(), 3000)
        const start = performance.now()
        const response = await fetch(api('/api/health'), {
          signal: controller.signal,
          cache: 'no-store',
        })
        window.clearTimeout(timeout)
        pingOk.value = response.ok
        if (response.ok) {
          latency.value = Math.max(1, Math.round(performance.now() - start))
          lastOkTime.value = Date.now()
        }
      } catch {
        pingOk.value = false
      }
    }, 3000)
  }

  function stopPing(): void {
    if (pingTimer !== null) {
      window.clearInterval(pingTimer)
      pingTimer = null
    }
  }

  function startCatalogRefresh(): void {
    stopCatalogRefresh()
    catalogTimer = window.setInterval(() => {
      void refreshCatalog(false).catch(() => undefined)
    }, 60000)
  }

  function stopCatalogRefresh(): void {
    if (catalogTimer !== null) {
      window.clearInterval(catalogTimer)
      catalogTimer = null
    }
  }

  return {
    isConnected,
    serverUrl,
    latency,
    lastOkTime,
    pingOk,
    providers,
    models,
    catalogLoading,
    catalogError,
    showConnectDialog,
    defaultProvider,
    api,
    refreshCatalog,
    connect,
    disconnect,
  }
})

async function readHTTPError(response: Response, fallback: string): Promise<string> {
  try {
    const contentType = response.headers.get('content-type') || ''
    if (contentType.includes('application/json')) {
      const payload = await response.json() as { error?: string }
      return payload.error || fallback
    }
    return (await response.text()).trim() || fallback
  } catch {
    return fallback
  }
}
