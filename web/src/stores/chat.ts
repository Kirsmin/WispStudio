import { computed, reactive, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import { useConnectionStore } from './connection'
import { useSessionsStore } from './sessions'

export type StreamPhase = 'idle' | 'waiting' | 'reasoning' | 'answer' | 'done' | 'error'
export type MessageStatus = 'complete' | 'streaming' | 'background' | 'aborted' | 'error'

export interface ToolView {
  id: string
  name: string
  status: 'detecting' | 'completed' | 'failed'
  output?: string
  error?: string
}

export interface ChatMessage {
  id: string
  type: 'user' | 'assistant'
  content: string
  reasoning?: string
  provider?: string
  model?: string
  thinking?: string
  tools?: ToolView[]
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
  status?: MessageStatus
}

export const OPENAI_REASONING_LEVELS = [
  'default', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max',
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
  if (model.thinking_style === 'disabled') return ['default']
  if (configured.length === 0 || configured.every(level => level === 'off' || level === 'default')) {
    return [...OPENAI_REASONING_LEVELS]
  }
  return configured
}

type SSEMessage = { event: string; data: string }

class SSEDecoder {
  private buffer = ''

  feed(chunk: string): SSEMessage[] {
    this.buffer = (this.buffer + chunk).replace(/\r\n/g, '\n')
    const out: SSEMessage[] = []
    let index = this.buffer.indexOf('\n\n')
    while (index >= 0) {
      const block = this.buffer.slice(0, index)
      this.buffer = this.buffer.slice(index + 2)
      const parsed = this.parse(block)
      if (parsed) out.push(parsed)
      index = this.buffer.indexOf('\n\n')
    }
    return out
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
      if (line.startsWith('event:')) event = line.slice(6).trim()
      else if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''))
    }
    return data.length ? { event, data: data.join('\n') } : null
  }
}

function parseJSON(data: string): Record<string, any> {
  try {
    return JSON.parse(data) as Record<string, any>
  } catch {
    return {}
  }
}

