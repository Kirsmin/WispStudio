import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { useConnectionStore } from './connection'
import { useSessionsStore } from './sessions'

export type StreamPhase = 'idle' | 'waiting' | 'reasoning' | 'answer' | 'done' | 'error'

export interface ChatMessage {
  id: string
  type: 'user' | 'assistant'
  content: string
  reasoning?: string
  model?: string
  thinking?: string
  usage?: {
    prompt_tokens: number
    completion_tokens: number
    cached_tokens: number
    reasoning_tokens: number
  }
  duration_ms?: number
  ttft_ms?: number
  finish?: string
  error?: string
  streaming?: boolean
  phase?: StreamPhase
}

// OpenAI Chat Completions reasoning_effort 当前是 model-dependent。
// /v1/models 标准对象只提供基础模型信息，并不声明支持哪些 effort，因此：
// 1) 后端/Provider 若明确返回 thinking_levels，优先使用；
// 2) 旧配置只有 ["off"] 时，UI 暴露 OpenAI 标准 effort 集合，由上游模型做最终校验。
export const OPENAI_REASONING_LEVELS = [
  'default',
  'none',
  'minimal',
  'low',
  'medium',
  'high',
  'xhigh',
  'max',
] as const

export function modelThinkingLevels(model?: { id?: string; thinking_levels?: string[]; thinking_style?: string }): string[] {
  if (!model) return ['default']
  const configured = (model.thinking_levels || [])
    .map(level => String(level).trim().toLowerCase())
    .filter(Boolean)

  const modelId = String(model.id || '').toLowerCase()
  if (modelId.startsWith('deepseek-v4-')) {
    // DeepSeek V4 Chat Completions: none 通过 thinking.type=disabled 表达；
    // effort 映射支持 low/medium/high/xhigh/max，default 表示不覆盖服务端默认。
    return ['default', 'none', 'low', 'medium', 'high', 'xhigh', 'max']
  }

  if (model.thinking_style === 'enable_thinking') {
    if (configured.some(level => level !== 'off')) return configured
    return ['off', 'on']
  }
  if (model.thinking_style === 'disabled') {
    return ['default']
  }

  // 兼容旧版 config.toml 中 thinking_levels=["off"], thinking_style="none"。
  if (configured.length === 0 || configured.every(level => level === 'off' || level === 'default')) {
    return [...OPENAI_REASONING_LEVELS]
  }
  return configured
}

type SSEMessage = { event: string; data: string }

// 增量 SSE 解码器：兼容 \n\n 和 \r\n\r\n，支持多行 data。
class SSEDecoder {
  private buffer = ''

  feed(chunk: string): SSEMessage[] {
    this.buffer += chunk
    this.buffer = this.buffer.replace(/\r\n/g, '\n')
    const output: SSEMessage[] = []
    let index = this.buffer.indexOf('\n\n')
    while (index >= 0) {
      const block = this.buffer.slice(0, index)
      this.buffer = this.buffer.slice(index + 2)
      const parsed = this.parse(block)
      if (parsed) output.push(parsed)
      index = this.buffer.indexOf('\n\n')
    }
    return output
  }

  flush(): SSEMessage[] {
    const parsed = this.parse(this.buffer.trim())
    this.buffer = ''
    return parsed ? [parsed] : []
  }

  private parse(block: string): SSEMessage | null {
    if (!block) return null
    let event = 'message'
    const data: string[] = []
    for (const line of block.split('\n')) {
      if (line.startsWith('event:')) {
        event = line.slice(6).trim()
      } else if (line.startsWith('data:')) {
        data.push(line.slice(5).replace(/^ /, ''))
      }
    }
    if (!data.length) return null
    return { event, data: data.join('\n') }
  }
}

function parseJSON(data: string): Record<string, unknown> {
  try {
    return JSON.parse(data) as Record<string, unknown>
  } catch {
    return {}
  }
}

