<template>
  <div v-loading="loading">
    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <el-page-header :content="`节点 / ${name}`" @back="router.back()" />
          <div class="header-actions">
            <el-tag v-if="detail && !detail.schedulable" type="warning" size="small" style="margin-right: 8px">已封锁 (SchedulingDisabled)</el-tag>
            <el-button v-if="detail?.schedulable" size="small" type="warning" plain @click="cordon(true)">封锁</el-button>
            <el-button v-else size="small" type="success" plain @click="cordon(false)">解除封锁</el-button>
            <el-button size="small" @click="load">刷新</el-button>
          </div>
        </div>
      </template>

      <el-descriptions :column="4" border size="small">
        <el-descriptions-item label="状态"><StatusTag :status="detail?.status || ''" /></el-descriptions-item>
        <el-descriptions-item label="角色">{{ detail?.roles }}</el-descriptions-item>
        <el-descriptions-item label="IP">{{ detail?.internalIP }}</el-descriptions-item>
        <el-descriptions-item label="kubelet">{{ detail?.version }}</el-descriptions-item>
        <el-descriptions-item label="系统镜像">{{ detail?.osImage || '-' }}</el-descriptions-item>
        <el-descriptions-item label="运行时">{{ detail?.containerRuntime || '-' }}</el-descriptions-item>
        <el-descriptions-item label="内核">{{ detail?.kernelVersion || '-' }}</el-descriptions-item>
        <el-descriptions-item label="运行时长">{{ detail?.age }}</el-descriptions-item>
      </el-descriptions>

      <el-tabs v-model="tab" style="margin-top: 12px">
        <!-- 容量 -->
        <el-tab-pane label="容量" name="capacity">
          <el-table border :data="capacityRows" size="small" stripe>
            <el-table-column prop="resource" label="资源" width="120" />
            <el-table-column prop="capacity" label="容量" width="160" />
            <el-table-column prop="allocatable" label="可分配" />
          </el-table>
        </el-tab-pane>

        <!-- 条件 -->
        <el-tab-pane label="Conditions" name="conditions">
          <el-table border :data="detail?.conditions || []" size="small" stripe>
            <el-table-column prop="type" label="类型" width="180" />
            <el-table-column label="状态" width="100">
              <template #default="{ row }"><StatusTag :status="row.status === 'True' ? 'Ready' : 'NotReady'" :text="row.status" /></template>
            </el-table-column>
            <el-table-column prop="reason" label="原因" width="140" />
            <el-table-column prop="message" label="消息" min-width="260" show-overflow-tooltip />
            <el-table-column prop="updated" label="更新时间" width="170" />
          </el-table>
        </el-tab-pane>

        <!-- 污点（可视化编辑） -->
        <el-tab-pane label="污点" name="taints">
          <div v-for="(t, i) in taintDraft" :key="i" class="taint-row">
            <el-input v-model="t.key" placeholder="key" size="small" style="width: 26%" />
            <el-input v-model="t.value" placeholder="value（可选）" size="small" style="width: 26%" />
            <el-select v-model="t.effect" size="small" style="width: 30%">
              <el-option v-for="e in ['NoSchedule', 'PreferNoSchedule', 'NoExecute']" :key="e" :label="e" :value="e" />
            </el-select>
            <el-button size="small" type="danger" text @click="taintDraft.splice(i, 1)"><el-icon><Delete /></el-icon></el-button>
          </div>
          <div class="taint-actions">
            <el-button size="small" type="primary" plain @click="taintDraft.push({ key: '', value: '', effect: 'NoSchedule' })">
              <el-icon><Plus /></el-icon>&nbsp;添加污点
            </el-button>
            <el-button size="small" type="primary" :loading="savingTaints" @click="saveTaints">保存污点</el-button>
          </div>
          <div class="taint-tip">污点格式：key=value:effect。带 NoSchedule 的污点会阻止新 Pod 调度到该节点。</div>
        </el-tab-pane>

        <!-- 标签（可视化编辑） -->
        <el-tab-pane label="标签" name="labels" lazy>
          <el-form label-width="80px">
            <el-form-item label="标签">
              <KvEditor v-model="labelDraft" key-placeholder="key" value-placeholder="value" />
            </el-form-item>
            <el-form-item>
              <el-button type="primary" :loading="savingLabels" @click="saveLabels">保存标签</el-button>
              <span class="label-tip">删除某行即删除该标签；系统标签（kubernetes.io 等）请谨慎修改</span>
            </el-form-item>
          </el-form>
        </el-tab-pane>

        <!-- Pod -->
        <el-tab-pane :label="`Pod (${detail?.pods?.length || 0})`" name="pods">
          <el-table border :data="detail?.pods || []" size="small" stripe>
            <el-table-column label="名称" min-width="220">
              <template #default="{ row }">
                <el-link type="primary" @click="goPod(row)">{{ row.name }}</el-link>
              </template>
            </el-table-column>
            <el-table-column prop="namespace" label="命名空间" width="150" />
            <el-table-column label="状态" width="140">
              <template #default="{ row }"><StatusTag :status="row.status" /></template>
            </el-table-column>
            <el-table-column prop="ready" label="就绪" width="70" align="center" />
            <el-table-column prop="ip" label="IP" width="130" />
            <el-table-column label="操作" width="90" align="center">
              <template #default="{ row }">
                <el-button size="small" type="warning" text @click="evictPod(row)">驱除</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-tab-pane>

        <!-- 监控 -->
        <el-tab-pane label="监控" name="monitor" lazy>
          <div class="monitor-toolbar">
            <RangeSwitch v-model="range" @change="loadMonitor" />
          </div>
          <MetricPanel :cards="monitorCards" :charts="monitorCharts" :loading="monitorLoading" />
          <!-- Kubelet 运行时（无 node 标签的采集环境自动隐藏） -->
          <template v-if="monitor?.kubelet">
            <div class="kubelet-title">Kubelet 运行时</div>
            <MetricPanel :cards="kubeletCards" :charts="[]" :loading="monitorLoading" />
          </template>
        </el-tab-pane>
      </el-tabs>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Plus } from '@element-plus/icons-vue'
