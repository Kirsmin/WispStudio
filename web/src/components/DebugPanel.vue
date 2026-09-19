<template>
  <n-drawer :show="show" :width="drawerWidth" placement="right" @update:show="emit('update:show', $event)">
    <n-drawer-content :native-scrollbar="false" closable>
      <template #header>
        <div class="debug-header">
          <div>
            <div class="debug-title">调试</div>
            <div class="debug-subtitle">实际上下文拼接与上游请求快照</div>
          </div>
          <n-button size="tiny" quaternary :loading="loading" @click="load">刷新</n-button>
        </div>
      </template>

      <div v-if="!sessionId" class="debug-empty">选择一个 Session 后可查看底层行为。</div>
      <div v-else-if="loading && !calls.length" class="debug-empty">正在读取调试快照…</div>
      <div v-else-if="error" class="debug-error">{{ error }}</div>
      <div v-else-if="!calls.length" class="debug-empty">这个 Session 还没有模型调用。</div>

      <div v-else class="debug-list">
        <details v-for="(call, index) in calls" :key="call.id" class="debug-call" :open="index === 0">
          <summary class="call-summary">
            <span class="call-index">#{{ call.call_index }}</span>
            <span class="call-agent">{{ contextAgent(call) }}</span>
            <span class="call-model">{{ call.model }}</span>
            <span class="status-chip" :class="`status-${call.status}`">{{ call.status }}</span>
            <span class="call-time">{{ formatDuration(call.duration_ms) }}</span>
          </summary>

          <div class="call-body">
            <div class="metric-row">
              <span>epoch {{ call.context_epoch }}</span>
              <span>{{ requestMessages(call).length }} messages</span>
              <span>{{ requestTools(call).length }} tools</span>
              <span v-if="call.prompt_tokens">{{ call.prompt_tokens }} prompt tokens</span>
              <span v-if="call.ttft_ms">TTFT {{ formatDuration(call.ttft_ms) }}</span>
            </div>

            <div v-if="call.error" class="call-error">{{ call.error }}</div>

            <details class="debug-section" open>
              <summary>
                <span>提示词布局</span>
                <span class="section-hint">{{ systemMessages(call).length }} 个 system block</span>
              </summary>
              <div class="section-body">
                <div v-if="promptLayout(call)" class="layout-grid">
                  <div v-for="(value, key) in promptLayout(call)" :key="String(key)" class="layout-item">
                    <span>{{ key }}</span><strong>{{ compactValue(value) }}</strong>
                  </div>
                </div>
                <details v-for="(message, messageIndex) in systemMessages(call)" :key="messageIndex" class="message-card" :open="messageIndex === 0">
                  <summary><span class="role system">system {{ messageIndex + 1 }}</span><span>{{ charCount(message.content) }} chars</span></summary>
                  <pre>{{ message.content }}</pre>
                </details>
              </div>
            </details>

            <details class="debug-section">
              <summary>
                <span>最终消息序列</span>
                <span class="section-hint">实际发送顺序</span>
              </summary>
              <div class="section-body message-list">
                <details v-for="(message, messageIndex) in requestMessages(call)" :key="messageIndex" class="message-card">
                  <summary>
                    <span class="role" :class="message.role">{{ message.role || 'unknown' }}</span>
                    <span class="message-preview">{{ messagePreview(message) }}</span>
                    <span>{{ charCount(message.content) }} chars</span>
                  </summary>
                  <pre v-if="message.reasoning_content" class="reasoning-pre">reasoning_content:
{{ message.reasoning_content }}</pre>
                  <pre v-if="message.content">{{ message.content }}</pre>
                  <pre v-if="message.tool_calls?.length">{{ pretty(message.tool_calls) }}</pre>
                  <pre v-if="message.tool_call_id">tool_call_id: {{ message.tool_call_id }}</pre>
                </details>
              </div>
            </details>

            <details class="debug-section">
              <summary>
                <span>工具定义</span>
                <span class="section-hint">{{ requestTools(call).length }} 个</span>
              </summary>
              <div class="section-body">
                <details v-for="tool in requestTools(call)" :key="tool.function?.name || JSON.stringify(tool)" class="message-card">
                  <summary><span class="role tool">tool</span><strong>{{ tool.function?.name || 'unknown' }}</strong></summary>
                  <div v-if="tool.function?.description" class="tool-description">{{ tool.function.description }}</div>
                  <pre>{{ pretty(tool.function?.parameters || tool) }}</pre>
                </details>
              </div>
            </details>

            <details class="debug-section">
              <summary><span>上下文编译信息</span><span class="section-hint">hash / artifact / checkpoint</span></summary>
              <div class="section-body">
                <div class="hash-row"><span>context</span><code>{{ shortHash(call.context_hash) }}</code></div>
                <div class="hash-row"><span>prefix</span><code>{{ shortHash(call.prefix_hash) }}</code></div>
                <pre>{{ pretty(call.context_debug) }}</pre>
              </div>
            </details>

            <details class="debug-section">
              <summary><span>原始请求 JSON</span><span class="section-hint">不含 Authorization</span></summary>
              <div class="section-body">
                <div v-if="call.request_url" class="request-url">{{ call.request_url }}</div>
                <pre>{{ pretty(call.request) }}</pre>
              </div>
            </details>
          </div>
        </details>
      </div>
    </n-drawer-content>
  </n-drawer>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { NButton, NDrawer, NDrawerContent } from 'naive-ui'
