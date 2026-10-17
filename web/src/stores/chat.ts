import { computed, reactive, ref } from 'vue'
import { defineStore } from 'pinia'
import { postJSON, responseError } from '../api/client'
import { SSEParser, type SSEFrame } from '../api/sse'
import type { EventDTO, StopDTO, TurnStartedDTO } from '../api/types'
import { useSessionsStore } from './sessions'

// 聊天项只有三种：消息、工具调用、系统提示。
export interface ChatMessage {
  kind: 'message'
  id: string
  seq: number
  role: 'user' | 'assistant'
  content: string
  reasoning: string
  streaming: boolean
}

export type ToolStatus = 'called' | 'awaiting' | 'done' | 'failed' | 'rejected' | 'cancelled'
export type ApprovalState = 'none' | 'pending' | 'approved' | 'rejected'

export interface ChatTool {
  kind: 'tool'
  id: string
  seq: number
  name: string
  args: string
  result: string
  resultStatus: string
  status: ToolStatus
  approval: ApprovalState
}

export interface ChatNotice {
  kind: 'notice'
  id: string
  seq: number
  text: string
  tone: 'muted' | 'error'
}

export type ChatItem = ChatMessage | ChatTool | ChatNotice
export type LinkState = 'idle' | 'connecting' | 'open' | 'reconnecting' | 'error'

// 心跳每 15 秒一次；45 秒没有任何数据视为半开连接并主动重连。
const WATCHDOG_MS = 45_000
const MAX_BACKOFF_MS = 15_000

