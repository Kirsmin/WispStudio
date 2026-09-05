<template>
  <MdPreview
    :id="previewId"
    :model-value="props.content"
    class="md"
    preview-theme="github"
    code-theme="github"
    :sanitize="sanitize"
  />
</template>

<script setup lang="ts">
import { MdPreview } from 'md-editor-v3'
import DOMPurify from 'dompurify'
import 'md-editor-v3/lib/preview.css'

const props = defineProps<{
  content: string
}>()

const previewId = 'mdp-' + Math.random().toString(36).slice(2, 9)
const sanitize = (html: string): string => DOMPurify.sanitize(html)
</script>

<style scoped>
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
