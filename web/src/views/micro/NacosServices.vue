<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>服务发现 ({{ filtered.length }})</span>
        <div class="header-right">
          <el-input v-model="search" placeholder="搜索服务名 / 分组..." :prefix-icon="Search" clearable style="width: 220px" />
          <NamespaceSelect v-model="namespace" multiple width="260px" @change="onNsChange" />
          <el-button :icon="Refresh" circle @click="load" />
        </div>
      </div>
    </template>

    <template v-if="ready.configured">
      <el-table border :data="filtered" v-loading="loading" stripe>
        <el-table-column prop="name" label="服务名" min-width="240" show-overflow-tooltip>
          <template #default="{ row }">
            <el-link type="primary" @click="openInstances(row)">{{ row.name }}</el-link>
          </template>
        </el-table-column>
        <el-table-column prop="groupName" label="分组" width="150" show-overflow-tooltip>
          <template #default="{ row }">{{ row.groupName || 'DEFAULT_GROUP' }}</template>
        </el-table-column>
        <el-table-column prop="namespace" label="命名空间" min-width="130" show-overflow-tooltip />
        <el-table-column label="集群数" width="80" align="center">
          <template #default="{ row }">{{ row.clusterCount || '—' }}</template>
        </el-table-column>
        <el-table-column label="实例" width="140" align="center">
          <template #default="{ row }">
            <el-tag v-if="row.ipCount > 0" size="small" :type="row.healthyCount >= row.ipCount ? 'success' : row.healthyCount > 0 ? 'warning' : 'danger'">
              {{ row.healthyCount }}/{{ row.ipCount }} 健康
            </el-tag>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="140" fixed="right" align="center">
          <template #default="{ row }">
            <el-button link type="primary" size="small" @click="openInstances(row)">实例</el-button>
            <el-button link type="danger" size="small" @click="removeService(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </template>

    <el-empty v-else description="当前集群尚未接入 Nacos（服务发现不可用）">
      <el-button v-if="userStore.isAdmin" type="primary" @click="$router.push('/nacos')">前往 Nacos 管理</el-button>
    </el-empty>

    <!-- 实例列表 -->
    <el-dialog v-model="instDlg" :title="`实例：${instTarget?.name || ''}（${instTarget?.namespace || ''}）`" width="860px">
      <el-table border :data="instances" size="small" v-loading="instLoading" stripe>
        <el-table-column label="实例" min-width="180">
          <template #default="{ row }">{{ row.ip }}:{{ row.port }}</template>
        </el-table-column>
        <el-table-column label="健康" width="80" align="center">
          <template #default="{ row }">
            <el-tag size="small" :type="row.healthy ? 'success' : 'danger'">{{ row.healthy ? '健康' : '异常' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="70" align="center">
          <template #default="{ row }">
            <el-tag size="small" :type="row.enabled ? 'success' : 'info'" effect="plain">{{ row.enabled ? '是' : '否' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="clusterName" label="集群" width="100" />
        <el-table-column prop="weight" label="权重" width="70" align="center" />
        <el-table-column label="临时" width="70" align="center">
          <template #default="{ row }">{{ row.ephemeral ? '是' : '否' }}</template>
        </el-table-column>
        <el-table-column label="元数据" min-width="180" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="metaKeys(row).length" class="mono">{{ metaText(row) }}</span>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
      </el-table>
    </el-dialog>
  </el-card>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { Refresh, Search } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { nacosApi, nsParam, type NacosServiceItem, type NacosInstanceItem } from '../../api'
import { useClusterStore } from '../../store/cluster'
import { useNamespaceStore } from '../../store/namespace'
import { useUserStore } from '../../store/user'
import NamespaceSelect from '../../components/NamespaceSelect.vue'

const clusterStore = useClusterStore()
const nsStore = useNamespaceStore()
const userStore = useUserStore()

const cluster = computed(() => clusterStore.current || '')
const ready = ref<{ configured: boolean; enabled?: boolean }>({ configured: true })
const loading = ref(false)
const services = ref<NacosServiceItem[]>([])
const search = ref('')

const instDlg = ref(false)
const instLoading = ref(false)
const instTarget = ref<NacosServiceItem>()
const instances = ref<NacosInstanceItem[]>([])

// 命名空间选择与顶栏全局选择双向同步（写回 store，其他页面随之联动）
const namespace = ref<string[]>([...nsStore.selected])
function onNsChange(v: string[] | string) {
  namespace.value = (Array.isArray(v) ? v : [v]) as string[]
  nsStore.select(namespace.value)
  load()
}

const filtered = computed(() => {
  const kw = search.value.trim().toLowerCase()
  if (!kw) return services.value
  return services.value.filter(
    (s) => s.name.toLowerCase().includes(kw) || (s.groupName || '').toLowerCase().includes(kw) || s.namespace.toLowerCase().includes(kw),
  )
})

async function load() {
  if (!cluster.value) return
  try {
    ready.value = await nacosApi.ready(cluster.value)
  } catch {
    ready.value = { configured: true }
  }
  if (!ready.value.configured) {
    services.value = []
    return
  }
  loading.value = true
  try {
    services.value = (await nacosApi.services(cluster.value, nsParam(namespace.value))) || []
  } catch {
    services.value = []
  } finally {
    loading.value = false
  }
}

async function openInstances(row: NacosServiceItem) {
  instTarget.value = row
  instDlg.value = true
  instLoading.value = true
  instances.value = []
  try {
    instances.value = (await nacosApi.instances(cluster.value, row.namespace, row.name, row.groupName || 'DEFAULT_GROUP')) || []
  } catch {
    /* 拦截器已提示 */
  } finally {
    instLoading.value = false
  }
}

async function removeService(row: NacosServiceItem) {
  try {
    await ElMessageBox.confirm(
      `删除服务 ${row.name}（分组 ${row.groupName || 'DEFAULT_GROUP'}，命名空间 ${row.namespace}）？` +
        '仅删除 Nacos 侧注册记录，不删除任何 K8s 资源；已注册实例心跳会自动重新注册。',
      '删除服务', { type: 'warning' },
    )
  } catch {
    return
  }
  try {
    await nacosApi.serviceDelete(cluster.value, row.namespace, row.name, row.groupName || 'DEFAULT_GROUP')
    ElMessage.success('已删除')
    await load()
  } catch {
    /* 拦截器已提示 */
  }
}

function metaKeys(row: NacosInstanceItem): string[] {
  return Object.keys(row.metadata || {})
}
function metaText(row: NacosInstanceItem): string {
  return metaKeys(row).map((k) => `${k}=${row.metadata![k]}`).join(', ')
}

// 顶栏全局命名空间变化时立即跟随（页面选择已写回 store 时值相同，不会循环）
watch(
  () => nsStore.selected,
  (v) => {
    if (JSON.stringify(v) !== JSON.stringify(namespace.value)) {
      namespace.value = [...v]
      load()
    }
  },
  { deep: true },
)
watch(cluster, load)

onMounted(load)
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 8px; }
.header-right { display: flex; align-items: center; gap: 10px; }
.muted { color: #909399; }
.mono { font-family: Consolas, monospace; font-size: 12px; }
</style>
