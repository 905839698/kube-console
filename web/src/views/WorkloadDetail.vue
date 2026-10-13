<template>
  <div v-loading="loading" class="kc-page">
    <!-- 顶部：标题 + 操作 + 发布进度 -->
    <el-card shadow="never">
      <div class="kc-top-head">
        <span class="kc-section-title"><el-icon><Box /></el-icon>{{ kindTitle }}详情 / <span class="mono">{{ name }}</span></span>
        <div class="actions">
          <el-button size="small" @click="refresh">刷新</el-button>
          <el-button size="small" type="primary" @click="openFormEdit">编辑</el-button>
          <el-button size="small" @click="openYaml">YAML</el-button>
          <el-button size="small" v-if="kind !== 'cronjobs' && kind !== 'jobs'" :disabled="noWrite" @click="openScale">缩放</el-button>
          <el-button size="small" type="warning" v-if="kind !== 'pods'" @click="imageVisible = true">调整镜像版本</el-button>
          <el-button size="small" type="success" v-if="['deployments', 'statefulsets', 'daemonsets'].includes(kind)" @click="collectVisible = true">日志采集</el-button>
          <el-button size="small" type="warning" v-if="kind !== 'cronjobs' && kind !== 'jobs'" :disabled="noWrite" @click="doRestart">重启</el-button>
          <el-button size="small" type="danger" :disabled="noWrite" @click="doDelete">删除</el-button>
        </div>
      </div>

      <div class="kc-status-strip">
        <StatusTag :status="replicasOk ? 'Ready' : 'Pending'" :text="replicasOk ? '已完成' : '进行中'" />
        <span class="kc-strip-label">发布进度</span>
        <div class="kc-progress-mini">
          <span class="kc-progress-track"><i :style="{ width: replicasPct + '%' }" /></span>
          <span class="kc-progress-text">{{ detail?.replicas.readyReplicas }} / {{ detail?.replicas.replicas }}</span>
        </div>
        <span class="kc-strip-item">期望 <b>{{ detail?.replicas.replicas ?? '-' }}</b></span>
        <span class="kc-strip-item">就绪 <b>{{ detail?.replicas.readyReplicas ?? '-' }}</b></span>
        <span class="kc-strip-item">可用 <b>{{ detail?.replicas.available ?? '-' }}</b></span>
        <span class="kc-strip-item">最新 <b>{{ detail?.replicas.updated ?? '-' }}</b></span>
        <span class="kc-strip-item">不可用 <b>{{ (detail?.replicas.replicas ?? 0) - (detail?.replicas.available ?? 0) }}</b></span>
      </div>
    </el-card>

    <div class="kc-two-col">
      <!-- 基本信息 -->
      <el-card shadow="never">
        <div class="kc-info-grid" style="margin-top: 2px">
          <div class="kc-info-item"><span class="kc-info-label">集群:</span><span class="kc-info-value">{{ clusterStore.current }}</span></div>
          <div class="kc-info-item"><span class="kc-info-label">名称空间:</span><span class="kc-info-value mono">{{ detail?.namespace }}</span></div>
          <div class="kc-info-item"><span class="kc-info-label">名称:</span><span class="kc-info-value mono">{{ detail?.name }}</span></div>
          <div class="kc-info-item"><span class="kc-info-label">创建时间:</span><span class="kc-info-value">{{ detail?.age || '-' }} 前</span></div>
          <div class="kc-info-item"><span class="kc-info-label">类型:</span><span class="kc-info-value">{{ kindTitle }} <el-tag size="small" type="info" effect="plain">{{ kind }}</el-tag></span></div>
        </div>
        <el-collapse v-model="moreOpen" class="kc-more">
          <el-collapse-item name="more">
            <template #title><span class="kc-more-title">更多细节</span></template>
            <div class="kc-info-grid" style="margin-top: 0">
              <div class="kc-info-item"><span class="kc-info-label">更新策略:</span><span class="kc-info-value">{{ detail?.strategy || '-' }}</span></div>
              <div class="kc-info-item" v-if="kind === 'cronjobs'"><span class="kc-info-label">调度:</span><span class="kc-info-value">{{ detail?.schedule }}<span v-if="nextRun" class="next-run"> · 下次执行 {{ nextRun }}</span></span></div>
              <div class="kc-info-item" style="grid-column: 1 / -1"><span class="kc-info-label">选择器:</span><span class="kc-info-value"><el-tag v-for="(v, k) in detail?.selector" :key="k" size="small" style="margin-right: 4px">{{ k }}={{ v }}</el-tag></span></div>
              <div class="kc-info-item" style="grid-column: 1 / -1"><span class="kc-info-label">镜像:</span><span class="kc-info-value mono img">{{ detail?.images.join(', ') || '-' }}</span></div>
            </div>
          </el-collapse-item>
        </el-collapse>
      </el-card>

      <!-- 关联事件 -->
      <el-card shadow="never">
        <template #header><span class="kc-section-title">关联事件</span></template>
        <el-table v-if="detail?.events?.length" border :data="detail.events" size="small" max-height="260">
          <el-table-column prop="reason" label="原因" width="140" />
          <el-table-column prop="lastSeen" label="时间" width="170" />
          <el-table-column prop="count" label="次数" width="70" align="center" />
          <el-table-column prop="message" label="信息" min-width="220" show-overflow-tooltip />
        </el-table>
        <el-empty v-else :image-size="56" :description="`没有与 ${name} 关联的事件`" />
      </el-card>
    </div>

    <!-- 三栏主体：历史版本 | 容器组列表 | 选中容器组详情 -->
    <div class="kc-three-col">
      <!-- 列 1：Deployment 历史版本时间线；其他类型显示资源导航 -->
      <el-card shadow="never" class="kc-rs-col">
        <template #header>
          <div class="rs-head">
            <span class="kc-section-title">{{ kind === 'deployments' ? '历史版本' : '资源导航' }}</span>
            <el-tag v-if="kind === 'deployments'" size="small" type="info" effect="plain">共 {{ rollouts.length }} 个版本</el-tag>
          </div>
        </template>
        <el-scrollbar max-height="620px">
          <div v-loading="rolloutsLoading">
            <template v-if="kind === 'deployments'">
              <div
                v-for="r in rollouts" :key="r.revision"
                class="rs-item" :class="{ 'rs-current': r.current }"
                @click="selectedRs = r.revision"
              >
                <div class="rs-top">
                  <span>已创建: {{ r.age }}前</span>
                  <span class="rs-rev">#{{ r.revision }}</span>
                </div>
                <div class="rs-name mono">{{ r.name }}</div>
                <div class="rs-meta">
                  期望: {{ r.replicas }}
                  <el-tag v-if="r.current" size="small" type="success" effect="dark">正常</el-tag>
                  <el-tag v-else size="small" type="info" effect="plain">非当前版本</el-tag>
                  <el-tag v-if="!r.current && r.replicas === 0" size="small" type="info" effect="plain">已缩容</el-tag>
                </div>
                <div class="rs-img mono" :title="r.image">{{ r.image }}</div>
                <div class="rs-actions" v-if="selectedRs === r.revision">
                  <el-button size="small" type="warning" :disabled="r.current || noWrite" @click.stop="doRollback(r)">回滚</el-button>
                  <el-button size="small" @click.stop="showRsYaml(r)">YAML</el-button>
                  <el-button size="small" type="danger" :disabled="r.current || noWrite" @click.stop="deleteRs(r)">删除</el-button>
                </div>
              </div>
              <el-empty v-if="!rolloutsLoading && !rollouts.length" description="无历史版本" :image-size="48" />
            </template>
            <el-tree
              v-else
              :data="treeData" node-key="id"
              :props="{ label: 'label', children: 'children' }"
              highlight-current default-expand-all
              @node-click="onTreeClick"
            >
              <template #default="{ data }">
                <span class="tree-node">
                  <span v-if="data.type === 'pod'" class="status-dot" :class="'dot-' + data.status"></span>
                  <el-icon v-else-if="data.type === 'container'" size="12"><Box /></el-icon>
                  <el-icon v-else size="14"><Odometer /></el-icon>
                  <span class="tree-label">{{ data.label }}</span>
                </span>
              </template>
            </el-tree>
          </div>
        </el-scrollbar>
      </el-card>

      <!-- 列 2：容器组列表 -->
      <el-card shadow="never" class="kc-pod-col">
        <template #header>
          <div class="rs-head">
            <span class="kc-section-title">容器组</span>
            <el-button size="small" text type="danger" :disabled="noWrite || !detail?.pods?.length" @click="deleteAllPods">删除容器组</el-button>
          </div>
        </template>
        <el-scrollbar max-height="620px">
          <div
            v-for="p in detail?.pods || []" :key="p.name"
            class="pod-item" :class="{ 'pod-active': selectedPod?.name === p.name }"
            @click="selectPod(p)"
          >
            <div class="pod-item-head">
              <span class="mono pod-item-name" :title="p.name">{{ p.name }}</span>
              <StatusTag :status="p.status" :text="p.status === 'Running' ? 'Ready' : p.status" />
            </div>
            <div class="pod-item-meta">{{ p.age }}前 · 容器组IP {{ p.ip || '-' }}</div>
            <div class="pod-item-meta">节点 {{ p.nodeName || '-' }} · 重启 {{ p.restarts }}</div>
          </div>
          <el-empty v-if="!detail?.pods?.length" description="无容器组" :image-size="48" />
        </el-scrollbar>
      </el-card>

      <!-- 列 3：选中容器组详情 -->
      <el-card shadow="never" class="kc-pod-detail" v-loading="podLoading">
        <template v-if="selectedPod">
          <!-- CPU / 内存趋势 -->
          <div class="pd-charts">
            <div class="pd-chart">
              <div class="pd-chart-title"><el-tag size="small" type="success" effect="plain">Prometheus</el-tag> CPU（核）</div>
              <MetricChart :series="[{ name: 'CPU', data: podMonitor?.cpuUsageTrend || [], color: '#2ba471', unit: '核' }]" height="150px" y-axis-name="核" />
            </div>
            <div class="pd-chart">
              <div class="pd-chart-title"><el-tag size="small" type="success" effect="plain">Prometheus</el-tag> 内存（Mi）</div>
              <MetricChart :series="[{ name: '内存', data: podMonitor?.memUsageTrend || [], color: '#2b4b83', unit: 'Mi' }]" height="150px" y-axis-name="Mi" />
            </div>
          </div>

          <!-- 容器组事件 -->
          <el-table v-if="podDetail?.events?.length" border :data="podDetail.events" size="small" max-height="200" style="margin: 10px 0 14px">
            <el-table-column prop="reason" label="原因" width="130" />
            <el-table-column prop="lastSeen" label="时间" width="160" />
            <el-table-column prop="count" label="次数" width="64" align="center" />
            <el-table-column prop="message" label="信息" min-width="220" show-overflow-tooltip />
          </el-table>

          <!-- 容器组标题栏（深蓝条） -->
          <div class="pod-bar">
            <span class="pod-bar-label">容器组:</span>
            <span class="pod-bar-name mono">{{ selectedPod.name }}</span>
            <span class="pod-bar-age">{{ selectedPod.age }} 前</span>
            <span class="spacer"></span>
            <el-button size="small" text @click="showPodYaml">YAML</el-button>
            <el-button size="small" text type="danger" :disabled="noWrite" @click="doDeletePod">删除</el-button>
            <el-dropdown trigger="click" @command="onPodMore">
              <el-button size="small" text>更多<el-icon><ArrowDown /></el-icon></el-button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="logs">日志</el-dropdown-item>
                  <el-dropdown-item command="terminal">终端</el-dropdown-item>
                  <el-dropdown-item command="files">文件</el-dropdown-item>
                  <el-dropdown-item command="full">进入 Pod 完整详情</el-dropdown-item>
                  <el-dropdown-item command="evict" divided :disabled="noWrite">驱除（尊重 PDB）</el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </div>

          <!-- 所在节点 / IP / 状态 -->
          <div class="pd-info-row">
            <span>所在节点: <span class="mono">{{ selectedPod.nodeName || '-' }}</span></span>
            <span>容器组IP: <span class="mono">{{ selectedPod.ip || '-' }}</span></span>
            <span>状态: <b>{{ podDetail?.phase || selectedPod.status }}</b></span>
            <span>重启次数: <b>{{ selectedPod.restarts }}</b></span>
          </div>

          <!-- 条件时间线 -->
          <div class="pd-timeline">
            <span v-for="t in podTimeline" :key="t.label" class="pd-tl-item" :class="{ 'pd-tl-bad': !t.ok }">
              <span class="pd-tl-dot"></span>{{ t.label }}
              <small v-if="t.time">{{ t.time }}</small>
            </span>
          </div>

          <!-- 容器卡片 -->
          <div v-for="c in podDetail?.containers || []" :key="c.name" class="ctr-card">
            <div class="ctr-head">
              <el-icon><Box /></el-icon>
              <span class="ctr-name">容器 <b>{{ c.name }}</b></span>
              <span class="spacer"></span>
              <StatusTag :status="c.state" :text="c.reason || c.state" />
            </div>
            <div class="ctr-line"><span class="ctr-label">镜像</span><span class="mono ctr-img">{{ configOf(c.name)?.image || '-' }}</span></div>
            <div class="ctr-line">
              <span class="ctr-label">当前状态</span>
              <el-tag size="small" :type="c.state === 'Running' ? 'success' : 'info'" effect="plain">{{ c.state === 'Running' ? 'Started' : c.state }}</el-tag>
              <el-tag size="small" :type="c.ready ? 'success' : 'warning'" effect="plain">{{ c.ready ? 'Ready' : 'NotReady' }}</el-tag>
              <el-tag size="small" type="info" effect="plain">{{ c.state }}</el-tag>
              <el-tag size="small" type="info" effect="plain">重启 {{ c.restartCount }} 次</el-tag>
            </div>
            <div class="ctr-line">
              <span class="ctr-label">端口</span>
              <el-tag v-for="pt in portsOf(c.name)" :key="pt" size="small" class="port-chip">{{ pt }}</el-tag>
              <span v-if="!portsOf(c.name).length" class="ctr-label">-</span>
              <span class="spacer"></span>
              <el-button size="small" type="primary" @click="openPodLogs(selectedPod, c.name)">下载日志</el-button>
              <el-button size="small" @click="openPodLogs(selectedPod, c.name)">追踪日志</el-button>
              <el-button size="small" type="success" :disabled="noWrite" @click="openTerminal(selectedPod, c.name)">bash</el-button>
              <el-button size="small" @click="openFiles(c.name)">文件</el-button>
            </div>
            <div v-if="c.message" class="ctr-msg">{{ c.message }}</div>
          </div>
          <div v-if="podDetail?.initContainers?.length" class="pd-init">
            Init 容器:
            <el-tag v-for="ic in podDetail.initContainers" :key="ic.name" size="small" :type="ic.state === 'Completed' || ic.ready ? 'success' : 'info'" effect="plain" style="margin-right: 4px">{{ ic.name }} ({{ ic.state }})</el-tag>
          </div>
        </template>
        <el-empty v-else :image-size="64" description="从左侧选择一个容器组查看详情" />
      </el-card>
    </div>

    <!-- 工作负载级监控 -->
    <el-card shadow="never">
      <template #header>
        <div class="rs-head">
          <span class="kc-section-title">资源监控</span>
          <span class="spacer"></span>
          <RangeSwitch v-model="range" @change="loadMonitor" />
        </div>
      </template>
      <MetricPanel :cards="monitorCards" :charts="monitorCharts" :loading="monitorLoading" />
    </el-card>

    <YamlDialog v-model="editVisible" :yaml="editYaml" :readonly="false" @applied="load" />
    <!-- 只读 YAML 查看（RS / Pod） -->
    <YamlDialog v-model="viewVisible" :yaml="viewYaml" :readonly="true" />

    <!-- 可视化编辑（表单|YAML 双视图） -->
    <el-drawer v-model="formEditVisible" :title="`可视化编辑 - ${kindTitle} / ${name}`" size="70%" destroy-on-close>
      <ObjectEditor v-if="formEditYaml !== null" :kind="kind" :yaml="formEditYaml" :namespace="namespace" @saved="onFormSaved" @cancel="formEditVisible = false" />
    </el-drawer>

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

    <!-- 调整镜像版本（tag 从已配置仓库拉取） -->
    <ImageUpdateDialog v-model="imageVisible" :kind="kind" :namespace="namespace" :name="name" @saved="load" />

    <!-- 容器内日志采集（Fluent Bit Sidecar） -->
    <LogCollectionDialog
      v-model="collectVisible"
      :kind="kind"
      :namespace="namespace"
      :name="name"
      :containers="(detail?.containers || []).map((c) => c.name)"
      :no-write="noWrite"
      @saved="load"
    />

    <!-- Pod 日志抽屉 / 终端 / 文件 -->
    <PodLogsDrawer v-model="logsVisible" :pod="logsPod" :container="logsContainer" />
    <WebTerminal
      v-if="terminalPod"
      v-model:visible="terminalVisible"
      :namespace="terminalPod.namespace || 'default'"
      :pod="terminalPod.name"
      :container="terminalContainer || undefined"
    />
    <el-dialog v-model="filesVisible" title="容器文件" width="900px">
      <FileBrowser :namespace="namespace" :pod="selectedPod?.name || ''" :containers="(podDetail?.containers || []).map((c) => c.name)" />
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Box, Odometer, ArrowDown } from '@element-plus/icons-vue'
import { k8sApi, type PodDetail, type PodItem, type PodMonitor, type RolloutItem, type WorkloadDetail, type WorkloadMonitor } from '../api'
import { cronNextRun } from '../utils/kube-validators'
import StatusTag from '../components/StatusTag.vue'
import YamlDialog from '../components/YamlDialog.vue'
import ObjectEditor from '../components/ObjectEditor.vue'
import PodLogsDrawer from '../components/PodLogsDrawer.vue'
import WebTerminal from '../components/WebTerminal.vue'
import FileBrowser from '../components/FileBrowser.vue'
import ImageUpdateDialog from '../components/ImageUpdateDialog.vue'
import LogCollectionDialog from '../components/LogCollectionDialog.vue'
import MetricPanel, { type MetricCardDef, type MetricChartDef } from '../components/MetricPanel.vue'
import MetricChart from '../components/MetricChart.vue'
import RangeSwitch from '../components/RangeSwitch.vue'
import { useClusterStore } from '../store/cluster'
import { usePerm } from '../store/perm'
import { confirmDelete } from '../utils/confirm'

