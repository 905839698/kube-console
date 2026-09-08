<template>
  <div v-loading="loading">
    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <el-page-header :content="`命名空间监控 / ${name}`" @back="router.back()" />
          <RangeSwitch v-model="range" @change="load" />
        </div>
      </template>
      <MetricPanel
        :cards="cards"
        :charts="charts"
        :loading="loading"
      />
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { k8sApi, type NamespaceMonitor } from '../api'
import MetricPanel, { type MetricCardDef, type MetricChartDef } from '../components/MetricPanel.vue'
import RangeSwitch from '../components/RangeSwitch.vue'
import { useClusterStore } from '../store/cluster'

const route = useRoute()
const router = useRouter()
const clusterStore = useClusterStore()

const name = computed(() => String(route.params.name))
const range = ref('6h')
const data = ref<NamespaceMonitor>()
const loading = ref(false)

const cards = computed<MetricCardDef[]>(() => [
  { label: 'CPU 使用率', value: data.value?.cpuUsagePct ?? null, unit: '%', color: '#409eff' },
  { label: '内存使用率', value: data.value?.memUsagePct ?? null, unit: '%', color: '#67c23a' },
  { label: 'Pod 数量', value: data.value?.podCount ?? null, unit: '个', decimals: 0, color: '#e6a23c' },
  { label: '磁盘写', value: data.value?.diskWriteMBs ?? null, unit: 'MB/s', decimals: 2, color: '#909399' },
  { label: '网络接收', value: data.value?.netRxMBs ?? null, unit: 'MB/s', decimals: 2, color: '#f56c6c' },
  { label: '网络发送', value: data.value?.netTxMBs ?? null, unit: 'MB/s', decimals: 2, color: '#9254de' },
])

const charts = computed<MetricChartDef[]>(() => [
  { title: 'CPU 使用率趋势', series: [{ name: 'CPU', data: data.value?.cpuUsageTrend || [], color: '#409eff', unit: '%' }], yAxisName: '%' },
  { title: '内存使用率趋势', series: [{ name: '内存', data: data.value?.memUsageTrend || [], color: '#67c23a', unit: '%' }], yAxisName: '%' },
  { title: 'Pod 数量趋势', series: [{ name: 'Pod 数', data: data.value?.podCountTrend || [], color: '#e6a23c', unit: '个' }], yAxisName: '个' },
  { title: '磁盘写趋势', series: [{ name: '磁盘写', data: data.value?.diskWriteTrend || [], color: '#909399', unit: 'MB/s' }], yAxisName: 'MB/s' },
  {
    title: '网络流量',
    series: [
      { name: '接收', data: data.value?.netRxTrend || [], color: '#f56c6c', unit: 'MB/s' },
      { name: '发送', data: data.value?.netTxTrend || [], color: '#9254de', unit: 'MB/s' },
    ],
    yAxisName: 'MB/s',
  },
])

onMounted(load)

async function load() {
  if (!clusterStore.current) return
  loading.value = true
  try {
    data.value = await k8sApi.monitorNamespace(name.value, range.value)
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
</style>
