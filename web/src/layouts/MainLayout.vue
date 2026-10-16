<template>
  <el-container class="layout">
    <!-- 一级分组导轨：子菜单移到内容区顶部页签 -->
    <el-aside width="208px" class="aside">
      <div class="logo">
        <div class="logo-icon">
          <el-icon :size="18"><Ship /></el-icon>
        </div>
        <span>Kube Console</span>
      </div>
      <el-scrollbar class="menu-scroll">
        <nav class="rail">
          <button
            v-for="g in navGroups"
            :key="g.key"
            class="rail-item"
            :class="{ 'is-on': activeGroupKey === g.key }"
            @click="onGroupClick(g)"
          >
            <el-icon><component :is="g.icon" /></el-icon>
            <span>{{ g.title }}</span>
          </button>
        </nav>
      </el-scrollbar>
    </el-aside>

    <el-container>
      <!-- 顶栏：面包屑 + 全局搜索 + 集群切换 + 用户 -->
      <el-header class="header">
        <div class="header-left">
          <el-breadcrumb separator="/">
            <el-breadcrumb-item>{{ route.meta.title || '' }}</el-breadcrumb-item>
          </el-breadcrumb>
        </div>
        <div class="header-center">
          <GlobalSearch />
        </div>
        <div class="header-right">
          <el-select :model-value="clusterStore.current" size="small" style="width: 180px" @change="onClusterChange" :disabled="clusterStore.clusters.length === 0">
            <template #prefix><el-icon><Connection /></el-icon></template>
            <el-option v-for="c in clusterStore.clusters" :key="c.name" :label="c.name" :value="c.name">
              <span>{{ c.name }}</span>
              <el-tag size="small" :type="c.status === 'connected' ? 'success' : c.status === 'error' ? 'danger' : 'info'" style="margin-left: 8px">
                {{ c.status === 'connected' ? '已连接' : c.status === 'error' ? '错误' : '未知' }}
              </el-tag>
            </el-option>
          </el-select>
          <!-- 全局命名空间选择：所有命名空间相关页面跟随 -->
          <!-- 集群就绪后才挂载；集群不可达时不挂载（否则会向不可达集群发 /namespaces，21s 超时并弹错误）；
               :key 绑定当前集群，切换集群时重挂载并重新拉取该集群的命名空间 -->
          <NamespaceSelect v-if="clusterStore.ready && clusterStore.currentCluster?.status !== 'error'" :key="clusterStore.current" v-model="nsModel" multiple size="small" width="240px" />
          <el-dropdown @command="onUserCommand">
            <span class="user">
              <el-avatar :size="26" class="user-avatar">{{ (userStore.username || 'A')[0].toUpperCase() }}</el-avatar>
              {{ userStore.username || 'admin' }}
              <el-icon><ArrowDown /></el-icon>
            </span>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="password">修改密码</el-dropdown-item>
                <el-dropdown-item command="token">API Token</el-dropdown-item>
                <el-dropdown-item command="theme">{{ userStore.dark ? '浅色模式' : '深色模式' }}</el-dropdown-item>
                <el-dropdown-item command="logout" divided>退出登录</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </el-header>

      <!-- 二级导航：当前分组的子项，横排在内容区顶部（与 CI 流水线页的页签同口径） -->
      <div v-if="subItems.length > 1" class="subnav">
        <!-- 用 tab-click 而非 tab-change：详情页里对应页签已是激活态，
             el-tabs 不会再发 change，点它就无法回列表 -->
        <el-tabs :model-value="activeMenu" @tab-click="onTabClick">
          <el-tab-pane v-for="it in subItems" :key="it.path" :label="it.label" :name="it.path" />
        </el-tabs>
      </div>

      <!-- 修改密码对话框 -->
      <el-dialog v-model="pwdVisible" title="修改密码" width="420px">
        <el-form label-width="90px">
          <el-form-item label="原密码"><el-input v-model="pwdForm.oldPassword" type="password" show-password /></el-form-item>
          <el-form-item label="新密码"><el-input v-model="pwdForm.newPassword" type="password" show-password placeholder="至少 6 位" /></el-form-item>
          <el-form-item label="确认新密码"><el-input v-model="pwdForm.confirm" type="password" show-password /></el-form-item>
        </el-form>
        <template #footer>
          <el-button @click="pwdVisible = false">取消</el-button>
          <el-button type="primary" :loading="pwdSaving" @click="savePassword">确定</el-button>
        </template>
      </el-dialog>

      <!-- API Token 管理 -->
      <el-dialog v-model="tokenVisible" title="API Token（脚本 / CI 调用）" width="560px">
        <div style="margin-bottom: 10px">
          <el-button type="primary" size="small" @click="createTokenDlg = true">新建 Token</el-button>
        </div>
        <el-table border :data="tokens" size="small">
          <el-table-column prop="name" label="名称" min-width="140" />
          <el-table-column label="过期时间" width="170">
            <template #default="{ row }">{{ row.expiresAt ? row.expiresAt.replace('T', ' ').slice(0, 19) : '永不过期' }}</template>
          </el-table-column>
          <el-table-column label="最近使用" width="170">
            <template #default="{ row }">{{ row.lastUsedAt ? row.lastUsedAt.replace('T', ' ').slice(0, 19) : '从未' }}</template>
          </el-table-column>
          <el-table-column label="操作" width="80" fixed="right">
            <template #default="{ row }">
              <el-button size="small" text type="danger" @click="revokeToken(row)">撤销</el-button>
            </template>
          </el-table-column>
        </el-table>
        <!-- 新建 -->
        <el-dialog v-model="createTokenDlg" title="新建 API Token" width="420px" append-to-body>
          <el-form label-width="90px" size="small">
            <el-form-item label="名称"><el-input v-model="tokenForm.name" placeholder="如 ci-deploy" /></el-form-item>
            <el-form-item label="有效期">
              <el-select v-model="tokenForm.days" style="width: 100%">
                <el-option label="永久" :value="0" />
                <el-option label="30 天" :value="30" />
                <el-option label="90 天" :value="90" />
                <el-option label="365 天" :value="365" />
              </el-select>
            </el-form-item>
          </el-form>
          <template #footer>
            <el-button @click="createTokenDlg = false">取消</el-button>
            <el-button type="primary" :loading="creatingToken" @click="doCreateToken">生成</el-button>
          </template>
        </el-dialog>
        <!-- 明文展示（仅一次） -->
        <el-dialog v-model="plainDlg" title="Token 已生成（仅显示一次）" width="520px" append-to-body>
          <el-alert type="warning" :closable="false" title="请立即复制保存，关闭后无法再次查看" style="margin-bottom: 10px" />
          <el-input :model-value="plainToken" readonly>
            <template #append>
              <el-button @click="copyPlain">复制</el-button>
            </template>
          </el-input>
        </el-dialog>
      </el-dialog>

      <el-main class="main">
        <!-- 集群就绪前不渲染内容页，避免页面在集群未确定时发起请求（缺少 X-Cluster 请求头） -->
        <div v-if="!clusterStore.ready" class="main-loading">
          <el-icon class="is-loading" :size="22"><Loading /></el-icon>
          <span>正在加载集群…</span>
        </div>
        <!-- 已就绪但尚无可用集群（集群管理页豁免：那正是添加集群的入口） -->
        <div v-else-if="!clusterStore.current && route.path !== '/clusters'" class="main-loading">
          <el-icon :size="22"><Warning /></el-icon>
          <span>暂无可用集群，请先到「集群管理」添加集群</span>
        </div>
        <!-- 当前集群不可达：明确展示原因并引导处理；不再向不可达集群发起数据请求
             （否则会等待 21s 超时并弹「连接失败」，掩盖真实原因）。集群管理页不受此限制。 -->
        <el-card v-else-if="clusterDown" shadow="never" class="cluster-down-card">
          <div class="cd-head">
            <el-icon :size="40" color="var(--el-color-warning)"><Warning /></el-icon>
            <div class="cd-body">
              <div class="cd-title">集群「{{ clusterStore.currentCluster?.name }}」当前不可达</div>
              <div class="cd-desc">{{ clusterStore.currentCluster?.errorMessage || '网络异常或集群 API Server 未启动' }}</div>
              <div class="cd-tip">可能是本机到集群的网络不通、API Server 未启动，或 kubeconfig 已变更。请检查网络后点击「重新检测连通性」，或前往「集群管理」处理。</div>
            </div>
          </div>
          <div class="cd-actions">
            <el-button type="warning" :loading="rechecking" @click="onRecheckCluster">重新检测连通性</el-button>
            <el-button @click="router.push('/clusters')">前往集群管理</el-button>
          </div>
        </el-card>
        <!-- 集群连通，正常渲染内容页：保证内容页挂载时 current 已就绪且集群可用 -->
        <router-view v-else :key="clusterStore.current" />
      </el-main>
    </el-container>
  </el-container>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useUserStore } from '../store/user'
