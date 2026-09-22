<template>
  <el-container class="layout">
    <!-- 白色侧边栏 -->
    <el-aside width="212px" class="aside">
      <div class="logo">
        <div class="logo-icon">
          <el-icon :size="18"><Ship /></el-icon>
        </div>
        <span>Kube Console</span>
      </div>
      <el-scrollbar class="menu-scroll">
        <el-menu :default-active="activeMenu" router class="menu">
          <el-menu-item index="/overview"><el-icon><Odometer /></el-icon><span>集群总览</span></el-menu-item>
          <el-sub-menu index="cluster-group">
            <template #title><el-icon><Connection /></el-icon><span>集群</span></template>
            <el-menu-item v-if="showPlatform" index="/clusters"><el-icon><Coordinate /></el-icon><span>集群管理</span></el-menu-item>
            <el-menu-item index="/namespaces"><el-icon><Folder /></el-icon><span>命名空间</span></el-menu-item>
            <el-menu-item index="/nodes"><el-icon><Cpu /></el-icon><span>节点</span></el-menu-item>
            <el-menu-item index="/resources"><el-icon><Files /></el-icon><span>自定义资源</span></el-menu-item>
          </el-sub-menu>
          <el-sub-menu index="workload-group">
            <template #title><el-icon><Suitcase /></el-icon><span>工作负载</span></template>
            <el-menu-item index="/workloads/matrix"><el-icon><Grid /></el-icon><span>矩阵视图</span></el-menu-item>
            <el-menu-item index="/workloads/deployments"><el-icon><Box /></el-icon><span>Deployment</span></el-menu-item>
            <el-menu-item index="/workloads/statefulsets"><el-icon><Collection /></el-icon><span>StatefulSet</span></el-menu-item>
            <el-menu-item index="/workloads/daemonsets"><el-icon><Operation /></el-icon><span>DaemonSet</span></el-menu-item>
            <el-menu-item index="/workloads/cronjobs"><el-icon><Timer /></el-icon><span>CronJob</span></el-menu-item>
            <el-menu-item index="/workloads/jobs"><el-icon><Tickets /></el-icon><span>Job</span></el-menu-item>
            <el-menu-item index="/workloads/pods"><el-icon><Cherry /></el-icon><span>Pod</span></el-menu-item>
            <el-menu-item index="/resources/horizontalpodautoscalers"><el-icon><Sort /></el-icon><span>HPA</span></el-menu-item>
          </el-sub-menu>
          <el-sub-menu index="network-group">
            <template #title><el-icon><Share /></el-icon><span>服务与网络</span></template>
            <el-sub-menu index="net-route-group">
              <template #title><el-icon><Position /></el-icon><span class="sub-title">服务与路由</span></template>
              <el-menu-item index="/resources/services"><el-icon><Connection /></el-icon><span>服务</span></el-menu-item>
              <el-menu-item index="/resources/endpointslices"><el-icon><Guide /></el-icon><span>EndpointSlice</span></el-menu-item>
              <el-menu-item index="/resources/routes"><el-icon><Share /></el-icon><span>路由</span></el-menu-item>
              <el-menu-item index="/resources/ingresses"><el-icon><Link /></el-icon><span>Ingress 类</span></el-menu-item>
            </el-sub-menu>
            <el-sub-menu index="net-gateway-group">
              <template #title><el-icon><Collection /></el-icon><span class="sub-title">网关</span></template>
              <el-menu-item index="/resources/gatewayclasses"><el-icon><Files /></el-icon><span>网关类</span></el-menu-item>
              <el-menu-item index="/resources/gateways"><el-icon><OfficeBuilding /></el-icon><span>网关</span></el-menu-item>
              <el-menu-item index="/resources/httproutes"><el-icon><Link /></el-icon><span>HTTP 路由</span></el-menu-item>
              <el-menu-item index="/resources/tcproutes"><el-icon><Connection /></el-icon><span>TCP 路由</span></el-menu-item>
              <el-menu-item index="/resources/tlsroutes"><el-icon><Lock /></el-icon><span>TLS 路由</span></el-menu-item>
              <el-menu-item index="/resources/udproutes"><el-icon><Message /></el-icon><span>UDP 路由</span></el-menu-item>
              <el-menu-item index="/resources/grpcroutes"><el-icon><ChatDotRound /></el-icon><span>GRPC 路由</span></el-menu-item>
              <el-menu-item index="/resources/referencegrants"><el-icon><Unlock /></el-icon><span>跨命名空间引用</span></el-menu-item>
            </el-sub-menu>
            <el-sub-menu index="net-policy-group">
              <template #title><el-icon><Lock /></el-icon><span class="sub-title">网络策略</span></template>
              <el-menu-item index="/resources/networkpolicies"><el-icon><Filter /></el-icon><span>NetworkPolicy</span></el-menu-item>
            </el-sub-menu>
          </el-sub-menu>
          <el-sub-menu index="config-group">
            <template #title><el-icon><Setting /></el-icon><span>配置中心</span></template>
            <el-menu-item index="/resources/configmaps"><el-icon><Document /></el-icon><span>ConfigMap</span></el-menu-item>
            <el-menu-item index="/resources/secrets"><el-icon><Key /></el-icon><span>Secret</span></el-menu-item>
            <el-menu-item index="/resources/serviceaccounts"><el-icon><User /></el-icon><span>ServiceAccount</span></el-menu-item>
          </el-sub-menu>
          <el-sub-menu index="storage-group">
            <template #title><el-icon><Coin /></el-icon><span>存储</span></template>
            <el-menu-item index="/resources/persistentvolumeclaims"><el-icon><Files /></el-icon><span>PVC</span></el-menu-item>
            <el-menu-item index="/resources/persistentvolumes"><el-icon><FolderOpened /></el-icon><span>PV</span></el-menu-item>
            <el-menu-item index="/resources/storageclasses"><el-icon><Collection /></el-icon><span>StorageClass</span></el-menu-item>
          </el-sub-menu>
          <el-sub-menu index="observe-group">
            <template #title><el-icon><TrendCharts /></el-icon><span>可观测性</span></template>
            <el-menu-item index="/monitor"><el-icon><DataLine /></el-icon><span>监控</span></el-menu-item>
            <el-menu-item index="/monitor/alerts"><el-icon><Bell /></el-icon><span>告警</span></el-menu-item>
            <el-menu-item index="/monitor/grafana"><el-icon><DataBoard /></el-icon><span>Grafana 面板</span></el-menu-item>
            <el-menu-item index="/events"><el-icon><Warning /></el-icon><span>事件中心</span></el-menu-item>
            <el-menu-item index="/logsearch"><el-icon><Document /></el-icon><span>日志检索</span></el-menu-item>
          </el-sub-menu>
          <el-sub-menu index="cicd-group">
            <template #title><el-icon><Promotion /></el-icon><span>CI / CD</span></template>
            <el-menu-item index="/ci"><el-icon><SetUp /></el-icon><span>CI 流水线</span></el-menu-item>
            <el-menu-item index="/argocd"><el-icon><Compass /></el-icon><span>ArgoCD 应用</span></el-menu-item>
            <el-menu-item index="/argocd/repos"><el-icon><FolderChecked /></el-icon><span>ArgoCD 仓库</span></el-menu-item>
            <el-menu-item index="/helm/releases"><el-icon><Download /></el-icon><span>Helm Releases</span></el-menu-item>
            <el-menu-item index="/helm/repos"><el-icon><FolderOpened /></el-icon><span>Chart 仓库</span></el-menu-item>
          </el-sub-menu>
          <el-sub-menu index="micro-group">
            <template #title><el-icon><Coin /></el-icon><span>微服务</span></template>
            <el-menu-item index="/micro/services"><el-icon><Connection /></el-icon><span>服务发现</span></el-menu-item>
            <el-menu-item index="/micro/configs"><el-icon><Document /></el-icon><span>配置管理</span></el-menu-item>
          </el-sub-menu>
          <el-sub-menu index="security-group">
            <template #title><el-icon><UserFilled /></el-icon><span>安全 (RBAC)</span></template>
            <el-menu-item index="/resources/roles"><el-icon><Avatar /></el-icon><span>Role</span></el-menu-item>
            <el-menu-item index="/resources/rolebindings"><el-icon><Connection /></el-icon><span>RoleBinding</span></el-menu-item>
            <el-menu-item index="/resources/clusterroles"><el-icon><User /></el-icon><span>ClusterRole</span></el-menu-item>
            <el-menu-item index="/resources/clusterrolebindings"><el-icon><Link /></el-icon><span>ClusterRoleBinding</span></el-menu-item>
          </el-sub-menu>
          <el-sub-menu index="quota-group">
            <template #title><el-icon><ScaleToOriginal /></el-icon><span>配额</span></template>
            <el-menu-item index="/quotas"><el-icon><PieChart /></el-icon><span>ResourceQuota</span></el-menu-item>
            <el-menu-item index="/resources/limitranges"><el-icon><Filter /></el-icon><span>LimitRange</span></el-menu-item>
          </el-sub-menu>
          <el-sub-menu v-if="showPlatform" index="platform-group">
            <template #title><el-icon><Tools /></el-icon><span>平台管理</span></template>
            <el-menu-item v-if="userStore.isAdmin" index="/users"><el-icon><User /></el-icon><span>用户管理</span></el-menu-item>
            <el-menu-item index="/authz"><el-icon><Key /></el-icon><span>授权管理</span></el-menu-item>
            <el-menu-item index="/roles"><el-icon><Stamp /></el-icon><span>角色管理</span></el-menu-item>
            <el-menu-item index="/audit"><el-icon><Tickets /></el-icon><span>审计日志</span></el-menu-item>
            <el-menu-item v-if="userStore.isAdmin" index="/notify"><el-icon><Bell /></el-icon><span>通知管理</span></el-menu-item>
            <el-menu-item v-if="userStore.isAdmin" index="/nacos"><el-icon><Connection /></el-icon><span>Nacos 管理</span></el-menu-item>
            <el-menu-item v-if="userStore.isAdmin" index="/registry"><el-icon><Picture /></el-icon><span>镜像仓库</span></el-menu-item>
            <el-menu-item index="/usage"><el-icon><DataAnalysis /></el-icon><span>用量报表</span></el-menu-item>
            <el-menu-item v-if="userStore.isAdmin" index="/backups"><el-icon><CopyDocument /></el-icon><span>备份概览</span></el-menu-item>
          </el-sub-menu>
        </el-menu>
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
            <el-icon :size="40" color="#E6A23C"><Warning /></el-icon>
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
import { computed, onMounted, reactive, ref } from 'vue'
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

