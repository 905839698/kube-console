<template>
  <div v-loading="loading">
    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <div class="title">
            <el-page-header :content="`Helm 应用 / ${name}`" @back="goBack" />
            <div class="meta" v-if="info">
              <el-tag :type="statusType(info.status)" size="small">{{ info.status }}</el-tag>
              <span class="meta-item">命名空间: {{ info.namespace }}</span>
              <span class="meta-item">Chart: {{ info.chart }}@{{ info.chartVersion }}</span>
              <span class="meta-item">修订版: {{ info.version }}</span>
              <span class="meta-item" v-if="info.appVersion">App 版本: {{ info.appVersion }}</span>
            </div>
          </div>
          <div class="actions">
            <el-button size="small" @click="load">刷新</el-button>
            <el-button size="small" type="danger" plain @click="doUninstall">卸载</el-button>
          </div>
        </div>
      </template>

      <el-tabs v-model="tab">
        <el-tab-pane label="Manifest" name="manifest">
          <div class="editor-wrap tall">
            <YamlEditor :model-value="info?.manifest || '# 暂无 manifest'" :dark="false" readonly />
          </div>
        </el-tab-pane>
        <el-tab-pane label="Values" name="values">
          <div class="editor-wrap tall">
            <YamlEditor :model-value="info?.values || '# 暂无 values'" :dark="false" readonly />
          </div>
        </el-tab-pane>
        <el-tab-pane label="历史" name="history">
          <el-table :data="history" v-loading="historyLoading" stripe size="small">
            <el-table-column prop="version" label="修订版" width="80" align="center" sortable />
            <el-table-column prop="status" label="状态" width="140">
              <template #default="{ row }">
                <el-tag :type="statusType(row.status)" size="small">{{ row.status }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="chart" label="Chart" min-width="180" />
            <el-table-column prop="age" label="更新时间" width="120" align="center" />
          </el-table>
        </el-tab-pane>
        <el-tab-pane label="Notes" name="notes">
          <pre class="notes" v-if="info?.notes">{{ info.notes }}</pre>
          <div class="empty" v-else>该 chart 无安装说明</div>
        </el-tab-pane>
      </el-tabs>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { helmApi, type HelmReleaseInfo, type HelmReleaseHistory } from '../api'
import YamlEditor from '../components/YamlEditor.vue'

const route = useRoute()
const router = useRouter()

const name = ref(String(route.params.name || ''))
const ns = ref(String(route.query.namespace || 'default'))

const loading = ref(false)
const info = ref<HelmReleaseInfo>()
const tab = ref('manifest')
const history = ref<HelmReleaseHistory[]>([])
const historyLoading = ref(false)

onMounted(load)

async function load() {
  loading.value = true
  try {
    info.value = await helmApi.releaseInfo(ns.value, name.value)
    history.value = await helmApi.releaseHistory(ns.value, name.value)
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
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

function goBack() {
  router.push('/helm/releases')
}

async function doUninstall() {
  try {
    await ElMessageBox.confirm(`确定卸载应用 ${name.value}？该操作不可恢复`, '卸载', { type: 'warning' })
  } catch {
    return
  }
  await helmApi.uninstall(ns.value, name.value, true)
  ElMessage.success('已卸载')
  goBack()
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.title { display: flex; align-items: center; gap: 16px; flex-wrap: wrap; }
.meta { display: flex; gap: 14px; align-items: center; flex-wrap: wrap; }
.meta-item { color: #606266; font-size: 13px; }
.actions { display: flex; gap: 8px; }
.editor-wrap { border: 1px solid #dcdfe6; border-radius: 4px; overflow: hidden; }
.editor-wrap.tall { height: 60vh; }
.notes { background: #f5f7fa; border: 1px solid #ebeef5; border-radius: 4px; padding: 14px; white-space: pre-wrap; word-break: break-word; font-size: 13px; }
.empty { color: #909399; font-size: 13px; padding: 20px 0; }
</style>
