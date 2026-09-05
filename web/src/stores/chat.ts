import { computed, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import { useConnectionStore } from './connection'
import { useSessionsStore } from './sessions'

export interface Usage {
  prompt_tokens: number
  completion_tokens: number
  cached_tokens?: number
  reasoning_tokens?: number
}

export type MessageStatus = 'complete' | 'streaming' | 'background' | 'aborted' | 'error'

export interface ChatMessage {
  id: string
  type: 'user' | 'assistant'
  ts?: string
  content: string
  provider?: string
  model?: string
  thinking?: string
  reasoning?: string
  usage?: Usage | null
  duration_ms?: number
  ttft_ms?: number
  finish?: string
  error?: string
  status?: MessageStatus
}

interface SSEMessage {
  event: string
  data: string
}

class SSEDecoder {
  private buffer = ''

  feed(chunk: string): SSEMessage[] {
    this.buffer += chunk.replace(/\r\n/g, '\n')
    const events: SSEMessage[] = []
    let separator = this.buffer.indexOf('\n\n')
    while (separator >= 0) {
      const block = this.buffer.slice(0, separator)
      this.buffer = this.buffer.slice(separator + 2)
      const event = this.parse(block)
      if (event) events.push(event)
      separator = this.buffer.indexOf('\n\n')
    }
    return events
  }

  flush(): SSEMessage[] {
    const event = this.parse(this.buffer.trim())
    this.buffer = ''
    return event ? [event] : []
  }

  private parse(block: string): SSEMessage | null {
    if (!block) return null
    let event = 'message'
    const data: string[] = []
    for (const line of block.split('\n')) {
      if (line.startsWith('event:')) event = line.slice(6).trim()
      else if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''))
    }
    if (!data.length) return null
    return { event, data: data.join('\n') }
  }
}

