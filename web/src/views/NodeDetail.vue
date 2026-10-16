<template>
  <div v-loading="loading" class="kc-page">
    <!-- 顶部：节点概况 -->
    <el-card shadow="never" class="kc-top-card">
      <div class="kc-top-head">
        <span class="kc-section-title">
          <DetailBack to="/nodes" label="节点列表" />
          <el-icon><Cpu /></el-icon>节点详情
        </span>
        <div class="header-actions">
          <el-tag v-if="detail && !detail.schedulable" type="warning" size="small" effect="dark">已封锁 (SchedulingDisabled)</el-tag>
          <el-button v-if="detail?.schedulable" size="small" type="warning" @click="cordon(true)">暂停调度</el-button>
          <el-button v-else size="small" type="success" @click="cordon(false)">恢复调度</el-button>
          <el-button size="small" type="primary" :disabled="!perm.canWriteCluster()" @click="terminalVisible = true">Shell</el-button>
          <el-button size="small" @click="load">刷新</el-button>
        </div>
      </div>

      <!-- 状态 + 运行时信息条 -->
      <div class="kc-status-strip">
        <StatusTag :status="detail?.status || ''" text="已就绪" v-if="detail?.status === 'Ready'" />
        <StatusTag :status="detail?.status || ''" v-else />
        <span class="kc-strip-item">操作系统镜像 <b>{{ detail?.osImage || '-' }}</b></span>
        <span class="kc-strip-item">操作系统内核 <b>{{ detail?.kernelVersion || '-' }}</b></span>
        <span class="kc-strip-item">容器引擎 <b>{{ detail?.containerRuntime || '-' }}</b></span>
        <span class="kc-strip-item">kubelet <b>{{ detail?.version || '-' }}</b></span>
      </div>

      <!-- 四列信息网格 -->
      <div class="kc-info-grid">
        <div class="kc-info-item"><span class="kc-info-label">集群:</span><span class="kc-info-value">{{ clusterStore.current }}</span></div>
        <div class="kc-info-item"><span class="kc-info-label">名称:</span><span class="kc-info-value mono">{{ name }}</span></div>
        <div class="kc-info-item"><span class="kc-info-label">创建时间:</span><span class="kc-info-value">{{ detail?.age || '-' }} 前</span></div>
        <div class="kc-info-item"><span class="kc-info-label">类型:</span><span class="kc-info-value">Node <el-tag size="small" type="info" effect="plain">nodes</el-tag></span></div>
      </div>

      <el-collapse v-model="moreOpen" class="kc-more">
        <el-collapse-item name="more">
          <template #title><span class="kc-more-title">更多细节</span></template>
          <div class="kc-info-grid" style="margin-top: 0">
            <div class="kc-info-item"><span class="kc-info-label">角色:</span><span class="kc-info-value">{{ detail?.roles || '-' }}</span></div>
            <div class="kc-info-item"><span class="kc-info-label">内网 IP:</span><span class="kc-info-value mono">{{ detail?.internalIP || '-' }}</span></div>
            <div class="kc-info-item"><span class="kc-info-label">CPU:</span><span class="kc-info-value">{{ detail?.cpuCores || '-' }} 核</span></div>
            <div class="kc-info-item"><span class="kc-info-label">内存:</span><span class="kc-info-value">{{ detail?.memGi || '-' }} Gi</span></div>
            <div class="kc-info-item"><span class="kc-info-label">污点:</span><span class="kc-info-value">{{ detail?.taints?.length ? detail.taints.join('，') : '无' }}</span></div>
            <div class="kc-info-item"><span class="kc-info-label">标签数:</span><span class="kc-info-value">{{ Object.keys(detail?.labels || {}).length }}</span></div>
          </div>
        </el-collapse-item>
      </el-collapse>
    </el-card>

    <div class="kc-two-col">
      <!-- 节点状态 -->
      <el-card shadow="never">
        <template #header><span class="kc-section-title">节点状态</span></template>
        <div class="kc-cond-row">
          <el-tag v-for="c in conditionPills" :key="c.type" size="small" :effect="c.effect" :type="c.type_">{{ c.text }}</el-tag>
        </div>
        <el-collapse v-model="condOpen">
          <el-collapse-item name="cond">
            <template #title><span class="kc-more-title">全部条件 ({{ detail?.conditions?.length || 0 }})</span></template>
            <el-table border :data="detail?.conditions || []" size="small" stripe>
              <el-table-column prop="type" label="类型" width="180" />
              <el-table-column label="状态" width="100">
                <template #default="{ row }"><StatusTag :status="row.status === 'True' ? 'Ready' : 'NotReady'" :text="row.status" /></template>
              </el-table-column>
              <el-table-column prop="reason" label="原因" width="140" />
              <el-table-column prop="message" label="消息" min-width="260" show-overflow-tooltip />
              <el-table-column prop="updated" label="更新时间" width="170" />
            </el-table>
          </el-collapse-item>
        </el-collapse>
      </el-card>

      <!-- 关联事件 -->
      <el-card shadow="never">
        <template #header><span class="kc-section-title">关联事件</span></template>
        <el-table v-if="nodeEvents.length" border :data="nodeEvents" size="small" max-height="320">
          <el-table-column prop="reason" label="原因" width="140" />
          <el-table-column prop="lastAt" label="时间" width="170" />
          <el-table-column prop="count" label="次数" width="70" align="center" />
          <el-table-column prop="message" label="信息" min-width="240" show-overflow-tooltip />
        </el-table>
        <el-empty v-else :image-size="56" :description="`没有与 ${name} 关联的事件`" />
      </el-card>
    </div>

    <!-- 资源与指标 -->
    <el-card shadow="never" class="kc-usage-card">
      <template #header>
        <div class="usage-head">
          <span class="kc-section-title">资源与指标</span>
          <span class="spacer"></span>
          <RangeSwitch v-model="range" @change="loadMonitor" />
        </div>
      </template>
      <template v-for="u in usageBars" :key="u.name">
        <div class="kc-usage-row">
          <span class="kc-usage-name">{{ u.name }}</span>
          <span class="kc-usage-stat">已用 <b>{{ u.used }}</b> / 可分配 <b>{{ u.allocatable }}</b> / 总量 <b>{{ u.capacity }}</b></span>
        </div>
        <div class="kc-usage-bar"><i :class="u.level" :style="{ width: Math.min(u.pct, 100) + '%' }" /></div>
      </template>

      <div class="kc-gauge-row">
        <div v-for="g in gauges" :key="g.label" class="kc-gauge-card">
          <el-progress type="circle" :width="46" :stroke-width="5" :percentage="Math.min(Math.round(g.pct ?? 0), 100)" :color="gaugeColor(g.pct)" :indeterminate="false" />
          <div>
            <div class="kc-gauge-label">{{ g.label }}</div>
            <div class="kc-gauge-value">{{ g.value }}<small> {{ g.sub }}</small></div>
          </div>
        </div>
      </div>

      <!-- 趋势图（原「监控」页签并入此处，Kuboard 口径：用量条 + 仪表 + 趋势同区展示） -->
      <div class="kc-chart-grid">
        <div v-for="ch in monitorCharts" :key="ch.title" class="kc-chart-card">
          <div class="kc-chart-title">{{ ch.title }}</div>
          <MetricChart :series="ch.series" height="200px" :y-axis-name="ch.yAxisName" />
        </div>
      </div>
      <div v-if="monitor?.kubelet" class="kc-kubelet-row">
        <span class="kc-usage-name">Kubelet 运行时</span>
        <el-tag size="small" type="info" effect="plain">运行中 Pod {{ monitor.kubelet.runningPods ?? '-' }}</el-tag>
        <el-tag size="small" type="info" effect="plain">运行中容器 {{ monitor.kubelet.runningContainers ?? '-' }}</el-tag>
        <el-tag size="small" type="info" effect="plain">PLEG Relist {{ fmtNum(monitor.kubelet.relistRate) }} 次/s</el-tag>
        <el-tag size="small" :type="(monitor.kubelet.runtimeErrorsRate ?? 0) > 0 ? 'danger' : 'info'" effect="plain">运行时操作错误 {{ fmtNum(monitor.kubelet.runtimeErrorsRate) }} 次/s</el-tag>
      </div>
    </el-card>

    <!-- 容器组 / 污点 / 标签 / 监控 -->
    <el-card shadow="never">
      <el-tabs v-model="tab">
        <el-tab-pane :label="`容器组 (${detail?.pods?.length || 0})`" name="pods">
          <el-table border :data="detail?.pods || []" size="small" stripe>
            <el-table-column label="名称" min-width="220">
              <template #default="{ row }">
                <span class="kc-name-link" @click="goPod(row)">{{ row.name }}</span>
              </template>
            </el-table-column>
            <el-table-column prop="namespace" label="命名空间" width="150" />
            <el-table-column label="状态" width="140">
              <template #default="{ row }"><StatusTag :status="row.status" /></template>
            </el-table-column>
            <el-table-column prop="ready" label="就绪" width="70" align="center" />
            <el-table-column prop="ip" label="IP" width="130" />
            <el-table-column label="操作" width="76" align="center">
              <template #default="{ row }">
                <el-button size="small" type="warning" text @click="evictPod(row)">驱除</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-tab-pane>

        <el-tab-pane label="容量" name="capacity">
          <el-table border :data="capacityRows" size="small" stripe>
            <el-table-column prop="resource" label="资源" width="120" />
            <el-table-column prop="capacity" label="容量" width="160" />
            <el-table-column prop="allocatable" label="可分配" />
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

        <!-- 镜像（kubelet status.images，与 kubectl describe node 同源） -->
        <el-tab-pane :label="`镜像 (${detail?.images?.length || 0})`" name="images" lazy>
          <el-table border :data="imageRows" size="small" stripe max-height="560">
            <el-table-column prop="name" label="镜像名称" min-width="420" show-overflow-tooltip>
              <template #default="{ row }"><span class="mono">{{ row.name }}</span></template>
            </el-table-column>
            <el-table-column prop="sizeText" label="大小" width="120" align="right" sortable :sort-by="(r: any) => r.sizeBytes" />
          </el-table>
        </el-tab-pane>
      </el-tabs>
    </el-card>

    <!-- 节点终端（nsenter 调试 Pod 进入宿主机，与节点列表同口径，需集群级写权限） -->
    <WebTerminal v-model:visible="terminalVisible" namespace="kube-system" :pod="name" :node="true" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Cpu, Delete, Plus } from '@element-plus/icons-vue'
