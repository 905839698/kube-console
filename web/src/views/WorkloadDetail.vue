<template>
  <div v-loading="loading">
    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <div class="title">
            <el-page-header :content="`${kindTitle} / ${name}`" @back="goBack" />
          </div>
          <div class="actions">
            <el-button size="small" @click="refresh">刷新</el-button>
            <el-button size="small" type="primary" @click="openFormEdit">可视化编辑</el-button>
            <el-button size="small" plain @click="openYaml">编辑 YAML</el-button>
            <el-button size="small" @click="openScale" v-if="kind !== 'cronjobs' && kind !== 'jobs'">缩放</el-button>
            <el-button size="small" type="warning" plain @click="doRestart" v-if="kind !== 'cronjobs' && kind !== 'jobs'">重启</el-button>
            <el-button size="small" type="danger" plain @click="doDelete">删除</el-button>
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
                <span v-if="data.type === 'pod'" class="status-dot" :class="'dot-' + data.status"></span>
                <el-icon v-else-if="data.type === 'container'" size="12"><Box /></el-icon>
                <el-icon v-else size="14"><Odometer /></el-icon>
                <span class="tree-label">{{ data.label }}</span>
                <!-- Pod 节点 hover 显示"进入 Pod 详情" -->
                <el-tooltip v-if="data.type === 'pod'" content="进入 Pod 详情" placement="right">
                  <el-icon class="tree-enter" @click.stop="goPod(data.pod!)"><Right /></el-icon>
                </el-tooltip>
              </span>
            </template>
          </el-tree>
        </aside>

        <!-- 右侧面板 -->
        <section class="detail-main">
          <!-- 工作负载视图 -->
          <template v-if="selectedType === 'workload'">
            <el-descriptions :column="4" border size="small">
              <el-descriptions-item label="命名空间">{{ detail?.namespace }}</el-descriptions-item>
              <el-descriptions-item label="副本">
                <span :class="{ 'ready-ok': detail?.replicas.readyReplicas === detail?.replicas.replicas }">
                  {{ detail?.replicas.readyReplicas }}/{{ detail?.replicas.replicas }}
                </span>
              </el-descriptions-item>
              <el-descriptions-item label="更新策略">{{ detail?.strategy }}</el-descriptions-item>
              <el-descriptions-item label="运行时长">{{ detail?.age }}</el-descriptions-item>
              <el-descriptions-item label="选择器" :span="2">
                <el-tag v-for="(v, k) in detail?.selector" :key="k" size="small" style="margin-right: 4px">{{ k }}={{ v }}</el-tag>
              </el-descriptions-item>
              <el-descriptions-item label="镜像" :span="2">{{ detail?.images.join(', ') }}</el-descriptions-item>
            </el-descriptions>

            <el-tabs v-model="tab" style="margin-top: 12px">
              <el-tab-pane label="容器" name="containers">
                <el-table :data="detail?.containers || []" size="small" stripe>
                  <el-table-column prop="name" label="名称" min-width="150" />
                  <el-table-column prop="image" label="镜像" min-width="220" show-overflow-tooltip />
                  <el-table-column prop="command" label="启动命令" min-width="160" show-overflow-tooltip />
                  <el-table-column prop="ports" label="端口" width="130" />
                  <el-table-column prop="requests" label="请求" width="130" />
                  <el-table-column prop="limits" label="限制" width="130" />
                  <el-table-column prop="readyProbe" label="就绪探针" min-width="140" />
                  <el-table-column prop="liveProbe" label="存活探针" min-width="140" />
                </el-table>
              </el-tab-pane>
              <el-tab-pane label="事件" name="events">
                <EventTable :events="detail?.events || []" />
              </el-tab-pane>
              <el-tab-pane label="YAML" name="yaml">
                <div class="yaml-pane">
                  <YamlEditor v-model="detailYaml" readonly />
                </div>
              </el-tab-pane>
              <el-tab-pane label="监控" name="monitor" lazy>
                <div class="monitor-toolbar">
                  <RangeSwitch v-model="range" @change="loadMonitor" />
                </div>
                <MetricPanel :cards="monitorCards" :charts="monitorCharts" :loading="monitorLoading" />
              </el-tab-pane>
            </el-tabs>
          </template>

          <!-- Pod 视图 -->
          <template v-else-if="selectedType === 'pod'">
            <div v-loading="podLoading" class="pod-view">
              <div class="view-header">
                <span class="view-title">
                  <span class="status-dot" :class="'dot-' + selectedPod?.status"></span>
                  {{ selectedPod?.name }}
                </span>
                <div>
                  <el-button size="small" type="success" plain @click="openTerminal(selectedPod!)">终端</el-button>
                  <el-button size="small" @click="openPodLogs(selectedPod!)">日志</el-button>
                  <el-button size="small" type="primary" @click="goPod(selectedPod!)">进入 Pod 详情</el-button>
                </div>
              </div>
              <el-descriptions :column="4" border size="small">
                <el-descriptions-item label="状态"><StatusTag :status="selectedPod?.status || ''" /></el-descriptions-item>
                <el-descriptions-item label="就绪">{{ selectedPod?.ready }}</el-descriptions-item>
                <el-descriptions-item label="重启">{{ selectedPod?.restarts }}</el-descriptions-item>
                <el-descriptions-item label="IP">{{ selectedPod?.ip || '-' }}</el-descriptions-item>
                <el-descriptions-item label="节点">{{ selectedPod?.nodeName || '-' }}</el-descriptions-item>
                <el-descriptions-item label="运行时长">{{ selectedPod?.age }}</el-descriptions-item>
              </el-descriptions>

              <div class="sub-title">容器</div>
              <el-table :data="podDetail?.containers || []" size="small" stripe>
                <el-table-column prop="name" label="名称" min-width="140" />
                <el-table-column label="状态" width="120">
                  <template #default="{ row }"><StatusTag :status="row.state" :text="row.reason || row.state" /></template>
                </el-table-column>
                <el-table-column prop="ready" label="就绪" width="70" align="center" />
                <el-table-column prop="restartCount" label="重启" width="70" align="center" />
                <el-table-column prop="message" label="信息" min-width="220" show-overflow-tooltip />
              </el-table>

              <div class="sub-title">事件</div>
              <EventTable :events="podDetail?.events || []" />
            </div>
          </template>

          <!-- 容器视图 -->
          <template v-else-if="selectedType === 'container'">
            <div class="pod-view">
              <div class="view-header">
                <span class="view-title">
                  <el-icon><Box /></el-icon>
                  {{ selectedContainer?.name }}
                  <span class="view-sub">{{ selectedPod?.name }}</span>
                </span>
                <div>
                  <el-button size="small" type="success" plain @click="openTerminal(selectedPod!, selectedContainer?.name)">终端</el-button>
                  <el-button size="small" @click="openPodLogs(selectedPod!, selectedContainer?.name)">查看日志</el-button>
                </div>
              </div>
              <el-descriptions :column="2" border size="small">
                <el-descriptions-item label="镜像">{{ containerConfig?.image || '-' }}</el-descriptions-item>
                <el-descriptions-item label="状态">
                  <template v-if="containerStatus">
                    <StatusTag :status="containerStatus.state" :text="containerStatus.reason || containerStatus.state" />
                  </template>
                  <span v-else>-</span>
                </el-descriptions-item>
                <el-descriptions-item label="启动命令">{{ containerConfig?.command || '-' }}</el-descriptions-item>
                <el-descriptions-item label="端口">{{ containerConfig?.ports || '-' }}</el-descriptions-item>
                <el-descriptions-item label="资源请求">{{ containerConfig?.requests || '-' }}</el-descriptions-item>
                <el-descriptions-item label="资源限制">{{ containerConfig?.limits || '-' }}</el-descriptions-item>
                <el-descriptions-item label="就绪探针">{{ containerConfig?.readyProbe || '-' }}</el-descriptions-item>
                <el-descriptions-item label="存活探针">{{ containerConfig?.liveProbe || '-' }}</el-descriptions-item>
                <el-descriptions-item label="重启次数">{{ containerStatus?.restartCount ?? '-' }}</el-descriptions-item>
                <el-descriptions-item label="就绪">{{ containerStatus?.ready ?? '-' }}</el-descriptions-item>
              </el-descriptions>
            </div>
          </template>
        </section>
      </div>
    </el-card>

    <YamlDialog v-model="editVisible" :yaml="editYaml" :readonly="false" @applied="load" />

    <!-- 可视化编辑（表单|YAML 双视图） -->
    <el-drawer v-model="formEditVisible" :title="`可视化编辑 - ${kindTitle} / ${name}`" size="70%" destroy-on-close>
      <ObjectEditor v-if="formEditYaml !== null" :kind="kind" :yaml="formEditYaml" :namespace="namespace" @saved="onFormSaved" @cancel="formEditVisible = false" />
    </el-drawer>

    <!-- Deployment 历史版本回滚 -->
    <el-dialog v-model="rolloutsVisible" title="Deployment 历史版本（回滚）" width="780px">
      <el-table :data="rollouts" v-loading="rolloutsLoading" size="small">
        <el-table-column prop="revision" label="版本" width="90" align="center">
          <template #default="{ row }">
            <el-tag v-if="row.current" type="success" size="small">当前 {{ row.revision }}</el-tag>
            <span v-else>{{ row.revision }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="image" label="镜像" min-width="240" show-overflow-tooltip />
        <el-table-column prop="replicas" label="副本" width="70" align="center" />
        <el-table-column prop="age" label="创建于" width="90" />
        <el-table-column prop="changeCause" label="变更说明" min-width="140" show-overflow-tooltip />
        <el-table-column label="操作" width="130" fixed="right">
          <template #default="{ row }">
            <el-button size="small" type="warning" plain :disabled="row.current" @click="doRollback(row)">回滚到此版本</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-dialog>

    <el-dialog v-model="scaleVisible" title="调整副本数" width="420px">
      <el-form label-width="90px">
        <el-form-item label="副本数" required>
          <el-input-number v-model="scaleReplicas" :min="0" :max="500" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="scaleVisible = false">取消</el-button>
        <el-button type="primary" :loading="scaling" @click="doScale">确定</el-button>
      </template>
    </el-dialog>

    <!-- Pod 日志抽屉 -->
    <PodLogsDrawer v-model="logsVisible" :pod="logsPod" :container="logsContainer" />
    <WebTerminal
      v-if="terminalPod"
      v-model:visible="terminalVisible"
      :namespace="terminalPod.namespace || 'default'"
      :pod="terminalPod.name"
      :container="terminalContainer || undefined"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Box, Odometer, Right } from '@element-plus/icons-vue'
