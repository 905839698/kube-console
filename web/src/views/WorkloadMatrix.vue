<template>
  <el-card shadow="never" v-loading="loading">
    <template #header>
      <div class="card-header">
        <span>工作负载矩阵（{{ matrix?.namespaces.length || 0 }} 个命名空间）</span>
        <el-button :icon="Refresh" circle size="small" @click="load" />
      </div>
    </template>

    <el-table border :data="rows" size="small" stripe :row-class-name="rowClass">
      <!-- 命名空间列 -->
      <el-table-column label="命名空间" min-width="180" fixed="left">
        <template #default="{ row }">
          <span class="ns-name">{{ row.namespace }}</span>
          <span class="ns-total">{{ row.total }} 个</span>
        </template>
      </el-table-column>
      <!-- 各类型列 -->
      <el-table-column v-for="k in matrix?.kinds || []" :key="k" :label="k" min-width="150" align="center">
        <template #default="{ row }">
          <div v-if="row.cells[k]" class="cell" :class="cellClass(row.cells[k])" @click="goList(row.namespace, k)" @click.stop>
            <span class="cell-ready">{{ row.cells[k].ready }}</span>
            <span class="cell-total">/{{ row.cells[k].total }}</span>
          </div>
          <span v-else class="cell-empty">-</span>
        </template>
      </el-table-column>
    </el-table>
  </el-card>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Refresh } from '@element-plus/icons-vue'
import { k8sApi, type WorkloadMatrix } from '../api'
import { useClusterStore } from '../store/cluster'

const router = useRouter()
const clusterStore = useClusterStore()
const matrix = ref<WorkloadMatrix>()
const loading = ref(false)

interface Row {
  namespace: string
  total: number
  cells: Record<string, { total: number; ready: number }>
}

const rows = computed<Row[]>(() => {
  const m = matrix.value
  if (!m) return []
  return m.namespaces.map((ns) => {
    const cells = m.cells[ns] || {}
    let total = 0
    for (const k of m.kinds) {
      total += cells[k]?.total || 0
    }
    return { namespace: ns, total, cells }
  })
})

onMounted(load)

async function load() {
  if (!clusterStore.current) return
  loading.value = true
  try {
    matrix.value = await k8sApi.workloadMatrix()
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

function cellClass(cell: { total: number; ready: number }) {
  if (cell.total === 0) return ''
  if (cell.ready >= cell.total) return 'cell-ok'
  if (cell.ready === 0) return 'cell-error'
  return 'cell-warn'
}

function rowClass({ row }: { row: Row }) {
  return row.total === 0 ? 'row-empty' : ''
}

function goList(ns: string, kind: string) {
  const kindPath: Record<string, string> = { Deployment: 'deployments', StatefulSet: 'statefulsets', DaemonSet: 'daemonsets' }
  router.push({ path: `/workloads/${kindPath[kind] || kind}`, query: { namespace: ns } })
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.ns-name { font-weight: 600; color: #1f2937; }
.ns-total { margin-left: 8px; font-size: 12px; color: #9ca3af; }
.cell {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 64px;
  padding: 3px 12px;
  border-radius: 6px;
  cursor: pointer;
  font-size: 13px;
}
.cell-ok { background: var(--el-color-primary-light-9); color: var(--el-color-primary); font-weight: 600; }
.cell-warn { background: #fdf6ec; color: #e6a23c; font-weight: 600; }
.cell-error { background: #fef0f0; color: #f56c6c; font-weight: 600; }
.cell-empty { color: #dcdfe6; }
.cell-ready { font-weight: 700; }
.cell-total { opacity: 0.7; }
:deep(.row-empty td) { color: #c0c4cc; }
</style>