import { useClusterStore } from '../store/cluster'
import { useNamespaceStore } from '../store/namespace'
import { usePerm } from '../store/perm'
import { authApi, tokenApi, type ApiTokenItem } from '../api'
import GlobalSearch from '../components/GlobalSearch.vue'
import NamespaceSelect from '../components/NamespaceSelect.vue'
import { confirmDelete } from '../utils/confirm'
import { KB_ENGINES, KB_GROUP_ICON } from '../utils/kb-engines'

const route = useRoute()
const router = useRouter()
const userStore = useUserStore()
const clusterStore = useClusterStore()
const nsStore = useNamespaceStore()
const perm = usePerm()

// 当前选中集群处于不可达状态（error）时，内容区展示「集群不可达」面板而非挂载数据页。
// 集群管理页（/clusters）豁免——那是排查/重测连通性的入口，不能因集群 down 而被锁住。
const clusterDown = computed(
  () =>
    route.path !== '/clusters' &&
    !!clusterStore.currentCluster &&
    clusterStore.currentCluster.status === 'error',
)
const rechecking = ref(false)
async function onRecheckCluster() {
  rechecking.value = true
  try {
    await clusterStore.recheck()
  } catch {
    /* 仍不可达：拦截器已弹出「连接失败」，保持面板 */
  } finally {
    rechecking.value = false
  }
}

