<template>
  <div>
    <div class="toolbar">
      <el-button v-if="userStore.isAdmin" type="primary" @click="openCreate"><el-icon><Plus /></el-icon>&nbsp;新建自定义角色</el-button>
      <el-select v-model="levelFilter" placeholder="全部层级" clearable size="default" style="width: 150px">
        <el-option label="平台层级" value="platform" />
        <el-option label="集群层级" value="cluster" />
        <el-option label="项目层级" value="project" />
      </el-select>
      <el-button :icon="Refresh" circle @click="load" />
      <span class="hint">内置角色由系统提供、不可修改；自定义角色通过勾选 API 授权项实现最小权限（集群/项目层角色授权时物化为 K8s ClusterRole）</span>
    </div>

    <el-table border :data="filtered" v-loading="loading" size="small">
      <el-table-column label="层级" width="80">
        <template #default="{ row }">
          <el-tag :type="levelTag(row.level)" size="small">{{ levelLabel(row.level) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="name" label="角色名" width="170" />
      <el-table-column prop="displayName" label="显示名" width="160" />
      <el-table-column label="类型" width="80">
        <template #default="{ row }">
          <el-tag :type="row.builtin ? 'info' : 'success'" size="small">{{ row.builtin ? '内置' : '自定义' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="description" label="说明" min-width="240" show-overflow-tooltip />
      <el-table-column label="授权项" min-width="220">
        <template #default="{ row }">
          <el-popover v-if="row.rules && row.rules.length" placement="right" width="420" trigger="click">
            <template #reference><el-link type="primary">{{ row.rules.length }} 组资源授权</el-link></template>
            <el-table border :data="row.rules" size="small">
              <el-table-column label="资源" min-width="160">
                <template #default="{ s }">{{ (s.apiGroup ? s.apiGroup + '/' : '') + s.resources.join(', ') }}</template>
              </el-table-column>
              <el-table-column label="动词" min-width="160">
                <template #default="{ s }">{{ s.verbs.join(', ') }}</template>
              </el-table-column>
            </el-table>
          </el-popover>
          <span v-else-if="row.level === 'platform'" class="hint">平台管理（内置语义）</span>
          <span v-else class="hint">跟随 K8s 内置 ClusterRole</span>
        </template>
      </el-table-column>
      <el-table-column v-if="userStore.isAdmin" label="操作" width="130" fixed="right">
        <template #default="{ row }">
          <template v-if="!row.builtin">
            <el-button size="small" text type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button size="small" text type="danger" @click="remove(row)">删除</el-button>
          </template>
        </template>
      </el-table-column>
    </el-table>

    <!-- 角色编辑器：基础信息 + 授权项勾选 -->
    <el-dialog v-model="editVisible" :title="editing ? '编辑自定义角色' : '新建自定义角色'" width="780px">
      <el-form label-width="90px" size="default">
        <el-form-item label="所属层级">
          <el-radio-group v-model="form.level" :disabled="!!editing">
            <el-radio value="platform">平台</el-radio>
            <el-radio value="cluster">集群</el-radio>
            <el-radio value="project">项目</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="角色名">
          <el-input v-model="form.name" :disabled="!!editing" placeholder="小写字母/数字/-，如 pod-logs-viewer" maxlength="63" />
        </el-form-item>
        <el-form-item label="显示名"><el-input v-model="form.displayName" placeholder="如 Pod 日志查看者" /></el-form-item>
        <el-form-item label="说明"><el-input v-model="form.description" /></el-form-item>
      </el-form>

      <template v-if="form.level !== 'platform'">
        <el-divider content-position="left">API 授权项（勾选资源组 × 读/写）</el-divider>
        <el-table border :data="catalog" size="small" max-height="330">
          <el-table-column label="资源组" width="150">
            <template #default="{ row }">
              {{ row.group }}
              <div class="hint">{{ row.apiGroup || 'core' }}/{{ row.resources.join(' ') }}</div>
            </template>
          </el-table-column>
          <el-table-column label="读取（get/list/watch）" width="150" align="center">
            <template #default="{ row }">
              <el-checkbox :model-value="has(row, 'read')" @change="(v: boolean) => toggle(row, 'read', v)" />
            </template>
          </el-table-column>
          <el-table-column label="写入（create/update/patch/delete…）" width="180" align="center">
            <template #default="{ row }">
              <el-checkbox :model-value="has(row, 'write')" @change="(v: boolean) => toggle(row, 'write', v)" />
            </template>
          </el-table-column>
          <el-table-column label="全部（*）" align="center">
            <template #default="{ row }">
              <el-checkbox :model-value="has(row, 'all')" @change="(v: boolean) => toggle(row, 'all', v)" />
            </template>
          </el-table-column>
        </el-table>
        <div class="hint" style="margin-top: 8px">
          已勾选 {{ selectedCount }} 组。集群层角色勾选写入即获得集群级操作权限；项目层角色勾选写入即可写所选命名空间。
        </div>
      </template>
      <el-alert v-else type="info" :closable="false" style="margin-top: 10px"
        title="平台层自定义角色当前按「平台管理只读」生效（等价 platform-viewer）；平台管理写权限请使用内置 platform-admin" />

      <template #footer>
        <el-button @click="editVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" :disabled="form.level !== 'platform' && !selectedCount" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { rbacApi, type RbacRole, type RbacRule, type PermissionItemsCatalog } from '../api'
import { useUserStore } from '../store/user'

const userStore = useUserStore()
const roles = ref<RbacRole[]>([])
const loading = ref(false)
const levelFilter = ref('')
const catalog = ref<PermissionItemsCatalog['items']>([])
const verbGroups = ref<{ label: string; verbs: string[] }[]>([])

const filtered = computed(() => (levelFilter.value ? roles.value.filter((r) => r.level === levelFilter.value) : roles.value))

const LEVEL_LABELS: Record<string, string> = { platform: '平台', cluster: '集群', project: '项目' }
function levelLabel(l: string) {
  return LEVEL_LABELS[l] || l
}
function levelTag(l: string) {
  if (l === 'platform') return 'danger'
  if (l === 'cluster') return 'warning'
  return 'success'
}

async function load() {
  loading.value = true
  try {
    roles.value = await rbacApi.roles()
  } finally {
    loading.value = false
  }
}

// ------------------- 角色编辑器 -------------------
const editVisible = ref(false)
const saving = ref(false)
const editing = ref<RbacRole | null>(null)
const form = reactive({ name: '', level: 'project', displayName: '', description: '' })
// 勾选状态：group key -> {read,write,all}
const checks = reactive<Record<string, { read?: boolean; write?: boolean; all?: boolean }>>({})

function keyOf(item: PermissionItemsCatalog['items'][number]) {
  return `${item.apiGroup}/${item.resources.join(',')}`
}
function has(item: PermissionItemsCatalog['items'][number], kind: 'read' | 'write' | 'all') {
  return !!checks[keyOf(item)]?.[kind]
}
function toggle(item: PermissionItemsCatalog['items'][number], kind: 'read' | 'write' | 'all', v: boolean) {
  const k = keyOf(item)
  checks[k] = checks[k] || {}
  checks[k][kind] = v
  if (v && kind === 'all') {
    checks[k].read = false
    checks[k].write = false
  }
  if (v && kind !== 'all') {
    checks[k].all = false
  }
}
const selectedCount = computed(() => Object.keys(checks).filter((k) => checks[k]?.read || checks[k]?.write || checks[k]?.all).length)

function buildRules(): RbacRule[] {
  const rules: RbacRule[] = []
  for (const item of catalog.value) {
    const c = checks[keyOf(item)]
    if (!c) continue
    const verbs: string[] = []
    if (c.all) verbs.push('*')
    else {
      if (c.read) verbs.push('get', 'list', 'watch')
      if (c.write) verbs.push('create', 'update', 'patch', 'delete', 'deletecollection')
    }
    if (verbs.length) rules.push({ apiGroup: item.apiGroup || undefined, resources: item.resources, verbs })
  }
  return rules
}

// 编辑已有角色：把 rules 反勾到目录
function fillChecksFromRules(rules: RbacRule[]) {
  Object.keys(checks).forEach((k) => delete checks[k])
  for (const item of catalog.value) {
    const k = keyOf(item)
    for (const r of rules) {
      if ((r.apiGroup || '') !== item.apiGroup) continue
      if (r.resources.length !== item.resources.length || !r.resources.every((x) => item.resources.includes(x))) continue
      if (r.verbs.includes('*')) toggle(item, 'all', true)
      else {
        if (r.verbs.some((v) => ['get', 'list', 'watch'].includes(v))) toggle(item, 'read', true)
        if (r.verbs.some((v) => !['get', 'list', 'watch'].includes(v))) toggle(item, 'write', true)
      }
    }
  }
}

function openCreate() {
  editing.value = null
  form.name = ''
  form.level = 'project'
  form.displayName = ''
  form.description = ''
  Object.keys(checks).forEach((k) => delete checks[k])
  editVisible.value = true
}

function openEdit(row: RbacRole) {
  editing.value = row
  form.name = row.name
  form.level = row.level
  form.displayName = row.displayName
  form.description = row.description
  fillChecksFromRules(row.rules || [])
  editVisible.value = true
}

async function save() {
  if (!form.name.trim()) {
    ElMessage.warning('请填写角色名')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      await rbacApi.updateRole(editing.value.id, {
        name: editing.value.name, level: editing.value.level,
        displayName: form.displayName, description: form.description,
        rules: form.level === 'platform' ? [] : buildRules(),
      })
    } else {
      await rbacApi.createRole({
        name: form.name.trim(), level: form.level,
        displayName: form.displayName, description: form.description,
        rules: form.level === 'platform' ? [] : buildRules(),
      })
    }
    ElMessage.success('已保存')
    editVisible.value = false
    await load()
  } finally {
    saving.value = false
  }
}

async function remove(row: RbacRole) {
  await ElMessageBox.confirm(`删除自定义角色「${row.displayName || row.name}」？使用中的角色需先回收授权。`, '删除角色', { type: 'warning' })
  await rbacApi.deleteRole(row.id)
  ElMessage.success('已删除')
  await load()
}

onMounted(async () => {
  void load()
  try {
    const cat = await rbacApi.permissionItems()
    catalog.value = cat.items
    verbGroups.value = cat.verbGroups
  } catch {
    /* 目录加载失败不影响内置角色展示 */
  }
})
</script>

<style scoped>
.toolbar { display: flex; gap: 8px; margin-bottom: 12px; align-items: center; }
.hint { color: #909399; font-size: 12px; }
</style>
