<template>
  <div>
    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <span>集群管理</span>
          <div class="header-right">
            <el-button :icon="Refresh" circle @click="load" />
            <el-button v-if="userStore.isAdmin" type="primary" size="small" @click="openAdd">
              <el-icon><Plus /></el-icon>&nbsp;添加集群
            </el-button>
          </div>
        </div>
      </template>

      <el-table border :data="full" v-loading="loading" stripe>
        <el-table-column prop="name" label="名称" min-width="160" />
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <StatusTag :status="row.status" :text="statusText(row)" />
          </template>
        </el-table-column>
        <el-table-column prop="server" label="API Server" min-width="220" show-overflow-tooltip />
        <el-table-column prop="context" label="Context" width="150" show-overflow-tooltip />
        <el-table-column label="Prometheus" width="180">
          <template #default="{ row }">
            <span v-if="row.prometheusService">{{ row.prometheusNamespace }}/{{ row.prometheusService }}:{{ row.prometheusPort }}</span>
            <span v-else class="gray">默认 (kuboard/prometheus-k8s:9090)</span>
          </template>
        </el-table-column>
        <el-table-column label="最后连接" width="160">
          <template #default="{ row }">{{ row.lastConnectedAt ? row.lastConnectedAt.slice(0, 19).replace('T', ' ') : '-' }}</template>
        </el-table-column>
        <el-table-column label="操作" width="290" fixed="right">
          <template #default="{ row }">
            <el-button size="small" @click="testConn(row)">测试连接</el-button>
            <el-button size="small" @click="testPrometheus(row)">测试监控</el-button>
            <template v-if="userStore.isAdmin">
              <el-button size="small" @click="openEdit(row)">编辑</el-button>
              <el-button size="small" type="danger" @click="removeCluster(row)">删除</el-button>
            </template>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="dialogVisible" :title="editing ? '编辑集群' : '添加集群'" width="720px" top="8vh">
      <el-form :model="form" label-width="110px">
        <el-form-item label="集群名称" required>
          <el-input v-model="form.name" placeholder="如: production-cluster" :disabled="editing" />
        </el-form-item>
        <el-form-item label="kubeconfig" required>
          <div class="kubeconfig-input">
            <el-input v-model="form.kubeconfig" type="textarea" :rows="8" placeholder="粘贴 kubeconfig 文件内容（yaml）" class="kubeconfig-area" />
            <el-upload :show-file-list="false" accept=".yaml,.yml,.conf" :before-upload="onFileUpload" class="upload-btn">
              <el-button size="small">从文件导入</el-button>
            </el-upload>
          </div>
        </el-form-item>
        <el-divider content-position="left">Prometheus 监控（可选，默认 kuboard/prometheus-k8s:9090）</el-divider>
        <el-form-item label="命名空间">
          <el-input v-model="form.prometheusNamespace" placeholder="如 kuboard" style="width: 240px" />
        </el-form-item>
        <el-form-item label="Service">
          <el-input v-model="form.prometheusService" placeholder="如 prometheus-k8s" style="width: 240px" />
        </el-form-item>
        <el-form-item label="端口">
          <el-input-number v-model="form.prometheusPort" :min="1" :max="65535" style="width: 160px" />
        </el-form-item>
        <el-divider content-position="left">Grafana 面板（可选，iframe 直连内嵌）</el-divider>
        <el-form-item label="Grafana 地址">
          <el-input v-model="form.grafanaURL" placeholder="如 http://grafana.kuboard:3000（需开启 allow_embedding + 匿名只读）" style="width: 420px" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存并校验连接</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { UploadFile } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { clusterApi, grafanaApi, k8sApi, type Cluster } from '../api'
import { useClusterStore } from '../store/cluster'
import { useUserStore } from '../store/user'
import StatusTag from '../components/StatusTag.vue'
import { confirmDelete } from '../utils/confirm'

const clusterStore = useClusterStore()
const userStore = useUserStore()
// 完整集群信息（server/context/prometheus 等）走平台角色接口 /clusters；
// 顶栏切换器用的最小字段在 cluster store（/my-clusters）
const full = ref<Cluster[]>([])
const loading = ref(false)
const dialogVisible = ref(false)
const editing = ref(false)
const saving = ref(false)
const form = reactive({ name: '', kubeconfig: '', prometheusNamespace: '', prometheusService: '', prometheusPort: 9090, grafanaURL: '' })

