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
            <ToolCallBlock v-if="item.type === 'tool'" :records="item.records" />
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
import { computed, nextTick, ref, watch } from 'vue'
import { useConnectionStore } from '../stores/connection'
import { useChatStore } from '../stores/chat'
import type { TimelineRecord } from '../stores/chat'
import { useSessionsStore } from '../stores/sessions'
import Composer from './Composer.vue'
import RuntimeControls from './RuntimeControls.vue'
import StreamingBlock from './StreamingBlock.vue'
import TimelineItem from './TimelineItem.vue'
import ToolCallBlock from './ToolCallBlock.vue'

const connectionStore = useConnectionStore()
const chatStore = useChatStore()
const sessionsStore = useSessionsStore()
const { isConnected } = storeToRefs(connectionStore)
const { timeline, streamingModel, backgroundGenerating } = storeToRefs(chatStore)
const { currentSessionId } = storeToRefs(sessionsStore)
const messagesRef = ref<HTMLDivElement | null>(null)
type DisplayTimelineItem =
  | { type: 'record'; key: string; record: TimelineRecord }
  | { type: 'tool'; key: string; records: TimelineRecord[] }

const displayTimeline = computed<DisplayTimelineItem[]>(() => {
  const items: DisplayTimelineItem[] = []
  const activeGroups = new Map<string, Extract<DisplayTimelineItem, { type: 'tool' }>>()
  for (const record of timeline.value) {
    const callID = record.kind.startsWith('tool.') ? String(record.data?.tool_call_id || '') : ''
    if (!callID) {
      items.push({ type: 'record', key: record.id, record })
      continue
    }
    let group = activeGroups.get(callID)
    if (!group || record.kind === 'tool.requested') {
      group = { type: 'tool', key: `tool:${callID}:${record.seq}`, records: [] }
      activeGroups.set(callID, group)
      items.push(group)
    }
    group.records.push(record)
    if (['tool.completed', 'tool.failed', 'tool.rejected', 'tool.cancelled'].includes(record.kind)) activeGroups.delete(callID)
  }
  return items
})
let stickToBottom = true

function openConnectDialog() { connectionStore.showConnectDialog = true }
function handleScroll() {
  const element = messagesRef.value; if (!element) return
  stickToBottom = element.scrollHeight - element.scrollTop - element.clientHeight < 120
}
async function scrollToBottomIfNeeded() {
  if (!stickToBottom) return
  await nextTick(); const element = messagesRef.value; if (element) element.scrollTop = element.scrollHeight
}
watch(currentSessionId, () => { stickToBottom = true })
watch([timeline, streamingModel], () => { void scrollToBottomIfNeeded() }, { deep: true, flush: 'post' })
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
.messages { flex: 1; overflow-y: auto; padding: 24px 20px 8px; }
.messages-inner { max-width: 840px; margin: 0 auto; }
.background-note { margin: 6px 0 16px; color: var(--text-3); font-size: 12px; display: flex; align-items: center; gap: 7px; }
.background-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--accent); animation: pulse 1.2s ease-in-out infinite; }
@keyframes pulse { 0%,100% { opacity: .35; } 50% { opacity: 1; } }
</style>
