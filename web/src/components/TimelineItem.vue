<template>
  <div v-if="visible" class="timeline-item" :class="category">
    <template v-if="category === 'user'">
      <div class="user-bubble">
        <span v-if="record.kind === 'user.steering'" class="steering-label">Steering</span>
        {{ record.content }}
      </div>
    </template>

    <template v-else-if="category === 'assistant'">
      <div class="assistant-body"><MarkdownView :content="record.content || ''" /></div>
    </template>

    <details v-else-if="record.kind === 'model.reasoning'" class="reasoning">
      <summary>思考过程</summary>
      <MarkdownView :content="record.content || ''" />
    </details>

    <ArtifactBlock v-else-if="artifact" :artifact="artifact" />
    <ApprovalBlock v-else-if="approval" :approval="approval" />
    <SubAgentBlock v-else-if="childRun" :run="childRun" />

    <div v-else-if="category === 'tool'" class="event-card tool-card">
      <div class="event-head">
        <span class="event-kind">{{ toolLabel }}</span>
        <strong>{{ stringData('name') || 'tool' }}</strong>
      </div>
      <details v-if="toolPayload" class="payload">
        <summary>详情</summary>
        <pre>{{ toolPayload }}</pre>
      </details>
    </div>

    <div v-else class="event-card" :class="category">
      <div class="event-head">
        <span class="event-kind">{{ eventLabel }}</span>
        <span v-if="record.content" class="event-content">{{ record.content }}</span>
      </div>
      <details v-if="showData" class="payload">
        <summary>详情</summary>
        <pre>{{ prettyData }}</pre>
      </details>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { AgentRun, Approval, Artifact, TimelineRecord } from '../stores/chat'
import { useChatStore } from '../stores/chat'
import ApprovalBlock from './ApprovalBlock.vue'
import ArtifactBlock from './ArtifactBlock.vue'
import MarkdownView from './MarkdownView.vue'
import SubAgentBlock from './SubAgentBlock.vue'

const props = defineProps<{ record: TimelineRecord }>()
const chatStore = useChatStore()
const data = computed(() => props.record.data || {})
const category = computed(() => {
  const kind = props.record.kind
  if (kind.startsWith('user.')) return 'user'
  if (kind === 'assistant.message') return 'assistant'
  if (kind.startsWith('tool.')) return 'tool'
  if (kind.startsWith('artifact.')) return 'artifact'
  if (kind.startsWith('approval.')) return 'approval'
  if (kind.startsWith('agent.')) return 'agent'
  if (kind.startsWith('checkpoint.')) return 'checkpoint'
  if (kind.startsWith('context.')) return 'context'
  if (kind.startsWith('runtime.')) return 'runtime'
  return 'model'
})
const artifact = computed<Artifact | null>(() => {
  if (!props.record.kind.startsWith('artifact.')) return null
  const id = String(data.value.artifact_id || '')
  return chatStore.runtimeState.artifacts.find(item => item.id === id) || null
})
const approval = computed<Approval | null>(() => {
  if (props.record.kind !== 'approval.requested') return null
  const id = String(data.value.id || data.value.approval_id || '')
  const callID = String(data.value.tool_call_id || '')
  return chatStore.runtimeState.approvals.find(item => item.id === id || (callID && item.tool_call_id === callID)) || null
})
const childRun = computed<AgentRun | null>(() => {
  if (props.record.kind !== 'agent.started') return null
  const id = String(data.value.agent_run_id || '')
  const run = chatStore.runtimeState.agent_runs.find(item => item.id === id)
  return run?.parent_run_id ? run : null
})
const visible = computed(() => {
  if (props.record.kind === 'artifact.version_created' || props.record.kind === 'artifact.version_activated') return !artifact.value
  if (props.record.kind === 'agent.completed' || props.record.kind === 'agent.failed') return false
  return true
})
const showData = computed(() => Object.keys(data.value).length > 0)
const prettyData = computed(() => JSON.stringify(data.value, null, 2))
const toolPayload = computed(() => {
  const value: Record<string, unknown> = {}
  if (data.value.arguments) value.arguments = data.value.arguments
  if (data.value.result) value.result = data.value.result
  return Object.keys(value).length ? JSON.stringify(value, null, 2) : ''
})
const toolLabels: Record<string, string> = {
  'tool.requested': 'Tool requested', 'tool.started': 'Tool running', 'tool.completed': 'Tool completed',
  'tool.failed': 'Tool failed', 'tool.rejected': 'Tool rejected', 'tool.cancelled': 'Tool cancelled',
}
const eventLabels: Record<string, string> = {
  'runtime.status': 'Runtime', 'runtime.command': 'Command', 'runtime.hint': 'Hint', 'runtime.error': 'Error',
  'checkpoint.created': 'Checkpoint', 'context.folded': 'Context folded', 'context.restored': 'Context restored',
  'approval.decided': 'Approval decision', 'agent.started': 'Agent',
}
const toolLabel = computed(() => toolLabels[props.record.kind] || props.record.kind)
const eventLabel = computed(() => eventLabels[props.record.kind] || props.record.kind)
function stringData(key: string) { const value = data.value[key]; return typeof value === 'string' ? value : '' }
</script>

<style scoped>
.timeline-item { margin-bottom: 14px; min-width: 0; }
.timeline-item.user { display: flex; justify-content: flex-end; }
.user-bubble { max-width: 82%; padding: 10px 14px; border-radius: 16px; background: var(--accent-soft); color: var(--text); white-space: pre-wrap; line-height: 1.55; }
.steering-label { display: inline-block; margin-right: 7px; color: var(--accent-text); font-size: 10px; font-weight: 700; text-transform: uppercase; letter-spacing: .07em; }
.assistant-body { width: 100%; padding: 2px 2px 4px; }
.reasoning { font-size: 13px; color: var(--text-2); padding: 8px 10px; border-left: 2px solid var(--border-strong); }
.reasoning summary { cursor: pointer; color: var(--accent-text); }
.reasoning :deep(.md) { margin-top: 7px; font-size: 13px; color: var(--text-2); }
.event-card { padding: 10px 12px; border: 1px solid var(--border); border-radius: 11px; background: var(--bg-soft); }
.event-card.tool-card { border-left: 3px solid var(--accent); }
.event-card.runtime, .event-card.context, .event-card.checkpoint { background: transparent; border-style: dashed; }
.event-head { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
.event-kind { color: var(--text-3); font-size: 11px; text-transform: uppercase; letter-spacing: .04em; flex-shrink: 0; }
.event-content { color: var(--text-2); font-size: 13px; white-space: pre-wrap; word-break: break-word; }
.payload { margin-top: 7px; color: var(--text-3); font-size: 11px; }
.payload summary { cursor: pointer; }
.payload pre { margin: 7px 0 0; padding: 8px; max-height: 260px; overflow: auto; background: var(--bg); border-radius: 8px; white-space: pre-wrap; word-break: break-word; color: var(--text-2); font-size: 11px; }
</style>
