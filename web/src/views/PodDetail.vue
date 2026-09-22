<template>
  <div v-loading="loading">
    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <el-page-header :content="`Pod / ${name}`" @back="router.back()" />
          <div class="actions">
            <el-button size="small" @click="load">刷新</el-button>
            <el-button size="small" type="success" plain :disabled="!perm.canWriteNS(namespace)" @click="openTerminal()">终端</el-button>
            <el-button size="small" type="primary" plain @click="logsVisible = true">日志</el-button>
            <el-button size="small" type="info" plain :disabled="!perm.canWriteNS(namespace)" @click="filesVisible = true">文件</el-button>
            <el-button size="small" type="danger" plain :disabled="!perm.canWriteNS(namespace)" @click="doDelete">删除</el-button>
          </div>
        </div>
      </template>

      <div class="detail-layout">
        <!-- 左侧资源树 -->
        <aside class="detail-side">
          <div class="side-title">资源导航</div>
          <el-tree
            :data="treeData"
            node-key="id"
            :props="{ label: 'label', children: 'children' }"
            highlight-current
            default-expand-all
            @node-click="onTreeClick"
          >
            <template #default="{ data }">
              <span class="tree-node">
                <span class="status-dot" :class="'dot-' + detail?.status"></span>
                <el-icon v-if="data.type === 'container'" size="12"><Box /></el-icon>
                <el-icon v-else size="14"><Grid /></el-icon>
                <span class="tree-label">{{ data.label }}</span>
              </span>
            </template>
          </el-tree>
        </aside>

        <!-- 右侧面板 -->
        <section class="detail-main">
          <template v-if="selectedType === 'pod'">
            <el-descriptions :column="4" border size="small" style="margin-bottom: 12px">
              <el-descriptions-item label="命名空间">{{ detail?.namespace }}</el-descriptions-item>
              <el-descriptions-item label="状态"><StatusTag :status="detail?.status || ''" /></el-descriptions-item>
              <el-descriptions-item label="Phase">{{ detail?.phase }}</el-descriptions-item>
              <el-descriptions-item label="IP">{{ detail?.ip || '-' }}</el-descriptions-item>
              <el-descriptions-item label="节点">{{ detail?.nodeName || '-' }}</el-descriptions-item>
              <el-descriptions-item label="ServiceAccount">{{ detail?.serviceAccount || '-' }}</el-descriptions-item>
              <el-descriptions-item label="启动时间">{{ detail?.startTime || '-' }}</el-descriptions-item>
              <el-descriptions-item label="重启次数">{{ detail?.restarts }}</el-descriptions-item>
            </el-descriptions>

            <el-tabs v-model="tab">
              <!-- 容器 -->
              <el-tab-pane label="容器" name="containers">
                <el-table border :data="detail?.containers || []" size="small" stripe>
                  <el-table-column prop="name" label="名称" min-width="140" />
                  <el-table-column label="状态" width="120">
                    <template #default="{ row }"><StatusTag :status="row.state" :text="row.reason || row.state" /></template>
                  </el-table-column>
                  <el-table-column prop="ready" label="就绪" width="70" align="center" />
                  <el-table-column prop="restartCount" label="重启" width="70" align="center" />
                  <el-table-column prop="message" label="信息" min-width="220" show-overflow-tooltip />
                  <el-table-column prop="image" label="镜像" min-width="200" show-overflow-tooltip>
                    <template #default="{ row }"><span class="mono-img">{{ row.image }}</span></template>
                  </el-table-column>
                  <el-table-column label="镜像 CVE" width="180">
                    <template #default="{ row }">
                      <VulnChip :image="row.image" />
                    </template>
                  </el-table-column>
                </el-table>
                <div v-if="detail?.initContainers?.length" style="margin-top: 12px">
                  <div class="sub-title">Init 容器</div>
                  <el-table border :data="detail.initContainers" size="small" stripe>
                    <el-table-column prop="name" label="名称" min-width="140" />
                    <el-table-column label="状态" width="120">
                      <template #default="{ row }"><StatusTag :status="row.state" :text="row.reason || row.state" /></template>
                    </el-table-column>
                    <el-table-column prop="ready" label="就绪" width="70" align="center" />
                    <el-table-column prop="restartCount" label="重启" width="70" align="center" />
                  </el-table>
                </div>
              </el-tab-pane>

              <!-- 条件 -->
              <el-tab-pane label="Conditions" name="conditions">
                <el-table border :data="detail?.conditions || []" size="small" stripe>
                  <el-table-column prop="type" label="类型" min-width="180" />
                  <el-table-column label="状态" width="100">
                    <template #default="{ row }"><StatusTag :status="row.status === 'True' ? 'Ready' : 'NotReady'" :text="row.status" /></template>
                  </el-table-column>
                  <el-table-column prop="reason" label="原因" min-width="140" />
                  <el-table-column prop="message" label="消息" min-width="260" show-overflow-tooltip />
                  <el-table-column prop="updated" label="更新时间" width="170" />
                </el-table>
              </el-tab-pane>

              <!-- 事件 -->
              <el-tab-pane label="事件" name="events">
                <EventTable :events="detail?.events || []" />
              </el-tab-pane>

              <!-- 污点容忍 -->
              <el-tab-pane label="容忍" name="tolerations">
                <el-empty v-if="!detail?.tolerations?.length" description="无容忍配置" :image-size="60" />
                <el-tag v-for="t in detail?.tolerations" :key="t" style="margin: 4px">{{ t }}</el-tag>
              </el-tab-pane>

              <!-- 监控 -->
              <el-tab-pane label="监控" name="monitor" lazy>
                <div class="monitor-toolbar">
                  <RangeSwitch v-model="range" @change="loadMonitor" />
                </div>
                <MetricPanel :cards="monitorCards" :charts="monitorCharts" :loading="monitorLoading" />
              </el-tab-pane>
            </el-tabs>
          </template>

          <!-- 容器视图 -->
          <template v-else>
            <div class="pod-view">
              <div class="view-header">
                <span class="view-title">
                  <el-icon><Box /></el-icon>
                  {{ selectedContainer?.name }}
                </span>
                <el-button size="small" type="success" plain :disabled="!perm.canWriteNS(namespace)" @click="openTerminal(selectedContainer?.name)">终端</el-button>
                <el-button size="small" @click="openContainerLogs">查看日志</el-button>
              </div>
              <el-descriptions :column="2" border size="small">
                <el-descriptions-item label="状态">
                  <StatusTag :status="selectedContainer?.state || ''" :text="selectedContainer?.reason || selectedContainer?.state || '-'" />
                </el-descriptions-item>
                <el-descriptions-item label="就绪">{{ selectedContainer?.ready ?? '-' }}</el-descriptions-item>
                <el-descriptions-item label="重启次数">{{ selectedContainer?.restartCount ?? '-' }}</el-descriptions-item>
                <el-descriptions-item label="信息">{{ selectedContainer?.message || '-' }}</el-descriptions-item>
              </el-descriptions>
            </div>
          </template>
        </section>
      </div>
    </el-card>

    <PodLogsDrawer v-model="logsVisible" :pod="podItem" :container="logsContainer" />
    <WebTerminal v-model:visible="terminalVisible" :namespace="namespace" :pod="name" :container="terminalContainer || undefined" />
    <el-dialog v-model="filesVisible" title="容器文件" width="900px">
      <FileBrowser :namespace="namespace" :pod="name" :containers="detail?.containers || []" />
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { k8sApi, type PodDetail, type PodItem, type PodMonitor } from '../api'
import StatusTag from '../components/StatusTag.vue'
import EventTable from '../components/EventTable.vue'
import PodLogsDrawer from '../components/PodLogsDrawer.vue'
import WebTerminal from '../components/WebTerminal.vue'
import FileBrowser from '../components/FileBrowser.vue'
import VulnChip from '../components/VulnChip.vue'
import MetricPanel, { type MetricCardDef, type MetricChartDef } from '../components/MetricPanel.vue'
import RangeSwitch from '../components/RangeSwitch.vue'
import { Box, Grid } from '@element-plus/icons-vue'
import { useClusterStore } from '../store/cluster'
import { usePerm } from '../store/perm'
import { confirmDelete } from '../utils/confirm'

