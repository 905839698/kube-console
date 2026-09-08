<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>Chart 仓库 ({{ repos.length }})</span>
        <div class="header-right">
          <el-button @click="load"><el-icon><Refresh /></el-icon></el-button>
          <el-button type="primary" @click="openAdd"><el-icon><Plus /></el-icon>&nbsp;添加仓库</el-button>
        </div>
      </div>
    </template>

    <el-table :data="repos" v-loading="loading" stripe>
      <el-table-column prop="name" label="名称" min-width="160" sortable />
      <el-table-column prop="url" label="URL" min-width="260">
        <template #default="{ row }">
          <span class="url">{{ row.url }}</span>
        </template>
      </el-table-column>
      <el-table-column label="认证" width="100" align="center">
        <template #default="{ row }">
          <el-tag v-if="row.hasAuth" size="small" type="warning">{{ row.username || '已配置' }}</el-tag>
          <span v-else class="none">无</span>
        </template>
      </el-table-column>
      <el-table-column prop="chartCount" label="Chart 数" width="100" align="center" />
      <el-table-column label="操作" width="280" fixed="right">
        <template #default="{ row }">
          <el-button size="small" type="primary" plain @click="openBrowse(row)">浏览</el-button>
          <el-button size="small" plain :loading="refreshingId === row.id" @click="doRefresh(row)">刷新</el-button>
          <el-button size="small" plain @click="openEdit(row)">编辑</el-button>
          <el-button size="small" type="danger" @click="doRemove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 添加/编辑仓库 -->
    <el-dialog v-model="formVisible" :title="editingId ? '编辑仓库' : '添加仓库'" width="520px" destroy-on-close>
      <el-form label-width="80px" size="small">
        <el-form-item label="名称" required v-if="!editingId">
          <el-input v-model="form.name" placeholder="如 helm-stable / bitnami" />
        </el-form-item>
        <el-form-item label="URL" required>
          <el-input v-model="form.url" placeholder="https://charts.helm.sh/stable" />
        </el-form-item>
        <el-form-item label="用户名">
          <el-input v-model="form.username" placeholder="私有仓库可选" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input v-model="form.password" type="password" show-password :placeholder="editingId ? '留空则不修改' : '私有仓库可选'" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="formVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>

    <!-- 浏览 chart -->
    <el-dialog v-model="browseVisible" :title="`Chart 列表 - ${browseRepo?.name || ''}`" width="720px" destroy-on-close>
      <el-input v-model="browseSearch" placeholder="搜索 chart..." :prefix-icon="Search" clearable style="width: 240px; margin-bottom: 12px" @input="loadCharts" />
      <el-table :data="charts" v-loading="chartsLoading" stripe size="small" max-height="440">
        <el-table-column prop="name" label="Chart" min-width="180">
          <template #default="{ row }">
            <el-link type="primary" @click="openInstall(row)">{{ row.name }}</el-link>
          </template>
        </el-table-column>
        <el-table-column prop="latestVersion" label="最新版本" width="110" />
        <el-table-column prop="description" label="描述" min-width="240" show-overflow-tooltip />
      </el-table>
    </el-dialog>

    <!-- 安装（携带 repo+chart） -->
    <el-dialog v-model="installVisible" title="安装 Helm 应用" width="680px" destroy-on-close>
      <el-alert type="info" :closable="false" show-icon class="mb12"
        title="离线集群提示：chart 安装后的 Pod 镜像通常需改为内网 Harbor 地址（在下方 values 中覆盖 image）" />
      <el-form label-width="100px" size="small">
        <el-form-item label="Chart">
          <el-input :model-value="install.chartName" disabled />
        </el-form-item>
        <el-form-item label="版本" required>
          <el-select v-model="install.version" placeholder="选择版本" filterable style="width: 240px" @change="prefillValues">
            <el-option v-for="v in install.versions" :key="v" :label="v" :value="v" />
          </el-select>
        </el-form-item>
        <el-form-item label="Release 名">
          <el-input v-model="install.releaseName" placeholder="留空自动生成" style="width: 240px" />
        </el-form-item>
        <el-form-item label="命名空间" required>
          <el-input v-model="install.namespace" placeholder="如 default" style="width: 240px" />
          <el-checkbox v-model="install.createNamespace" class="ns-check">不存在则创建</el-checkbox>
        </el-form-item>
        <el-form-item label="Values">
          <div class="editor-wrap">
            <YamlEditor v-model="install.values" :dark="false" />
          </div>
        </el-form-item>
        <el-form-item label="选项">
          <el-checkbox v-model="install.wait">等待就绪 (wait)</el-checkbox>
          <el-checkbox v-model="install.atomic">失败回滚 (atomic)</el-checkbox>
          <el-input-number v-model="install.timeoutSeconds" :min="30" :max="3600" :step="30" style="width: 140px; margin-left: 12px" />
          <span class="unit">秒</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="installVisible = false">取消</el-button>
        <el-button type="primary" :loading="installing" @click="doInstall">安装</el-button>
      </template>
    </el-dialog>
  </el-card>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Search } from '@element-plus/icons-vue'
import { helmApi, nsParam, type HelmRepoItem, type RepoChartItem } from '../api'
import { useNamespaceStore } from '../store/namespace'
import YamlEditor from '../components/YamlEditor.vue'

const nsStore = useNamespaceStore()

const repos = ref<HelmRepoItem[]>([])
const loading = ref(false)
const refreshingId = ref(0)

