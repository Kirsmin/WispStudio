<template>
  <n-drawer :show="show" :width="drawerWidth" placement="right" @update:show="emit('update:show', $event)">
    <n-drawer-content :native-scrollbar="false" closable>
      <template #header>
        <div class="debug-header">
          <div>
            <div class="debug-title">Trace 调试</div>
            <div class="debug-subtitle">为什么调用 · 拼了什么 · 模型决定了什么</div>
          </div>
          <div class="debug-actions">
            <span v-if="calls.length" class="call-count">{{ calls.length }} calls</span>
            <n-button size="tiny" quaternary :loading="loading" @click="reload">刷新</n-button>
          </div>
        </div>
      </template>

      <div v-if="!sessionId" class="debug-empty">选择一个 Session 后可查看 Trace。</div>
      <div v-else-if="loading && !calls.length" class="debug-empty">正在读取调用摘要…</div>
      <div v-else-if="error && !calls.length" class="debug-error">{{ error }}</div>
      <div v-else-if="!calls.length" class="debug-empty">这个 Session 还没有模型调用。</div>

      <div v-else class="trace-list">
        <div v-if="error" class="debug-error compact-error">{{ error }}</div>
        <details
          v-for="call in calls"
          :key="call.id"
          class="trace-call"
          @toggle="onToggle($event, call)"
        >
          <summary class="trace-summary">
            <div class="trace-rail">
              <span class="trace-dot" :class="`dot-${call.status}`" />
              <span class="trace-index">#{{ call.call_index }}</span>
            </div>
            <div class="summary-main">
              <div class="summary-top">
                <span class="agent-chip">{{ agentLabel(call) }}</span>
                <strong>{{ triggerLabel(call) }}</strong>
                <span v-if="call.decision" class="decision-inline">→ {{ call.decision }}</span>
              </div>
              <div class="summary-meta">
                <span>{{ call.model }}</span>
                <span>epoch {{ call.context_epoch }}</span>
                <span v-if="call.prompt_tokens">{{ compactNumber(call.prompt_tokens) }} in</span>
                <span v-if="call.completion_tokens">{{ compactNumber(call.completion_tokens) }} out</span>
                <span v-if="call.ttft_ms">TTFT {{ formatDuration(call.ttft_ms) }}</span>
              </div>
            </div>
            <div class="summary-tail">
              <span class="status-chip" :class="`status-${call.status}`">{{ statusLabel(call.status) }}</span>
              <span class="duration">{{ formatDuration(call.duration_ms) }}</span>
              <span class="chevron">⌄</span>
            </div>
          </summary>

          <div class="trace-body">
            <div v-if="call.error" class="call-error">{{ call.error }}</div>

            <section class="trace-card why-card">
              <div class="card-title">这一轮为什么发生</div>
              <div class="why-grid">
                <div>
                  <span>触发</span>
                  <strong>{{ triggerLabel(call) }}</strong>
                </div>
                <div>
                  <span>Agent</span>
                  <strong>{{ agentLabel(call) }}</strong>
                </div>
                <div>
                  <span>任务复杂度</span>
                  <strong>{{ complexityLabel(context(call).task_complexity) }}</strong>
                </div>
                <div>
                  <span>AGENTS.md</span>
                  <strong :class="context(call).agents_md_injected ? 'positive' : 'muted'">
                    {{ agentsState(call) }}
                  </strong>
                </div>
              </div>
              <div v-if="context(call).current_objective" class="objective-box">
                <span>Current Objective</span>
                <p>{{ context(call).current_objective }}</p>
              </div>
              <details v-if="userDecisions(call).length" class="micro-fold">
                <summary>用户后续决定 · {{ userDecisions(call).length }} 条</summary>
                <ol><li v-for="(item, i) in userDecisions(call)" :key="i">{{ item }}</li></ol>
              </details>
            </section>

            <section class="trace-card">
              <div class="card-title-row">
                <div class="card-title">Prompt / Context 拼接</div>
                <span class="card-hint">实际层级顺序</span>
              </div>
              <div class="layer-flow">
                <div v-for="layer in promptLayers(call)" :key="layer.name" class="layer-pill" :class="{ off: !layer.enabled }">
                  <span>{{ layer.label }}</span>
                  <small>{{ layer.enabled ? layer.detail : '未启用' }}</small>
                </div>
              </div>
              <div class="hash-strip">
                <span>context <code>{{ shortHash(call.context_hash) }}</code></span>
                <span>prefix <code>{{ shortHash(call.prefix_hash) }}</code></span>
                <span v-if="context(call).allow_reconnaissance === false">Recon 关闭</span>
              </div>
            </section>

            <section class="trace-card decision-card">
              <div class="card-title-row">
                <div class="card-title">模型决定</div>
                <span class="card-hint">{{ call.finish_reason || call.status }}</span>
              </div>
              <div class="decision-box">{{ call.decision || '等待模型结果' }}</div>

              <div v-if="detailLoading[call.id]" class="detail-loading">正在按需读取本轮完整 Trace…</div>
              <template v-else-if="details[call.id]">
                <div v-if="details[call.id].tool_actions?.length" class="tool-actions">
                  <div v-for="tool in details[call.id].tool_actions" :key="tool.tool_call_id" class="debug-tool">
                    <div class="debug-tool-head">
                      <strong>{{ tool.name }}</strong>
                      <span :class="`tool-${tool.status || 'running'}`">{{ toolStatusLabel(tool.status) }}</span>
                    </div>
                    <details class="micro-fold">
                      <summary>参数{{ hasToolResult(tool) ? ' / 结果' : '' }}</summary>
                      <div class="tool-payload-grid">
                        <div><span>Arguments</span><pre>{{ pretty(tool.arguments) }}</pre></div>
                        <div v-if="hasToolResult(tool)"><span>Result</span><pre>{{ pretty(tool.result) }}</pre></div>
                      </div>
                    </details>
                  </div>
                </div>
                <details v-if="details[call.id].assistant_output" class="micro-fold text-fold">
                  <summary>用户可见输出</summary>
                  <pre>{{ details[call.id].assistant_output }}</pre>
                </details>
                <details v-if="details[call.id].reasoning" class="micro-fold text-fold">
                  <summary>Reasoning（底层）</summary>
                  <pre>{{ details[call.id].reasoning }}</pre>
                </details>
              </template>
              <button v-else class="load-detail" type="button" @click="loadDetail(call)">加载完整 Trace</button>
            </section>

            <section class="trace-card metrics-card">
              <div class="card-title">性能</div>
              <div class="metric-grid">
                <div><span>Prompt</span><strong>{{ compactNumber(call.prompt_tokens) }}</strong></div>
                <div><span>Completion</span><strong>{{ compactNumber(call.completion_tokens) }}</strong></div>
                <div><span>Cached</span><strong>{{ compactNumber(call.cached_tokens) }}</strong></div>
                <div><span>Reasoning</span><strong>{{ compactNumber(call.reasoning_tokens) }}</strong></div>
                <div><span>TTFT</span><strong>{{ formatDuration(call.ttft_ms) }}</strong></div>
                <div><span>总耗时</span><strong>{{ formatDuration(call.duration_ms) }}</strong></div>
              </div>
            </section>

            <template v-if="details[call.id]">
              <details class="deep-fold">
                <summary>实际发送的消息序列 <span>{{ requestMessages(details[call.id]).length }} messages</span></summary>
                <div class="deep-body message-list">
                  <details v-for="(message, messageIndex) in requestMessages(details[call.id])" :key="messageIndex" class="message-card">
                    <summary>
                      <span class="role" :class="message.role">{{ message.role || 'unknown' }}</span>
                      <span class="message-preview">{{ messagePreview(message) }}</span>
                      <span>{{ charCount(message.content) }} chars</span>
                    </summary>
                    <pre v-if="message.reasoning_content" class="reasoning-pre">reasoning_content:\n{{ message.reasoning_content }}</pre>
                    <pre v-if="message.content">{{ message.content }}</pre>
                    <pre v-if="message.tool_calls?.length">{{ pretty(message.tool_calls) }}</pre>
                    <pre v-if="message.tool_call_id">tool_call_id: {{ message.tool_call_id }}</pre>
                  </details>
                </div>
              </details>

              <details class="deep-fold">
                <summary>System Prompt 层 <span>{{ systemMessages(details[call.id]).length }} blocks</span></summary>
                <div class="deep-body">
                  <details v-for="(message, messageIndex) in systemMessages(details[call.id])" :key="messageIndex" class="message-card">
                    <summary><span class="role system">system {{ messageIndex + 1 }}</span><span>{{ charCount(message.content) }} chars</span></summary>
                    <pre>{{ message.content }}</pre>
                  </details>
                </div>
              </details>

              <details class="deep-fold">
                <summary>工具定义 <span>{{ requestTools(details[call.id]).length }} tools</span></summary>
                <div class="deep-body">
                  <details v-for="tool in requestTools(details[call.id])" :key="tool.function?.name || JSON.stringify(tool)" class="message-card">
                    <summary><span class="role tool">tool</span><strong>{{ tool.function?.name || 'unknown' }}</strong></summary>
                    <p v-if="tool.function?.description" class="tool-description">{{ tool.function.description }}</p>
                    <pre>{{ pretty(tool.function?.parameters || tool) }}</pre>
                  </details>
                </div>
              </details>

              <details class="deep-fold raw-fold">
                <summary>原始请求 / Context Debug <span>高级</span></summary>
                <div class="deep-body">
                  <div v-if="details[call.id].request_url" class="request-url">{{ details[call.id].request_url }}</div>
                  <div class="raw-label">Context Debug</div>
                  <pre>{{ pretty(call.context_debug) }}</pre>
                  <div class="raw-label">Request JSON</div>
                  <pre>{{ pretty(details[call.id].request) }}</pre>
                </div>
              </details>
            </template>
          </div>
        </details>
      </div>
    </n-drawer-content>
  </n-drawer>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { NButton, NDrawer, NDrawerContent } from 'naive-ui'