const route = useRoute()
const router = useRouter()
const clusterStore = useClusterStore()

// 写权限：只读用户禁用删除按钮（后端仍强制判定）
const perm = usePerm()

const namespace = computed(() => String(route.params.namespace))
const name = computed(() => String(route.params.name))
const detail = ref<PodDetail>()
const tab = ref('containers')
const loading = ref(false)
const logsVisible = ref(false)
const filesVisible = ref(false)
const logsContainer = ref('')
const terminalVisible = ref(false)
const terminalContainer = ref('')

// 打开终端（不指定容器时使用第一个容器）
function openTerminal(container?: string) {
  terminalContainer.value = container || ''
  terminalVisible.value = true
}

// 左侧树选择
const selectedType = ref<'pod' | 'container'>('pod')
const selectedContainer = ref<{ name: string; state?: string; reason?: string; ready?: boolean; restartCount?: number; message?: string }>()

interface TreeNode {
  id: string
  label: string
  type: 'pod' | 'container'
  container?: string
  children?: TreeNode[]
}

const treeData = computed<TreeNode[]>(() => {
  const d = detail.value
  if (!d) return []
  const containers: TreeNode[] = [
    ...(d.containers || []).map((c) => ({ id: 'container-' + c.name, label: c.name, type: 'container' as const, container: c.name })),
    ...(d.initContainers || []).map((c) => ({ id: 'init-' + c.name, label: c.name + ' (init)', type: 'container' as const, container: c.name })),
  ]
  return [{ id: 'pod', label: name.value, type: 'pod' as const, children: containers }]
})