import { k8sApi, nodeApi, type NodeDetail, type NodeMonitor, type PodItem } from '../api'
import StatusTag from '../components/StatusTag.vue'
import MetricPanel, { type MetricCardDef, type MetricChartDef } from '../components/MetricPanel.vue'
import RangeSwitch from '../components/RangeSwitch.vue'
import KvEditor from '../components/forms/KvEditor.vue'
import { useClusterStore } from '../store/cluster'

const route = useRoute()
const router = useRouter()
const clusterStore = useClusterStore()

const name = computed(() => String(route.params.name))
const detail = ref<NodeDetail>()
const tab = ref('capacity')
const loading = ref(false)

// 监控
const range = ref('6h')
const monitor = ref<NodeMonitor>()
const monitorLoading = ref(false)

const monitorCards = computed<MetricCardDef[]>(() => [
  { label: 'CPU 使用率', value: monitor.value?.cpuUsagePct ?? null, unit: '%', color: '#409eff' },
  { label: '内存使用率', value: monitor.value?.memUsagePct ?? null, unit: '%', color: '#67c23a' },
  { label: '磁盘使用率', value: monitor.value?.diskUsagePct ?? null, unit: '%', color: '#e6a23c' },
  { label: '网络接收', value: monitor.value?.netRxMBs ?? null, unit: 'MB/s', color: '#f56c6c' },
  { label: '网络发送', value: monitor.value?.netTxMBs ?? null, unit: 'MB/s', color: '#909399' },
  { label: '负载 (1m)', value: monitor.value?.load1 ?? null, unit: '', decimals: 2, color: '#9254de', sub: `5m ${fmtNum(monitor.value?.load5)} / 15m ${fmtNum(monitor.value?.load15)}` },
  { label: 'Swap 使用率', value: monitor.value?.swapUsagePct ?? null, unit: '%', decimals: 1, color: '#c059cf' },
  { label: '磁盘读 IOPS', value: monitor.value?.diskReadIops ?? null, unit: '次/s', decimals: 1, color: '#409eff', sub: `写 ${fmtNum(monitor.value?.diskWriteIops)} 次/s` },
  { label: '磁盘读吞吐', value: monitor.value?.diskReadMBs ?? null, unit: 'MB/s', decimals: 2, color: '#67c23a', sub: `写 ${fmtNum(monitor.value?.diskWriteMBs)} MB/s` },
  { label: '磁盘 IO 利用率', value: monitor.value?.ioUtilPct ?? null, unit: '%', decimals: 1, color: '#e6a23c' },
  { label: '网络丢包', value: monitor.value?.netDropRate ?? null, unit: '次/s', decimals: 2, color: '#f56c6c' },
  { label: 'TCP 连接', value: monitor.value?.tcpEstablished ?? null, unit: '条', decimals: 0, color: '#00aa55' },
])