import { storeToRefs } from 'pinia'
import { useConnectionStore } from '../stores/connection'
import { useSessionsStore } from '../stores/sessions'

interface DebugCall {
  debug_seq: number
  id: string
  turn_id: string
  call_index: number
  provider: string
  model: string
  thinking: string
  status: string
  finish_reason?: string
  agent_run_id?: string
  context_epoch: number
  context_hash?: string
  prefix_hash?: string
  context_debug: Record<string, any>
  prompt_tokens: number
  completion_tokens: number
  cached_tokens: number
  reasoning_tokens: number
  duration_ms: number
  ttft_ms: number
  decision?: string
  error?: string
  created_at: string
  completed_at?: string
}
interface DebugToolAction {
  tool_call_id: string
  name: string
  arguments: any
  result?: any
  status?: string
}
interface DebugDetail extends DebugCall {
  system_prompt_snapshot: string
  request_url?: string
  request: Record<string, any>
  reasoning?: string
  assistant_output?: string
  tool_actions: DebugToolAction[]
}

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ 'update:show': [value: boolean] }>()
const connection = useConnectionStore()
const sessions = useSessionsStore()
const { currentSessionId } = storeToRefs(sessions)
const sessionId = computed(() => currentSessionId.value)
const calls = ref<DebugCall[]>([])
const details = reactive<Record<string, DebugDetail>>({})
const detailLoading = reactive<Record<string, boolean>>({})
const loading = ref(false)
const error = ref('')
const nextAfter = ref(0)
let timer: number | null = null
const drawerWidth = computed(() => Math.min(760, Math.max(390, window.innerWidth * 0.52)))

