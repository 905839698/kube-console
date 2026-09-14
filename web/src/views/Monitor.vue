<template>
  <div>
    <!-- PromQL 查询栏 -->
    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <span>PromQL 查询</span>
          <div class="header-right">
            <RangeSwitch v-model="range" @change="execute" />
            <el-button :icon="Refresh" circle @click="execute" style="margin-left: 12px" />
          </div>
        </div>
      </template>
      <div class="query-bar">
        <el-input
          v-model="query"
          class="query-input"
          placeholder='输入 PromQL 表达式，如 sum(rate(container_cpu_usage_seconds_total{container!=""}[5m]))'
          clearable
          @keyup.enter="execute"
        />
        <el-button type="primary" :loading="loading" @click="execute">执行</el-button>
      </div>
      <div class="preset-row">
        <span class="preset-label">快捷查询：</span>
        <el-tag
          v-for="p in presets"
          :key="p.label"
          class="preset-tag"
          effect="plain"
          @click="runPreset(p)"
        >{{ p.label }}</el-tag>
      </div>
    </el-card>

    <!-- 查询结果：范围查询图形 + 瞬时查询表格 -->
    <el-card shadow="never" class="result-card">
      <template #header>
        <div class="card-header">
          <span>查询结果</span>
          <span v-if="executed" class="result-meta">{{ seriesCount }} 个序列 · {{ queryTime }}</span>
        </div>
      </template>
      <div v-if="!executed" class="empty-tip">输入 PromQL 表达式后点击「执行」</div>
      <template v-else>
        <MetricChart :series="chartSeries" height="320px" />
        <div v-if="rangeNote" class="cap-note">{{ rangeNote }}</div>
        <el-table border :data="tableRows" size="small" max-height="360" class="instant-table">
          <el-table-column label="指标" min-width="200" show-overflow-tooltip>
            <template #default="{ row }"><code class="metric-name">{{ row.name || '(scalar)' }}</code></template>
          </el-table-column>
          <el-table-column v-for="k in labelKeys" :key="k" :label="k" min-width="140" show-overflow-tooltip>
            <template #default="{ row }">{{ row.labels[k] || '' }}</template>
          </el-table-column>
          <el-table-column label="值" width="140" align="right" fixed="right">
            <template #default="{ row }"><b>{{ fmtVal(row.value) }}</b></template>
          </el-table-column>
        </el-table>
        <div v-if="tableNote" class="cap-note">{{ tableNote }}</div>
      </template>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { k8sApi, type PromQueryResult } from '../api'
import { Refresh } from '@element-plus/icons-vue'
import MetricChart, { type ChartSeries } from '../components/MetricChart.vue'
import RangeSwitch from '../components/RangeSwitch.vue'
import { useClusterStore } from '../store/cluster'

const clusterStore = useClusterStore()

const DEFAULT_QUERY = 'sum(rate(container_cpu_usage_seconds_total{container!=""}[5m]))'
const query = ref(DEFAULT_QUERY)
const range = ref('6h')
const loading = ref(false)
const executed = ref(false)
const queryTime = ref('')
const instantRows = ref<PromQueryResult[]>([])
const rangeResults = ref<PromQueryResult[]>([])

// 常用 PromQL 示例（点击填入并立即执行）
const presets = [
  { label: '集群 CPU（核）', q: 'sum(rate(container_cpu_usage_seconds_total{container!=""}[5m]))' },
  { label: '集群内存（Mi）', q: 'sum(container_memory_working_set_bytes{container!=""}) / 1024 / 1024' },
  { label: '节点 CPU 使用率', q: '100 - avg(rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100' },
  { label: 'Pod 数量', q: 'count(kube_pod_info)' },
  { label: 'API 请求/s', q: 'sum(rate(apiserver_request_total[5m]))' },
  { label: '节点负载(1m)', q: 'avg(node_load1)' },
]

