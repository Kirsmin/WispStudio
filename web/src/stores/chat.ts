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

// OpenAI Chat Completions reasoning_effort 是 model-dependent。
// /v1/models 标准对象不声明模型具体支持哪些档位，因此：
// 1) Provider 明确返回 thinking_levels 时优先使用；
// 2) 旧配置只有 ["off"] 时展示 OpenAI-compatible 常见档位，由上游最终校验。
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
    return ['default', 'none', 'low', 'medium', 'high', 'xhigh', 'max']
  }

  if (model.thinking_style === 'enable_thinking') {
    if (configured.some(level => level !== 'off')) return configured
    return ['off', 'on']
  }
  if (model.thinking_style === 'disabled') {
    return ['default']
  }

  if (configured.length === 0 || configured.every(level => level === 'off' || level === 'default')) {
    return [...OPENAI_REASONING_LEVELS]
  }
  return configured
}

type SSEMessage = { event: string; data: string }

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

function visualChunkSize(backlog: number): number {
  // 小块时保持"逐字/逐几个字"的视觉效果；积压很多时快速追赶，避免长回答动画拖太久。
  if (backlog <= 8) return 1
  if (backlog <= 40) return 2
  return Math.min(64, Math.max(3, Math.ceil(backlog / 18)))
}

function takeTextChunk(source: string, count: number): [string, string] {
  let end = Math.min(source.length, count)
  // 不从 UTF-16 surrogate pair 中间切开 emoji / 扩展字符。
  if (end > 0 && end < source.length) {
    const code = source.charCodeAt(end - 1)
    if (code >= 0xD800 && code <= 0xDBFF) end++
  }
  return [source.slice(0, end), source.slice(end)]
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
  let messageLoadSeq = 0
  let cancelActiveVisual: (() => void) | null = null

  const connectionStore = useConnectionStore()
  const sessionsStore = useSessionsStore()

  function providerModels(providerId = selectedProvider.value) {
    if (!providerId) return connectionStore.models
    return connectionStore.models.filter(model => model.provider_id === providerId)
  }

  function currentModel() {
    return connectionStore.models.find(model =>
      model.id === selectedModel.value && (!selectedProvider.value || model.provider_id === selectedProvider.value),
    )
  }

  function ensureSelection() {
    const providersWithModels = connectionStore.providers.filter(provider =>
      connectionStore.models.some(model => model.provider_id === provider.id),
    )
    const currentProviderValid = selectedProvider.value && providersWithModels.some(provider => provider.id === selectedProvider.value)

    if (!currentProviderValid) {
      const defaultProvider = providersWithModels.find(provider => provider.default && provider.available)
        || providersWithModels.find(provider => provider.available)
        || providersWithModels[0]
      selectedProvider.value = defaultProvider?.id || connectionStore.models[0]?.provider_id || ''
    }

    const candidates = providerModels()
    if (!candidates.some(model => model.id === selectedModel.value)) {
      const defaultModel = candidates.find(model => model.default) || candidates[0]
      selectedModel.value = defaultModel?.id || ''
    }

    const levels = modelThinkingLevels(currentModel())
    if (!levels.includes(selectedThinking.value)) {
      selectedThinking.value = levels.includes('default') ? 'default' : levels[0]
    }
  }

  watch(
    () => [connectionStore.providers, connectionStore.models],
    ensureSelection,
    { immediate: true, deep: true },
  )
  watch(selectedProvider, ensureSelection)
  watch(selectedModel, () => {
    const levels = modelThinkingLevels(currentModel())
    if (!levels.includes(selectedThinking.value)) {
      selectedThinking.value = levels.includes('default') ? 'default' : levels[0]
    }
  })

  function applySessionSelection(sessionId: string) {
    const session = sessionsStore.sessions.find(item => item.id === sessionId)
    if (!session) return

    if (session.provider && connectionStore.models.some(model => model.provider_id === session.provider)) {
      selectedProvider.value = session.provider
    }
    if (session.model && connectionStore.models.some(model =>
      model.id === session.model && (!selectedProvider.value || model.provider_id === selectedProvider.value),
    )) {
      selectedModel.value = session.model
    }
    ensureSelection()
  }

  async function loadMessages(sessionId: string) {
    if (!sessionId || !connectionStore.isConnected) {
      messages.value = []
      return
    }

    const seq = ++messageLoadSeq
    const res = await fetch(`${connectionStore.serverUrl}/api/sessions/${encodeURIComponent(sessionId)}/messages`, { cache: 'no-store' })
    if (!res.ok) return
    const data = await res.json()

    // 防止旧的 loadMessages（尤其是"首条消息创建会话"时的空列表请求）覆盖正在流式显示的消息。
    if (seq !== messageLoadSeq) return
    if (sessionsStore.currentSessionId !== sessionId) return
    if (isStreaming.value) return

    messages.value = (Array.isArray(data) ? data : []).map((m: any) => ({
      id: m.id,
      type: m.type,
      content: m.content || '',
      reasoning: m.reasoning || '',
      provider: m.provider,
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

  function createVisualStream(message: ChatMessage, run: number) {
    let reasoningQueue = ''
    let answerQueue = ''
    let networkDone = false
    let cancelled = false
    let frameId: number | null = null
    let answerTransitionDeadline = 0
    let settled = false
    let resolveDrain!: () => void

    const drained = new Promise<void>((resolve) => {
      resolveDrain = resolve
    })

    function settle() {
      if (settled) return
      settled = true
      if (frameId !== null) {
        cancelAnimationFrame(frameId)
        frameId = null
      }
      resolveDrain()
    }

    function schedule() {
      if (cancelled || settled || frameId !== null) return
      frameId = requestAnimationFrame(tick)
    }

    function tick(now: number) {
      frameId = null
      if (cancelled || run !== activeRun) {
        settle()
        return
      }

      // 正常情况下 reasoning 一定在 answer 前。只要正文尚未开始显示，就先把 reasoning 队列画完。
      if (reasoningQueue && !message.content) {
        const [chunk, rest] = takeTextChunk(reasoningQueue, visualChunkSize(reasoningQueue.length))
        reasoningQueue = rest
        message.phase = 'reasoning'
        message.reasoning = (message.reasoning || '') + chunk
        schedule()
        return
      }

      if (answerQueue) {
        // 有可见思考时，在第一段正文前留一个很短的过渡，让"展开思考 -> 折叠 -> 正文"中间态真正能被浏览器画出来。
        if (message.reasoning && !message.content) {
          if (!answerTransitionDeadline) answerTransitionDeadline = now + 90
          if (now < answerTransitionDeadline) {
            schedule()
            return
          }
        }

        const [chunk, rest] = takeTextChunk(answerQueue, visualChunkSize(answerQueue.length))
        answerQueue = rest
        message.phase = 'answer'
        message.content += chunk
        schedule()
        return
      }

      // 极少数兼容端可能在正文后补 reasoning；保留内容，但不要把 UI 从正文重新切回"思考中"。
      if (reasoningQueue) {
        const [chunk, rest] = takeTextChunk(reasoningQueue, visualChunkSize(reasoningQueue.length))
        reasoningQueue = rest
        message.reasoning = (message.reasoning || '') + chunk
        schedule()
        return
      }

      if (networkDone) {
        settle()
      }
    }

    return {
      enqueueReasoning(text: string) {
        if (!text || cancelled) return
        reasoningQueue += text
        schedule()
      },
      enqueueAnswer(text: string) {
        if (!text || cancelled) return
        answerQueue += text
        schedule()
      },
      finishNetwork() {
        networkDone = true
        schedule()
      },
      waitForDrain() {
        return drained
      },
      cancel() {
        cancelled = true
        settle()
      },
    }
  }

  async function sendMessage() {
    const text = inputText.value.trim()
    if (!text || !connectionStore.isConnected || isStreaming.value || !selectedModel.value) return

    ensureSelection()
    const provider = selectedProvider.value
    const model = selectedModel.value
    const thinking = selectedThinking.value
    const run = ++activeRun

    // 关键：首条消息也先进入 UI，再创建持久化会话。
    // currentSessionId 的变化不再由 watch 自动清空/加载消息，因此不会出现"闪一下又回到空白页"。
    const userMsg = reactive<ChatMessage>({
      id: `temp_user_${Date.now()}`,
      type: 'user',
      content: text,
      provider,
      model,
      thinking,
      phase: 'done',
    })
    const assistantMsg = reactive<ChatMessage>({
      id: `stream_${Date.now()}`,
      type: 'assistant',
      content: '',
      reasoning: '',
      provider,
      model,
      thinking,
      streaming: true,
      phase: 'waiting',
    })

    messages.value.push(userMsg, assistantMsg)
    inputText.value = ''
    isStreaming.value = true
    abortController.value = new AbortController()
    const localController = abortController.value

    cancelActiveVisual?.()
    const visual = createVisualStream(assistantMsg, run)
    cancelActiveVisual = visual.cancel

    let currentId = sessionsStore.currentSessionId
    let gotSSE = false
    let finalFinish = ''
    let finalError = ''

    try {
      if (!currentId) {
        const session = await sessionsStore.createPersistedSession(text.slice(0, 20))
        currentId = session.id
      }
      if (!currentId || run !== activeRun) return

      const decoder = new SSEDecoder()
      const textDecoder = new TextDecoder()

      const handleEvent = (message: SSEMessage) => {
        gotSSE = true
        const payload = parseJSON(message.data)
        switch (message.event) {
          case 'start':
            break
          case 'ttft': {
            const value = Number(payload.ms)
            if (Number.isFinite(value)) assistantMsg.ttft_ms = value
            break
          }
          case 'reasoning': {
            const textPart = String(payload.text || '')
            if (textPart) visual.enqueueReasoning(textPart)
            break
          }
          case 'delta': {
            const textPart = String(payload.text || '')
            if (textPart) visual.enqueueAnswer(textPart)
            break
          }
          case 'usage':
            assistantMsg.usage = payload as unknown as ChatMessage['usage']
            break
          case 'error':
            finalError = String(payload.message || '生成失败')
            break
          case 'done': {
            finalFinish = String(payload.finish || 'stop')
            if (payload.error) finalError = String(payload.error)
            if (Number.isFinite(Number(payload.duration_ms))) assistantMsg.duration_ms = Number(payload.duration_ms)
            if (Number.isFinite(Number(payload.ttft_ms))) assistantMsg.ttft_ms = Number(payload.ttft_ms)
            break
          }
        }
      }

      const res = await fetch(`${connectionStore.serverUrl}/api/sessions/${encodeURIComponent(currentId)}/chat`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
        body: JSON.stringify({
          message: text,
          provider,
          model,
          thinking,
        }),
        signal: localController.signal,
      })

      if (res.status === 409) {
        finalError = '该会话有任务正在执行'
        throw new Error(finalError)
      }
      if (!res.ok) {
        const body = (await res.text()).trim()
        finalError = body || `发送失败 (${res.status})`
        throw new Error(finalError)
      }
      if (!res.body) {
        finalError = '浏览器没有拿到流式响应体'
        throw new Error(finalError)
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

      visual.finishNetwork()
      await visual.waitForDrain()

      if (run !== activeRun || sessionsStore.currentSessionId !== currentId) return

      assistantMsg.streaming = false
      assistantMsg.finish = finalFinish || (finalError ? 'error' : 'stop')
      assistantMsg.error = finalError || undefined
      if (finalError) {
        assistantMsg.phase = 'error'
      } else if (assistantMsg.content || assistantMsg.reasoning) {
        assistantMsg.phase = 'done'
      } else {
        assistantMsg.error = '模型没有返回可显示的内容'
        assistantMsg.phase = 'error'
      }

      // 不在这里 loadMessages()：服务端完整消息会直接覆盖视觉队列，重新造成"最后一下全出来"。
      // 只刷新左侧会话标题/更新时间即可；下次真正打开会话时再从服务端读取完整消息。
      await sessionsStore.loadSessions().catch(() => undefined)
    } catch (error: any) {
      visual.finishNetwork()
      await visual.waitForDrain()

      if (error?.name === 'AbortError' || run !== activeRun) return

      const message = finalError || (error instanceof Error ? error.message : String(error))
      assistantMsg.streaming = false
      assistantMsg.phase = 'error'
      assistantMsg.error = message
      assistantMsg.finish = 'error'
      if (!gotSSE || finalError) window.$message?.error(message)
      console.error('发送失败', error)
    } finally {
      if (run === activeRun) {
        isStreaming.value = false
        if (abortController.value === localController) abortController.value = null
        if (cancelActiveVisual === visual.cancel) cancelActiveVisual = null
      }
    }
  }

  function stopStream() {
    activeRun++
    cancelActiveVisual?.()
    cancelActiveVisual = null
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
    if (!id) return

    activeRun++
    messageLoadSeq++
    cancelActiveVisual?.()
    cancelActiveVisual = null
    abortController.value?.abort()
    abortController.value = null
    isStreaming.value = false
    messages.value = []

    sessionsStore.selectSession(id)
    applySessionSelection(id)
    await loadMessages(id)
  }

  function newConversation() {
    activeRun++
    messageLoadSeq++
    cancelActiveVisual?.()
    cancelActiveVisual = null
    abortController.value?.abort()
    abortController.value = null
    isStreaming.value = false
    messages.value = []
    sessionsStore.beginNewSession()
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