export const useChatStore = defineStore('chat', () => {
  const connection = useConnectionStore()
  const sessions = useSessionsStore()

  const messages = ref<ChatMessage[]>([])
  const inputText = ref('')
  const selectedProvider = ref('')
  const selectedModel = ref('')
  const selectedThinking = ref('off')
  const isStreaming = ref(false)
  const backgroundGenerating = ref(false)
  const notice = ref('')

  let controller: AbortController | null = null
  let operation = 0
  let pollTimer: ReturnType<typeof window.setTimeout> | null = null

  const modelsForProvider = computed(() =>
    connection.models.filter(model => model.provider_id === selectedProvider.value),
  )
  const selectedModelInfo = computed(() =>
    modelsForProvider.value.find(model => model.id === selectedModel.value),
  )
  const thinkingLevels = computed(() => {
    const levels = selectedModelInfo.value?.thinking_levels
    return levels && levels.length > 0 ? levels : ['off']
  })
  const isBusy = computed(() => isStreaming.value || backgroundGenerating.value)

  function ensureSelection(): void {
    if (!connection.providers.length) {
      selectedProvider.value = ''
      selectedModel.value = ''
      selectedThinking.value = 'off'
      return
    }

    // 默认落在确实有模型可选的 Provider，避免某一家暂时失效后整个输入区看起来“点不动”。
    const providersWithModels = connection.providers.filter(provider =>
      connection.models.some(model => model.provider_id === provider.id),
    )
    const selectable = providersWithModels.length > 0 ? providersWithModels : connection.providers
    if (!selectable.some(provider => provider.id === selectedProvider.value)) {
      selectedProvider.value = selectable.find(provider => provider.default)?.id || selectable[0]?.id || ''
    }
    ensureModelSelection()
  }

  function ensureModelSelection(): void {
    const candidates = connection.models.filter(model => model.provider_id === selectedProvider.value)
    if (!candidates.length) {
      selectedModel.value = ''
      selectedThinking.value = 'off'
      return
    }
    if (!candidates.some(model => model.id === selectedModel.value)) {
      selectedModel.value = candidates.find(model => model.default)?.id || candidates[0].id
    }
    ensureThinkingSelection()
  }

  function ensureThinkingSelection(): void {
    const levels = selectedModelInfo.value?.thinking_levels?.length
      ? selectedModelInfo.value.thinking_levels
      : ['off']
    if (!levels.includes(selectedThinking.value)) {
      selectedThinking.value = levels.includes('off') ? 'off' : levels[0]
    }
  }

  watch(
    () => connection.providers.map(provider => `${provider.id}:${provider.default}:${provider.available}`).join('|'),
    ensureSelection,
    { immediate: true },
  )
  watch(
    () => connection.models.map(model => `${model.provider_id}:${model.id}:${model.default}:${model.thinking_levels.join(',')}`).join('|'),
    ensureSelection,
    { immediate: true },
  )
  watch(selectedProvider, ensureModelSelection)
  watch(selectedModel, ensureThinkingSelection)

  function clearPoll(): void {
    if (pollTimer !== null) {
      window.clearTimeout(pollTimer)
      pollTimer = null
    }
  }

  function detachLocalStream(): void {
    if (controller) controller.abort()
    controller = null
    isStreaming.value = false
  }

  function newConversation(): void {
    operation++
    detachLocalStream()
    clearPoll()
    sessions.beginNewSession()
    messages.value = []
    backgroundGenerating.value = false
    notice.value = ''
    inputText.value = ''
  }

  async function openSession(sessionId: string): Promise<void> {
    operation++
    const expected = operation
    detachLocalStream()
    clearPoll()
    sessions.selectSession(sessionId)
    messages.value = []
    backgroundGenerating.value = false
    notice.value = ''

    try {
      await loadMessages(sessionId, expected)
      if (expected !== operation || sessions.currentSessionId !== sessionId) return
      applySessionSelection(sessionId)
      const active = await getRunStatus(sessionId)
      if (expected !== operation || sessions.currentSessionId !== sessionId) return
      backgroundGenerating.value = active
      if (active) {
        notice.value = '该会话仍在生成，完成后会自动刷新。'
        schedulePoll(sessionId, expected)
      }
    } catch (error) {
      if (expected === operation) {
        notice.value = error instanceof Error ? error.message : String(error)
        throw error
      }
    }
  }

  async function loadMessages(sessionId: string, expected = operation): Promise<void> {
    if (!sessionId || !connection.isConnected) return
    const response = await fetch(connection.api(`/api/sessions/${encodeURIComponent(sessionId)}/messages`), {
      cache: 'no-store',
    })
    if (!response.ok) throw new Error(await readHTTPError(response, `读取消息失败 (${response.status})`))
    const data = await response.json() as ChatMessage[]
    if (expected !== operation || sessions.currentSessionId !== sessionId) return
    messages.value = (Array.isArray(data) ? data : []).map(message => ({
      ...message,
      status: statusFromMessage(message),
    }))
  }

  function applySessionSelection(sessionId: string): void {
    const session = sessions.sessions.find(item => item.id === sessionId)
    if (!session) return

    let providerId = session.provider || ''
    if (!providerId && session.model) {
      const matches = connection.models.filter(model => model.id === session.model)
      if (matches.length === 1) providerId = matches[0].provider_id
    }
    if (providerId && connection.providers.some(provider => provider.id === providerId)) {
      selectedProvider.value = providerId
    }
    if (session.model && modelsForProvider.value.some(model => model.id === session.model)) {
      selectedModel.value = session.model
    }

    const lastSelection = [...messages.value].reverse().find(message =>
      message.provider === selectedProvider.value && message.model === selectedModel.value && message.thinking,
    )
    if (lastSelection?.thinking && thinkingLevels.value.includes(lastSelection.thinking)) {
      selectedThinking.value = lastSelection.thinking
    } else {
      ensureThinkingSelection()
    }
  }

  async function sendMessage(): Promise<void> {
    const text = inputText.value.trim()
    if (!text || !connection.isConnected || !selectedProvider.value || !selectedModel.value || isBusy.value) return

    notice.value = ''
    clearPoll()
    let sessionId = sessions.currentSessionId
    if (!sessionId) {
      try {
        const session = await sessions.createPersistedSession(text)
        sessionId = session.id
      } catch (error) {
        window.$message?.error(error instanceof Error ? error.message : String(error))
        return
      }
    }

    const run = ++operation
    const user: ChatMessage = {
      id: `local-user-${localID()}`,
      type: 'user',
      content: text,
      provider: selectedProvider.value,
      model: selectedModel.value,
      thinking: selectedThinking.value,
      status: 'complete',
      ts: new Date().toISOString(),
    }
    const assistant: ChatMessage = {
      id: `local-assistant-${localID()}`,
      type: 'assistant',
      content: '',
      reasoning: '',
      provider: selectedProvider.value,
      model: selectedModel.value,
      thinking: selectedThinking.value,
      status: 'streaming',
      ts: new Date().toISOString(),
    }
    messages.value.push(user, assistant)
    inputText.value = ''
    isStreaming.value = true
    backgroundGenerating.value = false

    const localController = new AbortController()
    controller = localController
    const decoder = new SSEDecoder()
    const textDecoder = new TextDecoder()
    let accepted = false
    let gotDone = false
    let persisted = true

    const removeOptimistic = (): void => {
      messages.value = messages.value.filter(message => message.id !== user.id && message.id !== assistant.id)
      if (!inputText.value) inputText.value = text
    }

    const handleEvent = (event: SSEMessage): void => {
      const payload = parseJSON(event.data)
      switch (event.event) {
        case 'ack': {
          const saved = payload.message as ChatMessage | undefined
          if (saved?.id) user.id = saved.id
          if (saved?.ts) user.ts = saved.ts
          break
        }
        case 'ttft': {
          const value = Number(payload.ms)
          if (Number.isFinite(value)) assistant.ttft_ms = value
          break
        }
        case 'reasoning':
          assistant.reasoning = (assistant.reasoning || '') + String(payload.text || '')
          break
        case 'delta':
          assistant.content += String(payload.text || '')
          break
        case 'usage':
          assistant.usage = payload as unknown as Usage
          break
        case 'error':
          assistant.error = String(payload.message || '生成失败')
          assistant.status = 'error'
          break
        case 'done':
          gotDone = true
          persisted = payload.persisted !== false
          assistant.finish = String(payload.finish || 'stop')
          if (payload.error) assistant.error = String(payload.error)
          if (payload.message_id) assistant.id = String(payload.message_id)
          if (Number.isFinite(Number(payload.duration_ms))) assistant.duration_ms = Number(payload.duration_ms)
          if (Number.isFinite(Number(payload.ttft_ms))) assistant.ttft_ms = Number(payload.ttft_ms)
          assistant.status = statusFromMessage(assistant)
          break
      }
    }

    try {
      const response = await fetch(connection.api(`/api/sessions/${encodeURIComponent(sessionId)}/chat`), {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Accept: 'text/event-stream',
        },
        body: JSON.stringify({
          message: text,
          provider: selectedProvider.value,
          model: selectedModel.value,
          thinking: selectedThinking.value,
        }),
        signal: localController.signal,
      })

      if (!response.ok) {
        removeOptimistic()
        throw new Error(await readHTTPError(response, `发送失败 (${response.status})`))
      }
      // 服务端只有在用户消息成功落盘后才会写出 200/SSE 响应头。
      accepted = true
      if (!response.body) throw new Error('浏览器没有拿到流式响应体')

      const reader = response.body.getReader()
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        const chunk = textDecoder.decode(value, { stream: true })
        for (const event of decoder.feed(chunk)) handleEvent(event)
      }
      for (const event of decoder.feed(textDecoder.decode())) handleEvent(event)
      for (const event of decoder.flush()) handleEvent(event)

      if (run !== operation || sessions.currentSessionId !== sessionId) return
      if (gotDone) {
        isStreaming.value = false
        if (!persisted) window.$message?.error(assistant.error || '回复保存失败')
        await loadMessages(sessionId, run)
        await sessions.loadSessions().catch(() => undefined)
      } else {
        await recoverBackgroundRun(sessionId, run, assistant)
      }
    } catch (error) {
      if (run !== operation || sessions.currentSessionId !== sessionId) return
      if (localController.signal.aborted) return

      const message = error instanceof Error ? error.message : String(error)
      if (!accepted) {
        removeOptimistic()
        window.$message?.error(message)
      } else {
        assistant.status = 'background'
        assistant.error = undefined
        notice.value = `${message}；服务端会继续生成，完成后自动刷新。`
        await recoverBackgroundRun(sessionId, run, assistant)
      }
    } finally {
      if (run === operation) {
        isStreaming.value = false
        if (controller === localController) controller = null
      }
    }
  }

  async function recoverBackgroundRun(sessionId: string, expected: number, assistant: ChatMessage): Promise<void> {
    if (expected !== operation || sessions.currentSessionId !== sessionId) return
    const active = await getRunStatus(sessionId)
    if (!active) {
      backgroundGenerating.value = false
      notice.value = ''
      await loadMessages(sessionId, expected).catch(() => undefined)
      await sessions.loadSessions().catch(() => undefined)
      return
    }
    assistant.status = 'background'
    backgroundGenerating.value = true
    if (!notice.value) notice.value = '连接已断开，但服务端仍在生成；完成后会自动刷新。'
    schedulePoll(sessionId, expected)
  }

  async function stopGeneration(): Promise<void> {
    const sessionId = sessions.currentSessionId
    if (!sessionId || !connection.isConnected || !isBusy.value) return

    const expected = ++operation
    clearPoll()
    controller?.abort()
    controller = null
    isStreaming.value = false
    backgroundGenerating.value = true
    notice.value = '正在停止生成…'

    try {
      const response = await fetch(connection.api(`/api/sessions/${encodeURIComponent(sessionId)}/chat/cancel`), {
        method: 'POST',
      })
      if (!response.ok) throw new Error(await readHTTPError(response, `停止失败 (${response.status})`))
    } catch (error) {
      window.$message?.error(error instanceof Error ? error.message : String(error))
    }
    schedulePoll(sessionId, expected, 250)
  }

  async function getRunStatus(sessionId: string): Promise<boolean> {
    try {
      const response = await fetch(connection.api(`/api/sessions/${encodeURIComponent(sessionId)}/chat/status`), {
        cache: 'no-store',
      })
      if (!response.ok) return false
      const payload = await response.json() as { active?: boolean }
      return Boolean(payload.active)
    } catch {
      return true
    }
  }

  function schedulePoll(sessionId: string, expected: number, delay = 900): void {
    clearPoll()
    pollTimer = window.setTimeout(async () => {
      if (expected !== operation || sessions.currentSessionId !== sessionId) return
      const active = await getRunStatus(sessionId)
      if (expected !== operation || sessions.currentSessionId !== sessionId) return
      if (active) {
        backgroundGenerating.value = true
        schedulePoll(sessionId, expected)
        return
      }
      backgroundGenerating.value = false
      notice.value = ''
      await loadMessages(sessionId, expected).catch(error => {
        notice.value = error instanceof Error ? error.message : String(error)
      })
      await sessions.loadSessions().catch(() => undefined)
    }, delay)
  }

  return {
    messages,
    inputText,
    selectedProvider,
    selectedModel,
    selectedThinking,
    modelsForProvider,
    selectedModelInfo,
    thinkingLevels,
    isStreaming,
    backgroundGenerating,
    isBusy,
    notice,
    openSession,
    newConversation,
    loadMessages,
    sendMessage,
    stopGeneration,
  }
})

function localID(): string {
  // 乐观消息只需要浏览器进程内唯一；非 HTTPS/LAN 环境也不能依赖 crypto.randomUUID。
  const randomUUID = globalThis.crypto?.randomUUID
  if (typeof randomUUID === 'function') return randomUUID.call(globalThis.crypto)
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}-${Math.random().toString(36).slice(2)}`
}

function statusFromMessage(message: ChatMessage): MessageStatus {
  if (message.finish === 'aborted') return 'aborted'
  if (message.finish === 'error' || message.error) return 'error'
  return 'complete'
}

function parseJSON(data: string): Record<string, unknown> {
  try {
    return JSON.parse(data) as Record<string, unknown>
  } catch {
    return {}
  }
}

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
