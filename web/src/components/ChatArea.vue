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
      <div ref="messagesRef" class="messages">
        <div class="messages-inner">
          <div v-if="messages.length === 0" class="empty-chat">
            <div class="empty-title">开始一段对话</div>
            <div class="empty-sub">消息将发送给当前选中的 Provider 与模型</div>
          </div>
          <div v-if="notice" class="notice">{{ notice }}</div>
          <MessageItem
            v-for="message in messages"
            :key="message.id"
            :message="message"
          />
        </div>
      </div>
      <Composer />
    </template>
  </div>
</template>

<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { NButton } from 'naive-ui'
import { storeToRefs } from 'pinia'
import { useConnectionStore } from '../stores/connection'
import { useChatStore } from '../stores/chat'
import MessageItem from './MessageItem.vue'
import Composer from './Composer.vue'

const connection = useConnectionStore()
const chat = useChatStore()
const { isConnected } = storeToRefs(connection)
const { messages, notice } = storeToRefs(chat)
const messagesRef = ref<HTMLDivElement | null>(null)

function openConnectDialog(): void {
  connection.showConnectDialog = true
}

watch(messages, async () => {
  await nextTick()
  const element = messagesRef.value
  if (!element) return
  element.scrollTop = element.scrollHeight
}, { deep: true })
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

.notice {
  margin: 0 0 14px;
  padding: 8px 12px;
  border-radius: 10px;
  border: 1px solid var(--border);
  background: var(--accent-tint);
  color: var(--accent-text);
  font-size: 12px;
}

@media (max-width: 640px) {
  .messages { padding-left: 12px; padding-right: 12px; }
}
</style>
