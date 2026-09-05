<template>
  <n-dialog-provider>
    <n-message-provider>
      <div class="app">
        <TopBar />
        <div class="main">
          <SessionList />
          <ChatArea />
        </div>
        <ConnectDialog />
      </div>
    </n-message-provider>
  </n-dialog-provider>
</template>

<script setup lang="ts">
import { NDialogProvider, NMessageProvider } from 'naive-ui'
import TopBar from './components/TopBar.vue'
import SessionList from './components/SessionList.vue'
import ChatArea from './components/ChatArea.vue'
import ConnectDialog from './components/ConnectDialog.vue'
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