const monitorCharts = computed<MetricChartDef[]>(() => [
  { title: 'CPU 使用率', series: [{ name: 'CPU', data: monitor.value?.cpuUsageTrend || [], color: '#409eff', unit: '%' }], yAxisName: '%' },
  { title: '内存使用率', series: [{ name: '内存', data: monitor.value?.memUsageTrend || [], color: '#67c23a', unit: '%' }], yAxisName: '%' },
  { title: '磁盘使用率', series: [{ name: '磁盘', data: monitor.value?.diskUsageTrend || [], color: '#e6a23c', unit: '%' }], yAxisName: '%' },
  {
    title: '网络流量',
    series: [
      { name: '接收', data: monitor.value?.netRxTrend || [], color: '#409eff', unit: 'MB/s' },
      { name: '发送', data: monitor.value?.netTxTrend || [], color: '#67c23a', unit: 'MB/s' },
    ],
    yAxisName: 'MB/s',
  },
  {
    title: '系统负载 (load1)',
    series: [{ name: 'load1', data: monitor.value?.load1Trend || [], color: '#9254de', unit: '' }],
    yAxisName: '',
  },
  {
    title: '磁盘 IOPS',
    series: [
      { name: '读', data: monitor.value?.diskIopsTrend || [], color: '#409eff', unit: '次/s' },
    ],
    yAxisName: '次/s',
  },
  {
    title: '磁盘吞吐',
    series: [{ name: '读写合计', data: monitor.value?.diskThroughputTrend || [], color: '#67c23a', unit: 'MB/s' }],
    yAxisName: 'MB/s',
  },
  {
    title: '磁盘 IO 利用率',
    series: [{ name: 'IO 利用率', data: monitor.value?.ioUtilTrend || [], color: '#e6a23c', unit: '%' }],
    yAxisName: '%',
  },
  {
    title: '网络丢包',
    series: [{ name: '丢包(收+发)', data: monitor.value?.netDropTrend || [], color: '#f56c6c', unit: '次/s' }],
    yAxisName: '次/s',
  },
])

const kubeletCards = computed<MetricCardDef[]>(() => [
  { label: '运行中 Pod', value: monitor.value?.kubelet?.runningPods ?? null, unit: '个', decimals: 0, color: '#409eff' },
  { label: '运行中容器', value: monitor.value?.kubelet?.runningContainers ?? null, unit: '个', decimals: 0, color: '#67c23a' },
  { label: 'PLEG Relist', value: monitor.value?.kubelet?.relistRate ?? null, unit: '次/s', decimals: 2, color: '#e6a23c' },
  { label: '运行时操作错误', value: monitor.value?.kubelet?.runtimeErrorsRate ?? null, unit: '次/s', decimals: 2, color: '#f56c6c' },
])

