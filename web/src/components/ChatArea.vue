<template>
  <div class="chat-area">
    <div v-if="!isConnected" class="not-connected">
      <div class="welcome-icon">✦</div>
      <div class="welcome-title">Wisp</div>
      <div class="not-connected-text">尚未连接服务器，连接后即可开始任务</div>
      <n-button class="connect-btn" type="primary" size="large" @click="openConnectDialog">连接服务器</n-button>
    </div>
    <template v-else>
      <div ref="messagesRef" class="messages" @scroll="handleScroll">
        <div class="messages-inner">
          <div v-if="timeline.length === 0 && !streamingModel" class="empty-chat">
            <div class="empty-title">开始一个任务</div>
            <div class="empty-sub">小任务直接规划，复杂任务才会展开代码库调查</div>
          </div>
          <template v-for="item in displayTimeline" :key="item.key">
            <ExecutionBlock v-if="item.type === 'execution'" :groups="item.groups" />
            <TimelineItem v-else :record="item.record" />
          </template>
          <StreamingBlock v-if="streamingModel" :stream="streamingModel" />
          <div v-if="backgroundGenerating && !streamingModel" class="background-note">
            <span class="background-dot" /> Runtime 正在后台继续执行；刷新或切换页面不会取消任务。
          </div>
        </div>
      </div>
      <RuntimeControls />
      <Composer />
    </template>
  </div>
</template>

<script setup lang="ts">
import { NButton } from 'naive-ui'
import { storeToRefs } from 'pinia'
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useConnectionStore } from '../stores/connection'
import { useChatStore } from '../stores/chat'
import type { TimelineRecord } from '../stores/chat'
import { useSessionsStore } from '../stores/sessions'
import Composer from './Composer.vue'
import ExecutionBlock from './ExecutionBlock.vue'
import RuntimeControls from './RuntimeControls.vue'
import StreamingBlock from './StreamingBlock.vue'
import TimelineItem from './TimelineItem.vue'

const connectionStore = useConnectionStore()
const chatStore = useChatStore()
const sessionsStore = useSessionsStore()
const { isConnected } = storeToRefs(connectionStore)
const { timeline, streamingModel, backgroundGenerating } = storeToRefs(chatStore)
const { currentSessionId } = storeToRefs(sessionsStore)
const messagesRef = ref<HTMLDivElement | null>(null)

type DisplayTimelineItem =
  | { type: 'record'; key: string; record: TimelineRecord }
  | { type: 'execution'; key: string; groups: TimelineRecord[][] }

function isInternal(record: TimelineRecord, toolModelCalls: Set<string>): boolean {
  const kind = record.kind
  if (kind === 'model.reasoning' || kind.startsWith('checkpoint.') || kind.startsWith('context.')) return true
  if (kind === 'runtime.status' || kind === 'runtime.command' || kind === 'approval.decided') return true
  if (kind.startsWith('agent.')) return true
  if (kind === 'artifact.created') {
    const type = String(record.data?.type || '')
    return type !== 'plan'
  }
  if (kind.startsWith('artifact.')) return true
  if (kind === 'assistant.message' && record.model_call_id && toolModelCalls.has(record.model_call_id) && !record.data?.runtime_generated) return true
  if (kind === 'approval.requested') {
    const id = String(record.data?.id || record.data?.approval_id || '')
    const callId = String(record.data?.tool_call_id || '')
    const approval = chatStore.runtimeState.approvals.find(item => item.id === id || item.tool_call_id === callId)
    return Boolean(approval && approval.status !== 'pending')
  }
  return false
}

const displayTimeline = computed<DisplayTimelineItem[]>(() => {
  const items: DisplayTimelineItem[] = []
  const toolModelCalls = new Set(timeline.value.filter(r => r.kind === 'tool.requested' && r.model_call_id).map(r => String(r.model_call_id)))
  let execution: Extract<DisplayTimelineItem, { type: 'execution' }> | null = null
  const toolGroups = new Map<string, TimelineRecord[]>()
  const flushExecution = () => {
    if (execution && execution.groups.length) items.push(execution)
    execution = null; toolGroups.clear()
  }
  for (const record of timeline.value) {
    if (record.kind.startsWith('tool.')) {
      const callID = String(record.data?.tool_call_id || '')
      if (!callID) continue
      if (!execution) execution = { type: 'execution', key: `execution:${record.turn_id || ''}:${record.seq}`, groups: [] }
      let group = toolGroups.get(callID)
      if (!group) { group = []; toolGroups.set(callID, group); execution.groups.push(group) }
      group.push(record)
      continue
    }
    if (isInternal(record, toolModelCalls)) continue
    flushExecution()
    items.push({ type: 'record', key: record.id, record })
  }
  flushExecution()
  return items
})

let stickToBottom = true
let scrollFrame = 0
function openConnectDialog() { connectionStore.showConnectDialog = true }
function handleScroll() { const element = messagesRef.value; if (element) stickToBottom = element.scrollHeight - element.scrollTop - element.clientHeight < 120 }
async function scrollToBottomIfNeeded() { if (!stickToBottom) return; await nextTick(); const element = messagesRef.value; if (element) element.scrollTop = element.scrollHeight }
function queueScrollToBottom() { if (!stickToBottom) return; if (scrollFrame) cancelAnimationFrame(scrollFrame); scrollFrame = requestAnimationFrame(() => { scrollFrame = 0; void scrollToBottomIfNeeded() }) }
watch(currentSessionId, () => { stickToBottom = true })
watch(() => [timeline.value.length, timeline.value[timeline.value.length - 1]?.seq || 0, streamingModel.value?.content.length || 0], queueScrollToBottom, { flush: 'post' })
onBeforeUnmount(() => { if (scrollFrame) cancelAnimationFrame(scrollFrame) })
</script>

<style scoped>
.chat-area { flex: 1; display: flex; flex-direction: column; background: var(--bg); overflow: hidden; min-width: 0; }
.not-connected { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 12px; }
.welcome-icon { font-size: 32px; color: var(--accent); line-height: 1; }.welcome-title { font-size: 26px; font-weight: 600; color: var(--text); }
.not-connected-text { font-size: 14px; color: var(--text-2); margin-bottom: 8px; }.connect-btn { min-width: 132px; font-weight: 500; }
.empty-chat { padding: 72px 0 40px; text-align: center; }.empty-title { font-size: 18px; font-weight: 600; color: var(--text); margin-bottom: 6px; }.empty-sub { font-size: 13px; color: var(--text-2); }
.messages { flex: 1; overflow-y: auto; padding: 22px 20px 8px; overscroll-behavior: contain; scrollbar-gutter: stable; }
.messages-inner { max-width: 780px; margin: 0 auto; }.messages-inner :deep(.timeline-item), .messages-inner :deep(.execution) { content-visibility: auto; contain-intrinsic-size: 90px; }
.background-note { margin: 6px 0 16px; color: var(--text-3); font-size: 12px; display: flex; align-items: center; gap: 7px; }.background-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--accent); animation: pulse 1.2s ease-in-out infinite; }
@keyframes pulse { 0%,100% { opacity: .35; } 50% { opacity: 1; } }
</style>