import { eventsApi, k8sApi, nodeApi, type EventItemEx, type NodeDetail, type NodeMonitor, type PodItem } from '../api'
import StatusTag from '../components/StatusTag.vue'
import DetailBack from '../components/DetailBack.vue'
import MetricChart from '../components/MetricChart.vue'
import RangeSwitch from '../components/RangeSwitch.vue'
import WebTerminal from '../components/WebTerminal.vue'
import KvEditor from '../components/forms/KvEditor.vue'
import { useClusterStore } from '../store/cluster'
import { usePerm } from '../store/perm'
import { METRIC_COLORS as MC, levelColor } from '../utils/chart-palette'

const route = useRoute()
const router = useRouter()
const clusterStore = useClusterStore()
const perm = usePerm()
const terminalVisible = ref(false)

const name = computed(() => String(route.params.name))
const detail = ref<NodeDetail>()
const tab = ref('pods')
const loading = ref(false)
const moreOpen = ref<string[]>([])
const condOpen = ref<string[]>([])

// 监控
const range = ref('6h')
const monitor = ref<NodeMonitor>()
const monitorLoading = ref(false)

// 关联事件（事件中心按对象名检索）
const nodeEvents = ref<EventItemEx[]>([])

const monitorCharts = computed(() => [
  { title: 'CPU 使用率', series: [{ name: 'CPU', data: monitor.value?.cpuUsageTrend || [], color: MC.primary, unit: '%' }], yAxisName: '%' },
  { title: '内存使用率', series: [{ name: '内存', data: monitor.value?.memUsageTrend || [], color: MC.sky, unit: '%' }], yAxisName: '%' },
  { title: '磁盘使用率', series: [{ name: '磁盘', data: monitor.value?.diskUsageTrend || [], color: MC.sun, unit: '%' }], yAxisName: '%' },
  {
    title: '网络流量',
    series: [
      { name: '接收', data: monitor.value?.netRxTrend || [], color: MC.primary, unit: 'MB/s' },
      { name: '发送', data: monitor.value?.netTxTrend || [], color: MC.sky, unit: 'MB/s' },
    ],
    yAxisName: 'MB/s',
  },
  { title: '系统负载 (load1)', series: [{ name: 'load1', data: monitor.value?.load1Trend || [], color: MC.grape, unit: '' }], yAxisName: '' },
  { title: '磁盘 IOPS', series: [{ name: '读', data: monitor.value?.diskIopsTrend || [], color: MC.primary, unit: '次/s' }], yAxisName: '次/s' },
  { title: '磁盘吞吐', series: [{ name: '读写合计', data: monitor.value?.diskThroughputTrend || [], color: MC.sky, unit: 'MB/s' }], yAxisName: 'MB/s' },
  { title: '磁盘 IO 利用率', series: [{ name: 'IO 利用率', data: monitor.value?.ioUtilTrend || [], color: MC.sun, unit: '%' }], yAxisName: '%' },
  { title: '网络丢包', series: [{ name: '丢包(收+发)', data: monitor.value?.netDropTrend || [], color: MC.coral, unit: '次/s' }], yAxisName: '次/s' },
  { title: 'TCP 连接（Established）', series: [{ name: 'TCP', data: monitor.value?.tcpTrend || [], color: MC.primary, unit: '条' }], yAxisName: '条' },
])

