<template>
  <div class="tool-block" :class="statusClass">
    <div class="tool-shell">
      <div class="tool-status-icon" aria-hidden="true">
        <span v-if="status === 'running'" class="running-dot" />
        <span v-else>{{ statusIcon }}</span>
      </div>

      <div class="tool-main">
        <div class="tool-title-row">
          <strong class="tool-title">{{ toolTitle }}</strong>
          <code class="tool-name">{{ toolName }}</code>
          <span class="tool-state">{{ statusLabel }}</span>
          <span v-if="elapsed" class="tool-elapsed">{{ elapsed }}</span>
        </div>
        <div v-if="requestSummary" class="tool-summary" :title="requestSummary">{{ requestSummary }}</div>
      </div>

      <button class="details-button" type="button" :aria-expanded="detailsOpen" @click="detailsOpen = !detailsOpen">
        <span>详情</span>
        <span class="chevron" :class="{ open: detailsOpen }">⌄</span>
      </button>
    </div>

    <div v-if="errorText || resultHeadline || previewText" class="tool-result">
      <div v-if="errorText" class="result-error">{{ errorText }}</div>
      <div v-else-if="resultHeadline" class="result-headline">{{ resultHeadline }}</div>
      <pre v-if="previewText" class="result-preview">{{ previewText }}</pre>
    </div>

    <div v-if="detailsOpen" class="tool-details">
      <div class="detail-grid">
        <section>
          <div class="detail-label">参数</div>
          <pre>{{ argumentsJSON }}</pre>
        </section>
        <section v-if="resultMetaJSON">
          <div class="detail-label">结果</div>
          <pre>{{ resultMetaJSON }}</pre>
        </section>
      </div>
      <section v-if="rawOutput" class="detail-output">
        <div class="detail-label">输出</div>
        <pre>{{ rawOutput }}</pre>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import type { TimelineRecord } from '../stores/chat'

const props = defineProps<{ records: TimelineRecord[] }>()
const detailsOpen = ref(false)
type ToolStatus = 'completed' | 'failed' | 'rejected' | 'cancelled' | 'running' | 'queued'

const ordered = computed(() => [...props.records].sort((a, b) => a.seq - b.seq))
const requested = computed(() => ordered.value.find(item => item.kind === 'tool.requested') || ordered.value[0])
const started = computed(() => ordered.value.find(item => item.kind === 'tool.started'))
const terminal = computed(() => [...ordered.value].reverse().find(item => terminalKinds.has(item.kind)))
const sourceData = computed<Record<string, any>>(() => requested.value?.data || {})
const terminalData = computed<Record<string, any>>(() => terminal.value?.data || {})
const toolName = computed(() => String(sourceData.value.name || terminalData.value.name || 'tool'))
const args = computed<Record<string, any>>(() => asObject(sourceData.value.arguments))
const result = computed<Record<string, any> | null>(() => asObjectOrNull(terminalData.value.result))

const status = computed<ToolStatus>(() => {
  switch (terminal.value?.kind) {
    case 'tool.completed': return 'completed'
    case 'tool.failed': return 'failed'
    case 'tool.rejected': return 'rejected'
    case 'tool.cancelled': return 'cancelled'
    default: return started.value ? 'running' : 'queued'
  }
})
const statusClass = computed(() => `status-${status.value}`)
const statusLabels: Record<ToolStatus, string> = { completed: '已完成', failed: '失败', rejected: '已拒绝', cancelled: '已取消', running: '执行中', queued: '等待执行' }
const statusIcons: Record<ToolStatus, string> = { completed: '✓', failed: '×', rejected: '–', cancelled: '×', running: '·', queued: '·' }
const statusLabel = computed(() => statusLabels[status.value])
const statusIcon = computed(() => statusIcons[status.value])
const toolTitle = computed(() => toolTitles[toolName.value] || '工具调用')
const requestSummary = computed(() => summarizeRequest(toolName.value, args.value))
const elapsed = computed(() => formatElapsed(requested.value?.created_at, terminal.value?.created_at))

const errorText = computed(() => {
  const value = result.value?.error
  return typeof value === 'string' ? value.trim() : ''
})
const rawOutput = computed(() => {
  const value = result.value
  if (!value) return ''
  const output = [value.output, value.stdout, value.stderr].find(item => typeof item === 'string' && item.length > 0)
  return typeof output === 'string' ? output.trimEnd() : ''
})
const resultHeadline = computed(() => summarizeResult(toolName.value, result.value, rawOutput.value))
const previewText = computed(() => makePreview(toolName.value, rawOutput.value, resultHeadline.value))
const argumentsJSON = computed(() => prettyLimited(args.value, 12000))
const resultMetaJSON = computed(() => {
  if (!result.value) return ''
  const meta = { ...result.value }
  delete meta.output
  delete meta.stdout
  delete meta.stderr
  return Object.keys(meta).length ? prettyLimited(meta, 12000) : ''
})

