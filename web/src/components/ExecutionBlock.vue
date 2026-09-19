<template>
  <details ref="detailsRef" class="execution">
    <summary>
      <span class="execution-icon">{{ statusIcon }}</span>
      <div class="summary-copy">
        <strong>执行过程</strong>
        <span>{{ groups.length }} 个步骤 · {{ statusText }}</span>
      </div>
      <span class="chevron">›</span>
    </summary>
    <div class="steps">
      <ToolCallBlock v-for="(records, index) in groups" :key="stepKey(records, index)" :records="records" compact />
    </div>
  </details>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import type { TimelineRecord } from '../stores/chat'
import ToolCallBlock from './ToolCallBlock.vue'

const props = defineProps<{ groups: TimelineRecord[][] }>()
const terminalKinds = new Set(['tool.completed', 'tool.failed', 'tool.rejected', 'tool.cancelled'])
const hasFailure = computed(() => props.groups.some(group => group.some(item => ['tool.failed', 'tool.rejected', 'tool.cancelled'].includes(item.kind))))
const isRunning = computed(() => props.groups.some(group => !group.some(item => terminalKinds.has(item.kind))))
const detailsRef = ref<HTMLDetailsElement | null>(null)
onMounted(() => {
  if (detailsRef.value && (isRunning.value || hasFailure.value)) detailsRef.value.open = true
})
watch([isRunning, hasFailure], ([running, failed], previous) => {
  const node = detailsRef.value
  if (!node) return
  if (running || failed) { node.open = true; return }
  if (previous?.[0]) node.open = false
}, { flush: 'post' })
const statusText = computed(() => hasFailure.value ? '有步骤需要注意' : isRunning.value ? '进行中' : '已完成')
const statusIcon = computed(() => hasFailure.value ? '!' : isRunning.value ? '…' : '✓')
function stepKey(records: TimelineRecord[], index: number): string { return String(records[0]?.data?.tool_call_id || records[0]?.id || index) }
</script>

<style scoped>
.execution { margin: 2px 0 16px; border: 1px solid var(--border); border-radius: 14px; background: color-mix(in srgb, var(--bg-soft) 72%, var(--bg)); overflow: hidden; }
.execution > summary { list-style: none; cursor: pointer; display: flex; align-items: center; gap: 10px; padding: 11px 13px; user-select: none; }
.execution > summary::-webkit-details-marker { display: none; }
.execution-icon { width: 22px; height: 22px; flex: 0 0 22px; border-radius: 7px; display: grid; place-items: center; background: var(--accent-soft); color: var(--accent-text); font-size: 12px; font-weight: 800; }
.summary-copy { min-width: 0; display: flex; align-items: baseline; gap: 8px; }
.summary-copy strong { color: var(--text); font-size: 12px; font-weight: 650; }
.summary-copy span { color: var(--text-3); font-size: 11px; }
.chevron { margin-left: auto; color: var(--text-3); font-size: 18px; transition: transform .16s ease; }
.execution[open] .chevron { transform: rotate(90deg); }
.steps { padding: 0 9px 8px; border-top: 1px solid color-mix(in srgb, var(--border) 72%, transparent); }
.steps :deep(.tool-block) { margin: 7px 0 0; box-shadow: none; }
</style>
