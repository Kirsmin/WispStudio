<template>
  <div class="message-item" :class="message.type">
    <div class="message-content">
      <div v-if="message.reasoning || message.isThinking" class="reasoning-block">
        <div class="reasoning-toggle" @click="showReasoning = !showReasoning">
          <template v-if="message.isThinking">
            思考中 <span class="thinking-spinner"></span>
          </template>
          <template v-else>
            思考 {{ showReasoning ? '▼' : '▶' }}
            <span v-if="message.thinkingDuration" class="thinking-duration">
              {{ (message.thinkingDuration / 1000).toFixed(1) }}s
            </span>
          </template>
        </div>
        <div v-show="showReasoning || message.isThinking" class="reasoning-text">
          {{ message.reasoning || '思考中...' }}
        </div>
      </div>
      
      <div v-if="message.content || !message.isThinking" class="message-body">
        <MarkdownView :content="message.content" />
      </div>
      <div v-else-if="message.isThinking && !message.reasoning" class="thinking-placeholder">
        <span class="thinking-spinner"></span> 思考中...
      </div>
      
      <div v-if="message.type === 'assistant' && message.usage" class="meta">
        {{ message.model || 'unknown' }} · in {{ message.usage.prompt_tokens }} · think {{ message.usage.reasoning_tokens ?? 0 }} · out {{ message.usage.completion_tokens }} · cache {{ message.usage.cached_tokens }} · {{ ((message.duration_ms ?? 0) / 1000).toFixed(1) }}s
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import MarkdownView from './MarkdownView.vue'

const props = defineProps<{
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
    finish?: string
    isThinking?: boolean
    thinkingDuration?: number
  }
}>()

const showReasoning = ref(props.message.isThinking ?? false)

watch(() => props.message.isThinking, (isThinking) => {
  if (isThinking) {
    showReasoning.value = true
  }
})
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
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 2px 0;
}

.thinking-duration {
  color: var(--text-2);
  font-size: 11px;
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

.thinking-placeholder {
  font-size: 13px;
  color: var(--text-2);
  padding: 8px 0;
  display: flex;
  align-items: center;
  gap: 6px;
}

.thinking-spinner {
  display: inline-block;
  width: 12px;
  height: 12px;
  border: 2px solid var(--border-strong);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

.message-body {
  min-height: 1em;
}

.meta {
  font-size: 12px;
  color: var(--text-2);
  margin-top: 8px;
  font-variant-numeric: tabular-nums;
}
</style>
