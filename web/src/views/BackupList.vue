<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>备份概览（Velero）</span>
        <el-select v-model="clusterName" style="width: 200px" @change="load">
          <el-option v-for="cl in clusters" :key="cl" :label="cl" :value="cl" />
        </el-select>
        <el-button :icon="Refresh" circle size="small" @click="load" style="margin-left: 12px" />
      </div>
    </template>

    <el-alert v-if="!installed" type="info" :closable="false"
      title="该集群未安装 Velero（或未部署备份 CRD）。可在集群中部署 Velero 后在此查看备份任务。" />
    <template v-else>
      <div class="sec-title">备份任务 ({{ items.length }})</div>
      <el-table border :data="items" size="small" stripe>
        <el-table-column prop="name" label="名称" min-width="220" />
        <el-table-column prop="age" label="创建于" width="90" align="center" />
        <el-table-column label="标签" min-width="200">
          <template #default="{ row }">
            <el-tag v-for="(v, k) in row.labels" :key="k" size="small" type="info" style="margin-right: 4px">{{ k }}={{ v }}</el-tag>
          </template>
        </el-table-column>
      </el-table>
      <div class="sec-title">备份计划 ({{ schedules.length }})</div>
      <el-table border :data="schedules" size="small" stripe>
        <el-table-column prop="name" label="名称" min-width="220" />
        <el-table-column prop="age" label="创建于" width="90" align="center" />
      </el-table>
    </template>
  </el-card>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { backupApi, clusterApi, type GenericItem } from '../api'

const clusters = ref<string[]>([])
const clusterName = ref('')
const installed = ref(false)
const items = ref<GenericItem[]>([])
const schedules = ref<GenericItem[]>([])

async function load() {
  if (!clusterName.value) return
  try {
    const r = await backupApi.list()
    installed.value = !!r.installed
    items.value = r.items || []
    schedules.value = r.schedules || []
  } catch {
    installed.value = false
  }
}

onMounted(async () => {
  try {
    clusters.value = (await clusterApi.list()).map((c) => c.name)
    clusterName.value = clusters.value[0] || ''
    await load()
  } catch { /* ignore */ }
})
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.sec-title { font-weight: 600; margin: 14px 0 8px; }
</style>
