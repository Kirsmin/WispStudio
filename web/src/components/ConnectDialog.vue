<template>
  <div class="connect-entry">
    <div v-if="isConnected" class="status-pill" @click="handleDisconnectClick"><span class="dot" :class="{ bad: !pingOk }" /><span class="status-text">{{ statusText }}</span></div>
    <button v-else class="link-btn" @click="showConnectDialog = true">连接服务器</button>
    <n-modal v-model:show="showConnectDialog" title="连接服务器" preset="card" style="width: min(380px, calc(100vw - 28px))"><n-input v-model:value="serverUrlInput" placeholder="http://127.0.0.1:7860" @keydown.enter="connect" /><template #footer><div class="modal-footer"><n-button @click="showConnectDialog = false">取消</n-button><n-button type="primary" :loading="connecting" @click="connect">连接</n-button></div></template></n-modal>
  </div>
</template>
<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NInput, NModal, useMessage } from 'naive-ui'
import { storeToRefs } from 'pinia'
import { useConnectionStore } from '../stores/connection'
const connection = useConnectionStore(); const { isConnected, pingOk, showConnectDialog } = storeToRefs(connection)
const message = useMessage(); const serverUrlInput = ref(connection.serverUrl); const armed = ref(false); const connecting = ref(false)
const statusText = computed(() => pingOk.value ? '已连接' : '无响应')
async function connect(): Promise<void> { connecting.value = true; try { if (await connection.connect(serverUrlInput.value)) showConnectDialog.value = false; else message.error('连接失败') } finally { connecting.value = false } }
function handleDisconnectClick(): void { if (!armed.value) { armed.value = true; message.info('再次点击以断开连接'); window.setTimeout(() => { armed.value = false }, 3000) } else { armed.value = false; connection.disconnect() } }
</script>
<style scoped>
.connect-entry { display: flex; align-items: center; }.status-pill { display: flex; align-items: center; gap: 8px; cursor: pointer; padding: 5px 12px; border-radius: 999px; border: 1px solid var(--border); background: var(--bg-soft); }.status-pill:hover { border-color: var(--accent-soft); background: var(--accent-tint); }.dot { width: 7px; height: 7px; border-radius: 50%; background: var(--dot); }.dot.bad { background: var(--error); }.status-text { font-size: 12px; color: var(--text-2); font-variant-numeric: tabular-nums; }.link-btn { border: none; background: none; padding: 5px 12px; border-radius: 999px; font-size: 13px; color: var(--accent-text); cursor: pointer; }.link-btn:hover { background: var(--accent-tint); }.modal-footer { display: flex; justify-content: flex-end; gap: 8px; }
</style>