import { k8sApi, type PodDetail, type PodItem, type RolloutItem, type WorkloadDetail, type WorkloadMonitor } from '../api'
import { cronNextRun } from '../utils/kube-validators'
import StatusTag from '../components/StatusTag.vue'
import YamlEditor from '../components/YamlEditor.vue'
import YamlDialog from '../components/YamlDialog.vue'
import ObjectEditor from '../components/ObjectEditor.vue'
import EventTable from '../components/EventTable.vue'
import PodLogsDrawer from '../components/PodLogsDrawer.vue'
import WebTerminal from '../components/WebTerminal.vue'
import MetricPanel, { type MetricCardDef, type MetricChartDef } from '../components/MetricPanel.vue'
import RangeSwitch from '../components/RangeSwitch.vue'
import { useClusterStore } from '../store/cluster'

const route = useRoute()
const router = useRouter()
const clusterStore = useClusterStore()

const kind = computed(() => String(route.params.kind))

// ------------------- Deployment 回滚 + CronJob 下次执行 -------------------
function fmtNextRun(d: any): string {
  const schedule = d?.schedule || d?.spec?.schedule
  if (!schedule) return ''
  const next = cronNextRun(String(schedule))
  return next ? next.toLocaleString() : ''
}

const rolloutsVisible = ref(false)
const rolloutsLoading = ref(false)
const rollouts = ref<RolloutItem[]>([])
const nextRun = computed(() => (kind.value === 'cronjobs' ? fmtNextRun(detail.value) : ''))

