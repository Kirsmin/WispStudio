import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
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
  const lastOkTime = ref(Date.now())
  // 最近一次心跳是否成功（响应式），后端宕机时 UI 能立即切到"无响应"
  const pingOk = ref(false)
  // 最近一次聊天的首字延迟（TTFT），有值时优先显示这个
  const ttft = ref(0)
  const models = ref<ModelConfig[]>([])
  const showConnectDialog = ref(false)
  let pingTimer: ReturnType<typeof setInterval> | null = null

  const defaultModel = computed(() => {
    const m = models.value.find(x => x.default)
    return m?.id || models.value[0]?.id || ''
  })

  // UI 显示的延迟：有 TTFT 时显示首字延迟，否则显示心跳 RTT
  const displayLatency = computed(() => {
    if (ttft.value > 0) return ttft.value
    return latency.value
  })

  // 延迟标签：TTFT 时显示 "首字"，否则显示空（保持原有样式）
  const latencyLabel = computed(() => {
    if (ttft.value > 0) return '首字'
    return ''
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
      pingOk.value = true
      serverUrl.value = url
      isConnected.value = true
      // 获取模型列表（映射 Go 大写驼峰到前端小写蛇形）
      const modelsRes = await fetch(`${url}/api/models`)
      if (modelsRes.ok) {
        const raw = await modelsRes.json()
        models.value = raw.map((m: any) => ({
          id: m.ID || m.id || '',
          name: m.Name || m.name || '',
          default: m.Default ?? m.default ?? false,
          thinking_levels: m.ThinkingLevels || m.thinking_levels || [],
          thinking_style: m.ThinkingStyle || m.thinking_style || '',
          provider_name: m.ProviderName || m.provider_name || '',
        }))
      }
      // 连接成功后自动加载会话列表
      const sessionsStore = useSessionsStore()
      await sessionsStore.loadSessions()
      startPing()
      return true
    } catch {
      return false
    }
  }

  function disconnect() {
    isConnected.value = false
    latency.value = 0
    ttft.value = 0
    pingOk.value = false
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
        const ok = res.ok
        if (ok) {
          latency.value = Math.round(performance.now() - start)
          lastOkTime.value = Date.now()
        }
        // 每次心跳都刷新响应式状态：成功置 true，失败置 false（UI 据此显示"无响应"）
        pingOk.value = ok
      } catch {
        pingOk.value = false
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
    displayLatency,
    latencyLabel,
    ttft,
    lastOkTime,
    pingOk,
    models,
    showConnectDialog,
    defaultModel,
    connect,
    disconnect,
  }
})
