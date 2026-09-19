<template>
  <div class="tool-block" :class="statusClass">
    <div class="tool-header">
      <div class="tool-icon" aria-hidden="true">{{ toolGlyph }}</div>

      <div class="tool-heading">
        <div class="tool-title-row">
          <strong class="tool-title">{{ toolTitle }}</strong>
          <code class="tool-name">{{ toolName }}</code>
        </div>
        <div v-if="requestSummary" class="tool-summary" :title="requestSummary">{{ requestSummary }}</div>
      </div>

      <div class="tool-meta">
        <span class="status-pill">
          <span class="status-dot" />
          {{ statusLabel }}
        </span>
        <span v-if="elapsed" class="tool-elapsed">{{ elapsed }}</span>
      </div>

      <button class="details-button" type="button" :aria-label="detailsOpen ? '收起工具详情' : '展开工具详情'" :aria-expanded="detailsOpen" @click="detailsOpen = !detailsOpen">
        <span class="chevron" :class="{ open: detailsOpen }">⌄</span>
      </button>
    </div>

    <div v-if="showResultArea" class="tool-result">
      <div class="result-bar">
        <span v-if="errorText" class="result-error">{{ errorText }}</span>
        <span v-else class="result-headline">{{ resultHeadline || pendingText }}</span>
        <span v-if="resultMetric" class="result-metric">{{ resultMetric }}</span>
      </div>

      <div v-if="toolName === 'list_files' && listEntries.length" class="file-list">
        <div v-for="entry in listEntries" :key="entry.path" class="file-row">
          <span class="file-mark" :class="{ directory: entry.directory }">{{ entry.directory ? '▸' : '·' }}</span>
          <code>{{ entry.path }}</code>
        </div>
        <div v-if="listHiddenCount > 0" class="more-row">还有 {{ listHiddenCount }} 项，展开详情查看完整输出</div>
      </div>

      <div v-else-if="toolName === 'search_text' && searchMatches.length" class="search-list">
        <div v-for="match in searchMatches" :key="`${match.path}:${match.line}:${match.text}`" class="search-row">
          <div class="search-location"><code>{{ match.path }}</code><span>:{{ match.line }}</span></div>
          <div class="search-snippet">{{ match.text }}</div>
        </div>
        <div v-if="searchHiddenCount > 0" class="more-row">还有 {{ searchHiddenCount }} 处匹配，展开详情查看</div>
      </div>

      <pre v-else-if="previewText" class="result-preview">{{ previewText }}</pre>
    </div>

    <div v-if="detailsOpen" class="tool-details">
      <div class="detail-grid">
        <section>
          <div class="detail-label">调用参数</div>
          <pre>{{ argumentsJSON }}</pre>
        </section>
        <section v-if="resultMetaJSON">
          <div class="detail-label">执行结果</div>
          <pre>{{ resultMetaJSON }}</pre>
        </section>
      </div>
      <section v-if="rawOutput" class="detail-output">
        <div class="detail-label">完整输出</div>
        <pre>{{ rawOutput }}</pre>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import type { TimelineRecord } from '../stores/chat'
import { useChatStore } from '../stores/chat'

const props = defineProps<{ records: TimelineRecord[] }>()
const chatStore = useChatStore()
const detailsOpen = ref(false)
type ToolStatus = 'completed' | 'failed' | 'rejected' | 'cancelled' | 'running' | 'approval' | 'queued'

const terminalKinds = new Set(['tool.completed', 'tool.failed', 'tool.rejected', 'tool.cancelled'])
const ordered = computed(() => [...props.records].sort((a, b) => a.seq - b.seq))
const requested = computed(() => ordered.value.find(item => item.kind === 'tool.requested') || ordered.value[0])
const started = computed(() => ordered.value.find(item => item.kind === 'tool.started'))
const terminal = computed(() => [...ordered.value].reverse().find(item => terminalKinds.has(item.kind)))
const sourceData = computed<Record<string, any>>(() => requested.value?.data || {})
const terminalData = computed<Record<string, any>>(() => terminal.value?.data || {})
const toolCallID = computed(() => String(sourceData.value.tool_call_id || terminalData.value.tool_call_id || ''))
const toolName = computed(() => String(sourceData.value.name || terminalData.value.name || 'tool'))
const args = computed<Record<string, any>>(() => asObject(sourceData.value.arguments))
const result = computed<Record<string, any> | null>(() => asObjectOrNull(terminalData.value.result))
const approval = computed(() => chatStore.runtimeState.approvals.find(item => item.tool_call_id === toolCallID.value))

