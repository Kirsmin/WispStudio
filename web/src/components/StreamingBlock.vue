<template>
  <div class="streaming-block">
    <div class="streaming-head">
      <span class="pulse" />
      <span>{{ title }}</span>
      <span v-if="stream.model" class="meta">{{ stream.model }}</span>
    </div>
    <div v-if="stream.toolNames.length" class="tooling">
      <span v-for="name in stream.toolNames" :key="name" class="tool-chip">{{ toolLabel(name) }}</span>
    </div>
    <div v-if="showContent" class="answer"><MarkdownView :content="stream.content" /></div>
    <div v-else class="waiting">{{ waitingText }}</div>
    <div v-if="stream.error" class="error">{{ stream.error }}</div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { StreamingModel } from '../stores/chat'
import MarkdownView from './MarkdownView.vue'

const props = defineProps<{ stream: StreamingModel }>()
const showContent = computed(() => Boolean(props.stream.content) && props.stream.toolNames.length === 0)
const title = computed(() => props.stream.profileId === 'build' ? 'Build 正在执行' : props.stream.profileId === 'plan' ? '正在整理计划' : '正在处理')
const waitingText = computed(() => props.stream.toolNames.length ? '正在调用工具，详细过程已折叠到执行过程。' : props.stream.reasoning ? '正在分析…' : '等待模型响应…')
const labels: Record<string, string> = { list_files:'列出文件', read_file:'读取文件', search_text:'搜索代码', write_file:'写入文件', run_command:'运行命令', new_plan:'保存计划', edit_plan:'更新计划', spawn_explorer:'定向调查', done_phase:'完成阶段' }
function toolLabel(name: string): string { return labels[name] || name }
</script>

<style scoped>
.streaming-block { margin: 0 0 18px; padding: 11px 13px; border: 1px solid var(--border); border-radius: 14px; background: var(--bg-soft); }
.streaming-head { display: flex; align-items: center; gap: 8px; color: var(--text-2); font-size: 12px; font-weight: 600; }.pulse { width: 8px; height: 8px; border-radius: 50%; background: var(--accent); animation: pulse 1.2s ease-in-out infinite; }.meta { margin-left: auto; color: var(--text-3); font-weight: 400; }
.tooling { margin-top: 9px; display: flex; flex-wrap: wrap; gap: 6px; }.tool-chip { padding: 3px 7px; border-radius: 999px; background: var(--bg); border: 1px solid var(--border); color: var(--text-2); font-size: 10px; }
.answer { margin-top: 10px; }.waiting { margin-top: 9px; color: var(--text-3); font-size: 12px; }.error { margin-top: 10px; color: #b33f52; font-size: 13px; white-space: pre-wrap; }
@keyframes pulse { 0%,100% { opacity: .35; transform: scale(.8); } 50% { opacity: 1; transform: scale(1); } }
</style>
