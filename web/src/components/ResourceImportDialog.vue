<template>
  <!-- 隐藏文件选择：由父组件 ref.pick() 触发 -->
  <input ref="fileInput" type="file" accept=".yaml,.yml" style="display: none" @change="onFile" />

  <el-dialog v-model="visible" title="导入资源" width="760px" :close-on-click-modal="false">
    <el-alert type="info" :closable="false" style="margin-bottom: 10px"
      :title="`按 create-or-update 逐个应用以下 ${docs.length} 个资源（已存在的更新，不存在的创建）`" />
    <el-table border :data="docs" size="small" max-height="360">
      <el-table-column label="Kind" width="160" show-overflow-tooltip>
        <template #default="{ row }">{{ row?.kind || row?.apiVersion }}</template>
      </el-table-column>
      <el-table-column label="名称" min-width="180" show-overflow-tooltip>
        <template #default="{ row }">{{ row?.metadata?.name }}</template>
      </el-table-column>
      <el-table-column label="命名空间" width="150">
        <template #default="{ row }">{{ row?.metadata?.namespace || 'default' }}</template>
      </el-table-column>
      <el-table-column label="结果" width="150">
        <template #default="{ $index }">
          <span v-if="results[$index] === undefined" class="pending">待应用</span>
          <el-tag v-else-if="!results[$index]" size="small" type="success">成功</el-tag>
          <el-tooltip v-else :content="results[$index]"><el-tag size="small" type="danger">失败</el-tag></el-tooltip>
        </template>
      </el-table-column>
    </el-table>
    <template #footer>
      <el-button @click="visible = false">关闭</el-button>
      <el-button type="primary" :loading="applying" :disabled="!docs.length || done" @click="apply">应用</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { loadAll as yamlLoadAll, dump as yamlDump } from 'js-yaml'
import { k8sApi } from '../api'

const emit = defineEmits<{ done: [] }>()

const fileInput = ref<HTMLInputElement>()
const visible = ref(false)
const applying = ref(false)
const done = ref(false)
const docs = ref<any[]>([])
const results = ref<Record<number, string>>({})

// 供父组件触发文件选择（工具栏「导入」按钮）
function pick() {
  fileInput.value?.click()
}

async function onFile(ev: Event) {
  const input = ev.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = '' // 允许再次选择同一文件
  if (!file) return
  try {
    const parsed = (yamlLoadAll(await file.text()) as any[]).filter((d) => d && typeof d === 'object' && d.metadata?.name)
    if (!parsed.length) {
      ElMessage.warning('文件中未找到资源定义（需带 apiVersion/kind/metadata.name）')
      return
    }
    docs.value = parsed
    results.value = {}
    done.value = false
    visible.value = true
  } catch (e) {
    ElMessage.error(`解析文件失败：${(e as Error).message || e}`)
  }
}

async function apply() {
  applying.value = true
  let ok = 0
  for (let i = 0; i < docs.value.length; i++) {
    try {
      await k8sApi.applyYaml(yamlDump(docs.value[i]))
      results.value[i] = ''
      ok++
    } catch (e) {
      results.value[i] = (e as Error).message || String(e)
    }
  }
  applying.value = false
  done.value = true
  if (ok === docs.value.length) ElMessage.success(`已应用 ${ok} 个资源`)
  else ElMessage.warning(`应用完成：成功 ${ok} / ${docs.value.length}，失败项见列表`)
  emit('done')
}

defineExpose({ pick })
</script>

<style scoped>
.pending { color: #909399; font-size: 12px; }
</style>
