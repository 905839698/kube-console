<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>节点 ({{ items.length }})</span>
        <el-button :icon="Refresh" circle @click="load" />
      </div>
    </template>

    <el-table :data="items" v-loading="loading" stripe @row-click="onRowClick">
      <el-table-column label="名称" prop="name" min-width="170" sortable>
        <template #default="{ row }">
          <el-link type="primary" @click="goDetail(row)">{{ row.name }}</el-link>
        </template>
      </el-table-column>
      <el-table-column label="状态" prop="status" width="100" sortable>
        <template #default="{ row }"><StatusTag :status="row.status" /></template>
      </el-table-column>
      <el-table-column prop="roles" label="角色" width="110"  sortable />
      <el-table-column prop="internalIP" label="IP" width="140"  sortable />
      <el-table-column prop="version" label="kubelet 版本" width="110"  sortable />
      <el-table-column prop="cpuCores" label="CPU(核)" width="80" align="center"  sortable :sort-by="(row) => Number(row.cpuCores || 0)" />
      <el-table-column prop="memGi" label="内存(Gi)" width="90" align="center"  sortable :sort-by="(row) => Number(row.memGi || 0)" />
      <el-table-column prop="age" label="运行时长" width="80" align="center" sortable :sort-by="(row) => parseDuration(row.age)" />
      <el-table-column label="操作" width="290" fixed="right">
        <template #default="{ row }">
          <el-button size="small" @click="goDetail(row)">详情</el-button>
          <el-button size="small" type="success" plain @click="openTerminal(row)">终端</el-button>
          <el-button v-if="row.schedulable" size="small" type="warning" plain @click="cordon(row)">封锁</el-button>
          <el-button v-else size="small" type="success" plain @click="uncordon(row)">解封</el-button>
          <el-button size="small" type="danger" plain @click="openDrain(row)">排空</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 节点终端 -->
    <WebTerminal v-model:visible="terminalVisible" :namespace="'kube-system'" :pod="terminalNode || ''" :node="true" />

    <!-- 排空选项 -->
    <el-dialog v-model="drainVisible" :title="`排空节点：${drainTarget?.name || ''}`" width="480px">
      <el-alert type="warning" :closable="false" style="margin-bottom: 12px"
        title="排空会先封锁节点，再驱逐其上的全部业务 Pod（尊重 PDB），System Pod 按选项处理。" />
      <el-form label-width="180px">
        <el-form-item label="忽略 DaemonSet Pod">
          <el-switch v-model="drainOpts.ignoreDaemonsets" />
        </el-form-item>
        <el-form-item label="强制驱逐裸 Pod">
          <el-switch v-model="drainOpts.force" />
        </el-form-item>
        <el-form-item label="允许删除 emptyDir 数据">
          <el-switch v-model="drainOpts.deleteEmptydirData" />
        </el-form-item>
        <el-form-item label="优雅终止 (秒)">
          <el-input-number v-model="drainOpts.gracePeriodSeconds" :min="0" :max="600" />
        </el-form-item>
        <el-form-item label="PDB 等待超时 (秒)">
          <el-input-number v-model="drainOpts.timeoutSeconds" :min="30" :max="1800" />
        </el-form-item>
      </el-form>
      <div v-if="drainResult" class="drain-result">
        <div v-if="drainResult.evicted.length">已驱逐 {{ drainResult.evicted.length }} 个 Pod</div>
        <div v-if="drainResult.skipped.length">跳过 {{ drainResult.skipped.length }} 个</div>
        <div v-if="drainResult.failed.length" class="drain-fail">失败 {{ drainResult.failed.length }} 个（见控制台日志）</div>
      </div>
      <template #footer>
        <el-button @click="drainVisible = false">关闭</el-button>
        <el-button type="danger" :loading="draining" @click="doDrain">开始排空</el-button>
      </template>
    </el-dialog>
  </el-card>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { k8sApi, nodeApi, type NodeDetail } from '../api'
import StatusTag from '../components/StatusTag.vue'
import WebTerminal from '../components/WebTerminal.vue'
import { useClusterStore } from '../store/cluster'
import { parseDuration } from '../utils/sort'

const router = useRouter()
const clusterStore = useClusterStore()
const items = ref<NodeDetail[]>([])
const loading = ref(false)

// ------------------- 封禁 / 排空 -------------------
const drainVisible = ref(false)
const draining = ref(false)
const drainTarget = ref<NodeDetail | null>(null)
const terminalVisible = ref(false)
const terminalNode = ref('')

function openTerminal(row: NodeDetail) {
  terminalNode.value = row.name
  terminalVisible.value = true
}
const drainResult = ref<{ evicted: string[]; skipped: string[]; failed: { Name: string; Error: string }[] } | null>(null)
const drainOpts = reactive({ force: false, ignoreDaemonsets: true, deleteEmptydirData: false, gracePeriodSeconds: 30, timeoutSeconds: 180 })

async function cordon(row: NodeDetail) {
  await ElMessageBox.confirm(`封锁节点「${row.name}」后不再调度新 Pod，确定？`, '封锁节点', { type: 'warning' })
  await nodeApi.cordon(row.name, true)
  ElMessage.success(`已封锁 ${row.name}`)
  await load()
}

async function uncordon(row: NodeDetail) {
  await nodeApi.cordon(row.name, false)
  ElMessage.success(`已解除封锁 ${row.name}`)
  await load()
}

function openDrain(row: NodeDetail) {
  drainTarget.value = row
  drainResult.value = null
  drainVisible.value = true
}

async function doDrain() {
  if (!drainTarget.value) return
  draining.value = true
  try {
    drainResult.value = await nodeApi.drain(drainTarget.value.name, { ...drainOpts })
    ElMessage.success(`排空完成：驱逐 ${drainResult.value.evicted.length}，跳过 ${drainResult.value.skipped.length}，失败 ${drainResult.value.failed.length}`)
    if (drainResult.value.failed.length) console.warn('排空失败明细', drainResult.value.failed)
    await load()
  } finally {
    draining.value = false
  }
}

onMounted(load)

async function load() {
  if (!clusterStore.current) return
  loading.value = true
  try {
    items.value = await k8sApi.nodes()
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

function onRowClick(row: NodeDetail, _col: unknown, event: Event) {
  if ((event.target as HTMLElement).closest('.el-button')) return
  goDetail(row)
}

function goDetail(row: NodeDetail) {
  router.push(`/nodes/${row.name}`)
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.drain-result { margin-top: 10px; color: #606266; font-size: 13px; line-height: 1.8; }
.drain-fail { color: #f56c6c; }
</style>
