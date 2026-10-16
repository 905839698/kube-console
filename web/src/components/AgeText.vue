<template>
  <span class="age-text">{{ age }}</span>
</template>

<script setup lang="ts">
defineProps<{ age: string }>()
</script>

<style scoped>
.age-text { color: var(--el-text-color-secondary); font-size: 13px; }
</style>
