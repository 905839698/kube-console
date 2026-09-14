<template>
  <div>
    <el-tabs v-model="pageTab">
    <el-tab-pane label="授权列表" name="grants">
    <div class="toolbar">
      <el-button type="primary" @click="openWizard"><el-icon><Plus /></el-icon>&nbsp;新增授权</el-button>
      <el-button :icon="Refresh" circle @click="load" />
      <span class="hint">授权 = 把某个角色的集群/命名空间权限授予指定用户（生成带 kc-grant- 前缀的 RBAC 绑定，可在此回收）</span>
    </div>

    <el-table border :data="grants" v-loading="loading" size="small">
      <el-table-column prop="grantee" label="用户" width="140" />
      <el-table-column label="角色" width="150">
        <template #default="{ row }">
          <el-tag :type="roleTagType(row.role)" size="small">{{ roleLabel(row.role) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="范围" min-width="220">
        <template #default="{ row }">
          <span v-if="row.kind === 'ClusterRoleBinding'" style="color: #e6a23c">全部命名空间（集群级）</span>
          <span v-else>{{ row.namespace }}</span>
        </template>
      </el-table-column>
      <el-table-column prop="subjects" label="绑定主体" min-width="140">
        <template #default="{ row }">{{ (row.subjects || []).join(', ') }}</template>
      </el-table-column>
      <el-table-column prop="age" label="创建于" width="90" />
      <el-table-column label="操作" width="90" fixed="right">
        <template #default="{ row }">
          <el-button size="small" text type="danger" @click="revoke(row)">回收</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 授权向导：选用户 → 选角色 → 选范围 -->
    <el-dialog v-model="wizardVisible" title="新增授权" width="560px">
      <el-steps :active="step" simple style="margin-bottom: 16px">
        <el-step title="选用户" />
        <el-step title="选角色" />
        <el-step title="选范围" />
      </el-steps>

      <template v-if="step === 0">
        <el-form label-width="90px">
          <el-form-item label="选择用户">
            <el-select v-model="wizard.username" filterable placeholder="选择平台用户" style="width: 100%">
              <el-option v-for="u in users" :key="u.username" :label="u.username" :value="u.username" />
            </el-select>
          </el-form-item>
          <el-form-item label="或输入名称">
            <el-input v-model="wizard.username" placeholder="不在列表中的用户可直接输入（如 LDAP 同步用户）" />
          </el-form-item>
        </el-form>
      </template>

      <template v-else-if="step === 1">
        <el-form label-width="90px">
          <el-form-item label="角色">
            <el-radio-group v-model="wizard.role">
              <el-radio value="view">只读 (view)</el-radio>
              <el-radio value="edit">运维 (edit)</el-radio>
              <el-radio value="admin">管理员 (admin)</el-radio>
              <el-radio value="custom">自定义</el-radio>
            </el-radio-group>
          </el-form-item>
          <el-form-item v-if="wizard.role === 'custom'" label="ClusterRole">
            <el-input v-model="wizard.customRole" placeholder="自定义 ClusterRole 名称" />
          </el-form-item>
          <div class="role-desc">
            <template v-if="wizard.role === 'view'">可查看命名空间内全部资源，不可修改</template>
            <template v-else-if="wizard.role === 'edit'">可增删改命名空间内工作负载/配置等（不含角色与配额修改）</template>
            <template v-else-if="wizard.role === 'admin'">命名空间全部权限（含角色与配额管理）</template>
            <template v-else>使用集群中已存在的自定义 ClusterRole</template>
          </div>
        </el-form>
      </template>

      <template v-else>
        <el-form label-width="90px">
          <el-form-item label="授权范围">
            <el-radio-group v-model="wizard.scopeAll">
              <el-radio :value="true">全部命名空间（集群级 ClusterRoleBinding）</el-radio>
              <el-radio :value="false">指定命名空间（RoleBinding）</el-radio>
            </el-radio-group>
          </el-form-item>
          <el-form-item v-if="!wizard.scopeAll" label="命名空间">
            <NamespaceSelect v-model="wizard.namespaces" multiple size="default" width="100%" />
          </el-form-item>
        </el-form>
      </template>

      <template #footer>
        <el-button v-if="step > 0" @click="step--">上一步</el-button>
        <el-button v-if="step < 2" type="primary" :disabled="!canNext" @click="step++">下一步</el-button>
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
      <el-empty v-else description="选择用户后查询其在当前集群的全部 RBAC 权限（含组继承）" />
    </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { authzApi, userApi, type GrantItem, type PlatformUser, type UserPermItem } from '../api'
import NamespaceSelect from '../components/NamespaceSelect.vue'

const grants = ref<GrantItem[]>([])
const pageTab = ref('grants')
const queryUser = ref('')
const queryLoading = ref(false)
const queryResult = ref<{ username: string; groups: string[]; permissions: UserPermItem[] }>()

async function doQuery() {
  queryLoading.value = true
  try {
    queryResult.value = await authzApi.userPermissions(queryUser.value)
  } finally {
    queryLoading.value = false
  }
}
const users = ref<PlatformUser[]>([])
const loading = ref(false)
const wizardVisible = ref(false)
const granting = ref(false)
const step = ref(0)
const wizard = reactive({ username: '', role: 'view', customRole: '', scopeAll: false, namespaces: [] as string[] })

const canNext = computed(() => (step.value === 0 ? !!wizard.username.trim() : true))

function roleLabel(role: string) {
  if (role === 'view') return '只读 (view)'
  if (role === 'edit') return '运维 (edit)'
  if (role === 'admin') return '管理员 (admin)'
  return role
}
function roleTagType(role: string): 'success' | 'warning' | 'danger' | 'info' {
  if (role === 'view') return 'info'
  if (role === 'edit') return 'warning'
  if (role === 'admin') return 'danger'
  return 'success'
}

async function load() {
  loading.value = true
  try {
    grants.value = await authzApi.bindings()
  } finally {
    loading.value = false
  }
}

function openWizard() {
  step.value = 0
  wizard.username = ''
  wizard.role = 'view'
  wizard.customRole = ''
  wizard.scopeAll = false
  wizard.namespaces = []
  userApi
    .list()
    .then((u) => (users.value = u))
    .catch(() => (users.value = []))
  wizardVisible.value = true
}

async function doGrant() {
  const role = wizard.role === 'custom' ? wizard.customRole.trim() : wizard.role
  if (!role) {
    ElMessage.warning('请填写自定义 ClusterRole 名称')
    return
  }
  const namespaces = wizard.scopeAll ? ['*'] : wizard.namespaces
  if (!namespaces.length) {
    ElMessage.warning('请选择命名空间')
    return
  }
  granting.value = true
  try {
    const res = await authzApi.grant({ username: wizard.username.trim(), role, namespaces })
    ElMessage.success(`授权完成（绑定 ${res.binding}）`)
    wizardVisible.value = false
    await load()
  } finally {
    granting.value = false
  }
}

async function revoke(row: GrantItem) {
  await ElMessageBox.confirm(
    `回收用户「${row.grantee}」在 ${row.kind === 'ClusterRoleBinding' ? '全部命名空间' : row.namespace} 的「${roleLabel(row.role)}」权限？`,
    '回收授权',
    { type: 'warning' },
  )
  await authzApi.revoke(row.kind, row.name, row.namespace)
  ElMessage.success('已回收')
  await load()
}

onMounted(load)
</script>

<style scoped>
.toolbar { display: flex; gap: 8px; margin-bottom: 12px; align-items: center; }
.hint { color: #909399; font-size: 12px; }
.role-desc { color: #909399; font-size: 12px; padding-left: 90px; }
</style>
