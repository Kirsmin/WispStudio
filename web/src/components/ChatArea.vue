<template>
  <div class="chat-area">
    <div v-if="!isConnected" class="not-connected">
      <div class="not-connected-text">未连接服务器</div>
      <n-button class="connect-btn" @click="showConnectDialog = true">连接</n-button>
    </div>
    <template v-else>
      <div class="messages">
        <div class="messages-inner">
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
const { isConnected, showConnectDialog } = storeToRefs(connectionStore)
const { messages } = storeToRefs(chatStore)
const { currentSessionId } = storeToRefs(sessionsStore)

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
}

.not-connected {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 16px;
}

.not-connected-text {
  font-size: 16px;
  color: var(--text-2);
}

.connect-btn {
  background: var(--accent);
  color: #3d1a4e;
  border: none;
}

.connect-btn:hover {
  background: var(--accent-hover);
}

.messages {
  flex: 1;
  overflow-y: auto;
  padding: 20px;
}

.messages-inner {
  max-width: 760px;
  margin: 0 auto;
}
</style>
