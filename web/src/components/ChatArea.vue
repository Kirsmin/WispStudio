<template>
  <div class="chat-area">
    <div v-if="!isConnected" class="not-connected">
      <div class="welcome-icon">✦</div>
      <div class="welcome-title">Wisp</div>
      <div class="not-connected-text">尚未连接服务器，连接后即可开始对话</div>
      <n-button class="connect-btn" type="primary" size="large" @click="openConnectDialog">
        连接服务器
      </n-button>
    </div>
    <template v-else>
      <div ref="messagesRef" class="messages" @scroll="handleScroll">
        <div class="messages-inner">
          <div v-if="messages.length === 0" class="empty-chat">
            <div class="empty-title">开始一段对话</div>
            <div class="empty-sub">消息将发送给当前选中的模型</div>
          </div>
          <MessageItem
            v-for="msg in messages"
            :key="msg.id"
            :message="msg"
          />
        </div>
      </div>
      <Composer />
    </template>
  </div>
</template>

<script setup lang="ts">
import { NButton } from 'naive-ui'
import { storeToRefs } from 'pinia'
import { nextTick, ref, watch } from 'vue'
import { useConnectionStore } from '../stores/connection'
import { useChatStore } from '../stores/chat'
import { useSessionsStore } from '../stores/sessions'
import MessageItem from './MessageItem.vue'
import Composer from './Composer.vue'

const connectionStore = useConnectionStore()
const chatStore = useChatStore()
const sessionsStore = useSessionsStore()
const { isConnected } = storeToRefs(connectionStore)
const { messages } = storeToRefs(chatStore)
const { currentSessionId } = storeToRefs(sessionsStore)

const messagesRef = ref<HTMLDivElement | null>(null)
let stickToBottom = true

function openConnectDialog() {
  connectionStore.showConnectDialog = true
}

function handleScroll() {
  const element = messagesRef.value
  if (!element) return
  const distance = element.scrollHeight - element.scrollTop - element.clientHeight
  stickToBottom = distance < 120
}

async function scrollToBottomIfNeeded() {
  if (!stickToBottom) return
  await nextTick()
  const element = messagesRef.value
  if (element) element.scrollTop = element.scrollHeight
}

// 这里只重置自动跟随滚动状态，不再自动 loadMessages。
// 会话读取由 chat.openSession() 显式负责；否则首条消息创建 session.id 时，
// 一个"空会话"的 loadMessages 请求会在流式过程中把临时 user/assistant 消息整个覆盖掉。
watch(currentSessionId, () => {
  stickToBottom = true
})

watch(messages, () => {
  void scrollToBottomIfNeeded()
}, { deep: true, flush: 'post' })
</script>

<style scoped>
.chat-area {
  flex: 1;
  display: flex;
  flex-direction: column;
  background: var(--bg);
  overflow: hidden;
  min-width: 0;
}

.not-connected {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 12px;
}

.welcome-icon {
  font-size: 32px;
  color: var(--accent);
  line-height: 1;
}

.welcome-title {
  font-size: 26px;
  font-weight: 600;
  color: var(--text);
}

.not-connected-text {
  font-size: 14px;
  color: var(--text-2);
  margin-bottom: 8px;
}

.connect-btn {
  min-width: 132px;
  font-weight: 500;
}

.empty-chat {
  padding: 72px 0 40px;
  text-align: center;
}

.empty-title {
  font-size: 18px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 6px;
}

.empty-sub {
  font-size: 13px;
  color: var(--text-2);
}

.messages {
  flex: 1;
  overflow-y: auto;
  padding: 24px 20px 8px;
}

.messages-inner {
  max-width: 760px;
  margin: 0 auto;
}
</style>
