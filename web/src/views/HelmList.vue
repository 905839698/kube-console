<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>Helm Releases ({{ items.length }})</span>
        <div class="header-right">
          <el-input v-model="search" placeholder="搜索应用 / chart..." :prefix-icon="Search" clearable style="width: 200px" @input="load" />
          <el-button @click="load"><el-icon><Refresh /></el-icon></el-button>
          <el-button type="primary" @click="openInstall"><el-icon><Plus /></el-icon>&nbsp;安装应用</el-button>
        </div>
      </div>
    </template>

    <!-- 状态过滤 -->
    <el-tabs v-model="statusTab" class="status-tabs" @tab-change="load">
      <el-tab-pane label="全部" name="" />
      <el-tab-pane label="已部署" name="deployed" />
      <el-tab-pane label="失败" name="failed" />
      <el-tab-pane label="已卸载" name="uninstalled" />
    </el-tabs>

    <el-table border :data="filtered" v-loading="loading" stripe>
      <el-table-column label="名称" prop="name" min-width="200">
        <template #default="{ row }">
          <el-link type="primary" @click="goDetail(row)">{{ row.name }}</el-link>
          <div class="desc" v-if="row.description">{{ row.description }}</div>
        </template>
      </el-table-column>
      <el-table-column prop="namespace" label="命名空间" width="150" sortable />
      <el-table-column label="Chart" min-width="160">
        <template #default="{ row }">{{ row.chart }}@{{ row.chartVersion }}</template>
      </el-table-column>
      <el-table-column prop="version" label="修订版" width="80" align="center" />
      <el-table-column prop="appVersion" label="App 版本" width="110" />
      <el-table-column label="状态" width="120" align="center">
        <template #default="{ row }">
          <el-tag :type="statusType(row.status)" size="small">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="age" label="更新时间" width="110" align="center" :sort-by="(row: any) => parseDuration(row.age)" />
      <el-table-column label="操作" width="280" fixed="right">
        <template #default="{ row }">
          <el-button size="small" type="primary" plain @click="openUpgrade(row)">升级</el-button>
          <el-button size="small" plain @click="openHistory(row)">历史</el-button>
          <el-button size="small" type="danger" plain @click="uninstall(row)">卸载</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 安装对话框 -->
    <el-dialog v-model="installVisible" title="安装 Helm 应用" width="680px" destroy-on-close>
      <el-alert type="info" :closable="false" show-icon class="mb12"
        title="离线集群提示：chart 安装后的 Pod 镜像通常需改为内网 Harbor 地址（在下方 values 中覆盖 image）" />
      <el-form label-width="100px" size="small">
        <el-form-item label="Chart 仓库" required>
          <el-select v-model="install.repoName" placeholder="选择仓库" style="width: 240px" @change="onRepoChange">
            <el-option v-for="r in repos" :key="r.id" :label="r.name" :value="r.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="Chart" required>
          <el-select v-model="install.chart" placeholder="选择 chart" filterable style="width: 240px" :loading="chartsLoading" @change="onChartChange">
            <el-option v-for="ch in charts" :key="ch.name" :label="ch.name" :value="ch.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="版本" required>
          <el-select v-model="install.version" placeholder="选择版本" filterable style="width: 240px" @change="prefillInstallValues">
            <el-option v-for="v in selectedChartVersions" :key="v" :label="v" :value="v" />
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

    <!-- 升级对话框 -->
    <el-dialog v-model="upgradeVisible" :title="`升级应用 - ${upgradeRow?.name || ''}`" width="680px" destroy-on-close>
      <el-form label-width="100px" size="small">
        <el-form-item label="Chart 仓库" required>
          <el-select v-model="upgrade.repoName" placeholder="选择仓库" style="width: 240px" @change="onUpgradeRepoChange">
            <el-option v-for="r in repos" :key="r.id" :label="r.name" :value="r.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="Chart" required>
          <el-select v-model="upgrade.chart" placeholder="选择 chart" filterable style="width: 240px" :loading="chartsLoading" @change="onUpgradeChartChange">
            <el-option v-for="ch in charts" :key="ch.name" :label="ch.name" :value="ch.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="版本" required>
          <el-select v-model="upgrade.version" placeholder="选择版本" filterable style="width: 240px" @change="prefillUpgradeValues">
            <el-option v-for="v in selectedChartVersions" :key="v" :label="v" :value="v" />
          </el-select>
        </el-form-item>
        <el-form-item label="Values">
          <div class="editor-wrap">
            <YamlEditor v-model="upgrade.values" :dark="false" />
          </div>
        </el-form-item>
        <el-form-item label="选项">
          <el-checkbox v-model="upgrade.wait">等待就绪 (wait)</el-checkbox>
          <el-input-number v-model="upgrade.timeoutSeconds" :min="30" :max="3600" :step="30" style="width: 140px; margin-left: 12px" />
          <span class="unit">秒</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="upgradeVisible = false">取消</el-button>
        <el-button type="primary" :loading="upgrading" @click="doUpgrade">升级</el-button>
      </template>
    </el-dialog>

    <!-- 历史对话框 -->
    <el-dialog v-model="historyVisible" :title="`版本历史 - ${historyRow?.name || ''}`" width="640px" destroy-on-close>
      <el-table border :data="history" v-loading="historyLoading" stripe size="small">
        <el-table-column prop="version" label="修订版" width="80" align="center" sortable />
        <el-table-column prop="status" label="状态" width="130">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)" size="small">{{ row.status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="chart" label="Chart" min-width="160" />
        <el-table-column prop="age" label="更新时间" width="110" align="center" />
        <el-table-column label="操作" width="120" fixed="right">
          <template #default="{ row }">
            <el-button size="small" type="primary" plain :disabled="row.status !== 'superseded' && row.status !== 'uninstalled'" @click="rollback(row)">回滚到此</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-dialog>
  </el-card>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Search } from '@element-plus/icons-vue'
