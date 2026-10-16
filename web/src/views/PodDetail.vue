<template>
  <div v-loading="loading" class="kc-page">
    <!-- 头部：标题 + 操作 + 状态条 + 信息网格 -->
    <el-card shadow="never">
      <div class="kc-top-head">
        <span class="kc-section-title">
          <DetailBack to="/workloads/pods" label="容器组列表" :query="{ namespace }" />
          <el-icon><Cherry /></el-icon>容器组详情 / <span class="mono">{{ name }}</span>
        </span>
        <div class="actions">
          <el-button v-if="detail?.ownerKind" size="small" type="primary" @click="goOwner">查看归属 {{ ownerLabel }}</el-button>
          <el-button size="small" @click="showYaml">YAML</el-button>
          <el-button size="small" type="danger" :disabled="noWrite" @click="doDelete">删除</el-button>
          <el-button size="small" @click="load">刷新</el-button>
        </div>
      </div>

      <div class="kc-status-strip">
        <StatusTag :status="detail?.status || ''" />
        <span class="kc-strip-item">就绪 <b>{{ detail?.ready || '-' }}</b></span>
        <span class="kc-strip-item">重启 <b>{{ detail?.restarts ?? '-' }}</b></span>
        <span class="kc-strip-item">运行时长 <b>{{ detail?.age || '-' }}</b></span>
      </div>

      <div class="kc-info-grid">
        <div class="kc-info-item"><span class="kc-info-label">集群:</span><span class="kc-info-value">{{ clusterStore.current }}</span></div>
        <div class="kc-info-item"><span class="kc-info-label">名称空间:</span><span class="kc-info-value mono">{{ detail?.namespace }}</span></div>
        <div class="kc-info-item"><span class="kc-info-label">名称:</span><span class="kc-info-value mono">{{ detail?.name }}</span></div>
        <div class="kc-info-item"><span class="kc-info-label">创建时间:</span><span class="kc-info-value">{{ detail?.age || '-' }} 前</span></div>
        <div class="kc-info-item"><span class="kc-info-label">类型:</span><span class="kc-info-value">Pod <el-tag size="small" type="info" effect="plain">pods</el-tag></span></div>
      </div>

      <el-collapse v-model="moreOpen" class="kc-more">
        <el-collapse-item name="more">
          <template #title><span class="kc-more-title">更多细节</span></template>
          <div class="kc-info-grid" style="margin-top: 0">
            <div class="kc-info-item"><span class="kc-info-label">Phase:</span><span class="kc-info-value">{{ detail?.phase || '-' }}</span></div>
            <div class="kc-info-item"><span class="kc-info-label">启动时间:</span><span class="kc-info-value">{{ detail?.startTime || '-' }}</span></div>
            <div class="kc-info-item"><span class="kc-info-label">ServiceAccount:</span><span class="kc-info-value mono">{{ detail?.serviceAccount || '-' }}</span></div>
            <div class="kc-info-item"><span class="kc-info-label">重启策略:</span><span class="kc-info-value">{{ detail?.restartPolicy || '-' }}</span></div>
            <div class="kc-info-item" style="grid-column: 1 / -1"><span class="kc-info-label">容忍:</span>
              <span class="kc-info-value">
                <template v-if="detail?.tolerations?.length"><el-tag v-for="t in detail.tolerations" :key="t" size="small" style="margin-right: 4px">{{ t }}</el-tag></template>
                <template v-else>无</template>
              </span>
            </div>
            <div class="kc-info-item" style="grid-column: 1 / -1"><span class="kc-info-label">标签:</span>
              <span class="kc-info-value"><el-tag v-for="(v, k) in detail?.labels" :key="k" size="small" style="margin-right: 4px">{{ k }}={{ v }}</el-tag><span v-if="!Object.keys(detail?.labels || {}).length">-</span></span>
            </div>
          </div>
        </el-collapse-item>
      </el-collapse>
    </el-card>

    <!-- 关联事件 -->
    <el-card shadow="never">
      <template #header><span class="kc-section-title">关联事件</span></template>
      <el-table v-if="detail?.events?.length" border :data="detail.events" size="small" max-height="300">
        <el-table-column prop="reason" label="原因" width="150" />
        <el-table-column prop="lastSeen" label="时间" width="170" />
        <el-table-column prop="count" label="次数" width="70" align="center" />
        <el-table-column prop="message" label="信息" min-width="260" show-overflow-tooltip />
      </el-table>
      <el-empty v-else :image-size="56" :description="`没有与 ${name} 关联的事件`" />
    </el-card>

    <!-- Prometheus 趋势 -->
    <div class="pd-charts">
      <div class="pd-chart">
        <div class="pd-chart-title"><el-tag size="small" type="success" effect="plain">Prometheus</el-tag> CPU（核）</div>
        <MetricChart :series="[{ name: 'CPU', data: monitor?.cpuUsageTrend || [], color: MC.primary, unit: '核' }]" height="170px" y-axis-name="核" />
      </div>
      <div class="pd-chart">
        <div class="pd-chart-title"><el-tag size="small" type="success" effect="plain">Prometheus</el-tag> 内存（Mi）</div>
        <MetricChart :series="[{ name: '内存', data: monitor?.memUsageTrend || [], color: MC.sky, unit: 'Mi' }]" height="170px" y-axis-name="Mi" />
      </div>
    </div>

    <!-- 容器组标题栏（深蓝条） -->
    <div class="pod-bar">
      <span class="pod-bar-label">容器组:</span>
      <span class="pod-bar-name mono">{{ name }}</span>
      <span class="pod-bar-age">{{ detail?.age }} 前</span>
      <span class="spacer"></span>
      <el-button size="small" text @click="showYaml">YAML</el-button>
      <el-button size="small" text type="danger" :disabled="noWrite" @click="doDelete">删除</el-button>
      <el-dropdown trigger="click" @command="onMore">
        <el-button size="small" text>更多<el-icon><ArrowDown /></el-icon></el-button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="logs">日志</el-dropdown-item>
            <el-dropdown-item command="terminal">终端</el-dropdown-item>
            <el-dropdown-item command="files">文件</el-dropdown-item>
            <el-dropdown-item command="evict" divided :disabled="noWrite">驱除（尊重 PDB）</el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </div>

    <!-- 所在节点 / IP / 状态 / 重启策略 -->
    <div class="pd-info-row">
      <span>所在节点: <el-link type="primary" class="mono" @click="goNode">{{ detail?.nodeName || '-' }}</el-link></span>
      <span>容器组IP: <b class="mono">{{ detail?.ip || '-' }}</b></span>
      <span>状态: <b>{{ detail?.phase || detail?.status }}</b></span>
      <span>重启策略: <b>{{ detail?.restartPolicy || '-' }}</b></span>
    </div>

    <!-- 条件时间线 -->
    <div class="pd-timeline">
      <span v-for="t in timeline" :key="t.label" class="pd-tl-item" :class="{ 'pd-tl-bad': !t.ok }">
        <span class="pd-tl-dot"></span>{{ t.label }}
        <small v-if="t.time">{{ t.time }}</small>
      </span>
    </div>

    <!-- 容器卡片 -->
    <div v-for="c in detail?.containers || []" :key="c.name" class="ctr-card">
      <div class="ctr-head">
        <el-icon :size="18" color="var(--kc-primary-deep)"><Ship /></el-icon>
        <el-tag size="small" effect="dark">容器</el-tag>
        <span class="ctr-name"><b>{{ c.name }}</b></span>
        <span class="spacer"></span>
        <el-tag v-if="c.state === 'Running'" size="small" type="success" effect="dark">running</el-tag>
        <StatusTag v-else :status="c.state" :text="c.reason || c.state" />
      </div>
      <div class="ctr-line">
        <span class="ctr-label">镜像抓取策略: {{ c.imagePullPolicy || '-' }}</span>
        <span class="ctr-label">镜像: <span class="mono ctr-img">{{ c.image || '-' }}</span></span>
        <VulnChip v-if="c.image" :image="c.image" />
      </div>
      <div class="ctr-line">
        <span class="ctr-label">当前状态</span>
        <el-tag size="small" :type="c.state === 'Running' ? 'success' : 'info'" effect="plain">{{ c.state === 'Running' ? 'Started' : c.state }}</el-tag>
        <el-tag size="small" :type="c.ready ? 'success' : 'warning'" effect="plain">{{ c.ready ? 'Ready' : 'NotReady' }}</el-tag>
        <el-tag size="small" type="info" effect="plain">{{ c.state }}</el-tag>
        <el-tag v-if="c.startedAt" size="small" type="info" effect="plain">StartedAt {{ c.startedAt }}</el-tag>
        <el-tag size="small" type="info" effect="plain">重启 {{ c.restartCount }} 次</el-tag>
      </div>
      <div class="ctr-line">
        <span class="ctr-label">端口</span>
        <el-tag v-for="p in c.ports || []" :key="p" size="small" class="port-chip">{{ p }}</el-tag>
        <span v-if="!c.ports?.length" class="ctr-label">-</span>
        <span class="spacer"></span>
        <el-button size="small" type="success" :disabled="noWrite" @click="openFiles(c.name)">文件浏览器</el-button>
        <el-button size="small" @click="openLogs(c.name)">下载日志</el-button>
        <el-button size="small" @click="openLogs(c.name)">追踪日志</el-button>
        <el-button size="small" type="danger" :disabled="noWrite" @click="openTerminal(c.name)">bash</el-button>
      </div>
      <div v-if="c.message" class="ctr-msg">{{ c.message }}</div>
    </div>

    <!-- Init 容器卡片 -->
    <div v-for="c in detail?.initContainers || []" :key="'init-' + c.name" class="ctr-card ctr-init">
      <div class="ctr-head">
        <el-icon :size="18" color="var(--kc-mute)"><Ship /></el-icon>
        <el-tag size="small" type="info" effect="dark">Init 容器</el-tag>
        <span class="ctr-name"><b>{{ c.name }}</b></span>
        <span class="spacer"></span>
        <StatusTag :status="c.state" :text="c.reason || c.state" />
      </div>
      <div class="ctr-line">
        <span class="ctr-label">镜像: <span class="mono ctr-img">{{ c.image || '-' }}</span></span>
        <el-tag size="small" type="info" effect="plain">重启 {{ c.restartCount }} 次</el-tag>
      </div>
      <div v-if="c.message" class="ctr-msg">{{ c.message }}</div>
    </div>
  </div>

  <YamlDialog v-model="yamlVisible" :yaml="yamlText" :readonly="true" />
  <PodLogsDrawer v-model="logsVisible" :pod="podItem" :container="logsContainer" />
  <WebTerminal v-model:visible="terminalVisible" :namespace="namespace" :pod="name" :container="terminalContainer || undefined" />
  <el-dialog v-model="filesVisible" title="容器文件" width="900px">
    <FileBrowser :namespace="namespace" :pod="name" :containers="(detail?.containers || []).map((c) => c.name)" />
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { METRIC_COLORS as MC } from '../utils/chart-palette'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowDown, Cherry, Ship } from '@element-plus/icons-vue'
import { k8sApi, type PodDetail, type PodItem, type PodMonitor } from '../api'
import StatusTag from '../components/StatusTag.vue'
import DetailBack from '../components/DetailBack.vue'
import MetricChart from '../components/MetricChart.vue'
import YamlDialog from '../components/YamlDialog.vue'
import PodLogsDrawer from '../components/PodLogsDrawer.vue'
import WebTerminal from '../components/WebTerminal.vue'
import FileBrowser from '../components/FileBrowser.vue'
import VulnChip from '../components/VulnChip.vue'
import { useClusterStore } from '../store/cluster'
import { usePerm } from '../store/perm'
import { confirmDelete } from '../utils/confirm'

