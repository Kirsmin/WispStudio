<template>
  <div class="connect-entry">
    <div v-if="isConnected" class="status-pill" @click="handleDisconnectClick">
      <span class="dot"></span>
      <span class="latency" :style="{ color: latencyColor }">{{ latencyText }}</span>
    </div>
    <button v-else class="link-btn" @click="showConnectDialog = true">连接服务器</button>

    <n-modal v-model:show="showConnectDialog" title="连接服务器" preset="card" style="width: 380px">
      <n-input
        v-model:value="serverUrl"
        placeholder="http://127.0.0.1:7860"
        @keydown.enter="connect"
      />
      <template #footer>
        <div class="modal-footer">
          <n-button @click="showConnectDialog = false">取消</n-button>
          <n-button type="primary" @click="connect">连接</n-button>
        </div>
      </template>
    </n-modal>
  </div>
</template>

<script setup lang="ts">
import { NButton, NModal, NInput, useMessage } from 'naive-ui'
import { computed, ref } from 'vue'
import { useConnectionStore } from '../stores/connection'
import { storeToRefs } from 'pinia'

const connectionStore = useConnectionStore()
const { isConnected, latency, lastOkTime, showConnectDialog } = storeToRefs(connectionStore)
const { connect: doConnect, disconnect } = connectionStore

const message = useMessage()
const serverUrl = ref(localStorage.getItem('serverUrl') || 'http://127.0.0.1:7860')
const armed = ref(false)

const latencyText = computed(() => {
  if (Date.now() - lastOkTime.value > 10000) {
    return '无响应'
  }
  return latency.value > 0 ? `${latency.value}ms` : ''
})

const latencyColor = computed(() => {
  if (Date.now() - lastOkTime.value > 10000) return 'var(--error)'
  if (latency.value > 500) return 'var(--error)'
  if (latency.value > 150) return 'var(--warn)'
  return 'var(--text-2)'
})

async function connect() {
  localStorage.setItem('serverUrl', serverUrl.value)
  const ok = await doConnect(serverUrl.value)
  if (ok) {
    showConnectDialog.value = false
  } else {
    message.error('连接失败')
  }
}

function handleDisconnectClick() {
  if (!armed.value) {
    armed.value = true
    message.info('再次点击以断开连接')
    setTimeout(() => (armed.value = false), 3000)
  } else {
    armed.value = false
    disconnect()
  }
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