import { helmApi, nsParam, type HelmReleaseItem, type HelmRepoItem, type RepoChartItem } from '../api'
import { useNamespaceStore } from '../store/namespace'
import { parseDuration } from '../utils/sort'
import YamlEditor from '../components/YamlEditor.vue'
import { confirmDelete } from '../utils/confirm'

const router = useRouter()
const nsStore = useNamespaceStore()

const items = ref<HelmReleaseItem[]>([])
const search = ref('')
const loading = ref(false)
const statusTab = ref('')

const repos = ref<HelmRepoItem[]>([])
const charts = ref<RepoChartItem[]>([])
const chartsLoading = ref(false)
const chartsRepoId = ref(0)

const filtered = computed(() => items.value)

onMounted(async () => {
  await loadRepos()
  await load()
})

async function load() {
  loading.value = true
  try {
    const all = await helmApi.releases(nsParam(nsStore.selected), search.value)
    if (statusTab.value) {
      items.value = all.filter((i) => i.status === statusTab.value)
    } else {
      items.value = all
    }
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

async function loadRepos() {
  try {
    repos.value = await helmApi.repos()
  } catch {
    /* 忽略 */
  }
}

function repoIdByName(name: string): number {
  const r = repos.value.find((x) => x.name === name)
  return r ? r.id : 0
}

function statusType(s: string): 'success' | 'danger' | 'warning' | 'info' {
  switch (s) {
    case 'deployed':
    case 'uninstalling':
      return 'success'
    case 'failed':
      return 'danger'
    case 'pending-install':
    case 'pending-upgrade':
    case 'pending-rollback':
      return 'warning'
    case 'uninstalled':
    case 'superseded':
      return 'info'
    default:
      return 'warning'
  }
}

function goDetail(row: HelmReleaseItem) {
  router.push({ path: `/helm/releases/${row.name}`, query: { namespace: row.namespace } })
}

// ---------------- 安装 ----------------

const installVisible = ref(false)
const installing = ref(false)
const install = ref({
  repoName: '',
  chart: '',
  version: '',
  releaseName: '',
  namespace: 'default',
  createNamespace: false,
  values: '',
  wait: false,
  timeoutSeconds: 300,
  atomic: true,
})

const selectedChart = computed(() => charts.value.find((c) => c.name === install.value.chart) || charts.value.find((c) => c.name === upgrade.value.chart))
const selectedChartVersions = computed(() => selectedChart.value?.versions || [])

function openInstall() {
  install.value = {
    repoName: '',
    chart: '',
    version: '',
    releaseName: '',
    namespace: nsParam(nsStore.selected).split(',')[0] || 'default',
    createNamespace: false,
    values: '',
    wait: false,
    timeoutSeconds: 300,
    atomic: true,
  }
  charts.value = []
  installVisible.value = true
}

async function onRepoChange(repoName: string) {
  install.value.chart = ''
  install.value.version = ''
  await loadCharts(repoIdByName(repoName))
}

async function loadCharts(repoId: number) {
  if (!repoId) return
  chartsLoading.value = true
  chartsRepoId.value = repoId
  try {
    charts.value = await helmApi.repoCharts(repoId)
  } catch {
    charts.value = []
  } finally {
    chartsLoading.value = false
  }
}

function onChartChange(chartName: string) {
  const ch = charts.value.find((c) => c.name === chartName)
  install.value.version = ch?.latestVersion || ''
  void prefillInstallValues()
}

// 安装 Values 预填：选中 chart/版本后拉取 chart 默认 values（仅在用户未输入时填充，不覆盖手改内容）
async function prefillInstallValues() {
  const v = install.value
  if (!v.repoName || !v.chart || !v.version || v.values.trim()) return
  try {
    const r = await helmApi.chartValues(v.repoName, v.chart, v.version)
    if (install.value === v && !install.value.values.trim()) install.value.values = r.values || ''
  } catch {
    /* 拉取失败留空，不阻塞安装（chart 无 values.yaml 也正常为空） */
  }
}

async function doInstall() {
  if (!install.value.repoName || !install.value.chart || !install.value.version) {
    ElMessage.warning('请选择仓库、chart 和版本')
    return
  }
  installing.value = true
  try {
    await helmApi.install({ ...install.value })
    ElMessage.success('安装已提交')
    installVisible.value = false
    load()
  } catch {
    /* 拦截器已提示 */
  } finally {
    installing.value = false
  }
}

// ---------------- 升级 ----------------

const upgradeVisible = ref(false)
const upgrading = ref(false)
const upgradeRow = ref<HelmReleaseItem>()
const upgrade = ref({
  repoName: '',
  chart: '',
  version: '',
  values: '',
  wait: false,
  timeoutSeconds: 300,
})

async function openUpgrade(row: HelmReleaseItem) {
  upgradeRow.value = row
  upgrade.value = {
    repoName: '',
    chart: row.chart,
    version: row.chartVersion,
    values: '',
    wait: false,
    timeoutSeconds: 300,
  }
  charts.value = []
  upgradeVisible.value = true
  // 预填：优先用 release 当前存储的 values（用户 values → release 内 chart 默认值），
  // 不依赖 chart 源仓库是否仍注册；取不到再走仓库路线兜底
  try {
    const rv = await helmApi.releaseUpgradeValues(row.namespace, row.name)
    if ((rv.config || '').trim()) {
      upgrade.value.values = rv.config
    } else if ((rv.chartValues || '').trim()) {
      upgrade.value.values = rv.chartValues
    }
  } catch {
    /* 回退仓库路线 */
  }
  // 尝试推断 chart 所属仓库并加载
  for (const r of repos.value) {
    try {
      const cs = await helmApi.repoCharts(r.id, row.chart)
      if (cs.length) {
        charts.value = cs
        chartsRepoId.value = r.id
        upgrade.value.repoName = r.name
        void prefillUpgradeValues()
        break
      }
    } catch {
      /* 忽略 */
    }
  }
}

async function onUpgradeRepoChange(repoName: string) {
  upgrade.value.chart = ''
  upgrade.value.version = ''
  await loadCharts(repoIdByName(repoName))
}

function onUpgradeChartChange(chartName: string) {
  const ch = charts.value.find((c) => c.name === chartName)
  upgrade.value.version = ch?.latestVersion || ''

  void prefillUpgradeValues()
}

// 升级 Values 预填：拉取所选版本 chart 的默认 values（仅未输入时填充）
async function prefillUpgradeValues() {
  const v = upgrade.value
  if (!v.repoName || !v.chart || !v.version || v.values.trim()) return
  try {
    const r = await helmApi.chartValues(v.repoName, v.chart, v.version)
    if (upgrade.value === v && !upgrade.value.values.trim()) upgrade.value.values = r.values || ''
  } catch {
    /* 同上 */
  }
}

async function doUpgrade() {
  if (!upgrade.value.repoName || !upgrade.value.chart || !upgrade.value.version) {
    ElMessage.warning('请选择仓库、chart 和版本')
    return
  }
  if (!upgradeRow.value) return
  upgrading.value = true
  try {
    await helmApi.upgrade(upgradeRow.value.namespace, upgradeRow.value.name, { ...upgrade.value })
    ElMessage.success('升级已提交')
    upgradeVisible.value = false
    load()
  } catch {
    /* 拦截器已提示 */
  } finally {
    upgrading.value = false
  }
}

// ---------------- 历史 / 回滚 ----------------

const historyVisible = ref(false)
const historyLoading = ref(false)
const historyRow = ref<HelmReleaseItem>()
const history = ref<{ version: number; status: string; updated: string; age: string; chart: string }[]>([])

async function openHistory(row: HelmReleaseItem) {
  historyRow.value = row
  history.value = []
  historyVisible.value = true
  historyLoading.value = true
  try {
    history.value = await helmApi.releaseHistory(row.namespace, row.name)
  } catch {
    /* 拦截器已提示 */
  } finally {
    historyLoading.value = false
  }
}

async function rollback(row: { version: number }) {
  if (!historyRow.value) return
  try {
    await ElMessageBox.confirm(`确定回滚到修订版 ${row.version}？`, '回滚', { type: 'warning' })
  } catch {
    return
  }
  await helmApi.rollback(historyRow.value.namespace, historyRow.value.name, row.version, false)
  ElMessage.success('回滚已提交')
  historyVisible.value = false
  load()
}

// ---------------- 卸载 ----------------

async function uninstall(row: HelmReleaseItem) {
  try {
    await confirmDelete(row.name, { title: '卸载应用', warning: '卸载不可恢复。' })
  } catch {
    return
  }
  await helmApi.uninstall(row.namespace, row.name, true)
  ElMessage.success('已卸载')
  load()
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.header-right { display: flex; gap: 8px; align-items: center; }
.desc { color: #909399; font-size: 12px; line-height: 1.4; }
.status-tabs { margin-bottom: 8px; }
.mb12 { margin-bottom: 12px; }
.ns-check { margin-left: 12px; }
.unit { margin-left: 6px; color: #909399; font-size: 12px; }
.editor-wrap { width: 100%; height: 260px; border: 1px solid #dcdfe6; border-radius: 4px; overflow: hidden; }
</style>
