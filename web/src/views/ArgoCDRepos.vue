<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>ArgoCD 仓库（Git / Helm，含账号密码）</span>
        <div>
          <el-button v-if="userStore.isAdmin && installed" type="primary" size="small" @click="openCreate">新建仓库</el-button>
          <el-button :icon="Refresh" circle @click="load" />
        </div>
      </div>
    </template>

    <el-alert v-if="!installed" type="info" :closable="false"
      title="当前集群未部署 ArgoCD（未发现 repositories.argoproj.io CRD）。" />

    <el-table v-else :data="repos" v-loading="loading" size="small" stripe>
      <el-table-column prop="name" label="名称" min-width="170">
        <template #default="{ row }">
          <span>{{ row.name }}</span>
          <el-tag v-if="row.credentialOnly" size="small" type="warning" style="margin-left: 6px">凭据模板</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="url" label="仓库 URL" min-width="260" show-overflow-tooltip />
      <el-table-column prop="type" label="类型" width="80" align="center">
        <template #default="{ row }">
          <el-tag size="small" :type="row.type === 'git' ? '' : 'success'">{{ row.type }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="认证" width="130" align="center">
        <template #default="{ row }">
          <el-tag v-if="row.hasSSHKey" size="small" type="warning">SSH 密钥</el-tag>
          <el-tag v-else-if="row.hasPassword" size="small" type="success">HTTP（{{ row.username || '匿名' }}）</el-tag>
          <span v-else class="muted">匿名</span>
        </template>
      </el-table-column>
      <el-table-column label="LFS" width="60" align="center">
        <template #default="{ row }">
          <el-tag v-if="row.enableLfs" size="small" type="info">开</el-tag>
          <span v-else class="muted">-</span>
        </template>
      </el-table-column>
      <el-table-column prop="namespace" label="命名空间" width="110" />
      <el-table-column label="操作" width="130" fixed="right">
        <template #default="{ row }">
          <el-button size="small" text type="primary" :disabled="!userStore.isAdmin" @click="openEdit(row)">编辑</el-button>
          <el-button size="small" text type="danger" :disabled="!userStore.isAdmin" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-alert v-if="installed" type="info" :closable="false" style="margin-top: 10px"
      title="完整仓库：Application 的 repoURL 须与仓库 URL 一致才会用此凭据；凭据模板（仅域名）：该域下所有仓库自动继承账号密码。修改密码保存后 ArgoCD 自动生效。" />

    <!-- 新建 / 编辑仓库 -->
    <el-dialog v-model="dialogVisible" :title="editing ? `编辑仓库 - ${form.name}` : '新建 ArgoCD 仓库'" width="640px" destroy-on-close>
      <el-form :model="form" label-width="110px" size="default">
        <el-row :gutter="12">
          <el-col :span="12">
            <el-form-item label="名称" required>
              <el-input v-model="form.name" :disabled="editing" placeholder="如 gitlab-cqyxpt" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="命名空间" required>
              <el-input v-model="form.namespace" :disabled="editing" placeholder="argocd" />
            </el-form-item>
          </el-col>
        </el-row>
        <el-form-item label="仓库 URL" required>
          <el-input v-model="form.url" placeholder="完整仓库 https://gitlab.example.com/group/repo.git，或仅域名 = 凭据模板" />
          <div class="form-tip">填完整仓库地址 = 注册该仓库；只填域名（如 http://gitlab.xxx.com）= 凭据模板，该域下所有仓库的 Application 自动继承此账号密码。</div>
        </el-form-item>
        <el-row :gutter="12">
          <el-col :span="12">
            <el-form-item label="类型">
              <el-radio-group v-model="form.type">
                <el-radio value="git">Git</el-radio>
                <el-radio value="helm">Helm</el-radio>
              </el-radio-group>
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="认证方式">
              <el-radio-group v-model="form.authType">
                <el-radio value="none">匿名</el-radio>
                <el-radio value="http">账号密码</el-radio>
                <el-radio value="ssh">SSH 密钥</el-radio>
              </el-radio-group>
            </el-form-item>
          </el-col>
        </el-row>
        <template v-if="form.authType === 'http'">
          <el-form-item label="用户名">
            <el-input v-model="form.username" placeholder="Git 账号 / Personal Access Token 用户名" />
          </el-form-item>
          <el-form-item :label="editing ? '密码 / Token' : '密码'" required>
            <el-input v-model="form.password" type="password" show-password
              :placeholder="editing ? '留空 = 保持原密码不变' : '密码或 Access Token'" />
          </el-form-item>
        </template>
        <el-form-item v-if="form.authType === 'ssh'" label="SSH 私钥" required>
          <el-input v-model="form.sshPrivateKey" type="textarea" :rows="4"
            placeholder="-----BEGIN OPENSSH PRIVATE KEY-----（编辑时留空 = 保持原密钥）" />
        </el-form-item>
        <el-row :gutter="12">
          <el-col :span="12">
            <el-form-item label="跳过证书校验">
              <el-switch v-model="form.insecure" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="启用 Git LFS">
              <el-switch v-model="form.enableLfs" />
            </el-form-item>
          </el-col>
        </el-row>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </el-card>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { argocdApi, type ArgoCDRepo } from '../api'
import { useClusterStore } from '../store/cluster'
import { useUserStore } from '../store/user'

const userStore = useUserStore()
const clusterStore = useClusterStore()
const installed = ref(true)
const loading = ref(false)
const repos = ref<ArgoCDRepo[]>([])

const dialogVisible = ref(false)
const editing = ref(false)
const saving = ref(false)
const form = reactive({
  namespace: 'argocd',
  name: '',
  url: '',
  type: 'git',
  authType: 'none',
  username: '',
  password: '',
  sshPrivateKey: '',
  insecure: false,
  enableLfs: false,
})

async function load() {
  if (!clusterStore.current) return
  loading.value = true
  try {
    const r = await argocdApi.repos()
    installed.value = r.installed !== false
    if (r.namespace) form.namespace = r.namespace
    repos.value = r.items || []
  } catch {
    installed.value = false
  } finally {
    loading.value = false
  }
}
function openCreate() {
  editing.value = false
  Object.assign(form, { namespace: form.namespace || 'argocd', name: '', url: '', type: 'git', authType: 'none', username: '', password: '', sshPrivateKey: '', insecure: false, enableLfs: false })
  dialogVisible.value = true
}
function openEdit(row: ArgoCDRepo) {
  editing.value = true
  Object.assign(form, {
    namespace: row.namespace,
    name: row.name,
    url: row.url,
    type: row.type || 'git',
    authType: row.hasSSHKey ? 'ssh' : row.hasPassword ? 'http' : 'none',
    username: row.username || '',
    password: '',
    sshPrivateKey: '',
    insecure: row.insecure,
    enableLfs: row.enableLfs,
  })
  dialogVisible.value = true
}
function buildPayload() {
  const payload: any = {
    namespace: form.namespace,
    name: form.name,
    url: form.url,
    type: form.type,
    insecure: form.insecure,
    enableLfs: form.enableLfs,
    username: '',
    password: '',
    sshPrivateKey: '',
  }
  if (form.authType === 'http') {
    payload.username = form.username
    payload.password = form.password
  } else if (form.authType === 'ssh') {
    payload.sshPrivateKey = form.sshPrivateKey
  }
  return payload
}
async function save() {
  if (!form.name || !form.url) {
    ElMessage.warning('请填写仓库名称和 URL')
    return
  }
  if (form.authType === 'http' && !editing && !form.password) {
    ElMessage.warning('请填写密码 / Token')
    return
  }
  if (form.authType === 'ssh' && !editing && !form.sshPrivateKey) {
    ElMessage.warning('请填写 SSH 私钥')
    return
  }
  saving.value = true
  try {
    // 统一走 PUT upsert（服务端：不存在则创建，存在则合并更新），不依赖前端建/改状态
    await argocdApi.repoUpdate(form.name, buildPayload())
    ElMessage.success(editing ? '仓库已更新，ArgoCD 自动生效' : '仓库已创建')
    dialogVisible.value = false
    load()
  } catch (e: any) {
    ElMessage.error(e?.message || '保存失败')
  } finally {
    saving.value = false
  }
}
async function remove(row: ArgoCDRepo) {
  await ElMessageBox.confirm(`删除仓库 ${row.name} 后，引用它的 ArgoCD 应用将失去凭据、同步失败。确认删除？`, '删除仓库', { type: 'warning' })
  await argocdApi.repoDelete(row.name, row.namespace)
  ElMessage.success('已删除')
  load()
}

onMounted(load)
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.muted { color: #909399; font-size: 12px; }
.form-tip { color: #909399; font-size: 12px; line-height: 1.5; margin-top: 4px; }
</style>
