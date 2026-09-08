<template>
  <div>
    <!-- 指标卡片 -->
    <el-row :gutter="16">
      <el-col v-for="c in cards" :key="c.label" :span="Math.max(6, 24 / (cards.length || 1))">
        <MetricCard :label="c.label" :value="c.value" :unit="c.unit" :decimals="c.decimals ?? 1" :color="c.color" :sub="c.sub" />
      </el-col>
    </el-row>
    <!-- 趋势图 -->
    <div v-for="ch in charts" :key="ch.title" style="margin-top: 12px">
      <div class="chart-title">{{ ch.title }}</div>
      <MetricChart :series="ch.series" height="220px" :y-axis-name="ch.yAxisName" />
    </div>
    <el-empty v-if="noData" description="无监控数据（Prometheus 指标缺失或未采集）" :image-size="60" style="margin-top: 20px" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import MetricCard from './MetricCard.vue'
import MetricChart, { type ChartSeries } from './MetricChart.vue'

export interface MetricCardDef {
  label: string
  value: number | null
  unit?: string
  decimals?: number
  color?: string
  sub?: string
}

export interface MetricChartDef {
  title: string
  series: ChartSeries[]
  yAxisName?: string
}

const props = defineProps<{ cards: MetricCardDef[]; charts: MetricChartDef[]; loading?: boolean }>()

const noData = computed(() => {
  if (props.loading) return false
  const hasCard = props.cards.some((c) => c.value !== null && c.value !== undefined)
  const hasChart = props.charts.some((ch) => ch.series.some((s) => s.data.length > 0))
  return !hasCard && !hasChart
})
</script>

<style scoped>
.chart-title { font-size: 13px; color: #606266; font-weight: 600; margin-bottom: 6px; }
</style>
