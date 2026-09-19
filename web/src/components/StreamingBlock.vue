<template>
  <div class="streaming-block">
    <div class="streaming-head">
      <span class="pulse" />
      <span>{{ title }}</span>
      <span v-if="stream.model" class="meta">{{ stream.model }}</span>
    </div>
    <div v-if="stream.content && stream.phase === 'answer'" class="answer"><MarkdownView :content="stream.content" /></div>
    <div v-else class="waiting">正在处理任务…</div>
    <div v-if="stream.error" class="error">{{ stream.error }}</div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { StreamingModel } from '../stores/chat'
import MarkdownView from './MarkdownView.vue'

const props = defineProps<{ stream: StreamingModel }>()
const title = computed(() => props.stream.profileId === 'build' ? '执行中' : props.stream.profileId === 'plan' ? '规划中' : '处理中')
</script>

<style scoped>
.streaming-block { margin: 0 0 18px; padding: 12px 14px; border: 1px solid var(--border); border-radius: 14px; background: var(--bg-soft); }
.streaming-head { display: flex; align-items: center; gap: 8px; color: var(--text-2); font-size: 12px; font-weight: 600; }
.pulse { width: 8px; height: 8px; border-radius: 50%; background: var(--accent); animation: pulse 1.2s ease-in-out infinite; }
.meta { margin-left: auto; color: var(--text-3); font-weight: 400; }
.answer { margin-top: 10px; }
.waiting { margin-top: 10px; color: var(--text-3); font-size: 13px; }
.error { margin-top: 10px; color: #b33f52; font-size: 13px; white-space: pre-wrap; }
@keyframes pulse { 0%,100% { opacity: .35; transform: scale(.8); } 50% { opacity: 1; transform: scale(1); } }
</style>
