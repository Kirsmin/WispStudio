<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NCollapseTransition, NTag } from 'naive-ui'
import MarkdownView from './MarkdownView.vue'
import type { ChatMessage } from '../stores/chat'

const props = defineProps<{ message: ChatMessage }>()
const reasoningOpen = ref(false)

const isAssistant = computed(() => props.message.type === 'assistant')
const hasReasoning = computed(() => Boolean(props.message.reasoning))
const reasoningLabel = computed(() => {
  if (props.message.status === 'streaming' && !props.message.content) return '思考中'
  return '思考过程'
})
const metaText = computed(() => {
  const chunks: string[] = []
  if (props.message.ttft_ms) chunks.push(`首字 ${props.message.ttft_ms}ms`)
  if (props.message.duration_ms) chunks.push(`总耗时 ${(props.message.duration_ms / 1000).toFixed(1)}s`)
  if (props.message.usage?.completion_tokens) chunks.push(`${props.message.usage.completion_tokens} tokens`)
  return chunks.join(' · ')
})
const emptyFinalHint = computed(() => {
  if (!isAssistant.value || props.message.content || props.message.status === 'streaming') return ''
  if (props.message.status === 'aborted') return '生成已停止。'
  if (props.message.status === 'background') return '浏览器连接已断开，服务端仍在继续生成。'
  if (props.message.error) return props.message.error
  if (props.message.finish === 'length' || props.message.finish === 'max_tokens') {
    return '上游在生成最终答案前触发了输出长度限制；推理内容可能存在，但最终正文没有完整返回。'
  }
  if (props.message.reasoning) return '模型返回了推理内容，但没有返回最终正文。'
  return '模型没有返回可展示的正文。'
})
</script>

<template>
  <article class="message" :class="message.type">
    <div class="avatar" aria-hidden="true">{{ message.type === 'user' ? '你' : 'W' }}</div>
    <div class="message-main">
      <div v-if="message.type === 'user'" class="user-text">{{ message.content }}</div>
      <template v-else>
        <div v-if="hasReasoning || message.status === 'streaming'" class="reasoning-shell">
          <NButton text size="small" class="reasoning-toggle" @click="reasoningOpen = !reasoningOpen">
            <span class="chevron">{{ reasoningOpen ? '▾' : '▸' }}</span>
            {{ reasoningLabel }}
            <span v-if="hasReasoning" class="reasoning-size">{{ message.reasoning?.length.toLocaleString() }} 字符</span>
          </NButton>
          <NCollapseTransition :show="reasoningOpen">
            <div class="reasoning-content">{{ message.reasoning || '正在等待模型返回推理内容…' }}</div>
          </NCollapseTransition>
        </div>

        <MarkdownView v-if="message.content" :content="message.content" />
        <div v-else-if="emptyFinalHint" class="empty-final" :class="{ error: message.status === 'error' }">
          {{ emptyFinalHint }}
        </div>

        <div v-if="message.status === 'streaming'" class="streaming-indicator" aria-label="正在生成">
          <i></i><i></i><i></i>
        </div>
      </template>

      <div v-if="isAssistant && (metaText || message.status === 'aborted' || message.status === 'error')" class="message-meta">
        <span v-if="metaText">{{ metaText }}</span>
        <NTag v-if="message.status === 'aborted'" size="small" :bordered="false">已停止</NTag>
        <NTag v-if="message.status === 'error'" size="small" type="error" :bordered="false">异常结束</NTag>
      </div>
    </div>
  </article>
</template>

<style scoped>
.message {
  display: grid;
  grid-template-columns: 34px minmax(0, 1fr);
  gap: 12px;
  width: min(860px, calc(100% - 32px));
  margin: 0 auto;
  padding: 16px 0;
}

.avatar {
  width: 32px;
  height: 32px;
  display: grid;
  place-items: center;
  border-radius: 10px;
  font-size: 12px;
  font-weight: 700;
  background: #f3e8ed;
  color: #8c536a;
}

.assistant .avatar {
  background: #d95f8d;
  color: white;
}

.message-main { min-width: 0; }
.user-text {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  line-height: 1.72;
  padding-top: 5px;
}

.reasoning-shell {
  margin: 1px 0 10px;
  border-left: 2px solid #e4c8d3;
  padding-left: 10px;
}

.reasoning-toggle { color: #7c6d73; }
.reasoning-size { margin-left: 7px; color: #aaa0a4; font-size: 11px; }
.reasoning-content {
  max-height: min(45vh, 520px);
  overflow: auto;
  margin-top: 8px;
  padding: 10px 12px;
  border-radius: 8px;
  background: #faf7f8;
  color: #71676b;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  line-height: 1.62;
  font-size: 13px;
}

.empty-final {
  margin-top: 8px;
  padding: 10px 12px;
  border-radius: 9px;
  background: #faf7f8;
  color: #756a6f;
}
.empty-final.error { background: #fff1f1; color: #a53f45; }

.streaming-indicator { display: flex; gap: 4px; padding-top: 10px; }
.streaming-indicator i {
  width: 5px; height: 5px; border-radius: 50%; background: #d95f8d;
  animation: pulse 1.1s infinite ease-in-out;
}
.streaming-indicator i:nth-child(2) { animation-delay: .14s; }
.streaming-indicator i:nth-child(3) { animation-delay: .28s; }
@keyframes pulse { 0%, 70%, 100% { opacity: .25; transform: translateY(0); } 35% { opacity: 1; transform: translateY(-2px); } }

.message-meta {
  display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
  margin-top: 10px; color: #a0979b; font-size: 11px;
}
</style>
