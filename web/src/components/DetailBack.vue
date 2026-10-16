<template>
  <el-button size="small" text icon="ArrowLeft" class="kc-back" @click="go">返回{{ label }}</el-button>
</template>

<script setup lang="ts">
import { useRouter } from 'vue-router'

// 详情页统一返回入口：显式 push 回列表（不用 history.back，
// 直接敲 URL 或刷新进来时 back 会跳出控制台）
const props = defineProps<{
  to: string
  /** 列表名，渲染成「返回XX列表」 */
  label: string
  query?: Record<string, string | undefined>
}>()

const router = useRouter()

function go() {
  const query = Object.fromEntries(
    Object.entries(props.query || {}).filter(([, v]) => v != null && v !== ''),
  )
  router.push({ path: props.to, query })
}
</script>

<style scoped>
.kc-back { margin-right: 2px; }
</style>
