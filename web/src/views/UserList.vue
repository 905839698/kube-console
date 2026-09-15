<template>
  <div>
    <el-tabs v-model="pageTab">
      <el-tab-pane label="用户" name="users">
    <div class="toolbar">
      <el-button type="primary" @click="openCreate"><el-icon><Plus /></el-icon>&nbsp;新建用户</el-button>
      <el-button :icon="Refresh" circle @click="load" />
    </div>

    <el-table border :data="users" v-loading="loading" size="small">
      <el-table-column prop="id" label="ID" width="70" />
      <el-table-column prop="username" label="用户名" min-width="160">
        <template #default="{ row }">
          <span>{{ row.username }}</span>
          <el-tag v-if="row.role === 'admin'" type="danger" size="small" style="margin-left: 8px">管理员</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="所属组" width="170">
        <template #default="{ row }">
          <el-select
            :model-value="row.groupId || 0"
            size="small"
            clearable
            placeholder="未分组"
            style="width: 150px"
            @update:model-value="(v: number) => setGroup(row, v || 0)"
          >
            <el-option label="未分组" :value="0" />
            <el-option v-for="g in groups" :key="g.id" :label="g.name" :value="g.id" />
          </el-select>
        </template>
      </el-table-column>
      <el-table-column prop="createdAt" label="创建时间" width="180" />
      <el-table-column label="操作" width="260" fixed="right">
        <template #default="{ row }">
          <el-button size="small" text type="primary" @click="openReset(row)">重置密码</el-button>
          <el-button v-if="row.role !== 'admin'" size="small" text type="warning" @click="toggleRole(row)">设为管理员</el-button>
          <el-button v-else size="small" text type="info" @click="toggleRole(row)">取消管理员</el-button>
          <el-button size="small" text type="danger" :disabled="row.username === userStore.username" @click="removeUser(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 新建 / 重置密码 对话框 -->
    <el-dialog v-model="dlgVisible" :title="editing ? `重置密码：${editing.username}` : '新建用户'" width="440px">
      <el-form label-width="90px">
        <el-form-item v-if="!editing" label="用户名">
          <el-input v-model="form.username" placeholder="2-64 个字符" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input v-model="form.password" type="password" show-password placeholder="至少 6 位" />
        </el-form-item>
        <el-form-item v-if="!editing" label="角色">
          <el-radio-group v-model="form.role">
            <el-radio value="user">普通用户</el-radio>
            <el-radio value="admin">平台管理员</el-radio>
          </el-radio-group>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlgVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">确定</el-button>
      </template>
    </el-dialog>
      </el-tab-pane>

      <el-tab-pane label="用户组" name="groups">
        <div class="toolbar">
          <el-button type="primary" size="small" @click="groupDlg = true"><el-icon><Plus /></el-icon>&nbsp;新建用户组</el-button>
          <el-button :icon="Refresh" circle size="small" @click="loadGroups" />
        </div>
        <el-table border :data="groups" v-loading="groupsLoading" size="small">
          <el-table-column prop="name" label="组名" min-width="160" />
          <el-table-column prop="description" label="描述" min-width="220" />
          <el-table-column prop="users" label="成员数" width="90" align="center" />
          <el-table-column label="操作" width="140" fixed="right">
            <template #default="{ row }">
              <el-button size="small" text type="danger" @click="removeGroup(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
        <el-alert type="info" :closable="false" style="margin-top: 10px"
          title="用户组可直接用于命名空间授权（K8s RoleBinding 的 Group 主体）。把用户加入组后，在「授权管理」里按组授权即可批量生效。" />
      </el-tab-pane>
    </el-tabs>

    <!-- 新建/编辑组 -->
    <el-dialog v-model="groupDlg" title="新建用户组" width="420px">
      <el-form label-width="70px" size="small">
        <el-form-item label="组名"><el-input v-model="groupForm.name" placeholder="如 platform-team" /></el-form-item>
        <el-form-item label="描述"><el-input v-model="groupForm.description" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="groupDlg = false">取消</el-button>
        <el-button type="primary" @click="saveGroup">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { userApi, groupApi, type PlatformUser, type UserGroupItem } from '../api'
import { useUserStore } from '../store/user'
import { confirmDelete } from '../utils/confirm'

const userStore = useUserStore()
const users = ref<PlatformUser[]>([])
const pageTab = ref('users')
const groups = ref<UserGroupItem[]>([])
const groupsLoading = ref(false)
const groupDlg = ref(false)
const groupForm = reactive({ name: '', description: '' })

async function loadGroups() {
  groupsLoading.value = true
  try {
    groups.value = await groupApi.list()
  } finally {
    groupsLoading.value = false
  }
}

async function saveGroup() {
  if (!groupForm.name.trim()) {
    ElMessage.warning('组名必填')
    return
  }
  await groupApi.save(groupForm)
  ElMessage.success('已保存')
  groupDlg.value = false
  groupForm.name = ''
  groupForm.description = ''
  await loadGroups()
}

async function removeGroup(row: UserGroupItem) {
  await confirmDelete(row.name, { title: '删除用户组', warning: '组内用户将变为未分组。' })
  await groupApi.remove(row.id)
  await Promise.all([loadGroups(), load()])
}

async function setGroup(row: PlatformUser, groupId: number) {
  await groupApi.setUserGroup(row.id, groupId)
  ElMessage.success('已更新分组')
  await load()
}
const loading = ref(false)
const dlgVisible = ref(false)
const saving = ref(false)
const editing = ref<PlatformUser | null>(null)
const form = reactive({ username: '', password: '', role: 'user' })

async function load() {
  loading.value = true
  try {
    users.value = await userApi.list()
    loadGroups()
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = null
  form.username = ''
  form.password = ''
  form.role = 'user'
  dlgVisible.value = true
}

function openReset(row: PlatformUser) {
  editing.value = row
  form.password = ''
  dlgVisible.value = true
}

async function save() {
  if (form.password.length < 6) {
    ElMessage.warning('密码至少 6 位')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      await userApi.resetPassword(editing.value.id, form.password)
      ElMessage.success('密码已重置')
    } else {
      await userApi.create({ username: form.username, password: form.password, role: form.role })
      ElMessage.success('用户已创建')
    }
    dlgVisible.value = false
    await load()
  } finally {
    saving.value = false
  }
}

async function toggleRole(row: PlatformUser) {
  const role = row.role === 'admin' ? 'user' : 'admin'
  await ElMessageBox.confirm(`将用户「${row.username}」的角色改为 ${role === 'admin' ? '平台管理员' : '普通用户'}？`, '确认', { type: 'warning' })
  await userApi.updateRole(row.id, role)
  ElMessage.success('已更新')
  await load()
}

async function removeUser(row: PlatformUser) {
  await confirmDelete(row.username, { title: '删除用户' })
  await userApi.remove(row.id)
  ElMessage.success('已删除')
  await load()
}

onMounted(load)
</script>

<style scoped>
.toolbar { display: flex; gap: 8px; margin-bottom: 12px; }
</style>
