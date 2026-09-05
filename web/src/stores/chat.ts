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
}

export const useChatStore = defineStore('chat', () => {
  const messages = ref<ChatMessage[]>([])
  const inputText = ref('')
  const isStreaming = ref(false)
  const selectedModel = ref('')
  const selectedThinking = ref('off')
  const abortController = ref<AbortController | null>(null)

  // 递增序号：只有最新一次流式任务能改写流状态，旧的收尾一律作废
  let activeRun = 0

  const connectionStore = useConnectionStore()
  const sessionsStore = useSessionsStore()

  // 默认模型
  watch(() => connectionStore.models, (models) => {
    if (models.length > 0 && !selectedModel.value) {
      const def = models.find(m => m.default)
      selectedModel.value = def?.id || models[0].id
    }
  }, { immediate: true })

  // 切换模型时更新 thinking 选项
  watch(selectedModel, (modelId) => {
    const model = connectionStore.models.find(m => m.id === modelId)
    if (model) {
      selectedThinking.value = model.thinking_levels?.[0] || 'off'
    }
  })

  // 切换会话时中止在途流：否则 A 会话生成中途切到 B，A 结束后会把历史写回 B 界面
  watch(() => sessionsStore.currentSessionId, () => {
    activeRun++
    if (abortController.value) {
      abortController.value.abort()
    }
    abortController.value = null
    isStreaming.value = false
    messages.value = [] // 立即清空旧会话消息，等待新会话加载
  })

  async function loadMessages(sessionId: string) {
    if (!sessionId || !connectionStore.isConnected) {
      messages.value = []
      return
    }
    const res = await fetch(`${connectionStore.serverUrl}/api/sessions/${sessionId}/messages`)
    if (res.ok) {
      const data = await res.json()
      // 加载期间若已切换会话，丢弃这份过期历史，防止串台
      if (sessionsStore.currentSessionId !== sessionId) return
      messages.value = data.map((m: any) => ({
        id: m.id,
        type: m.type,
        content: m.content,
        reasoning: m.reasoning,
        model: m.model,
        usage: m.usage,
        duration_ms: m.duration_ms,
      }))
    }
  }

  async function sendMessage() {
    const text = inputText.value.trim()
    if (!text || !connectionStore.isConnected) return

    const sessionId = sessionsStore.currentSessionId
    if (!sessionId) {
      // 需要先创建会话
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

    // 添加 user 消息到列表（乐观更新）
    const userMsg: ChatMessage = {
      id: 'temp_' + Date.now(),
      type: 'user',
      content: text,
    }
    messages.value.push(userMsg)
    inputText.value = ''

    // 开始流式请求
    isStreaming.value = true
    abortController.value = new AbortController()

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
        return
      }

      if (!res.ok) {
        return
      }

      // 创建 assistant 消息占位
      const assistantMsg: ChatMessage = {
        id: 'stream_' + Date.now(),
        type: 'assistant',
        content: '',
        reasoning: '',
        model: selectedModel.value,
      }
      messages.value.push(assistantMsg)

      // SSE 读取
      const reader = res.body!.getReader()
      const dec = new TextDecoder()
      let buf = ''

      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        buf += dec.decode(value, { stream: true })

        let i: number
        while ((i = buf.indexOf('\n\n')) >= 0) {
          const block = buf.slice(0, i)
          buf = buf.slice(i + 2)
          handleSSEBlock(block, assistantMsg)
        }
      }

      // 处理剩余
      if (buf.trim()) {
        handleSSEBlock(buf, assistantMsg)
      }

      // 刷新历史（loadMessages 内部会校验会话未切换）
      if (run === activeRun && sessionsStore.currentSessionId === currentId) {
        await loadMessages(currentId)
      }
    } catch (e: any) {
      if (e.name !== 'AbortError') {
        console.error('发送失败', e)
      }
    } finally {
      // 只有最新一次任务才能清理流状态，避免旧任务的 finally 覆盖新任务
      if (run === activeRun) {
        isStreaming.value = false
        abortController.value = null
      }
    }
  }

  function handleSSEBlock(block: string, msg: ChatMessage) {
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
    if (!event || !data) return

    try {
      const payload = JSON.parse(data)
      switch (event) {
        case 'delta':
          msg.content += payload.text || ''
          break
        case 'reasoning':
          msg.reasoning = (msg.reasoning || '') + (payload.text || '')
          break
        case 'usage':
          msg.usage = payload
          break
      }
    } catch {
      // 忽略解析错误
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