// 平台管理菜单：platform-admin 全量；platform-viewer 只读子集（授权/角色/审计/报表）
const showPlatform = computed(() => userStore.isAdmin || userStore.viewer)

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
}
.logo {
  height: 56px;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 16px;
  color: #ffffff;
  font-size: 16px;
  font-weight: 700;
  border-bottom: 1px solid var(--kc-sidebar-border);
}
.logo-icon {
  width: 30px;
  height: 30px;
  border-radius: 8px;
  background: var(--kc-logo-gradient);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
}
.menu-scroll { flex: 1; }
.menu {
  border-right: none;
  padding: 8px;
  --el-menu-item-height: 42px;
  /* 关键：EP 菜单默认白底会盖住深色侧边栏，置透明让 aside 底色透出 */
  --el-menu-bg-color: transparent;
  --el-menu-hover-bg-color: transparent;
  background-color: transparent;
}
.menu :deep(.el-menu-item) {
  border-radius: 6px;
  margin-bottom: 2px;
  color: var(--kc-sidebar-text);
  font-size: 13.5px;
}
.menu :deep(.el-menu-item .el-icon) {
  font-size: 15px;
}
.menu :deep(.el-menu-item:hover),
.menu :deep(.el-sub-menu__title:hover) {
  background: var(--kc-sidebar-hover);
  color: #e3ecf1;
}
.menu :deep(.el-menu-item.is-active) {
  background: var(--kc-sidebar-active-bg);
  color: var(--kc-sidebar-text-active);
  font-weight: 600;
}
.menu :deep(.el-menu-item.is-active)::before {
  content: '';
  position: absolute;
  left: 0;
  top: 20%;
  bottom: 20%;
  width: 3px;
  border-radius: 2px;
  background: var(--kc-sidebar-accent);
}
.menu :deep(.el-sub-menu__title) {
  border-radius: 6px;
  color: #c2d2da;
  font-weight: 600;
  font-size: 13.5px;
}
.menu :deep(.el-sub-menu .el-menu-item) {
  min-width: 212px;
  padding-left: 48px !important;
}
/* 三级菜单（服务与网络 → 子分组 → 项）进一步缩进 */
.menu :deep(.el-sub-menu .el-sub-menu .el-menu-item) {
  padding-left: 72px !important;
}
.sub-title { font-size: 13px; color: var(--kc-sidebar-text); font-weight: 400; }
.header {
  background: var(--kc-header-bg);
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.05);
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
  color: #4b5563;
  font-size: 14px;
}
.user-avatar {
  background: var(--kc-logo-gradient);
  color: #fff;
  font-weight: 600;
}
.main { background: var(--kc-bg); padding: 16px; overflow-y: auto; }
.main-loading {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  height: 60vh;
  color: #909399;
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
  color: #303133;
  margin-bottom: 8px;
}
.cd-desc {
  font-size: 13px;
  color: #e6a23c;
  line-height: 1.5;
  word-break: break-all;
}
.cd-tip {
  margin-top: 8px;
  font-size: 13px;
  color: #909399;
  line-height: 1.6;
}
.cd-actions {
  margin-top: 20px;
  padding-top: 16px;
  border-top: 1px solid var(--kc-border);
}
</style>