const route = useRoute()
const router = useRouter()
const clusterStore = useClusterStore()
const perm = usePerm()

const namespace = computed(() => String(route.params.namespace))
const name = computed(() => String(route.params.name))
const noWrite = computed(() => !perm.canWriteNS(namespace.value))

const detail = ref<PodDetail>()
const loading = ref(false)
const moreOpen = ref<string[]>([])

const monitor = ref<PodMonitor>()

const yamlVisible = ref(false)
const yamlText = ref('')
const logsVisible = ref(false)
const logsContainer = ref('')
const terminalVisible = ref(false)
const terminalContainer = ref('')
const filesVisible = ref(false)

const podItem = computed<PodItem | undefined>(() =>
  detail.value
    ? { name: detail.value.name, namespace: detail.value.namespace, nodeName: detail.value.nodeName, status: detail.value.status, ready: detail.value.ready, restarts: detail.value.restarts, ip: detail.value.ip, age: detail.value.age, labels: detail.value.labels }
    : undefined,
)

const ownerKindAlias: Record<string, string> = {
  replicaset: 'replicasets', statefulset: 'statefulsets', daemonset: 'daemonsets',
  job: 'jobs', cronjob: 'cronjobs', replicationcontroller: 'replicationcontrollers',
}
const ownerKindDisplay: Record<string, string> = {
  replicaset: 'ReplicaSet', statefulset: 'StatefulSet', daemonset: 'DaemonSet',
  job: 'Job', cronjob: 'CronJob', replicationcontroller: 'ReplicationController',
}
const ownerLabel = computed(() => {
  const k = detail.value?.ownerKind || ''
  return `${ownerKindDisplay[k] || k} ${detail.value?.ownerName || ''}`
})
function goOwner() {
  const kindPath = ownerKindAlias[detail.value?.ownerKind || '']
  if (!kindPath) return
  router.push({ path: `/workloads/${kindPath}/${detail.value!.ownerName}`, query: { namespace: namespace.value } })
}
function goNode() {
  if (detail.value?.nodeName) router.push(`/nodes/${detail.value.nodeName}`)
}

