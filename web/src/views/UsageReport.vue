<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>命名空间资源用量报表（Prometheus）</span>
        <div>
          <el-select v-model="days" style="width: 130px; margin-right: 8px" @change="load">
            <el-option v-for="d in [1, 7, 30, 90]" :key="d" :label="`近 ${d} 天`" :value="d" />
          </el-select>
          <el-button size="small" @click="exportCsv" :disabled="!rows.length">导出 CSV</el-button>
          <el-button :icon="Refresh" circle size="small" @click="load" style="margin-left: 8px" />
        </div>
      </div>
    </template>

    <el-table border :data="rows" v-loading="loading" size="small" stripe>
      <el-table-column prop="namespace" label="命名空间" min-width="180" />
      <el-table-column label="CPU 平均（核）" width="140" align="right">
        <template #default="{ row }">{{ row.cpuAvgCores.toFixed(3) }}</template>
      </el-table-column>
      <el-table-column label="CPU 峰值（核）" width="140" align="right">
        <template #default="{ row }">{{ row.cpuMaxCores.toFixed(3) }}</template>
      </el-table-column>
      <el-table-column label="内存平均 (GiB)" width="140" align="right">
        <template #default="{ row }">{{ row.memAvgGi.toFixed(3) }}</template>
      </el-table-column>
      <el-table-column label="内存峰值 (GiB)" width="140" align="right">
        <template #default="{ row }">{{ row.memMaxGi.toFixed(3) }}</template>
      </el-table-column>
      <el-table-column label="平均 Pod 数" width="120" align="right">
        <template #default="{ row }">{{ row.podAvgCount.toFixed(1) }}</template>
      </el-table-column>
      <el-table-column label="CPU 占用" min-width="160">
        <template #default="{ row }">
          <el-progress :percentage="pct(row.cpuAvgCores, maxCpu)" :stroke-width="8" />
        </template>
      </el-table-column>
    </el-table>
    <el-alert type="info" :closable="false" style="margin-top: 10px"
      title="数据来自 Prometheus 历史指标（container_cpu/memory + kube-state-metrics）。采样点约 120 个，数值为区间平均与峰值。" />
  </el-card>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { usageApi, type NSUsageRow } from '../api'

const days = ref(7)
const rows = ref<NSUsageRow[]>([])
const loading = ref(false)

const maxCpu = computed(() => Math.max(...rows.value.map((r) => r.cpuAvgCores), 0.001))
const pct = (v: number, max: number) => Math.min(100, Math.round((v / max) * 100))

async function load() {
  loading.value = true
  try {
    rows.value = await usageApi.report(days.value)
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

function exportCsv() {
  const header = ['namespace', 'cpuAvgCores', 'cpuMaxCores', 'memAvgGi', 'memMaxGi', 'podAvgCount']
  const lines = [header.join(',')]
  for (const r of rows.value) {
    lines.push([r.namespace, r.cpuAvgCores.toFixed(4), r.cpuMaxCores.toFixed(4), r.memAvgGi.toFixed(4), r.memMaxGi.toFixed(4), r.podAvgCount.toFixed(1)].join(','))
  }
  const blob = new Blob(['\ufeff' + lines.join('\n')], { type: 'text/csv' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `usage-${days.value}d-${new Date().toISOString().slice(0, 10)}.csv`
  a.click()
}

onMounted(load)
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
</style>