const status = computed<ToolStatus>(() => {
  switch (terminal.value?.kind) {
    case 'tool.completed': return 'completed'
    case 'tool.failed': return 'failed'
    case 'tool.rejected': return 'rejected'
    case 'tool.cancelled': return 'cancelled'
    default:
      if (started.value) return 'running'
      if (approval.value?.status === 'pending') return 'approval'
      return 'queued'
  }
})
const statusClass = computed(() => `status-${status.value}`)
const statusLabels: Record<ToolStatus, string> = {
  completed: '已完成', failed: '失败', rejected: '已拒绝', cancelled: '已取消', running: '执行中', approval: '待确认', queued: '等待执行',
}
const statusLabel = computed(() => statusLabels[status.value])
const toolTitle = computed(() => toolTitles[toolName.value] || '工具调用')
const toolGlyph = computed(() => toolGlyphs[toolName.value] || '◇')
const requestSummary = computed(() => summarizeRequest(toolName.value, args.value))
const elapsed = computed(() => formatElapsed(requested.value?.created_at, terminal.value?.created_at))

const errorText = computed(() => {
  const value = result.value?.error
  return typeof value === 'string' ? value.trim() : ''
})
const rawOutput = computed(() => collectOutput(result.value))
const resultHeadline = computed(() => summarizeResult(toolName.value, result.value, rawOutput.value))
const resultMetric = computed(() => summarizeMetric(toolName.value, result.value))
const pendingText = computed(() => {
  if (status.value === 'approval') return approval.value?.risk || '该操作需要你的确认后才能继续。'
  if (status.value === 'running') return '正在执行工具…'
  if (status.value === 'queued') return '等待 Runtime 调度…'
  return ''
})
const showResultArea = computed(() => Boolean(errorText.value || resultHeadline.value || previewText.value || pendingText.value || listEntries.value.length || searchMatches.value.length))
const previewText = computed(() => makePreview(toolName.value, rawOutput.value, resultHeadline.value))

const listLines = computed(() => rawOutput.value.split(/\r?\n/).map(line => line.trim()).filter(Boolean))
const listEntries = computed(() => listLines.value.slice(0, 10).map(path => ({ path, directory: path.endsWith('/') })))
const listHiddenCount = computed(() => Math.max(0, listLines.value.length - listEntries.value.length))
const parsedSearchMatches = computed(() => rawOutput.value.split(/\r?\n/).map(parseSearchMatch).filter((item): item is SearchMatch => item !== null))
const searchMatches = computed(() => parsedSearchMatches.value.slice(0, 6))
const searchHiddenCount = computed(() => Math.max(0, parsedSearchMatches.value.length - searchMatches.value.length))

const argumentsJSON = computed(() => prettyLimited(sanitizeArguments(args.value), 12000))
const resultMetaJSON = computed(() => {
  if (!result.value) return ''
  const meta = { ...result.value }
  delete meta.output
  delete meta.stdout
  delete meta.stderr
  return Object.keys(meta).length ? prettyLimited(meta, 12000) : ''
})