const condLabelMap: Record<string, string> = {
  Initialized: '已初始化',
  PodScheduled: '已调度',
  ContainersReady: '可启动容器',
  Ready: '容器组已就绪',
}
const timeline = computed(() =>
  (detail.value?.conditions || [])
    .filter((c) => condLabelMap[c.type])
    .map((c) => ({ label: condLabelMap[c.type], ok: c.status === 'True', time: c.updated })),
)

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
  loadMonitor()
}

async function loadMonitor() {
  try {
    monitor.value = await k8sApi.monitorPod(namespace.value, name.value, '1h')
  } catch {
    monitor.value = undefined
  }
}

async function showYaml() {
  const { yaml } = await k8sApi.getYaml('pods', namespace.value, name.value)
  yamlText.value = yaml
  yamlVisible.value = true
}

function openLogs(container?: string) {
  logsContainer.value = container || ''
  logsVisible.value = true
}
function openTerminal(container?: string) {
  terminalContainer.value = container || ''
  terminalVisible.value = true
}
function openFiles(_container?: string) {
  filesVisible.value = true
}

function onMore(cmd: string) {
  if (cmd === 'logs') openLogs()
  else if (cmd === 'terminal') openTerminal()
  else if (cmd === 'files') openFiles()
  else if (cmd === 'evict') doEvict()
}

