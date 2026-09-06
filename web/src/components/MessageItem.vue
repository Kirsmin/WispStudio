<template>
  <div class="message-item" :class="[message.type, { 'has-tool': hasToolEvent }]">
    <div class="message-content">
      <div v-if="message.type === 'user'" class="user-text">{{ message.content }}</div>
      <template v-else>
        <div v-if="showReasoningBlock" class="reasoning-block">
          <button class="reasoning-toggle" type="button" @click="manualToggleReasoning">
            <span>{{ reasoningLabel }}</span>
            <span v-if="message.phase === 'reasoning' && message.streaming" class="thinking-spinner" />
            <span v-else>{{ showReasoning ? '▼' : '▶' }}</span>
          </button>
          <div v-show="showReasoning" class="reasoning-text">
            <MarkdownView v-if="message.reasoning" :content="message.reasoning" />
            <span v-else>思考中…</span>
          </div>
        </div>

        <div v-if="message.content" class="answer-body">
          <MarkdownView :content="message.content" />
        </div>

        <div v-if="message.tools?.length" class="tool-list">
          <div v-for="tool in message.tools" :key="tool.id" class="tool-row" :class="tool.status">
            <span class="tool-icon" aria-hidden="true">{{ tool.status === 'detecting' ? '🔍' : tool.status === 'completed' ? '🪄' : '⚠️' }}</span>
            <span v-if="tool.status === 'detecting'">正在寻找工具</span>
            <span v-else-if="tool.status === 'completed'">使用了 {{ tool.name }}</span>
            <span v-else>工具 {{ tool.name || '未知' }} 调用失败</span>
          </div>
        </div>

        <div
          v-else-if="message.streaming && message.phase === 'waiting' && !message.reasoning && !message.content"
          class="waiting-row"
        >
          <span class="thinking-spinner" />
          <span>等待模型首个响应…</span>
        </div>

        <div v-if="message.error" class="error-text">{{ message.error }}</div>
        <div v-if="message.usage" class="meta">
          {{ message.model || 'unknown' }}
          · in {{ message.usage.prompt_tokens }}
          · think {{ message.usage.reasoning_tokens ?? 0 }}
          · out {{ message.usage.completion_tokens }}
          · cache {{ message.usage.cached_tokens ?? 0 }}
          · {{ ((message.duration_ms ?? 0) / 1000).toFixed(1) }}s
          <span v-if="message.ttft_ms"> · 首字 {{ message.ttft_ms }}ms</span>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { ChatMessage } from '../stores/chat'
import MarkdownView from './MarkdownView.vue'

const props = defineProps<{ message: ChatMessage }>()
const showReasoning = ref(false)
const manuallyChanged = ref(false)
const hasToolEvent = computed(() => props.message.type === 'assistant' && Boolean(props.message.tools?.length))

const showReasoningBlock = computed(() =>
  props.message.type === 'assistant' && Boolean(props.message.reasoning || props.message.phase === 'reasoning'),
)
const reasoningLabel = computed(() =>
  props.message.phase === 'reasoning' && props.message.streaming ? '思考中' : '思考',
)

function manualToggleReasoning() {
  manuallyChanged.value = true
  showReasoning.value = !showReasoning.value
}

watch(() => props.message.phase, (phase, previous) => {
  if (phase === 'reasoning') {
    manuallyChanged.value = false
    showReasoning.value = true
    return
  }
  if ((phase === 'answer' || phase === 'done') && previous === 'reasoning') {
    manuallyChanged.value = false
    showReasoning.value = false
    return
  }
  if (phase === 'error' && !manuallyChanged.value) showReasoning.value = false
}, { immediate: true })
</script>

<style scoped>
.message-item { display: flex; margin-bottom: 18px; }
.message-item.user { justify-content: flex-end; }
.message-item.assistant { justify-content: flex-start; }
.message-content { max-width: 760px; padding: 12px 16px; border-radius: 16px; word-break: break-word; min-width: 0; }
.message-item.user .message-content { background: var(--accent-soft); color: var(--text); padding: 10px 16px; }
.user-text { white-space: pre-wrap; line-height: 1.55; margin: 0; padding: 0; }
.message-item.assistant .message-content { background: transparent; padding-left: 2px; padding-right: 2px; width: 100%; }
.message-item.assistant.has-tool { margin-bottom: 4px; }
.message-item.assistant.has-tool .message-content { padding-bottom: 0; }
.reasoning-block { margin-bottom: 8px; }
.reasoning-toggle { border: 0; background: transparent; font: inherit; font-size: 12px; color: var(--accent-text); cursor: pointer; user-select: none; display: inline-flex; align-items: center; gap: 6px; padding: 2px 0; }
.reasoning-text { font-size: 13px; color: var(--text-2); margin-top: 4px; padding: 10px 12px; background: var(--bg-soft); border: 1px solid var(--border); border-radius: 10px; max-height: 320px; overflow-y: auto; }
.reasoning-text :deep(.md) { font-size: 13px; color: var(--text-2); }
.answer-body { min-height: 1em; }
.tool-list { display: flex; flex-direction: column; gap: 6px; margin: 6px 0 0; }
.tool-row { display: inline-flex; width: fit-content; align-items: center; gap: 7px; padding: 5px 9px; border: 1px solid var(--border); border-radius: 9px; color: var(--text-2); background: var(--bg-soft); font-size: 12px; line-height: 1.4; }
.tool-row.failed { color: #a84646; }
.tool-icon { width: 1.2em; text-align: center; }
.waiting-row { display: flex; align-items: center; gap: 7px; min-height: 24px; font-size: 13px; color: var(--text-2); }
.thinking-spinner { width: 11px; height: 11px; border: 1.5px solid currentColor; border-right-color: transparent; border-radius: 50%; display: inline-block; animation: spin .8s linear infinite; }
.error-text { margin-top: 8px; color: #b33f52; font-size: 13px; white-space: pre-wrap; }
.meta { margin-top: 8px; color: var(--text-3); font-size: 11px; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
