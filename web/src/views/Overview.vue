<template>
  <div v-loading="loading">
    <div class="page-toolbar">
      <el-button :icon="Refresh" circle @click="load" />
    </div>
    <!-- 统计卡片 -->
    <el-row :gutter="16">
      <el-col v-for="card in cards" :key="card.label" :span="6">
        <el-card shadow="hover" class="stat-card">
          <div class="stat-inner">
            <el-icon :size="30" :color="card.color"><component :is="card.icon" /></el-icon>
            <div>
              <div class="stat-value">{{ card.value }}</div>
              <div class="stat-label">{{ card.label }}</div>
            </div>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <!-- 监控使用率 -->
    <el-row :gutter="16" style="margin-top: 16px">
      <el-col :span="6">
        <MetricCard label="CPU 使用率" :value="monitor?.cpuUsagePct ?? null" unit="%" :decimals="1" color="#409eff" :trend="monitor?.cpuUsageTrend" />
      </el-col>
      <el-col :span="6">
        <MetricCard label="内存使用率" :value="monitor?.memUsagePct ?? null" unit="%" :decimals="1" color="#67c23a" :trend="monitor?.memUsageTrend" />
      </el-col>
      <el-col :span="6">
        <MetricCard label="磁盘使用率" :value="monitor?.diskUsagePct ?? null" unit="%" :decimals="1" color="#e6a23c" :trend="monitor?.diskUsageTrend" />
      </el-col>
      <el-col :span="6">
        <MetricCard label="网络接收" :value="monitor?.netRxMBs ?? null" unit="MB/s" :decimals="1" color="#f56c6c" :sub="netTxText" :trend="monitor?.netRxTrend" />
      </el-col>
    </el-row>

    <!-- Request/Limit 配额视角（kube-state-metrics 未采集时隐藏） -->
    <el-row v-if="quotaVisible" :gutter="16" style="margin-top: 16px">
      <el-col :span="6"><MetricCard label="CPU Requests" :value="monitor?.cpuRequestsCores ?? null" unit="核" :decimals="2" color="#409eff" :sub="allocSub('cpu', monitor?.cpuRequestsCores)" /></el-col>
      <el-col :span="6"><MetricCard label="CPU Limits" :value="monitor?.cpuLimitsCores ?? null" unit="核" :decimals="2" color="#337ecc" :sub="allocSub('cpu', monitor?.cpuLimitsCores)" /></el-col>
      <el-col :span="6"><MetricCard label="内存 Requests" :value="monitor?.memRequestsGi ?? null" unit="Gi" :decimals="2" color="#67c23a" :sub="allocSub('mem', monitor?.memRequestsGi)" /></el-col>
      <el-col :span="6"><MetricCard label="内存 Limits" :value="monitor?.memLimitsGi ?? null" unit="Gi" :decimals="2" color="#4d9e57" :sub="allocSub('mem', monitor?.memLimitsGi)" /></el-col>
    </el-row>

    <!-- PV 存储用量（数量/总容量走 kube-state-metrics 的 PV 容量，已用率走 kubelet volume stats） -->
    <el-row v-if="pvVisible" :gutter="16" style="margin-top: 16px">
      <el-col :span="6"><MetricCard label="PV 数量" :value="monitor?.pvCount ?? null" unit="个" :decimals="0" color="#9254de" sub="集群内 PersistentVolume 总数" /></el-col>
      <el-col :span="6"><MetricCard label="PV 总容量" :value="monitor?.pvTotalGi ?? null" unit="Gi" :decimals="1" color="#409eff" sub="各 PV spec.capacity 之和" /></el-col>
      <el-col :span="6"><MetricCard label="卷已用率" :value="monitor?.pvUsedPct ?? null" unit="%" :decimals="1" color="#e6a23c" sub="kubelet 挂载点口径" /></el-col>
      <el-col :span="6">
        <el-card shadow="never" class="pv-rank-card">
          <template #header>
            PVC 使用率 Top
            <el-tooltip
              placement="top"
              content="取自 kubelet 的 volume stats，是卷所在文件系统的用量：local-path / 共享盘会把整块节点盘算进来，不等于该 PVC 自己的用量。"
            >
              <el-icon class="pv-tip"><QuestionFilled /></el-icon>
            </el-tooltip>
          </template>
          <RankTable :items="monitor?.pvTopUsed || []" unit="%" />
        </el-card>
      </el-col>
    </el-row>

    <!-- 节点状态 + 节点资源使用排行 -->
    <el-row :gutter="16" style="margin-top: 16px">
      <el-col :span="12">
        <el-card shadow="never">
          <template #header>节点状态</template>
          <el-table border :data="stats?.nodesList || []" size="small" max-height="360" @row-click="goNode">
            <el-table-column prop="name" label="名称" min-width="140" sortable />
            <el-table-column label="状态" prop="status" width="80" sortable>
              <template #default="{ row }"><StatusTag :status="row.status" /></template>
            </el-table-column>
            <el-table-column prop="roles" label="角色" width="90" sortable />
            <el-table-column prop="internalIP" label="IP" width="120" sortable />
            <el-table-column prop="version" label="版本" width="80" />
            <el-table-column prop="kernelVersion" label="内核版本" width="130" show-overflow-tooltip />
            <el-table-column prop="containerRuntime" label="运行时版本" width="130" show-overflow-tooltip />
            <el-table-column prop="cpuCores" label="CPU" width="60" align="center" sortable :sort-by="(row) => Number(row.cpuCores || 0)" />
            <el-table-column prop="memGi" label="内存" width="60" align="center" sortable :sort-by="(row) => Number(row.memGi || 0)" />
          </el-table>
        </el-card>
      </el-col>
      <el-col :span="12">
        <el-card shadow="never">
          <template #header>节点资源使用排行</template>
          <el-tabs v-model="rankTab">
            <el-tab-pane label="CPU" name="cpu">
              <RankTable :items="monitor?.nodeCpuRank || []" unit="%" />
            </el-tab-pane>
            <el-tab-pane label="内存" name="mem">
              <RankTable :items="monitor?.nodeMemRank || []" unit="%" />
            </el-tab-pane>
          </el-tabs>
        </el-card>
      </el-col>
    </el-row>

    <!-- 控制面组件监控与趋势已迁至「可观测性 → 控制平面组件」（按需查询，总览不再背着它刷新） -->
  </div>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { k8sApi, type OverviewStats, type MonitorOverview, type RankItem } from '../api'