const route = useRoute()
const router = useRouter()
const clusterStore = useClusterStore()

// 写权限（按「授权」页的命名空间粒度授权）：只读用户禁用写按钮，后端仍强制判定
const perm = usePerm()
const noWrite = computed(() => !perm.canWriteNS(namespace.value))

const kind = computed(() => String(route.params.kind))

const rolloutsLoading = ref(false)
const rollouts = ref<RolloutItem[]>([])
const selectedRs = ref<number>()
const nextRun = computed(() => (kind.value === 'cronjobs' ? fmtNextRun(detail.value) : ''))
const moreOpen = ref<string[]>([])

function fmtNextRun(d: any): string {
  const schedule = d?.schedule || d?.spec?.schedule
  if (!schedule) return ''
  const next = cronNextRun(String(schedule))
  return next ? next.toLocaleString() : ''
}

async function loadRollouts() {
  if (kind.value !== 'deployments') return
  rolloutsLoading.value = true
  try {
    rollouts.value = await k8sApi.rollouts(kind.value, namespace.value, name.value)
    const cur = rollouts.value.find((r) => r.current)
    if (cur && selectedRs.value == null) selectedRs.value = cur.revision
  } finally {
    rolloutsLoading.value = false
  }
}

async function doRollback(row: RolloutItem) {
  try {
    await ElMessageBox.confirm(`确定回滚到版本 ${row.revision}（镜像 ${row.image}）？将触发滚动更新。`, '回滚确认', { type: 'warning' })
  } catch {
    return
  }
  await k8sApi.rollback(namespace.value, name.value, row.revision)
  ElMessage.success(`已回滚到版本 ${row.revision}`)
  await refresh()
}

