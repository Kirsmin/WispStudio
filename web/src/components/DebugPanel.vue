<template>
  <n-drawer :show="show" :width="drawerWidth" placement="right" @update:show="emit('update:show', $event)">
    <n-drawer-content :native-scrollbar="false" closable>
      <template #header>
        <div class="header">
          <div><strong>调试 · Trace Explorer</strong><small>模型调用 → 提示词层 → 工具 → 结果；仅在展开时读取完整请求</small></div>
          <n-button quaternary size="tiny" :loading="loading" @click="refresh(true)">刷新</n-button>
        </div>
      </template>
      <div v-if="!sessionId" class="empty">选择会话后可查看执行轨迹。</div>
      <div v-else-if="error" class="error">{{ error }}</div>
      <div v-else-if="!calls.length" class="empty">{{ loading ? '读取中…' : '暂无模型调用' }}</div>
      <div v-if="calls.length" class="trace-list">
        <div class="stats">{{ calls.length }} 次模型调用 <span v-if="moreOlder">· 可继续向前加载</span> · 摘要增量同步</div>
        <n-button v-if="moreOlder" block size="small" secondary :loading="loadingOlder" @click="loadOlder">加载更早的 40 次调用</n-button>
        <details v-for="(call, index) in calls" :key="call.id" class="trace-card" @toggle="onToggle($event, call)">
          <summary class="trace-head">
            <span class="ordinal">#{{ call.call_index }}</span>
            <span class="agent">{{ agentName(call.agent) }}</span>
            <span class="head-copy"><b>{{ call.trigger || (call.call_index === 1 ? '初次规划' : '继续执行') }}</b><small>{{ call.model }} · {{ formatTime(call.created_at) }}</small></span>
            <span class="status" :class="call.status">{{ statusName(call.status) }}</span>
            <span class="duration">{{ formatDuration(call.duration_ms) }}</span>
            <span class="chevron">⌄</span>
          </summary>
          <div class="trace-body">
            <div class="metrics"><span>提示词 {{ call.prompt_tokens.toLocaleString() }} tokens</span><span>输出 {{ call.completion_tokens.toLocaleString() }}</span><span>TTFT {{ formatDuration(call.ttft_ms) }}</span><span>Epoch {{ call.context_epoch }}</span></div>
            <div class="step"><label>1 · 触发原因</label><p>{{ call.trigger || '继续执行当前任务' }} · {{ index > 0 && calls[index - 1].prefix_hash !== call.prefix_hash ? '提示词前缀已变化' : '提示词前缀未变化' }}</p></div>
            <div class="step"><label>2 · 本次模型动作</label><p>{{ call.tools.length ? call.tools.map(toolName).join(' · ') : call.status === 'running' ? '模型正在处理…' : '返回文字 / 阶段结论' }}</p></div>
            <div class="step"><label>3 · 下一状态</label><p>{{ call.error || (call.status === 'running' ? '等待上游响应' : call.tools.length ? '按顺序处理工具结果，必要时向用户申请授权' : call.agent === 'plan' ? '等待方案确认或用户反馈' : '进入结果与验证阶段') }}</p></div>
            <div v-if="!details[call.id]" class="hint">{{ detailLoading[call.id] ? '正在按需加载完整上下文…' : '展开后加载详细追踪' }}</div>
            <template v-if="details[call.id]">
              <div class="divider" />
              <div class="step"><label>提示词层 · 实际发送内容</label><p>{{ promptChange(call) }}</p></div>
              <details v-for="(layer, layerIndex) in promptLayers(details[call.id].call)" :key="`${call.id}-${layerIndex}`" class="fold">
                <summary><span>{{ layer.title }}</span><small>{{ layer.content.length.toLocaleString() }} 字符</small></summary>
                <pre>{{ layer.content }}</pre>
              </details>
              <details class="fold"><summary>消息序列 · {{ messages(details[call.id].call).length }} 条 <small>默认折叠</small></summary>
                <details v-for="(message, n) in messages(details[call.id].call)" :key="n" class="message"><summary><b>{{ message.role }}</b> · {{ preview(message) }}</summary><pre>{{ pretty(message) }}</pre></details>
              </details>
              <details class="fold"><summary>工具行为 · {{ details[call.id].trace.filter(r => r.kind.startsWith('tool.')).length }} 条记录 <small>请求 / 结果</small></summary>
                <div v-for="record in details[call.id].trace.filter(r => r.kind.startsWith('tool.'))" :key="record.id" class="event">
                  <b>{{ eventName(record.kind) }} · {{ toolName(String(record.data?.name || '工具')) }}</b><pre>{{ pretty(record.data) }}</pre>
                </div>
              </details>
              <details class="fold"><summary>上下文编译 / Hash <small>Epoch {{ call.context_epoch }}</small></summary>
                <div class="hash">Prefix {{ call.prefix_hash || '—' }}<br />Context {{ call.context_hash || '—' }}</div>
                <pre>{{ pretty(details[call.id].call.context_debug) }}</pre>
              </details>
              <details class="fold"><summary>工具定义 <small>按需展开</small></summary><pre>{{ pretty(details[call.id].call.request?.tools || []) }}</pre></details>
              <details class="fold"><summary>原始请求 JSON <small>完整留存，最后一层</small></summary><pre>{{ pretty(details[call.id].call.request) }}</pre></details>
              <details class="fold"><summary>原始事件 · {{ details[call.id].trace.length }} 条</summary><pre>{{ pretty(details[call.id].trace) }}</pre></details>
            </template>
          </div>
        </details>
        <div class="stats">原始 Prompt 和完整请求不会在列表轮询时下载。</div>
      </div>
    </n-drawer-content>
  </n-drawer>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { NButton, NDrawer, NDrawerContent } from 'naive-ui'
