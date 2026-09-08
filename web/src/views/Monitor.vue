<template>
  <div v-loading="loading">
    <!-- 使用率卡片 -->
    <el-row :gutter="16">
      <el-col :span="6">
        <MetricCard label="CPU 使用率" :value="data?.cpuUsagePct ?? null" unit="%" :decimals="1" color="#409eff" :trend="data?.cpuUsageTrend" />
      </el-col>
      <el-col :span="6">
        <MetricCard label="内存使用率" :value="data?.memUsagePct ?? null" unit="%" :decimals="1" color="#67c23a" :trend="data?.memUsageTrend" />
      </el-col>
      <el-col :span="6">
        <MetricCard label="磁盘使用率" :value="data?.diskUsagePct ?? null" unit="%" :decimals="1" color="#e6a23c" :trend="data?.diskUsageTrend" />
      </el-col>
      <el-col :span="6">
        <MetricCard label="网络接收" :value="data?.netRxMBs ?? null" unit="MB/s" :decimals="1" color="#f56c6c" :sub="netTxText" :trend="data?.netRxTrend" />
      </el-col>
    </el-row>
    <el-row :gutter="16" style="margin-top: 16px">
      <el-col :span="6">
        <MetricCard label="CPU 总核数" :value="data?.cpuCores ?? null" unit="核" :decimals="0" color="#409eff" />
      </el-col>
      <el-col :span="6">
        <MetricCard label="内存总量" :value="data?.memTotalGi ?? null" unit="Gi" :decimals="1" color="#67c23a" />
      </el-col>
      <el-col :span="6" />
      <el-col :span="6" />
    </el-row>

    <!-- 趋势图 -->
    <el-card shadow="never" style="margin-top: 16px">
      <template #header>
        <div class="card-header">
          <span>集群资源趋势</span>
          <RangeSwitch v-model="range" @change="load" />
        </div>
      </template>
      <MetricChart :series="chartSeries" height="300px" y-axis-name="%" />
    </el-card>

    <!-- 控制面组件监控 -->
    <el-card shadow="never" style="margin-top: 16px">
      <template #header>
        <div class="card-header">
          <span>控制面组件监控</span>
          <RangeSwitch v-model="range" @change="load" />
        </div>
      </template>
      <div v-if="(data?.controlPlane || []).length === 0" class="cp-empty-tip">
        暂无控制面组件监控数据（Prometheus 未采集 kube-apiserver/etcd 等组件时此处为空）
      </div>
      <div class="cp-grid">
        <div v-for="cp in data?.controlPlane || []" :key="cp.name" class="cp-item">
          <div class="cp-head">
            <span class="cp-name">{{ cp.name }}</span>
            <el-tag size="small" :type="cpTagType(cp)">{{ cpTagText(cp) }}</el-tag>
            <span v-if="cp.total > 0" class="cp-replicas">{{ cp.ready }}/{{ cp.total }}</span>
          </div>
          <div v-if="cp.cards?.length" class="cp-cards">
            <div v-for="card in cp.cards" :key="card.label" class="cp-card">
              <div class="cp-card-label">{{ card.label }}</div>
              <div class="cp-card-value">{{ fmtCard(card) }}</div>
            </div>
          </div>
          <div v-else-if="cp.total === 0" class="cp-empty">未接入 Prometheus 采集</div>
          <div v-else class="cp-empty">已采集，暂无可用指标</div>
        </div>
      </div>
    </el-card>

    <el-row :gutter="16" style="margin-top: 16px">
      <!-- 节点排行 -->
      <el-col :span="12">
        <el-card shadow="never">
          <template #header>节点资源使用排行</template>
          <el-tabs v-model="rankTab">
            <el-tab-pane label="CPU" name="cpu">
              <RankTable :items="data?.nodeCpuRank || []" unit="%" />
            </el-tab-pane>
            <el-tab-pane label="内存" name="mem">
              <RankTable :items="data?.nodeMemRank || []" unit="%" />
            </el-tab-pane>
          </el-tabs>
        </el-card>
      </el-col>
      <el-col :span="12">
        <el-card shadow="never">
          <template #header>命名空间 CPU 使用排行</template>
          <RankTable :items="data?.namespaceCpuRank || []" unit="%" />
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, onMounted, ref } from 'vue'
import { k8sApi, type ControlPlaneItem, type MonitorOverview, type RankItem, type ControlCard } from '../api'
import MetricCard from '../components/MetricCard.vue'
import MetricChart, { type ChartSeries } from '../components/MetricChart.vue'
import RangeSwitch from '../components/RangeSwitch.vue'
import { useClusterStore } from '../store/cluster'