async function load(reset = false) {
  const id = sessionId.value
  if (!id || !connection.isConnected) { resetState(); return }
  if (reset) {
    calls.value = []
    nextAfter.value = 0
    for (const key of Object.keys(details)) delete details[key]
  }
  if (loading.value) return
  loading.value = true
  error.value = ''
  try {
    const response = await fetch(connection.api(`/api/sessions/${encodeURIComponent(id)}/debug?after=${reset ? 0 : nextAfter.value}`), { cache: 'no-store' })
    if (!response.ok) throw new Error(await readHTTPError(response, `读取调试摘要失败 (${response.status})`))
    const payload = await response.json() as { calls?: DebugCall[]; next_after?: number }
    mergeCalls(Array.isArray(payload.calls) ? payload.calls : [])
    nextAfter.value = Math.max(nextAfter.value, Number(payload.next_after || 0))
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason)
  } finally {
    loading.value = false
  }
}

function mergeCalls(incoming: DebugCall[]) {
  const byID = new Map(calls.value.map(call => [call.id, call]))
  for (const call of incoming) {
    const previous = byID.get(call.id)
    if (previous && previous.status !== call.status && details[call.id]) delete details[call.id]
    byID.set(call.id, normalizeCall(call))
  }
  calls.value = [...byID.values()].sort((a, b) => a.debug_seq - b.debug_seq)
}