// 顶栏命名空间选择器 ↔ 全局 store（集群切换时 getter 自动联动）
const nsModel = computed({
  get: () => nsStore.selected,
  set: (v) => nsStore.select(v as string[]),
})

const activeMenu = computed(() => {
  // Pod 详情归入工作负载组下的 Pod 菜单
  if (route.path.startsWith('/pods/')) return '/workloads/pods'
  if (route.path.startsWith('/workloads/')) {
    // 矩阵视图与详情页归入工作负载
    if (route.path.startsWith('/workloads/matrix')) return '/workloads/matrix'
    return `/workloads/${route.params.kind}`
  }
  if (route.path.startsWith('/nodes/')) return '/nodes'
  if (route.path.startsWith('/resources/')) return route.path
  if (route.path.startsWith('/crd/')) return '/resources'
  if (route.path === '/monitor/alerts') return '/monitor/alerts'
  if (route.path.startsWith('/monitor/')) return '/monitor'
  if (route.path.startsWith('/helm/releases/')) return '/helm/releases'
  return route.path
})

// 导航定义：左侧只留一级分组，子项渲染成内容区顶部页签。
// 菜单键必须与后端能力表一致（canMenu / canGroup），漏登记的键谁都看不见。
type NavItem = { path: string; label: string }
type NavGroup = { key: string; title: string; icon: string; items: NavItem[] }
type NavDef = NavGroup & { group?: string }