const terminalKinds = new Set(['tool.completed', 'tool.failed', 'tool.rejected', 'tool.cancelled'])
const toolTitles: Record<string, string> = {
  list_files: '浏览文件',
  read_file: '读取文件',
  search_text: '搜索文本',
  write_file: '写入文件',
  run_command: '运行命令',
  spawn_explorer: '探索代码库',
  new_plan: '创建计划',
  edit_plan: '更新计划',
  create_phase: '创建阶段',
  update_phase: '更新阶段',
  done_phase: '完成阶段',
}

function asObject(value: unknown): Record<string, any> {
  if (value && typeof value === 'object' && !Array.isArray(value)) return value as Record<string, any>
  return {}
}
function asObjectOrNull(value: unknown): Record<string, any> | null {
  const object = asObject(value)
  return Object.keys(object).length ? object : null
}
function textArg(values: Record<string, any>, key: string, fallback = ''): string {
  const value = values[key]
  return typeof value === 'string' && value.trim() ? value.trim() : fallback
}
function numberArg(values: Record<string, any>, key: string): number | null {
  const value = Number(values[key])
  return Number.isFinite(value) && value > 0 ? value : null
}
function summarizeRequest(name: string, values: Record<string, any>): string {
  const path = textArg(values, 'path', '.')
  switch (name) {
    case 'list_files': {
      const max = numberArg(values, 'max_entries')
      return `${path}${max ? ` · 最多 ${max} 项` : ''}`
    }
    case 'read_file': {
      const start = numberArg(values, 'start_line')
      const end = numberArg(values, 'end_line')
      return `${path}${start ? ` · ${start}${end ? `–${end}` : ''} 行` : ''}`
    }
    case 'search_text': {
      const query = textArg(values, 'query')
      const max = numberArg(values, 'max_results')
      return `“${shorten(query, 72)}” · ${path}${max ? ` · 最多 ${max} 条` : ''}`
    }
    case 'write_file': {
      const content = textArg(values, 'content')
      return `${path}${content ? ` · ${formatBytes(new Blob([content]).size)}` : ''}`
    }
    case 'run_command': return `$ ${shorten(textArg(values, 'command'), 110)}${textArg(values, 'cwd') ? ` · ${textArg(values, 'cwd')}` : ''}`
    case 'spawn_explorer': return shorten(textArg(values, 'query'), 140)
    case 'new_plan':
    case 'edit_plan': return `${formatBytes(new Blob([textArg(values, 'content')]).size)} 内容`
    case 'create_phase': return shorten(textArg(values, 'title'), 120)
    case 'update_phase': return `${textArg(values, 'phase_id')}${textArg(values, 'status') ? ` · ${textArg(values, 'status')}` : ''}`
    case 'done_phase': return `${textArg(values, 'phase_id')}${textArg(values, 'summary') ? ` · ${shorten(textArg(values, 'summary'), 100)}` : ''}`
    default: return summarizeObject(values)
  }
}
function summarizeResult(name: string, value: Record<string, any> | null, output: string): string {
  if (!value) return ''
  const data = asObject(value.data)
  if (name === 'list_files') return typeof data.count === 'number' ? `找到 ${data.count} 项` : '目录读取完成'
  if (name === 'search_text') return typeof data.count === 'number' ? `找到 ${data.count} 处匹配` : '搜索完成'
  if (name === 'read_file') {
    const path = textArg(data, 'path')
    const start = Number(data.start_line || 0)
    const end = Number(data.end_line || 0)
    return path ? `${path}${start ? ` · ${start}${end ? `–${end}` : ''} 行` : ''}` : '文件读取完成'
  }
  if (name === 'write_file') {
    const path = textArg(data, 'path')
    const bytes = Number(data.bytes || 0)
    const verb = data.created ? '已创建' : '已更新'
    return `${verb}${path ? ` ${path}` : ''}${bytes ? ` · ${formatBytes(bytes)}` : ''}`
  }
  if (name === 'run_command') {
    const code = value.exit_code
    return typeof code === 'number' ? `命令结束 · Exit ${code}` : '命令结束'
  }
  if (typeof value.output === 'string' && value.output.trim() && !value.output.includes('\n')) return shorten(value.output.trim(), 160)
  if (output) return '已返回输出'
  return value.status === 'success' ? '执行完成' : ''
}
function makePreview(name: string, output: string, headline: string): string {
  if (!output) return ''
  const lines = output.split(/\r?\n/).filter((line, index, all) => line.length > 0 || index < all.length - 1)
  const limits: Record<string, number> = { list_files: 7, search_text: 6, read_file: 9, run_command: 10 }
  const limit = limits[name] || (output.length > 240 || lines.length > 2 ? 6 : 0)
  if (!limit) return headline === output.trim() ? '' : output.trim()
  const shown = lines.slice(0, limit)
  if (lines.length > limit) shown.push(`… 还有 ${lines.length - limit} 行`)
  return shown.join('\n')
}
function summarizeObject(values: Record<string, any>): string {
  const entries = Object.entries(values).filter(([key]) => key !== 'content').slice(0, 3)
  return entries.map(([key, value]) => `${key}: ${shorten(String(value), 60)}`).join(' · ')
}
function shorten(value: string, max: number): string {
  if (value.length <= max) return value
  return `${value.slice(0, Math.max(0, max - 1))}…`
}
function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}
function formatElapsed(from?: string, to?: string): string {
  if (!from || !to) return ''
  const start = Date.parse(from)
  const end = Date.parse(to)
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return ''
  const ms = end - start
  if (ms < 1000) return `${ms}ms`
  return ms < 10000 ? `${(ms / 1000).toFixed(1)}s` : `${Math.round(ms / 1000)}s`
}
function prettyLimited(value: unknown, max: number): string {
  const text = JSON.stringify(value, null, 2) || '{}'
  return text.length <= max ? text : `${text.slice(0, max)}\n… 已截断`
}
</script>

