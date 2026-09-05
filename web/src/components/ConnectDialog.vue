<script setup lang="ts">
import { ref, watch } from 'vue'
import { NButton, NInput, NModal } from 'naive-ui'
import { useConnectionStore } from '../stores/connection'
import { useChatStore } from '../stores/chat'
import { useSessionsStore } from '../stores/sessions'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ 'update:show': [value: boolean] }>()
const connection = useConnectionStore()
const chat = useChatStore()
const sessions = useSessionsStore()
const value = ref(connection.serverUrl)

watch(() => props.show, (show) => {
  if (show) value.value = connection.serverUrl
})

async function connect(): Promise<void> {
  const ok = await connection.connect(value.value)
  if (ok) {
    if (sessions.currentSessionId) await chat.openSession(sessions.currentSessionId)
    emit('update:show', false)
    window.$message?.success('已连接后端')
  }
}

function disconnect(): void {
  chat.newConversation()
  connection.disconnect()
  emit('update:show', false)
}
</script>

<template>
  <NModal
    :show="show"
    preset="card"
    title="后端连接"
    class="connect-modal"
    :mask-closable="!connection.isConnecting"
    @update:show="v => emit('update:show', Boolean(v))"
  >
    <div class="field-label">服务地址</div>
    <NInput v-model:value="value" placeholder="http://127.0.0.1:7860" :disabled="connection.isConnecting" @keyup.enter="connect" />
    <p class="help">可以填写本机或远程 WispStudio 后端地址。地址会保存在当前浏览器。</p>
    <div v-if="connection.lastError" class="error-text">{{ connection.lastError }}</div>
    <div v-if="connection.isConnected" class="connection-info">
      当前已连接 · {{ connection.models.length }} 个模型
      <span v-if="connection.latencyMs != null"> · {{ connection.latencyMs }}ms</span>
    </div>
    <div class="actions">
      <NButton v-if="connection.isConnected" tertiary @click="disconnect">断开</NButton>
      <span class="spacer"></span>
      <NButton @click="emit('update:show', false)">取消</NButton>
      <NButton type="primary" :loading="connection.isConnecting" @click="connect">连接</NButton>
    </div>
  </NModal>
</template>

<style scoped>
.connect-modal { width: min(470px, calc(100vw - 30px)); }
.field-label { font-size: 12px; color: #766b70; margin-bottom: 7px; }
.help { margin: 9px 2px 0; color: #9d9297; font-size: 12px; line-height: 1.55; }
.error-text { margin-top: 12px; padding: 9px 10px; border-radius: 8px; background: #fff0f0; color: #a33e45; font-size: 12px; }
.connection-info { margin-top: 12px; color: #6e8f73; font-size: 12px; }
.actions { display: flex; gap: 8px; margin-top: 20px; }
.spacer { flex: 1; }
</style>