export const useChatStore = defineStore('chat', () => {
  const sessions = useSessionsStore()

  const items = ref<ChatItem[]>([])
  const byId = new Map<string, ChatItem>()
  const activeAssistantId = ref('')
  const activeTurnId = ref('')
  const streaming = ref(false)
  const stopping = ref(false)
  const linkState = ref<LinkState>('idle')
  const linkMessage = ref('')

  let lastSeq = 0
  let streamGeneration = 0
  let streamAbort: AbortController | null = null
  let reconnectTimer: number | null = null
  let watchdogTimer: number | null = null
  let reconnectAttempt = 0
  let sessionsTimer: number | null = null

  const activeMessage = computed<ChatMessage | null>(() => {
    const id = activeAssistantId.value
    if (!id) return null
    const item = byId.get(id)
    return item && item.kind === 'message' ? item : null
  })

  const awaitingApproval = computed(() => items.value.some(
    item => item.kind === 'tool' && item.approval === 'pending',
  ))

  // ---- 基础事件折叠 ----

  function addItem<T extends ChatItem>(item: T): T {
    const proxy = reactive(item) as unknown as T
    byId.set(item.id, proxy)
    items.value.push(proxy)
    return proxy
  }

  function ensureAssistant(event: EventDTO): ChatMessage {
    const existing = byId.get(event.SubjectID)
    if (existing && existing.kind === 'message') return existing
    return addItem({
      kind: 'message',
      id: event.SubjectID,
      seq: event.Seq,
      role: 'assistant',
      content: '',
      reasoning: '',
      streaming: true,
    })
  }

  function attrString(attrs: Record<string, unknown> | undefined, key: string): string {
    const value = attrs?.[key]
    return typeof value === 'string' ? value : ''
  }

  function resultStatus(raw: string): string {
    if (!raw) return ''
    try {
      const parsed = JSON.parse(raw) as { status?: unknown }
      return typeof parsed.status === 'string' ? parsed.status : ''
    } catch {
      return ''
    }
  }

  function addNotice(seq: number, text: string, tone: 'muted' | 'error'): void {
    const id = `notice-${seq}`
    if (byId.has(id)) return
    addItem({ kind: 'notice', id, seq, text, tone })
  }

  function scheduleSessionRefresh(delay = 300): void {
    if (sessionsTimer !== null) window.clearTimeout(sessionsTimer)
    sessionsTimer = window.setTimeout(() => {
      sessionsTimer = null
      void sessions.load().catch(() => undefined)
    }, delay)
  }

  function applyEvent(event: EventDTO): void {
    switch (event.Type) {
      case 'message.delta': {
        const message = ensureAssistant(event)
        const delta = event.BlobText || ''
        if (!delta) return
        if (attrString(event.Attrs, 'kind') === 'reasoning') message.reasoning += delta
        else message.content += delta
        message.streaming = true
        activeAssistantId.value = message.id
        streaming.value = true
        return
      }
      case 'message.created': {
        const role = attrString(event.Attrs, 'role') === 'user' ? 'user' : 'assistant'
        const content = event.BlobText ?? ''
        const existing = byId.get(event.SubjectID)
        if (role === 'user') {
          if (existing && existing.kind === 'message') {
            existing.content = content
            existing.streaming = false
          } else {
            addItem({
              kind: 'message',
              id: event.SubjectID,
              seq: event.Seq,
              role: 'user',
              content,
              reasoning: '',
              streaming: false,
                    })
          }
          scheduleSessionRefresh(400)
          return
        }
        const target = existing && existing.kind === 'message'
          ? existing
          : addItem({
            kind: 'message',
            id: event.SubjectID,
            seq: event.Seq,
            role: 'assistant',
            content: '',
            reasoning: '',
            streaming: false,
                })
        // message.created 是最终正文；用 blob 正文覆盖流式累积，避免回放时重复。
        if (content) target.content = content
        target.streaming = false
        if (activeAssistantId.value === event.SubjectID) activeAssistantId.value = ''
        return
      }
      case 'tool.called': {
        const id = event.SubjectID
        if (!byId.has(id)) {
          addItem({
            kind: 'tool',
            id,
            seq: event.Seq,
                  name: attrString(event.Attrs, 'name') || 'tool',
            args: event.BlobText || '',
            result: '',
            resultStatus: '',
            status: 'called',
            approval: 'none',
          })
        }
        activeAssistantId.value = ''
        return
      }
      case 'tool.returned': {
        let tool = byId.get(event.SubjectID)
        if (!tool || tool.kind !== 'tool') {
          tool = addItem({
            kind: 'tool',
            id: event.SubjectID,
            seq: event.Seq,
                  name: 'tool',
            args: '',
            result: '',
            resultStatus: '',
            status: 'called',
            approval: 'none',
          })
        }
        if (tool.kind !== 'tool') return
        tool.result = event.BlobText || ''
        tool.resultStatus = resultStatus(tool.result)
        tool.status = tool.resultStatus === 'failure'
          ? 'failed'
          : tool.resultStatus === 'cancelled' ? 'cancelled' : 'done'
        return
      }
      case 'approval.requested': {
        const toolCallID = attrString(event.Attrs, 'tool_call_id') || event.SubjectID
        let tool = byId.get(toolCallID)
        if (!tool || tool.kind !== 'tool') {
          tool = addItem({
            kind: 'tool',
            id: toolCallID,
            seq: event.Seq,
                  name: attrString(event.Attrs, 'name') || 'tool',
            args: '',
            result: '',
            resultStatus: '',
            status: 'awaiting',
            approval: 'pending',
          })
        }
        if (tool.kind !== 'tool') return
        tool.status = 'awaiting'
        tool.approval = 'pending'
        activeAssistantId.value = ''
        return
      }
      case 'approval.decided': {
        const tool = byId.get(event.SubjectID)
        if (!tool || tool.kind !== 'tool') return
        tool.approval = attrString(event.Attrs, 'decision') === 'approved' ? 'approved' : 'rejected'
        tool.status = tool.approval === 'approved' ? 'called' : 'rejected'
        return
      }
      case 'turn.started': {
        activeTurnId.value = event.SubjectID
        streaming.value = true
        stopping.value = false
        return
      }
      case 'llm.requested': {
        streaming.value = true
        return
      }
      case 'turn.completed': {
        streaming.value = false
        stopping.value = false
        activeAssistantId.value = ''
        activeTurnId.value = ''
        // 回合结束后不再允许审批；停在等待状态的工具标记为已取消。
        for (const item of items.value) {
          if (item.kind === 'tool' && item.approval === 'pending') {
            item.approval = 'none'
            item.status = 'cancelled'
          }
        }
        const failure = attrString(event.Attrs, 'error')
        if (failure) {
          addNotice(
            event.Seq,
            failure === 'stopped' ? '已停止' : `回合失败：${failure}`,
            failure === 'stopped' ? 'muted' : 'error',
          )
        }
        scheduleSessionRefresh(200)
        return
      }
      default:
        return
    }
  }

  // ---- SSE 连接：补发 + 指数退避 ----

  function clearReconnectTimer(): void {
    if (reconnectTimer !== null) {
      window.clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
  }

  function clearWatchdog(): void {
    if (watchdogTimer !== null) {
      window.clearTimeout(watchdogTimer)
      watchdogTimer = null
    }
  }

  function clearSessionsTimer(): void {
    if (sessionsTimer !== null) {
      window.clearTimeout(sessionsTimer)
      sessionsTimer = null
    }
  }

  function touchWatchdog(): void {
    clearWatchdog()
    watchdogTimer = window.setTimeout(() => {
      watchdogTimer = null
      if (linkState.value !== 'open') return
      linkState.value = 'reconnecting'
      linkMessage.value = '连接超时，正在重连…'
      streamAbort?.abort()
      scheduleReconnect()
    }, WATCHDOG_MS)
  }

  function scheduleReconnect(): void {
    if (reconnectTimer !== null) return
    const attempt = reconnectAttempt++
    const delay = Math.min(500 * 2 ** attempt, MAX_BACKOFF_MS) + Math.floor(Math.random() * 300)
    linkState.value = 'reconnecting'
    linkMessage.value = `连接已断开，${(delay / 1000).toFixed(1)} 秒后重连…`
    reconnectTimer = window.setTimeout(() => {
      reconnectTimer = null
      const sessionId = sessions.currentId
      if (!sessionId) return
      void connect(sessionId, lastSeq)
    }, delay)
  }

  function consumeFrame(frame: SSEFrame): void {
    let event: EventDTO
    try {
      event = JSON.parse(frame.data) as EventDTO
    } catch {
      return
    }
    if (!event || typeof event.Seq !== 'number') return
    // after_seq 已经去重；这里再挡一次异常重复帧。
    if (event.Seq <= lastSeq) return
    lastSeq = event.Seq
    applyEvent(event)
  }

  async function connect(sessionId: string, afterSeq: number): Promise<void> {
    const generation = ++streamGeneration
    streamAbort?.abort()
    clearWatchdog()
    const controller = new AbortController()
    streamAbort = controller
    if (linkState.value !== 'reconnecting') linkState.value = 'connecting'

    try {
      const response = await fetch(
        `/api/sessions/${encodeURIComponent(sessionId)}/events?after_seq=${afterSeq}`,
        {
          method: 'GET',
          headers: { Accept: 'text/event-stream' },
          cache: 'no-store',
          signal: controller.signal,
        },
      )
      if (generation !== streamGeneration) return
      if (!response.ok) {
        const error = await responseError(response)
        if (response.status === 404 || response.status === 410) {
          linkState.value = 'error'
          linkMessage.value = error.message
          return
        }
        throw error
      }
      if (!response.body) throw new Error('浏览器没有拿到事件流响应体')
      linkState.value = 'open'
      linkMessage.value = ''
      reconnectAttempt = 0
      touchWatchdog()

      const reader = response.body.getReader()
      const decoder = new TextDecoder()
      const parser = new SSEParser()
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        if (generation !== streamGeneration) return
        touchWatchdog()
        for (const frame of parser.push(decoder.decode(value, { stream: true }))) {
          consumeFrame(frame)
        }
      }
      if (generation === streamGeneration) scheduleReconnect()
    } catch {
      if (generation !== streamGeneration) return
      scheduleReconnect()
    } finally {
      if (generation === streamGeneration) {
        clearWatchdog()
        streamAbort = null
      }
    }
  }

  function resetState(): void {
    streamGeneration++
    streamAbort?.abort()
    streamAbort = null
    clearReconnectTimer()
    clearWatchdog()
    clearSessionsTimer()
    items.value = []
    byId.clear()
    lastSeq = 0
    activeAssistantId.value = ''
    activeTurnId.value = ''
    streaming.value = false
    stopping.value = false
    linkState.value = 'idle'
    linkMessage.value = ''
    reconnectAttempt = 0
  }

  function openSession(sessionId: string): void {
    if (!sessionId) return
    resetState()
    sessions.select(sessionId)
    linkState.value = 'connecting'
    void connect(sessionId, 0)
  }

  function startNew(): void {
    resetState()
    sessions.clearCurrent()
  }

  function retryNow(): void {
    if (!sessions.currentId) return
    if (linkState.value === 'open' || linkState.value === 'connecting') return
    clearReconnectTimer()
    reconnectAttempt = 0
    void connect(sessions.currentId, lastSeq)
  }

  if (typeof window !== 'undefined') {
    window.addEventListener('online', retryNow)
    document.addEventListener('visibilitychange', () => {
      if (!document.hidden) retryNow()
    })
  }

  // ---- 发送 / 停止 / 审批 ----

  async function send(text: string, model: string): Promise<void> {
    const body = text.trim()
    if (!body) return
    let sessionId = sessions.currentId
    if (!sessionId) {
      const created = await sessions.create(model)
      sessionId = created.ID
      sessions.select(sessionId)
      resetState()
      void connect(sessionId, 0)
    }
    streaming.value = true
    try {
      await postJSON<TurnStartedDTO>(
        `/api/sessions/${encodeURIComponent(sessionId)}/messages`,
        { Text: body, Model: model || undefined },
      )
    } catch (error) {
      if (!activeTurnId.value) streaming.value = false
      throw error
    }
    scheduleSessionRefresh(500)
  }

  async function stop(): Promise<void> {
    const sessionId = sessions.currentId
    if (!sessionId) return
    stopping.value = true
    try {
      const result = await postJSON<StopDTO>(
        `/api/sessions/${encodeURIComponent(sessionId)}/stop`,
        {},
      )
      // 后端没有可停止的回合时才就地收尾；等待审批的回合由 turn.completed 收尾。
      const pendingApproval = items.value.some(item => item.kind === 'tool' && item.approval === 'pending')
      if (!result.Stopped && !pendingApproval) {
        streaming.value = false
        activeAssistantId.value = ''
        activeTurnId.value = ''
      }
    } finally {
      stopping.value = false
    }
  }

  async function decideApproval(tool: ChatTool, approve: boolean): Promise<void> {
    if (tool.approval !== 'pending' || !sessions.currentId) return
    const previousApproval = tool.approval
    const previousStatus = tool.status
    tool.approval = approve ? 'approved' : 'rejected'
    tool.status = approve ? 'called' : 'rejected'
    try {
      await postJSON(
        `/api/sessions/${encodeURIComponent(sessions.currentId)}/approvals/${encodeURIComponent(tool.id)}`,
        { Decision: approve ? 'approved' : 'rejected', Reason: approve ? '' : '用户在界面拒绝' },
      )
    } catch (error) {
      tool.approval = previousApproval
      tool.status = previousStatus
      throw error
    }
  }

  return {
    items,
    activeMessage,
    streaming,
    stopping,
    linkState,
    linkMessage,
    awaitingApproval,
    openSession,
    startNew,
    send,
    stop,
    decideApproval,
    retryNow,
  }
})
