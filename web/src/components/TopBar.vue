<template>
  <div class="top-bar">
    <div class="logo">
      <span class="logo-dot"></span>Wisp
    </div>
    <div class="top-actions">
      <n-button class="top-action" size="small" quaternary :disabled="!hasSession" :loading="exporting" @click="exportCurrentSession">
        导出会话
      </n-button>
      <n-button class="top-action debug-action" size="small" quaternary :disabled="!hasSession" @click="emit('debug')">
        <span class="debug-dot" />调试
      </n-button>
      <div class="connection-status"><ConnectDialog /></div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton } from 'naive-ui'
import { useSessionsStore } from '../stores/sessions'
import { useChatStore } from '../stores/chat'
import ConnectDialog from './ConnectDialog.vue'

const emit = defineEmits<{ debug: [] }>()
const sessions = useSessionsStore()
const chat = useChatStore()
const exporting = ref(false)
const hasSession = computed(() => Boolean(sessions.currentSessionId))

async function exportCurrentSession() {
  if (!hasSession.value || exporting.value) return
  exporting.value = true
  try {
    await chat.exportSession()
    window.$message?.success('Session 已导出')
  } catch (error) {
    window.$message?.error(error instanceof Error ? error.message : String(error))
  } finally {
    exporting.value = false
  }
}
</script>

<style scoped>
.top-bar {
  height: 52px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 16px 0 20px;
  border-bottom: 1px solid var(--border);
  background: rgba(255, 255, 255, .94);
  backdrop-filter: blur(10px);
  flex-shrink: 0;
  z-index: 10;
}
.logo { display: flex; align-items: center; gap: 8px; font-size: 17px; font-weight: 650; letter-spacing: .2px; color: var(--text); }
.logo-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--accent); }
.top-actions { display: flex; align-items: center; gap: 4px; }
.top-action { --n-font-size: 12px !important; }
.debug-action { color: var(--text-2); }
.debug-dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: var(--accent); margin-right: 6px; vertical-align: 1px; }
.connection-status { display: flex; align-items: center; gap: 8px; margin-left: 4px; }
@media (max-width: 600px) { .top-bar { padding-left: 12px; padding-right: 8px; } .top-action { padding-left: 6px !important; padding-right: 6px !important; } }
</style>