const GRAPH_MAX = 30
const TABLE_MAX = 100
const PALETTE = ['#00aa55', '#409eff', '#67c23a', '#e6a23c', '#f56c6c', '#9254de', '#00c1de', '#ff7f50', '#87ceeb', '#7c889a', '#d4b106', '#c059cf']

// Prometheus 风格图例：metric{label="value", ...}
function seriesName(r: PromQueryResult): string {
  const m = { ...(r.metric || {}) }
  const name = m['__name__'] || ''
  delete m['__name__']
  const labels = Object.keys(m).sort().map((k) => `${k}="${m[k]}"`)
  return labels.length ? `${name}{${labels.join(', ')}}` : name
}

function fmtVal(v: number): string {
  if (v == null || !Number.isFinite(v)) return 'NaN'
  const a = Math.abs(v)
  if (a === 0) return '0'
  if (a >= 1000) return v.toFixed(1)
  if (a >= 1) return v.toFixed(3)
  if (a >= 0.001) return v.toFixed(5)
  return v.toExponential(2)
}

const chartSeries = computed<ChartSeries[]>(() =>
  rangeResults.value
    .slice(0, GRAPH_MAX)
    .map((r, i) => ({
      name: seriesName(r),
      data: (r.values || []).map(([ts, v]) => [ts, parseFloat(v)] as [number, number]),
      color: PALETTE[i % PALETTE.length],
    }))
    .filter((s) => s.data.length > 0),
)

const rangeNote = computed(() =>
  rangeResults.value.length > GRAPH_MAX ? `共 ${rangeResults.value.length} 个序列，图形仅展示前 ${GRAPH_MAX} 个` : '',
)

const seriesCount = computed(() => Math.max(rangeResults.value.length, instantRows.value.length))

// 表格列 = 各序列 label 的并集（与 Prometheus 一致，不含 __name__）
const labelKeys = computed(() => {
  const keys = new Set<string>()
  for (const r of instantRows.value) {
    for (const k of Object.keys(r.metric || {})) {
      if (k !== '__name__') keys.add(k)
    }
  }
  return [...keys].sort()
})

const tableRows = computed(() =>
  instantRows.value.slice(0, TABLE_MAX).map((r) => {
    const m = { ...(r.metric || {}) }
    const name = m['__name__'] || ''
    delete m['__name__']
    return { name, labels: m, value: r.value ? parseFloat(r.value[1]) : NaN }
  }),
)

const tableNote = computed(() =>
  instantRows.value.length > TABLE_MAX ? `共 ${instantRows.value.length} 个序列，表格仅展示前 ${TABLE_MAX} 个` : '',
)

async function execute() {
  const q = query.value.trim()
  if (!q) {
    ElMessage.warning('请先输入 PromQL 表达式')
    return
  }
  if (!clusterStore.current) return
  loading.value = true
  try {
    // 瞬时查询（表格）与范围查询（图形）并行
    const [instant, ranged] = await Promise.all([
      k8sApi.monitorQuery(q),
      k8sApi.monitorQueryRange(q, range.value),
    ])
    instantRows.value = instant || []
    rangeResults.value = ranged || []
    queryTime.value = new Date().toLocaleTimeString('zh-CN', { hour12: false })
    executed.value = true
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

function runPreset(p: { label: string; q: string }) {
  query.value = p.q
  execute()
}

onMounted(execute)
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.query-bar { display: flex; gap: 12px; }
.query-input { flex: 1; }
.query-input :deep(.el-input__inner) {
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace;
  font-size: 13px;
}
.preset-row { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-top: 12px; }
.preset-label { font-size: 13px; color: #909399; }
.preset-tag { cursor: pointer; }
.preset-tag:hover { color: var(--el-color-primary); border-color: var(--el-color-primary); }
.result-card { margin-top: 16px; }
.result-meta { font-size: 12px; color: #909399; }
.empty-tip { color: #909399; font-size: 13px; padding: 32px 0; text-align: center; }
.instant-table { margin-top: 16px; }
.cap-note { font-size: 12px; color: #e6a23c; margin-top: 8px; }
.metric-name { font-size: 12px; word-break: break-all; }
</style>