onMounted(load)

async function load() {
  loading.value = true
  try {
    await clusterStore.load()
    full.value = await clusterApi.list()
  } finally {
    loading.value = false
  }
}

function statusText(c: Cluster) {
  return c.status === 'connected' ? '已连接' : c.status === 'error' ? `连接失败: ${c.errorMessage?.slice(0, 60)}` : '未测试'
}

function openAdd() {
  editing.value = false
  form.name = ''
  form.kubeconfig = ''
  form.prometheusNamespace = ''
  form.prometheusService = ''
  form.prometheusPort = 9090
  form.grafanaURL = ''
  dialogVisible.value = true
}

function openEdit(row: Cluster) {
  editing.value = true
  form.name = row.name
  form.kubeconfig = ''
  form.prometheusNamespace = row.prometheusNamespace || ''
  form.prometheusService = row.prometheusService || ''
  form.prometheusPort = row.prometheusPort || 9090
  form.grafanaURL = row.grafanaURL || ''
  dialogVisible.value = true
}

function onFileUpload(file: UploadFile) {
  const reader = new FileReader()
  reader.onload = () => {
    form.kubeconfig = String(reader.result || '')
  }
  reader.readAsText(file.raw as Blob)
  return false
}

async function save() {
  if (!form.name.trim() || (!editing.value && !form.kubeconfig.trim())) {
    ElMessage.warning('请填写集群名称和 kubeconfig')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      // 编辑时后端从不回传 kubeconfig 明文：留空 = 不改 kubeconfig。
      // 旧实现无条件带空串调 update，后端校验「kubeconfig 不能为空」直接 400，
      // 编辑功能 100% 保存失败（后续 Prometheus/Grafana 保存全部不执行）
      if (form.kubeconfig.trim()) {
        await clusterApi.update(form.name, form.kubeconfig)
      }
      // 保存 Prometheus 配置
      await clusterApi.updatePrometheus(form.name, {
        prometheusNamespace: form.prometheusNamespace,
        prometheusService: form.prometheusService,
        prometheusPort: form.prometheusPort,
      })
      await grafanaSync(form.name)
      ElMessage.success('集群已更新')
    } else {
      await clusterApi.create(form.name.trim(), form.kubeconfig)
      // 端口也参与判断：只改端口（ns/service 留空走默认）时同样要保存
      if (form.prometheusNamespace || form.prometheusService || form.prometheusPort !== 9090) {
        await clusterApi.updatePrometheus(form.name.trim(), {
          prometheusNamespace: form.prometheusNamespace,
          prometheusService: form.prometheusService,
          prometheusPort: form.prometheusPort,
        })
      }
      await grafanaSync(form.name.trim())
      ElMessage.success('集群添加成功')
    }
    dialogVisible.value = false
    await load()
  } catch {
    /* 错误提示由拦截器处理 */
  } finally {
    saving.value = false
  }
}

async function testPrometheus(row: Cluster) {
  // 旧实现测的是「当前选中集群」（http 层默认 X-Cluster），对 row 无效
  await k8sApi.monitorCheck(row.name)
  ElMessage.success(`Prometheus 连接正常（${row.name}）`)
}

// Grafana 地址变化时才调用保存接口
async function grafanaSync(name: string) {
  const current = clusterStore.clusters.find((c) => c.name === name)?.grafanaURL || ''
  if (form.grafanaURL !== current) {
    await grafanaApi.update(name, form.grafanaURL)
  }
}

async function testConn(row: Cluster) {
  await clusterApi.connectivity(row.name)
  ElMessage.success('连接正常')
  await load()
}

async function removeCluster(row: Cluster) {
  try {
    await confirmDelete(row.name, { title: '删除集群', warning: '将删除集群注册信息，该操作不可恢复。' })
  } catch {
    return
  }
  await clusterApi.remove(row.name)
  ElMessage.success('集群已删除')
  await load()
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.kubeconfig-input { width: 100%; }
.upload-btn { margin-top: 8px; }
</style>
