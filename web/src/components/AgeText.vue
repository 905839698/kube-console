<template>
  <span class="age-text">{{ age }}</span>
</template>

<script setup lang="ts">
defineProps<{ age: string }>()
</script>

<style scoped>
.age-text { color: #909399; font-size: 13px; }
</style>