async function deleteRs(row: RolloutItem) {
  await confirmDelete(row.name, { title: '删除 ReplicaSet' })
  await k8sApi.deleteWorkload('replicasets', namespace.value, row.name)
  ElMessage.success('已删除')
  await loadRollouts()
}

const viewVisible = ref(false)
const viewYaml = ref('')
async function showRsYaml(row: RolloutItem) {
  const { yaml } = await k8sApi.getYaml('replicasets', namespace.value, row.name)
  viewYaml.value = yaml
  viewVisible.value = true
}
async function showPodYaml() {
  if (!selectedPod.value) return
  const { yaml } = await k8sApi.getYaml('pods', selectedPod.value.namespace, selectedPod.value.name)
  viewYaml.value = yaml
  viewVisible.value = true
}

const name = computed(() => String(route.params.name))
const namespace = computed(() => String(route.query.namespace || 'default'))
const kindTitle = computed(() => {
  const titles: Record<string, string> = {
    deployments: '部署', statefulsets: '有状态副本集', daemonsets: '守护进程集',
    cronjobs: '定时任务', jobs: '任务', replicasets: '副本集', replicationcontrollers: '复制控制器',
  }
  return titles[kind.value] || kind.value
})

const detail = ref<WorkloadDetail>()
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

const replicasOk = computed(() => {
  const r = detail.value?.replicas
  return !!r && r.readyReplicas === r.replicas
})
const replicasPct = computed(() => {
  const r = detail.value?.replicas
  if (!r) return 0
  if (!r.replicas) return 100
  return (r.readyReplicas / r.replicas) * 100
})

