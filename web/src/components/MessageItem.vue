<template>
  <div class="message-item" :class="message.type">
    <div class="message-content">
      <MarkdownView :content="message.content" />
      <div v-if="message.reasoning" class="reasoning-block">
        <div class="reasoning-toggle" @click="showReasoning = !showReasoning">
          思考 {{ showReasoning ? '▼' : '▶' }}
        </div>
        <div v-show="showReasoning" class="reasoning-text">{{ message.reasoning }}</div>
      </div>
      <div v-if="message.type === 'assistant' && message.usage" class="meta">
        {{ message.model || 'unknown' }} · in {{ message.usage.prompt_tokens }} · out {{ message.usage.completion_tokens }} · cache {{ message.usage.cached_tokens }} · {{ (message.duration_ms / 1000).toFixed(1) }}s
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
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
    }
    duration_ms?: number
  }
}>()

const showReasoning = ref(false)
</script>

<style scoped>
.message-item {
  display: flex;
  margin-bottom: 16px;
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
  border-radius: 8px;
}

.message-item.user .message-content {
  background: var(--accent-tint);
}

.message-item.assistant .message-content {
  background: transparent;
  padding-left: 0;
}

.reasoning-block {
  margin-top: 8px;
}

.reasoning-toggle {
  font-size: 12px;
  color: var(--text-2);
  font-style: italic;
  cursor: pointer;
  user-select: none;
}

.reasoning-text {
  font-size: 13px;
  color: var(--text-2);
  font-style: italic;
  margin-top: 4px;
  padding: 8px;
  background: #faf8fb;
  border-radius: 8px;
}

.meta {
  font-size: 12px;
  color: var(--text-2);
  margin-top: 8px;
}
</style>