import { QuestionFilled, Refresh } from '@element-plus/icons-vue'
import StatusTag from '../components/StatusTag.vue'
import MetricCard from '../components/MetricCard.vue'
import { useClusterStore } from '../store/cluster'
import { textSelected } from '../utils/selection'

const router = useRouter()
const clusterStore = useClusterStore()
const stats = ref<OverviewStats>()
const monitor = ref<MonitorOverview>()
const loading = ref(false)
const rankTab = ref('cpu')

const cards = computed(() => [
  { label: '节点', value: stats.value?.nodes ?? '-', icon: 'Monitor', color: '#409eff' },
  { label: 'Pod', value: stats.value ? `${stats.value.podsRunning}/${stats.value.pods}` : '-', icon: 'Grid', color: '#67c23a' },
  { label: '工作负载', value: stats.value ? stats.value.deployments + stats.value.statefulSets + stats.value.daemonSets : '-', icon: 'Box', color: '#e6a23c' },
  { label: '命名空间', value: stats.value?.namespaces ?? '-', icon: 'FolderOpened', color: '#f56c6c' },
])

const netTxText = computed(() =>
  monitor.value?.netTxMBs != null ? `发送 ${monitor.value.netTxMBs.toFixed(1)} MB/s` : '',
)

// Request/Limit 配额视角（集群可分配资源来自 kube_node_status_allocatable）
const quotaVisible = computed(
  () => (monitor.value?.cpuAllocatableCores ?? 0) > 0 || (monitor.value?.memAllocatableGi ?? 0) > 0,
)
const pvVisible = computed(
  () => (monitor.value?.pvCount ?? 0) > 0 || (monitor.value?.pvTopUsed || []).length > 0,
)

