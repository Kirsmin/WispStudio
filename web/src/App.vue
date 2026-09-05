<template>
  <n-config-provider :theme-overrides="themeOverrides">
    <n-message-provider>
      <div class="app-layout">
        <TopBar />
        <div class="main-layout">
          <SessionList />
          <ChatArea />
        </div>
      </div>
    </n-message-provider>
  </n-config-provider>
</template>

<script setup lang="ts">
import { NConfigProvider, NMessageProvider } from 'naive-ui'
import { themeOverrides } from './theme'
import TopBar from './components/TopBar.vue'
import SessionList from './components/SessionList.vue'
import ChatArea from './components/ChatArea.vue'
</script>

<style scoped>
.app-layout {
  display: flex;
  flex-direction: column;
  height: 100vh;
  background: var(--bg);
}

.main-layout {
  display: flex;
  flex: 1;
  overflow: hidden;
}
</style>
