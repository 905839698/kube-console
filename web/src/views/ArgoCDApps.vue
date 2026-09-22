<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>ArgoCD 应用（CD 视图）</span>
        <div>
          <el-button v-if="userStore.isAdmin && installed" type="primary" size="small" @click="openCreate">新建应用</el-button>
          <el-button :icon="Refresh" circle @click="load" />
        </div>
      </div>
    </template>

    <el-alert v-if="!installed" type="info" :closable="false"
      title="当前集群未部署 ArgoCD（未发现 applications.argoproj.io CRD）。" />

    <template v-else>
      <el-alert v-if="loadError" type="error" :closable="false" style="margin-bottom: 10px"
        :title="`应用列表加载失败：${loadError}（不是「未部署 ArgoCD」，请检查当前集群的 RBAC / 网络）`" />
      <el-alert v-if="specErrors.length" type="error" :closable="false" show-icon style="margin-bottom: 10px"
        title="以下应用 spec 非法，ArgoCD 无法加载（同步/健康状态恒为 Unknown）：">
        <div v-for="e in specErrors" :key="e.namespace + '/' + e.name">
          • {{ e.namespace }}/{{ e.name }}：{{ e.specError }}
          <span v-if="e.project" class="spec-hint">（spec.project = {{ e.project }}）</span>
        </div>
      </el-alert>

      <!-- 原生式状态筛选 chips + 项目/搜索 -->
      <div class="filter-bar">
        <el-check-tag v-for="f in statusFilters" :key="f.key" :checked="activeFilter === f.key"
          @change="activeFilter = f.key">
          {{ f.label }}<span class="chip-count">{{ countOf(f.key) }}</span>
        </el-check-tag>
        <div class="filter-right">
          <el-select v-model="projectFilter" placeholder="全部 Project" clearable size="small" style="width: 160px">
            <el-option v-for="p in projectNames" :key="p" :label="p" :value="p" />
          </el-select>
          <el-input v-model="keyword" placeholder="搜索应用名" size="small" clearable style="width: 180px"
            :prefix-icon="Search" />
        </div>
      </div>

      <el-table border :data="filtered" v-loading="loading" size="small" stripe
        row-class-name="app-row" @row-click="openDrawer">
        <el-table-column prop="name" label="应用" min-width="170">
          <template #default="{ row }">
            <span class="h-dot" :style="{ background: healthColor(row.health) }" />
            <span class="app-name">{{ row.name }}</span>
            <el-tag v-if="row.autoSync && !row.paused" size="small" type="success" class="mini-tag">自动同步</el-tag>
            <el-tag v-if="row.paused" size="small" type="warning" class="mini-tag">已暂停</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="同步状态" width="118" align="center">
          <template #default="{ row }">
            <el-tooltip v-if="row.specError" :content="row.specError" placement="top">
              <el-tag size="small" type="danger">{{ row.sync }}</el-tag>
            </el-tooltip>
            <el-tag v-else size="small" :type="syncType(row.sync)">{{ row.sync }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="健康状态" width="118" align="center">
          <template #default="{ row }">
            <el-tag size="small" :type="healthType(row.health)">{{ row.health }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="project" label="Project" width="130" show-overflow-tooltip>
          <template #default="{ row }">
            <span :class="{ 'spec-bad': !!row.specError }">{{ row.project || 'default' }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="repoURL" label="配置仓库" min-width="200" show-overflow-tooltip />
        <el-table-column prop="path" label="路径" min-width="130" show-overflow-tooltip />
        <el-table-column prop="target" label="Revision" width="100" />
        <el-table-column prop="destNamespace" label="目标命名空间" width="120" />
        <el-table-column prop="age" label="创建于" width="80" align="center" />
        <el-table-column label="操作" width="150" fixed="right" @click.stop>
          <template #default="{ row }">
            <div @click.stop>
              <el-button size="small" text type="primary" :disabled="!canOperate(row)" @click="doSync(row)">SYNC</el-button>
              <el-button size="small" text type="warning" :disabled="!canOperate(row) || !row.autoSync"
                @click="doPause(row)">{{ row.paused ? 'RESUME' : 'PAUSE' }}</el-button>
              <el-dropdown trigger="click" @command="(cmd: string) => onMore(cmd, row)">
                <el-button size="small" text>更多<el-icon><ArrowDown /></el-icon></el-button>
                <template #dropdown>
                  <el-dropdown-menu>
                    <el-dropdown-item command="refresh">刷新比对（软）</el-dropdown-item>
                    <el-dropdown-item command="hard">重新获取（硬刷新）</el-dropdown-item>
                    <el-dropdown-item command="autosync">{{ row.autoSync ? '关闭自动同步' : '开启自动同步' }}</el-dropdown-item>
                    <el-dropdown-item command="edit" divided>编辑（表单/YAML）</el-dropdown-item>
                    <el-dropdown-item command="yaml">查看 YAML</el-dropdown-item>
                    <el-dropdown-item command="delete" style="color: #f56c6c">删除应用…</el-dropdown-item>
                  </el-dropdown-menu>
                </template>
              </el-dropdown>
            </div>
          </template>
        </el-table-column>
      </el-table>
      <el-alert type="info" :closable="false" style="margin-top: 10px">
        SYNC / PAUSE / 自动同步 / 删除按应用<b>目标命名空间</b>的写权限放行（edit/admin 或平台管理员）；
        SYNC 通过 Application 的 operation 字段触发（部分 ArgoCD 版本 CRD 不支持时会给出提示，可改用自动同步）。
        点击行打开详情抽屉（资源树 / 操作进度 / 历史）。私有仓库凭据在
        <router-link to="/argocd/repos">ArgoCD 仓库</router-link> 页面注册。
      </el-alert>
    </template>

    <!-- 详情抽屉（原生 app details 风格） -->
    <el-drawer v-model="drawerVisible" size="62%" :destroy-on-close="true">
      <template #header>
        <div class="drawer-head" v-if="drawerApp">
          <span class="drawer-title">{{ drawerApp.name }}</span>
          <el-tag size="small" :type="syncType(drawerApp.sync)">SYNC {{ drawerApp.sync }}</el-tag>
          <el-tag size="small" :type="healthType(drawerApp.health)">HEALTH {{ drawerApp.health }}</el-tag>
          <el-tag v-if="detail?.operation?.phase" size="small" type="primary">
            操作 {{ detail.operation.phase }}
          </el-tag>
        </div>
      </template>
      <div v-loading="drawerLoading">
        <template v-if="detail">
          <!-- 操作进度 -->
          <el-alert v-if="detail.operation && detail.operation.phase === 'Running'" type="primary" :closable="false"
            show-icon class="op-alert" :title="`同步进行中：${detail.operation.message || 'ArgoCD 正在应用清单'}`" />
          <el-alert v-else-if="detail.operation && ['Failed', 'Error'].includes(detail.operation.phase)" type="error"
            :closable="false" show-icon class="op-alert"
            :title="`最近操作 ${detail.operation.phase}`" :description="detail.operation.message" />

          <el-tabs v-model="drawerTab">
            <el-tab-pane label="资源树" name="tree">
              <el-input v-model="treeFilter" placeholder="过滤 kind / 名称" size="small" clearable class="tree-filter"
                :prefix-icon="Search" />
              <el-tree v-if="treeData.length" ref="treeRef" :data="treeData" node-key="key"
                :filter-node-method="filterTreeNode" :props="{ children: 'children', label: '' }" default-expand-all>
                <template #default="{ data }">
                  <span class="tree-node">
                    <span class="h-dot" :style="{ background: healthColor(data.health || (data.kind === 'Application' ? data.health : '')) }" />
                    <span class="tree-kind">{{ data.kind }}</span>
                    <span class="tree-name" :title="`${data.namespace || ''}/${data.name}`">{{ data.name }}</span>
                    <el-tag v-if="data.sync && data.sync !== 'Synced' && data.kind !== 'Application'" size="small"
                      :type="data.sync === 'OutOfSync' ? 'warning' : 'info'">{{ data.sync }}</el-tag>
                    <el-tag v-if="data.missing" size="small" type="info">集群缺失</el-tag>
                    <el-tag v-if="data.requiresPruning" size="small" type="warning">待剪枝</el-tag>
                    <el-button v-if="treeJump(data)" size="small" text type="primary" class="tree-jump"
                      @click.stop="goResource(data, treeJump(data)!)">打开</el-button>
                  </span>
                </template>
              </el-tree>
              <el-empty v-else description="该应用没有托管资源（尚未同步，或资源超过展示上限）" :image-size="60" />
            </el-tab-pane>

            <el-tab-pane label="概览" name="overview">
              <el-descriptions :column="1" border size="small">
                <el-descriptions-item label="Project">{{ drawerApp.project || 'default' }}</el-descriptions-item>
                <el-descriptions-item label="同步状态">{{ drawerApp.sync }}（revision {{ shortRev(detail.syncRevision) }}）</el-descriptions-item>
                <el-descriptions-item label="健康状态">{{ drawerApp.health }}</el-descriptions-item>
                <el-descriptions-item label="配置源">{{ drawerApp.repoURL }} @ {{ drawerApp.target }} / {{ drawerApp.path }}</el-descriptions-item>
                <el-descriptions-item label="目标">{{ detail.destServer }} / {{ drawerApp.destNamespace }}</el-descriptions-item>
                <el-descriptions-item label="同步策略">
                  <template v-if="drawerApp.autoSync">
                    自动同步{{ detail.prune ? ' · PRUNE' : '' }}{{ detail.selfHeal ? ' · SELFHEAL' : '' }}
                    <el-tag v-if="drawerApp.paused" size="small" type="warning">已暂停（PAUSED）</el-tag>
                  </template>
                  <span v-else>手动</span>
                </el-descriptions-item>
                <el-descriptions-item label="最近操作" v-if="detail.operation && detail.operation.phase">
                  {{ detail.operation.phase }} · {{ detail.operation.startedAt }}
                  <div class="op-msg">{{ detail.operation.message }}</div>
                </el-descriptions-item>
              </el-descriptions>
              <template v-if="detail.conditions.length">
                <h4 class="sec">Conditions</h4>
                <div v-for="(cd, i) in detail.conditions" :key="i" class="cond">
                  <el-tag size="small" :type="cd.type === 'ErrorMessage' || cd.type.includes('Error') ? 'danger' : 'info'">{{ cd.type }}</el-tag>
                  <span class="cond-msg">{{ cd.message }}</span>
                </div>
              </template>
            </el-tab-pane>

            <el-tab-pane label="同步历史" name="history">
              <el-table border size="small" :data="detail.history">
                <el-table-column prop="revision" label="Revision" width="120">
                  <template #default="{ row }">{{ shortRev(row.revision) }}</template>
                </el-table-column>
                <el-table-column prop="deployedAt" label="部署时间" width="180" />
                <el-table-column prop="initiator" label="触发方" width="90" />
                <el-table-column prop="author" label="Git 作者" width="140" show-overflow-tooltip />
                <el-table-column prop="message" label="提交信息" min-width="200" show-overflow-tooltip />
              </el-table>
              <el-empty v-if="!detail.history.length" description="暂无同步历史" :image-size="60" />
            </el-tab-pane>
          </el-tabs>
        </template>
      </div>
    </el-drawer>

    <!-- 删除应用（级联选项，需输入名称确认） -->
    <el-dialog v-model="delVisible" title="删除 ArgoCD 应用" width="480px">
      <el-alert type="warning" :closable="false" class="mb12"
        :title="`删除应用「${delTarget?.name || ''}」（ArgoCD 记录）`" />
      <el-checkbox v-model="delCascade">同时删除该应用托管的全部集群资源（级联，等价原生 CASCADE）</el-checkbox>
      <div v-if="delCascade" class="cascade-warn">资源将被真正删除且不可恢复；如需保留资源请取消勾选（非级联）。</div>
      <div class="confirm-tip">输入应用名确认：<b>{{ delTarget?.name }}</b></div>
      <el-input v-model="delConfirm" size="small" />
      <template #footer>
        <el-button @click="delVisible = false">取消</el-button>
        <el-button type="danger" :disabled="delConfirm !== delTarget?.name" :loading="deleting" @click="doDelete">删除</el-button>
      </template>
    </el-dialog>

    <!-- 可视化编辑器（表单 + YAML 双视图，保存前变更对比；写走通用 applyYaml） -->
    <el-dialog v-model="editorVisible" :title="editorCreating ? '新建 ArgoCD 应用' : `编辑 ArgoCD 应用 - ${editorName}`" width="860px" top="4vh" destroy-on-close>
      <ObjectEditor
        v-if="editorYaml !== null"
        kind="applications"
        :yaml="editorYaml"
        :creating="editorCreating"
        :namespace="editorNs"
        @saved="onSaved"
        @cancel="editorVisible = false"
      />
    </el-dialog>
  </el-card>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { ArrowDown, Refresh, Search } from '@element-plus/icons-vue'
import { argocdApi, type ArgoCDApp, type ArgoAppDetailResp, type ArgoTreeNode } from '../api'
import { useClusterStore } from '../store/cluster'
import { useUserStore } from '../store/user'
import { usePerm } from '../store/perm'
import ObjectEditor from '../components/ObjectEditor.vue'

const router = useRouter()
const userStore = useUserStore()
const clusterStore = useClusterStore()
const perm = usePerm()
const installed = ref(true)
const loading = ref(false)
const apps = ref<ArgoCDApp[]>([])

const specErrors = computed(() => apps.value.filter((a) => !!a.specError))
const loadError = ref('')

// ---------------- 筛选 ----------------
type FilterKey = 'all' | 'Synced' | 'OutOfSync' | 'Healthy' | 'Degraded' | 'Progressing' | 'Unknown' | 'paused'
const statusFilters: { key: FilterKey; label: string }[] = [
  { key: 'all', label: '全部' },
  { key: 'Synced', label: 'Synced' },
  { key: 'OutOfSync', label: 'OutOfSync' },
  { key: 'Healthy', label: 'Healthy' },
  { key: 'Degraded', label: 'Degraded' },
  { key: 'Progressing', label: 'Progressing' },
  { key: 'Unknown', label: 'Unknown' },
  { key: 'paused', label: '已暂停' },
]
const activeFilter = ref<FilterKey>('all')
const projectFilter = ref('')
const keyword = ref('')
const projectNames = computed(() => Array.from(new Set(apps.value.map((a) => a.project || 'default'))).sort())

function matchFilter(a: ArgoCDApp, k: FilterKey) {
  if (k === 'all') return true
  if (k === 'paused') return !!a.paused
  return a.sync === k || a.health === k
}
function countOf(k: FilterKey) {
  return k === 'all' ? apps.value.length : apps.value.filter((a) => matchFilter(a, k)).length
}
const filtered = computed(() =>
  apps.value.filter((a) => {
    if (!matchFilter(a, activeFilter.value)) return false
    if (projectFilter.value && (a.project || 'default') !== projectFilter.value) return false
    if (keyword.value && !a.name.toLowerCase().includes(keyword.value.toLowerCase())) return false
    return true
  }),
)

function syncType(s: string) {
  if (s === 'Synced') return 'success'
  if (s === 'OutOfSync') return 'warning'
  if (s === 'Unknown') return 'info'
  return 'primary'
}
function healthType(s: string) {
  if (s === 'Healthy') return 'success'
  if (s === 'Degraded') return 'danger'
  if (s === 'Progressing') return 'primary'
  return 'info'
}
function healthColor(h?: string) {
  if (h === 'Healthy') return '#00aa55'
  if (h === 'Degraded') return '#f56c6c'
  if (h === 'Progressing') return '#409eff'
  if (h === 'Suspended') return '#e6a23c'
  if (h === 'Missing') return '#909399'
  return '#c0c4cc'
}
function shortRev(r?: string) {
  return r ? r.slice(0, 7) : '-'
}

async function load() {
  if (!clusterStore.current) return
  loading.value = true
  try {
    const r = await argocdApi.apps()
    installed.value = !!r.installed
    apps.value = r.items || []
    loadError.value = ''
  } catch (e: any) {
    loadError.value = e?.message || '列表加载失败'
  } finally {
    loading.value = false
  }
}

// ---------------- 动作 ----------------
function canOperate(row: ArgoCDApp) {
  return perm.canWriteNS(row.destNamespace)
}

async function doSync(row: ArgoCDApp) {
  await argocdApi.sync(row.namespace, row.name)
  ElMessage.success(`已对 ${row.name} 下发 SYNC`)
  setTimeout(load, 2500)
}
async function doPause(row: ArgoCDApp) {
  await argocdApi.setPause(row.namespace, row.name, !row.paused)
  ElMessage.success(row.paused ? '已恢复自动同步' : '已暂停自动同步')
  await load()
}
async function onMore(cmd: string, row: ArgoCDApp) {
  if (cmd === 'refresh' || cmd === 'hard') {
    await argocdApi.refresh(row.namespace, row.name, cmd === 'hard' ? 'hard' : 'normal')
    ElMessage.success(cmd === 'hard' ? '已触发硬刷新（重新拉取 Git）' : '已触发刷新比对')
    setTimeout(load, 2000)
  } else if (cmd === 'autosync') {
    await argocdApi.setAutoSync(row.namespace, row.name, !row.autoSync)
    ElMessage.success(row.autoSync ? '已关闭自动同步' : '已开启自动同步（prune + selfHeal）')
    await load()
  } else if (cmd === 'edit') {
    openEdit(row)
  } else if (cmd === 'yaml') {
    openEdit(row)
  } else if (cmd === 'delete') {
    delTarget.value = row
    delConfirm.value = ''
    delCascade.value = true
    delVisible.value = true
  }
}

// ---------------- 删除 ----------------
const delVisible = ref(false)
const deleting = ref(false)
const delTarget = ref<ArgoCDApp | null>(null)
const delCascade = ref(true)
const delConfirm = ref('')
async function doDelete() {
  if (!delTarget.value) return
  deleting.value = true
  try {
    await argocdApi.remove(delTarget.value.namespace, delTarget.value.name, delCascade.value)
    ElMessage.success(delCascade.value ? '应用与托管资源删除中（级联）' : '应用已删除（保留资源）')
    delVisible.value = false
    await load()
  } finally {
    deleting.value = false
  }
}

// ---------------- 详情抽屉 ----------------
const drawerVisible = ref(false)
const drawerLoading = ref(false)
const drawerApp = ref<ArgoCDApp | null>(null)
const detail = ref<ArgoAppDetailResp | null>(null)
const drawerTab = ref('tree')
const treeData = ref<any[]>([])
const treeFilter = ref('')
const treeRef = ref<any>(null)

function decorate(nodes: ArgoTreeNode[]): any[] {
  return nodes.map((n, i) => ({
    ...n,
    key: `${n.group || 'core'}/${n.kind}/${n.namespace || ''}/${n.name}/${i}`,
    children: n.children ? decorate(n.children) : undefined,
  }))
}
function filterTreeNode(value: string, data: any) {
  if (!value) return true
  const q = value.toLowerCase()
  return data.kind.toLowerCase().includes(q) || data.name.toLowerCase().includes(q)
}
watch(treeFilter, (v) => treeRef.value?.filter(v))
// 常见官方 kind → 控制台路由（点「打开」跳资源列表；CRD/未知 kind 不提供跳转）
const KIND_ROUTE: Record<string, string> = {
  Pod: '/workloads/pods',
  Deployment: '/workloads/deployments',
  StatefulSet: '/workloads/statefulsets',
  DaemonSet: '/workloads/daemonsets',
  Job: '/workloads/jobs',
  CronJob: '/workloads/cronjobs',
  Service: '/resources/services',
  Ingress: '/resources/ingresses',
  ConfigMap: '/resources/configmaps',
  Secret: '/resources/secrets',
  PersistentVolumeClaim: '/resources/persistentvolumeclaims',
}
function treeJump(data: ArgoTreeNode): string | null {
  return KIND_ROUTE[data.kind] || null
}
function goResource(data: ArgoTreeNode, path: string) {
  const r = router.push({ path, query: { namespace: data.namespace || '' } })
  void r
}

async function openDrawer(row: ArgoCDApp) {
  drawerApp.value = row
  detail.value = null
  treeData.value = []
  drawerTab.value = 'tree'
  drawerVisible.value = true
  drawerLoading.value = true
  try {
    const [d, t] = await Promise.all([
      argocdApi.detail(row.namespace, row.name),
      argocdApi.tree(row.namespace, row.name).catch(() => ({ tree: [] })),
    ])
    detail.value = d
    treeData.value = decorate(t.tree || [])
  } finally {
    drawerLoading.value = false
  }
}

// ---------------- 编辑器 ----------------
const editorVisible = ref(false)
const editorCreating = ref(false)
const editorName = ref('')
const editorNs = ref('argocd')
const editorYaml = ref<string | null>(null)

async function openEdit(row: ArgoCDApp) {
  editorCreating.value = false
  editorName.value = row.name
  editorNs.value = row.namespace
  editorYaml.value = null
  editorVisible.value = true
  try {
    const d = await argocdApi.appDetail(row.namespace, row.name)
    if (editorVisible.value) editorYaml.value = d.yaml
  } catch {
    editorVisible.value = false
  }
}
function openCreate() {
  editorCreating.value = true
  editorName.value = ''
  editorNs.value = 'argocd'
  editorYaml.value = ''
  editorVisible.value = true
}
function onSaved() {
  editorVisible.value = false
  ElMessage.success('Application 已保存，ArgoCD 将自动应用')
  load()
}

onMounted(load)
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.spec-hint { color: #909399; }
.spec-bad { color: #f56c6c; }
.filter-bar { display: flex; gap: 6px; align-items: center; margin-bottom: 10px; flex-wrap: wrap; }
.filter-right { margin-left: auto; display: flex; gap: 8px; }
.chip-count { margin-left: 4px; color: #909399; font-size: 12px; }
.app-row { cursor: pointer; }
.app-icon { margin-right: 6px; font-size: 14px; vertical-align: -2px; }
.app-name { font-weight: 600; }
.mini-tag { margin-left: 6px; }
.drawer-head { display: flex; align-items: center; gap: 8px; }
.drawer-title { font-size: 16px; font-weight: 700; }
.op-alert { margin-bottom: 10px; }
.tree-filter { margin-bottom: 8px; max-width: 320px; }
.tree-node { display: flex; align-items: center; gap: 6px; }
.h-dot { width: 8px; height: 8px; border-radius: 50%; display: inline-block; flex: 0 0 8px; }
.tree-kind { color: #909399; font-size: 12px; }
.tree-name { font-weight: 600; }
.tree-jump { margin-left: 8px; }
.sec { margin: 14px 0 6px; font-size: 13px; }
.cond { display: flex; gap: 8px; margin-bottom: 6px; align-items: flex-start; }
.cond-msg { font-size: 12px; color: #606266; word-break: break-all; }
.op-msg { font-size: 12px; color: #909399; word-break: break-all; }
.mb12 { margin-bottom: 12px; }
.cascade-warn { color: #f56c6c; font-size: 12px; margin-top: 6px; }
.confirm-tip { margin: 12px 0 4px; font-size: 13px; color: #606266; }
</style>
