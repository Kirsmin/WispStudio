<template>
  <div class="message-item" :class="message.type">
    <div class="message-content">
      <div v-if="message.type === 'assistant' && (message.reasoning || message.status === 'streaming')" class="reasoning-block">
        <button class="reasoning-toggle" @click="showReasoning = !showReasoning">
          <template v-if="message.status === 'streaming' && !message.content">思考中 <span class="thinking-spinner" /></template>
          <template v-else>思考 {{ showReasoning ? '▼' : '▶' }}</template>
        </button>
        <div v-show="showReasoning" class="reasoning-text">{{ message.reasoning || '思考中...' }}</div>
      </div>
      <div v-if="message.content" class="message-body"><MarkdownView :content="message.content" /></div>
      <div v-else-if="message.type === 'assistant' && message.status === 'streaming' && !message.reasoning" class="thinking-placeholder"><span class="thinking-spinner" /> 正在等待模型响应…</div>
      <div v-if="message.error" class="error-text">{{ message.error }}</div>
      <div v-if="message.type === 'assistant' && message.usage" class="meta">
        {{ message.model || 'unknown' }} · in {{ message.usage.prompt_tokens }} · think {{ message.usage.reasoning_tokens ?? 0 }} · out {{ message.usage.completion_tokens }} · cache {{ message.usage.cached_tokens ?? 0 }} · {{ ((message.duration_ms ?? 0) / 1000).toFixed(1) }}s<span v-if="message.ttft_ms"> · 首字 {{ message.ttft_ms }}ms</span>
      </div>
    </div>
  </div>
</template>
<script setup lang="ts">
import { ref, watch } from 'vue'
import type { ChatMessage } from '../stores/chat'
import MarkdownView from './MarkdownView.vue'
const props = defineProps<{ message: ChatMessage }>()
const showReasoning = ref(props.message.status === 'streaming')
watch(() => props.message.status, status => { if (status === 'streaming') showReasoning.value = true })
watch(() => props.message.content, content => { if (content && props.message.status === 'streaming') showReasoning.value = false })
</script>
<style scoped>
.message-item { display: flex; margin-bottom: 18px; }
.message-item.user { justify-content: flex-end; }
.message-item.assistant { justify-content: flex-start; }
.message-content { max-width: 760px; padding: 12px 16px; border-radius: 16px; word-break: break-word; min-width: 0; }
.message-item.user .message-content { background: var(--accent-soft); color: var(--text); max-width: min(82%, 680px); white-space: pre-wrap; }
.message-item.assistant .message-content { background: transparent; padding-left: 2px; padding-right: 2px; width: 100%; }
.reasoning-block { margin-bottom: 8px; }
.reasoning-toggle { border: 0; background: transparent; font: inherit; font-size: 12px; color: var(--accent-text); cursor: pointer; user-select: none; display: inline-flex; align-items: center; gap: 6px; padding: 2px 0; }
.reasoning-text { font-size: 13px; color: var(--text-2); margin-top: 4px; padding: 10px 12px; background: var(--bg-soft); border: 1px solid var(--border); border-radius: 10px; white-space: pre-wrap; max-height: 320px; overflow-y: auto; }
.thinking-placeholder { font-size: 13px; color: var(--text-2); padding: 8px 0; display: flex; align-items: center; gap: 6px; }
.thinking-spinner { display: inline-block; width: 12px; height: 12px; border: 2px solid var(--border-strong); border-top-color: var(--accent); border-radius: 50%; animation: spin .8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.message-body { min-height: 1em; }
.error-text { margin-top: 8px; padding: 8px 10px; border-radius: 8px; background: rgba(224, 69, 90, .07); color: var(--error); font-size: 12px; white-space: pre-wrap; }
.meta { font-size: 12px; color: var(--text-2); margin-top: 8px; font-variant-numeric: tabular-nums; }
</style>
