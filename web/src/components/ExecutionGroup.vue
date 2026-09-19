<template>
  <section class="execution-group" :class="{ 'has-failure': failed }">
    <button class="execution-header" type="button" :aria-expanded="expanded" @click="expanded = !expanded">
      <span class="execution-indicator" :class="{ busy: !finished }">{{ failed ? '!' : finished ? '✓' : '◌' }}</span>
      <span class="execution-copy">
        <strong>执行过程 <span class="execution-count">· {{ steps.length }} 步</span></strong>
        <small>{{ description }}</small>
      </span>
      <span class="execution-status">{{ failed ? '需注意' : finished ? '已完成' : '进行中' }}</span>
      <span class="chevron" :class="{ opened: expanded }">⌄</span>
    </button>
    <div v-if="expanded" class="execution-detail">
      <div class="detail-hint">工具请求、输入和输出 · 点击单个步骤查看细节</div>
      <ToolCallBlock v-for="step in steps" :key="step.id" :records="step.records" />
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import type { TimelineRecord } from '../stores/chat'
import ToolCallBlock from './ToolCallBlock.vue'

const props = defineProps<{ records: TimelineRecord[]; complete: boolean }>()
const expanded = ref(false)
const names: Record<string, string> = {
  write_file: '写入文件', run_command: '执行命令', verify_file: '核验文件',
  list_files: '查看文件', read_file: '读取源码', search_text: '搜索代码',
  spawn_explorer: '探索项目', new_plan: '保存计划', edit_plan: '修订计划',
}
const steps = computed(() => {
  const groups: Array<{ id: string; name: string; records: TimelineRecord[] }> = []
  const index = new Map<string, number>()
  for (const record of props.records) {
    const callId = String(record.data?.tool_call_id || '')
    if (!callId) continue
    let at = index.get(callId)
    if (at === undefined) {
      at = groups.length
      index.set(callId, at)
      groups.push({ id: callId, name: String(record.data?.name || 'tool'), records: [] })
    }
    groups[at].records.push(record)
  }
  return groups
})
const failed = computed(() => steps.value.some(step => step.records.some(record => ['tool.failed', 'tool.rejected', 'tool.cancelled'].includes(record.kind))))
const finished = computed(() => props.complete && steps.value.every(step => step.records.some(record => ['tool.completed', 'tool.failed', 'tool.rejected', 'tool.cancelled'].includes(record.kind))))
const description = computed(() => {
  const namesSeen = [...new Set(steps.value.map(step => names[step.name] || step.name))]
  return namesSeen.slice(0, 3).join(' · ') + (namesSeen.length > 3 ? ` · 另 ${namesSeen.length - 3} 类` : '')
})
</script>

<style scoped>
.execution-group { margin: 8px 0 16px; border: 1px solid var(--border-strong); border-radius: 14px; background: var(--bg-soft); overflow: hidden; }
.execution-group.has-failure { border-color: color-mix(in srgb, var(--error) 35%, var(--border)); }
.execution-header { display: flex; align-items: center; text-align: left; width: 100%; gap: 10px; padding: 12px 14px; border: 0; background: transparent; cursor: pointer; color: var(--text); }
.execution-header:hover { background: var(--accent-tint); }
.execution-indicator { display: grid; place-items: center; flex: 0 0 22px; height: 22px; border-radius: 8px; background: #e6f7ef; color: #27845a; font-size: 13px; font-weight: 700; }
.execution-indicator.busy { background: var(--accent-soft); color: var(--accent-text); }
.has-failure .execution-indicator { background: #fff0f2; color: var(--error); }
.execution-copy { flex: 1; display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.execution-copy strong { font-size: 12px; font-weight: 650; }
.execution-count { color: var(--text-3); font-weight: 400; }
.execution-copy small { color: var(--text-2); font-size: 11px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.execution-status { flex: none; font-size: 11px; color: var(--text-3); }
.chevron { color: var(--text-3); display: inline-block; transition: transform .12s; }
.chevron.opened { transform: rotate(180deg); }
.execution-detail { padding: 0 12px 12px; border-top: 1px solid var(--border); }
.detail-hint { padding: 10px 2px; color: var(--text-3); font-size: 11px; }
.execution-detail :deep(.tool-block) { margin-bottom: 8px; }
</style>