const NAV_DEFS: NavDef[] = [
  { key: 'overview', title: '集群总览', icon: 'Odometer', items: [{ path: '/overview', label: '集群总览' }] },
  { key: 'cluster-group', group: 'cluster-group', title: '集群', icon: 'Connection', items: [
    { path: '/clusters', label: '集群管理' },
    { path: '/namespaces', label: '命名空间' },
    { path: '/nodes', label: '节点' },
    { path: '/resources', label: '自定义资源' },
  ] },
  { key: 'workload-group', group: 'workload-group', title: '工作负载', icon: 'Suitcase', items: [
    { path: '/workloads/matrix', label: '矩阵视图' },
    { path: '/workloads/deployments', label: 'Deployment' },
    { path: '/workloads/statefulsets', label: 'StatefulSet' },
    { path: '/workloads/daemonsets', label: 'DaemonSet' },
    { path: '/workloads/cronjobs', label: 'CronJob' },
    { path: '/workloads/jobs', label: 'Job' },
    { path: '/workloads/pods', label: 'Pod' },
    { path: '/resources/horizontalpodautoscalers', label: 'HPA' },
  ] },
  { key: 'network-group', group: 'network-group', title: '服务与网络', icon: 'Share', items: [
    { path: '/resources/services', label: '服务' },
    { path: '/resources/endpointslices', label: 'EndpointSlice' },
    { path: '/resources/routes', label: '路由' },
    { path: '/resources/ingresses', label: 'Ingress 类' },
    { path: '/resources/gatewayclasses', label: '网关类' },
    { path: '/resources/gateways', label: '网关' },
    { path: '/resources/httproutes', label: 'HTTP 路由' },
    { path: '/resources/tcproutes', label: 'TCP 路由' },
    { path: '/resources/tlsroutes', label: 'TLS 路由' },
    { path: '/resources/udproutes', label: 'UDP 路由' },
    { path: '/resources/grpcroutes', label: 'GRPC 路由' },
    { path: '/resources/referencegrants', label: '跨命名空间引用' },
    { path: '/resources/networkpolicies', label: 'NetworkPolicy' },
  ] },
  { key: 'config-group', group: 'config-group', title: '配置中心', icon: 'Setting', items: [
    { path: '/resources/configmaps', label: 'ConfigMap' },
    { path: '/resources/secrets', label: 'Secret' },
    { path: '/resources/serviceaccounts', label: 'ServiceAccount' },
  ] },
  { key: 'storage-group', group: 'storage-group', title: '存储', icon: 'Coin', items: [
    { path: '/resources/persistentvolumeclaims', label: 'PVC' },
    { path: '/resources/persistentvolumes', label: 'PV' },
    { path: '/resources/storageclasses', label: 'StorageClass' },
  ] },
  { key: 'observe-group', group: 'observe-group', title: '可观测性', icon: 'TrendCharts', items: [
    { path: '/monitor', label: '监控' },
    { path: '/monitor/controlplane', label: '控制平面组件' },
    { path: '/monitor/alerts', label: '告警' },
    { path: '/monitor/grafana', label: 'Grafana 面板' },
    { path: '/events', label: '事件中心' },
    { path: '/logsearch', label: '日志检索' },
  ] },
  { key: 'cicd-group', group: 'cicd-group', title: 'CI / CD', icon: 'Promotion', items: [
    { path: '/ci', label: 'CI 流水线' },
    { path: '/argocd', label: 'ArgoCD 应用' },
    { path: '/argocd/repos', label: 'ArgoCD 仓库' },
    { path: '/helm/releases', label: 'Helm Releases' },
    { path: '/helm/repos', label: 'Chart 仓库' },
  ] },
  { key: 'micro-group', group: 'micro-group', title: '微服务', icon: 'Connection', items: [
    { path: '/micro/services', label: '服务发现' },
    { path: '/micro/configs', label: '配置管理' },
  ] },
  { key: 'kb-group', group: 'kb-group', title: '数据库', icon: KB_GROUP_ICON, items: [] },
  { key: 'security-group', group: 'security-group', title: '安全 (RBAC)', icon: 'UserFilled', items: [
    { path: '/resources/roles', label: 'Role' },
    { path: '/resources/rolebindings', label: 'RoleBinding' },
    { path: '/resources/clusterroles', label: 'ClusterRole' },
    { path: '/resources/clusterrolebindings', label: 'ClusterRoleBinding' },
  ] },
  { key: 'quota-group', group: 'quota-group', title: '配额', icon: 'ScaleToOriginal', items: [
    { path: '/quotas', label: 'ResourceQuota' },
    { path: '/resources/limitranges', label: 'LimitRange' },
  ] },
  { key: 'platform-group', group: 'platform-group', title: '平台管理', icon: 'Tools', items: [
    { path: '/users', label: '用户管理' },
    { path: '/authz', label: '授权管理' },
    { path: '/roles', label: '角色管理' },
    { path: '/audit', label: '审计日志' },
    { path: '/notify', label: '通知管理' },
    { path: '/nacos', label: 'Nacos 管理' },
    { path: '/registry', label: '镜像仓库' },
    { path: '/usage', label: '用量报表' },
    { path: '/backups', label: '备份概览' },
  ] },
]

