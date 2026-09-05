import { defineStore } from 'pinia'
import { ref, computed } from 'vue'

export interface ModelConfig {
  id: string
  name: string
  default: boolean
  thinking_levels: string[]
  thinking_style: string
}

export const useConnectionStore = defineStore('connection', () => {
  const isConnected = ref(false)
  const serverUrl = ref(localStorage.getItem('serverUrl') || 'http://127.0.0.1:7860')
  const latency = ref(0)
  const lastOkTime = ref(Date.now())
  const models = ref<ModelConfig[]>([])
  const showConnectDialog = ref(false)
  let pingTimer: ReturnType<typeof setInterval> | null = null

  const defaultModel = computed(() => {
    const m = models.value.find(x => x.default)
    return m?.id || models.value[0]?.id || ''
  })

  async function connect(url: string): Promise<boolean> {
    try {
      const controller = new AbortController()
      const timeout = setTimeout(() => controller.abort(), 3000)
      const start = performance.now()
      const res = await fetch(`${url}/api/health`, { signal: controller.signal })
      clearTimeout(timeout)
      if (!res.ok) return false
      latency.value = Math.round(performance.now() - start)
      lastOkTime.value = Date.now()
      serverUrl.value = url
      isConnected.value = true
      // 获取模型列表
      const modelsRes = await fetch(`${url}/api/models`)
      if (modelsRes.ok) {
        models.value = await modelsRes.json()
      }
      startPing()
      return true
    } catch {
      return false
    }
  }

  function disconnect() {
    isConnected.value = false
    latency.value = 0
    models.value = []
    stopPing()
  }

  function startPing() {
    stopPing()
    pingTimer = setInterval(async () => {
      try {
        const controller = new AbortController()
        const timeout = setTimeout(() => controller.abort(), 3000)
        const start = performance.now()
        const res = await fetch(`${serverUrl.value}/api/health`, { signal: controller.signal })
        clearTimeout(timeout)
        if (res.ok) {
          latency.value = Math.round(performance.now() - start)
          lastOkTime.value = Date.now()
        }
      } catch {
        // ping 失败，不更新 lastOkTime
      }
    }, 3000)
  }

  function stopPing() {
    if (pingTimer) {
      clearInterval(pingTimer)
      pingTimer = null
    }
  }

  return {
    isConnected,
    serverUrl,
    latency,
    lastOkTime,
    models,
    showConnectDialog,
    defaultModel,
    connect,
    disconnect,
  }
})
