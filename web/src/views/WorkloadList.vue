<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>{{ kindTitle }} ({{ items.length }})</span>
        <div class="header-right">
          <el-button v-if="selected.length && !isPod" size="small" type="warning" plain :disabled="kind === 'cronjobs' || kind === 'jobs'" @click="batchRestart">重启选中 ({{ selected.length }})</el-button>
          <el-button v-if="selected.length" size="small" type="danger" plain @click="batchDelete">删除选中 ({{ selected.length }})</el-button>
          <el-input v-model="search" placeholder="搜索..." :prefix-icon="Search" clearable style="width: 200px" @input="load" />
          <el-button :icon="Refresh" circle @click="load" />
          <el-button size="default" @click="exportVisible = true">导出</el-button>
          <el-button size="default" @click="importDlg?.pick()">导入</el-button>
          <el-button v-if="!isPod" type="primary" size="default" @click="openCreate">
            <el-icon><Plus /></el-icon>&nbsp;新建
          </el-button>
        </div>
      </div>
    </template>

    <el-table border :data="paged" v-loading="loading" stripe @row-click="onRowClick" @selection-change="(rows: WorkloadItem[]) => (selected = rows)">
      <el-table-column type="selection" width="42" />
      <el-table-column label="名称" prop="name" min-width="200" sortable>
        <template #default="{ row }">
          <el-link type="primary" @click="goDetail(row)">{{ row.name }}</el-link>
        </template>
      </el-table-column>
      <el-table-column v-if="!isPod" label="副本" width="90" align="center" sortable :sort-by="(row) => Number(row.ready || 0)">
        <template #default="{ row }">
          <span :class="{ 'ready-ok': row.ready === row.replicas, 'ready-warn': row.ready !== row.replicas }">
            {{ row.ready }}/{{ row.replicas }}
          </span>
        </template>
      </el-table-column>
      <template v-if="isPod">
        <el-table-column label="状态" width="150" sortable>
          <template #default="{ row }"><StatusTag :status="row.status" /></template>
        </el-table-column>
        <el-table-column prop="readyStr" label="就绪" width="70" align="center" />
        <el-table-column prop="restarts" label="重启" width="70" align="center" sortable :sort-by="(row) => Number(row.restarts || 0)" />
        <el-table-column prop="ip" label="IP" width="130" sortable />
        <el-table-column prop="nodeName" label="节点" min-width="150" show-overflow-tooltip sortable />
      </template>
      <el-table-column v-if="hasExtra" prop="extra" label="调度/完成" min-width="200" show-overflow-tooltip />
      <el-table-column prop="images" label="镜像" min-width="220" show-overflow-tooltip sortable>
        <template #default="{ row }">{{ row.images.join(', ') }}</template>
      </el-table-column>
      <el-table-column label="标签" min-width="150">
        <template #default="{ row }">
          <el-tag v-for="(v, k) in row.labels" :key="k" size="small" style="margin-right: 4px">{{ k }}={{ v }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="age" label="运行时长" width="80" align="center" sortable :sort-by="(row) => parseDuration(row.age)" />
      <el-table-column label="操作" width="200" fixed="right">
        <template #default="{ row }">
          <el-dropdown trigger="click" @command="(cmd: string) => onCommand(cmd, row)">
            <el-button size="small" type="primary" plain>操作<el-icon><ArrowDown /></el-icon></el-button>
            <template #dropdown>
              <el-dropdown-menu v-if="isPod">
                <el-dropdown-item command="detail">详情</el-dropdown-item>
                <el-dropdown-item command="logs">日志</el-dropdown-item>
                <el-dropdown-item command="terminal">终端</el-dropdown-item>
                <el-dropdown-item command="evict" divided>驱除（尊重 PDB）</el-dropdown-item>
                <el-dropdown-item command="delete" divided>删除</el-dropdown-item>
              </el-dropdown-menu>
              <el-dropdown-menu v-else>
                <el-dropdown-item command="detail">详情</el-dropdown-item>
                <el-dropdown-item command="edit">可视化编辑</el-dropdown-item>
                <el-dropdown-item command="yaml" divided>编辑 YAML</el-dropdown-item>
                <el-dropdown-item command="scale" :disabled="kind === 'daemonsets' || kind === 'cronjobs'">缩放</el-dropdown-item>
                <el-dropdown-item command="restart" :disabled="kind === 'cronjobs' || kind === 'jobs'">重启</el-dropdown-item>
                <el-dropdown-item command="rollouts" v-if="kind === 'deployments'">历史版本/回滚</el-dropdown-item>
                <el-dropdown-item command="image">调整镜像</el-dropdown-item>
                <el-dropdown-item command="delete" divided>删除</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </template>
      </el-table-column>
    </el-table>
    <div class="pager">
      <el-pagination
        v-model:current-page="page"
        v-model:page-size="pageSize" @size-change="page = 1"
        :total="items.length"
        layout="total, prev, pager, next, sizes"
        :page-sizes="[20, 50, 100]"
        background
      />
    </div>

    <!-- 新建：可视化编辑（表单|YAML 双视图） -->
    <el-dialog v-model="createVisible" :title="`新建 ${kindTitle}`" width="920px" top="5vh" destroy-on-close>
      <div style="min-height: 480px">
        <ObjectEditor :kind="kind" :creating="true" :namespace="createNs" @saved="onCreated" @cancel="createVisible = false" />
      </div>
    </el-dialog>
    <!-- 编辑 YAML -->
    <YamlDialog v-model="editVisible" :yaml="editYaml" :readonly="false" @applied="load" />
    <!-- 可视化编辑（表单|YAML 双视图） -->
    <el-dialog v-model="formEditVisible" :title="`可视化编辑 - ${kindTitle} / ${formEditName}`" width="920px" top="5vh" destroy-on-close>
      <div style="min-height: 480px">
        <ObjectEditor v-if="formEditYaml !== null" :kind="kind" :yaml="formEditYaml" :namespace="namespace" @saved="onFormSaved" @cancel="formEditVisible = false" />
      </div>
    </el-dialog>

    <!-- Pod 日志 / 终端（与控制器共用列表框架） -->
    <PodLogsDrawer v-model="logsVisible" :pod="logsPod" />
    <WebTerminal
      v-if="terminalPod"
      v-model:visible="terminalVisible"
      :namespace="terminalPod.namespace"
      :pod="terminalPod.name"
    />

    <!-- 缩放 -->
    <el-dialog v-model="scaleVisible" title="调整副本数" width="420px">
      <el-form label-width="90px">
        <el-form-item label="工作负载">
          <el-input :model-value="scaleTarget?.name || ''" disabled />
        </el-form-item>
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
    <ImageUpdateDialog
      v-model="imageVisible"
      :kind="kind"
      :namespace="imageTarget?.namespace || namespace[0] || 'default'"
      :name="imageTarget?.name || ''"
      @saved="load"
    />

    <!-- Kuboard 式导出（逐层选择：ns → 控制器/服务/配置/其他） -->
    <ResourceExportDialog v-model="exportVisible" />
    <!-- 导入（多文档 YAML 预览 + 逐个应用） -->
    <ResourceImportDialog ref="importDlg" @done="load" />
  </el-card>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ElPagination } from 'element-plus'