const navGroups = computed<NavGroup[]>(() => {
  // 数据库分组跟着引擎能力走：读不到 KubeBlocks 就不给这些入口
  const kbItems: NavItem[] = [
    ...KB_ENGINES.map((e) => ({ path: `/kb/${e.key}`, label: e.label })),
    { path: '/kb/repos', label: '备份仓库' },
  ]
  return NAV_DEFS
    .filter((d) => !d.group || perm.canGroup(d.group))
    .map((d) => ({
      key: d.key,
      title: d.title,
      icon: d.icon,
      items: (d.key === 'kb-group' ? kbItems : d.items).filter((i) => perm.canMenu(i.path)),
    }))
    .filter((d) => d.items.length > 0)
})

// 点详情页（/nodes/xxx、/pods/<ns>/<name>）时也要把分组和页签点亮：
// 先精确匹配，再退到"路径前缀最长的那个子项"
const lastGroupKey = ref('')
const activeGroup = computed<NavGroup | undefined>(() => {
  const path = activeMenu.value
  const groups = navGroups.value
  const exact = groups.find((g) => g.items.some((i) => i.path === path))
  if (exact) return exact
  const prefixed = groups
    .flatMap((g) => g.items.filter((i) => path.startsWith(`${i.path}/`)).map((i) => ({ g, len: i.path.length })))
    .sort((a, b) => b.len - a.len)[0]
  if (prefixed) return prefixed.g
  return groups.find((g) => g.key === lastGroupKey.value) || groups[0]
})
const activeGroupKey = computed(() => activeGroup.value?.key || '')
const subItems = computed(() => activeGroup.value?.items || [])

function onGroupClick(g: NavGroup) {
  lastGroupKey.value = g.key
  router.push(g.items[0].path)
}
function onTabClick(tab: { paneName?: string | number; props?: { name?: string | number } }) {
  const path = String(tab?.paneName ?? tab?.props?.name ?? '')
  if (path.startsWith('/')) router.push(path)
}

// 平台管理菜单：platform-admin 全量；platform-viewer 只读子集（授权/角色/审计/报表）

// 直接敲 URL 也要挡：菜单藏了的页面进不去，落到用户真能看的第一个菜单上
function guardRoute() {
  const path = route.path
  // 登录页与外壳不参与；详情类路径由 menuKeyOfPath 归到它的列表页菜单键（canVisit 内部处理）
  if (path === '/' || path === '/login') return
  if (perm.canVisit(path)) return
  const to = perm.firstMenu()
  if (to === path) return
  ElMessage.warning('没有该页面的访问权限，已跳转到可用菜单')
  router.replace(to)
}
watch(() => route.path, guardRoute)