import { useConnectionStore } from '../stores/connection'
import { useSessionsStore } from '../stores/sessions'
import { useChatStore, type TimelineRecord } from '../stores/chat'

type CallSummary = {
  cursor: number; id: string; turn_id: string; call_index: number; model: string; status: string
  agent: string; trigger: string; context_epoch: number; context_hash: string; prefix_hash: string
  prompt_tokens: number; completion_tokens: number; duration_ms: number; ttft_ms: number
  tools: string[]; error?: string; created_at: string
}
type DebugDetail = { call: { system_prompt_snapshot: string; context_debug: Record<string, unknown>; request: { messages?: Array<Record<string, any>>; tools?: unknown[] }; [key: string]: any }; trace: TimelineRecord[] }
type StatusUpdate = { id: string; status: string; prompt_tokens: number; completion_tokens: number; duration_ms: number; ttft_ms: number; error?: string }
type Page = { calls: CallSummary[]; next_cursor: number; has_more: boolean; updates: StatusUpdate[] }
const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ 'update:show': [value: boolean] }>()
const connection = useConnectionStore()
const sessions = useSessionsStore()
const chat = useChatStore()
const sessionId = computed(() => sessions.currentSessionId)
const calls = ref<CallSummary[]>([])
const details = ref<Record<string, DebugDetail>>({})
const detailLoading = ref<Record<string, boolean>>({})
const loading = ref(false)
const loadingOlder = ref(false)
const moreOlder = ref(false)
const error = ref('')
const drawerWidth = computed(() => 'min(740px, 94vw)')
let timer: number | undefined
let generation = 0
let fetchingFor: string | null = null

async function requestPage(id: string, query: string): Promise<Page> {
  const response = await fetch(connection.api(`/api/sessions/${encodeURIComponent(id)}/debug${query}`), { cache: 'no-store' })
  if (!response.ok) throw new Error(`读取调试记录失败：HTTP ${response.status}`)
  return response.json() as Promise<Page>
}
function mergePage(page: Page, first: boolean): void {
  const byId = new Map(calls.value.map(item => [item.id, item]))
  for (const call of page.calls || []) byId.set(call.id, call)
  for (const update of page.updates || []) {
    const call = byId.get(update.id)
    if (call) Object.assign(call, update)
  }
  calls.value = [...byId.values()].sort((a, b) => a.cursor - b.cursor)
  if (first) moreOlder.value = Boolean(page.has_more)
}
async function refresh(first = false) {
  if (!sessionId.value || !props.show || fetchingFor === `${sessionId.value}:${generation}`) return
  const id = sessionId.value; const seq = generation
  const key = `${id}:${seq}`
  fetchingFor = key
  if (first && calls.value.length === 0) loading.value = true
  try {
    const after = first ? '' : calls.value.length ? `?after=${calls.value[calls.value.length - 1].cursor}` : ''
    const page = await requestPage(id, after)
    if (generation === seq && props.show && sessionId.value === id) {
      mergePage(page, first || !calls.value.length)
      error.value = ''
    }
  } catch (reason) { if (generation === seq) error.value = reason instanceof Error ? reason.message : String(reason) }
  finally { if (fetchingFor === key) fetchingFor = null; if (generation === seq) { loading.value = false; planPoll() } }
}
async function loadOlder() {
  if (!calls.value.length || !moreOlder.value || loadingOlder.value) return
  const id = sessionId.value; const seq = generation
  loadingOlder.value = true
  try {
    const page = await requestPage(id, `?before=${calls.value[0].cursor}`)
    if (generation === seq) { mergePage(page, false); moreOlder.value = page.has_more }
  } catch (reason) { if (generation === seq) error.value = reason instanceof Error ? reason.message : String(reason) }
  finally { if (generation === seq) loadingOlder.value = false }
}
async function onToggle(event: Event, summary: CallSummary) {
  const target = event.target as HTMLDetailsElement
  if (!target.open || details.value[summary.id] || detailLoading.value[summary.id]) return
  const id = sessionId.value; const seq = generation
  detailLoading.value[summary.id] = true
  try {
    const response = await fetch(connection.api(`/api/sessions/${encodeURIComponent(id)}/debug/calls/${encodeURIComponent(summary.id)}`), { cache: 'no-store' })
    if (!response.ok) throw new Error(`读取模型详情失败：HTTP ${response.status}`)
    const detail = await response.json() as DebugDetail
    if (generation === seq) details.value[summary.id] = detail
  } catch (reason) { if (generation === seq) error.value = reason instanceof Error ? reason.message : String(reason) }
  finally { if (generation === seq) detailLoading.value[summary.id] = false }
}
function planPoll() {
  if (timer !== undefined) window.clearTimeout(timer)
  timer = undefined
  if (!props.show || !sessionId.value || !chat.executionActive) return
  timer = window.setTimeout(() => void refresh(), 3000)
}
watch(() => [props.show, sessionId.value] as const, () => {
  generation++
  fetchingFor = null
  loading.value = false
  calls.value = []; details.value = {}; detailLoading.value = {}; moreOlder.value = false; error.value = ''
  planPoll()
  if (props.show) void refresh(true)
}, { immediate: true })
watch(() => chat.executionActive, (active, previous) => {
  if (!active && previous && props.show) void refresh()
  planPoll()
})
onBeforeUnmount(() => { generation++; if (timer !== undefined) window.clearTimeout(timer) })

