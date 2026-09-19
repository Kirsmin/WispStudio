<template>
  <div class="timeline-item" :class="category">
    <div v-if="category === 'user'" class="user-bubble">
      <span v-if="record.kind === 'user.steering'" class="steering-label">追加要求</span>
      {{ record.content }}
    </div>
    <div v-else-if="category === 'assistant'" class="assistant-body">
      <MarkdownView :content="record.content || ''" />
    </div>
    <details v-else-if="artifact" class="plan-fold">
      <summary>当前方案 <span class="version">V{{ artifact.active_version }}</span><span class="fold-hint">展开 / 收起</span></summary>
      <ArtifactBlock :artifact="artifact" />
    </details>
    <ApprovalBlock v-else-if="approval" :approval="approval" />
    <div v-else-if="record.kind === 'runtime.error'" class="runtime-error">{{ record.content || '执行失败，请打开调试查看详情。' }}</div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { Approval, Artifact, TimelineRecord } from '../stores/chat'
import { useChatStore } from '../stores/chat'
import ApprovalBlock from './ApprovalBlock.vue'
import ArtifactBlock from './ArtifactBlock.vue'
import MarkdownView from './MarkdownView.vue'

const props = defineProps<{ record: TimelineRecord }>()
const chat = useChatStore()
const category = computed(() => props.record.kind.startsWith('user.') ? 'user' : props.record.kind === 'assistant.message' ? 'assistant' : 'event')
const artifact = computed<Artifact | undefined>(() => chat.runtimeState.artifacts.find(item => item.id === props.record.data?.artifact_id && item.active_version === Number(props.record.data?.version)))
const approval = computed<Approval | undefined>(() => chat.runtimeState.approvals.find(item => item.id === props.record.data?.id && item.status === 'pending'))
</script>

<style scoped>
.timeline-item { margin-bottom: 15px; min-width: 0; }
.timeline-item.user { display: flex; justify-content: flex-end; }
.user-bubble { max-width: 84%; padding: 11px 15px; border-radius: 16px; background: var(--accent-soft); color: var(--text); white-space: pre-wrap; line-height: 1.6; overflow-wrap: anywhere; }
.steering-label { display: inline-block; margin-right: 7px; color: var(--accent-text); font-size: 11px; font-weight: 650; }
.assistant-body { width: 100%; padding: 2px 2px 4px; }
.plan-fold { border: 1px solid var(--border); border-radius: 12px; overflow: hidden; }
.plan-fold > summary { padding: 11px 14px; cursor: pointer; display: flex; gap: 9px; align-items: center; font-size: 13px; font-weight: 650; list-style: none; }
.plan-fold > summary::-webkit-details-marker { display: none; }
.plan-fold > summary::before { content: '›'; color: var(--accent-text); font-size: 17px; }
.plan-fold[open] > summary::before { transform: rotate(90deg); }
.version { font-weight: 500; font-size: 11px; color: var(--accent-text); }
.fold-hint { margin-left: auto; color: var(--text-3); font-size: 11px; font-weight: 400; }
.plan-fold :deep(.artifact-block) { margin: 0 9px 9px; }
.runtime-error { padding: 12px 14px; border: 1px solid var(--error); border-radius: 10px; color: var(--error); font-size: 13px; }
</style>
