<template>
  <div>
    <n-button v-if="!isConnected" @click="showDialog = true">连接服务器</n-button>
    <div v-else class="connected" @click="handleDisconnectClick">
      <span class="dot"></span>
      <span class="latency" :style="{ color: latencyColor }">{{ latencyText }}</span>
    </div>

    <n-modal v-model:show="showDialog" title="连接服务器" preset="card" style="width: 400px">
      <n-input v-model:value="serverUrl" placeholder="http://127.0.0.1:7860" />
      <template #footer>
        <n-button type="primary" @click="connect">连接</n-button>
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
const { isConnected, latency, lastOkTime } = storeToRefs(connectionStore)
const { connect: doConnect, disconnect } = connectionStore

const message = useMessage()
const showDialog = ref(false)
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
    showDialog.value = false
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
.connected {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  padding: 4px 8px;
  border-radius: 8px;
}

.connected:hover {
  background: var(--accent-tint);
}

.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--dot);
}

.latency {
  font-size: 13px;
}
</style>
