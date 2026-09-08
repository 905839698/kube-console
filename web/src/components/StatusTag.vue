<template>
  <el-tag :type="tagType" size="small" effect="light">{{ text }}</el-tag>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ status: string; text?: string }>()

// kubectl 风格状态配色：Running=绿 / Pending、Waiting=黄 / Failed、Error、CrashLoopBackOff、ImagePullBackOff=红 / 其余=灰
const typeMap: Record<string, 'success' | 'warning' | 'danger' | 'info' | 'primary'> = {
  Running: 'success',
  Completed: 'success',
  Ready: 'success',
  Succeeded: 'success',
  Active: 'success',
  connected: 'success',
  Pending: 'warning',
  Waiting: 'warning',
  Terminating: 'warning',
  unknown: 'info',
  CrashLoopBackOff: 'danger',
  ImagePullBackOff: 'danger',
  Failed: 'danger',
  Error: 'danger',
  NotReady: 'danger',
  error: 'danger',
}

const tagType = computed(() => typeMap[props.status] || 'info')
const text = computed(() => props.text ?? props.status)
</script>
