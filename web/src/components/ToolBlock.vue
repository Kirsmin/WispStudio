<template>
  <div class="tool" :class="statusClass">
    <button class="tool-head" type="button" :aria-expanded="open" @click="open = !open">
      <span class="tool-dot" />
      <span class="tool-name">{{ tool.name }}</span>
      <span class="tool-summary">{{ summary }}</span>
      <span class="tool-state">{{ statusLabel }}</span>
      <span class="tool-chevron" :class="{ open }">›</span>
    </button>

    <div v-if="tool.approval === 'pending'" class="approval">
      <span class="approval-text">该工具会产生工作区副作用，需要批准后执行。</span>
      <span class="approval-actions">
        <button class="mini primary" type="button" :disabled="busy" @click="decide(true)">批准</button>
        <button class="mini" type="button" :disabled="busy" @click="decide(false)">拒绝</button>
      </span>
    </div>

    <div v-if="open" class="tool-body">
      <section v-if="tool.args" class="tool-section">
        <div class="tool-label">参数</div>
        <pre>{{ prettyArgs }}</pre>
      </section>
      <section v-if="resultText" class="tool-section">
        <div class="tool-label">结果</div>
        <pre :class="{ failure: tool.resultStatus === 'failure' }">{{ resultText }}</pre>
      </section>
      <div v-else-if="tool.status === 'called'" class="tool-pending">等待工具返回…</div>
      <div v-if="actionError" class="tool-error">{{ actionError }}</div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { ChatTool } from '../stores/chat'
import { useChatStore } from '../stores/chat'

const props = defineProps<{ tool: ChatTool }>()
const chat = useChatStore()

const open = ref(false)
const busy = ref(false)
const actionError = ref('')

watch(() => props.tool.approval, (value) => {
  if (value === 'pending') open.value = true
}, { immediate: true })

const statusClass = computed(() => `status-${props.tool.approval === 'rejected' ? 'rejected' : props.tool.status}`)

const statusLabel = computed(() => {
  if (props.tool.approval === 'pending') return '等待批准'
  if (props.tool.approval === 'rejected') return '已拒绝'
  switch (props.tool.status) {
    case 'awaiting': return '等待批准'
    case 'called': return '执行中'
    case 'done': return '已完成'
    case 'failed': return '失败'
    case 'cancelled': return '已取消'
    case 'rejected': return '已拒绝'
    default: return '执行中'
  }
})

const args = computed<Record<string, unknown>>(() => {
  try {
    const parsed = JSON.parse(props.tool.args || '{}') as unknown
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed)
      ? parsed as Record<string, unknown>
      : {}
  } catch {
    return {}
  }
})

const prettyArgs = computed(() => prettyJSON(props.tool.args))

const summary = computed(() => {
  const name = props.tool.name
  const path = text(args.value.path) || '.'
  switch (name) {
    case 'read_file': {
      const start = number(args.value.start_line)
      const end = number(args.value.end_line)
      return `${path}${start ? ` · ${start}${end ? `-${end}` : ''} 行` : ''}`
    }
    case 'write_file': return `写入 ${path} · ${text(args.value.content).length} 字`
    case 'list_files': return `列出 ${path}`
    case 'search_text': return `搜索 “${shorten(text(args.value.query), 60)}”`
    case 'run_command': return `$ ${shorten(text(args.value.command), 100)}`
    default: {
      const first = Object.entries(args.value).slice(0, 2)
      return first.map(([key, value]) => `${key}: ${shorten(String(value), 40)}`).join(' · ')
    }
  }
})

interface ToolResultPayload {
  status?: string
  output?: string
  error?: string
  stdout?: string
  stderr?: string
  exit_code?: number
  data?: unknown
}

const result = computed<ToolResultPayload | null>(() => {
  if (!props.tool.result) return null
  try {
    const parsed = JSON.parse(props.tool.result) as unknown
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      return parsed as ToolResultPayload
    }
  } catch {
    // 结果不是 JSON 时按纯文本展示
  }
  return { output: props.tool.result }
})