function normalizeCall(call: DebugCall): DebugCall {
  return { ...call, context_debug: asObject(call.context_debug) }
}

async function loadDetail(call: DebugCall, force = false) {
  const id = sessionId.value
  if (!id || detailLoading[call.id] || (details[call.id] && !force)) return
  detailLoading[call.id] = true
  try {
    const response = await fetch(connection.api(`/api/sessions/${encodeURIComponent(id)}/debug/calls/${encodeURIComponent(call.id)}`), { cache: 'no-store' })
    if (!response.ok) throw new Error(await readHTTPError(response, `读取完整 Trace 失败 (${response.status})`))
    const detail = await response.json() as DebugDetail
    details[call.id] = { ...detail, context_debug: asObject(detail.context_debug), request: asObject(detail.request) }
  } catch (reason) {
    window.$message?.error(reason instanceof Error ? reason.message : String(reason))
  } finally {
    detailLoading[call.id] = false
  }
}

function onToggle(event: Event, call: DebugCall) {
  const node = event.currentTarget as HTMLDetailsElement
  if (node.open) void loadDetail(call)
}
function reload() { void load(true) }
function resetState() {
  calls.value = []
  nextAfter.value = 0
  error.value = ''
  for (const key of Object.keys(details)) delete details[key]
}
function startPolling() {
  stopPolling()
  if (!props.show || !sessionId.value) return
  timer = window.setInterval(() => { void load(false) }, 1500)
}
function stopPolling() { if (timer !== null) { window.clearInterval(timer); timer = null } }

watch(() => [props.show, sessionId.value] as const, ([open], previous) => {
  const sessionChanged = Boolean(previous && previous[1] !== sessionId.value)
  // 即使抽屉关闭，也在切换 Session 时清空旧 Trace，避免下次打开沿用上一会话的 after 游标与调用卡片。
  if (sessionChanged) resetState()
  if (!open) { stopPolling(); return }
  void load(sessionChanged || !calls.value.length)
  startPolling()
}, { immediate: true })
onBeforeUnmount(stopPolling)

