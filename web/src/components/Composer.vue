<template>
  <div class="composer">
    <div class="composer-inner">
      <n-select
        v-model:value="selectedModel"
        :options="modelOptions"
        size="small"
        style="width: 140px"
      />
      <n-select
        v-if="thinkingOptions.length > 0"
        v-model:value="selectedThinking"
        :options="thinkingOptions"
        size="small"
        style="width: 100px"
      />
      <n-input
        v-model:value="inputText"
        type="textarea"
        :autosize="{ minRows: 1, maxRows: 6 }"
        placeholder="输入消息... (Ctrl+Enter 发送)"
        @keydown="handleKeydown"
      />
      <n-button
        :type="isStreaming ? 'error' : 'primary'"
        @click="isStreaming ? stopStream() : sendMessage()"
      >
        {{ isStreaming ? '⏹' : '发送' }}
      </n-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { NInput, NButton, NSelect } from 'naive-ui'
import { computed, ref } from 'vue'
import { useChatStore } from '../stores/chat'
import { useConnectionStore } from '../stores/connection'
import { storeToRefs } from 'pinia'

const chatStore = useChatStore()
const connectionStore = useConnectionStore()
const { isStreaming, selectedModel, selectedThinking, inputText } = storeToRefs(chatStore)
const { models } = storeToRefs(connectionStore)

const modelOptions = computed(() =>
  models.value.map(m => ({ label: m.name, value: m.id }))
)

const thinkingOptions = computed(() => {
  const model = models.value.find(m => m.id === selectedModel.value)
  if (!model) return []
  return model.thinking_levels.map(l => ({ label: l, value: l }))
})

function handleKeydown(e: KeyboardEvent) {
  if (e.ctrlKey && e.key === 'Enter') {
    e.preventDefault()
    if (!isStreaming.value) sendMessage()
  }
}

function sendMessage() {
  chatStore.sendMessage()
}

function stopStream() {
  chatStore.stopStream()
}
</script>

<style scoped>
.composer {
  border-top: 1px solid var(--border);
  padding: 12px 20px;
  background: var(--bg);
}

.composer-inner {
  max-width: 760px;
  margin: 0 auto;
  display: flex;
  align-items: flex-end;
  gap: 8px;
}
</style>