async function openRollouts() {
  rolloutsVisible.value = true
  rolloutsLoading.value = true
  try {
    rollouts.value = await k8sApi.rollouts(kind.value, namespace.value, name.value)
  } finally {
    rolloutsLoading.value = false
  }
}

async function doRollback(row: RolloutItem) {
  await ElMessageBox.confirm(`确定回滚到版本 ${row.revision}（镜像 ${row.image}）？将触发滚动更新。`, '回滚确认', { type: 'warning' })
  await k8sApi.rollback(namespace.value, name.value, row.revision)
  ElMessage.success(`已回滚到版本 ${row.revision}`)
  rolloutsVisible.value = false
  await refresh()
}
const name = computed(() => String(route.params.name))
const namespace = computed(() => String(route.query.namespace || 'default'))
const kindTitle = computed(() => {
  const titles: Record<string, string> = {
    deployments: 'Deployment', statefulsets: 'StatefulSet', daemonsets: 'DaemonSet',
    cronjobs: 'CronJob', jobs: 'Job', replicasets: 'ReplicaSet', replicationcontrollers: 'ReplicationController',
  }
  return titles[kind.value] || kind.value
})

const detail = ref<WorkloadDetail>()
const detailYaml = ref('')
const tab = ref('containers')
const loading = ref(false)
const editVisible = ref(false)
const editYaml = ref('')
const formEditVisible = ref(false)
const formEditYaml = ref<string | null>(null)
const scaleVisible = ref(false)
const scaleReplicas = ref(1)
const scaling = ref(false)
const logsVisible = ref(false)
const logsPod = ref<PodItem>()
const logsContainer = ref('')

