<template>
  <el-card shadow="hover" class="metric-card">
    <div class="metric-inner">
      <div class="metric-label">{{ label }}</div>
      <div class="metric-value" :style="{ color: color }">
        {{ value !== null && value !== undefined ? value.toFixed(decimals) : '--' }}
        <span class="metric-unit">{{ unit }}</span>
      </div>
      <div class="metric-sub" v-if="sub">{{ sub }}</div>
      <div v-if="trend && trend.length > 1" class="metric-trend">
        <MetricChart :series="[{ name: label, data: trend, color, unit }]" height="64px" :smooth="true" :mini="true" />
      </div>
    </div>
  </el-card>
</template>

<script setup lang="ts">
import MetricChart from './MetricChart.vue'

defineProps<{
  label: string
  value: number | null
  unit?: string
  decimals?: number
  color?: string
  sub?: string
  trend?: [number, number][]
}>()
</script>

<style scoped>
.metric-inner { padding: 4px 2px; }
.metric-label { font-size: 13px; color: #909399; margin-bottom: 6px; }
.metric-value { font-size: 26px; font-weight: 600; }
.metric-unit { font-size: 13px; font-weight: 400; color: #909399; margin-left: 2px; }
.metric-sub { font-size: 12px; color: #c0c4cc; margin-top: 4px; }
.metric-trend { margin-top: 8px; }
</style>