onMounted(async () => {
  // 每次进入主界面都对齐一次角色：kc-role 是本地缓存，后台降级/提升后
  // 旧缓存会让已降权用户继续看到 admin 菜单与路由入口（后端仍会拒绝，但 UI 误导）
  if (userStore.token) {
    try {
      const me = await authApi.me()
      userStore.setRole((me as any).role || 'user')
    } catch {
      /* 401 已由拦截器处理 */
    }
  }
  if (clusterStore.clusters.length === 0) {
    try {
      await clusterStore.load()
    } catch {
      /* 401 已由拦截器处理 */
    }
  }
  // 权限快照（KubeSphere 式三层角色）：写按钮与平台菜单据此渲染
  await perm.load(true)
  userStore.setViewer(!userStore.isAdmin && !!perm.access.value.platformViewer)
  guardRoute()
})

// 切换集群：以 change 事件的新值为准，经 select() 原子化更新 current 与 localStorage
// （此前 v-model 直改 current、localStorage 延后同步，二者短暂不一致会导致
//  页面已按新集群发请求但请求头仍是旧集群/为空，报「缺少 X-Cluster 请求头」）
function onClusterChange(name: string) {
  clusterStore.select(name)
  ElMessage.success(`已切换到集群: ${name}`)
  // 权限是按集群判定的（同一个人在不同集群的授权不同）：切集群必须重取
  void perm.load(true)
}

// ------------------- 修改密码 -------------------
const pwdVisible = ref(false)
const tokenVisible = ref(false)
const createTokenDlg = ref(false)
const creatingToken = ref(false)
const plainDlg = ref(false)
const plainToken = ref('')
const tokens = ref<ApiTokenItem[]>([])
const tokenForm = reactive({ name: '', days: 90 })

async function loadTokens() {
  tokens.value = await tokenApi.list()
}
async function doCreateToken() {
  if (!tokenForm.name.trim()) {
    ElMessage.warning('请填写 Token 名称')
    return
  }
  creatingToken.value = true
  try {
    const r = await tokenApi.create(tokenForm.name, tokenForm.days)
    plainToken.value = r.token
    createTokenDlg.value = false
    plainDlg.value = true
    await loadTokens()
  } finally {
    creatingToken.value = false
  }
}
async function revokeToken(row: ApiTokenItem) {
  await confirmDelete(row.name, { title: '撤销 Token', warning: '撤销后使用它的脚本将立即失效。' })
  await tokenApi.revoke(row.id)
  await loadTokens()
}
function copyPlain() {
  navigator.clipboard.writeText(plainToken.value)
  ElMessage.success('已复制')
}
const pwdSaving = ref(false)
const pwdForm = reactive({ oldPassword: '', newPassword: '', confirm: '' })

async function onUserCommand(cmd: string) {
  if (cmd === 'logout') {
    userStore.logout()
    router.push('/login')
  } else if (cmd === 'theme') {
    userStore.toggleDark()
  } else if (cmd === 'token') {
    tokenVisible.value = true
    await loadTokens().catch(() => {})
  } else if (cmd === 'password') {
    pwdForm.oldPassword = ''
    pwdForm.newPassword = ''
    pwdForm.confirm = ''
    pwdVisible.value = true
  }
}

async function savePassword() {
  if (!pwdForm.oldPassword || pwdForm.newPassword.length < 6) {
    ElMessage.warning('新密码至少 6 位')
    return
  }
  if (pwdForm.newPassword !== pwdForm.confirm) {
    ElMessage.warning('两次输入的新密码不一致')
    return
  }
  pwdSaving.value = true
  try {
    await authApi.changeMyPassword(pwdForm.oldPassword, pwdForm.newPassword)
    ElMessage.success('密码修改成功')
    pwdVisible.value = false
  } finally {
    pwdSaving.value = false
  }
}
</script>