function fmtNum(v?: number | null): string {
  return v == null ? '-' : v.toFixed(2)
}

// ---- Kuboard 式条件胶囊：Ready=True 实心绿，压力类=False 浅色，异常实心红 ----
const condLabels: Record<string, string> = {
  Ready: '节点就绪',
  DiskPressure: '磁盘压力',
  MemoryPressure: '内存压力',
  PIDPressure: '进程数压力',
  NetworkUnavailable: '网络不可用',
}
const conditionPills = computed(() =>
  (detail.value?.conditions || []).map((c) => {
    const good = c.type === 'Ready' ? c.status === 'True' : c.status === 'False'
    return {
      type: c.type,
      text: condLabels[c.type] || c.type,
      type_: (good ? 'success' : 'danger') as 'success' | 'danger',
      effect: (c.type === 'Ready' || !good ? 'dark' : 'light') as 'dark' | 'light',
    }
  }),
)

// ---- 资源用量条：内存 /  ephemeral 存储 / 容器组数量 ----
function parseMemGi(v?: string): number | null {
  if (!v) return null
  const m = /^(\d+(?:\.\d+)?)(Ki|Mi|Gi|Ti)?$/i.exec(v)
  if (!m) return null
  const n = Number(m[1])
  const unit = (m[2] || '').toLowerCase()
  const factors: Record<string, number> = { '': 1 / 1024 ** 3, ki: 1 / 1024 ** 2, mi: 1 / 1024, gi: 1, ti: 1024 }
  return n * (factors[unit] ?? 1)
}