<style scoped>
.tool-block {
  margin-bottom: 10px;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: var(--bg);
  overflow: hidden;
  --tool-muted: #756a76;
}
.tool-shell {
  display: grid;
  grid-template-columns: 30px minmax(0, 1fr) auto;
  gap: 10px;
  align-items: center;
  padding: 10px 12px;
}
.tool-status-icon {
  width: 28px;
  height: 28px;
  border-radius: 9px;
  display: grid;
  place-items: center;
  background: var(--bg-soft);
  color: var(--text-3);
  font-size: 15px;
  font-weight: 700;
}
.status-completed .tool-status-icon { background: #eef9f3; color: #27845a; }
.status-failed .tool-status-icon { background: #fff0f2; color: var(--error); }
.status-rejected .tool-status-icon,
.status-cancelled .tool-status-icon { background: #f7f4f6; color: var(--tool-muted); }
.status-running .tool-status-icon { background: var(--accent-soft); color: var(--accent); }
.running-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: currentColor;
  animation: toolPulse 1s ease-in-out infinite;
}
@keyframes toolPulse { 0%, 100% { opacity: .35; } 50% { opacity: 1; } }
.tool-main { min-width: 0; }
.tool-title-row { display: flex; align-items: center; gap: 7px; min-width: 0; flex-wrap: wrap; }
.tool-title { color: var(--text); font-size: 13px; font-weight: 600; }
.tool-name {
  padding: 2px 6px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg-soft);
  color: var(--tool-muted);
  font-size: 10px;
  line-height: 1.35;
}
.tool-state { color: var(--tool-muted); font-size: 11px; }
.status-completed .tool-state { color: #27845a; }
.status-failed .tool-state { color: var(--error); }
.status-running .tool-state { color: var(--accent-text); }
.tool-elapsed { color: var(--text-3); font-size: 10px; }
.tool-summary {
  margin-top: 3px;
  color: var(--tool-muted);
  font-size: 12px;
  line-height: 1.45;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.details-button {
  border: 0;
  background: transparent;
  color: var(--text-3);
  padding: 4px 2px 4px 8px;
  font: inherit;
  font-size: 11px;
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 4px;
}
.details-button:hover { color: var(--accent-text); }
.chevron { display: inline-block; font-size: 14px; line-height: 1; transform: translateY(-1px); }
.chevron.open { transform: rotate(180deg) translateY(1px); }
.tool-result {
  margin: -2px 12px 10px 50px;
  padding: 8px 10px;
  border-radius: 8px;
  background: var(--bg-soft);
  min-width: 0;
}
.result-headline { color: var(--tool-muted); font-size: 12px; line-height: 1.45; }
.result-error { color: var(--error); font-size: 12px; line-height: 1.5; white-space: pre-wrap; word-break: break-word; }
.result-preview {
  margin: 6px 0 0;
  max-height: 150px;
  overflow: auto;
  white-space: pre;
  font: 11px/1.55 ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  color: var(--tool-muted);
}
.tool-details {
  border-top: 1px solid var(--border);
  background: var(--bg-soft);
  padding: 10px 12px 12px 50px;
}
.detail-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
.detail-label {
  margin-bottom: 5px;
  color: var(--text-3);
  font-size: 10px;
  font-weight: 600;
  letter-spacing: .05em;
  text-transform: uppercase;
}
.tool-details pre {
  margin: 0;
  padding: 8px 9px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--bg);
  color: var(--tool-muted);
  max-height: 260px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
  font: 11px/1.55 ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
}
.detail-output { margin-top: 10px; }
.detail-output pre { white-space: pre; word-break: normal; }
@media (max-width: 680px) {
  .tool-shell { grid-template-columns: 28px minmax(0, 1fr); }
  .details-button { grid-column: 2; justify-self: start; padding-left: 0; }
  .tool-result, .tool-details { margin-left: 12px; padding-left: 10px; }
  .detail-grid { grid-template-columns: 1fr; }
}
</style>
