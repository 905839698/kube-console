<!-- 共享的元信息编辑器：标签(labels) + 注解(annotations)。
     供各可视化表单复用（除已有自身标签字段的 WorkloadForm 外）。
     关键点：返回稳定的 metadata 引用，并确保 labels/annotations 为真实对象，
     让 KvEditor 直接原地编辑、即时刷新（避免 computed 返回不稳定引用导致编辑后需额外刷新才显示）。 -->
<template>
  <div class="meta-editor">
    <el-form-item label="标签">
      <KvEditor v-model="meta.labels" key-placeholder="key" value-placeholder="value" />
    </el-form-item>
    <el-form-item v-if="!noAnnotations" label="注解">
      <KvEditor v-model="meta.annotations" key-placeholder="key" value-placeholder="value" :multiline="true" />
    </el-form-item>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import KvEditor from './KvEditor.vue'

// noAnnotations: 部分资源用 annotations 承载业务数据（如路由），此时不暴露注解编辑器
const props = defineProps<{ object: any; noAnnotations?: boolean }>()

// 幂等地确保 metadata / labels / annotations 为真实对象，并返回稳定的 metadata 引用。
// （仅在缺失时创建，正常情况为纯读取；返回同一对象引用，KvEditor 深度监听不会误判为变化）
const meta = computed(() => {
  const o = props.object
  if (!o || typeof o !== 'object') return {}
  if (!o.metadata || typeof o.metadata !== 'object') o.metadata = {}
  const m = o.metadata
  if (!m.labels || typeof m.labels !== 'object') m.labels = {}
  if (!m.annotations || typeof m.annotations !== 'object') m.annotations = {}
  return m
})
</script>

<style scoped>
.meta-editor { width: 100%; }
</style>