export const useChatStore = defineStore('chat', () => {
  const messages = ref<ChatMessage[]>([])
  const inputText = ref('')
  const isStreaming = ref(false)
  const backgroundGenerating = ref(false)
  const notice = ref('')
  const selectedProvider = ref('')
  const selectedModel = ref('')
  const selectedThinking = ref('default')
  const abortController = ref<AbortController | null>(null)

  const connectionStore = useConnectionStore()
  const sessionsStore = useSessionsStore()
  let activeRun = 0
  let messageLoadSeq = 0
  let backgroundTimer: number | null = null

  const isBusy = computed(() => isStreaming.value || backgroundGenerating.value)
  const thinkingOptions = computed(() => modelThinkingLevels(currentModel()))

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
    if (!selectedProvider.value || !providersWithModels.some(provider => provider.id === selectedProvider.value)) {
      const provider = providersWithModels.find(item => item.default && item.available)
        || providersWithModels.find(item => item.available)
        || providersWithModels[0]
      selectedProvider.value = provider?.id || connectionStore.models[0]?.provider_id || ''
    }
    const candidates = providerModels()
    if (!candidates.some(model => model.id === selectedModel.value)) {
      selectedModel.value = (candidates.find(model => model.default) || candidates[0])?.id || ''
    }
    const levels = modelThinkingLevels(currentModel())
    if (!levels.includes(selectedThinking.value)) {
      selectedThinking.value = levels.includes('default') ? 'default' : levels[0]
    }
  }

  watch(() => [connectionStore.providers, connectionStore.models], ensureSelection, { immediate: true, deep: true })
  watch(selectedProvider, ensureSelection)
  watch(selectedModel, () => {
    const levels = modelThinkingLevels(currentModel())
    if (!levels.includes(selectedThinking.value)) selectedThinking.value = levels.includes('default') ? 'default' : levels[0]
  })

  function applySessionSelection(sessionId: string) {
    const session = sessionsStore.sessions.find(item => item.id === sessionId)
    if (!session) return
    if (session.provider && connectionStore.models.some(model => model.provider_id === session.provider)) {
      selectedProvider.value = session.provider
    }
    if (session.model && connectionStore.models.some(model => model.id === session.model)) {
      selectedModel.value = session.model
    }
    ensureSelection()
  }

  function normalizeMessage(m: any): ChatMessage {
    return {
      id: String(m.id || `message_${Date.now()}`),
      type: m.type === 'user' ? 'user' : 'assistant',
      content: String(m.content || ''),
      reasoning: String(m.reasoning || ''),
      provider: m.provider,
      model: m.model,
      thinking: m.thinking,
      tools: Array.isArray(m.tools) ? m.tools.map((tool: any) => ({
        id: String(tool.id || ''),
        name: String(tool.name || ''),
        status: tool.status === 'failed' ? 'failed' : (tool.status === 'running' ? 'detecting' : 'completed'),
        output: tool.output ? String(tool.output) : undefined,
        error: tool.error ? String(tool.error) : undefined,
      })) : [],
      usage: m.usage,
      duration_ms: m.duration_ms,
      ttft_ms: m.ttft_ms,
      finish: m.finish,
      error: m.error,
      streaming: false,
      phase: m.error ? 'error' : 'done',
      status: m.error ? 'error' : (m.finish === 'aborted' ? 'aborted' : 'complete'),
    }
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
    if (seq !== messageLoadSeq || sessionsStore.currentSessionId !== sessionId || isStreaming.value) return
    messages.value = (Array.isArray(data) ? data : []).map(normalizeMessage)
  }

  function createDeltaBatcher(getMessage: () => ChatMessage | null) {
    let reasoning = ''
    let content = ''
    let frame: number | null = null

    function flush() {
      frame = null
      const target = getMessage()
      if (!target) {
        reasoning = ''
        content = ''
        return
      }
      if (reasoning) {
        target.reasoning = (target.reasoning || '') + reasoning
        if (!target.content) target.phase = 'reasoning'
        reasoning = ''
      }
      if (content) {
        target.content += content
        target.phase = 'answer'
        content = ''
      }
    }

    function schedule() {
      if (frame == null) frame = requestAnimationFrame(flush)
    }

    return {
      reasoning(text: string) { reasoning += text; schedule() },
      content(text: string) { content += text; schedule() },
      flush() {
        if (frame != null) cancelAnimationFrame(frame)
        flush()
      },
      cancel() {
        if (frame != null) cancelAnimationFrame(frame)
        frame = null
        reasoning = ''
        content = ''
      },
    }
  }

  function createAssistant(payload: Record<string, any>): ChatMessage {
    const message = reactive<ChatMessage>({
      id: String(payload.call_id || `stream_${Date.now()}`),
      type: 'assistant',
      content: '',
      reasoning: '',
      provider: String(payload.provider || selectedProvider.value),
      model: String(payload.model || selectedModel.value),
      thinking: String(payload.thinking || selectedThinking.value),
      tools: [],
      streaming: true,
      phase: 'waiting',
      status: 'streaming',
    })
    messages.value.push(message)
    return message
  }

  function findTool(message: ChatMessage | null, id: string): ToolView | undefined {
    return message?.tools?.find(tool => tool.id === id)
  }

  function finishToolAfterPaint(message: ChatMessage | null, payload: Record<string, any>, status: 'completed' | 'failed') {
    if (!message) return
    const id = String(payload.call_id || '')
    const apply = () => {
      let tool = findTool(message, id)
      if (!tool) {
        message.tools ||= []
        tool = reactive<ToolView>({ id, name: '', status })
        message.tools.push(tool)
      }
      tool.name = String(payload.name || tool.name || '')
      tool.status = status
      tool.output = payload.output ? String(payload.output) : undefined
      tool.error = payload.error ? String(payload.error) : undefined
    }
    const tool = findTool(message, id)
    if (!tool || tool.status !== 'detecting') {
      apply()
      return
    }
    window.setTimeout(apply, 80)
  }

  async function sendMessage() {
    const text = inputText.value.trim()
    if (!text || !connectionStore.isConnected || isBusy.value || !selectedModel.value) return

    ensureSelection()
    const provider = selectedProvider.value
    const model = selectedModel.value
    const thinking = selectedThinking.value
    const run = ++activeRun
    const userMsg = reactive<ChatMessage>({
      id: `temp_user_${Date.now()}`,
      type: 'user',
      content: text,
      provider,
      model,
      thinking,
      phase: 'done',
      status: 'complete',
    })
    messages.value.push(userMsg)
    inputText.value = ''
    isStreaming.value = true
    backgroundGenerating.value = false
    notice.value = ''

    let currentId = sessionsStore.currentSessionId
    let currentAssistant: ChatMessage | null = null
    let gotSSE = false
    let finalError = ''
    const controller = new AbortController()
    abortController.value = controller
    const batcher = createDeltaBatcher(() => currentAssistant)

    try {
      if (!currentId) {
        const session = await sessionsStore.createPersistedSession(text.slice(0, 20))
        currentId = session.id
      }
      if (!currentId || run !== activeRun) return

      const decoder = new SSEDecoder()
      const textDecoder = new TextDecoder()
      const handleEvent = (event: SSEMessage) => {
        gotSSE = true
        const payload = parseJSON(event.data)
        switch (event.event) {
          case 'ack': {
            const saved = payload.message
            if (saved?.id) userMsg.id = String(saved.id)
            break
          }
          case 'model.start':
            batcher.flush()
            if (currentAssistant?.streaming) {
              currentAssistant.streaming = false
              currentAssistant.phase = 'done'
              currentAssistant.status = 'complete'
            }
            currentAssistant = createAssistant(payload)
            break
          case 'ttft': {
            if (!currentAssistant) currentAssistant = createAssistant({})
            const value = Number(payload.ms)
            if (Number.isFinite(value)) currentAssistant.ttft_ms = value
            break
          }
          case 'reasoning':
            if (!currentAssistant) currentAssistant = createAssistant({})
            batcher.reasoning(String(payload.text || ''))
            break
          case 'delta':
            if (!currentAssistant) currentAssistant = createAssistant({})
            batcher.content(String(payload.text || ''))
            break
          case 'usage':
            if (currentAssistant) currentAssistant.usage = payload as ChatMessage['usage']
            break
          case 'tool.detecting': {
            batcher.flush()
            if (!currentAssistant) currentAssistant = createAssistant({})
            currentAssistant.phase = currentAssistant.content ? 'answer' : 'done'
            const id = String(payload.call_id || `tc_${Date.now()}`)
            if (!findTool(currentAssistant, id)) {
              currentAssistant.tools ||= []
              currentAssistant.tools.push(reactive<ToolView>({ id, name: '', status: 'detecting' }))
            }
            break
          }
          case 'tool.completed':
            batcher.flush()
            finishToolAfterPaint(currentAssistant, payload, 'completed')
            break
          case 'tool.failed':
            batcher.flush()
            finishToolAfterPaint(currentAssistant, payload, 'failed')
            break
          case 'model.done': {
            batcher.flush()
            if (!currentAssistant) break
            currentAssistant.streaming = false
            currentAssistant.finish = String(payload.finish || 'stop')
            currentAssistant.duration_ms = Number.isFinite(Number(payload.duration_ms)) ? Number(payload.duration_ms) : currentAssistant.duration_ms
            currentAssistant.ttft_ms = Number.isFinite(Number(payload.ttft_ms)) ? Number(payload.ttft_ms) : currentAssistant.ttft_ms
            if (payload.error) currentAssistant.error = String(payload.error)
            currentAssistant.phase = currentAssistant.error ? 'error' : 'done'
            currentAssistant.status = currentAssistant.error ? 'error' : (currentAssistant.finish === 'aborted' ? 'aborted' : 'complete')
            break
          }
          case 'error':
            finalError = String(payload.message || '生成失败')
            if (currentAssistant) currentAssistant.error = finalError
            break
          case 'done':
            if (payload.error) finalError = String(payload.error)
            break
        }
      }

      const res = await fetch(`${connectionStore.serverUrl}/api/sessions/${encodeURIComponent(currentId)}/chat`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
        body: JSON.stringify({ message: text, provider, model, thinking }),
        signal: controller.signal,
      })
      if (!res.ok) {
        const body = (await res.text()).trim()
        throw new Error(body || `发送失败 (${res.status})`)
      }
      if (!res.body) throw new Error('浏览器没有拿到流式响应体')

      const reader = res.body.getReader()
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        for (const event of decoder.feed(textDecoder.decode(value, { stream: true }))) handleEvent(event)
      }
      for (const event of decoder.feed(textDecoder.decode())) handleEvent(event)
      for (const event of decoder.flush()) handleEvent(event)
      batcher.flush()

      if (run !== activeRun || sessionsStore.currentSessionId !== currentId) return
      if (currentAssistant?.streaming) {
        currentAssistant.streaming = false
        currentAssistant.phase = finalError ? 'error' : 'done'
        currentAssistant.status = finalError ? 'error' : 'complete'
        currentAssistant.error = finalError || currentAssistant.error
      }
      if (finalError) window.$message?.error(finalError)
      await sessionsStore.loadSessions().catch(() => undefined)
    } catch (error: any) {
      batcher.flush()
      if (error?.name === 'AbortError' || run !== activeRun) return
      const message = error instanceof Error ? error.message : String(error)
      if (currentAssistant) {
        currentAssistant.streaming = false
        currentAssistant.phase = 'error'
        currentAssistant.status = 'error'
        currentAssistant.finish = 'error'
        currentAssistant.error = message
      } else {
        messages.value.push(reactive<ChatMessage>({
          id: `error_${Date.now()}`, type: 'assistant', content: '', error: message,
          phase: 'error', status: 'error', streaming: false,
        }))
      }
      if (!gotSSE) window.$message?.error(message)
      console.error('发送失败', error)
    } finally {
      batcher.cancel()
      if (run === activeRun) {
        isStreaming.value = false
        if (abortController.value === controller) abortController.value = null
        if (currentId) await refreshRunStatus(currentId)
      }
    }
  }

  async function refreshRunStatus(sessionId: string) {
    if (!sessionId || !connectionStore.isConnected) return false
    try {
      const res = await fetch(`${connectionStore.serverUrl}/api/sessions/${encodeURIComponent(sessionId)}/chat/status`, { cache: 'no-store' })
      if (!res.ok) return false
      const data = await res.json() as { active?: boolean }
      const active = Boolean(data.active)
      if (sessionsStore.currentSessionId === sessionId) backgroundGenerating.value = active && !isStreaming.value
      if (active && sessionsStore.currentSessionId === sessionId) scheduleBackgroundPoll(sessionId)
      return active
    } catch {
      return false
    }
  }

  function scheduleBackgroundPoll(sessionId: string) {
    if (backgroundTimer != null) window.clearTimeout(backgroundTimer)
    backgroundTimer = window.setTimeout(async () => {
      backgroundTimer = null
      if (sessionsStore.currentSessionId !== sessionId) return
      const active = await refreshRunStatus(sessionId)
      if (!active) {
        backgroundGenerating.value = false
        await loadMessages(sessionId)
        await sessionsStore.loadSessions().catch(() => undefined)
      }
    }, 1000)
  }

  async function stopGeneration() {
    const sessionId = sessionsStore.currentSessionId
    if (sessionId && connectionStore.isConnected) {
      await fetch(`${connectionStore.serverUrl}/api/sessions/${encodeURIComponent(sessionId)}/chat/cancel`, { method: 'POST' }).catch(() => undefined)
    }
    activeRun++
    abortController.value?.abort()
    abortController.value = null
    isStreaming.value = false
    backgroundGenerating.value = false
    const last = [...messages.value].reverse().find(message => message.type === 'assistant' && (message.streaming || message.status === 'background'))
    if (last) {
      last.streaming = false
      last.finish = 'aborted'
      last.phase = 'done'
      last.status = 'aborted'
    }
    if (sessionId) window.setTimeout(() => void loadMessages(sessionId), 120)
  }

  function stopStream() {
    void stopGeneration()
  }

  async function openSession(id: string) {
    if (!id) return
    activeRun++
    messageLoadSeq++
    abortController.value?.abort()
    abortController.value = null
    isStreaming.value = false
    backgroundGenerating.value = false
    notice.value = ''
    messages.value = []
    sessionsStore.selectSession(id)
    applySessionSelection(id)
    await loadMessages(id)
    await refreshRunStatus(id)
  }

  function newConversation() {
    activeRun++
    messageLoadSeq++
    abortController.value?.abort()
    abortController.value = null
    isStreaming.value = false
    backgroundGenerating.value = false
    notice.value = ''
    messages.value = []
    sessionsStore.beginNewSession()
  }

  return {
    messages,
    input: inputText,
    inputText,
    isStreaming,
    isBusy,
    backgroundGenerating,
    notice,
    selectedProvider,
    selectedModel,
    selectedThinking,
    thinkingOptions,
    loadMessages,
    sendMessage,
    stopStream,
    stopGeneration,
    openSession,
    newConversation,
  }
})