// ---------- 选中容器组 ----------
const selectedPod = ref<PodItem>()
const podDetail = ref<PodDetail>()
const podLoading = ref(false)
const podMonitor = ref<PodMonitor>()

async function selectPod(p: PodItem) {
  selectedPod.value = p
  podLoading.value = true
  podDetail.value = undefined
  podMonitor.value = undefined
  try {
    podDetail.value = await k8sApi.podDetail(p.namespace || namespace.value, p.name)
  } catch {
    /* 拦截器已提示 */
  } finally {
    podLoading.value = false
  }
  loadPodMonitor(p)
}

async function loadPodMonitor(p: PodItem) {
  try {
    podMonitor.value = await k8sApi.monitorPod(p.namespace || namespace.value, p.name, '1h')
  } catch {
    podMonitor.value = undefined
  }
}

const condLabelMap: Record<string, string> = {
  Initialized: '已初始化',
  PodScheduled: '已调度',
  ContainersReady: '可启动容器',
  Ready: '容器组已就绪',
}
const podTimeline = computed(() =>
  (podDetail.value?.conditions || [])
    .filter((c) => condLabelMap[c.type])
    .map((c) => ({
      label: condLabelMap[c.type],
      ok: c.status === 'True',
      time: (c.updated || '').replace('T', ' ').slice(0, 19),
    })),
)

