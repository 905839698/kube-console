<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>{{ title }} ({{ unavailable ? 0 : items.length }})</span>
        <div class="header-right">
          <el-input v-model="search" placeholder="搜索..." :prefix-icon="Search" clearable style="width: 200px" @input="load" />
          <el-button :icon="Refresh" circle @click="load" />
          <template v-if="!isGvr && !unavailable">
            <el-button size="default" :loading="exporting" @click="doExport">导出</el-button>
            <el-button size="default" @click="fileInput?.click()">导入</el-button>
          </template>
          <input ref="fileInput" type="file" accept=".yaml,.yml" style="display: none" @change="onImportFile" />
          <el-button v-if="!unavailable" type="primary" size="default" @click="openCreate">
            <el-icon><Plus /></el-icon>&nbsp;新建
          </el-button>
        </div>
      </div>
    </template>

    <el-alert
      v-if="unavailable"
      :title="unavailableMsg"
      type="warning"
      :closable="false"
      show-icon
      style="margin-bottom: 12px"
    />

    <el-table border v-else :data="items" v-loading="loading" stripe @row-click="onRowClick">
      <el-table-column label="名称" prop="name" min-width="200" sortable>
        <template #default="{ row }">
          <el-link type="primary" @click="onNameClick(row)">{{ row.name }}</el-link>
        </template>
      </el-table-column>
      <el-table-column label="概要" prop="summary" min-width="280" sortable>
        <template #default="{ row }">
          <span v-if="row.summary && row.summary !== '-'" class="summary">{{ row.summary }}</span>
          <span v-else class="summary">-</span>
        </template>
      </el-table-column>
      <el-table-column v-if="kind === 'services'" label="服务地址" min-width="300">
        <template #default="{ row }">
          <div v-if="row.addresses?.length" class="addr-list">
            <div v-for="a in row.addresses" :key="a" class="addr-line">
              <span class="mono-addr">{{ a }}</span>
              <el-button size="small" text type="primary" @click="copyText(a)">复制</el-button>
            </div>
          </div>
          <span v-else class="summary">-</span>
        </template>
      </el-table-column>
      <el-table-column label="标签" min-width="150">
        <template #default="{ row }">
          <el-tag v-for="(v, k) in row.labels" :key="k" size="small" style="margin-right: 4px">{{ k }}={{ v }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="age" label="运行时长" width="80" align="center" sortable :sort-by="(row) => parseDuration(row.age)" />
      <el-table-column label="操作" width="160" fixed="right">
        <template #default="{ row }">
          <el-button size="small" @click="viewDetail(row)">详情</el-button>
          <el-button size="small" type="danger" @click="remove(row)">删除</el-button>
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

    <!-- 详情/编辑（双视图） -->
    <el-dialog v-model="detailVisible" :title="`${title} / ${detailName}`" width="900px" top="5vh" destroy-on-close>
      <div v-loading="detailLoading" class="detail-body">
        <ObjectEditor v-if="detailYaml !== null" :kind="kind" :yaml="detailYaml" :namespace="namespace[0] || 'default'" :namespaced="namespaced" @saved="onSaved" @cancel="detailVisible = false" />
      </div>
      <template #footer>
        <el-button v-if="detailName" type="danger" plain @click="removeFromDetail">删除</el-button>
        <el-button @click="detailVisible = false">关闭</el-button>
      </template>
    </el-dialog>

    <!-- 新建 -->
    <el-dialog v-model="createVisible" :title="`新建 ${title}`" width="900px" top="5vh" destroy-on-close>
      <div class="detail-body">
        <ObjectEditor :kind="kind" :creating="true" :namespace="namespace[0] || 'default'" :namespaced="namespaced" @saved="onCreated" @cancel="createVisible = false" />
      </div>
    </el-dialog>

    <!-- Service 后端 Pod（selector 匹配） -->
    <el-dialog v-model="podsVisible" :title="podsTitle" width="880px">
      <div v-if="podsHint" class="pods-hint">{{ podsHint }}</div>
      <el-table border :data="podItems" size="small" v-loading="podsLoading" stripe>
        <el-table-column prop="name" label="Pod" min-width="220" show-overflow-tooltip />
        <el-table-column label="状态" width="120" align="center">
          <template #default="{ row }"><StatusTag :status="row.status" /></template>
        </el-table-column>
        <el-table-column prop="readyStr" label="就绪" width="70" align="center" />
        <el-table-column prop="restarts" label="重启" width="60" align="center" />
        <el-table-column prop="ip" label="IP" width="130" />
        <el-table-column prop="nodeName" label="节点" min-width="120" show-overflow-tooltip />
        <el-table-column prop="age" label="运行时长" width="90" />
      </el-table>
      <el-empty v-if="!podsLoading && !podItems.length" description="无匹配的 Pod（选择器可能未命中任何 Pod）" />
    </el-dialog>

    <!-- 导入（多文档 YAML 预览 + 逐个应用） -->
    <el-dialog v-model="importVisible" title="导入资源" width="760px" :close-on-click-modal="false">
      <el-alert type="info" :closable="false" style="margin-bottom: 10px"
        :title="`按 create-or-update 逐个应用以下 ${importDocs.length} 个资源（已存在的更新，不存在的创建）`" />
      <el-table border :data="importDocs" size="small" max-height="360">
        <el-table-column label="Kind" width="160" show-overflow-tooltip>
          <template #default="{ row }">{{ row?.kind || row?.apiVersion }}</template>
        </el-table-column>
        <el-table-column label="名称" min-width="180" show-overflow-tooltip>
          <template #default="{ row }">{{ row?.metadata?.name }}</template>
        </el-table-column>
        <el-table-column label="命名空间" width="150">
          <template #default="{ row }">{{ row?.metadata?.namespace || 'default' }}</template>
        </el-table-column>
        <el-table-column label="结果" width="150">
          <template #default="{ $index }">
            <span v-if="importResults[$index] === undefined" class="summary">待应用</span>
            <el-tag v-else-if="!importResults[$index]" size="small" type="success">成功</el-tag>
            <el-tooltip v-else :content="importResults[$index]"><el-tag size="small" type="danger">失败</el-tag></el-tooltip>
          </template>
        </el-table-column>
      </el-table>
      <template #footer>
        <el-button @click="importVisible = false">关闭</el-button>
        <el-button type="primary" :loading="importing" :disabled="!importDocs.length || importDone" @click="doImport">应用</el-button>
      </template>
    </el-dialog>

  </el-card>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ElPagination } from 'element-plus'
