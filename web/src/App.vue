<template>
  <n-config-provider :theme-overrides="themeOverrides">
    <n-dialog-provider>
      <n-message-provider>
        <!-- 挂载全局消息实例到 window.$message -->
        <MessageRegister />
        <div class="app">
          <TopBar />
          <div class="main">
            <SessionList />
            <ChatArea />
          </div>
        </div>
      </n-message-provider>
    </n-dialog-provider>
  </n-config-provider>
</template>

<script setup lang="ts">
import { defineComponent } from 'vue'
import { NConfigProvider, NDialogProvider, NMessageProvider, useMessage } from 'naive-ui'
import { themeOverrides } from './theme'
import TopBar from './components/TopBar.vue'
import SessionList from './components/SessionList.vue'
import ChatArea from './components/ChatArea.vue'

// 在 NMessageProvider 内部注册 window.$message，供 store 等场景使用
const MessageRegister = defineComponent({
  setup() {
    window.$message = useMessage()
    return () => null
  },
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