function onTreeClick(node: TreeNode) {
  selectedType.value = node.type
  if (node.type === 'container') {
    const status =
      (detail.value?.containers || []).find((c) => c.name === node.container) ||
      (detail.value?.initContainers || []).find((c) => c.name === node.container)
    selectedContainer.value = {
      name: node.container || '',
      state: status?.state,
      reason: status?.reason,
      ready: status?.ready,
      restartCount: status?.restartCount,
      message: status?.message,
    }
  }
}

function openContainerLogs() {
  logsContainer.value = selectedContainer.value?.name || ''
  logsVisible.value = true
}

// 监控
const range = ref('6h')
const monitor = ref<PodMonitor>()
const monitorLoading = ref(false)

// 空闲 Pod CPU 用量可低至 ~0.0000x 核：固定 2 位小数会显示 0.00，看起来像没数据
const cpuCardDecimals = computed(() => {
  const v = monitor.value?.cpuUsage ?? 0
  return v > 0 && v < 0.01 ? 4 : 2
})

const monitorCards = computed<MetricCardDef[]>(() => [
  { label: 'CPU 使用量', value: monitor.value?.cpuUsage ?? null, unit: '核', decimals: cpuCardDecimals, color: '#409eff' },
  { label: '内存使用', value: monitor.value?.memUsageMi ?? null, unit: 'Mi', color: '#67c23a' },
  { label: '网络接收', value: monitor.value?.netRxMBs ?? null, unit: 'MB/s', color: '#e6a23c' },
  { label: '网络发送', value: monitor.value?.netTxMBs ?? null, unit: 'MB/s', color: '#f56c6c' },
  { label: '磁盘写', value: monitor.value?.diskWriteMBs ?? null, unit: 'MB/s', decimals: 2, color: '#909399' },
  { label: '文件系统', value: monitor.value?.fsUsageMi ?? null, unit: 'Mi', decimals: 1, color: '#9254de' },
])

