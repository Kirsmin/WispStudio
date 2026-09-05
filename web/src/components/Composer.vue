<template>
  <div class="composer">
    <div class="composer-card">
      <n-input
        v-model:value="inputText"
        type="textarea"
        :bordered="false"
        :autosize="{ minRows: 1, maxRows: 8 }"
        placeholder="向 Wisp 发送消息…"
        @keydown="handleKeydown"
      />
      <div class="composer-bar">
        <div class="bar-left">
          <n-select
            v-model:value="selectedModel"
            :options="modelOptions"
            size="small"
            class="pill-select"
            style="width: 160px"
          />
          <n-select
            v-model:value="selectedThinking"
            :options="thinkingOptions"
            size="small"
            class="pill-select"
            style="width: 104px"
          />
        </div>
        <button
          class="send-btn"
          :class="{ streaming: isStreaming }"
          :disabled="!isStreaming && (!inputText.trim() || !selectedModel)"
          :title="isStreaming ? '停止生成' : '发送 (Enter)'"
          @click="isStreaming ? stopStream() : sendMessage()"
        >
          <svg v-if="isStreaming" viewBox="0 0 16 16" width="14" height="14">
            <rect x="3.5" y="3.5" width="9" height="9" rx="2" fill="currentColor" />
          </svg>
          <svg v-else viewBox="0 0 16 16" width="14" height="14">
            <path d="M8 2.5v11M8 2.5L3.5 7M8 2.5L12.5 7" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" fill="none" />
          </svg>
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { NInput, NSelect } from 'naive-ui'
import { computed } from 'vue'
import { storeToRefs } from 'pinia'
import { modelThinkingLevels, useChatStore } from '../stores/chat'
import { useConnectionStore } from '../stores/connection'

const chatStore = useChatStore()
const connectionStore = useConnectionStore()
const { isStreaming, selectedModel, selectedThinking, inputText } = storeToRefs(chatStore)
const { models } = storeToRefs(connectionStore)

const modelOptions = computed(() =>
  models.value.map(model => ({ label: model.name, value: model.id }))
)

const thinkingLabels: Record<string, string> = {
  default: '默认',
  off: '关闭',
  on: '开启',
  none: '关闭',
  minimal: '最小',
  low: '低',
  medium: '中',
  high: '高',
  xhigh: '极高',
  max: '最大',
}

const thinkingOptions = computed(() => {
  const model = models.value.find(item => item.id === selectedModel.value)
  return modelThinkingLevels(model).map(level => ({
    label: thinkingLabels[level] || level,
    value: level,
  }))
})

function handleKeydown(event: KeyboardEvent) {
  if (event.key !== 'Enter' || event.shiftKey || event.isComposing) return
  event.preventDefault()
  if (!isStreaming.value) void chatStore.sendMessage()
}

function sendMessage() {
  void chatStore.sendMessage()
}

function stopStream() {
  chatStore.stopStream()
}
</script>

<style scoped>
.composer {
  padding: 10px 20px 16px;
  background: var(--bg);
}

.composer-card {
  max-width: 760px;
  margin: 0 auto;
  border: 1px solid var(--border-strong);
  border-radius: 18px;
  background: var(--bg);
  padding: 12px 14px 10px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.composer-card:focus-within {
  border-color: var(--accent);
}

.composer-card :deep(.n-input-wrapper) {
  padding: 0;
}

.composer-card :deep(.n-input__textarea) {
  font-size: 14px;
}

.composer-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.bar-left {
  display: flex;
  align-items: center;
  gap: 6px;
}

.pill-select :deep(.n-base-selection) {
  border-radius: 999px;
  font-size: 12px;
}

.send-btn {
  width: 34px;
  height: 34px;
  flex-shrink: 0;
  border: none;
  border-radius: 50%;
  background: var(--accent);
  color: #fff;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
}

.send-btn:hover {
  background: var(--accent-hover);
}

.send-btn:disabled {
  opacity: 0.35;
  cursor: not-allowed;
}

.send-btn.streaming {
  background: var(--accent-hover);
}
</style>
