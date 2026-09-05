<template>
  <n-config-provider :theme-overrides="themeOverrides"><n-dialog-provider><n-message-provider><MessageRegister /><div class="app"><TopBar /><div class="main"><SessionList /><ChatArea /></div></div></n-message-provider></n-dialog-provider></n-config-provider>
</template>
<script setup lang="ts">
import { defineComponent, onMounted } from 'vue'
import { NConfigProvider, NDialogProvider, NMessageProvider, useMessage } from 'naive-ui'
import { themeOverrides } from './theme'
import { useConnectionStore } from './stores/connection'
import TopBar from './components/TopBar.vue'
import SessionList from './components/SessionList.vue'
import ChatArea from './components/ChatArea.vue'
const MessageRegister = defineComponent({ setup() { window.$message = useMessage(); return () => null } })
const connection = useConnectionStore()
onMounted(() => { if (localStorage.getItem('serverUrl')) void connection.connect(connection.serverUrl) })
</script>
<style scoped>.app { display: flex; flex-direction: column; height: 100vh; overflow: hidden; }.main { display: flex; flex: 1; overflow: hidden; }</style>