// 左侧树选择状态
const selectedType = ref<'workload' | 'pod' | 'container'>('workload')
const selectedPod = ref<PodItem>()
const selectedContainer = ref<{ name: string }>()
const podDetail = ref<PodDetail>()
const podLoading = ref(false)

// 监控
const range = ref('6h')
const monitor = ref<WorkloadMonitor>()
const monitorLoading = ref(false)

const monitorCards = computed<MetricCardDef[]>(() => {
  const m = monitor.value
  // 有 limit 时显示使用率（%）；无 limit（覆盖不全）时显示绝对值（核 / Mi），避免「使用率为空」
  const cpuPct = m?.cpuUsagePct ?? null
  const memPct = m?.memUsagePct ?? null
  return [
    cpuPct != null
      ? { label: 'CPU 使用率', value: cpuPct, unit: '%', color: '#00b8a9' }
      : { label: 'CPU 用量', value: m?.cpuUsage ?? null, unit: '核', decimals: 2, color: '#00b8a9' },
    memPct != null
      ? { label: '内存使用率', value: memPct, unit: '%', color: '#67c23a' }
      : { label: '内存用量', value: m?.memUsageMi ?? null, unit: 'Mi', decimals: 0, color: '#67c23a' },
    { label: 'Pod 数量', value: m?.podCount ?? null, unit: '个', decimals: 0, color: '#e6a23c' },
    { label: '磁盘写', value: m?.diskWriteMBs ?? null, unit: 'MB/s', decimals: 2, color: '#909399' },
    { label: '网络接收', value: m?.netRxMBs ?? null, unit: 'MB/s', decimals: 2, color: '#f56c6c' },
    { label: '网络发送', value: m?.netTxMBs ?? null, unit: 'MB/s', decimals: 2, color: '#9254de' },
  ]
})

