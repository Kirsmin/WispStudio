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
      <div class="messages">
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
import { watch } from 'vue'
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

function openConnectDialog() {
  connectionStore.showConnectDialog = true
}

// 切换会话时加载消息
watch(currentSessionId, (id) => {
  if (id) {
    chatStore.loadMessages(id)
  } else {
    chatStore.messages = []
  }
})
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
