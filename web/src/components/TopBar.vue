<template>
  <div class="top-bar">
    <div class="logo">
      <span class="logo-dot"></span>Wisp
    </div>
    <div class="top-actions">
      <n-tooltip trigger="hover" :delay="250">
        <template #trigger>
          <div class="agents-toggle" :class="{ disabled: agentsDisabled }">
            <span class="agents-label">AGENTS.md</span>
            <n-switch
              size="small"
              :value="sessions.injectAgents"
              :loading="updatingAgents"
              :disabled="agentsDisabled"
              @update:value="setAgentsInjection"
            />
          </div>
        </template>
        <span>{{ agentsHint }}</span>
      </n-tooltip>

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
import { NButton, NSwitch, NTooltip } from 'naive-ui'
import { useSessionsStore } from '../stores/sessions'
import { useChatStore } from '../stores/chat'
import { useConnectionStore } from '../stores/connection'
import ConnectDialog from './ConnectDialog.vue'

const emit = defineEmits<{ debug: [] }>()
const sessions = useSessionsStore()
const chat = useChatStore()
const connection = useConnectionStore()
const exporting = ref(false)
const updatingAgents = ref(false)
const hasSession = computed(() => Boolean(sessions.currentSessionId))
const agentsDisabled = computed(() => updatingAgents.value || !connection.isConnected || (hasSession.value && chat.executionActive))
const agentsHint = computed(() => {
  if (hasSession.value && chat.executionActive) return '当前正在执行，完成或暂停后可切换，避免同一轮上下文中途变化。'
  if (hasSession.value) return sessions.injectAgents
    ? '已启用：每次模型调用会注入工作区根目录 AGENTS.md。'
    : '已关闭：此 Session 不向模型注入 AGENTS.md。'
  return sessions.injectAgents
    ? '新 Session 默认注入工作区根目录 AGENTS.md。'
    : '新 Session 默认不注入 AGENTS.md。'
})

async function setAgentsInjection(enabled: boolean) {
  if (agentsDisabled.value) return
  updatingAgents.value = true
  try {
    await sessions.setAgentsInjection(enabled)
    window.$message?.success(enabled ? '已启用 AGENTS.md 注入' : '已关闭 AGENTS.md 注入')
  } catch (error) {
    window.$message?.error(error instanceof Error ? error.message : String(error))
  } finally {
    updatingAgents.value = false
  }
}

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
.agents-toggle {
  height: 30px;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 0 9px;
  margin-right: 2px;
  border: 1px solid var(--border);
  border-radius: 9px;
  background: var(--bg-soft);
  transition: border-color .16s ease, background .16s ease, opacity .16s ease;
}
.agents-toggle:not(.disabled):hover { border-color: var(--accent); background: var(--bg); }
.agents-toggle.disabled { opacity: .58; }
.agents-label { color: var(--text-2); font: 11px/1 ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; }
.connection-status { display: flex; align-items: center; gap: 8px; margin-left: 4px; }
@media (max-width: 760px) { .agents-label { display: none; } .agents-toggle { padding: 0 7px; } }
@media (max-width: 600px) { .top-bar { padding-left: 12px; padding-right: 8px; } .top-action { padding-left: 6px !important; padding-right: 6px !important; } }
</style>