const configOf = (cname: string) => (detail.value?.containers || []).find((c) => c.name === cname)
function portsOf(cname: string): string[] {
  const raw = configOf(cname)?.ports || ''
  return raw.split(/[,，;]\s*|\s+/).filter(Boolean)
}

// ---------- 容器组操作 ----------
async function doDeletePod() {
  if (!selectedPod.value) return
  await confirmDelete(selectedPod.value.name, { title: '删除 Pod' })
  await k8sApi.deletePod(selectedPod.value.namespace, selectedPod.value.name)
  ElMessage.success('已发起删除')
  await load()
  selectedPod.value = undefined
}

async function deleteAllPods() {
  await ElMessageBox.confirm(`删除「${name.value}」当前全部 ${detail.value?.pods.length || 0} 个容器组？控制器会自动重建。`, '删除容器组', { type: 'warning' })
  for (const p of detail.value?.pods || []) {
    await k8sApi.deletePod(p.namespace || namespace.value, p.name).catch(() => {})
  }
  ElMessage.success('已发起删除')
  await load()
}

function onPodMore(cmd: string) {
  if (!selectedPod.value) return
  if (cmd === 'logs') openPodLogs(selectedPod.value)
  else if (cmd === 'terminal') openTerminal(selectedPod.value)
  else if (cmd === 'files') openFiles()
  else if (cmd === 'full') goPod(selectedPod.value)
  else if (cmd === 'evict') doEvict(selectedPod.value)
}

