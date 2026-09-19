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
            <div class="empty-sub">Wisp 会先规划，再由你决定是否开始执行</div>
          </div>
          <template v-for="item in displayTimeline" :key="item.key">
            <ExecutionGroup v-if="item.type === 'execution'" :records="item.records" :complete="item.complete" />
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
import RuntimeControls from './RuntimeControls.vue'
import StreamingBlock from './StreamingBlock.vue'
import TimelineItem from './TimelineItem.vue'
import ExecutionGroup from './ExecutionGroup.vue'

const connectionStore = useConnectionStore()
const chatStore = useChatStore()
const sessionsStore = useSessionsStore()
const { isConnected } = storeToRefs(connectionStore)
const { timeline, streamingModel, backgroundGenerating } = storeToRefs(chatStore)
const { currentSessionId } = storeToRefs(sessionsStore)
const messagesRef = ref<HTMLDivElement | null>(null)
type DisplayTimelineItem =
  | { type: 'record'; key: string; record: TimelineRecord }
  | { type: 'execution'; key: string; records: TimelineRecord[]; complete: boolean }

// 主聊天只呈现用户、真实回复、Active Plan、待审批和折叠执行过程。
// reasoning / runtime / checkpoint / Context 等低层事件仅在调试抽屉读取。
const displayTimeline = computed<DisplayTimelineItem[]>(() => {
  const items: DisplayTimelineItem[] = []
  const toolModels = new Set(timeline.value.filter(record => record.kind === 'tool.requested').map(record => record.model_call_id))
  const terminal = new Set(chatStore.runtimeState.turns.filter(turn =>
    ['completed', 'failed', 'stopped', 'cancelled'].includes(turn.status)).map(turn => turn.id))
  let group: Extract<DisplayTimelineItem, { type: 'execution' }> | null = null
  for (const record of timeline.value) {
    const kind = record.kind
    if (kind === 'user.message' || kind === 'user.steering') {
      group = null
      items.push({ type: 'record', key: record.id, record })
      continue
    }
    if (kind.startsWith('tool.')) {
      if (!group || !group.key.startsWith(`execution:${record.turn_id || ''}:`)) {
        // 一个连续执行窗口只对应一个折叠卡片；不为 requested/started/result 分别创建节点。
        group = { type: 'execution', key: `execution:${record.turn_id || ''}:${record.seq}`, records: [], complete: false }
        items.push(group)
      }
      group.records.push(record)
      group.complete = terminal.has(record.turn_id || '')
      continue
    }
    if (kind === 'assistant.message') {
      if (!record.content?.trim() || toolModels.has(record.model_call_id)) continue
      group = null
      items.push({ type: 'record', key: record.id, record })
      continue
    }
    if (kind === 'artifact.version_created' && record.data?.type === 'plan') {
      const artifact = chatStore.runtimeState.artifacts.find(item => item.id === record.data?.artifact_id)
      if (artifact?.active_version === Number(record.data?.version)) {
        group = null
        items.push({ type: 'record', key: record.id, record })
      }
      continue
    }
    if (kind === 'approval.requested') {
      const approval = chatStore.runtimeState.approvals.find(item => item.id === record.data?.id)
      if (approval?.status === 'pending') {
        group = null
        items.push({ type: 'record', key: record.id, record })
      }
      continue
    }
    if (kind === 'runtime.error') {
      group = null
      items.push({ type: 'record', key: record.id, record })
    }
  }
  return items
})
let stickToBottom = true
let scrollFrame = 0

function openConnectDialog() { connectionStore.showConnectDialog = true }
function handleScroll() {
  const element = messagesRef.value; if (!element) return
  stickToBottom = element.scrollHeight - element.scrollTop - element.clientHeight < 120
}
async function scrollToBottomIfNeeded() {
  if (!stickToBottom) return
  await nextTick(); const element = messagesRef.value; if (element) element.scrollTop = element.scrollHeight
}
function queueScrollToBottom() {
  if (!stickToBottom) return
  if (scrollFrame) cancelAnimationFrame(scrollFrame)
  scrollFrame = requestAnimationFrame(() => { scrollFrame = 0; void scrollToBottomIfNeeded() })
}
watch(currentSessionId, () => { stickToBottom = true })
watch(() => [timeline.value.length, timeline.value[timeline.value.length - 1]?.seq || 0, streamingModel.value?.content.length || 0, streamingModel.value?.reasoning.length || 0], queueScrollToBottom, { flush: 'post' })
onBeforeUnmount(() => { if (scrollFrame) cancelAnimationFrame(scrollFrame) })
</script>

<style scoped>
.chat-area { flex: 1; display: flex; flex-direction: column; background: var(--bg); overflow: hidden; min-width: 0; }
.not-connected { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 12px; }
.welcome-icon { font-size: 32px; color: var(--accent); line-height: 1; }
.welcome-title { font-size: 26px; font-weight: 600; color: var(--text); }
.not-connected-text { font-size: 14px; color: var(--text-2); margin-bottom: 8px; }
.connect-btn { min-width: 132px; font-weight: 500; }
.empty-chat { padding: 72px 0 40px; text-align: center; }
.empty-title { font-size: 18px; font-weight: 600; color: var(--text); margin-bottom: 6px; }
.empty-sub { font-size: 13px; color: var(--text-2); }
.messages { flex: 1; overflow-y: auto; padding: 22px 20px 8px; overscroll-behavior: contain; scrollbar-gutter: stable; }
.messages-inner { max-width: 780px; margin: 0 auto; }
.messages-inner :deep(.timeline-item), .messages-inner :deep(.tool-block) { content-visibility: auto; contain-intrinsic-size: 90px; }
.background-note { margin: 6px 0 16px; color: var(--text-3); font-size: 12px; display: flex; align-items: center; gap: 7px; }
.background-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--accent); animation: pulse 1.2s ease-in-out infinite; }
@keyframes pulse { 0%,100% { opacity: .35; } 50% { opacity: 1; } }
</style>
