import { computed, reactive, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import { SSEDecoder, parseJSON } from '../lib/sse'
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
  content: string
  reasoning?: string
  model?: string
  thinking?: string
  usage?: Usage | null
  duration_ms?: number
  ttft_ms?: number
  finish?: string
  error?: string
  created_at?: string
  status?: MessageStatus
}

type EventPayload = Record<string, unknown>

export const useChatStore = defineStore('chat', () => {
  const connection = useConnectionStore()
  const sessions = useSessionsStore()

  const messages = ref<ChatMessage[]>([])
  const input = ref('')
  const selectedModel = ref('')
  const selectedThinking = ref('off')
  const isStreaming = ref(false)
  const backgroundGenerating = ref(false)
  const streamSessionId = ref('')
  const notice = ref('')

  let localController: AbortController | null = null
  let operationSeq = 0
  let pollTimer: number | null = null
  let explicitStopSeq = -1

  const selectedModelInfo = computed(() => connection.models.find((item) => item.id === selectedModel.value))
  const thinkingOptions = computed(() => selectedModelInfo.value?.thinking_levels || ['off'])
  const isBusy = computed(() => isStreaming.value || backgroundGenerating.value)

  watch(
    () => connection.models,
    (available) => {
      if (!available.length) {
        selectedModel.value = ''
        selectedThinking.value = 'off'
        return
      }
      if (!available.some((item) => item.id === selectedModel.value)) {
        selectedModel.value = available.find((item) => item.default)?.id || available[0].id
      }
    },
    { deep: true, immediate: true },
  )

  watch(selectedModelInfo, (model) => {
    const levels = model?.thinking_levels || ['off']
    if (!levels.includes(selectedThinking.value)) {
      selectedThinking.value = levels.includes('auto') ? 'auto' : levels[0] || 'off'
    }
  })

  function clearPoll(): void {
    if (pollTimer != null) {
      window.clearTimeout(pollTimer)
      pollTimer = null
    }
  }

  /** Detach only the browser reader. The backend run deliberately keeps going. */
  function detachLocalStream(): void {
    operationSeq += 1
    localController?.abort()
    localController = null
    isStreaming.value = false
    streamSessionId.value = ''
  }

  async function loadMessages(sessionId: string, seq = operationSeq): Promise<void> {
    if (!sessionId || !connection.serverUrl) return
    const response = await fetch(`${connection.serverUrl}/api/sessions/${encodeURIComponent(sessionId)}/messages`, { cache: 'no-store' })
    if (!response.ok) throw new Error(`读取消息失败 (${response.status})`)
    const data = (await response.json()) as ChatMessage[]
    if (seq !== operationSeq || sessions.currentSessionId !== sessionId) return
    messages.value = data.map(normalizePersistedMessage)
  }

  async function openSession(sessionId: string): Promise<void> {
    if (!sessionId) return
    if (sessions.currentSessionId === sessionId && messages.value.length > 0) return

    detachLocalStream()
    clearPoll()
    const seq = ++operationSeq
    sessions.selectSession(sessionId)
    messages.value = []
    backgroundGenerating.value = false
    notice.value = ''

    try {
      await loadMessages(sessionId, seq)
      await refreshRunStatus(sessionId, seq)
    } catch (error) {
      if (seq === operationSeq) {
        notice.value = error instanceof Error ? error.message : String(error)
      }
    }
  }

  function newConversation(): void {
    detachLocalStream()
    clearPoll()
    operationSeq += 1
    sessions.beginNewSession()
    messages.value = []
    backgroundGenerating.value = false
    notice.value = ''
    input.value = ''
  }

  async function refreshRunStatus(sessionId = sessions.currentSessionId, seq = operationSeq): Promise<boolean> {
    if (!sessionId || !connection.isConnected) return false
    try {
      const response = await fetch(`${connection.serverUrl}/api/sessions/${encodeURIComponent(sessionId)}/chat/status`, { cache: 'no-store' })
      if (!response.ok) return false
      const data = (await response.json()) as { active: boolean }
      if (seq !== operationSeq || sessions.currentSessionId !== sessionId) return data.active
      backgroundGenerating.value = data.active && !isStreaming.value
      if (data.active) scheduleBackgroundPoll(sessionId, seq)
      return data.active
    } catch {
      return false
    }
  }

  function scheduleBackgroundPoll(sessionId: string, seq: number): void {
    clearPoll()
    pollTimer = window.setTimeout(async () => {
      if (seq !== operationSeq || sessions.currentSessionId !== sessionId) return
      const active = await refreshRunStatus(sessionId, seq)
      if (active) return
      if (seq !== operationSeq || sessions.currentSessionId !== sessionId) return
      backgroundGenerating.value = false
      try {
        await loadMessages(sessionId, seq)
        await sessions.loadSessions()
        notice.value = ''
      } catch (error) {
        notice.value = error instanceof Error ? error.message : String(error)
      }
    }, 900)
  }

  async function ensureSession(firstMessage: string): Promise<string> {
    if (sessions.currentSessionId) return sessions.currentSessionId
    const session = await sessions.createSession(firstMessage.slice(0, 40))
    // No watcher performs a competing load/clear here. The new session and the
    // optimistic messages belong to the same transaction.
    return session.id
  }

  async function sendMessage(): Promise<void> {
    const text = input.value.trim()
    if (!text || !connection.isConnected || !selectedModel.value || isBusy.value) return

    notice.value = ''
    let sessionId = ''
    try {
      sessionId = await ensureSession(text)
    } catch (error) {
      window.$message?.error(error instanceof Error ? error.message : String(error))
      return
    }

    const seq = ++operationSeq
    const userMessage = reactive<ChatMessage>({
      id: `local-user-${crypto.randomUUID()}`,
      type: 'user',
      content: text,
      model: selectedModel.value,
      thinking: selectedThinking.value,
      status: 'complete',
      created_at: new Date().toISOString(),
    })
    const assistantMessage = reactive<ChatMessage>({
      id: `local-assistant-${crypto.randomUUID()}`,
      type: 'assistant',
      content: '',
      reasoning: '',
      model: selectedModel.value,
      thinking: selectedThinking.value,
      status: 'streaming',
      created_at: new Date().toISOString(),
    })

    // Push Vue proxies, not raw objects. Every incoming token now invalidates the UI.
    messages.value.push(userMessage, assistantMessage)
    input.value = ''
    isStreaming.value = true
    backgroundGenerating.value = false
    streamSessionId.value = sessionId
    connection.ttftMs = null

    const controller = new AbortController()
    localController = controller
    let gotDone = false
    let persisted = true
    let gotAnyStreamEvent = false
    const decoder = new SSEDecoder()
    const textDecoder = new TextDecoder()

    const removeOptimisticPair = (): void => {
      messages.value = messages.value.filter((msg) => msg !== userMessage && msg !== assistantMessage)
      if (!input.value) input.value = text
    }

    const handleEvent = (event: string, rawData: string): void => {
      gotAnyStreamEvent = true
      const data = parseJSON<EventPayload>(rawData) || {}
      switch (event) {
        case 'ack': {
          const message = data.message as ChatMessage | undefined
          if (message?.id) userMessage.id = message.id
          break
        }
        case 'ttft': {
          const value = Number(data.ms)
          if (Number.isFinite(value)) {
            assistantMessage.ttft_ms = value
            connection.ttftMs = value
          }
          break
        }
        case 'reasoning':
          assistantMessage.reasoning = (assistantMessage.reasoning || '') + String(data.text || '')
          break
        case 'delta':
          assistantMessage.content += String(data.text || '')
          break
        case 'usage':
          assistantMessage.usage = data as unknown as Usage
          break
        case 'error':
          assistantMessage.error = String(data.message || '生成失败')
          if (data.persisted === false) persisted = false
          break
        case 'done': {
          gotDone = true
          persisted = data.persisted !== false
          assistantMessage.finish = String(data.finish || 'stop')
          assistantMessage.error = String(data.error || assistantMessage.error || '') || undefined
          const duration = Number(data.duration_ms)
          const ttft = Number(data.ttft_ms)
          if (Number.isFinite(duration)) assistantMessage.duration_ms = duration
          if (Number.isFinite(ttft) && ttft > 0) assistantMessage.ttft_ms = ttft
          if (data.message_id) assistantMessage.id = String(data.message_id)
          assistantMessage.status = statusFromFinish(assistantMessage.finish, assistantMessage.error)
          break
        }
      }
    }

    try {
      const response = await fetch(`${connection.serverUrl}/api/sessions/${encodeURIComponent(sessionId)}/chat`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
        body: JSON.stringify({ message: text, model: selectedModel.value, thinking: selectedThinking.value }),
        signal: controller.signal,
      })
      if (!response.ok) {
        removeOptimisticPair()
        throw new Error(await readHTTPError(response, `发送失败 (${response.status})`))
      }
      if (!response.body) throw new Error('浏览器没有拿到可读取的流式响应')

      const reader = response.body.getReader()
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        const chunk = textDecoder.decode(value, { stream: true })
        for (const message of decoder.feed(chunk)) handleEvent(message.event, message.data)
      }
      const tail = textDecoder.decode()
      for (const message of decoder.feed(tail)) handleEvent(message.event, message.data)
      for (const message of decoder.flush()) handleEvent(message.event, message.data)

      if (!persisted) {
        removeOptimisticPair()
        const message = assistantMessage.error || '上游没有接受本次请求'
        window.$message?.error(message)
      } else if (!gotDone) {
        // Network detached unexpectedly. The backend run is intentionally independent,
        // so keep polling instead of falsely marking the model as stopped.
        assistantMessage.status = 'background'
        assistantMessage.error = undefined
        backgroundGenerating.value = true
        notice.value = '连接中断，服务端会继续生成；完成后会自动刷新。'
        scheduleBackgroundPoll(sessionId, seq)
      }
    } catch (error) {
      if (controller.signal.aborted) {
        // Session switch detaches locally; explicit Stop has already cancelled the server run.
        if (explicitStopSeq !== seq && seq === operationSeq && sessions.currentSessionId === sessionId && !gotDone) {
          backgroundGenerating.value = true
          scheduleBackgroundPoll(sessionId, seq)
        }
      } else {
        const message = error instanceof Error ? error.message : String(error)
        if (!gotAnyStreamEvent) removeOptimisticPair()
        else {
          assistantMessage.status = 'background'
          assistantMessage.error = undefined
          backgroundGenerating.value = true
          notice.value = `${message}；正在检查服务端生成状态。`
          scheduleBackgroundPoll(sessionId, seq)
        }
        window.$message?.error(message)
      }
    } finally {
      if (seq === operationSeq) {
        isStreaming.value = false
        streamSessionId.value = ''
        if (localController === controller) localController = null
        if (gotDone && persisted) {
          backgroundGenerating.value = false
          await sessions.loadSessions().catch(() => undefined)
        }
      }
    }
  }

  async function stopGeneration(): Promise<void> {
    const sessionId = streamSessionId.value || sessions.currentSessionId
    if (!sessionId || !connection.isConnected) return
    explicitStopSeq = operationSeq
    const target = [...messages.value].reverse().find((msg) => msg.type === 'assistant' && ['streaming', 'background'].includes(msg.status || ''))
    if (target) {
      target.status = 'aborted'
      target.finish = 'aborted'
    }
    notice.value = '正在停止生成…'
    try {
      await fetch(`${connection.serverUrl}/api/sessions/${encodeURIComponent(sessionId)}/chat/cancel`, { method: 'POST' })
    } finally {
      localController?.abort()
      localController = null
      isStreaming.value = false
      backgroundGenerating.value = false
      streamSessionId.value = ''
      clearPoll()
      const seq = operationSeq
      window.setTimeout(async () => {
        if (sessions.currentSessionId !== sessionId || seq !== operationSeq) return
        await loadMessages(sessionId, seq).catch(() => undefined)
        notice.value = ''
      }, 250)
    }
  }

  return {
    messages,
    input,
    selectedModel,
    selectedThinking,
    selectedModelInfo,
    thinkingOptions,
    isStreaming,
    backgroundGenerating,
    isBusy,
    notice,
    loadMessages,
    openSession,
    newConversation,
    sendMessage,
    stopGeneration,
    refreshRunStatus,
    detachLocalStream,
  }
})

function normalizePersistedMessage(message: ChatMessage): ChatMessage {
  return {
    ...message,
    status: statusFromFinish(message.finish, message.error),
  }
}

function statusFromFinish(finish?: string, error?: string): MessageStatus {
  if (finish === 'aborted') return 'aborted'
  if (finish === 'error' || finish === 'timeout' || error) return 'error'
  return 'complete'
}

async function readHTTPError(response: Response, fallback: string): Promise<string> {
  try {
    const type = response.headers.get('content-type') || ''
    if (type.includes('application/json')) {
      const body = (await response.json()) as { error?: string }
      return body.error || fallback
    }
    return (await response.text()).trim() || fallback
  } catch {
    return fallback
  }
}
