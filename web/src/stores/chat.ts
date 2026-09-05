import { defineStore } from 'pinia'
import { reactive, ref, watch } from 'vue'
import { useConnectionStore } from './connection'
import { useSessionsStore } from './sessions'

export type StreamPhase = 'idle' | 'waiting' | 'reasoning' | 'answer' | 'done' | 'error'

export interface ChatMessage {
  id: string
  type: 'user' | 'assistant'
  content: string
  reasoning?: string
  provider?: string
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
type VisualPartKind = 'reasoning' | 'answer'

interface VisualPart {
  kind: VisualPartKind
  chars: string[]
  offset: number
}

// 这是“视觉流”的节奏，而不是网络节流。
// 即使浏览器一次 reader.read() 收到几十个 SSE delta，或者代理/TCP 把小包合并，
// 也会在多个 animation frame 中逐步写入 Vue 的响应式 message，从而保证中间态可见。
const TYPEWRITER_MIN_CHARS_PER_FRAME = 2
const TYPEWRITER_MAX_CHARS_PER_FRAME = 24
const TYPEWRITER_TARGET_DRAIN_FRAMES = 24
const MIN_REASONING_VISIBLE_MS = 180

class VisualStreamPump {
  private queue: VisualPart[] = []
  private queuedChars = 0
  private rafId: number | null = null
  private stopped = false
  private reasoningVisibleAt = 0
  private drainResolvers: Array<() => void> = []

  constructor(
    private readonly target: ChatMessage,
    private readonly isActive: () => boolean,
  ) {}

  enqueue(kind: VisualPartKind, text: string): void {
    if (this.stopped || !text) return
    const chars = Array.from(text)
    if (chars.length === 0) return

    const last = this.queue[this.queue.length - 1]
    if (last && last.kind === kind && last.offset === 0) {
      last.chars.push(...chars)
    } else {
      this.queue.push({ kind, chars, offset: 0 })
    }
    this.queuedChars += chars.length
    this.ensureFrame()
  }

  drain(): Promise<void> {
    if (this.stopped || this.queuedChars === 0) return Promise.resolve()
    this.ensureFrame()
    return new Promise(resolve => this.drainResolvers.push(resolve))
  }

  cancel(): void {
    if (this.stopped) return
    this.stopped = true
    this.queue = []
    this.queuedChars = 0
    if (this.rafId !== null) {
      window.cancelAnimationFrame(this.rafId)
      this.rafId = null
    }
    this.resolveDrainWaiters()
  }

  private ensureFrame(): void {
    if (this.stopped || this.rafId !== null || this.queuedChars === 0) return
    this.rafId = window.requestAnimationFrame(timestamp => this.renderFrame(timestamp))
  }

  private renderFrame(timestamp: number): void {
    this.rafId = null
    if (this.stopped || !this.isActive()) {
      this.cancel()
      return
    }

    const first = this.queue[0]
    if (!first) {
      this.resolveDrainWaiters()
      return
    }

    // 如果 reasoning 和 answer 在同一个网络批次里到达，不能同一帧“展开又折叠”。
    // 至少让 reasoning 区域真实绘制一小段时间，用户能看到思考中间态。
    if (
      first.kind === 'answer' &&
      this.target.phase === 'reasoning' &&
      this.reasoningVisibleAt > 0 &&
      timestamp - this.reasoningVisibleAt < MIN_REASONING_VISIBLE_MS
    ) {
      this.ensureFrame()
      return
    }

    const frameKind = first.kind
    const adaptive = Math.ceil(this.queuedChars / TYPEWRITER_TARGET_DRAIN_FRAMES)
    let budget = Math.min(
      TYPEWRITER_MAX_CHARS_PER_FRAME,
      Math.max(TYPEWRITER_MIN_CHARS_PER_FRAME, adaptive),
    )

    // 一个 frame 只渲染一种阶段。这样 reasoning -> answer 的阶段切换一定跨越浏览器绘制边界。
    while (budget > 0 && this.queue.length > 0 && this.queue[0].kind === frameKind) {
      const part = this.queue[0]
      const remaining = part.chars.length - part.offset
      const take = Math.min(budget, remaining)
      const text = part.chars.slice(part.offset, part.offset + take).join('')

      if (frameKind === 'reasoning') {
        if (this.target.phase !== 'reasoning') {
          this.target.phase = 'reasoning'
          this.reasoningVisibleAt = timestamp
        }
        this.target.reasoning = (this.target.reasoning || '') + text
      } else {
        this.target.phase = 'answer'
        this.target.content += text
      }

      part.offset += take
      this.queuedChars -= take
      budget -= take
      if (part.offset >= part.chars.length) this.queue.shift()
    }

    if (this.queuedChars > 0) {
      this.ensureFrame()
    } else {
      this.resolveDrainWaiters()
    }
  }