import { Refresh, Search } from '@element-plus/icons-vue'
import { k8sApi, nsParam, type WorkloadItem } from '../api'
import YamlDialog from '../components/YamlDialog.vue'
import ObjectEditor from '../components/ObjectEditor.vue'
import StatusTag from '../components/StatusTag.vue'
import PodLogsDrawer from '../components/PodLogsDrawer.vue'
import WebTerminal from '../components/WebTerminal.vue'
import ImageUpdateDialog from '../components/ImageUpdateDialog.vue'
import ResourceExportDialog from '../components/ResourceExportDialog.vue'
import ResourceImportDialog from '../components/ResourceImportDialog.vue'
import { useClusterStore } from '../store/cluster'
import { useNamespaceStore } from '../store/namespace'
import { parseDuration } from '../utils/sort'
import { confirmDelete, confirmDeleteCount } from '../utils/confirm'

const route = useRoute()
const router = useRouter()
const clusterStore = useClusterStore()
const nsStore = useNamespaceStore()

const kind = computed(() => String(route.params.kind))
const kindTitle = computed(() => {
  const titles: Record<string, string> = {
    deployments: 'Deployment', statefulsets: 'StatefulSet', daemonsets: 'DaemonSet',
    cronjobs: 'CronJob', jobs: 'Job', replicasets: 'ReplicaSet', replicationcontrollers: 'ReplicationController',
    pods: 'Pod',
  }
  return titles[kind.value] || kind.value
})
const hasExtra = computed(() => ['cronjobs', 'jobs'].includes(kind.value))
// Pod 与控制器同页融合：列与操作按类型切换
const isPod = computed(() => kind.value === 'pods')