export const useChatStore = defineStore('chat', () => {
  const messages = ref<ChatMessage[]>([])
  const inputText = ref('')
  const isStreaming = ref(false)
  const selectedProvider = ref('')
  const selectedModel = ref('')
  const selectedThinking = ref('default')
  const abortController = ref<AbortController | null>(null)

  let activeRun = 0

  const connectionStore = useConnectionStore()
  const sessionsStore = useSessionsStore()

  function syncCatalogSelection(): void {
    const providers = connectionStore.providers
    const models = connectionStore.models
    if (models.length === 0) {
      selectedProvider.value = ''
      selectedModel.value = ''
      return
    }

    const providerHasModels = (providerId: string) => models.some(model => model.provider_id === providerId)
    if (!selectedProvider.value || !providers.some(provider => provider.id === selectedProvider.value) || !providerHasModels(selectedProvider.value)) {
      const preferred = providers.find(provider => provider.default && providerHasModels(provider.id))
        || providers.find(provider => providerHasModels(provider.id))
      selectedProvider.value = preferred?.id || models[0].provider_id
    }

    const providerModels = models.filter(model => model.provider_id === selectedProvider.value)
    if (!providerModels.some(model => model.id === selectedModel.value)) {
      const def = providerModels.find(model => model.default)
      selectedModel.value = def?.id || providerModels[0]?.id || ''
    }
  }

  watch(
    [() => connectionStore.providers, () => connectionStore.models],
    syncCatalogSelection,
    { immediate: true, deep: true },
  )

  watch(selectedProvider, () => {
    syncCatalogSelection()
  })

  watch([selectedProvider, selectedModel], ([providerId, modelId]) => {
    const model = connectionStore.models.find(item => item.provider_id === providerId && item.id === modelId)
    const levels = modelThinkingLevels(model)
    if (!levels.includes(selectedThinking.value)) {
      selectedThinking.value = levels.includes('default') ? 'default' : levels[0]
    }
  }, { immediate: true })

  watch(() => sessionsStore.currentSessionId, () => {
    activeRun++
    abortController.value?.abort()
    abortController.value = null
    isStreaming.value = false
    messages.value = []
  })

  async function loadMessages(sessionId: string) {
    if (!sessionId || !connectionStore.isConnected) {
      messages.value = []
      return
    }
    const res = await fetch(`${connectionStore.serverUrl}/api/sessions/${encodeURIComponent(sessionId)}/messages`, { cache: 'no-store' })
    if (!res.ok) return
    const data = await res.json()
    if (sessionsStore.currentSessionId !== sessionId) return
    messages.value = data.map((m: any) => ({
      id: m.id,
      type: m.type,
      content: m.content || '',
      reasoning: m.reasoning || '',
      model: m.model,
      thinking: m.thinking,
      usage: m.usage,
      duration_ms: m.duration_ms,
      ttft_ms: m.ttft_ms,
      finish: m.finish,
      error: m.error,
      streaming: false,
      phase: m.error ? 'error' : 'done',
    }))
  }

  async function sendMessage() {
    const text = inputText.value.trim()
    if (!text || !connectionStore.isConnected || isStreaming.value || !selectedModel.value) return

    if (!sessionsStore.currentSessionId) {
      const res = await fetch(`${connectionStore.serverUrl}/api/sessions`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: text.slice(0, 20) }),
      })
      if (!res.ok) return
      const session = await res.json()
      sessionsStore.currentSessionId = session.id
      await sessionsStore.loadSessions()
    }

    const currentId = sessionsStore.currentSessionId
    if (!currentId) return
    const run = ++activeRun

    const userMsg: ChatMessage = {
      id: `temp_user_${Date.now()}`,
      type: 'user',
      content: text,
      model: selectedModel.value,
      thinking: selectedThinking.value,
      phase: 'done',
    }
    const assistantMsg: ChatMessage = {
      id: `stream_${Date.now()}`,
      type: 'assistant',
      content: '',
      reasoning: '',
      model: selectedModel.value,
      thinking: selectedThinking.value,
      streaming: true,
      phase: 'waiting',
    }

    // 先把 user + assistant 占位放进 UI，再发请求；用户点击发送后立即有反馈。
    messages.value.push(userMsg, assistantMsg)
    inputText.value = ''
    isStreaming.value = true
    abortController.value = new AbortController()
    const localController = abortController.value

    const decoder = new SSEDecoder()
    const textDecoder = new TextDecoder()
    let gotSSE = false

    const handleEvent = (message: SSEMessage) => {
      gotSSE = true
      const payload = parseJSON(message.data)
      switch (message.event) {
        case 'start':
          assistantMsg.phase = 'waiting'
          break
        case 'ttft': {
          const value = Number(payload.ms)
          if (Number.isFinite(value)) assistantMsg.ttft_ms = value
          break
        }
        case 'reasoning': {
          const textPart = String(payload.text || '')
          if (!textPart) break
          assistantMsg.phase = 'reasoning'
          assistantMsg.reasoning = (assistantMsg.reasoning || '') + textPart
          break
        }
        case 'delta': {
          const textPart = String(payload.text || '')
          if (!textPart) break
          // 第一段可见答案到达即代表“思考阶段结束”；MessageItem 会自动折叠思考块。
          assistantMsg.phase = 'answer'
          assistantMsg.content += textPart
          break
        }
        case 'usage':
          assistantMsg.usage = payload as unknown as ChatMessage['usage']
          break
        case 'error':
          assistantMsg.error = String(payload.message || '生成失败')
          assistantMsg.phase = 'error'
          break
        case 'done': {
          assistantMsg.streaming = false
          assistantMsg.finish = String(payload.finish || 'stop')
          if (payload.error) assistantMsg.error = String(payload.error)
          if (Number.isFinite(Number(payload.duration_ms))) assistantMsg.duration_ms = Number(payload.duration_ms)
          if (Number.isFinite(Number(payload.ttft_ms))) assistantMsg.ttft_ms = Number(payload.ttft_ms)
          assistantMsg.phase = assistantMsg.error ? 'error' : 'done'
          break
        }
      }
    }

    try {
      const res = await fetch(`${connectionStore.serverUrl}/api/sessions/${encodeURIComponent(currentId)}/chat`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
        body: JSON.stringify({
          message: text,
          // model ID 在不同 Provider 中可能重名，因此 provider 必须作为选择的一部分发送。
          // 后端仍兼容旧客户端只传 model 的请求。
          provider: selectedProvider.value,
          model: selectedModel.value,
          thinking: selectedThinking.value,
        }),
        signal: localController.signal,
      })

      if (res.status === 409) {
        assistantMsg.streaming = false
        assistantMsg.phase = 'error'
        assistantMsg.error = '该会话有任务正在执行'
        window.$message?.error(assistantMsg.error)
        return
      }
      if (!res.ok) {
        const body = (await res.text()).trim()
        assistantMsg.streaming = false
        assistantMsg.phase = 'error'
        assistantMsg.error = body || `发送失败 (${res.status})`
        window.$message?.error(assistantMsg.error)
        return
      }
      if (!res.body) {
        throw new Error('浏览器没有拿到流式响应体')
      }

      const reader = res.body.getReader()
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        const decoded = textDecoder.decode(value, { stream: true })
        for (const event of decoder.feed(decoded)) handleEvent(event)
      }
      for (const event of decoder.feed(textDecoder.decode())) handleEvent(event)
      for (const event of decoder.flush()) handleEvent(event)

      if (!assistantMsg.error && assistantMsg.phase !== 'done') {
        assistantMsg.streaming = false
        assistantMsg.phase = assistantMsg.content || assistantMsg.reasoning ? 'done' : 'error'
        if (assistantMsg.phase === 'error') assistantMsg.error = '模型没有返回可显示的内容'
      }

      if (run === activeRun && sessionsStore.currentSessionId === currentId) {
        await loadMessages(currentId)
        await sessionsStore.loadSessions().catch(() => undefined)
      }
    } catch (e: any) {
      if (e?.name !== 'AbortError') {
        const message = e instanceof Error ? e.message : String(e)
        assistantMsg.streaming = false
        assistantMsg.phase = 'error'
        assistantMsg.error = message
        if (!gotSSE) window.$message?.error(message)
        console.error('发送失败', e)
      }
    } finally {
      if (run === activeRun) {
        isStreaming.value = false
        if (abortController.value === localController) abortController.value = null
      }
    }
  }

  function stopStream() {
    activeRun++
    abortController.value?.abort()
    abortController.value = null
    isStreaming.value = false
    const last = [...messages.value].reverse().find(message => message.type === 'assistant' && message.streaming)
    if (last) {
      last.streaming = false
      last.finish = 'aborted'
      last.phase = 'done'
    }
  }

  async function openSession(id: string) {
    const session = sessionsStore.sessions.find(item => item.id === id)
    if (session?.provider && connectionStore.models.some(model => model.provider_id === session.provider && model.id === session.model)) {
      selectedProvider.value = session.provider
      selectedModel.value = session.model
    } else if (session?.model) {
      const candidate = connectionStore.models.find(model => model.id === session.model)
      if (candidate) {
        selectedProvider.value = candidate.provider_id
        selectedModel.value = candidate.id
      }
    }
    sessionsStore.currentSessionId = id
    await loadMessages(id)
  }

  function newConversation() {
    sessionsStore.currentSessionId = ''
    messages.value = []
  }

  return {
    messages,
    inputText,
    isStreaming,
    selectedProvider,
    selectedModel,
    selectedThinking,
    loadMessages,
    sendMessage,
    stopStream,
    openSession,
    newConversation,
  }
})
