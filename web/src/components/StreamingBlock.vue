<template>
  <div class="streaming-block">
    <div class="streaming-head">
      <span class="pulse" />
      <span>{{ title }}</span>
      <span v-if="stream.model" class="meta">{{ stream.model }}</span>
    </div>
    <details v-if="stream.reasoning" class="reasoning" :open="!stream.content">
      <summary>思考过程</summary>
      <MarkdownView :content="stream.reasoning" />
    </details>
    <div v-if="stream.content" class="answer"><MarkdownView :content="stream.content" /></div>
    <div v-else-if="!stream.reasoning" class="waiting">等待模型响应…</div>
    <div v-if="stream.error" class="error">{{ stream.error }}</div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { StreamingModel } from '../stores/chat'
import MarkdownView from './MarkdownView.vue'

const props = defineProps<{ stream: StreamingModel }>()
const title = computed(() => props.stream.profileId ? `${props.stream.profileId} · Model` : 'Model')
</script>

<style scoped>
.streaming-block { margin: 0 0 18px; padding: 12px 14px; border: 1px solid var(--border); border-radius: 14px; background: var(--bg-soft); }
.streaming-head { display: flex; align-items: center; gap: 8px; color: var(--text-2); font-size: 12px; font-weight: 600; }
.pulse { width: 8px; height: 8px; border-radius: 50%; background: var(--accent); animation: pulse 1.2s ease-in-out infinite; }
.meta { margin-left: auto; color: var(--text-3); font-weight: 400; }
.reasoning { margin-top: 10px; font-size: 13px; color: var(--text-2); }
.reasoning summary { cursor: pointer; color: var(--accent-text); }
.reasoning :deep(.md) { margin-top: 8px; color: var(--text-2); font-size: 13px; }
.answer { margin-top: 10px; }
.waiting { margin-top: 10px; color: var(--text-3); font-size: 13px; }
.error { margin-top: 10px; color: #b33f52; font-size: 13px; white-space: pre-wrap; }
@keyframes pulse { 0%,100% { opacity: .35; transform: scale(.8); } 50% { opacity: 1; transform: scale(1); } }
</style>