function fmtNum(v?: number | null): string {
  return v == null ? '-' : v.toFixed(2)
}

const capacityRows = computed(() => {
  const d = detail.value
  if (!d) return []
  const resources = ['cpu', 'memory', 'ephemeral-storage', 'pods']
  return resources
    .filter((r) => d.capacity[r])
    .map((r) => ({ resource: r, capacity: d.capacity[r], allocatable: d.allocatable[r] || '-' }))
})

onMounted(() => {
  load()
  loadMonitor() // 预加载监控数据，切到监控 tab 立即可见
})

// 切换到监控 tab 时若数据为空则重新加载
watch(tab, (v) => {
  if (v === 'monitor' && !monitor.value) loadMonitor()
})

async function load() {
  if (!clusterStore.current) return
  loading.value = true
  try {
    detail.value = await k8sApi.nodeDetail(name.value)
    // 同步污点/标签草稿
    taintDraft.value = (detail.value?.taintItems || []).map((t) => ({ ...t }))
    labelDraft.value = { ...(detail.value?.labels || {}) }
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

// ------------------- 封禁 / 污点 / 标签 -------------------
const savingTaints = ref(false)
const savingLabels = ref(false)
const taintDraft = ref<{ key: string; value: string; effect: string }[]>([])
const labelDraft = ref<Record<string, string>>({})

async function cordon(on: boolean) {
  const verb = on ? '封锁（不再调度新 Pod）' : '解除封锁'
  await ElMessageBox.confirm(`确定${verb}节点「${name.value}」？`, '确认', { type: 'warning' })
  await nodeApi.cordon(name.value, on)
  ElMessage.success(on ? '已封锁' : '已解除封锁')
  await load()
}

async function saveTaints() {
  const items = taintDraft.value.filter((t) => t.key.trim())
  savingTaints.value = true
  try {
    await nodeApi.updateTaints(name.value, items)
    ElMessage.success('污点已保存')
    await load()
  } finally {
    savingTaints.value = false
  }
}

async function saveLabels() {
  savingLabels.value = true
  try {
    await nodeApi.updateLabels(name.value, { ...labelDraft.value })
    ElMessage.success('标签已保存')
    await load()
  } finally {
    savingLabels.value = false
  }
}

async function loadMonitor() {
  if (!clusterStore.current) return
  monitorLoading.value = true
  try {
    monitor.value = await k8sApi.monitorNode(name.value, range.value)
  } catch {
    /* 拦截器已提示 */
  } finally {
    monitorLoading.value = false
  }
}

function goPod(pod: PodItem) {
  router.push({ path: `/pods/${pod.namespace}/${pod.name}` })
}

// 驱除 Pod（走 Eviction API，尊重 PDB；DaemonSet/静态 Pod 由后端拒绝）
async function evictPod(pod: PodItem) {
  await ElMessageBox.confirm(
    `驱除 Pod「${pod.name}」？将触发控制器重建（若有）。DaemonSet / 静态 Pod 不可驱除。`,
    '驱除 Pod',
    { type: 'warning' },
  )
  try {
    await k8sApi.evictPod(pod.namespace, pod.name)
    ElMessage.success(`已发起驱除：${pod.name}`)
    await load()
  } catch {
    /* 拦截器已提示 */
  }
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.header-actions { display: flex; align-items: center; gap: 8px; }
.monitor-toolbar { margin-bottom: 12px; display: flex; justify-content: flex-end; }
.kubelet-title { font-weight: 600; font-size: 14px; margin: 16px 0 8px; }
.taint-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; }
.taint-actions { display: flex; gap: 8px; margin-top: 8px; }
.taint-tip { color: #909399; font-size: 12px; margin-top: 10px; }
.label-tip { color: #909399; font-size: 12px; margin-left: 10px; }
</style>
