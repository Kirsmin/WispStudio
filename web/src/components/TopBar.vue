<template><div class="top-bar"><div class="logo"><span class="logo-dot" />Wisp</div><div class="connection-status"><ConnectDialog /></div></div></template>
<script setup lang="ts">import ConnectDialog from './ConnectDialog.vue'</script>
<style scoped>
.top-bar { height: 52px; display: flex; align-items: center; justify-content: space-between; padding: 0 20px; border-bottom: 1px solid var(--border); background: var(--bg); flex-shrink: 0; }.logo { display: flex; align-items: center; gap: 8px; font-size: 17px; font-weight: 600; letter-spacing: .2px; color: var(--text); }.logo-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--accent); }.connection-status { display: flex; align-items: center; gap: 8px; }
</style>
