import { computed, reactive, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import { useConnectionStore } from './connection'
import { useSessionsStore } from './sessions'

export interface Usage { prompt_tokens: number; completion_tokens: number; cached_tokens?: number; reasoning_tokens?: number }
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
      if (line.startsWith('event:')) event = line.slice(6).trim()
      else if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''))
    }
    if (!data.length) return null
    return { event, data: data.join('\n') }
  }
}
function parseJSON(data: string): Record<string, unknown> {
  try { return JSON.parse(data) as Record<string, unknown> } catch { return {} }
}

export const useChatStore = defineStore('chat', () => {
  const connection = useConnectionStore()
  const sessions = useSessionsStore()
  const messages = ref<ChatMessage[]>([])
  const inputText = ref('')
  const selectedModel = ref('')
  const selectedThinking = ref('off')
  const isStreaming = ref(false)
  const backgroundGenerating = ref(false)
  const notice = ref('')
  let controller: AbortController | null = null
  let operation = 0
  let pollTimer: ReturnType<typeof setTimeout> | null = null

  const selectedModelInfo = computed(() => connection.models.find(model => model.id === selectedModel.value))
  const thinkingLevels = computed(() => selectedModelInfo.value?.thinking_levels?.length ? selectedModelInfo.value.thinking_levels : ['off'])
  const isBusy = computed(() => isStreaming.value || backgroundGenerating.value)

  watch(() => connection.models, available => {
    if (!available.length) { selectedModel.value = ''; selectedThinking.value = 'off'; return }
    if (!available.some(model => model.id === selectedModel.value)) selectedModel.value = available.find(model => model.default)?.id || available[0].id
  }, { deep: true, immediate: true })
  watch(selectedModelInfo, model => {
    const levels = model?.thinking_levels?.length ? model.thinking_levels : ['off']
    if (!levels.includes(selectedThinking.value)) selectedThinking.value = levels.includes('off') ? 'off' : levels[0]
  }, { immediate: true })

  function clearPoll(): void { if (pollTimer) { window.clearTimeout(pollTimer); pollTimer = null } }
  function detachLocalStream(): void { operation += 1; controller?.abort(); controller = null; isStreaming.value = false }

  async function loadMessages(sessionId: string, expectedOperation = operation): Promise<void> {
    if (!sessionId || !connection.isConnected) return
    const response = await fetch(`${connection.serverUrl}/api/sessions/${encodeURIComponent(sessionId)}/messages`, { cache: 'no-store' })
    if (!response.ok) throw new Error(`读取消息失败 (${response.status})`)
    const data = await response.json() as ChatMessage[]
    if (expectedOperation !== operation || sessions.currentSessionId !== sessionId) return
    messages.value = data.map(message => ({ ...message, status: statusFromMessage(message) }))
  }

  async function openSession(sessionId: string): Promise<void> {
    if (!sessionId) return
    detachLocalStream(); clearPoll()
    const expected = ++operation
    sessions.selectSession(sessionId)
    messages.value = []
    backgroundGenerating.value = false
    notice.value = ''
    try {
      await loadMessages(sessionId, expected)
      const active = await getRunStatus(sessionId)
      if (expected !== operation || sessions.currentSessionId !== sessionId) return
      backgroundGenerating.value = active
      if (active) { notice.value = '该会话仍在生成，完成后会自动刷新。'; schedulePoll(sessionId, expected) }
    } catch (error) { if (expected === operation) notice.value = error instanceof Error ? error.message : String(error) }
  }

  function newConversation(): void {
    detachLocalStream(); clearPoll(); operation += 1
    sessions.beginNewSession(); messages.value = []; backgroundGenerating.value = false; notice.value = ''; inputText.value = ''
  }

  async function sendMessage(): Promise<void> {
    const text = inputText.value.trim()
    if (!text || !connection.isConnected || !selectedModel.value || isBusy.value) return
    notice.value = ''
    let sessionId = sessions.currentSessionId
    if (!sessionId) {
      try { sessionId = (await sessions.createPersistedSession(text.slice(0, 40))).id }
      catch (error) { window.$message?.error(error instanceof Error ? error.message : String(error)); return }
    }
    const expected = ++operation
    const user = reactive<ChatMessage>({ id: `local-user-${crypto.randomUUID()}`, type: 'user', content: text, model: selectedModel.value, thinking: selectedThinking.value, status: 'complete', created_at: new Date().toISOString() })
    const assistant = reactive<ChatMessage>({ id: `local-assistant-${crypto.randomUUID()}`, type: 'assistant', content: '', reasoning: '', model: selectedModel.value, thinking: selectedThinking.value, status: 'streaming', created_at: new Date().toISOString() })
    messages.value.push(user, assistant)
    inputText.value = ''
    isStreaming.value = true
    backgroundGenerating.value = false
    connection.ttft = 0
    controller = new AbortController()
    const localController = controller
    let gotEvent = false
    let gotDone = false
    let persisted = true
    const decoder = new SSEDecoder()
    const textDecoder = new TextDecoder()

    const removeOptimistic = (): void => {
      messages.value = messages.value.filter(message => message.id !== user.id && message.id !== assistant.id)
      if (!inputText.value) inputText.value = text
    }
    const handle = (message: SSEMessage): void => {
      gotEvent = true
      const data = parseJSON(message.data)
      switch (message.event) {
        case 'ack': {
          const persistedUser = data.message as ChatMessage | undefined
          if (persistedUser?.id) user.id = persistedUser.id
          break
        }
        case 'ttft': {
          const value = Number(data.ms)
          if (Number.isFinite(value)) { assistant.ttft_ms = value; connection.ttft = value }
          break
        }
        case 'reasoning': assistant.reasoning = (assistant.reasoning || '') + String(data.text || ''); break
        case 'delta': assistant.content += String(data.text || ''); break
        case 'usage': assistant.usage = data as unknown as Usage; break
        case 'error': assistant.error = String(data.message || '生成失败'); assistant.status = 'error'; break
        case 'done':
          gotDone = true; persisted = data.persisted !== false
          assistant.finish = String(data.finish || 'stop')
          if (data.error) assistant.error = String(data.error)
          assistant.status = statusFromMessage(assistant)
          if (data.message_id) assistant.id = String(data.message_id)
          if (Number.isFinite(Number(data.duration_ms))) assistant.duration_ms = Number(data.duration_ms)
          if (Number.isFinite(Number(data.ttft_ms))) assistant.ttft_ms = Number(data.ttft_ms)
          break
      }
    }

    try {
      const response = await fetch(`${connection.serverUrl}/api/sessions/${encodeURIComponent(sessionId)}/chat`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
        body: JSON.stringify({ message: text, model: selectedModel.value, thinking: selectedThinking.value }), signal: localController.signal,
      })
      if (!response.ok) { removeOptimistic(); throw new Error(await readHTTPError(response, `发送失败 (${response.status})`)) }
      if (!response.body) throw new Error('浏览器没有拿到流式响应体')
      const reader = response.body.getReader()
      while (true) {
        const { done, value } = await reader.read(); if (done) break
        for (const event of decoder.feed(textDecoder.decode(value, { stream: true }))) handle(event)
      }
      for (const event of decoder.feed(textDecoder.decode())) handle(event)
      for (const event of decoder.flush()) handle(event)
      if (!persisted) { window.$message?.error(assistant.error || '回复保存失败') }
      if (gotDone && persisted && expected === operation && sessions.currentSessionId === sessionId) {
        await loadMessages(sessionId, expected)
        await sessions.loadSessions().catch(() => undefined)
      } else if (!gotDone && expected === operation) {
        assistant.status = 'background'; backgroundGenerating.value = true; notice.value = '连接中断，服务端会继续生成；完成后会自动刷新。'; schedulePoll(sessionId, expected)
      }
    } catch (error) {
      if (!localController.signal.aborted) {
        const message = error instanceof Error ? error.message : String(error)
        if (!gotEvent) removeOptimistic()
        else if (!gotDone && expected === operation) { assistant.status = 'background'; assistant.error = undefined; backgroundGenerating.value = true; notice.value = `${message}；正在检查服务端生成状态。`; schedulePoll(sessionId, expected) }
        window.$message?.error(message)
      }
    } finally {
      if (expected === operation) { isStreaming.value = false; if (controller === localController) controller = null }
    }
  }

  async function stopGeneration(): Promise<void> {
    const sessionId = sessions.currentSessionId
    if (!sessionId || !connection.isConnected || !isBusy.value) return
    clearPoll(); notice.value = '正在停止生成…'
    try { await fetch(`${connection.serverUrl}/api/sessions/${encodeURIComponent(sessionId)}/chat/cancel`, { method: 'POST' }) }
    finally {
      controller?.abort(); controller = null; isStreaming.value = false; backgroundGenerating.value = false
      const expected = operation
      window.setTimeout(async () => { if (expected !== operation || sessions.currentSessionId !== sessionId) return; await loadMessages(sessionId, expected).catch(() => undefined); notice.value = '' }, 350)
    }
  }

  async function getRunStatus(sessionId: string): Promise<boolean> {
    try {
      const response = await fetch(`${connection.serverUrl}/api/sessions/${encodeURIComponent(sessionId)}/chat/status`, { cache: 'no-store' })
      if (!response.ok) return false
      return Boolean((await response.json() as { active?: boolean }).active)
    } catch { return false }
  }
  function schedulePoll(sessionId: string, expected: number): void {
    clearPoll()
    pollTimer = window.setTimeout(async () => {
      if (expected !== operation || sessions.currentSessionId !== sessionId) return
      if (await getRunStatus(sessionId)) { schedulePoll(sessionId, expected); return }
      backgroundGenerating.value = false
      await loadMessages(sessionId, expected).catch(() => undefined)
      await sessions.loadSessions().catch(() => undefined)
      notice.value = ''
    }, 900)
  }
  return { messages, inputText, selectedModel, selectedThinking, selectedModelInfo, thinkingLevels, isStreaming, backgroundGenerating, isBusy, notice, loadMessages, openSession, newConversation, sendMessage, stopGeneration, detachLocalStream }
})

function statusFromMessage(message: ChatMessage): MessageStatus {
  if (message.finish === 'aborted') return 'aborted'
  if (message.finish === 'error' || message.error) return 'error'
  return 'complete'
}
async function readHTTPError(response: Response, fallback: string): Promise<string> {
  try {
    if ((response.headers.get('content-type') || '').includes('application/json')) return (await response.json() as { error?: string }).error || fallback
    return (await response.text()).trim() || fallback
  } catch { return fallback }
}
