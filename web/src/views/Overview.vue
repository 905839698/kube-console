<template>
  <div v-loading="loading">
    <!-- 统计卡片 -->
    <el-row :gutter="16">
      <el-col v-for="card in cards" :key="card.label" :span="6">
        <el-card shadow="hover" class="stat-card">
          <div class="stat-inner">
            <el-icon :size="30" :color="card.color"><component :is="card.icon" /></el-icon>
            <div>
              <div class="stat-value">{{ card.value }}</div>
              <div class="stat-label">{{ card.label }}</div>
            </div>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <el-row :gutter="16" style="margin-top: 16px">
      <el-col :span="12">
        <el-card shadow="never">
          <template #header>节点状态</template>
          <el-table :data="stats?.nodesList || []" size="small" max-height="420" @row-click="goNode">
            <el-table-column prop="name" label="名称" min-width="150"  sortable />
            <el-table-column label="状态" prop="status" width="90" sortable>
              <template #default="{ row }"><StatusTag :status="row.status" /></template>
            </el-table-column>
            <el-table-column prop="roles" label="角色" width="100"  sortable />
            <el-table-column prop="internalIP" label="IP" width="130"  sortable />
            <el-table-column prop="version" label="版本" width="90" />
            <el-table-column prop="cpuCores" label="CPU(核)" width="80" align="center"  sortable :sort-by="(row) => Number(row.cpuCores || 0)" />
            <el-table-column prop="memGi" label="内存(Gi)" width="90" align="center"  sortable :sort-by="(row) => Number(row.memGi || 0)" />
            <el-table-column prop="age" label="运行时长" width="70" align="center" sortable :sort-by="(row) => parseDuration(row.age)" />
          </el-table>
        </el-card>
      </el-col>
      <el-col :span="12">
        <el-card shadow="never">
          <template #header>资源容量</template>
          <div class="capacity-box">
            <div class="capacity-item">
              <div class="capacity-label">CPU 总容量</div>
              <div class="capacity-value">{{ stats?.cpuCapacity || '-' }} <span>核</span></div>
            </div>
            <div class="capacity-item">
              <div class="capacity-label">内存总容量</div>
              <div class="capacity-value">{{ stats?.memCapacity || '-' }} <span>Gi</span></div>
            </div>
            <div class="capacity-item">
              <div class="capacity-label">Pod 健康度</div>
              <div class="capacity-value">{{ stats?.podsRunning || 0 }} / {{ stats?.pods || 0 }} <span>运行中</span></div>
            </div>
            <div class="capacity-item">
              <div class="capacity-label">节点健康度</div>
              <div class="capacity-value">{{ stats?.nodesReady || 0 }} / {{ stats?.nodes || 0 }} <span>就绪</span></div>
            </div>
          </div>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { k8sApi, type OverviewStats } from '../api'
import StatusTag from '../components/StatusTag.vue'
import { useClusterStore } from '../store/cluster'
import { parseDuration } from '../utils/sort'

const router = useRouter()
const clusterStore = useClusterStore()
const stats = ref<OverviewStats>()
const loading = ref(false)

const cards = computed(() => [
  { label: '节点', value: stats.value?.nodes ?? '-', icon: 'Monitor', color: '#409eff' },
  { label: 'Pod', value: stats.value ? `${stats.value.podsRunning}/${stats.value.pods}` : '-', icon: 'Grid', color: '#67c23a' },
  { label: '工作负载', value: stats.value ? stats.value.deployments + stats.value.statefulSets + stats.value.daemonSets : '-', icon: 'Box', color: '#e6a23c' },
  { label: '命名空间', value: stats.value?.namespaces ?? '-', icon: 'FolderOpened', color: '#f56c6c' },
])

async function load() {
  if (!clusterStore.current) return
  loading.value = true
  try {
    stats.value = await k8sApi.overview()
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

function goNode(row: { name: string }) {
  router.push(`/nodes/${row.name}`)
}

onMounted(load)
</script>

<style scoped>
.stat-card { cursor: default; }
.stat-inner { display: flex; align-items: center; gap: 14px; }
.stat-value { font-size: 26px; font-weight: 600; color: #303133; }
.stat-label { font-size: 13px; color: #909399; }
.capacity-box { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
.capacity-item { background: #f5f7fa; border-radius: 6px; padding: 16px; }
.capacity-label { font-size: 13px; color: #909399; margin-bottom: 8px; }
.capacity-value { font-size: 22px; font-weight: 600; color: #303133; }
.capacity-value span { font-size: 13px; font-weight: 400; color: #909399; }
</style>