async function doEvict(p: PodItem) {
  await ElMessageBox.confirm(`驱除 Pod「${p.name}」？将触发控制器重建（若有）。`, '驱除 Pod', { type: 'warning' })
  await k8sApi.evictPod(p.namespace || namespace.value, p.name)
  ElMessage.success('已发起驱除')
  await load()
}

function openPodLogs(pod: PodItem, container?: string) {
  logsPod.value = pod
  logsContainer.value = container || ''
  logsVisible.value = true
}

const terminalVisible = ref(false)
const terminalPod = ref<PodItem>()
const terminalContainer = ref('')
function openTerminal(pod: PodItem, container?: string) {
  terminalPod.value = pod
  terminalContainer.value = container || ''
  terminalVisible.value = true
}

const filesVisible = ref(false)
function openFiles(_container?: string) {
  filesVisible.value = true
}

// ---------- 非 Deployment 的资源树 ----------
interface TreeNode {
  id: string
  label: string
  type: 'workload' | 'pod' | 'container'
  pod?: PodItem
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
    })),
  }
  return [root]
})

function onTreeClick(node: TreeNode) {
  if (node.type === 'pod' && node.pod) selectPod(node.pod)
}

// ---------- 工作负载级监控 ----------
const range = ref('6h')
const monitor = ref<WorkloadMonitor>()
const monitorLoading = ref(false)

const monitorCards = computed<MetricCardDef[]>(() => {
  const m = monitor.value
  const cpuPct = m?.cpuUsagePct ?? null
  const memPct = m?.memUsagePct ?? null
  return [
    cpuPct != null
      ? { label: 'CPU 使用率', value: cpuPct, unit: '%', color: '#2b4b83' }
      : { label: 'CPU 用量', value: m?.cpuUsage ?? null, unit: '核', decimals: 2, color: '#2b4b83' },
    memPct != null
      ? { label: '内存使用率', value: memPct, unit: '%', color: '#2ba471' }
      : { label: '内存用量', value: m?.memUsageMi ?? null, unit: 'Mi', decimals: 0, color: '#2ba471' },
    { label: 'Pod 数量', value: m?.podCount ?? null, unit: '个', decimals: 0, color: '#f5872b' },
    { label: '磁盘写', value: m?.diskWriteMBs ?? null, unit: 'MB/s', decimals: 2, color: '#74829b' },
    { label: '网络接收', value: m?.netRxMBs ?? null, unit: 'MB/s', decimals: 2, color: '#e5484d' },
    { label: '网络发送', value: m?.netTxMBs ?? null, unit: 'MB/s', decimals: 2, color: '#9254de' },
  ]
})

