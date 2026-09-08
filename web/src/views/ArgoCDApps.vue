<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>ArgoCD 应用（CD 视图）</span>
        <div>
          <el-button v-if="userStore.isAdmin && installed" type="primary" size="small" @click="openCreate">新建应用</el-button>
          <el-button :icon="Refresh" circle @click="load" />
        </div>
      </div>
    </template>

    <el-alert v-if="!installed" type="info" :closable="false"
      title="当前集群未部署 ArgoCD（未发现 applications.argoproj.io CRD）。" />

    <el-table v-else :data="apps" v-loading="loading" size="small" stripe>
      <el-table-column prop="name" label="应用" min-width="160">
        <template #default="{ row }">
          <span>{{ row.name }}</span>
          <el-tag v-if="row.autoSync" size="small" type="success" style="margin-left: 6px">自动同步</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="同步状态" width="120" align="center">
        <template #default="{ row }">
          <el-tag size="small" :type="syncType(row.sync)">{{ row.sync }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="健康状态" width="120" align="center">
        <template #default="{ row }">
          <el-tag size="small" :type="healthType(row.health)">{{ row.health }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="repoURL" label="配置仓库" min-width="220" show-overflow-tooltip />
      <el-table-column prop="path" label="路径" min-width="140" show-overflow-tooltip />
      <el-table-column prop="target" label="Revision" width="110" />
      <el-table-column prop="destNamespace" label="目标命名空间" width="130" />
      <el-table-column prop="age" label="创建于" width="90" align="center" />
      <el-table-column label="操作" width="170" fixed="right">
        <template #default="{ row }">
          <el-button size="small" text type="primary" :disabled="!userStore.isAdmin" @click="openEdit(row)">编辑</el-button>
          <el-button size="small" text type="primary" :disabled="!userStore.isAdmin" @click="refresh(row)">刷新比对</el-button>
        </template>
      </el-table-column>
    </el-table>
    <el-alert v-if="installed" type="info" :closable="false" style="margin-top: 10px" title="">
      「编辑」打开可视化表单 + YAML 双视图（保存前显示变更对比）；「刷新比对」打 refresh 注解触发立即比对 Git（自动同步策略下随即部署）。私有仓库需先在
      <router-link to="/argocd/repos">ArgoCD 仓库</router-link>
      页面注册（配置账号密码），repoURL 与仓库 URL 一致才会使用其凭据。
    </el-alert>

    <!-- 可视化编辑器（表单 + YAML 双视图，保存前变更对比；写走通用 applyYaml） -->
    <el-dialog v-model="editorVisible" :title="editorCreating ? '新建 ArgoCD 应用' : `编辑 ArgoCD 应用 - ${editorName}`" width="860px" top="4vh" destroy-on-close>
      <ObjectEditor
        v-if="editorYaml !== null"
        kind="applications"
        :yaml="editorYaml"
        :creating="editorCreating"
        :namespace="editorNs"
        @saved="onSaved"
        @cancel="editorVisible = false"
      />
    </el-dialog>
  </el-card>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { argocdApi, type ArgoCDApp } from '../api'
import { useClusterStore } from '../store/cluster'
import { useUserStore } from '../store/user'
import ObjectEditor from '../components/ObjectEditor.vue'

const userStore = useUserStore()
const clusterStore = useClusterStore()
const installed = ref(true)
const loading = ref(false)
const apps = ref<ArgoCDApp[]>([])

// 编辑器
const editorVisible = ref(false)
const editorCreating = ref(false)
const editorName = ref('')
const editorNs = ref('argocd')
const editorYaml = ref<string | null>(null)

function syncType(s: string) {
  if (s === 'Synced') return 'success'
  if (s === 'OutOfSync') return 'warning'
  if (s === 'Unknown') return 'info'
  return 'primary'
}
function healthType(s: string) {
  if (s === 'Healthy') return 'success'
  if (s === 'Degraded') return 'danger'
  if (s === 'Progressing') return 'primary'
  return 'info'
}

async function load() {
  if (!clusterStore.current) return
  loading.value = true
  try {
    const r = await argocdApi.apps()
    installed.value = !!r.installed
    apps.value = r.items || []
  } catch {
    installed.value = false
  } finally {
    loading.value = false
  }
}
async function refresh(row: ArgoCDApp) {
  await argocdApi.refresh(row.namespace, row.name)
  ElMessage.success(`已触发 ${row.name} 刷新比对`)
  setTimeout(load, 2000)
}

async function openEdit(row: ArgoCDApp) {
  editorCreating.value = false
  editorName.value = row.name
  editorNs.value = row.namespace
  editorYaml.value = null
  editorVisible.value = true
  try {
    const d = await argocdApi.appDetail(row.namespace, row.name)
    if (editorVisible.value) editorYaml.value = d.yaml
  } catch {
    editorVisible.value = false
  }
}
function openCreate() {
  editorCreating.value = true
  editorName.value = ''
  editorNs.value = apps.value[0]?.namespace || 'argocd'
  editorYaml.value = ''
  editorVisible.value = true
}
function onSaved() {
  editorVisible.value = false
  ElMessage.success('Application 已保存，ArgoCD 将自动应用')
  load()
}

onMounted(load)
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
</style>
