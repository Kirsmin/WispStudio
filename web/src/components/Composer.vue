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
            v-model:value="selectedProvider"
            :options="providerOptions"
            :disabled="isBusy || providerOptions.length === 0"
            size="small"
            class="pill-select provider-select"
          />
          <n-select
            v-model:value="selectedModel"
            :options="modelOptions"
            :disabled="isBusy || modelOptions.length === 0"
            size="small"
            class="pill-select model-select"
          />
          <n-select
            v-model:value="selectedThinking"
            :options="thinkingOptions"
            :disabled="isBusy || thinkingOptions.length === 0"
            size="small"
            class="pill-select thinking-select"
          />
        </div>
        <button
          class="send-btn"
          :class="{ streaming: isBusy }"
          :disabled="!isBusy && (!inputText.trim() || !selectedProvider || !selectedModel)"
          :title="isBusy ? '停止生成' : '发送 (Enter)'"
          @click="isBusy ? stopGeneration() : sendMessage()"
        >
          <svg v-if="isBusy" viewBox="0 0 16 16" width="14" height="14">
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
import { computed } from 'vue'
import { NInput, NSelect } from 'naive-ui'
import { storeToRefs } from 'pinia'
import { useChatStore } from '../stores/chat'
import { useConnectionStore } from '../stores/connection'

const chat = useChatStore()
const connection = useConnectionStore()
const {
  inputText,
  selectedProvider,
  selectedModel,
  selectedThinking,
  modelsForProvider,
  thinkingLevels,
  isBusy,
} = storeToRefs(chat)
const { providers } = storeToRefs(connection)

const providerOptions = computed(() => providers.value.map(provider => ({
  label: provider.name,
  value: provider.id,
  disabled: !provider.available && provider.models_count === 0,
})))

const modelOptions = computed(() => modelsForProvider.value.map(model => ({
  label: model.name,
  value: model.id,
})))

const thinkingLabels: Record<string, string> = {
  off: '关闭',
  none: '关闭',
  on: '开启',
  auto: '自动',
  minimal: '最小',
  low: '低',
  medium: '中',
  high: '高',
}

const thinkingOptions = computed(() => thinkingLevels.value.map(level => ({
  label: thinkingLabels[level] || level,
  value: level,
})))

function handleKeydown(event: KeyboardEvent): void {
  if (event.key !== 'Enter' || event.shiftKey || event.isComposing) return
  event.preventDefault()
  if (!isBusy.value) void chat.sendMessage()
}

function sendMessage(): void {
  void chat.sendMessage()
}

function stopGeneration(): void {
  void chat.stopGeneration()
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
  min-width: 0;
}

.provider-select { width: 116px; }
.model-select { width: 160px; }
.thinking-select { width: 92px; }

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

@media (max-width: 720px) {
  .provider-select { width: 96px; }
  .model-select { width: 128px; }
  .thinking-select { width: 78px; }
}

@media (max-width: 560px) {
  .composer { padding-left: 10px; padding-right: 10px; }
  .bar-left { gap: 4px; }
  .provider-select { width: 82px; }
  .model-select { width: 108px; }
  .thinking-select { width: 72px; }
}
</style>