const monitorCharts = computed<MetricChartDef[]>(() => {
  const cpuIsPct = monitor.value?.cpuTrendIsPct ?? false
  const memIsPct = monitor.value?.memTrendIsPct ?? false
  const cpuUnit = cpuIsPct ? '%' : '核'
  const memUnit = memIsPct ? '%' : 'Mi'
  return [
    { title: cpuIsPct ? 'CPU 使用率趋势' : 'CPU 用量趋势', series: [{ name: 'CPU', data: monitor.value?.cpuUsageTrend || [], color: '#2b4b83', unit: cpuUnit }], yAxisName: cpuUnit },
    { title: memIsPct ? '内存使用率趋势' : '内存用量趋势', series: [{ name: '内存', data: monitor.value?.memUsageTrend || [], color: '#2ba471', unit: memUnit }], yAxisName: memUnit },
    { title: '磁盘写趋势', series: [{ name: '磁盘写', data: monitor.value?.diskWriteTrend || [], color: '#74829b', unit: 'MB/s' }], yAxisName: 'MB/s' },
    {
      title: '网络流量',
      series: [
        { name: '接收', data: monitor.value?.netRxTrend || [], color: '#e5484d', unit: 'MB/s' },
        { name: '发送', data: monitor.value?.netTxTrend || [], color: '#9254de', unit: 'MB/s' },
      ],
      yAxisName: 'MB/s',
    },
  ]
})

// ---------- 数据加载 ----------
onMounted(() => {
  load()
  loadMonitor()
  loadRollouts()
  // 列表页跳转过来时自动选中当前版本
  if (route.query.rollouts && kind.value === 'deployments') loadRollouts()
})

watch([() => clusterStore.current, namespace], () => {
  load()
  loadRollouts()
})

async function load() {
  if (!clusterStore.current) return
  loading.value = true
  try {
    detail.value = await k8sApi.workloadDetail(kind.value, namespace.value, name.value)
    // 默认选中第一个容器组
    if (!selectedPod.value && detail.value?.pods?.length) {
      selectPod(detail.value.pods[0])
    }
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
  loadRollouts()
}

function goBack() {
  router.push({ path: `/workloads/${kind.value}`, query: { namespace: namespace.value } })
}

function goPod(pod: PodItem) {
  router.push({ path: `/pods/${pod.namespace}/${pod.name}` })
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
    await confirmDelete(name.value, { title: `删除${kindTitle.value}` })
  } catch {
    return
  }
  await k8sApi.deleteWorkload(kind.value, namespace.value, name.value)
  ElMessage.success('已删除')
  goBack()
}

// ------------------- 调整镜像版本（逻辑见 ImageUpdateDialog） -------------------
const imageVisible = ref(false)

// ------------------- 容器内日志采集（逻辑见 LogCollectionDialog） -------------------
const collectVisible = ref(false)
</script>

