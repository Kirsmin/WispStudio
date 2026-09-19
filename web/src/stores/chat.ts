import { computed, reactive, ref, watch } from 'vue'
import { defineStore } from 'pinia'
import { useConnectionStore } from './connection'
import { useSessionsStore } from './sessions'

export interface ChatMessage {
  id: string
  type: 'user' | 'assistant'
  content: string
  reasoning?: string
  phase?: 'waiting' | 'reasoning' | 'answer' | 'done' | 'error'
  streaming?: boolean
  error?: string
  usage?: Record<string, number>
  model?: string
  duration_ms?: number
  ttft_ms?: number
}

export interface TimelineRecord {
  id: string
  session_id: string
  turn_id?: string
  seq: number
  model_call_id?: string
  kind: string
  content?: string
  data?: Record<string, any>
  created_at: string
  temporary?: boolean
}

export interface TurnState {
  id: string
  session_id: string
  turn_index: number
  status: string
  objective: string
  active_agent: string
  context_epoch: number
  active_agent_run_id?: string
  root_agent_run_id?: string
  active_checkpoint_id?: string
}

export interface ArtifactVersion {
  id: string
  artifact_id: string
  version: number
  content: string
  data?: Record<string, any>
  created_by?: string
  created_at: string
}
export interface Artifact {
  id: string
  session_id: string
  turn_id: string
  type: string
  name: string
  active_version: number
  versions: ArtifactVersion[]
}
export interface Approval {
  id: string
  session_id: string
  turn_id: string
  agent_run_id?: string
  tool_call_id: string
  tool_name: string
  args: Record<string, any>
  risk: string
  status: string
}
export interface AgentRun {
  id: string
  turn_id: string
  parent_run_id?: string
  profile_id: string
  status: string
  result?: Record<string, any>
}
export interface RuntimeCapabilities {
  can_pause?: boolean
  can_resume?: boolean
  can_stop?: boolean
  can_cancel?: boolean
  can_steer?: boolean
  can_start_build?: boolean
  can_approve?: boolean
  can_reject?: boolean
  can_challenge?: boolean
}
export interface RuntimeState {
  turn: TurnState | null
  turns: TurnState[]
  execution: { active: boolean; turn_id?: string; started_at?: string }
  capabilities: RuntimeCapabilities
  artifacts: Artifact[]
  approvals: Approval[]
  agent_runs: AgentRun[]
  can_restore_fold: boolean
}
export interface StreamingModel {
  callId: string
  agentRunId?: string
  profileId?: string
  model?: string
  reasoning: string
  content: string
  phase: 'waiting' | 'reasoning' | 'answer' | 'done' | 'error'
  usage?: Record<string, number>
  error?: string
}

export const OPENAI_REASONING_LEVELS = ['default', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'] as const
export function modelThinkingLevels(model?: { id?: string; thinking_levels?: string[]; thinking_style?: string }): string[] {
  if (!model) return ['default']
  const configured = (model.thinking_levels || []).map(level => String(level).trim().toLowerCase()).filter(Boolean)
  const modelId = String(model.id || '').toLowerCase()
  if (modelId.startsWith('deepseek-v4-')) return ['default', 'none', 'low', 'medium', 'high', 'xhigh', 'max']
  if (model.thinking_style === 'enable_thinking') return configured.some(level => level !== 'off') ? configured : ['off', 'on']
  if (model.thinking_style === 'disabled') return ['default']
  if (configured.length === 0 || configured.every(level => level === 'off' || level === 'default')) return [...OPENAI_REASONING_LEVELS]
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
      const parsed = this.parse(this.buffer.slice(0, index)); this.buffer = this.buffer.slice(index + 2)
      if (parsed) out.push(parsed); index = this.buffer.indexOf('\n\n')
    }
    return out
  }
  flush(): SSEMessage[] { const parsed = this.parse(this.buffer.trim()); this.buffer = ''; return parsed ? [parsed] : [] }
  private parse(block: string): SSEMessage | null {
    if (!block) return null
    let event = 'message'; const data: string[] = []
    for (const line of block.split('\n')) { if (line.startsWith('event:')) event = line.slice(6).trim(); else if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, '')) }
    return data.length ? { event, data: data.join('\n') } : null
  }
}
function parseJSON(data: string): Record<string, any> { try { return JSON.parse(data) as Record<string, any> } catch { return {} } }