const monitorCharts = computed<MetricChartDef[]>(() => {
  const cpuIsPct = monitor.value?.cpuTrendIsPct ?? false
  const memIsPct = monitor.value?.memTrendIsPct ?? false
  const cpuUnit = cpuIsPct ? '%' : '核'
  const memUnit = memIsPct ? '%' : 'Mi'
  return [
    { title: cpuIsPct ? 'CPU 使用率趋势' : 'CPU 用量趋势', series: [{ name: 'CPU', data: monitor.value?.cpuUsageTrend || [], color: '#00b8a9', unit: cpuUnit }], yAxisName: cpuUnit },
    { title: memIsPct ? '内存使用率趋势' : '内存用量趋势', series: [{ name: '内存', data: monitor.value?.memUsageTrend || [], color: '#67c23a', unit: memUnit }], yAxisName: memUnit },
    { title: '磁盘写趋势', series: [{ name: '磁盘写', data: monitor.value?.diskWriteTrend || [], color: '#909399', unit: 'MB/s' }], yAxisName: 'MB/s' },
    {
      title: '网络流量',
      series: [
        { name: '接收', data: monitor.value?.netRxTrend || [], color: '#f56c6c', unit: 'MB/s' },
        { name: '发送', data: monitor.value?.netTxTrend || [], color: '#9254de', unit: 'MB/s' },
      ],
      yAxisName: 'MB/s',
    },
  ]
})

// ---------- 左侧树 ----------
interface TreeNode {
  id: string
  label: string
  type: 'workload' | 'pod' | 'container'
  pod?: PodItem
  container?: string
  status?: string
  children?: TreeNode[]
}

const treeData = computed<TreeNode[]>(() => {
  const d = detail.value
  if (!d) return []
  const root: TreeNode = {
    id: 'workload',
    label: `${d.name} (${d.replicas.readyReplicas}/${d.replicas.replicas})`,
    type: 'workload',
    children: (d.pods || []).map((p) => ({
      id: 'pod-' + p.name,
      label: p.name,
      type: 'pod' as const,
      pod: p,
      status: p.status,
      children: (d.containers || []).map((c) => ({
        id: 'container-' + p.name + '-' + c.name,
        label: c.name,
        type: 'container' as const,
        pod: p,
        container: c.name,
      })),
    })),
  }
  return [root]
})

const containerConfig = computed(() => {
  if (!selectedContainer.value) return undefined
  return (detail.value?.containers || []).find((c) => c.name === selectedContainer.value?.name)
})

const containerStatus = computed(() => {
  if (!selectedContainer.value) return undefined
  return (podDetail.value?.containers || []).find((c) => c.name === selectedContainer.value?.name)
})

async function onTreeClick(node: TreeNode) {
  selectedType.value = node.type
  if (node.type === 'workload') {
    selectedPod.value = undefined
    selectedContainer.value = undefined
    podDetail.value = undefined
    return
  }
  selectedPod.value = node.pod
  selectedContainer.value = node.type === 'container' ? { name: node.container || '' } : undefined
  if (node.pod) {
    await loadPodDetail(node.pod)
  }
}

async function loadPodDetail(pod: PodItem) {
  podLoading.value = true
  try {
    podDetail.value = await k8sApi.podDetail(pod.namespace, pod.name)
  } catch {
    podDetail.value = undefined
  } finally {
    podLoading.value = false
  }
}

// ---------- 数据加载 ----------
onMounted(() => {
  load()
  loadMonitor() // 预加载监控数据
})

watch(tab, (v) => {
  if (v === 'monitor' && !monitor.value) loadMonitor()
})

