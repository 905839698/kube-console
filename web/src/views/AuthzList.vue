<template>
  <div>
    <el-tabs v-model="pageTab">
    <el-tab-pane label="授权列表" name="grants">
    <div class="toolbar">
      <el-button v-if="userStore.isAdmin" type="primary" @click="openWizard"><el-icon><Plus /></el-icon>&nbsp;新增授权</el-button>
      <el-button :icon="Refresh" circle @click="load" />
      <span class="hint">授权 = 把某一层级角色（平台 / 集群 / 项目）授予用户或用户组；集群/项目层同时物化为 K8s RBAC（kubectl 侧生效），旧版 kc-grant 授权兼容展示</span>
    </div>

    <el-table border :data="bindings" v-loading="loading" size="small">
      <el-table-column label="层级" width="90">
        <template #default="{ row }">
          <el-tag :type="levelTag(row.level)" size="small">{{ levelLabel(row.level) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="主体" min-width="140">
        <template #default="{ row }">
          <el-tag size="small" type="info" style="margin-right: 4px">{{ row.granteeType === 'group' ? '组' : '用户' }}</el-tag>
          {{ row.granteeName || '-' }}
        </template>
      </el-table-column>
      <el-table-column label="角色" min-width="150">
        <template #default="{ row }">
          <span>{{ row.roleDisplay }}</span>
          <el-tag v-if="!row.builtin" size="small" style="margin-left: 4px">自定义</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="范围" min-width="220">
        <template #default="{ row }">
          <span v-if="row.level === 'platform'">平台（全部集群）</span>
          <span v-else>{{ row.cluster }}
            <template v-if="row.level === 'project'">
              / <span v-if="row.namespaces.includes('*')" style="color: #e6a23c">全部命名空间</span>
              <span v-else>{{ row.namespaces.join(', ') }}</span>
            </template>
          </span>
        </template>
      </el-table-column>
      <el-table-column label="来源" width="90">
        <template #default="{ row }">
          <el-tag v-if="row.legacy" size="small" type="warning">旧版</el-tag>
          <span v-else>权限管理</span>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="90" fixed="right">
        <template #default="{ row }">
          <el-button v-if="userStore.isAdmin" size="small" text type="danger" @click="revoke(row)">回收</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 授权向导：选层级 → 选主体 → 选角色 → 选范围 -->
    <el-dialog v-model="wizardVisible" title="新增授权" width="620px">
      <el-steps :active="step" simple style="margin-bottom: 16px">
        <el-step title="授权层级" />
        <el-step title="授权主体" />
        <el-step title="角色" />
        <el-step title="范围" />
      </el-steps>

      <template v-if="step === 0">
        <el-radio-group v-model="wizard.level" class="level-group">
          <el-radio value="platform" class="level-radio">
            平台层级
            <div class="sub">控制台平台功能管理权限（用户、授权、集群注册等平台管理）</div>
          </el-radio>
          <el-radio value="cluster" class="level-radio">
            集群层级
            <div class="sub">特定集群的集群级操作：节点运维、命名空间增删、告警静默等</div>
          </el-radio>
          <el-radio value="project" class="level-radio">
            项目层级（命名空间）
            <div class="sub">命名空间内工作负载/配置/存储等资源的增删改查权限</div>
          </el-radio>
        </el-radio-group>
      </template>

      <template v-else-if="step === 1">
        <el-form label-width="90px">
          <el-form-item label="主体类型">
            <el-radio-group v-model="wizard.granteeType">
              <el-radio value="user">用户</el-radio>
              <el-radio value="group">用户组</el-radio>
            </el-radio-group>
          </el-form-item>
          <el-form-item :label="wizard.granteeType === 'group' ? '选择组' : '选择用户'">
            <el-select v-model="wizard.granteeName" filterable allow-create placeholder="选择或输入名称" style="width: 100%">
              <template v-if="wizard.granteeType === 'group'">
                <el-option v-for="g in groups" :key="g.id" :label="g.name" :value="g.name" />
              </template>
              <template v-else>
                <el-option v-for="u in users" :key="u.username" :label="u.username" :value="u.username" />
              </template>
            </el-select>
          </el-form-item>
        </el-form>
      </template>

      <template v-else-if="step === 2">
        <el-radio-group v-model="wizard.roleName" class="role-group">
          <el-radio v-for="r in levelRoles" :key="r.name" :value="r.name" class="role-radio">
            {{ r.displayName || r.name }}
            <el-tag v-if="!r.builtin" size="small" style="margin-left: 4px">自定义</el-tag>
            <div class="sub">{{ r.description }}</div>
          </el-radio>
        </el-radio-group>
        <el-empty v-if="!levelRoles.length" description="该层级没有可用角色（自定义角色请在「角色管理」创建）" :image-size="60" />
      </template>

      <template v-else>
        <el-form label-width="90px">
          <template v-if="wizard.level === 'platform'">
            <el-alert type="info" :closable="false" title="平台层授权作用于控制台平台管理功能，覆盖全部已注册集群" />
          </template>
          <template v-else-if="wizard.level === 'cluster'">
            <el-form-item label="集群">
              <el-select v-model="wizard.cluster" style="width: 100%">
                <el-option v-for="cl in clusterStore.clusters" :key="cl.name" :label="cl.name" :value="cl.name" />
              </el-select>
            </el-form-item>
          </template>
          <template v-else>
            <el-form-item label="集群">
              <el-select v-model="wizard.cluster" style="width: 100%">
                <el-option v-for="cl in clusterStore.clusters" :key="cl.name" :label="cl.name" :value="cl.name" />
              </el-select>
            </el-form-item>
            <el-form-item label="授权范围">
              <el-radio-group v-model="wizard.scopeAll">
                <el-radio :value="true">全部命名空间（物化为 ClusterRoleBinding）</el-radio>
                <el-radio :value="false">指定命名空间（物化为 RoleBinding）</el-radio>
              </el-radio-group>
            </el-form-item>
            <el-form-item v-if="!wizard.scopeAll" label="命名空间">
              <NamespaceSelect v-model="wizard.namespaces" multiple size="default" width="100%" />
            </el-form-item>
          </template>
        </el-form>
      </template>

      <template #footer>
        <el-button v-if="step > 0" @click="step--">上一步</el-button>
        <el-button v-if="step < 3" type="primary" :disabled="!canNext" @click="step++">下一步</el-button>
        <el-button v-else type="primary" :loading="granting" @click="doGrant">确认授权</el-button>
      </template>
    </el-dialog>
    </el-tab-pane>

    <el-tab-pane label="权限查询" name="query">
      <div class="toolbar">
        <el-select v-model="queryUser" filterable placeholder="选择用户" style="width: 220px">
          <el-option v-for="u in users" :key="u.username" :label="u.username" :value="u.username" />
        </el-select>
        <el-button type="primary" :disabled="!queryUser" :loading="queryLoading" @click="doQuery">查询权限</el-button>
      </div>
      <template v-if="queryResult">
        <el-alert v-if="queryResult.groups.length" type="info" :closable="false" style="margin-bottom: 10px"
          :title="`所属组：${queryResult.groups.join('、')}`" />
        <h4 class="sec-title">控制台角色（权限管理页授权）</h4>
        <el-table border :data="queryResult.consoleRoles" size="small" style="margin-bottom: 14px">
          <el-table-column label="层级" width="90">
            <template #default="{ row }"><el-tag :type="levelTag(row.level)" size="small">{{ levelLabel(row.level) }}</el-tag></template>
          </el-table-column>
          <el-table-column label="角色" min-width="160">
            <template #default="{ row }">{{ row.displayName || row.name }}</template>
          </el-table-column>
          <el-table-column label="范围" min-width="220">
            <template #default="{ row }">
              <span v-if="row.level === 'platform'">平台（全部集群）</span>
              <span v-else>{{ row.cluster }}<template v-if="row.level === 'project'"> / {{ row.namespaces === '*' ? '全部命名空间' : row.namespaces }}</template></span>
            </template>
          </el-table-column>
        </el-table>
        <el-empty v-if="!queryResult.consoleRoles.length" description="该用户没有在权限管理页获得授权" :image-size="60" />

        <h4 class="sec-title">K8s 生效权限（RBAC 绑定，kubectl 可见）</h4>
        <el-alert v-if="!queryResult.permissions.length" type="info" :closable="false" style="margin-bottom: 10px"
          title="该用户在当前集群没有任何 K8s RBAC 绑定"
          description="平台管理员的权限来自平台角色、不受绑定限制；需要在 kubectl 里读写资源，请用上面的「新增授权」授予集群/项目层角色。" />
        <el-table border :data="queryResult.permissions" size="small" stripe>
          <el-table-column label="范围" width="190">
            <template #default="{ row }">
              <el-tag v-if="row.kind === 'ClusterRoleBinding'" size="small" type="warning">集群级</el-tag>
              <span v-else>{{ row.namespace }}</span>
            </template>
          </el-table-column>
          <el-table-column prop="role" label="角色" width="180">
            <template #default="{ row }">
              <span>{{ row.role }}</span>
              <el-tag size="small" type="info" style="margin-left: 6px">{{ row.roleKind }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="name" label="绑定名" min-width="200" show-overflow-tooltip />
          <el-table-column label="动词" min-width="180">
            <template #default="{ row }">
              <el-tag v-for="v in row.verbs.slice(0, 8)" :key="v" size="small" style="margin-right: 4px">{{ v }}</el-tag>
              <span v-if="row.verbs.length > 8">+{{ row.verbs.length - 8 }}</span>
            </template>
          </el-table-column>
          <el-table-column label="资源" min-width="200">
            <template #default="{ row }">
              <span class="res-list">{{ row.resources.slice(0, 6).join(', ') || '*' }}{{ row.resources.length > 6 ? ' ...' : '' }}</span>
            </template>
          </el-table-column>
        </el-table>
      </template>
      <el-empty v-else description="选择用户后查询其控制台角色与当前集群的全部 RBAC 权限（含组继承）" />
    </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import {
  authzApi, userApi, groupApi, rbacApi,
  type PlatformUser, type UserPermItem, type RbacRole, type RbacBindingItem, type RoleBrief,
} from '../api'
import NamespaceSelect from '../components/NamespaceSelect.vue'
import { useUserStore } from '../store/user'
import { useClusterStore } from '../store/cluster'

const userStore = useUserStore()
const clusterStore = useClusterStore()
const bindings = ref<RbacBindingItem[]>([])
const pageTab = ref('grants')
const queryUser = ref('')
const queryLoading = ref(false)
const queryResult = ref<{
  username: string
  groups: string[]
  permissions: UserPermItem[]
  consoleRoles: RoleBrief[]
}>()

const LEVEL_LABELS: Record<string, string> = { platform: '平台', cluster: '集群', project: '项目' }
function levelLabel(l: string) {
  return LEVEL_LABELS[l] || l
}
function levelTag(l: string) {
  if (l === 'platform') return 'danger'
  if (l === 'cluster') return 'warning'
  return 'success'
}

async function doQuery() {
  queryLoading.value = true
  try {
    const [k8s, consoleRoles] = await Promise.all([
      authzApi.userPermissions(queryUser.value),
      rbacApi.userRoles(queryUser.value).catch(() => [] as RoleBrief[]),
    ])
    queryResult.value = { ...k8s, consoleRoles }
  } finally {
    queryLoading.value = false
  }
}

const users = ref<PlatformUser[]>([])
const groups = ref<{ id: number; name: string; description: string }[]>([])
const roles = ref<RbacRole[]>([])
const loading = ref(false)
const wizardVisible = ref(false)
const granting = ref(false)
const step = ref(0)
const wizard = reactive({
  level: 'project' as 'platform' | 'cluster' | 'project',
  granteeType: 'user' as 'user' | 'group',
  granteeName: '',
  roleName: '',
  customRoleId: 0,
  cluster: '',
  scopeAll: false,
  namespaces: [] as string[],
})

const levelRoles = computed(() => roles.value.filter((r) => r.level === wizard.level))

const canNext = computed(() => {
  if (step.value === 1) return !!wizard.granteeName.trim()
  if (step.value === 2) return !!wizard.roleName
  return true
})

async function load() {
  loading.value = true
  try {
    bindings.value = await rbacApi.bindings(clusterStore.current || '')
  } finally {
    loading.value = false
  }
}

function loadUsers() {
  return userApi
    .list()
    .then((u) => (users.value = u))
    .catch(() => (users.value = []))
}

function openWizard() {
  step.value = 0
  wizard.level = 'project'
  wizard.granteeType = 'user'
  wizard.granteeName = ''
  wizard.roleName = ''
  wizard.cluster = clusterStore.current || ''
  wizard.scopeAll = false
  wizard.namespaces = []
  void loadUsers()
  void groupApi.list().then((g) => (groups.value = g)).catch(() => (groups.value = []))
  void rbacApi.roles().then((r) => (roles.value = r)).catch(() => (roles.value = []))
  wizardVisible.value = true
}

async function doGrant() {
  const payload: Parameters<typeof rbacApi.grant>[0] = {
    level: wizard.level,
    roleName: wizard.roleName,
    granteeType: wizard.granteeType,
    granteeName: wizard.granteeName.trim(),
  }
  if (wizard.level !== 'platform') payload.cluster = wizard.cluster
  if (wizard.level === 'project') payload.namespaces = wizard.scopeAll ? ['*'] : wizard.namespaces
  if (wizard.level === 'project' && !payload.namespaces?.length) {
    ElMessage.warning('请选择命名空间')
    return
  }
  granting.value = true
  try {
    await rbacApi.grant(payload)
    ElMessage.success('授权完成')
    wizardVisible.value = false
    await load()
    await permRefresh()
  } finally {
    granting.value = false
  }
}

// 授权变了 → 当前用户自己的权限快照也刷新（给自己授权后按钮立即可用）
async function permRefresh() {
  const { usePerm } = await import('../store/perm')
  await usePerm().load(true)
}

async function revoke(row: RbacBindingItem) {
  const scope = row.level === 'platform' ? '平台' : `${row.cluster}${row.level === 'project' ? `/${row.namespaces.includes('*') ? '全部命名空间' : row.namespaces.join(',')}` : ''}`
  await ElMessageBox.confirm(
    `回收 ${row.granteeType === 'group' ? '用户组' : '用户'}「${row.granteeName}」在「${scope}」的「${row.roleDisplay}」授权？`,
    '回收授权',
    { type: 'warning' },
  )
  if (row.legacy) {
    // 旧版 kc-grant-* 绑定：走旧接口删除 K8s 对象
    const kind = row.namespaces.includes('*') ? 'ClusterRoleBinding' : 'RoleBinding'
    const ns = kind === 'RoleBinding' ? row.namespaces[0] : ''
    await authzApi.revoke(kind, row.bindingName || '', ns)
  } else {
    await rbacApi.revoke(row.id)
  }
  ElMessage.success('已回收')
  await load()
  await permRefresh()
}

// 「权限查询」标签页的用户下拉：页面挂载即加载（原来只在打开向导时顺带加载，
// 直接进标签页时下拉为空）
onMounted(() => {
  void load()
  void loadUsers()
  void rbacApi.roles().then((r) => (roles.value = r)).catch(() => (roles.value = []))
})
</script>

<style scoped>
.toolbar { display: flex; gap: 8px; margin-bottom: 12px; align-items: center; }
.hint { color: #909399; font-size: 12px; }
.sec-title { margin: 6px 0 8px; font-size: 13px; color: #303133; }
.level-group, .role-group { display: flex; flex-direction: column; gap: 10px; width: 100%; }
.level-radio, .role-radio { height: auto; align-items: flex-start; padding: 6px 0; white-space: normal; }
.level-radio :deep(.el-radio__label), .role-radio :deep(.el-radio__label) { line-height: 1.5; }
.sub { color: #909399; font-size: 12px; font-weight: 400; }
</style>
