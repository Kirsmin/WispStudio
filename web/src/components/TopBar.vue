<script setup lang="ts">
import { NButton, NTooltip } from 'naive-ui'
import { useConnectionStore } from '../stores/connection'

const emit = defineEmits<{ menu: []; connect: [] }>()
const connection = useConnectionStore()

async function refresh(): Promise<void> {
  try {
    await connection.refreshModels()
    window.$message?.success('模型列表已刷新')
  } catch (error) {
    window.$message?.error(error instanceof Error ? error.message : String(error))
  }
}
</script>

<template>
  <header class="topbar">
    <NButton quaternary circle class="mobile-menu" aria-label="打开会话列表" @click="emit('menu')">
      <span aria-hidden="true">☰</span>
    </NButton>
    <div class="top-title">WispStudio</div>
    <div class="top-actions">
      <NTooltip v-if="connection.isConnected" trigger="hover">
        <template #trigger>
          <NButton quaternary circle size="small" aria-label="刷新模型" @click="refresh">
            <span aria-hidden="true">↻</span>
          </NButton>
        </template>
        刷新模型列表
      </NTooltip>
      <button class="status-pill" :class="{ online: connection.isConnected }" @click="emit('connect')">
        <span class="status-dot"></span>
        <span v-if="connection.isConnected">已连接 {{ connection.displayURL }}</span>
        <span v-else>连接后端</span>
        <small v-if="connection.latencyMs != null">{{ connection.latencyMs }}ms</small>
      </button>
    </div>
  </header>
</template>

<style scoped>
.topbar {
  height: 52px; min-height: 52px; display: flex; align-items: center; gap: 9px; padding: 0 14px;
  border-bottom: 1px solid #f0eaed; background: rgba(255,255,255,.92); backdrop-filter: blur(10px);
}
.top-title { color: #45393e; font-weight: 650; font-size: 13px; }
.top-actions { margin-left: auto; display: flex; align-items: center; gap: 5px; }
.status-pill {
  display: flex; align-items: center; gap: 7px; max-width: 330px; padding: 6px 9px;
  border: 1px solid #eadfe4; border-radius: 999px; background: #fff; color: #74686d; cursor: pointer; font-size: 11px;
}
.status-pill small { color: #aaa0a4; }
.status-dot { width: 7px; height: 7px; border-radius: 50%; background: #c8bdc1; }
.status-pill.online .status-dot { background: #60a86b; box-shadow: 0 0 0 3px rgba(96,168,107,.12); }
.mobile-menu { display: none; }
@media (max-width: 760px) {
  .mobile-menu { display: inline-flex; }
  .top-title { display: none; }
  .status-pill { max-width: 72vw; }
  .status-pill span:nth-child(2) { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
}
</style>