onMounted(load)

async function load() {
  loading.value = true
  try {
    repos.value = await helmApi.repos()
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

// ---------------- 添加 / 编辑 ----------------

const formVisible = ref(false)
const saving = ref(false)
const editingId = ref(0)
const form = ref({ name: '', url: '', username: '', password: '' })

function openAdd() {
  editingId.value = 0
  form.value = { name: '', url: '', username: '', password: '' }
  formVisible.value = true
}

function openEdit(row: HelmRepoItem) {
  editingId.value = row.id
  form.value = { name: row.name, url: row.url, username: row.username, password: '' }
  formVisible.value = true
}

async function save() {
  if (!form.value.url.trim()) {
    ElMessage.warning('请填写仓库 URL')
    return
  }
  saving.value = true
  try {
    if (editingId.value) {
      await helmApi.updateRepo(editingId.value, { url: form.value.url.trim(), username: form.value.username, password: form.value.password })
      ElMessage.success('已更新')
    } else {
      if (!form.value.name.trim()) {
        ElMessage.warning('请填写仓库名称')
        return
      }
      await helmApi.addRepo({ name: form.value.name.trim(), url: form.value.url.trim(), username: form.value.username, password: form.value.password })
      ElMessage.success('已添加')
    }
    formVisible.value = false
    load()
  } catch {
    /* 拦截器已提示 */
  } finally {
    saving.value = false
  }
}

async function doRemove(row: HelmRepoItem) {
  try {
    await ElMessageBox.confirm(`确定删除仓库 ${row.name}？`, '删除', { type: 'warning' })
  } catch {
    return
  }
  await helmApi.removeRepo(row.id)
  ElMessage.success('已删除')
  load()
}

// ---------------- 刷新 ----------------

async function doRefresh(row: HelmRepoItem) {
  refreshingId.value = row.id
  try {
    const { chartCount } = await helmApi.refreshRepo(row.id)
    ElMessage.success(`已刷新，共 ${chartCount} 个 chart`)
    load()
  } catch {
    /* 拦截器已提示 */
  } finally {
    refreshingId.value = 0
  }
}

// ---------------- 浏览 chart ----------------

const browseVisible = ref(false)
const browseRepo = ref<HelmRepoItem>()
const browseSearch = ref('')
const charts = ref<RepoChartItem[]>([])
const chartsLoading = ref(false)

function openBrowse(row: HelmRepoItem) {
  browseRepo.value = row
  browseSearch.value = ''
  charts.value = []
  browseVisible.value = true
  loadCharts()
}

async function loadCharts() {
  if (!browseRepo.value) return
  chartsLoading.value = true
  try {
    charts.value = await helmApi.repoCharts(browseRepo.value.id, browseSearch.value)
  } catch {
    charts.value = []
  } finally {
    chartsLoading.value = false
  }
}

// ---------------- 安装 ----------------

const installVisible = ref(false)
const installing = ref(false)
const install = ref({
  repoId: 0,
  repoName: '',
  chartName: '',
  versions: [] as string[],
  version: '',
  releaseName: '',
  namespace: 'default',
  createNamespace: false,
  values: '',
  wait: false,
  timeoutSeconds: 300,
  atomic: true,
})

function openInstall(row: RepoChartItem) {
  if (!browseRepo.value) return
  install.value = {
    repoId: browseRepo.value.id,
    repoName: browseRepo.value.name,
    chartName: row.name,
    versions: row.versions,
    version: row.latestVersion,
    releaseName: '',
    namespace: nsParam(nsStore.selected).split(',')[0] || 'default',
    createNamespace: false,
    values: '',
    wait: false,
    timeoutSeconds: 300,
    atomic: true,
  }
  installVisible.value = true
  void prefillValues()
}

// Values 预填：拉取 chart 默认 values.yaml（仅 Values 为空时填充，不覆盖手改内容）
async function prefillValues() {
  const v = install.value
  if (!v.repoName || !v.chartName || !v.version || v.values.trim()) return
  try {
    const r = await helmApi.chartValues(v.repoName, v.chartName, v.version)
    if (install.value === v && !install.value.values.trim()) install.value.values = r.values || ''
  } catch {
    /* 拉取失败留空，不阻塞安装 */
  }
}

async function doInstall() {
  if (!install.value.version) {
    ElMessage.warning('请选择版本')
    return
  }
  installing.value = true
  try {
    await helmApi.install({
      repoName: install.value.repoName,
      chart: install.value.chartName,
      version: install.value.version,
      releaseName: install.value.releaseName,
      namespace: install.value.namespace,
      createNamespace: install.value.createNamespace,
      values: install.value.values,
      wait: install.value.wait,
      timeoutSeconds: install.value.timeoutSeconds,
      atomic: install.value.atomic,
    })
    ElMessage.success('安装已提交')
    installVisible.value = false
    browseVisible.value = false
  } catch {
    /* 拦截器已提示 */
  } finally {
    installing.value = false
  }
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.header-right { display: flex; gap: 8px; align-items: center; }
.url { font-size: 13px; color: #409eff; word-break: break-all; }
.none { color: #909399; font-size: 13px; }
.mb12 { margin-bottom: 12px; }
.ns-check { margin-left: 12px; }
.unit { margin-left: 6px; color: #909399; font-size: 12px; }
.editor-wrap { width: 100%; height: 260px; border: 1px solid #dcdfe6; border-radius: 4px; overflow: hidden; }
</style>