function context(call: DebugCall): Record<string, any> { return asObject(call.context_debug) }
function agentLabel(call: DebugCall): string {
  const agent = String(context(call).agent || '').toLowerCase()
  if (agent === 'plan') return 'Plan'
  if (agent === 'build') return 'Build'
  if (agent === 'explore' || agent === 'explorer') return 'Explorer'
  if (agent === 'explain') return 'Explain'
  return agent || 'Agent'
}
function triggerLabel(call: DebugCall): string {
  const trigger = String(context(call).trigger || '').trim()
  const labels: Record<string, string> = {
    'turn.start': 'Turn 开始', 'tool.result': '工具返回', 'approval.resolved': '审批通过',
    'steering': '用户 Steering', 'build.start': '开始执行', 'child.start': '子 Agent 调用',
  }
  return labels[trigger] || trigger || '继续推理'
}
function statusLabel(status: string): string {
  return ({ running: '运行中', completed: '完成', failed: '失败', cancelled: '取消' } as Record<string, string>)[status] || status
}
function complexityLabel(value: unknown): string {
  return ({ trivial: '轻量', standard: '标准', complex: '复杂' } as Record<string, string>)[String(value || '')] || String(value || '标准')
}
function agentsState(call: DebugCall): string {
  const ctx = context(call)
  if (ctx.agents_md_injected) return '已注入'
  if (ctx.agents_md_enabled) return '已启用 · 无可注入文件'
  return '已关闭'
}
function userDecisions(call: DebugCall): string[] {
  const value = context(call).user_decisions
  return Array.isArray(value) ? value.filter(item => typeof item === 'string') : []
}
function promptLayers(call: DebugCall): Array<{ name: string; label: string; enabled: boolean; detail: string }> {
  const raw = context(call).prompt_layers
  const labels: Record<string, string> = {
    kernel: 'Kernel', profile: 'Agent Profile', project_instructions: 'Project Instructions',
    state: 'Current State', objective: 'Current Objective', decisions: 'User Decisions', artifacts: 'Artifacts', history: 'History', tools: 'Tools',
  }
  if (Array.isArray(raw)) return raw.map((item: any, index) => {
    if (typeof item === 'string') return { name: item, label: labels[item] || item, enabled: true, detail: `${index + 1}` }
    const object = asObject(item)
    const name = String(object.name || object.id || `layer_${index + 1}`)
    return { name, label: labels[name] || String(object.label || name), enabled: object.enabled !== false, detail: String(object.summary || object.detail || (object.chars ? `${object.chars} chars` : `${index + 1}`)) }
  })
  if (raw && typeof raw === 'object') return Object.entries(raw).map(([name, value], index) => ({
    name, label: labels[name] || name, enabled: value !== false && value !== 0 && value !== '', detail: typeof value === 'number' ? `${value} chars` : `${index + 1}`,
  }))
  return [
    { name: 'kernel', label: 'Kernel', enabled: true, detail: 'system' },
    { name: 'profile', label: 'Agent Profile', enabled: true, detail: agentLabel(call) },
    { name: 'project_instructions', label: 'Project Instructions', enabled: Boolean(context(call).agents_md_injected), detail: 'AGENTS.md' },
    { name: 'state', label: 'Current State', enabled: true, detail: `epoch ${call.context_epoch}` },
  ]
}
function requestMessages(detail: DebugDetail): any[] { return Array.isArray(detail.request?.messages) ? detail.request.messages : [] }
function requestTools(detail: DebugDetail): any[] { return Array.isArray(detail.request?.tools) ? detail.request.tools : [] }
function systemMessages(detail: DebugDetail): any[] { return requestMessages(detail).filter(message => message?.role === 'system') }
function messagePreview(message: any): string {
  if (Array.isArray(message?.tool_calls) && message.tool_calls.length) return `tool_calls · ${message.tool_calls.map((item: any) => item?.function?.name || 'tool').join(', ')}`
  const text = typeof message?.content === 'string' ? message.content.replace(/\s+/g, ' ').trim() : ''
  return text ? shorten(text, 90) : (message?.tool_call_id ? `tool_call_id ${message.tool_call_id}` : 'empty')
}
function hasToolResult(tool: DebugToolAction): boolean { return tool.result !== undefined && tool.result !== null }
function toolStatusLabel(status?: string): string {
  return ({ completed: '完成', failed: '失败', rejected: '拒绝', cancelled: '取消', running: '运行中' } as Record<string, string>)[status || ''] || status || '等待结果'
}
function asObject(value: unknown): Record<string, any> { return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, any> : {} }
function pretty(value: unknown): string { try { return JSON.stringify(value ?? {}, null, 2) } catch { return String(value ?? '') } }
function charCount(value: unknown): number { return typeof value === 'string' ? value.length : 0 }
function shorten(value: string, max: number): string { return value.length <= max ? value : `${value.slice(0, max - 1)}…` }
function shortHash(value?: string): string { return value ? (value.length > 14 ? `${value.slice(0, 7)}…${value.slice(-5)}` : value) : '—' }
function compactNumber(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '—'
  if (value < 1000) return String(value)
  if (value < 1_000_000) return `${(value / 1000).toFixed(value < 10_000 ? 1 : 0)}k`
  return `${(value / 1_000_000).toFixed(1)}m`
}
function formatDuration(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '—'
  if (value < 1000) return `${Math.round(value)}ms`
  return value < 10_000 ? `${(value / 1000).toFixed(1)}s` : `${Math.round(value / 1000)}s`
}
async function readHTTPError(response: Response, fallback: string): Promise<string> {
  try { const payload = await response.json() as { error?: string }; return payload.error || fallback } catch { return fallback }
}
</script>

