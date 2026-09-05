<script setup lang="ts">
import { onBeforeUnmount } from 'vue'
import { useMessage } from 'naive-ui'

const message = useMessage()
window.$message = message
onBeforeUnmount(() => {
  if (window.$message === message) window.$message = undefined
})
</script>

<template><span style="display:none"></span></template>