export const useChatStore = defineStore('chat', () => {
  const timeline = ref<TimelineRecord[]>([])
  const runtimeState = ref<RuntimeState>({ turn: null, turns: [], execution: { active: false }, capabilities: {}, artifacts: [], approvals: [], agent_runs: [], can_restore_fold: false })
  const streamingModel = ref<StreamingModel | null>(null)
  const inputText = ref('')
  const isStreaming = ref(false)
  const sendingSteering = ref(false)
  const backgroundGenerating = ref(false)
  const runtimeActionPending = ref('')
  const notice = ref('')
  const selectedProvider = ref('')
  const selectedModel = ref('')
  const selectedThinking = ref('default')
  const abortController = ref<AbortController | null>(null)

  const connectionStore = useConnectionStore()
  const sessionsStore = useSessionsStore()
  let activeRun = 0
  let loadSeq = 0
  let backgroundTimer: number | null = null
  let liveRefreshTimer: number | null = null

  const isBusy = computed(() => isStreaming.value || sendingSteering.value)
  const executionActive = computed(() => Boolean(runtimeState.value.execution?.active) || isStreaming.value || backgroundGenerating.value)
  const capabilities = computed(() => runtimeState.value.capabilities || {})
  const thinkingOptions = computed(() => modelThinkingLevels(currentModel()))

  function providerModels(providerId = selectedProvider.value) { return providerId ? connectionStore.models.filter(model => model.provider_id === providerId) : connectionStore.models }
  function currentModel() { return connectionStore.models.find(model => model.id === selectedModel.value && (!selectedProvider.value || model.provider_id === selectedProvider.value)) }
  function ensureSelection() {
    const providersWithModels = connectionStore.providers.filter(provider => connectionStore.models.some(model => model.provider_id === provider.id))
    if (!selectedProvider.value || !providersWithModels.some(provider => provider.id === selectedProvider.value)) {
      const provider = providersWithModels.find(item => item.default && item.available) || providersWithModels.find(item => item.available) || providersWithModels[0]
      selectedProvider.value = provider?.id || connectionStore.models[0]?.provider_id || ''
    }
    const candidates = providerModels()
    if (!candidates.some(model => model.id === selectedModel.value)) selectedModel.value = (candidates.find(model => model.default) || candidates[0])?.id || ''
    const levels = modelThinkingLevels(currentModel())
    if (!levels.includes(selectedThinking.value)) selectedThinking.value = levels.includes('default') ? 'default' : levels[0]
  }
  watch(() => [connectionStore.providers, connectionStore.models], ensureSelection, { immediate: true, deep: true })
  watch(selectedProvider, ensureSelection)
  watch(selectedModel, () => { const levels = modelThinkingLevels(currentModel()); if (!levels.includes(selectedThinking.value)) selectedThinking.value = levels.includes('default') ? 'default' : levels[0] })

  function applySessionSelection(sessionId: string) {
    const session = sessionsStore.sessions.find(item => item.id === sessionId); if (!session) return
    if (session.provider && connectionStore.models.some(model => model.provider_id === session.provider)) selectedProvider.value = session.provider
    if (session.model && connectionStore.models.some(model => model.id === session.model)) selectedModel.value = session.model
    ensureSelection()
  }

  async function loadTimeline(sessionId: string, reset = false) {
    if (!sessionId || !connectionStore.isConnected) { timeline.value = []; return }
    const seq = ++loadSeq
    const sameSession = timeline.value.length === 0 || timeline.value[0]?.session_id === sessionId
    const after = !reset && sameSession && timeline.value.length ? Number(timeline.value[timeline.value.length - 1]?.seq || 0) : 0
    const query = after > 0 ? `?after=${after}` : ''
    const res = await fetch(connectionStore.api(`/api/sessions/${encodeURIComponent(sessionId)}/timeline${query}`), { cache: 'no-store' })
    if (!res.ok) return
    const data = await res.json() as TimelineRecord[]
    if (seq !== loadSeq || sessionsStore.currentSessionId !== sessionId) return
    const records = Array.isArray(data) ? data.map(normalizeRecord) : []
    if (reset || after === 0 || !sameSession) {
      timeline.value = records
      return
    }
    mergeRecords(records)
  }
  async function loadRuntime(sessionId: string) {
    if (!sessionId || !connectionStore.isConnected) return
    const res = await fetch(connectionStore.api(`/api/sessions/${encodeURIComponent(sessionId)}/runtime`), { cache: 'no-store' })
    if (!res.ok) return
    const data = await res.json() as RuntimeState
    if (sessionsStore.currentSessionId !== sessionId) return
    runtimeState.value = data
    backgroundGenerating.value = Boolean(data.execution?.active) && !isStreaming.value
    if (data.execution?.active) scheduleBackgroundPoll(sessionId)
  }
  async function refreshAll(sessionId = sessionsStore.currentSessionId, resetTimeline = false, includeSessions = true) {
    if (!sessionId) return
    await Promise.all([loadTimeline(sessionId, resetTimeline), loadRuntime(sessionId)])
    if (includeSessions) await sessionsStore.loadSessions().catch(() => undefined)
  }
  function scheduleLiveRefresh(sessionId: string, delay = 80) {
    if (liveRefreshTimer != null) window.clearTimeout(liveRefreshTimer)
    liveRefreshTimer = window.setTimeout(() => {
      liveRefreshTimer = null
      if (sessionsStore.currentSessionId !== sessionId) return
      void Promise.all([loadTimeline(sessionId), loadRuntime(sessionId)])
    }, delay)
  }

  function normalizeRecord(r: any): TimelineRecord {
    let data = r.data
    if (typeof data === 'string') { try { data = JSON.parse(data) } catch { data = {} } }
    return { id: String(r.id || `record_${Date.now()}`), session_id: String(r.session_id || ''), turn_id: r.turn_id, seq: Number(r.seq || 0), model_call_id: r.model_call_id, kind: String(r.kind || 'runtime.status'), content: String(r.content || ''), data: data || {}, created_at: String(r.created_at || '') }
  }
  function upsertRecord(record: TimelineRecord) {
    const index = timeline.value.findIndex(item => item.id === record.id)
    if (index >= 0) timeline.value[index] = record
    else timeline.value.push(record)
  }
  function mergeRecords(records: TimelineRecord[]) {
    if (!records.length) return
    const existing = new Map(timeline.value.map((item, index) => [item.id, index]))
    let changedOrder = false
    for (const record of records) {
      const index = existing.get(record.id)
      if (index == null) {
        timeline.value.push(record)
        existing.set(record.id, timeline.value.length - 1)
        changedOrder = true
      } else {
        timeline.value[index] = record
      }
    }
    if (changedOrder) timeline.value.sort((a, b) => a.seq - b.seq)
  }

  async function postChat(text: string, steeringOnly = false) {
    const sessionId = sessionsStore.currentSessionId
    if (!sessionId) return
    const controller = steeringOnly ? new AbortController() : new AbortController()
    if (!steeringOnly) abortController.value = controller
    const decoder = new SSEDecoder(); const textDecoder = new TextDecoder()
    const res = await fetch(connectionStore.api(`/api/sessions/${encodeURIComponent(sessionId)}/chat`), {
      method: 'POST', headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
      body: JSON.stringify({ message: text, provider: selectedProvider.value, model: selectedModel.value, thinking: selectedThinking.value }), signal: controller.signal,
    })
    if (!res.ok) throw new Error((await res.text()).trim() || `发送失败 (${res.status})`)
    if (!res.body) throw new Error('浏览器没有拿到流式响应体')
    const handle = (event: SSEMessage) => {
      const payload = parseJSON(event.data)
      if (event.event === 'ack') {
        const record = payload.record
        if (record?.id) upsertRecord(normalizeRecord(record))
      } else if (event.event === 'model.start') {
        streamingModel.value = reactive<StreamingModel>({ callId: String(payload.call_id || ''), agentRunId: payload.agent_run_id, profileId: payload.profile_id, model: payload.model, reasoning: '', content: '', phase: 'waiting' })
        runtimeState.value.execution = { active: true, turn_id: String(payload.turn_id || runtimeState.value.turn?.id || '') }
        if (runtimeState.value.turn && payload.profile_id) {
          runtimeState.value.turn.active_agent = String(payload.profile_id)
          runtimeState.value.turn.status = 'running'
        }
      } else if (event.event === 'reasoning') {
        if (streamingModel.value) { streamingModel.value.reasoning += String(payload.text || ''); if (!streamingModel.value.content) streamingModel.value.phase = 'reasoning' }
      } else if (event.event === 'delta') {
        if (streamingModel.value) { streamingModel.value.content += String(payload.text || ''); streamingModel.value.phase = 'answer' }
      } else if (event.event === 'usage') {
        if (streamingModel.value) streamingModel.value.usage = payload as Record<string, number>
      } else if (event.event === 'model.done') {
        if (streamingModel.value) { streamingModel.value.phase = payload.error ? 'error' : 'done'; streamingModel.value.error = payload.error ? String(payload.error) : undefined }
        scheduleLiveRefresh(sessionId, 60)
      } else if (event.event.startsWith('tool.') || event.event.startsWith('approval.')) {
        scheduleLiveRefresh(sessionId)
      } else if (event.event === 'runtime.status') {
        const status = String(payload.status || '')
        if (runtimeState.value.turn && status) runtimeState.value.turn.status = status
        if (['waiting_user', 'paused', 'completed', 'stopped', 'cancelled', 'failed'].includes(status)) runtimeState.value.execution.active = false
        scheduleLiveRefresh(sessionId, 40)
      } else if (event.event === 'runtime.error') {
        window.$message?.error(String(payload.message || 'Runtime 执行失败'))
      }
    }
    const reader = res.body.getReader()
    while (true) { const { done, value } = await reader.read(); if (done) break; for (const event of decoder.feed(textDecoder.decode(value, { stream: true }))) handle(event) }
    for (const event of decoder.feed(textDecoder.decode())) handle(event); for (const event of decoder.flush()) handle(event)
  }

  async function sendMessage() {
    const text = inputText.value.trim()
    if (!text || !connectionStore.isConnected || !selectedModel.value) return
    ensureSelection()
    if (!sessionsStore.currentSessionId) {
      const session = await sessionsStore.createPersistedSession(text.slice(0, 20)); applySessionSelection(session.id)
    }
    const sessionId = sessionsStore.currentSessionId; if (!sessionId) return
    inputText.value = ''
    const steering = executionActive.value && (capabilities.value.can_steer || isStreaming.value)
    if (steering) {
      sendingSteering.value = true
      try { await postChat(text, true); await loadTimeline(sessionId) } catch (error) { const message = error instanceof Error ? error.message : String(error); window.$message?.error(message); inputText.value = text } finally { sendingSteering.value = false }
      return
    }
    if (isStreaming.value) return
    const run = ++activeRun; isStreaming.value = true; backgroundGenerating.value = false; notice.value = ''; streamingModel.value = null
    try { await postChat(text, false); if (run === activeRun) await refreshAll(sessionId) }
    catch (error: any) { if (error?.name !== 'AbortError' && run === activeRun) { const message = error instanceof Error ? error.message : String(error); window.$message?.error(message); console.error('发送失败', error) } }
    finally { if (run === activeRun) { isStreaming.value = false; abortController.value = null; streamingModel.value = null; await loadRuntime(sessionId) } }
  }

  async function runtimeCommand(path: string, body: Record<string, any> = {}) {
    const sessionId = sessionsStore.currentSessionId; const turn = runtimeState.value.turn; if (!sessionId || !turn) return
    runtimeActionPending.value = path
    try {
      const response = await fetch(connectionStore.api(`/api/sessions/${encodeURIComponent(sessionId)}/turns/${encodeURIComponent(turn.id)}/${path}`), { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...body, provider: selectedProvider.value, model: selectedModel.value, thinking: selectedThinking.value }) })
      if (!response.ok) throw new Error(await readHTTPError(response, '操作失败'))
      scheduleLiveRefresh(sessionId, 30)
    } finally {
      runtimeActionPending.value = ''
    }
  }
  async function pauseTurn() { await runtimeCommand('pause') }
  async function resumeTurn() { await runtimeCommand('resume') }
  async function stopTurn() { await runtimeCommand('stop') }
  async function startBuild() {
    const turn = runtimeState.value.turn
    if (!turn) return
    const previous = { agent: turn.active_agent, status: turn.status, execution: { ...runtimeState.value.execution }, canStartBuild: runtimeState.value.capabilities.can_start_build }
    // 先更新本地状态，让“开始执行”点击后立即进入 Build；服务端状态随后增量校准。
    turn.active_agent = 'build'
    turn.status = 'running'
    runtimeState.value.execution = { active: true, turn_id: turn.id }
    runtimeState.value.capabilities.can_start_build = false
    backgroundGenerating.value = true
    try {
      await runtimeCommand('start-build')
      scheduleBackgroundPoll(turn.session_id)
    } catch (error) {
      turn.active_agent = previous.agent
      turn.status = previous.status
      runtimeState.value.execution = previous.execution
      runtimeState.value.capabilities.can_start_build = previous.canStartBuild
      backgroundGenerating.value = Boolean(previous.execution.active)
      throw error
    }
  }
  async function hardCancel() {
    const id = sessionsStore.currentSessionId; if (!id) return
    await fetch(connectionStore.api(`/api/sessions/${encodeURIComponent(id)}/chat/cancel`), { method: 'POST' }).catch(() => undefined)
    ++activeRun; abortController.value?.abort(); abortController.value = null; isStreaming.value = false; streamingModel.value = null
    await refreshAll(id)
  }
  function stopStream() { void hardCancel() }
  function stopGeneration() { return hardCancel() }

  async function exportSession() {
    const sessionId = sessionsStore.currentSessionId
    if (!sessionId) throw new Error('请先选择会话')
    const response = await fetch(connectionStore.api(`/api/sessions/${encodeURIComponent(sessionId)}/export`), { cache: 'no-store' })
    if (!response.ok) throw new Error(await readHTTPError(response, '导出 Session 失败'))
    const blob = await response.blob()
    const session = sessionsStore.sessions.find(item => item.id === sessionId)
    const title = sanitizeFilename(session?.title || sessionId)
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = `${title || 'wisp-session'}-${sessionId}.zip`
    document.body.appendChild(anchor)
    anchor.click()
    anchor.remove()
    window.setTimeout(() => URL.revokeObjectURL(url), 1000)
  }

  async function decideApproval(id: string, decision: 'approve' | 'reject') {
    const response = await fetch(connectionStore.api(`/api/approvals/${encodeURIComponent(id)}/${decision}`), { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ provider: selectedProvider.value, model: selectedModel.value, thinking: selectedThinking.value }) })
    if (!response.ok) throw new Error(await readHTTPError(response, '处理 Approval 失败'))
    if (sessionsStore.currentSessionId) await refreshAll(sessionsStore.currentSessionId)
  }
  async function challengeApproval(id: string, question: string) {
    const response = await fetch(connectionStore.api(`/api/approvals/${encodeURIComponent(id)}/challenge`), { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ question, provider: selectedProvider.value, model: selectedModel.value, thinking: selectedThinking.value }) })
    if (!response.ok) throw new Error(await readHTTPError(response, 'Challenge 失败'))
    if (sessionsStore.currentSessionId) scheduleBackgroundPoll(sessionsStore.currentSessionId)
  }
  async function activateArtifact(artifactId: string, version: number) {
    const response = await fetch(connectionStore.api(`/api/artifacts/${encodeURIComponent(artifactId)}/activate`), { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ version }) })
    if (!response.ok) throw new Error(await readHTTPError(response, '切换 Artifact 版本失败'))
    if (sessionsStore.currentSessionId) await refreshAll(sessionsStore.currentSessionId)
  }
  async function foldTurn(turnId: string) {
    const id = sessionsStore.currentSessionId; if (!id) return
    const response = await fetch(connectionStore.api(`/api/sessions/${encodeURIComponent(id)}/turns/${encodeURIComponent(turnId)}/fold`), { method: 'POST' })
    if (!response.ok) throw new Error(await readHTTPError(response, 'Fold 失败')); await refreshAll(id)
  }
  async function restoreFold() {
    const id = sessionsStore.currentSessionId; if (!id) return
    const response = await fetch(connectionStore.api(`/api/sessions/${encodeURIComponent(id)}/folds/restore`), { method: 'POST' })
    if (!response.ok) throw new Error(await readHTTPError(response, 'Restore 失败')); await refreshAll(id)
  }
  async function loadAgentTimeline(runId: string): Promise<TimelineRecord[]> {
    const response = await fetch(connectionStore.api(`/api/agent-runs/${encodeURIComponent(runId)}/timeline`), { cache: 'no-store' })
    if (!response.ok) throw new Error(await readHTTPError(response, '读取 SubAgent Timeline 失败'))
    return ((await response.json()) as TimelineRecord[]).map(normalizeRecord)
  }

  function scheduleBackgroundPoll(sessionId: string) {
    if (backgroundTimer != null) window.clearTimeout(backgroundTimer)
    backgroundTimer = window.setTimeout(async () => {
      backgroundTimer = null; if (sessionsStore.currentSessionId !== sessionId) return
      await Promise.all([loadRuntime(sessionId), loadTimeline(sessionId)])
      if (!runtimeState.value.execution?.active) { backgroundGenerating.value = false; await sessionsStore.loadSessions().catch(() => undefined) }
    }, 800)
  }
  async function refreshRunStatus(sessionId: string) { await loadRuntime(sessionId); return Boolean(runtimeState.value.execution?.active) }

  async function openSession(id: string) {
    if (!id) return; ++activeRun; ++loadSeq; abortController.value?.abort(); abortController.value = null; isStreaming.value = false; streamingModel.value = null; backgroundGenerating.value = false; notice.value = ''; timeline.value = []
    if (backgroundTimer != null) { window.clearTimeout(backgroundTimer); backgroundTimer = null }
    if (liveRefreshTimer != null) { window.clearTimeout(liveRefreshTimer); liveRefreshTimer = null }
    sessionsStore.selectSession(id); applySessionSelection(id); await refreshAll(id, true)
  }
  function newConversation() {
    if (backgroundTimer != null) { window.clearTimeout(backgroundTimer); backgroundTimer = null }
    if (liveRefreshTimer != null) { window.clearTimeout(liveRefreshTimer); liveRefreshTimer = null }
    ++activeRun; ++loadSeq; abortController.value?.abort(); abortController.value = null; isStreaming.value = false; streamingModel.value = null; backgroundGenerating.value = false; runtimeActionPending.value = ''; notice.value = ''; timeline.value = []; runtimeState.value = { turn: null, turns: [], execution: { active: false }, capabilities: {}, artifacts: [], approvals: [], agent_runs: [], can_restore_fold: false }; sessionsStore.beginNewSession()
  }

  return {
    timeline, runtimeState, streamingModel, input: inputText, inputText, isStreaming, isBusy, executionActive, sendingSteering, backgroundGenerating, runtimeActionPending, notice,
    selectedProvider, selectedModel, selectedThinking, thinkingOptions, capabilities,
    loadTimeline, loadRuntime, refreshAll, refreshRunStatus, sendMessage, stopStream, stopGeneration, hardCancel,
    pauseTurn, resumeTurn, stopTurn, startBuild, decideApproval, challengeApproval, activateArtifact, foldTurn, restoreFold, loadAgentTimeline, exportSession,
    openSession, newConversation,
  }
})

async function readHTTPError(response: Response, fallback: string): Promise<string> {
  try { const payload = await response.json() as { error?: string }; return payload.error || fallback } catch { return fallback }
}

function sanitizeFilename(value: string): string {
  return value.trim().replace(/[\\/:*?"<>|\u0000-\u001f]/g, '-').replace(/\s+/g, ' ').slice(0, 72)
}