<style scoped>
.debug-header { display: flex; align-items: center; justify-content: space-between; gap: 16px; min-width: 0; }
.debug-title { color: var(--text); font-size: 15px; font-weight: 680; }
.debug-subtitle { margin-top: 2px; color: var(--text-3); font-size: 11px; font-weight: 400; }
.debug-actions { display: flex; align-items: center; gap: 7px; }
.call-count { color: var(--text-3); font-size: 10px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.debug-empty { padding: 34px 12px; text-align: center; color: var(--text-3); font-size: 12px; }
.debug-error, .call-error { padding: 9px 11px; border: 1px solid #f2c7cc; border-radius: 9px; background: #fff4f5; color: var(--error); font-size: 12px; line-height: 1.5; }
.compact-error { margin-bottom: 10px; }
.trace-list { padding-bottom: 20px; }
.trace-call { border: 1px solid var(--border); border-radius: 12px; background: var(--bg); margin-bottom: 10px; overflow: hidden; }
.trace-call[open] { border-color: #d7ccd8; box-shadow: 0 7px 24px rgba(45, 31, 44, .045); }
.trace-summary { list-style: none; display: grid; grid-template-columns: 50px minmax(0, 1fr) auto; gap: 10px; align-items: center; padding: 11px 12px; cursor: pointer; user-select: none; }
.trace-summary::-webkit-details-marker { display: none; }
.trace-summary:hover { background: var(--bg-soft); }
.trace-rail { display: flex; align-items: center; gap: 6px; color: var(--text-3); }
.trace-dot { width: 7px; height: 7px; border-radius: 50%; background: #aaa; box-shadow: 0 0 0 3px rgba(120, 110, 120, .08); }
.dot-running { background: var(--accent); animation: pulse 1.2s ease-in-out infinite; }
.dot-completed { background: #2f9368; }
.dot-failed { background: var(--error); }
@keyframes pulse { 0%, 100% { opacity: .45; } 50% { opacity: 1; } }
.trace-index { font: 10px/1 ui-monospace, SFMono-Regular, Menlo, monospace; }
.summary-main { min-width: 0; }
.summary-top { display: flex; align-items: center; gap: 7px; min-width: 0; color: var(--text); font-size: 12px; }
.summary-top strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.agent-chip { padding: 2px 6px; border-radius: 6px; background: var(--accent-soft); color: var(--accent-text); font-size: 10px; font-weight: 650; }
.decision-inline { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-3); font-size: 11px; }
.summary-meta { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 4px; color: var(--text-3); font-size: 10px; }
.summary-tail { display: flex; align-items: center; gap: 7px; color: var(--text-3); }
.status-chip { padding: 2px 6px; border-radius: 999px; background: var(--bg-soft); font-size: 10px; }
.status-completed { color: #2c805e; background: #eef8f3; }
.status-running { color: var(--accent-text); background: var(--accent-soft); }
.status-failed { color: var(--error); background: #fff1f2; }
.duration { font-size: 10px; min-width: 34px; text-align: right; }
.chevron { font-size: 14px; transition: transform .16s ease; }
.trace-call[open] .chevron { transform: rotate(180deg); }
.trace-body { border-top: 1px solid var(--border); padding: 11px; background: #fcfbfc; }
.trace-card { border: 1px solid var(--border); border-radius: 10px; background: var(--bg); padding: 11px; margin-bottom: 9px; }
.card-title, .card-title-row { color: var(--text); font-size: 11px; font-weight: 680; }
.card-title-row { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.card-hint { color: var(--text-3); font-size: 10px; font-weight: 400; }
.why-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 7px; margin-top: 9px; }
.why-grid > div { min-width: 0; padding: 7px 8px; border-radius: 8px; background: var(--bg-soft); }
.why-grid span, .metric-grid span { display: block; color: var(--text-3); font-size: 9px; margin-bottom: 3px; }
.why-grid strong { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-2); font-size: 11px; }
.positive { color: #2c805e !important; }.muted { color: var(--text-3) !important; }
.objective-box { margin-top: 8px; padding: 8px 9px; border-left: 2px solid var(--accent); border-radius: 0 8px 8px 0; background: var(--accent-soft); }
.objective-box span { color: var(--accent-text); font: 9px/1 ui-monospace, SFMono-Regular, Menlo, monospace; text-transform: uppercase; }
.objective-box p { margin: 5px 0 0; color: var(--text-2); font-size: 11px; line-height: 1.55; white-space: pre-wrap; }
.layer-flow { display: flex; flex-wrap: wrap; gap: 5px; margin-top: 9px; }
.layer-pill { min-width: 86px; padding: 6px 8px; border: 1px solid #ded4df; border-radius: 8px; background: #fdfbfe; }
.layer-pill span { display: block; color: var(--text-2); font-size: 10px; font-weight: 620; }
.layer-pill small { display: block; margin-top: 2px; color: var(--text-3); font-size: 9px; }
.layer-pill.off { opacity: .46; border-style: dashed; }
.hash-strip { display: flex; flex-wrap: wrap; gap: 7px 12px; margin-top: 9px; color: var(--text-3); font-size: 9px; }
.hash-strip code { color: var(--text-2); }
.decision-box { margin-top: 8px; padding: 8px 9px; border-radius: 8px; background: var(--bg-soft); color: var(--text-2); font-size: 11px; line-height: 1.45; }
.detail-loading { margin-top: 8px; color: var(--text-3); font-size: 10px; }
.load-detail { margin-top: 8px; border: 1px solid var(--border); border-radius: 7px; background: var(--bg); color: var(--text-2); padding: 5px 8px; font: inherit; font-size: 10px; cursor: pointer; }
.load-detail:hover { border-color: var(--accent); color: var(--accent-text); }
.tool-actions { margin-top: 9px; display: grid; gap: 6px; }
.debug-tool { border: 1px solid var(--border); border-radius: 8px; background: #fdfcfd; overflow: hidden; }
.debug-tool-head { display: flex; justify-content: space-between; gap: 8px; padding: 7px 9px; color: var(--text-2); font: 10px/1.3 ui-monospace, SFMono-Regular, Menlo, monospace; }
.tool-completed { color: #2c805e; }.tool-failed, .tool-rejected { color: var(--error); }.tool-running { color: var(--accent-text); }
.micro-fold { margin-top: 7px; border-top: 1px solid var(--border); }
.micro-fold > summary { list-style: none; cursor: pointer; padding: 7px 0 2px; color: var(--text-3); font-size: 10px; }
.micro-fold > summary::-webkit-details-marker { display: none; }
.micro-fold ol { margin: 6px 0 0 18px; padding: 0; color: var(--text-2); font-size: 10px; line-height: 1.5; }
.tool-payload-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px; padding: 5px 8px 8px; }
.tool-payload-grid span, .raw-label { display: block; margin: 3px 0 5px; color: var(--text-3); font-size: 9px; text-transform: uppercase; letter-spacing: .04em; }
pre { margin: 0; padding: 8px; border: 1px solid var(--border); border-radius: 7px; background: #faf8fa; max-height: 340px; overflow: auto; white-space: pre-wrap; word-break: break-word; color: #645b64; font: 10px/1.52 ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; }
.text-fold pre { margin-top: 5px; }
.metric-grid { display: grid; grid-template-columns: repeat(6, minmax(0, 1fr)); gap: 6px; margin-top: 8px; }
.metric-grid > div { min-width: 0; padding: 6px 7px; border-radius: 7px; background: var(--bg-soft); }
.metric-grid strong { color: var(--text-2); font-size: 10px; }
.deep-fold { border: 1px solid var(--border); border-radius: 9px; background: var(--bg); margin-top: 8px; overflow: hidden; }
.deep-fold > summary { list-style: none; display: flex; justify-content: space-between; gap: 8px; padding: 9px 10px; cursor: pointer; color: var(--text-2); font-size: 10px; font-weight: 620; }
.deep-fold > summary::-webkit-details-marker { display: none; }
.deep-fold > summary span { color: var(--text-3); font-weight: 400; }
.deep-body { border-top: 1px solid var(--border); padding: 8px; }
.message-list, .deep-body { display: grid; gap: 6px; }
.message-card { border: 1px solid var(--border); border-radius: 7px; background: #fdfcfd; overflow: hidden; }
.message-card > summary { list-style: none; display: grid; grid-template-columns: auto minmax(0, 1fr) auto; gap: 7px; align-items: center; padding: 7px 8px; cursor: pointer; color: var(--text-3); font-size: 9px; }
.message-card > summary::-webkit-details-marker { display: none; }
.message-card > pre { border: 0; border-top: 1px solid var(--border); border-radius: 0; }
.role { padding: 2px 5px; border-radius: 5px; background: var(--bg-soft); color: var(--text-2); font: 9px/1.25 ui-monospace, SFMono-Regular, Menlo, monospace; }
.role.system { background: var(--accent-soft); color: var(--accent-text); }.role.tool { background: #eef8f3; color: #2c805e; }
.message-preview { min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; color: var(--text-2); }
.reasoning-pre { color: #7a6475; }
.tool-description { margin: 0; padding: 8px; color: var(--text-3); font-size: 10px; line-height: 1.45; }
.request-url { margin-bottom: 8px; padding: 6px 8px; border-radius: 7px; background: var(--bg-soft); color: var(--text-3); font: 9px/1.4 ui-monospace, SFMono-Regular, Menlo, monospace; word-break: break-all; }
.raw-fold { opacity: .86; }
@media (max-width: 680px) {
  .trace-summary { grid-template-columns: 42px minmax(0, 1fr); }
  .summary-tail { grid-column: 2; justify-content: flex-start; }
  .why-grid, .metric-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .tool-payload-grid { grid-template-columns: 1fr; }
}
</style>