// 分配占比副文案：requests/limits 占集群可分配资源的百分比
function allocSub(kind: 'cpu' | 'mem', v?: number | null): string {
  if (v == null) return ''
  const alloc = kind === 'cpu' ? monitor.value?.cpuAllocatableCores : monitor.value?.memAllocatableGi
  if (!alloc) return ''
  const unit = kind === 'cpu' ? '核' : 'Gi'
  return `占可分配 ${((v / alloc) * 100).toFixed(1)}%（可分配 ${alloc.toFixed(1)} ${unit}）`
}

// RankTable 排行榜组件
const RankTable = defineComponent({
  props: {
    items: { type: Array as () => RankItem[], default: () => [] },
    unit: { type: String, default: '%' },
  },
  setup(props) {
    return () => {
      const rows = [...props.items].sort((a, b) => b.value - a.value).slice(0, 10)
      return h(
        'div',
        { class: 'rank-list' },
        rows.map((r, i) =>
          h('div', { class: 'rank-row' }, [
            h('span', { class: 'rank-idx', style: { color: i < 3 ? '#f56c6c' : '#909399' } }, String(i + 1)),
            h('span', { class: 'rank-name' }, r.name),
            h('div', { class: 'rank-bar-wrap' }, [h('div', { class: 'rank-bar', style: { width: Math.min(100, r.value) + '%' } })]),
            h('span', { class: 'rank-value' }, r.value.toFixed(1) + props.unit),
          ]),
        ),
      )
    }
  },
})

async function load() {
  if (!clusterStore.current) return
  loading.value = true
  try {
    await Promise.all([loadStats(), loadMonitor()])
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

async function loadStats() {
  try {
    stats.value = await k8sApi.overview()
  } catch { /* ignore */ }
}

async function loadMonitor() {
  try {
    // 无时间范围切换控件，固定近 6h（趋势数据仍供顶部 MetricCard 迷你图使用）
    monitor.value = await k8sApi.monitorOverview('6h')
  } catch { /* 无 Prometheus 时静默降级 */ }
}

function goNode(row: { name: string }) {
  if (textSelected()) return // 选中文本（复制）不跳节点详情
  router.push(`/nodes/${row.name}`)
}

onMounted(load)
</script>

<style scoped>
.page-toolbar { display: flex; justify-content: flex-end; margin-bottom: 8px; }
.stat-card { cursor: default; }
.stat-inner { display: flex; align-items: center; gap: 14px; }
.stat-value { font-size: 26px; font-weight: 600; color: #303133; }
.stat-label { font-size: 13px; color: #909399; }
.pv-rank-card :deep(.rank-list) { max-height: 120px; }
.pv-tip { font-size: 13px; color: #c0c4cc; vertical-align: -2px; margin-left: 4px; cursor: help; }
</style>

<style>
.rank-list { max-height: 400px; overflow-y: auto; }
.rank-row { display: flex; align-items: center; gap: 8px; padding: 6px 0; }
.rank-idx { width: 20px; font-weight: 600; flex-shrink: 0; }
.rank-name { width: 160px; font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex-shrink: 0; }
.rank-bar-wrap { flex: 1; background: #f0f2f5; border-radius: 3px; height: 8px; min-width: 40px; }
.rank-bar { height: 8px; border-radius: 3px; background: linear-gradient(90deg, #2fbf71, #00aa55); }
.rank-value { width: 64px; text-align: right; font-size: 13px; color: #606266; flex-shrink: 0; }
</style>