const items = ref<WorkloadItem[]>([])
const page = ref(1)
const pageSize = ref(20)
const selected = ref<WorkloadItem[]>([])
const paged = computed(() => {
  const start = (page.value - 1) * pageSize.value
  return items.value.slice(start, start + pageSize.value)
})

const nsOf = (row: WorkloadItem) => row.namespace || namespace.value[0] || 'default'

async function batchDelete() {
  if (!selected.value.length) return
  await confirmDeleteCount(selected.value.length, { title: '批量删除', warning: `将删除选中的 ${selected.value.length} 个 ${kindTitle.value}，不可恢复。` })
  for (const w of selected.value) {
    try { await k8sApi.deleteWorkload(kind.value, nsOf(w), w.name) } catch { /* 拦截器已提示 */ }
  }
  ElMessage.success(`已批量删除 ${selected.value.length} 个`)
  await load()
}

async function batchRestart() {
  if (!selected.value.length) return
  await ElMessageBox.confirm(`确定重启选中的 ${selected.value.length} 个 ${kindTitle.value}？将触发滚动更新。`, '批量重启', { type: 'warning' })
  for (const w of selected.value) {
    try { await k8sApi.restartWorkload(kind.value, nsOf(w), w.name) } catch { /* 拦截器已提示 */ }
  }
  ElMessage.success(`已批量重启 ${selected.value.length} 个`)
  await load()
}
// 命名空间：URL query 优先（矩阵/命名空间页跳转），否则跟随顶栏全局选择；有 query 时同步到全局
const namespace = ref<string[]>(initNamespace())
function initNamespace(): string[] {
  const q = String(route.query.namespace || '')
  if (q) {
    const arr = q.split(',').filter(Boolean).map((v) => (v === '*' ? '__all__' : v))
    nsStore.select(arr)
    return arr
  }
  return [...nsStore.selected]
}
const search = ref(String(route.query.search || ''))
const loading = ref(false)

const createVisible = ref(false)
// 导入 / 导出（导出为 Kuboard 式分层勾选对话框，与当前列表 kind 无关）
const exportVisible = ref(false)
const importDlg = ref<InstanceType<typeof ResourceImportDialog>>()
// 新建默认命名空间：全选时用 default，否则用第一个选中
const createNs = computed(() => (nsParam(namespace.value) === '*' ? 'default' : namespace.value[0] || 'default'))
const editVisible = ref(false)
const editYaml = ref('')
const editTarget = ref<WorkloadItem>()
const formEditVisible = ref(false)
const formEditYaml = ref<string | null>(null)
const formEditName = ref('')
const scaleVisible = ref(false)
const scaleTarget = ref<WorkloadItem>()
const scaleReplicas = ref(1)
const scaling = ref(false)
const imageVisible = ref(false)
const imageTarget = ref<WorkloadItem>()

// Pod 日志 / 终端（字段与 PodItem 兼容，按 any 透传避免结构约束）
const logsVisible = ref(false)
const logsPod = ref<any>()
const terminalVisible = ref(false)
const terminalPod = ref<any>()


// 筛选状态与 URL 同步：全选/多选/搜索在刷新、返回、分享时保持；变化同时写回全局 store
watch(
  namespace,
  () => {
    nsStore.select(namespace.value)
    const ns = nsParam(namespace.value)
    router.replace({ query: { ...route.query, namespace: ns || undefined } })
    load()
  },
  { deep: true },
)

// 顶栏全局命名空间变化时立即跟随（页面选择已写回 store 时值相同，不会循环）
watch(
  () => nsStore.selected,
  (v) => {
    if (JSON.stringify(v) !== JSON.stringify(namespace.value)) {
      namespace.value = [...v]
    }
  },
)

let searchTimer: ReturnType<typeof setTimeout> | undefined
watch(search, () => {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    router.replace({ query: { ...route.query, search: search.value || undefined } })
  }, 500)
})

let timer: ReturnType<typeof setTimeout> | undefined
onMounted(load)
watch(kind, load)