import { Refresh, Search } from '@element-plus/icons-vue'
import { load as yamlLoad, loadAll as yamlLoadAll, dump as yamlDump } from 'js-yaml'
import { k8sApi, nsParam, type GenericItem, type GatewayAvailability, type WorkloadItem } from '../api'
import ObjectEditor from '../components/ObjectEditor.vue'
import StatusTag from '../components/StatusTag.vue'
import { downloadText } from '../utils/download'
import { useClusterStore } from '../store/cluster'
import { useNamespaceStore } from '../store/namespace'
import { parseDuration } from '../utils/sort'

// Gateway API 资源：版本随渠道变化，需 discovery 确认当前集群是否提供
const GATEWAY_KINDS = new Set(['gatewayclasses', 'gateways', 'httproutes', 'grpcroutes', 'tlsroutes', 'tcproutes', 'udproutes', 'referencegrants'])

const route = useRoute()
const router = useRouter()
const clusterStore = useClusterStore()
const nsStore = useNamespaceStore()

// 两种模式：kind 模式（/resources/:kind）或 GVR 模式（/crd/:group/:version/:resource）
const isGvr = computed(() => !!route.params.group)
const kind = computed(() => String(route.params.kind || route.params.resource))
const group = computed(() => String(route.params.group || ''))
const version = computed(() => String(route.params.version || ''))
const resource = computed(() => String(route.params.resource || ''))
const namespaced = computed(() => route.query.namespaced !== '0')

const title = computed(() => {
  const t = route.meta.title
  return (t ? String(t) : '') || (isGvr.value ? resource.value : kindTitle(kind.value))
})