const monitorCharts = computed<MetricChartDef[]>(() => [
  { title: 'CPU 使用量趋势', series: [{ name: 'CPU', data: monitor.value?.cpuUsageTrend || [], color: '#409eff', unit: '核' }], yAxisName: '核' },
  { title: '内存使用量趋势', series: [{ name: '内存', data: monitor.value?.memUsageTrend || [], color: '#67c23a', unit: 'Mi' }], yAxisName: 'Mi' },
  {
    title: '网络流量',
    series: [
      { name: '接收', data: monitor.value?.netRxTrend || [], color: '#409eff', unit: 'MB/s' },
      { name: '发送', data: monitor.value?.netTxTrend || [], color: '#67c23a', unit: 'MB/s' },
    ],
    yAxisName: 'MB/s',
  },
  { title: '磁盘写趋势', series: [{ name: '磁盘写', data: monitor.value?.diskWriteTrend || [], color: '#909399', unit: 'MB/s' }], yAxisName: 'MB/s' },
  { title: '文件系统使用趋势', series: [{ name: '文件系统', data: monitor.value?.fsUsageTrend || [], color: '#9254de', unit: 'Mi' }], yAxisName: 'Mi' },
])

const podItem = computed<PodItem | undefined>(() =>
  detail.value
    ? { name: detail.value.name, namespace: detail.value.namespace, nodeName: detail.value.nodeName, status: detail.value.status, ready: detail.value.ready, restarts: detail.value.restarts, ip: detail.value.ip, age: detail.value.age, labels: detail.value.labels }
    : undefined,
)

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
    detail.value = await k8sApi.podDetail(namespace.value, name.value)
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

async function loadMonitor() {
  if (!clusterStore.current) return
  monitorLoading.value = true
  try {
    monitor.value = await k8sApi.monitorPod(namespace.value, name.value, range.value)
  } catch {
    /* 拦截器已提示 */
  } finally {
    monitorLoading.value = false
  }
}

async function doDelete() {
  try {
    await confirmDelete(name.value, { title: '删除 Pod' })
  } catch {
    return
  }
  await k8sApi.deletePod(namespace.value, name.value)
  ElMessage.success('已发起删除')
  router.back()
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.actions { display: flex; gap: 6px; }
.sub-title { font-weight: 600; color: #606266; margin-bottom: 8px; }
.monitor-toolbar { margin-bottom: 12px; display: flex; justify-content: flex-end; }

/* 左树右面板布局 */
.detail-layout { display: flex; gap: 16px; min-height: 480px; }
.detail-side {
  width: 240px;
  flex-shrink: 0;
  border: 1px solid var(--kc-border);
  border-radius: var(--kc-card-radius);
  padding: 8px;
  background: #fff;
  max-height: 700px;
  overflow-y: auto;
}
.side-title { font-size: 13px; font-weight: 600; color: #6b7280; padding: 6px 8px 10px; }
.detail-main { flex: 1; min-width: 0; }
.tree-node { display: flex; align-items: center; gap: 6px; font-size: 13px; }
.tree-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  display: inline-block;
  flex-shrink: 0;
}
.dot-Running { background: #67c23a; }
.dot-Pending, .dot-Waiting, .dot-Terminating { background: #e6a23c; }
.dot-Failed, .dot-Error, .dot-CrashLoopBackOff, .dot-ImagePullBackOff { background: #f56c6c; }
.dot-Completed { background: #909399; }
.pod-view { padding: 4px 2px; }
.view-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.view-title { display: flex; align-items: center; gap: 8px; font-size: 16px; font-weight: 600; color: #1f2937; }
</style>
<style>
.mono-img { font-family: ui-monospace, Consolas, monospace; font-size: 12px; }
</style>
