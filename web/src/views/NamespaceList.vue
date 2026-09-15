<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>命名空间</span>
        <div class="header-right">
          <el-button :icon="Refresh" circle @click="load" />
          <el-button type="primary" size="small" @click="openCreate">
            <el-icon><Plus /></el-icon>&nbsp;新建命名空间
          </el-button>
        </div>
      </div>
    </template>

    <el-input v-model="search" placeholder="搜索命名空间..." :prefix-icon="Search" clearable style="width: 260px; margin-bottom: 12px" @input="load" />

    <el-table border :data="items" v-loading="loading" stripe>
      <el-table-column prop="name" label="名称" min-width="180" sortable>
        <template #default="{ row }">
          <el-link type="primary" @click="goPods(row.name)">{{ row.name }}</el-link>
        </template>
      </el-table-column>
      <el-table-column label="状态" prop="status" width="120" sortable>
        <template #default="{ row }"><StatusTag :status="row.status" /></template>
      </el-table-column>
      <el-table-column label="标签" min-width="220">
        <template #default="{ row }">
          <el-tag v-for="(v, k) in row.labels" :key="k" size="small" style="margin-right: 4px">{{ k }}={{ v }}</el-tag>
          <span v-if="!row.labels || Object.keys(row.labels).length === 0">-</span>
        </template>
      </el-table-column>
      <el-table-column prop="age" label="运行时长" width="90" align="center" sortable :sort-by="(row) => parseDuration(row.age)" />
      <el-table-column label="操作" width="220" fixed="right">
        <template #default="{ row }">
          <el-button size="small" @click="goMonitor(row.name)">监控</el-button>
          <el-button size="small" @click="goPods(row.name)">查看 Pod</el-button>
          <el-button size="small" type="danger" @click="remove(row.name)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>
  </el-card>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, Search } from '@element-plus/icons-vue'
import { k8sApi, type NamespaceItem } from '../api'
import StatusTag from '../components/StatusTag.vue'
import { useClusterStore } from '../store/cluster'
import { parseDuration } from '../utils/sort'
import { confirmDelete } from '../utils/confirm'

const router = useRouter()
const route = useRoute()
const clusterStore = useClusterStore()
const items = ref<NamespaceItem[]>([])
const search = ref(String(route.query.search || ''))
const loading = ref(false)

// 搜索状态与 URL 同步
let searchTimer: ReturnType<typeof setTimeout> | undefined
watch(search, () => {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    router.replace({ query: { ...route.query, search: search.value || undefined } })
  }, 500)
})

let timer: ReturnType<typeof setTimeout> | undefined
onMounted(load)

async function load() {
  clearTimeout(timer)
  timer = setTimeout(async () => {
    if (!clusterStore.current) return
    loading.value = true
    try {
      items.value = await k8sApi.namespaces(search.value)
    } catch {
      /* 拦截器已提示 */
    } finally {
      loading.value = false
    }
  }, 200)
}

async function openCreate() {
  try {
    const { value } = await ElMessageBox.prompt('请输入命名空间名称', '新建命名空间', {
      confirmButtonText: '创建',
      cancelButtonText: '取消',
      inputPattern: /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/,
      inputErrorMessage: '名称仅支持小写字母、数字和连字符',
    })
    await k8sApi.createNamespace(value.trim())
    ElMessage.success('命名空间创建成功')
    load()
  } catch {
    /* 用户取消或失败 */
  }
}

async function remove(name: string) {
  try {
    await confirmDelete(name, { title: '删除命名空间', warning: '将同时删除命名空间内的所有资源，不可恢复。' })
  } catch {
    return
  }
  await k8sApi.deleteNamespace(name)
  ElMessage.success('已发起删除')
  load()
}

function goPods(ns: string) {
  router.push({ path: '/pods', query: { namespace: ns } })
}

function goMonitor(ns: string) {
  router.push(`/monitor/namespace/${ns}`)
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
</style>