const items = ref<GenericItem[]>([])
const page = ref(1)
const pageSize = ref(20)
const paged = computed(() => {
  const start = (page.value - 1) * pageSize.value
  return items.value.slice(start, start + pageSize.value)
})
// 命名空间：集群级资源不筛选；否则 URL query 优先（CRD/列表跳转），跟随顶栏全局选择；有 query 时同步到全局
const namespace = ref<string[]>(initNamespace())
function initNamespace(): string[] {
  if (route.query.namespaced === '0') return []
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
const detailVisible = ref(false)
const detailName = ref('')
const detailYaml = ref<string | null>(null)
const detailLoading = ref(false)
const createVisible = ref(false)

// Gateway API 资源可用性（discovery 解析）
const gatewayAvail = ref<GatewayAvailability | null>(null)
const unavailable = computed(() => !isGvr.value && GATEWAY_KINDS.has(kind.value) && !!gatewayAvail.value && !gatewayAvail.value.found)
const unavailableMsg = computed(() =>
  `当前集群未提供 ${title.value}（Gateway API 未安装或未启用该资源）。` +
  (gatewayAvail.value?.version ? ` 可用版本：${gatewayAvail.value.version}。` : ' 请在集群安装/启用 Gateway API 后重试。'),
)


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

// 顶栏全局命名空间变化时立即跟随（集群级资源不筛选；页面选择已写回 store 时值相同，不会循环）
watch(
  () => nsStore.selected,
  (v) => {
    if (route.query.namespaced === '0') return
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
onMounted(() => {
  load()
  checkAvailability()
})
watch([kind, group, version], () => {
  load()
  checkAvailability()
})
watch(() => clusterStore.current, () => {
  load()
  checkAvailability()
})

// Gateway API 资源：通过 discovery 确认当前集群是否提供该版本
async function checkAvailability() {
  gatewayAvail.value = null
  if (isGvr.value || !GATEWAY_KINDS.has(kind.value)) return
  try {
    const list = await k8sApi.gatewayAvailability()
    gatewayAvail.value = list.find((a) => a.kind === kind.value) || null
  } catch {
    /* 非致命：保持列表加载，拦截器已提示 */
  }
}

async function load() {
  clearTimeout(timer)
  timer = setTimeout(async () => {
    if (!clusterStore.current) return
    // Gateway 资源不可用时不请求列表（由横幅提示，避免重复报错）
    if (unavailable.value) {
      items.value = []
      return
    }
    loading.value = true
    try {
      if (isGvr.value) {
        items.value = await k8sApi.genericList(group.value, version.value, resource.value, nsParam(namespace.value), search.value)
      } else {
        items.value = await k8sApi.resources(kind.value, nsParam(namespace.value), search.value)
      }
    } catch {
      /* 拦截器已提示 */
    } finally {
      loading.value = false
    }
  }, 200)
}

async function fetchYaml(name: string, ns: string): Promise<string> {
  if (isGvr.value) {
    const { yaml } = await k8sApi.genericYaml(group.value, version.value, resource.value, ns, name)
    return yaml
  }
  const { yaml } = await k8sApi.resourceYaml(kind.value, ns, name)
  return yaml
}

function onRowClick(row: GenericItem, _col: unknown, event: Event) {
  if ((event.target as HTMLElement).closest('.el-button')) return
  onNameClick(row)
}

// 名称点击：Service 展示 selector 匹配的后端 Pod，其余资源进详情
function onNameClick(row: GenericItem) {
  if (kind.value === 'services') {
    viewServicePods(row)
  } else {
    viewDetail(row)
  }
}

// Service 后端 Pod：拉取 Service YAML 取 selector，列出该 ns 下标签匹配的 Pod
const podsVisible = ref(false)
const podsLoading = ref(false)
const podsTitle = ref('')
const podsHint = ref('')
const podItems = ref<WorkloadItem[]>([])

async function viewServicePods(row: GenericItem) {
  const ns = row.namespace || namespace.value[0] || 'default'
  podsTitle.value = `Service / ${row.name} · 后端 Pod（${ns}）`
  podsHint.value = ''
  podItems.value = []
  podsVisible.value = true
  podsLoading.value = true
  try {
    const yamlStr = await fetchYaml(row.name, ns)
    const svc: any = yamlLoad(yamlStr)
    const entries = Object.entries(svc?.spec?.selector || {})
    if (!entries.length) {
      podsHint.value = '该 Service 未定义标签选择器（ExternalName 类型或 Endpoints 手动管理）'
      return
    }
    podsHint.value = `选择器：${entries.map(([k, v]) => `${k}=${v}`).join(', ')}`
    const pods = await k8sApi.workloads('pods', ns)
    podItems.value = (pods || []).filter((p) => entries.every(([k, v]) => p.labels?.[k] === v))
  } catch (e) {
    ElMessage.error(`加载后端 Pod 失败：${(e as Error).message || e}`)
  } finally {
    podsLoading.value = false
  }
}

async function viewDetail(row: GenericItem) {
  // 立即打开弹窗并显示加载中（YAML 从集群拉取需 1-2s）
  detailName.value = row.name
  detailYaml.value = null
  detailLoading.value = true
  detailVisible.value = true
  try {
    detailYaml.value = await fetchYaml(row.name, row.namespace || namespace.value[0] || '')
  } catch (e) {
    ElMessage.error(`加载 ${title.value} 详情失败：${(e as Error).message || e}`)
  } finally {
    detailLoading.value = false
  }
}

async function copyText(text: string) {
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    // 非安全上下文或剪贴板 API 不可用时的兜底
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    ta.remove()
  }
  ElMessage.success(`已复制：${text}`)
}

async function remove(row: GenericItem) {
  await doRemove(row.name, row.namespace || namespace.value[0] || '')
  load()
}

async function removeFromDetail() {
  await doRemove(detailName.value, namespace.value[0] || '')
  detailVisible.value = false
  load()
}

async function doRemove(name: string, ns: string) {
  try {
    await ElMessageBox.confirm(`确定删除 ${title.value} ${name}？`, '删除', { type: 'warning' })
  } catch {
    return
  }
  if (isGvr.value) {
    await k8sApi.genericDelete(group.value, version.value, resource.value, ns, name)
  } else {
    await k8sApi.deleteResource(kind.value, ns, name)
  }
  ElMessage.success('已删除')
}

function openCreate() {
  createVisible.value = true
}

// ---- 导入 / 导出（kind 模式；导出跟随当前命名空间筛选与搜索，深度清洗后可直接再导入） ----
const exporting = ref(false)
const fileInput = ref<HTMLInputElement>()
const importVisible = ref(false)
const importing = ref(false)
const importDone = ref(false)
const importDocs = ref<any[]>([])
const importResults = ref<Record<number, string>>({})

async function doExport() {
  if (!items.value.length) {
    ElMessage.warning('当前列表为空，无可导出资源')
    return
  }
  exporting.value = true
  try {
    const { yaml } = await k8sApi.exportYaml(kind.value, nsParam(namespace.value), search.value)
    if (!yaml.trim()) {
      ElMessage.warning('无可导出资源')
      return
    }
    const ns = (nsParam(namespace.value) || 'all').replace(/[^a-zA-Z0-9_-]/g, '-')
    downloadText(yaml, `${kind.value}_${ns}.yaml`, 'text/yaml;charset=utf-8')
  } catch {
    /* 拦截器已提示 */
  } finally {
    exporting.value = false
  }
}

async function onImportFile(ev: Event) {
  const input = ev.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = '' // 允许再次选择同一文件
  if (!file) return
  try {
    const docs = (yamlLoadAll(await file.text()) as any[]).filter((d) => d && typeof d === 'object' && d.metadata?.name)
    if (!docs.length) {
      ElMessage.warning('文件中未找到资源定义（需带 apiVersion/kind/metadata.name）')
      return
    }
    importDocs.value = docs
    importResults.value = {}
    importDone.value = false
    importVisible.value = true
  } catch (e) {
    ElMessage.error(`解析文件失败：${(e as Error).message || e}`)
  }
}

async function doImport() {
  importing.value = true
  let ok = 0
  for (let i = 0; i < importDocs.value.length; i++) {
    try {
      await k8sApi.applyYaml(yamlDump(importDocs.value[i]))
      importResults.value[i] = ''
      ok++
    } catch (e) {
      importResults.value[i] = (e as Error).message || String(e)
    }
  }
  importing.value = false
  importDone.value = true
  if (ok === importDocs.value.length) ElMessage.success(`已应用 ${ok} 个资源`)
  else ElMessage.warning(`应用完成：成功 ${ok} / ${importDocs.value.length}，失败项见列表`)
  load()
}

function onSaved() {
  detailVisible.value = false
  load()
}

function onCreated() {
  createVisible.value = false
  load()
}

function kindTitle(kind: string): string {
  const titles: Record<string, string> = {
    services: 'Service', ingresses: 'Ingress', configmaps: 'ConfigMap', secrets: 'Secret',
    persistentvolumeclaims: 'PVC', persistentvolumes: 'PV', storageclasses: 'StorageClass',
    networkpolicies: 'NetworkPolicy', serviceaccounts: 'ServiceAccount',
    horizontalpodautoscalers: 'HPA', roles: 'Role', rolebindings: 'RoleBinding',
    clusterroles: 'ClusterRole', clusterrolebindings: 'ClusterRoleBinding',
    resourcequotas: 'ResourceQuota', limitranges: 'LimitRange',
    gatewayclasses: 'GatewayClass', gateways: 'Gateway', httproutes: 'HTTPRoute',
    tcproutes: 'TCPRoute', udproutes: 'UDPRoute',
    grpcroutes: 'GRPCRoute', tlsroutes: 'TLSRoute', referencegrants: 'ReferenceGrant',
    routes: '路由',
    endpoints: 'Endpoints', endpointslices: 'EndpointSlice', events: 'Events',
  }
  return titles[kind] || kind
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.header-right { display: flex; gap: 8px; align-items: center; }
.summary { color: #606266; }
.mono-addr { font-family: ui-monospace, Consolas, monospace; font-size: 12px; color: #303133; }
.addr-list { display: flex; flex-direction: column; gap: 2px; }
.addr-line { display: flex; align-items: center; gap: 2px; }
/* 详情/新建对话框内容自适应：高度随视口，内容超出时滚动 */
.detail-body { height: calc(100vh - 240px); min-height: 400px; overflow-y: auto; }
.pods-hint { margin-bottom: 10px; font-size: 12.5px; color: #606266; }
.pager { margin-top: 12px; display: flex; justify-content: flex-end; }
</style>