const resultText = computed(() => {
  const value = result.value
  if (!value) return ''
  const parts: string[] = []
  for (const part of [value.error, value.output, value.stdout, value.stderr]) {
    if (typeof part === 'string' && part.trim() && !parts.includes(part.trim())) {
      parts.push(part.trim())
    }
  }
  if (parts.length > 0) return parts.join('\n')
  if (typeof value.exit_code === 'number') return `exit code: ${value.exit_code}`
  if (value.data !== undefined) return prettyJSON(JSON.stringify(value.data))
  return ''
})

async function decide(approve: boolean): Promise<void> {
  if (busy.value) return
  busy.value = true
  actionError.value = ''
  try {
    await chat.decideApproval(props.tool, approve)
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : String(error)
  } finally {
    busy.value = false
  }
}

function text(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function number(value: unknown): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0
}

function shorten(value: string, max: number): string {
  return value.length <= max ? value : `${value.slice(0, Math.max(0, max - 1))}…`
}

function prettyJSON(raw: string): string {
  if (!raw) return ''
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}
</script>

<style scoped>
.tool {
  border: 1px solid var(--border);
  margin: 0 0 14px;
  background: var(--bg);
}

.tool-head {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 8px 10px;
  border: 0;
  background: var(--bg-soft);
  text-align: left;
  cursor: pointer;
}

.tool-head:hover {
  background: #f0f2f5;
}

.tool-dot {
  width: 7px;
  height: 7px;
  flex: none;
  background: var(--text-3);
}

.status-called .tool-dot { background: var(--blue); }
.status-awaiting .tool-dot { background: var(--warn); }
.status-done .tool-dot { background: var(--ok); }
.status-failed .tool-dot,
.status-rejected .tool-dot { background: var(--error); }
.status-cancelled .tool-dot { background: var(--text-3); }

.tool-name {
  font-family: var(--mono);
  font-size: 12px;
  color: var(--text);
  flex: none;
}

.tool-summary {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  color: var(--text-2);
  font-size: 12px;
}

.tool-state {
  flex: none;
  color: var(--text-3);
  font-size: 11px;
}

.status-awaiting .tool-state { color: var(--warn); }
.status-failed .tool-state,
.status-rejected .tool-state { color: var(--error); }

.tool-chevron {
  flex: none;
  color: var(--text-3);
  font-size: 15px;
  line-height: 1;
  transition: transform .12s ease;
}

.tool-chevron.open {
  transform: rotate(90deg);
}

.approval {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 8px 10px;
  border-top: 1px solid var(--border);
  background: #fbf7ed;
  font-size: 12px;
}

.approval-text {
  color: var(--text-2);
}

.approval-actions {
  display: flex;
  gap: 6px;
  flex: none;
}

.tool-body {
  border-top: 1px solid var(--border);
  padding: 10px;
}

.tool-section + .tool-section {
  margin-top: 10px;
}

.tool-label {
  color: var(--text-3);
  font-size: 11px;
  margin-bottom: 4px;
}

.tool-body pre {
  margin: 0;
  padding: 8px;
  border: 1px solid var(--border);
  background: var(--bg-soft);
  color: var(--text-2);
  font-family: var(--mono);
  font-size: 12px;
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 320px;
  overflow: auto;
}

.tool-body pre.failure {
  border-color: #e5c4c4;
  background: #fdf3f3;
  color: var(--error);
}

.tool-pending {
  color: var(--text-3);
  font-size: 12px;
}

.tool-error {
  margin-top: 8px;
  color: var(--error);
  font-size: 12px;
}

.mini {
  padding: 3px 9px;
  border: 1px solid var(--border-strong);
  background: var(--bg);
  color: var(--text-2);
  font-size: 12px;
  cursor: pointer;
}

.mini.primary {
  border-color: var(--blue);
  background: var(--blue);
  color: #fff;
}

.mini:hover:not(:disabled) {
  border-color: var(--text-3);
}

.mini.primary:hover:not(:disabled) {
  border-color: var(--blue-hover);
  background: var(--blue-hover);
}

.mini:disabled {
  opacity: .45;
  cursor: not-allowed;
}
</style>
