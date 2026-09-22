<template>
  <div ref="chartEl" class="metric-chart" />
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import echarts from '../utils/echarts'

export interface ChartSeries {
  name: string
  /** [timestamp, value][] */
  data: [number, number][]
  color?: string
  unit?: string
}

const props = defineProps<{
  series: ChartSeries[]
  height?: string
  yAxisName?: string
  smooth?: boolean
  /** 迷你图模式：无坐标轴文字、紧凑网格，曲线铺满 */
  mini?: boolean
}>()

const chartEl = ref<HTMLElement>()
let chart: ReturnType<typeof echarts.init> | null = null
let observer: ResizeObserver | null = null
let resizeFallback: (() => void) | null = null

function render() {
  if (!chartEl.value) return
  if (!chart) {
    chart = echarts.init(chartEl.value)
  }
  const series = props.series.filter((s) => s.data.length > 0)
  const mini = props.mini === true
  const hasData = series.length > 0
  chart.setOption(
    {
      // 无数据时显示占位文本（避免空白图表）
      graphic: hasData
        ? []
        : [
            {
              type: 'text',
              left: 'center',
              top: 'middle',
              style: { text: '暂无数据', fill: '#c0c4cc', fontSize: 13 },
            },
          ],
      color: series.map((s) => s.color).filter(Boolean) as string[],
      tooltip: {
        trigger: 'axis',
        formatter: (params: any) => {
          if (!params.length) return ''
          const ts = params[0].value[0]
          const time = new Date(ts * 1000).toLocaleString('zh-CN', { hour12: false })
          let html = `<div style="font-size:12px">${time}</div>`
          for (const p of params) {
            const s = series[p.seriesIndex]
            // 小数值自适应精度：空闲 Pod 的 CPU 用量 ~0.0000x 核，固定 2 位小数会全显 0.00
            const raw = p.value[1]
            const v = typeof raw === 'number' ? (Math.abs(raw) < 0.01 ? raw.toFixed(4) : raw.toFixed(2)) : raw
            html += `<div>${s.name}: <b>${v}${s.unit || ''}</b></div>`
          }
          return html
        },
      },
      legend: !mini && series.length > 1 ? { data: series.map((s) => s.name), top: 0 } : undefined,
      grid: mini
        ? { left: 2, right: 2, top: 2, bottom: 2 }
        : { left: 50, right: 16, top: series.length > 1 ? 32 : 16, bottom: 24 },
      xAxis: {
        type: 'time',
        axisLabel: mini
          ? { show: false }
          : { formatter: (v: number) => new Date(v * 1000).toLocaleTimeString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit' }) },
        axisLine: mini ? { show: false } : undefined,
        axisTick: mini ? { show: false } : undefined,
        splitLine: mini ? { show: false } : undefined,
      },
      yAxis: {
        type: 'value',
        name: mini ? '' : props.yAxisName,
        scale: true,
        axisLabel: mini ? { show: false } : undefined,
        axisLine: mini ? { show: false } : undefined,
        axisTick: mini ? { show: false } : undefined,
        splitLine: mini ? { show: false } : { lineStyle: { type: 'dashed' } },
      },
      series: series.map((s) => ({
        name: s.name,
        type: 'line',
        data: s.data,
        smooth: props.smooth !== false,
        showSymbol: false,
        lineStyle: { width: mini ? 1.5 : 2 },
        areaStyle: series.length === 1 ? { opacity: mini ? 0.15 : 0.08 } : undefined,
      })),
    },
    true,
  )
  chart.resize()
}

onMounted(() => {
  render()
  // ResizeObserver：容器从隐藏变为可见（如 tab 切换）或窗口变化时自动重绘
  if (chartEl.value && typeof ResizeObserver !== 'undefined') {
    observer = new ResizeObserver(() => {
      if (chartEl.value && chartEl.value.clientWidth > 0) {
        chart?.resize()
      }
    })
    observer.observe(chartEl.value)
  } else if (chartEl.value) {
    // 旧实现把 handler 存到 __resizeHandler 短路赋值（首挂载时 undefined → 永不赋值），
    // unmount 时 removeEventListener(undefined) 移不掉 → 每次挂载泄漏一个 resize 监听
    resizeFallback = () => chart?.resize()
    window.addEventListener('resize', resizeFallback)
  }
})

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = null
  if (resizeFallback) {
    window.removeEventListener('resize', resizeFallback)
    resizeFallback = null
  }
  chart?.dispose()
  chart = null
})

watch(
  () => props.series,
  () => render(),
  { deep: true },
)
</script>

<style scoped>
.metric-chart { height: v-bind('height'); width: 100%; }
</style>