const usageBars = computed(() => {
  const d = detail.value
  const m = monitor.value
  const rows: { name: string; pct: number; used: string; allocatable: string; capacity: string; level: string }[] = []
  const memCap = parseMemGi(d?.capacity.memory)
  const memAlloc = parseMemGi(d?.allocatable.memory)
  if (memCap) {
    const pct = m?.memUsagePct ?? 0
    rows.push({
      name: '内存',
      pct,
      used: pct ? `${((memCap * pct) / 100).toFixed(2)} GiB` : '-',
      allocatable: `${(memAlloc ?? memCap).toFixed(2)} GiB`,
      capacity: `${memCap.toFixed(2)} GiB`,
      level: pct >= 90 ? 'danger' : pct >= 75 ? 'warn' : '',
    })
  }
  const diskPct = m?.diskUsagePct ?? 0
  rows.push({ name: '临时存储', pct: diskPct, used: `${diskPct.toFixed(1)}%`, allocatable: '-', capacity: d?.capacity['ephemeral-storage'] || '-', level: diskPct >= 90 ? 'danger' : diskPct >= 75 ? 'warn' : '' })
  const podMax = Number(d?.capacity.pods || 0)
  const podNow = d?.pods?.length ?? 0
  if (podMax) {
    rows.push({
      name: '容器组数量',
      pct: (podNow / podMax) * 100,
      used: String(podNow),
      allocatable: String(Number(d?.allocatable.pods || podMax)),
      capacity: String(podMax),
      level: podNow / podMax >= 0.9 ? 'danger' : podNow / podMax >= 0.75 ? 'warn' : '',
    })
  }
  return rows
})

const gauges = computed(() => {
  const m = monitor.value
  const cores = Number(detail.value?.cpuCores || 0)
  const load1 = m?.load1 ?? null
  return [
    { label: 'CPU 使用率', pct: m?.cpuUsagePct ?? null, value: fmtNum(m?.cpuUsagePct), sub: '%' },
    { label: '内存使用率', pct: m?.memUsagePct ?? null, value: fmtNum(m?.memUsagePct), sub: '%' },
    { label: '磁盘使用率', pct: m?.diskUsagePct ?? null, value: fmtNum(m?.diskUsagePct), sub: '%' },
    { label: '负载 (1m)', pct: load1 != null && cores ? (load1 / cores) * 100 : null, value: fmtNum(load1), sub: cores ? `/ ${cores} 核` : '' },
    { label: 'Swap 使用率', pct: m?.swapUsagePct ?? null, value: fmtNum(m?.swapUsagePct), sub: '%' },
    { label: 'IO 利用率', pct: m?.ioUtilPct ?? null, value: fmtNum(m?.ioUtilPct), sub: '%' },
  ]
})