import { storeToRefs } from 'pinia'
import { useConnectionStore } from '../stores/connection'
import { useSessionsStore } from '../stores/sessions'
import { useChatStore } from '../stores/chat'

interface DebugCall {
  id: string
  turn_id: string
  call_index: number
  provider: string
  model: string
  thinking: string
  status: string
  finish_reason?: string
  system_prompt_snapshot: string
  agent_run_id?: string
  context_epoch: number
  context_hash?: string
  prefix_hash?: string
  context_debug: Record<string, any>
  request_url?: string
  request: Record<string, any>
  prompt_tokens: number
  completion_tokens: number
  cached_tokens: number
  reasoning_tokens: number
  duration_ms: number
  ttft_ms: number
  error?: string
  created_at: string
  completed_at?: string
}

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ 'update:show': [value: boolean] }>()
const connection = useConnectionStore()
const sessions = useSessionsStore()
const chat = useChatStore()
const { currentSessionId } = storeToRefs(sessions)
const sessionId = computed(() => currentSessionId.value)
const calls = ref<DebugCall[]>([])
const loading = ref(false)
const error = ref('')
let timer: number | null = null
const drawerWidth = computed(() => Math.min(620, Math.max(360, window.innerWidth * 0.46)))

async function load() {
  const id = sessionId.value
  if (!id || !connection.isConnected) { calls.value = []; return }
  loading.value = true
  error.value = ''
  try {
    const response = await fetch(connection.api(`/api/sessions/${encodeURIComponent(id)}/debug`), { cache: 'no-store' })
    if (!response.ok) throw new Error(await response.text() || `HTTP ${response.status}`)
    const payload = await response.json() as { calls?: DebugCall[] }
    if (sessionId.value === id) calls.value = Array.isArray(payload.calls) ? payload.calls : []
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason)
  } finally {
    loading.value = false
  }
}

function schedulePolling() {
  if (timer != null) window.clearTimeout(timer)
  timer = null
  if (!props.show || !sessionId.value || !chat.executionActive) return
  timer = window.setTimeout(async () => { await load(); schedulePolling() }, 1200)
}
watch(() => [props.show, sessionId.value], () => { if (props.show) void load(); else calls.value = []; schedulePolling() }, { immediate: true })
watch(() => chat.executionActive, schedulePolling)
onBeforeUnmount(() => { if (timer != null) window.clearTimeout(timer) })

function requestMessages(call: DebugCall): any[] { return Array.isArray(call.request?.messages) ? call.request.messages : [] }
function requestTools(call: DebugCall): any[] { return Array.isArray(call.request?.tools) ? call.request.tools : [] }
function systemMessages(call: DebugCall): any[] {
  const messages = requestMessages(call).filter(message => message?.role === 'system')
  if (messages.length) return messages
  return call.system_prompt_snapshot ? [{ role: 'system', content: call.system_prompt_snapshot }] : []
}
function promptLayout(call: DebugCall): Record<string, any> | null {
  const value = call.context_debug?.prompt_layout
  return value && typeof value === 'object' && !Array.isArray(value) ? value : null
}
function contextAgent(call: DebugCall): string { return String(call.context_debug?.agent || 'agent') }
function charCount(value: unknown): number { return typeof value === 'string' ? value.length : 0 }
function messagePreview(message: any): string {
  if (message?.tool_calls?.length) return `tool_calls · ${message.tool_calls.map((item: any) => item?.function?.name || 'tool').join(', ')}`
  if (message?.tool_call_id) return `result · ${message.tool_call_id}`
  const text = String(message?.content || '').replace(/\s+/g, ' ').trim()
  return text.length > 78 ? `${text.slice(0, 77)}…` : text || '空内容'
}
function compactValue(value: unknown): string {
  if (Array.isArray(value)) return value.join(', ')
  if (value && typeof value === 'object') return Object.entries(value as Record<string, any>).map(([key, item]) => `${key}:${item}`).join(' · ')
  return String(value ?? '')
}
function pretty(value: unknown): string { try { return JSON.stringify(value ?? {}, null, 2) } catch { return String(value ?? '') } }
function shortHash(value?: string): string { return value ? `${value.slice(0, 12)}…${value.slice(-6)}` : '—' }
function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '—'
  if (ms < 1000) return `${ms}ms`
  return ms < 10000 ? `${(ms / 1000).toFixed(1)}s` : `${Math.round(ms / 1000)}s`
}
</script>