async function load() {
  clearTimeout(timer)
  timer = setTimeout(async () => {
    if (!clusterStore.current) return
    loading.value = true
    try {
      items.value = await k8sApi.workloads(kind.value, nsParam(namespace.value), search.value)
    } catch {
      /* 拦截器已提示 */
    } finally {
      loading.value = false
    }
  }, 200)
}

function onRowClick(row: WorkloadItem, _col: unknown, event: Event) {
  if ((event.target as HTMLElement).closest('.el-button')) return
  goDetail(row)
}

function goDetail(row: WorkloadItem) {
  const ns = row.namespace || namespace.value[0] || 'default'
  // Pod 详情走专用详情页（容器/事件/文件/监控）
  if (isPod.value) {
    router.push(`/pods/${ns}/${row.name}`)
    return
  }
  router.push({ path: `/workloads/${kind.value}/${row.name}`, query: { namespace: ns } })
}

function openCreate() {
  createVisible.value = true
}

async function onCommand(cmd: string, row: WorkloadItem) {
  const ns = row.namespace || namespace.value[0] || 'default'
  switch (cmd) {
    case 'detail':
      goDetail(row)
      break
    case 'logs':
      logsPod.value = row
      logsVisible.value = true
      break
    case 'terminal':
      terminalPod.value = row
      terminalVisible.value = true
      break
    case 'evict':
      await confirmAndDo(
        `确定驱除 Pod ${row.name}？将调用 Eviction API（尊重 PDB）；无控制器时会真正删除。`,
        async () => {
          await k8sApi.evictPod(ns, row.name)
          ElMessage.success('已触发驱除')
        },
      )
      break
    case 'yaml': {
      const { yaml } = await k8sApi.getYaml(kind.value, row.namespace || namespace.value[0] || 'default', row.name)
      editYaml.value = yaml
      editTarget.value = row
      editVisible.value = true
      break
    }
    case 'edit': {
      const { yaml } = await k8sApi.getYaml(kind.value, row.namespace || namespace.value[0] || 'default', row.name)
      formEditYaml.value = yaml
      formEditName.value = row.name
      formEditVisible.value = true
      break
    }
    case 'scale':
      scaleTarget.value = row
      scaleReplicas.value = row.replicas
      scaleVisible.value = true
      break
    case 'restart':
      await confirmAndDo(`确定重启 ${row.name}？将触发滚动更新。`, async () => {
        await k8sApi.restartWorkload(kind.value, row.namespace || namespace.value[0] || 'default', row.name)
        ElMessage.success('已触发重启')
      })
      break
    case 'rollouts':
      // 回滚交互（版本列表 + 确认）在详情页，跳过去并自动打开历史版本对话框
      router.push({ path: `/workloads/${kind.value}/${row.name}`, query: { namespace: ns, rollouts: '1' } })
      break
    case 'image':
      imageTarget.value = row
      imageVisible.value = true
      break
    case 'delete':
      await confirmDelete(row.name, { title: `删除${kindTitle.value}`, warning: `命名空间 ${row.namespace || namespace.value[0] || 'default'}` })
      try {
        await k8sApi.deleteWorkload(kind.value, row.namespace || namespace.value[0] || 'default', row.name)
        ElMessage.success('已删除')
      } catch { /* 拦截器已提示 */ }
      load()
      break
  }
}

async function confirmAndDo(msg: string, fn: () => Promise<void>) {
  try {
    await ElMessageBox.confirm(msg, '确认操作', { type: 'warning' })
  } catch {
    return
  }
  await fn()
  load()
}

async function doScale() {
  if (!scaleTarget.value) return
  scaling.value = true
  try {
    await k8sApi.scaleWorkload(kind.value, scaleTarget.value.namespace || namespace.value[0] || 'default', scaleTarget.value.name, scaleReplicas.value)
    ElMessage.success('副本数已更新')
    scaleVisible.value = false
    load()
  } finally {
    scaling.value = false
  }
}

function onFormSaved() {
  formEditVisible.value = false
  load()
}

function onCreated() {
  createVisible.value = false
  load()
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.header-right { display: flex; gap: 8px; align-items: center; }
.ready-ok { color: #67c23a; font-weight: 600; }
.ready-warn { color: #e6a23c; font-weight: 600; }
.pager { margin-top: 12px; display: flex; justify-content: flex-end; }
</style>
