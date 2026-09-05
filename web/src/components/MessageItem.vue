<template>
  <div class="message-item" :class="message.type">
    <div class="message-content">
      <div v-if="message.reasoning" class="reasoning-block">
        <div class="reasoning-toggle" @click="showReasoning = !showReasoning">
          思考 {{ showReasoning ? '▼' : '▶' }}
        </div>
        <div v-show="showReasoning" class="reasoning-text">{{ message.reasoning }}</div>
      </div>
      <MarkdownView :content="message.content" />
      <div v-if="message.type === 'assistant' && message.usage" class="meta">
        {{ message.model || 'unknown' }} · in {{ message.usage.prompt_tokens }} · think {{ message.usage.reasoning_tokens ?? 0 }} · out {{ message.usage.completion_tokens }} · cache {{ message.usage.cached_tokens }} · {{ ((message.duration_ms ?? 0) / 1000).toFixed(1) }}s
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import MarkdownView from './MarkdownView.vue'

defineProps<{
  message: {
    id: string
    type: 'user' | 'assistant'
    content: string
    reasoning?: string
    model?: string
    usage?: {
      prompt_tokens: number
      completion_tokens: number
      cached_tokens: number
      reasoning_tokens: number
    }
    duration_ms?: number
  }
}>()

const showReasoning = ref(false)
</script>

<style scoped>
.message-item {
  display: flex;
  margin-bottom: 18px;
}

.message-item.user {
  justify-content: flex-end;
}

.message-item.assistant {
  justify-content: flex-start;
}

.message-content {
  max-width: 760px;
  padding: 12px 16px;
  border-radius: 16px;
  word-break: break-word;
}

.message-item.user .message-content {
  background: var(--accent-soft);
  color: var(--text);
}

.message-item.assistant .message-content {
  background: transparent;
  padding-left: 2px;
  padding-right: 2px;
}

.reasoning-block {
  margin-bottom: 8px;
}

.reasoning-toggle {
  font-size: 12px;
  color: var(--accent-text);
  cursor: pointer;
  user-select: none;
  display: inline-block;
  padding: 2px 0;
}

.reasoning-text {
  font-size: 13px;
  color: var(--text-2);
  margin-top: 4px;
  padding: 10px 12px;
  background: var(--bg-soft);
  border: 1px solid var(--border);
  border-radius: 10px;
  white-space: pre-wrap;
  max-height: 320px;
  overflow-y: auto;
}

.meta {
  font-size: 12px;
  color: var(--text-2);
  margin-top: 8px;
  font-variant-numeric: tabular-nums;
}
</style>
