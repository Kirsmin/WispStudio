<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { NConfigProvider, NDrawer, NDrawerContent, NMessageProvider } from 'naive-ui'
import ChatArea from './components/ChatArea.vue'
import ConnectDialog from './components/ConnectDialog.vue'
import MessageRegister from './components/MessageRegister.vue'
import SessionList from './components/SessionList.vue'
import TopBar from './components/TopBar.vue'
import { themeOverrides } from './theme'
import { useConnectionStore } from './stores/connection'
import { useChatStore } from './stores/chat'
import { useSessionsStore } from './stores/sessions'

const connection = useConnectionStore()
const chat = useChatStore()
const sessions = useSessionsStore()
const connectDialog = ref(false)
const mobileDrawer = ref(false)
let pingTimer: number | null = null

onMounted(async () => {
  const ok = await connection.connect(connection.serverUrl)
  if (!ok) connectDialog.value = true
  else if (sessions.currentSessionId) await chat.openSession(sessions.currentSessionId)
  pingTimer = window.setInterval(() => void connection.ping(), 15000)
})

onBeforeUnmount(() => {
  if (pingTimer != null) window.clearInterval(pingTimer)
})
</script>

<template>
  <NConfigProvider :theme-overrides="themeOverrides">
    <NMessageProvider>
      <MessageRegister />
      <div class="app-shell">
        <div class="desktop-sidebar"><SessionList /></div>
        <section class="workspace">
          <TopBar @menu="mobileDrawer = true" @connect="connectDialog = true" />
          <ChatArea />
        </section>
      </div>
      <NDrawer v-model:show="mobileDrawer" placement="left" :width="280">
        <NDrawerContent body-content-style="padding:0">
          <SessionList />
        </NDrawerContent>
      </NDrawer>
      <ConnectDialog v-model:show="connectDialog" />
    </NMessageProvider>
  </NConfigProvider>
</template>
