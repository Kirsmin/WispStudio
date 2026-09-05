<template>
  <div class="markdown-view md" v-html="sanitizedHtml"></div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
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

const sanitizedHtml = computed(() => {
  const raw = md.render(props.content || '')
  return DOMPurify.sanitize(raw)
})
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