function gaugeColor(pct: number | null | undefined) {
  return levelColor(pct)
}

const capacityRows = computed(() => {
  const d = detail.value
  if (!d) return []
  const resources = ['cpu', 'memory', 'ephemeral-storage', 'pods']
  return resources
    .filter((r) => d.capacity[r])
    .map((r) => ({ resource: r, capacity: d.capacity[r], allocatable: d.allocatable[r] || '-' }))
})

// 镜像页签：一条镜像可能有多个 tag，逐行展开（与 Kuboard 一致），按体积降序（后端已排）
function fmtBytes(b: number): string {
  if (!b) return '-'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let v = b
  let i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}
const imageRows = computed(() =>
  (detail.value?.images || []).flatMap((img) =>
    (img.names.length ? img.names : ['<none>']).map((n) => ({ name: n, sizeBytes: img.sizeBytes, sizeText: fmtBytes(img.sizeBytes) })),
  ),
)

onMounted(() => {
  load()
  loadMonitor() // 趋势图与仪表常驻「资源与指标」区，进入即加载
})

async function load() {
  if (!clusterStore.current) return
  loading.value = true
  try {
    detail.value = await k8sApi.nodeDetail(name.value)
    // 同步污点/标签草稿
    taintDraft.value = (detail.value?.taintItems || []).map((t) => ({ ...t }))
    labelDraft.value = { ...(detail.value?.labels || {}) }
    loadEvents()
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

async function loadEvents() {
  try {
    const all = await eventsApi.list('', '', name.value)
    nodeEvents.value = all.filter((e) => e.object?.includes(name.value))
  } catch {
    nodeEvents.value = []
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

// ------------------- 封锁 / 污点 / 标签 -------------------
const savingTaints = ref(false)
const savingLabels = ref(false)
const taintDraft = ref<{ key: string; value: string; effect: string }[]>([])
const labelDraft = ref<Record<string, string>>({})

async function cordon(on: boolean) {
  const verb = on ? '暂停调度（不再调度新 Pod）' : '恢复调度'
  await ElMessageBox.confirm(`确定${verb}节点「${name.value}」？`, '确认', { type: 'warning' })
  await nodeApi.cordon(name.value, on)
  ElMessage.success(on ? '已暂停调度' : '已恢复调度')
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
.kc-page { display: flex; flex-direction: column; gap: 12px; }
.kc-top-head { display: flex; justify-content: space-between; align-items: center; }
.header-actions { display: flex; align-items: center; gap: 8px; }
.kc-status-strip {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 18px;
  margin-top: 14px;
  font-size: 13px;
  color: var(--kc-mute);
}
.kc-strip-item b { color: var(--kc-ink); font-weight: 600; margin-left: 4px; }
.kc-two-col { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; align-items: start; }
@media (max-width: 1200px) { .kc-two-col { grid-template-columns: 1fr; } }
.kc-cond-row { display: flex; flex-wrap: wrap; gap: 8px; }
.kc-cond-row + .kc-more, .kc-cond-row + el-collapse { margin-top: 10px; }
.kc-more { border-top: none; }
.kc-more-title { color: var(--kc-link); font-size: 13.5px; }
.usage-head { display: flex; align-items: center; gap: 10px; }
.usage-head .spacer { flex: 1; }
.kc-chart-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-top: 16px; }
@media (max-width: 1100px) { .kc-chart-grid { grid-template-columns: 1fr; } }
.kc-chart-card { border: 1px solid var(--kc-border); border-radius: 4px; padding: 8px 10px; }
.kc-chart-title { font-size: 13px; color: var(--kc-ink-2); font-weight: 600; margin-bottom: 4px; }
.kc-kubelet-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-top: 14px; }
.mono { font-family: var(--kc-mono); }
.taint-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; }
.taint-actions { display: flex; gap: 8px; margin-top: 8px; }
.taint-tip { color: var(--el-text-color-secondary); font-size: 13px; margin-top: 10px; }
.label-tip { color: var(--el-text-color-secondary); font-size: 13px; margin-left: 10px; }
</style>