<style scoped>
.layout { height: 100%; }
.aside {
  background: var(--kc-sidebar-bg);
  display: flex;
  flex-direction: column;
  border-right: 1px solid var(--kc-sidebar-border);
}
.logo {
  height: 56px;
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 0 16px;
  color: var(--kc-title);
  font-family: var(--kc-font-display);
  font-size: 16px;
  font-weight: 800;
  letter-spacing: -0.01em;
  border-bottom: 1px solid var(--kc-sidebar-border);
}
.logo-icon {
  width: 32px;
  height: 32px;
  border-radius: 11px;
  background: var(--kc-logo-gradient);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 6px 14px rgba(23, 184, 154, 0.32);
}
.menu-scroll { flex: 1; }
.rail {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 8px;
}
.rail-item {
  position: relative;
  display: flex;
  align-items: center;
  gap: 9px;
  height: 38px;
  padding: 0 10px;
  border: 0;
  border-radius: 11px;
  background: none;
  color: var(--kc-sidebar-text);
  font-family: var(--kc-font-body);
  font-size: 13.5px;
  font-weight: 600;
  text-align: left;
  cursor: pointer;
  transition: background 0.16s, color 0.16s, transform 0.16s;
}
.rail-item .el-icon {
  font-size: 15px;
  flex-shrink: 0;
}
.rail-item:hover {
  background: var(--kc-sidebar-hover);
  color: var(--kc-title);
  transform: translateX(2px);
}
.rail-item.is-on {
  background: var(--kc-sidebar-active-bg);
  color: var(--kc-sidebar-text-active);
  font-weight: 700;
}
.rail-item.is-on::before {
  content: '';
  position: absolute;
  left: -8px;
  top: 22%;
  bottom: 22%;
  width: 4px;
  border-radius: 0 4px 4px 0;
  background: var(--kc-sidebar-accent);
}
/* 顶部二级导航：与 CI 流水线页的 el-tabs 同一观感，只借它的页签头 */
.subnav {
  flex-shrink: 0;
  background: var(--kc-header-bg);
  border-bottom: 1px solid var(--kc-line);
  padding: 0 18px;
}
.subnav :deep(.el-tabs__header) { margin: 0; }
.subnav :deep(.el-tabs__content) { display: none; }
.subnav :deep(.el-tabs__nav-wrap::after) { display: none; }
.subnav :deep(.el-tabs__item) {
  height: 44px;
  line-height: 44px;
  font-family: var(--kc-font-display);
  font-size: 14px;
  font-weight: 700;
  letter-spacing: -0.01em;
  color: var(--kc-ink-2);
}
.subnav :deep(.el-tabs__item:hover) { color: var(--kc-primary-deep); }
.subnav :deep(.el-tabs__item.is-active) { color: var(--kc-primary-deep); }
.subnav :deep(.el-tabs__active-bar) {
  height: 3px;
  border-radius: 3px 3px 0 0;
  background-color: var(--kc-primary);
}
.header {
  background: var(--kc-header-bg);
  box-shadow: var(--kc-shadow);
  display: flex;
  align-items: center;
  height: 56px;
  padding: 0 20px;
}
.header-left { flex: 0 0 auto; }
.header-center { flex: 1; display: flex; justify-content: center; padding: 0 20px; }
.header-right { display: flex; align-items: center; gap: 16px; flex: 0 0 auto; }
.user {
  display: flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  color: var(--kc-ink-2);
  font-size: 14px;
}
.user-avatar {
  background: var(--kc-logo-gradient);
  color: #fff;
  font-weight: 600;
}
.main { background: var(--kc-bg); padding: 18px; overflow-y: auto; }
.main-loading {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  height: 60vh;
  color: var(--el-text-color-secondary);
  font-size: 14px;
}
.cluster-down-card {
  max-width: 760px;
  margin: 8vh auto 0;
}
.cd-head {
  display: flex;
  gap: 16px;
  align-items: flex-start;
}
.cd-body { flex: 1; }
.cd-title {
  font-size: 16px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  margin-bottom: 8px;
}
.cd-desc {
  font-size: 13px;
  color: var(--el-color-warning);
  line-height: 1.5;
  word-break: break-all;
}
.cd-tip {
  margin-top: 8px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
  line-height: 1.6;
}
.cd-actions {
  margin-top: 20px;
  padding-top: 16px;
  border-top: 1px solid var(--kc-border);
}
</style>