const clusterStore = useClusterStore()
const data = ref<MonitorOverview>()
const range = ref('6h')
const loading = ref(false)
const rankTab = ref('cpu')

const chartSeries = computed<ChartSeries[]>(() => [
  { name: 'CPU 使用率', data: data.value?.cpuUsageTrend || [], color: '#409eff', unit: '%' },
  { name: '内存使用率', data: data.value?.memUsageTrend || [], color: '#67c23a', unit: '%' },
  { name: '磁盘使用率', data: data.value?.diskUsageTrend || [], color: '#e6a23c', unit: '%' },
  { name: '网络接收', data: data.value?.netRxTrend || [], color: '#f56c6c', unit: 'MB/s' },
])

const netTxText = computed(() =>
  data.value?.netTxMBs != null ? `发送 ${data.value.netTxMBs.toFixed(1)} MB/s` : '',
)

// 控制面组件状态
function cpTagType(cp: ControlPlaneItem): 'success' | 'warning' | 'danger' | 'info' {
  if (cp.total === 0) return 'info'
  if (cp.ready < cp.total) return 'danger'
  return 'success'
}

function cpTagText(cp: ControlPlaneItem): string {
  if (cp.total === 0) return '未接入'
  if (cp.ready < cp.total) return `降级 ${cp.ready}/${cp.total}`
  return '正常'
}

function fmtCard(card: ControlCard): string {
  const v = card.value.toFixed(card.dec ?? 0)
  return card.unit ? `${v} ${card.unit}` : v
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

onMounted(load)

async function load() {
  if (!clusterStore.current) return
  loading.value = true
  try {
    data.value = await k8sApi.monitorOverview(range.value)
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.cp-empty-tip { color: #909399; font-size: 13px; padding: 8px 0; }
.cp-grid { display: flex; flex-wrap: wrap; gap: 12px; }
.cp-item {
  flex: 1 1 30%;
  min-width: 300px;
  border: 1px solid var(--kc-border);
  border-radius: 8px;
  padding: 12px;
  background: #fff;
}
.cp-head { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; }
.cp-name { font-weight: 600; font-size: 14px; }
.cp-replicas { font-size: 12px; color: #909399; }
.cp-cards { display: flex; flex-wrap: wrap; gap: 8px; }
.cp-card {
  flex: 1 1 30%;
  min-width: 90px;
  background: #f7f9fc;
  border-radius: 6px;
  padding: 8px 10px;
  text-align: center;
}
.cp-card-label { font-size: 12px; color: #909399; margin-bottom: 4px; }
.cp-card-value { font-size: 15px; font-weight: 600; color: #303133; }
.cp-empty { font-size: 12px; color: #c0c4cc; padding: 6px 0; }
</style>

<!-- RankTable 由 h() 手写渲染，scoped 样式不生效，需全局样式 -->
<style>
.rank-list { max-height: 400px; overflow-y: auto; }
.rank-row { display: flex; align-items: center; gap: 8px; padding: 6px 0; }
.rank-idx { width: 20px; font-weight: 600; flex-shrink: 0; }
.rank-name { width: 160px; font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex-shrink: 0; }
.rank-bar-wrap { flex: 1; background: #f0f2f5; border-radius: 3px; height: 8px; min-width: 40px; }
.rank-bar { height: 8px; border-radius: 3px; background: linear-gradient(90deg, #00b8a9, #00d2c0); }
.rank-value { width: 64px; text-align: right; font-size: 13px; color: #606266; flex-shrink: 0; }
</style>
