import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { useConnectionStore } from './connection'
import { useSessionsStore } from './sessions'

export interface ChatMessage {
  id: string
  type: 'user' | 'assistant'
  content: string
  reasoning?: string
  model?: string
  usage?: {
    prompt_tokens: number
    completion_tokens: number
    cached_tokens: number
    reasoning_tokens: number
  }
  duration_ms?: number
  finish?: string
  isThinking?: boolean
  thinkingDuration?: number
}

export const useChatStore = defineStore('chat', () => {
  const messages = ref<ChatMessage[]>([])
  const inputText = ref('')
  const isStreaming = ref(false)
  const selectedModel = ref('')
  const selectedThinking = ref('off')
  const abortController = ref<AbortController | null>(null)

  let activeRun = 0

  const connectionStore = useConnectionStore()
  const sessionsStore = useSessionsStore()

  watch(() => connectionStore.models, (models) => {
    if (models.length > 0 && !selectedModel.value) {
      const def = models.find(m => m.default)
      selectedModel.value = def?.id || models[0].id
    }
  }, { immediate: true })

  watch(selectedModel, (modelId) => {
    const model = connectionStore.models.find(m => m.id === modelId)
    if (model) {
      selectedThinking.value = model.thinking_levels?.[0] || 'off'
    }
  })

  watch(() => sessionsStore.currentSessionId, () => {
    activeRun++
    if (abortController.value) {
      abortController.value.abort()
    }
    abortController.value = null
    isStreaming.value = false
    messages.value = []
  })

  async function loadMessages(sessionId: string) {
    if (!sessionId || !connectionStore.isConnected) {
      messages.value = []
      return
    }
    const res = await fetch(`${connectionStore.serverUrl}/api/sessions/${sessionId}/messages`)
    if (res.ok) {
      const data = await res.json()
      if (sessionsStore.currentSessionId !== sessionId) return
      messages.value = data.map((m: any) => ({
        id: m.id,
        type: m.type,
        content: m.content,
        reasoning: m.reasoning,
        model: m.model,
        usage: m.usage,
        duration_ms: m.duration_ms,
        finish: m.finish,
        isThinking: false,
      }))
    }
  }

  async function sendMessage() {
    const text = inputText.value.trim()
    if (!text || !connectionStore.isConnected) return

    const sessionId = sessionsStore.currentSessionId
    if (!sessionId) {
      const res = await fetch(`${connectionStore.serverUrl}/api/sessions`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: text.slice(0, 20) }),
      })
      if (!res.ok) {
        window.$message?.error('创建会话失败')
        return
      }
      const session = await res.json()
      sessionsStore.currentSessionId = session.id
      await sessionsStore.loadSessions()
    }

    const currentId = sessionsStore.currentSessionId
    if (!currentId) return
    const run = ++activeRun

    const userMsg: ChatMessage = {
      id: 'temp_' + Date.now(),
      type: 'user',
      content: text,
    }
    messages.value.push(userMsg)
    inputText.value = ''

    isStreaming.value = true
    abortController.value = new AbortController()

    const assistantMsg: ChatMessage = {
      id: 'stream_' + Date.now(),
      type: 'assistant',
      content: '',
      reasoning: '',
      model: selectedModel.value,
      isThinking: true,
    }
    messages.value.push(assistantMsg)

    const thinkingStartTime = Date.now()

    try {
      const res = await fetch(`${connectionStore.serverUrl}/api/sessions/${currentId}/chat`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          message: text,
          model: selectedModel.value,
          thinking: selectedThinking.value,
        }),
        signal: abortController.value.signal,
      })

      if (res.status === 409) {
        window.$message?.error('该会话有任务正在执行')
        messages.value.pop()
        inputText.value = text
        return
      }

      if (!res.ok) {
        const errorText = await res.text().catch(() => '请求失败')
        window.$message?.error(`发送失败: ${errorText}`)
        messages.value.pop()
        inputText.value = text
        return
      }

      const reader = res.body!.getReader()
      const dec = new TextDecoder()
      let buf = ''
      let hasReceivedContent = false
      let hasReceivedReasoning = false

      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        buf += dec.decode(value, { stream: true })

        let i: number
        while ((i = buf.indexOf('\n\n')) >= 0) {
          const block = buf.slice(0, i)
          buf = buf.slice(i + 2)
          const evt = parseSSEBlock(block)
          if (!evt) continue

          switch (evt.type) {
            case 'delta':
              if (evt.text) {
                if (!hasReceivedContent) {
                  assistantMsg.isThinking = false
                  assistantMsg.thinkingDuration = Date.now() - thinkingStartTime
                }
                hasReceivedContent = true
                assistantMsg.content += evt.text
              }
              break
            case 'reasoning':
              if (evt.text) {
                hasReceivedReasoning = true
                assistantMsg.reasoning = (assistantMsg.reasoning || '') + evt.text
              }
              break
            case 'usage':
              assistantMsg.usage = evt.data
              break
            case 'done':
              assistantMsg.finish = evt.data?.finish
              break
            case 'error':
              window.$message?.error(`流式错误: ${evt.data?.message || '未知错误'}`)
              break
          }
        }
      }

      if (buf.trim()) {
        const evt = parseSSEBlock(buf)
        if (evt) {
          switch (evt.type) {
            case 'delta':
              if (evt.text) {
                if (!hasReceivedContent) {
                  assistantMsg.isThinking = false
                  assistantMsg.thinkingDuration = Date.now() - thinkingStartTime
                }
                assistantMsg.content += evt.text
              }
              break
            case 'reasoning':
              if (evt.text) {
                assistantMsg.reasoning = (assistantMsg.reasoning || '') + evt.text
              }
              break
            case 'usage':
              assistantMsg.usage = evt.data
              break
            case 'done':
              assistantMsg.finish = evt.data?.finish
              break
          }
        }
      }

      if (!hasReceivedReasoning && !assistantMsg.reasoning) {
        assistantMsg.isThinking = false
      }

      if (run === activeRun && sessionsStore.currentSessionId === currentId) {
        await loadMessages(currentId)
      }
    } catch (e: any) {
      if (e.name === 'AbortError') {
        // 用户主动中止
      } else {
        console.error('发送失败', e)
        window.$message?.error('服务器无响应，消息已退回')
        if (messages.value[messages.value.length - 1]?.id === assistantMsg.id) {
          messages.value.pop()
        }
        inputText.value = text
      }
    } finally {
      if (run === activeRun) {
        isStreaming.value = false
        abortController.value = null
      }
    }
  }

  function parseSSEBlock(block: string): { type: string; text?: string; data?: any } | null {
    const lines = block.split('\n')
    let event = ''
    let data = ''
    for (const line of lines) {
      if (line.startsWith('event: ')) {
        event = line.slice(7)
      } else if (line.startsWith('data: ')) {
        data = line.slice(6)
      }
    }
    if (!event || !data) return null

    try {
      const payload = JSON.parse(data)
      return { type: event, text: payload.text, data: payload }
    } catch {
      return { type: event, data }
    }
  }

  function stopStream() {
    activeRun++
    if (abortController.value) {
      abortController.value.abort()
    }
    abortController.value = null
    isStreaming.value = false
  }

  return {
    messages,
    inputText,
    isStreaming,
    selectedModel,
    selectedThinking,
    loadMessages,
    sendMessage,
    stopStream,
  }
})
