<template>
  <div class="metric-card">
    <div class="metric-inner">
      <div class="metric-label">{{ label }}</div>
      <div class="metric-value" :style="{ color: value == null ? 'var(--kc-mute)' : color }">
        {{ value !== null && value !== undefined ? value.toFixed(decimals) : '--' }}
        <span class="metric-unit">{{ unit }}</span>
      </div>
      <div class="metric-sub" v-if="sub">{{ sub }}</div>
      <div v-if="trend && trend.length > 1" class="metric-trend">
        <MetricChart :series="[{ name: label, data: trend, color, unit }]" height="64px" :smooth="true" :mini="true" />
      </div>
    </div>
  </div>
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
/* Meadow 口径的紧凑指标卡：浅底量板 + display 字体数值，与 kc-gauge-card 同族 */
.metric-card {
  border: 1px solid var(--kc-line);
  border-radius: var(--kc-radius-md);
  background: var(--kc-surface-2);
  padding: 12px 14px;
  height: 100%;
  box-sizing: border-box;
  transition: transform 0.24s cubic-bezier(0.2, 0.8, 0.2, 1), box-shadow 0.24s;
}
.metric-card:hover {
  transform: translateY(-3px);
  box-shadow: var(--kc-shadow);
}
.metric-inner { padding: 0; }
.metric-label { font-size: 13px; font-weight: 600; color: var(--kc-ink-2); margin-bottom: 5px; }
.metric-value {
  font-family: var(--kc-font-display);
  font-size: 24px;
  font-weight: 800;
  letter-spacing: -0.035em;
  font-variant-numeric: tabular-nums;
  color: var(--kc-ink);
  line-height: 1.1;
}
.metric-unit { font-family: var(--kc-font-body); font-size: 13px; font-weight: 600; color: var(--kc-mute); margin-left: 3px; letter-spacing: 0; }
.metric-sub { font-size: 13px; color: var(--kc-mute); margin-top: 4px; }
.metric-trend { margin-top: 8px; }
</style>
