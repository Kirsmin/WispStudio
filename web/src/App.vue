<template>
  <n-config-provider :theme-overrides="themeOverrides">
    <n-dialog-provider>
      <n-message-provider>
        <MessageRegister />
        <div class="app">
          <TopBar @debug="debugOpen = true" />
          <div class="main">
            <SessionList />
            <ChatArea />
          </div>
          <DebugPanel v-model:show="debugOpen" />
        </div>
      </n-message-provider>
    </n-dialog-provider>
  </n-config-provider>
</template>

<script setup lang="ts">
import { defineComponent, onMounted, ref } from 'vue'
import { NConfigProvider, NDialogProvider, NMessageProvider, useMessage } from 'naive-ui'
import { themeOverrides } from './theme'
import { useConnectionStore } from './stores/connection'
import { useSessionsStore } from './stores/sessions'
import { useChatStore } from './stores/chat'
import TopBar from './components/TopBar.vue'
import SessionList from './components/SessionList.vue'
import ChatArea from './components/ChatArea.vue'
import DebugPanel from './components/DebugPanel.vue'

const MessageRegister = defineComponent({
  setup() {
    window.$message = useMessage()
    return () => null
  },
})

const connection = useConnectionStore()
const sessions = useSessionsStore()
const chat = useChatStore()
const debugOpen = ref(false)

onMounted(async () => {
  // Go 托管时默认同源直连；Vite 开发模式也通过 /api 代理同样工作。
  if (!await connection.connect(connection.serverUrl)) return
  try {
    await sessions.loadSessions()
    if (sessions.currentSessionId) await chat.openSession(sessions.currentSessionId)
  } catch (error) {
    window.$message?.error(error instanceof Error ? error.message : String(error))
  }
})
</script>

<style scoped>
.app {
  display: flex;
  flex-direction: column;
  height: 100vh;
  overflow: hidden;
}

.main {
  display: flex;
  flex: 1;
  overflow: hidden;
}
</style>