<style scoped>
.kc-page { display: flex; flex-direction: column; gap: 12px; }
.mono { font-family: var(--kc-mono); }
.kc-top-head { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 8px; }
.actions { display: flex; gap: 6px; flex-wrap: wrap; }
.kc-status-strip {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 14px;
  margin-top: 14px;
  font-size: 13px;
  color: #8a94a6;
}
.kc-strip-label { color: #6b7689; }
.kc-strip-item b { color: #2f3b52; font-weight: 600; margin-left: 4px; }
.kc-progress-mini { display: flex; align-items: center; gap: 8px; width: 180px; }
.kc-progress-track {
  flex: 1;
  height: 6px;
  border-radius: 3px;
  background: #e3e8f1;
  overflow: hidden;
}
.kc-progress-track i {
  display: block;
  height: 100%;
  background: var(--el-color-success);
  border-radius: 3px;
  transition: width 0.4s;
}
.kc-progress-text { font-size: 13px; color: #2f3b52; font-weight: 600; }
.kc-two-col { display: grid; grid-template-columns: 1.2fr 1fr; gap: 12px; align-items: start; }
@media (max-width: 1200px) { .kc-two-col { grid-template-columns: 1fr; } }
.kc-more { border-top: none; }
.kc-more-title { color: var(--kc-link); font-size: 13.5px; }
.kc-info-value.img { font-weight: 400; font-size: 13px; }
.next-run { color: var(--el-color-primary); font-size: 13px; margin-left: 4px; }

/* 三栏主体 */
.kc-three-col {
  display: grid;
  grid-template-columns: 300px 300px minmax(0, 1fr);
  gap: 12px;
  align-items: start;
}
@media (max-width: 1280px) {
  .kc-three-col { grid-template-columns: 1fr 1fr; }
  .kc-pod-detail { grid-column: 1 / -1; }
}
@media (max-width: 900px) { .kc-three-col { grid-template-columns: 1fr; } }
.rs-head { display: flex; align-items: center; gap: 8px; }
.rs-head .spacer { flex: 1; }

/* 历史版本时间线 */
.rs-item {
  border: 1px solid var(--kc-border);
  border-radius: 4px;
  padding: 10px 12px;
  margin-bottom: 10px;
  cursor: pointer;
  position: relative;
}
.rs-item:hover { border-color: var(--el-color-primary-light-5); }
.rs-current { border-color: var(--el-color-success); background: #f4fbf7; }
html.dark .rs-current { background: rgba(43, 164, 113, 0.08); }
.rs-item::before {
  content: '';
  position: absolute;
  left: -18px;
  top: 16px;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: #c0c6d1;
}
.rs-current::before { background: var(--el-color-success); }
.rs-top { display: flex; justify-content: space-between; font-size: 12.5px; color: #8a94a6; }
.rs-rev { color: #8a94a6; }
.rs-name { font-size: 13px; color: var(--kc-link); margin: 4px 0; word-break: break-all; }
.rs-meta { display: flex; align-items: center; gap: 6px; font-size: 12.5px; color: #6b7689; flex-wrap: wrap; }
.rs-img {
  font-size: 12px;
  color: #8a94a6;
  margin-top: 4px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.rs-actions { display: flex; gap: 6px; margin-top: 8px; }

/* 容器组列表 */
.pod-item {
  border: 1px solid var(--kc-border);
  border-radius: 4px;
  padding: 10px 12px;
  margin-bottom: 10px;
  cursor: pointer;
}
.pod-item:hover { border-color: var(--el-color-primary-light-5); }
.pod-active { border-color: var(--el-color-primary); background: var(--el-color-primary-light-9); }
.pod-item-head { display: flex; justify-content: space-between; align-items: center; gap: 8px; }
.pod-item-name { font-size: 13px; color: #2f3b52; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pod-item-meta { font-size: 12.5px; color: #8a94a6; margin-top: 4px; }

/* 选中容器组详情 */
.pd-charts { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
@media (max-width: 1100px) { .pd-charts { grid-template-columns: 1fr; } }
.pd-chart { border: 1px solid var(--kc-border); border-radius: 4px; padding: 8px 10px; }
.pd-chart-title { font-size: 13px; color: #6b7689; font-weight: 600; margin-bottom: 4px; display: flex; align-items: center; gap: 6px; }
.pod-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  background: var(--kc-table-head-bg);
  color: #fff;
  border-radius: 4px;
  padding: 6px 12px;
  margin-bottom: 10px;
}
.pod-bar-label { font-size: 13px; opacity: 0.85; }
.pod-bar-name { font-weight: 700; font-size: 14px; max-width: 60%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pod-bar-age { font-size: 12.5px; opacity: 0.8; }
.pod-bar .spacer { flex: 1; }
.pod-bar :deep(.el-button) { color: #fff; }
.pod-bar :deep(.el-button--danger:hover) { color: #ffb3b3; }
.pd-info-row {
  display: flex;
  flex-wrap: wrap;
  gap: 24px;
  font-size: 13px;
  color: #6b7689;
  padding-bottom: 10px;
  border-bottom: 1px solid var(--kc-border);
}
.pd-info-row b { color: #2f3b52; }
.pd-timeline { display: flex; flex-wrap: wrap; gap: 8px 22px; margin: 12px 0; }
.pd-tl-item { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; color: #2f3b52; }
.pd-tl-item small { color: #8a94a6; }
.pd-tl-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--el-color-success); display: inline-block; }
.pd-tl-bad .pd-tl-dot { background: var(--el-color-danger); }
.pd-tl-bad { color: var(--el-color-danger); }

/* 容器卡片 */
.ctr-card {
  border: 1px solid var(--kc-border);
  border-radius: 4px;
  padding: 10px 12px;
  margin-bottom: 10px;
}
.ctr-head { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.ctr-name { font-size: 14px; color: #2f3b52; }
.ctr-head .spacer { flex: 1; }
.ctr-line { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 6px; font-size: 13px; }
.ctr-line .spacer { flex: 1; }
.ctr-label { color: #8a94a6; flex-shrink: 0; }
.ctr-img { color: #2f3b52; word-break: break-all; font-size: 12.5px; }
.port-chip { font-family: var(--kc-mono); }
.ctr-msg { font-size: 12.5px; color: #e5484d; word-break: break-all; }
.pd-init { font-size: 13px; color: #6b7689; margin-top: 6px; }

/* 树（非 Deployment） */
.tree-node { display: flex; align-items: center; gap: 6px; font-size: 13px; flex: 1; }
.tree-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
.status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  display: inline-block;
  flex-shrink: 0;
}
.dot-Running { background: #2ba471; }
.dot-Pending, .dot-Waiting, .dot-Terminating { background: #f5872b; }
.dot-Failed, .dot-Error, .dot-CrashLoopBackOff, .dot-ImagePullBackOff { background: #e5484d; }
.dot-Completed { background: #909399; }
</style>
