<template>
  <MdPreview
    :id="previewId"
    :model-value="displayContent"
    class="md"
    preview-theme="github"
    code-theme="github"
    :sanitize="sanitize"
    :show-code-row-number="false"
    :code-foldable="false"
    :no-mermaid="true"
    :no-echarts="true"
    :no-img-zoom-in="true"
  />
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { MdPreview } from 'md-editor-v3'
import DOMPurify from 'dompurify'
import 'md-editor-v3/lib/preview.css'

const props = defineProps<{
  content: string
}>()

// 每个实例独立 id，避免同页多个预览冲突
const previewId = 'mdp-' + Math.random().toString(36).slice(2, 9)

// 使用 DOMPurify 做 XSS 防护
const sanitize = (html: string) => DOMPurify.sanitize(html)

const displayContent = ref('')
let renderTimer: ReturnType<typeof setTimeout> | null = null

// 节流渲染：流式输出时避免每字符都触发完整 markdown 解析
watch(() => props.content, (newContent) => {
  if (renderTimer) {
    clearTimeout(renderTimer)
  }
  // 立即渲染（首次或内容较短时）
  if (!newContent || newContent.length < 100) {
    displayContent.value = newContent || ''
    return
  }
  // 节流：最多每 80ms 渲染一次，减少 DOM 更新频率
  renderTimer = setTimeout(() => {
    displayContent.value = newContent || ''
    renderTimer = null
  }, 80)
}, { immediate: true })
</script>

<style scoped>
/* 让 md-editor-v3 与全局粉色主题统一 */
.md {
  --md-color: var(--text);
  --md-bk-color: transparent;
  --md-theme: var(--accent-text);
  --md-border-color: var(--border);
  --md-bk-color-outstand: var(--bg-soft);
  font-size: 14px;
  line-height: 1.7;
  background: transparent;
}

.md :deep(.md-editor-preview-wrapper) {
  padding: 0;
}

.md :deep(p) {
  margin: 0 0 8px 0;
}

.md :deep(p:last-child) {
  margin-bottom: 0;
}

.md :deep(pre) {
  border-radius: 10px;
}

.md :deep(code) {
  font-family: 'SF Mono', Monaco, 'Cascadia Code', monospace;
}
</style>