  private resolveDrainWaiters(): void {
    const waiters = this.drainResolvers.splice(0)
    for (const resolve of waiters) resolve()
  }
}

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
  let cancelVisualStream: (() => void) | null = null

  const connectionStore = useConnectionStore()
  const sessionsStore = useSessionsStore()

  function modelsForProvider(providerId: string) {
    if (!providerId) return connectionStore.models
    return connectionStore.models.filter(model => model.provider_id === providerId)
  }

  function syncCatalogSelection(): void {
    const providers = connectionStore.providers
    if (providers.length > 0 && !providers.some(provider => provider.id === selectedProvider.value)) {
      selectedProvider.value = connectionStore.defaultProvider || providers[0].id
    }

    const candidates = modelsForProvider(selectedProvider.value)
    if (candidates.length === 0) {
      selectedModel.value = ''
      return
    }
    if (!candidates.some(model => model.id === selectedModel.value)) {
      const preferred = candidates.find(model => model.default)
      selectedModel.value = preferred?.id || candidates[0].id
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

  watch(selectedModel, (modelId) => {
    const model = connectionStore.models.find(item =>
      item.id === modelId && (!selectedProvider.value || item.provider_id === selectedProvider.value),
    )
    if (model && selectedProvider.value !== model.provider_id) {
      selectedProvider.value = model.provider_id
    }
    const levels = modelThinkingLevels(model)
    if (!levels.includes(selectedThinking.value)) {
      selectedThinking.value = levels.includes('default') ? 'default' : levels[0]
    }
  }, { immediate: true })

  watch(() => sessionsStore.currentSessionId, (sessionId) => {
    activeRun++
    cancelVisualStream?.()
    cancelVisualStream = null
    abortController.value?.abort()
    abortController.value = null
    isStreaming.value = false
    messages.value = []

    const session = sessionsStore.sessions.find(item => item.id === sessionId)
    if (!session) return
    if (session.provider && connectionStore.providers.some(provider => provider.id === session.provider)) {
      selectedProvider.value = session.provider
    }
    const sessionModel = connectionStore.models.find(model =>
      model.id === session.model && (!session.provider || model.provider_id === session.provider),
    )
    if (sessionModel) {
      selectedProvider.value = sessionModel.provider_id
      selectedModel.value = sessionModel.id
    }
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
      provider: selectedProvider.value,
      model: selectedModel.value,
      thinking: selectedThinking.value,
      phase: 'done',
    }

    // 必须保留这个 reactive()：之前把普通对象 push 进 reactive 数组后，
    // SSE 回调仍直接修改那个“原始对象”，绕过了 Vue Proxy，导致每个 delta 都没有触发视图更新；
    // 最后 loadMessages() 替换整个数组时才突然一次性显示完整回复。
    const assistantMsg = reactive<ChatMessage>({
      id: `stream_${Date.now()}`,
      type: 'assistant',
      content: '',
      reasoning: '',
      provider: selectedProvider.value,
      model: selectedModel.value,
      thinking: selectedThinking.value,
      streaming: true,
      phase: 'waiting',
    })

    // 先把 user + assistant 占位放进 UI，再发请求；用户点击发送后立即有反馈。
    messages.value.push(userMsg, assistantMsg)
    inputText.value = ''
    isStreaming.value = true
    abortController.value = new AbortController()
    const localController = abortController.value

    const visualStream = new VisualStreamPump(assistantMsg, () => run === activeRun)
    cancelVisualStream = () => visualStream.cancel()

    const decoder = new SSEDecoder()
    const textDecoder = new TextDecoder()
    let gotSSE = false
    let finalUsage: ChatMessage['usage'] | undefined
    let finalError = ''
    let finalFinish = ''
    let finalDurationMs: number | undefined
    let finalTTFTMs: number | undefined

    const handleEvent = (message: SSEMessage) => {
      if (run !== activeRun) return
      gotSSE = true
      const payload = parseJSON(message.data)
      switch (message.event) {
        case 'start':
          assistantMsg.phase = 'waiting'
          break
        case 'ttft': {
          const value = Number(payload.ms)
          if (Number.isFinite(value)) {
            finalTTFTMs = value
            assistantMsg.ttft_ms = value
          }
          break
        }
        case 'reasoning': {
          const textPart = String(payload.text || '')
          if (textPart) visualStream.enqueue('reasoning', textPart)
          break
        }
        case 'delta': {
          const textPart = String(payload.text || '')
          if (textPart) visualStream.enqueue('answer', textPart)
          break
        }
        case 'usage':
          finalUsage = payload as unknown as ChatMessage['usage']
          break
        case 'error':
          finalError = String(payload.message || '生成失败')
          break
        case 'done': {
          finalFinish = String(payload.finish || 'stop')
          if (payload.error) finalError = String(payload.error)
          if (Number.isFinite(Number(payload.duration_ms))) finalDurationMs = Number(payload.duration_ms)
          if (Number.isFinite(Number(payload.ttft_ms))) finalTTFTMs = Number(payload.ttft_ms)
          break
        }
      }
    }

    const finishVisualMessage = async (): Promise<void> => {
      await visualStream.drain()
      if (run !== activeRun) return

      assistantMsg.streaming = false
      assistantMsg.usage = finalUsage
      assistantMsg.finish = finalFinish || (finalError ? 'error' : 'stop')
      assistantMsg.duration_ms = finalDurationMs
      if (finalTTFTMs !== undefined) assistantMsg.ttft_ms = finalTTFTMs
      if (finalError) assistantMsg.error = finalError

      if (assistantMsg.error) {
        assistantMsg.phase = 'error'
      } else if (assistantMsg.content || assistantMsg.reasoning) {
        assistantMsg.phase = 'done'
      } else {
        assistantMsg.phase = 'error'
        assistantMsg.error = '模型没有返回可显示的内容'
      }
    }

    try {
      const res = await fetch(`${connectionStore.serverUrl}/api/sessions/${encodeURIComponent(currentId)}/chat`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
        body: JSON.stringify({
          message: text,
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

      // 关键：不要在网络流结束时立刻 loadMessages()。
      // 网络可能已经把全部 token 收完，但视觉流还在逐帧输出；如果此时重新读取数据库，
      // 会用“完整文本”替换当前消息，再次造成一瞬间全出来。
      await finishVisualMessage()

      if (run === activeRun && sessionsStore.currentSessionId === currentId) {
        await loadMessages(currentId)
        await sessionsStore.loadSessions().catch(() => undefined)
      }
    } catch (e: any) {
      if (e?.name !== 'AbortError' && run === activeRun) {
        finalError = e instanceof Error ? e.message : String(e)
        await finishVisualMessage()
        if (!gotSSE) window.$message?.error(finalError)
        console.error('发送失败', e)
      }
    } finally {
      if (run === activeRun) {
        isStreaming.value = false
        if (abortController.value === localController) abortController.value = null
        if (cancelVisualStream) cancelVisualStream = null
      }
    }
  }

  function stopStream() {
    activeRun++
    cancelVisualStream?.()
    cancelVisualStream = null
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
