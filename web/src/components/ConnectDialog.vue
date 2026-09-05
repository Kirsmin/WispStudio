<template>
  <div class="connect-entry">
    <div v-if="isConnected" class="status-pill" @click="handleDisconnectClick">
      <span class="dot" :class="{ bad: !pingOk }"></span>
      <span class="latency" :style="{ color: latencyColor }">{{ latencyText }}</span>
    </div>
    <button v-else class="link-btn" @click="showConnectDialog = true">连接服务器</button>

    <n-modal v-model:show="showConnectDialog" title="连接服务器" preset="card" style="width: min(380px, calc(100vw - 28px))">
      <n-input
        v-model:value="serverUrlInput"
        placeholder="http://127.0.0.1:7860"
        @keydown.enter="connect"
      />
      <template #footer>
        <div class="modal-footer">
          <n-button @click="showConnectDialog = false">取消</n-button>
          <n-button type="primary" :loading="connecting" @click="connect">连接</n-button>
        </div>
      </template>
    </n-modal>
  </div>
</template>

<script setup lang="ts">
import { NButton, NModal, NInput, useMessage } from 'naive-ui'
import { computed, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useConnectionStore } from '../stores/connection'
import { useSessionsStore } from '../stores/sessions'
import { useChatStore } from '../stores/chat'

const connection = useConnectionStore()
const sessions = useSessionsStore()
const chat = useChatStore()
const { isConnected, latency, pingOk, showConnectDialog } = storeToRefs(connection)
const message = useMessage()
const serverUrlInput = ref(connection.serverUrl)
const armed = ref(false)
const connecting = ref(false)

const latencyText = computed(() => {
  if (!pingOk.value) return '无响应'
  return latency.value > 0 ? `${latency.value}ms` : '已连接'
})

const latencyColor = computed(() => {
  if (!pingOk.value || latency.value > 500) return 'var(--error)'
  if (latency.value > 150) return 'var(--warn)'
  return 'var(--text-2)'
})

async function connect(): Promise<void> {
  if (connecting.value) return
  connecting.value = true
  try {
    const ok = await connection.connect(serverUrlInput.value)
    if (!ok) {
      message.error('连接失败')
      return
    }
    await sessions.loadSessions()
    if (sessions.currentSessionId) await chat.openSession(sessions.currentSessionId)
    showConnectDialog.value = false
    if (connection.catalogError) message.warning(connection.catalogError)
  } catch (error) {
    message.error(error instanceof Error ? error.message : String(error))
  } finally {
    connecting.value = false
  }
}

function handleDisconnectClick(): void {
  if (!armed.value) {
    armed.value = true
    message.info('再次点击以断开连接')
    window.setTimeout(() => { armed.value = false }, 3000)
    return
  }
  armed.value = false
  connection.disconnect()
}
</script>

<style scoped>
.connect-entry {
  display: flex;
  align-items: center;
}

.status-pill {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  padding: 5px 12px;
  border-radius: 999px;
  border: 1px solid var(--border);
  background: var(--bg-soft);
}

.status-pill:hover {
  border-color: var(--accent-soft);
  background: var(--accent-tint);
}

.dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--dot);
}

.dot.bad {
  background: var(--error);
}

.latency {
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

.link-btn {
  border: none;
  background: none;
  padding: 5px 12px;
  border-radius: 999px;
  font-size: 13px;
  color: var(--accent-text);
  cursor: pointer;
}

.link-btn:hover {
  background: var(--accent-tint);
}

.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
