<template>
  <div class="markdown-view md" v-html="renderedHtml"></div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import MarkdownIt from 'markdown-it'
import DOMPurify from 'dompurify'

const props = defineProps<{
  content: string
}>()

const md = new MarkdownIt({
  html: false,
  linkify: true,
  typographer: true,
})

const renderedHtml = ref('')
let renderTimer: ReturnType<typeof setTimeout> | null = null

// 节流渲染：流式输出时避免每字符都触发完整 markdown 解析
watch(() => props.content, (newContent) => {
  if (renderTimer) {
    clearTimeout(renderTimer)
  }
  // 立即渲染（首次或内容较短时）
  if (!newContent || newContent.length < 100) {
    renderedHtml.value = DOMPurify.sanitize(md.render(newContent || ''))
    return
  }
  // 节流：最多每 80ms 渲染一次，减少 DOM 更新频率
  renderTimer = setTimeout(() => {
    renderedHtml.value = DOMPurify.sanitize(md.render(newContent || ''))
    renderTimer = null
  }, 80)
}, { immediate: true })
</script>

<style scoped>
.markdown-view {
  line-height: 1.6;
}

.markdown-view :deep(p) {
  margin: 0 0 8px 0;
}

.markdown-view :deep(p:last-child) {
  margin-bottom: 0;
}

.markdown-view :deep(ul), .markdown-view :deep(ol) {
  margin: 8px 0;
  padding-left: 20px;
}

.markdown-view :deep(li) {
  margin: 4px 0;
}
</style>
