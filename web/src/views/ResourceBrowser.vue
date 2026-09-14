<template>
  <div>
    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <span>自定义资源浏览器（discovery 驱动，含全部 CRD）</span>
          <div class="header-right">
            <el-input v-model="search" placeholder="搜索资源..." :prefix-icon="Search" clearable style="width: 240px" />
            <el-button :icon="Refresh" circle @click="load(true)" />
          </div>
        </div>
      </template>

      <el-collapse v-model="openGroups">
        <el-collapse-item v-for="g in filteredGroups" :key="g.group + g.version" :name="g.group + g.version">
          <template #title>
            <span class="group-title">{{ g.group || 'core (v1)' }}</span>
            <el-tag size="small" style="margin-left: 8px">{{ g.resources.length }}</el-tag>
          </template>
          <el-table border :data="g.resources" size="small" stripe>
            <el-table-column label="资源" min-width="220">
              <template #default="{ row }">
                <el-link type="primary" @click="openResource(g, row)">{{ row.resource }}</el-link>
              </template>
            </el-table-column>
            <el-table-column prop="kind" label="Kind" width="180" />
            <el-table-column prop="version" label="版本" width="120" />
            <el-table-column label="作用域" width="100">
              <template #default="{ row }">
                <el-tag :type="row.namespaced ? 'info' : 'warning'" size="small">{{ row.namespaced ? '命名空间' : '集群' }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="短名" width="120">
              <template #default="{ row }">{{ (row.shortNames || []).join(', ') }}</template>
            </el-table-column>
            <el-table-column label="操作" width="90">
              <template #default="{ row }">
                <el-button size="small" @click="openResource(g, row)">浏览</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-collapse-item>
      </el-collapse>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { Refresh, Search } from '@element-plus/icons-vue'
import { k8sApi, nsParam, type GroupDef, type ResourceDef } from '../api'
import { useClusterStore } from '../store/cluster'
import { useNamespaceStore } from '../store/namespace'

const router = useRouter()
const route = useRoute()
const clusterStore = useClusterStore()
const nsStore = useNamespaceStore()
const groups = ref<GroupDef[]>([])
const openGroups = ref<string[]>([])
const search = ref(String(route.query.search || ''))

const filteredGroups = computed(() => {
  if (!search.value) return groups.value
  const s = search.value.toLowerCase()
  return groups.value
    .map((g) => ({ ...g, resources: g.resources.filter((r) => r.resource.toLowerCase().includes(s) || r.kind.toLowerCase().includes(s)) }))
    .filter((g) => g.resources.length > 0)
})

onMounted(() => load())

async function load(force = false) {
  try {
    groups.value = await k8sApi.resourceDefs(force)
    if (openGroups.value.length === 0) {
      openGroups.value = groups.value.slice(0, 3).map((g) => g.group + g.version)
    }
  } catch {
    /* 拦截器已提示 */
  }
}

function openResource(g: GroupDef, r: ResourceDef) {
  router.push({
    path: `/crd/${g.group || 'core'}/${r.version}/${r.resource}`,
    // 命名空间级资源跟随顶栏全局选择；集群级资源标记为集群作用域
    query: r.namespaced ? { namespaced: '1', namespace: nsParam(nsStore.selected) } : { namespaced: '0' },
  })
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.header-right { display: flex; gap: 8px; align-items: center; }
.group-title { font-weight: 600; }
</style>