<style scoped>
.debug-header { display: flex; align-items: center; justify-content: space-between; gap: 12px; width: 100%; }
.debug-title { font-size: 15px; font-weight: 650; color: var(--text); }
.debug-subtitle { margin-top: 2px; color: var(--text-3); font-size: 11px; font-weight: 400; }
.debug-empty { padding: 44px 10px; text-align: center; color: var(--text-3); font-size: 13px; }
.debug-error { padding: 12px; border: 1px solid #f4ccd3; border-radius: 10px; background: #fff6f7; color: var(--error); font-size: 12px; white-space: pre-wrap; }
.debug-list { display: flex; flex-direction: column; gap: 9px; }
.debug-call { border: 1px solid var(--border); border-radius: 12px; background: var(--bg); overflow: hidden; }
.call-summary { list-style: none; cursor: pointer; padding: 10px 11px; display: grid; grid-template-columns: auto auto minmax(0,1fr) auto auto; gap: 7px; align-items: center; }
.call-summary::-webkit-details-marker, .debug-section > summary::-webkit-details-marker, .message-card > summary::-webkit-details-marker { display: none; }
.call-index { color: var(--text-3); font: 10px ui-monospace, SFMono-Regular, Menlo, monospace; }
.call-agent { padding: 2px 6px; border-radius: 999px; background: var(--accent-soft); color: var(--accent-text); font-size: 10px; font-weight: 650; text-transform: uppercase; }
.call-model { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text); font-size: 12px; font-weight: 600; }
.status-chip { padding: 2px 6px; border-radius: 999px; background: var(--bg-soft); color: var(--text-3); font-size: 10px; }
.status-completed { background: #eef9f3; color: #27845a; }
.status-failed { background: #fff0f2; color: var(--error); }
.status-running { background: var(--accent-soft); color: var(--accent-text); }
.call-time { color: var(--text-3); font-size: 10px; font-variant-numeric: tabular-nums; }
.call-body { padding: 0 10px 10px; }
.metric-row { display: flex; flex-wrap: wrap; gap: 6px 10px; padding: 7px 2px 9px; color: var(--text-3); font-size: 10px; }
.call-error { margin-bottom: 8px; padding: 8px 9px; border-radius: 8px; background: #fff5f6; color: var(--error); font-size: 11px; white-space: pre-wrap; }
.debug-section { border-top: 1px solid var(--border); }
.debug-section > summary { list-style: none; cursor: pointer; padding: 9px 2px; display: flex; align-items: center; justify-content: space-between; gap: 8px; color: var(--text); font-size: 11px; font-weight: 600; }
.debug-section > summary::before { content: '›'; color: var(--text-3); font-size: 15px; margin-right: 2px; }
.debug-section[open] > summary::before { content: '⌄'; }
.debug-section > summary > span:first-child { flex: 1; }
.section-hint { color: var(--text-3); font-size: 10px; font-weight: 400; }
.section-body { padding: 0 0 9px; }
.layout-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 5px; margin-bottom: 8px; }
.layout-item { min-width: 0; padding: 6px 7px; border-radius: 7px; background: var(--bg-soft); display: flex; flex-direction: column; gap: 2px; }
.layout-item span { color: var(--text-3); font-size: 9px; }
.layout-item strong { color: var(--text-2); font-size: 10px; font-weight: 550; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.message-list { display: flex; flex-direction: column; gap: 5px; }
.message-card { border: 1px solid var(--border); border-radius: 8px; background: var(--bg); overflow: hidden; }
.message-card > summary { list-style: none; cursor: pointer; padding: 7px 8px; display: flex; align-items: center; gap: 7px; color: var(--text-3); font-size: 10px; }
.message-preview { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-2); }
.role { padding: 2px 5px; border-radius: 5px; background: var(--bg-soft); color: var(--text-2); font: 9px ui-monospace, SFMono-Regular, Menlo, monospace; text-transform: uppercase; }
.role.system { background: #f1efff; color: #6553a4; }
.role.user { background: #eef6ff; color: #3d6c9d; }
.role.assistant { background: var(--accent-soft); color: var(--accent-text); }
.role.tool { background: #eef9f3; color: #27845a; }
.message-card pre, .section-body > pre { margin: 0; padding: 8px 9px; border-top: 1px solid var(--border); max-height: 360px; overflow: auto; white-space: pre-wrap; word-break: break-word; color: #6f6470; background: #fff; font: 10.5px/1.58 ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; }
.message-card .reasoning-pre { background: #fbf9ff; color: #756a83; }
.tool-description { padding: 0 9px 8px; color: var(--text-2); font-size: 11px; }
.hash-row { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 4px 2px; color: var(--text-3); font-size: 10px; }
.hash-row code { color: var(--text-2); font-size: 10px; }
.request-url { padding: 6px 8px; margin-bottom: 5px; border-radius: 7px; background: var(--bg-soft); color: var(--text-2); font: 10px ui-monospace, SFMono-Regular, Menlo, monospace; overflow-wrap: anywhere; }
@media (max-width: 680px) { .layout-grid { grid-template-columns: 1fr; } .call-summary { grid-template-columns: auto auto minmax(0,1fr) auto; } .call-time { display: none; } }
</style>