function agentName(value: string) { return ({ plan: '规划', build: '执行', explore: '探索', explain: '解释' } as Record<string, string>)[value] || value || 'Agent' }
function statusName(value: string) { return ({ running: '进行中', completed: '完成', failed: '失败', cancelled: '已取消' } as Record<string, string>)[value] || value }
function toolName(value: string) { return ({ write_file: '写文件', run_command: '运行命令', verify_file: '核验文件', list_files: '列出文件', read_file: '读取文件', search_text: '搜索内容', new_plan: '保存计划', edit_plan: '更新计划', spawn_explorer: '启动探索' } as Record<string,string>)[value] || value }
function eventName(value: string) { return ({ 'tool.requested': '请求', 'tool.started': '开始', 'tool.completed': '成功', 'tool.failed': '失败', 'tool.rejected': '拒绝', 'tool.cancelled': '取消' } as Record<string,string>)[value] || value }
function formatDuration(ms: number) { if (!ms) return '—'; return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s` }
function formatTime(value: string) { return value ? new Date(value).toLocaleTimeString() : '—' }
function pretty(value: unknown) { try { return JSON.stringify(value ?? {}, null, 2) } catch { return String(value ?? '') } }
function preview(message: Record<string, any>) { return String(message.content || message.tool_calls?.map((tool: any) => tool.function?.name).join(', ') || message.tool_call_id || '').replace(/\s+/g, ' ').slice(0, 86) }
function messages(detail: DebugDetail['call']) { return Array.isArray(detail.request?.messages) ? detail.request.messages : [] }
function promptChange(call: CallSummary) { return call.context_epoch > 1 ? `第 ${call.context_epoch} 个 Context Epoch；下方按层显示实际请求中的拼接内容。` : '此调用使用的实际系统指令和会话状态在下方分层展示。' }
function promptLayers(call: DebugDetail['call']): Array<{ title: string; content: string }> {
  const system = messages(call).filter(message => message.role === 'system').map(message => String(message.content || ''))
  if (!system.length && call.system_prompt_snapshot) system.push(call.system_prompt_snapshot)
  const specs: Array<[string,string]> = [
    ['RuntimeCore', '核心行为 / Prompt'], ['RuntimeSafety', '权限与执行策略'], ['ProjectInstructions', 'AGENTS.md · 项目指令'],
    ['AgentProfile', 'Agent 职责'], ['CurrentObjective', '当前有效目标'], ['UserOverrides', '最新用户修改'],
    ['Execution', '状态机 / 复杂度'], ['ActivePlan', '当前 Plan 引用'], ['Checkpoint', 'Checkpoint'],
    ['Artifacts', 'Artifact / 当前成果'], ['PreviousTurns', '先前会话'],
  ]
  const result: Array<{ title: string; content: string }> = []
  let remaining = system.join('\n')
  for (const [tag, title] of specs) {
    // 同名 tag 可出现多次，保留所有层；原始消息仍可从“消息序列”中完整查看。
    const regex = new RegExp(`<${tag}(?:\\s[^>]*)?(?:\\/>|>[\\s\\S]*?<\\/${tag}>)`, 'g')
    const matches = [...remaining.matchAll(regex)].map(match => match[0])
    if (matches.length) { result.push({ title, content: matches.join('\n\n') }); remaining = remaining.replace(regex, '') }
  }
  if (remaining.trim()) result.push({ title: '其他系统上下文', content: remaining.trim() })
  return result
}
</script>

<style scoped>
.header { width: 100%; display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.header strong { display: block; font-size: 15px; font-weight: 650; }
.header small { display: block; margin-top: 3px; color: var(--text-3); font-size: 10px; font-weight: 400; }
.empty,.stats { padding: 18px 4px; text-align: center; color: var(--text-3); font-size: 12px; }
.error { background: #fff3f4; padding: 12px; border-radius: 9px; color: var(--error); font-size: 12px; }
.trace-list { display: flex; flex-direction: column; gap: 9px; }
.trace-card { border: 1px solid var(--border); border-radius: 12px; background: var(--bg); overflow: hidden; }
.trace-card[open] { border-color: var(--border-strong); }
.trace-head { list-style: none; display: flex; align-items: center; gap: 8px; padding: 12px; cursor: pointer; }
.trace-head::-webkit-details-marker,.fold > summary::-webkit-details-marker,.message > summary::-webkit-details-marker { display: none; }
.trace-head:hover { background: var(--bg-soft); }
.ordinal { color: var(--text-3); font: 11px ui-monospace,monospace; }
.agent { padding: 4px 7px; border-radius: 7px; background: var(--accent-soft); color: var(--accent-text); font-size: 11px; white-space: nowrap; }
.head-copy { display: flex; flex: 1; flex-direction: column; min-width: 0; gap: 3px; }
.head-copy b { font-size: 12px; font-weight: 650; }
.head-copy small { color: var(--text-3); font-size: 10px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.status { padding: 3px 7px; border-radius: 7px; background: var(--bg-soft); color: var(--text-3); font-size: 10px; }
.status.completed { color: #287a4c; background: #eaf8f0; }.status.running { color: var(--accent-text); background: var(--accent-soft); }.status.failed { color: var(--error); background: #fff2f2; }
.duration { color: var(--text-3); font-size: 10px; font-variant-numeric: tabular-nums; }.chevron { color: var(--text-3); }.trace-card[open] .chevron { transform: rotate(180deg); }
.trace-body { padding: 0 12px 12px; }.metrics { display: flex; gap: 7px 12px; flex-wrap: wrap; border-bottom: 1px solid var(--border); padding: 8px 0; color: var(--text-3); font-size: 10px; }
.step { padding: 10px 0 2px; }.step label { color: var(--text-3); font-size: 10px; font-weight: 600; }.step p { margin: 4px 0; font-size: 12px; color: var(--text-2); overflow-wrap: anywhere; }
.hint { padding: 12px 0; font-size: 11px; color: var(--text-3); }.divider { border-top: 1px solid var(--border); margin-top: 10px; }
.fold { margin-top: 8px; border: 1px solid var(--border); border-radius: 8px; overflow: hidden; }.fold > summary { padding: 10px; list-style: none; display: flex; justify-content: space-between; cursor: pointer; font-size: 11px; font-weight: 600; }
.fold > summary:before { content: '›'; margin-right: 8px; color: var(--text-3); }.fold[open] > summary:before { content: '⌄'; }.fold > summary span { flex: 1; }.fold small { color: var(--text-3); font-weight: 400; font-size: 10px; }
pre { margin: 0; padding: 10px; max-height: 400px; overflow: auto; white-space: pre-wrap; word-break: break-word; background: var(--bg-soft); color: var(--text-2); font: 10.5px/1.6 ui-monospace,monospace; }
.message { padding: 5px 10px; border-top: 1px solid var(--border); }.message > summary { list-style: none; cursor: pointer; color: var(--text-2); font-size: 11px; overflow-wrap: anywhere; }.message pre { margin-top: 8px; }
.event { padding: 10px; border-top: 1px solid var(--border); font-size: 11px; }.event pre { margin-top: 6px; }.hash { padding: 9px; word-break: break-all; font: 10px/1.7 ui-monospace,monospace; }
@media(max-width:540px){.duration { display: none; }.trace-head { gap: 5px; padding: 10px; }.trace-body { padding: 0 9px 9px; }}
</style>
