<template>
  <div>
    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <span>配额总览（已配置 {{ configured }} / 共 {{ items.length }} 个命名空间）</span>
          <div class="header-right">
            <el-tag v-if="configured === 0" type="warning" size="small">集群未配置 ResourceQuota，可在任意命名空间新建</el-tag>
            <el-button :icon="Refresh" circle size="small" @click="load" />
          </div>
        </div>
      </template>

      <el-table border :data="items" v-loading="loading" stripe size="small">
        <el-table-column prop="namespace" label="命名空间" min-width="170" sortable />
        <el-table-column label="配额名称" min-width="170">
          <template #default="{ row }">
            <el-tag v-if="row.quotaName" type="success" size="small">{{ row.quotaName }}</el-tag>
            <el-tag v-else type="info" size="small">未配置</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="限制 (hard)" min-width="240">
          <template #default="{ row }">
            <span v-if="Object.keys(row.hard).length" class="quota-values">{{ formatHard(row.hard) }}</span>
            <span v-else class="gray">-</span>
          </template>
        </el-table-column>
        <el-table-column label="当前使用 (used)" min-width="240">
          <template #default="{ row }">
            <span v-if="Object.keys(row.used).length" class="quota-values">{{ formatHard(row.used) }}</span>
            <span v-else class="gray">-</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="160" fixed="right">
          <template #default="{ row }">
            <el-button v-if="!row.quotaName" size="small" type="primary" plain @click="openCreate(row.namespace)">新建配额</el-button>
            <el-button v-else size="small" @click="viewDetail(row)">详情</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- 新建配额（表单|YAML） -->
    <el-dialog v-model="createVisible" :title="`新建 ResourceQuota - ${createNs}`" width="860px" top="8vh" destroy-on-close>
      <div style="min-height: 420px">
        <ObjectEditor
          v-if="createVisible"
          :kind="'resourcequotas'"
          :creating="true"
          :namespace="createNs"
          @saved="onCreated"
          @cancel="createVisible = false"
        />
      </div>
    </el-dialog>

    <!-- 配额详情 -->
    <el-dialog v-model="detailVisible" :title="`ResourceQuota / ${detailName}`" width="860px" top="8vh" destroy-on-close>
      <div style="min-height: 420px">
        <ObjectEditor v-if="detailYaml !== null" :kind="'resourcequotas'" :yaml="detailYaml" :namespace="detailNs" @saved="onSaved" @cancel="detailVisible = false" />
      </div>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { k8sApi, type QuotaOverviewItem } from '../api'
import ObjectEditor from '../components/ObjectEditor.vue'
import { useClusterStore } from '../store/cluster'

const clusterStore = useClusterStore()
const items = ref<QuotaOverviewItem[]>([])
const loading = ref(false)
const createVisible = ref(false)
const createNs = ref('default')
const detailVisible = ref(false)
const detailYaml = ref<string | null>(null)
const detailName = ref('')
const detailNs = ref('')

const configured = computed(() => items.value.filter((i) => i.quotaName).length)

onMounted(load)

async function load() {
  if (!clusterStore.current) return
  loading.value = true
  try {
    items.value = await k8sApi.quotaOverview()
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

/** 格式化配额键值："requests.cpu: 100m, memory: 4Gi, pods: 20"（只显示关键项） */
function formatHard(hard: Record<string, string>): string {
  const keys = ['requests.cpu', 'requests.memory', 'limits.cpu', 'limits.memory', 'cpu', 'memory', 'pods', 'count/deployments.apps']
  const parts: string[] = []
  for (const k of keys) {
    if (hard[k] !== undefined) parts.push(`${k}: ${hard[k]}`)
  }
  if (parts.length === 0) {
    for (const [k, v] of Object.entries(hard).slice(0, 4)) {
      parts.push(`${k}: ${v}`)
    }
  }
  return parts.join('，')
}

function openCreate(ns: string) {
  createNs.value = ns
  createVisible.value = true
}

async function viewDetail(row: QuotaOverviewItem) {
  const { yaml } = await k8sApi.resourceYaml('resourcequotas', row.namespace, row.quotaName)
  detailYaml.value = yaml
  detailName.value = row.quotaName
  detailNs.value = row.namespace
  detailVisible.value = true
}

function onCreated() {
  createVisible.value = false
  load()
}

function onSaved() {
  detailVisible.value = false
  load()
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.header-right { display: flex; gap: 8px; align-items: center; }
.quota-values { font-size: 12px; color: #606266; }
.gray { color: #c0c4cc; }
</style>