type SearchMatch = { path: string; line: number; text: string }
const toolTitles: Record<string, string> = {
  list_files: '浏览工作区',
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
const toolGlyphs: Record<string, string> = {
  list_files: '☷', read_file: '↗', search_text: '⌕', write_file: '✎', run_command: '›_', spawn_explorer: '◇',
  new_plan: '≡', edit_plan: '≡', create_phase: '+', update_phase: '↻', done_phase: '✓',
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
      return `${path}${start ? ` · 第 ${start}${end ? `–${end}` : ''} 行` : ''}`
    }
    case 'search_text': {
      const query = textArg(values, 'query')
      const max = numberArg(values, 'max_results')
      return `“${shorten(query, 72)}” · ${path}${max ? ` · 最多 ${max} 条` : ''}`
    }
    case 'write_file': {
      const content = textArg(values, 'content')
      return `${path}${content ? ` · ${formatBytes(utf8Bytes(content))}` : ''}`
    }
    case 'run_command': return `$ ${shorten(textArg(values, 'command'), 110)}${textArg(values, 'cwd') ? ` · ${textArg(values, 'cwd')}` : ''}`
    case 'spawn_explorer': return shorten(textArg(values, 'query'), 140)
    case 'new_plan':
    case 'edit_plan': return `${formatBytes(utf8Bytes(textArg(values, 'content')))} 内容`
    case 'create_phase': return shorten(textArg(values, 'title'), 120)
    case 'update_phase': return `${textArg(values, 'phase_id')}${textArg(values, 'status') ? ` · ${textArg(values, 'status')}` : ''}`
    case 'done_phase': return `${textArg(values, 'phase_id')}${textArg(values, 'summary') ? ` · ${shorten(textArg(values, 'summary'), 100)}` : ''}`
    default: return summarizeObject(values)
  }
}
function summarizeResult(name: string, value: Record<string, any> | null, output: string): string {
  if (!value) return ''
  const data = asObject(value.data)
  if (name === 'list_files') return typeof data.count === 'number' ? '工作区扫描完成' : '目录读取完成'
  if (name === 'search_text') return typeof data.count === 'number' ? '搜索完成' : '文本搜索完成'
  if (name === 'read_file') {
    const path = textArg(data, 'path')
    const start = Number(data.start_line || 0)
    const end = Number(data.end_line || 0)
    return path ? `${path}${start ? ` · 第 ${start}${end ? `–${end}` : ''} 行` : ''}` : '文件读取完成'
  }
  if (name === 'write_file') {
    const path = textArg(data, 'path')
    const verb = data.created ? '已创建' : '已更新'
    return `${verb}${path ? ` ${path}` : '文件'}`
  }
  if (name === 'run_command') {
    const code = value.exit_code
    return typeof code === 'number' ? (code === 0 ? '命令执行完成' : `命令退出 · Exit ${code}`) : '命令执行完成'
  }
  if (typeof value.output === 'string' && value.output.trim() && !value.output.includes('\n')) return shorten(value.output.trim(), 160)
  if (output) return '已返回输出'
  return value.status === 'success' ? '执行完成' : ''
}
function summarizeMetric(name: string, value: Record<string, any> | null): string {
  if (!value) return ''
  const data = asObject(value.data)
  if (name === 'list_files' && typeof data.count === 'number') return `${data.count} 项`
  if (name === 'search_text' && typeof data.count === 'number') return `${data.count} 处匹配`
  if (name === 'write_file' && typeof data.bytes === 'number') return formatBytes(data.bytes)
  if (name === 'run_command' && typeof value.exit_code === 'number') return `Exit ${value.exit_code}`
  return ''
}
function collectOutput(value: Record<string, any> | null): string {
  if (!value) return ''
  const output = typeof value.output === 'string' ? value.output.trimEnd() : ''
  const stdout = typeof value.stdout === 'string' ? value.stdout.trimEnd() : ''
  const stderr = typeof value.stderr === 'string' ? value.stderr.trimEnd() : ''
  if (output) return output
  if (stdout && stderr) return `${stdout}\n\n[stderr]\n${stderr}`
  return stdout || stderr
}
function makePreview(name: string, output: string, headline: string): string {
  if (!output || name === 'list_files' || name === 'search_text') return ''
  const lines = output.split(/\r?\n/)
  const limits: Record<string, number> = { read_file: 12, run_command: 12, spawn_explorer: 8 }
  const limit = limits[name] || (output.length > 320 || lines.length > 3 ? 8 : 0)
  if (!limit) return headline === output.trim() ? '' : output.trim()
  const shown = lines.slice(0, limit)
  if (lines.length > limit) shown.push(`… 还有 ${lines.length - limit} 行`)
  return shown.join('\n')
}
function parseSearchMatch(line: string): SearchMatch | null {
  const match = line.match(/^(.*?):(\d+):\s?(.*)$/)
  if (!match) return null
  return { path: match[1], line: Number(match[2]), text: match[3] }
}
function sanitizeArguments(values: Record<string, any>): Record<string, any> {
  const result: Record<string, any> = { ...values }
  for (const key of ['content']) {
    const value = result[key]
    if (typeof value === 'string' && value.length > 2400) result[key] = `${value.slice(0, 2400)}\n… 已截断（共 ${value.length} 字符）`
  }
  return result
}
function summarizeObject(values: Record<string, any>): string {
  const entries = Object.entries(values).filter(([key]) => key !== 'content').slice(0, 3)
  return entries.map(([key, value]) => `${key}: ${shorten(String(value), 60)}`).join(' · ')
}
function shorten(value: string, max: number): string {
  if (value.length <= max) return value
  return `${value.slice(0, Math.max(0, max - 1))}…`
}
function utf8Bytes(value: string): number { return new TextEncoder().encode(value).length }
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
  margin: 0 0 12px;
  border: 1px solid var(--border-strong);
  border-radius: 14px;
  background: var(--bg);
  overflow: hidden;
}
.tool-header {
  display: grid;
  grid-template-columns: 34px minmax(0, 1fr) auto 24px;
  align-items: center;
  gap: 10px;
  padding: 11px 12px;
  background: linear-gradient(90deg, var(--accent-tint), transparent 46%);
}
.tool-icon {
  width: 32px;
  height: 32px;
  border: 1px solid var(--border-strong);
  border-radius: 10px;
  display: grid;
  place-items: center;
  background: var(--bg);
  color: var(--accent-text);
  font: 600 15px/1 ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
}
.tool-heading { min-width: 0; }
.tool-title-row { display: flex; align-items: center; gap: 7px; min-width: 0; flex-wrap: wrap; }
.tool-title { color: var(--text); font-size: 13px; font-weight: 650; }
.tool-name {
  padding: 2px 6px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: rgba(255, 255, 255, .72);
  color: #7f7180;
  font-size: 10px;
  line-height: 1.35;
}
.tool-summary {
  margin-top: 3px;
  color: #7c707c;
  font-size: 12px;
  line-height: 1.4;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.tool-meta { display: flex; align-items: center; gap: 8px; white-space: nowrap; }
.status-pill {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 3px 7px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--bg);
  color: #837683;
  font-size: 10px;
  font-weight: 600;
}
.status-dot { width: 5px; height: 5px; border-radius: 50%; background: currentColor; }
.status-completed .status-pill { color: var(--accent-text); border-color: #f1cddd; background: var(--accent-tint); }
.status-running .status-pill { color: var(--accent-text); background: var(--accent-tint); }
.status-running .status-dot { animation: toolPulse 1s ease-in-out infinite; }
.status-approval .status-pill { color: #9a6812; background: #fff9ec; border-color: #f5e8c6; }
.status-failed .status-pill { color: var(--error); background: #fff4f5; border-color: #f6d9de; }
.status-rejected .status-pill,
.status-cancelled .status-pill { color: #8a7d89; background: #faf8f9; }
@keyframes toolPulse { 0%, 100% { opacity: .3; } 50% { opacity: 1; } }
.tool-elapsed { color: var(--text-3); font-size: 10px; }
.details-button {
  width: 24px;
  height: 24px;
  padding: 0;
  border: 0;
  border-radius: 7px;
  background: transparent;
  color: var(--text-3);
  cursor: pointer;
  display: grid;
  place-items: center;
}
.details-button:hover { background: var(--accent-tint); color: var(--accent-text); }
.chevron { font-size: 15px; line-height: 1; transform: translateY(-1px); }
.chevron.open { transform: rotate(180deg) translateY(1px); }
.tool-result {
  margin: 0 12px 11px 56px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: #fffdfd;
  overflow: hidden;
}
.result-bar {
  min-height: 18px;
  padding: 7px 9px;
  display: flex;
  align-items: center;
  gap: 8px;
  border-bottom: 1px solid var(--border);
  color: #756a76;
  font-size: 11px;
}
.result-headline { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.result-error { color: var(--error); white-space: pre-wrap; word-break: break-word; }
.result-metric { margin-left: auto; color: var(--accent-text); font-weight: 650; white-space: nowrap; }
.result-preview {
  margin: 0;
  padding: 9px 10px;
  max-height: 190px;
  overflow: auto;
  white-space: pre;
  color: #675d68;
  background: var(--bg);
  font: 11px/1.58 ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
}
.file-list { padding: 5px 0; background: var(--bg); }
.file-row {
  min-height: 23px;
  display: grid;
  grid-template-columns: 18px minmax(0, 1fr);
  align-items: center;
  padding: 0 10px;
  color: #706671;
  font-size: 11px;
}
.file-row:hover { background: var(--accent-tint); }
.file-row code { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font: inherit; }
.file-mark { color: #b5a7b4; font-size: 12px; }
.file-mark.directory { color: var(--accent-text); }
.search-list { background: var(--bg); }
.search-row { padding: 7px 10px; border-bottom: 1px solid var(--border); }
.search-row:last-of-type { border-bottom: 0; }
.search-location { display: flex; align-items: baseline; gap: 2px; color: var(--accent-text); font-size: 10px; }
.search-location code { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.search-location span { color: var(--text-3); }
.search-snippet { margin-top: 3px; color: #706671; font: 11px/1.45 ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.more-row { padding: 6px 10px; border-top: 1px dashed var(--border); color: var(--text-3); font-size: 10px; }
.tool-details { border-top: 1px solid var(--border); background: var(--bg-soft); padding: 11px 12px 12px 56px; }
.detail-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
.detail-label { margin-bottom: 5px; color: #8e818e; font-size: 10px; font-weight: 650; }
.tool-details pre {
  margin: 0;
  padding: 8px 9px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--bg);
  color: #6d626d;
  max-height: 280px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
  font: 11px/1.55 ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
}
.detail-output { margin-top: 10px; }
.detail-output pre { white-space: pre; word-break: normal; }
@media (max-width: 720px) {
  .tool-header { grid-template-columns: 32px minmax(0, 1fr) 24px; }
  .tool-meta { grid-column: 2; justify-content: flex-start; }
  .details-button { grid-column: 3; grid-row: 1 / span 2; }
  .tool-result { margin-left: 12px; }
  .tool-details { padding-left: 12px; }
  .detail-grid { grid-template-columns: 1fr; }
}
</style>