async function doEvict() {
  await ElMessageBox.confirm(`驱除 Pod「${name.value}」？将触发控制器重建（若有）。`, '驱除 Pod', { type: 'warning' })
  await k8sApi.evictPod(namespace.value, name.value)
  ElMessage.success('已发起驱除')
  router.back()
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

onMounted(load)
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
  color: var(--kc-mute);
}
.kc-strip-item b { color: var(--kc-ink); font-weight: 600; margin-left: 4px; }
.kc-more { border-top: none; }
.kc-more-title { color: var(--kc-link); font-size: 13.5px; }

.pd-charts { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
@media (max-width: 1100px) { .pd-charts { grid-template-columns: 1fr; } }
.pd-chart { border: 1px solid var(--kc-border); border-radius: 4px; padding: 8px 10px; background: var(--kc-header-bg); }
.pd-chart-title { font-size: 13px; color: var(--kc-ink-2); font-weight: 600; margin-bottom: 4px; display: flex; align-items: center; gap: 6px; }

.pod-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  background: var(--kc-table-head-bg);
  color: #fff;
  border-radius: 4px;
  padding: 6px 12px;
}
.pod-bar-label { font-size: 13px; opacity: 0.85; }
.pod-bar-name { font-weight: 700; font-size: 14px; max-width: 60%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pod-bar-age { font-size: 13px; opacity: 0.8; }
.pod-bar .spacer { flex: 1; }
.pod-bar :deep(.el-button) { color: #fff; }
.pod-bar :deep(.el-button--danger:hover) { color: var(--kc-coral); }

.pd-info-row {
  display: flex;
  flex-wrap: wrap;
  gap: 24px;
  font-size: 13px;
  color: var(--kc-ink-2);
  background: var(--kc-header-bg);
  border: 1px solid var(--kc-border);
  border-radius: 4px;
  padding: 10px 14px;
}
.pd-info-row b { color: var(--kc-ink); }

.pd-timeline { display: flex; flex-wrap: wrap; gap: 8px 22px; background: var(--kc-header-bg); border: 1px solid var(--kc-border); border-radius: 4px; padding: 10px 14px; }
.pd-tl-item { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; color: var(--kc-ink); }
.pd-tl-item small { color: var(--kc-mute); }
.pd-tl-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--el-color-success); display: inline-block; }
.pd-tl-bad .pd-tl-dot { background: var(--el-color-danger); }
.pd-tl-bad { color: var(--el-color-danger); }

.ctr-card { border: 1px solid var(--kc-border); border-radius: 4px; padding: 10px 14px; background: var(--kc-header-bg); }
.ctr-init { opacity: 0.92; }
.ctr-head { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.ctr-name { font-size: 14px; color: var(--kc-ink); }
.ctr-head .spacer { flex: 1; }
.ctr-line { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 6px; font-size: 13px; }
.ctr-line .spacer { flex: 1; }
.ctr-label { color: var(--kc-mute); flex-shrink: 0; }
.ctr-img { color: var(--kc-ink); word-break: break-all; font-size: 13px; }
.port-chip { font-family: var(--kc-mono); }
.ctr-msg { font-size: 13px; color: var(--kc-crit-text); word-break: break-all; }
</style>