async function load() {
  if (!clusterStore.current) return
  loading.value = true
  try {
    detail.value = await k8sApi.workloadDetail(kind.value, namespace.value, name.value)
    const { yaml } = await k8sApi.getYaml(kind.value, namespace.value, name.value)
    detailYaml.value = yaml
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
    monitor.value = await k8sApi.monitorWorkload(kind.value, namespace.value, name.value, range.value)
  } catch {
    /* 拦截器已提示 */
  } finally {
    monitorLoading.value = false
  }
}

function refresh() {
  load()
}

function goBack() {
  router.push({ path: `/workloads/${kind.value}`, query: { namespace: namespace.value } })
}

function goPod(pod: PodItem) {
  router.push({ path: `/pods/${pod.namespace}/${pod.name}` })
}

function openPodLogs(pod: PodItem, container?: string) {
  logsPod.value = pod
  logsContainer.value = container || ''
  logsVisible.value = true
}

// 打开 Web 终端（不指定容器时使用第一个容器）
const terminalVisible = ref(false)
const terminalPod = ref<PodItem>()
const terminalContainer = ref('')
function openTerminal(pod: PodItem, container?: string) {
  terminalPod.value = pod
  terminalContainer.value = container || ''
  terminalVisible.value = true
}

async function openYaml() {
  const { yaml } = await k8sApi.getYaml(kind.value, namespace.value, name.value)
  editYaml.value = yaml
  editVisible.value = true
}

async function openFormEdit() {
  const { yaml } = await k8sApi.getYaml(kind.value, namespace.value, name.value)
  formEditYaml.value = yaml
  formEditVisible.value = true
}

function onFormSaved() {
  formEditVisible.value = false
  load()
}

function openScale() {
  scaleReplicas.value = detail.value?.replicas.replicas ?? 1
  scaleVisible.value = true
}

async function doScale() {
  scaling.value = true
  try {
    await k8sApi.scaleWorkload(kind.value, namespace.value, name.value, scaleReplicas.value)
    ElMessage.success('副本数已更新')
    scaleVisible.value = false
    load()
  } finally {
    scaling.value = false
  }
}

async function doRestart() {
  try {
    await ElMessageBox.confirm(`确定重启 ${name.value}？将触发滚动更新。`, '重启', { type: 'warning' })
  } catch {
    return
  }
  await k8sApi.restartWorkload(kind.value, namespace.value, name.value)
  ElMessage.success('已触发重启')
}

async function doDelete() {
  try {
    await ElMessageBox.confirm(`确定删除 ${kindTitle.value} ${name.value}？`, '删除', { type: 'warning' })
  } catch {
    return
  }
  await k8sApi.deleteWorkload(kind.value, namespace.value, name.value)
  ElMessage.success('已删除')
  goBack()
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.actions { display: flex; gap: 6px; }
.title :deep(.el-page-header__title) { font-size: 16px; }
.ready-ok { color: #67c23a; font-weight: 600; }
.yaml-pane { height: 480px; }
.monitor-toolbar { margin-bottom: 12px; display: flex; justify-content: flex-end; }

/* 左树右面板布局 */
.detail-layout { display: flex; gap: 16px; min-height: 520px; }
.detail-side {
  width: 260px;
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
.tree-node { display: flex; align-items: center; gap: 6px; font-size: 13px; flex: 1; }
.tree-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
/* 进入 Pod 按钮：默认隐藏，hover 节点时显示 */
.tree-enter {
  color: var(--el-color-primary);
  cursor: pointer;
  opacity: 0;
  transition: opacity 0.15s;
  flex-shrink: 0;
}
.el-tree-node__content:hover .tree-enter { opacity: 1; }
.tree-enter:hover { transform: scale(1.15); }
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

/* Pod/容器视图 */
.pod-view { padding: 4px 2px; }
.view-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.view-title { display: flex; align-items: center; gap: 8px; font-size: 16px; font-weight: 600; color: #1f2937; }
.view-sub { font-size: 12px; color: #9ca3af; font-weight: 400; }
.sub-title { font-weight: 600; color: #606266; margin: 16px 0 8px; }
</style>
