<template>
  <div>
    <div class="ops-bar">
      <el-select v-model="range" size="small" style="width: 110px" @change="load">
        <el-option v-for="r in RANGES" :key="r.v" :label="r.l" :value="r.v" />
      </el-select>
      <el-select v-model="group" size="small" style="width: 130px">
        <el-option label="全部分组" value="" />
        <el-option label="引擎指标" value="引擎指标" />
        <el-option label="资源" value="资源" />
      </el-select>
      <el-checkbox v-model="hideEmpty" size="small">隐藏无数据</el-checkbox>
      <el-button size="small" :loading="loading" @click="load">刷新</el-button>
      <el-button size="small" :disabled="!writable" @click="access">一键接入 exporter 抓取</el-button>
      <span class="tip">{{ note }}</span>
    </div>

    <div v-if="loading && !charts.length" class="tip" style="padding: 24px 0">正在拉取 {{ total }} 个指标的时间序列…</div>

    <div class="chart-grid">
      <el-card v-for="c in shown" :key="c.title" shadow="never" class="chart-card" body-style="padding: 8px 10px">
        <div class="chart-head">
          <span class="name">{{ c.title }}</span>
          <span class="val" v-if="c.series.length === 1 && c.last !== null">{{ fmtMetric(String(c.last), c.unit) }}</span>
          <span class="val empty" v-else-if="!c.series.length">无数据</span>
          <span class="val multi" v-else>{{ c.series.length }} 条序列</span>
        </div>
        <MetricChart :series="withUnit(c)" :height="'200px'" />
        <!-- 多序列时逐条给当前值：标题位只放一个数会被误读成集群总量 -->
        <div class="vals" v-if="c.series.length > 1">
          <span v-for="s in c.series" :key="s.name" class="val-item">
            <i :style="{ background: s.color }" />{{ s.name }} = {{ fmtMetric(String(s.data[s.data.length - 1][1]), c.unit) }}
          </span>
        </div>
        <div class="cap" v-if="c.total > c.series.length">共 {{ c.total }} 条序列，仅画前 {{ c.series.length }} 条</div>
        <div class="cap mono" :title="c.promql">{{ c.promql }}</div>
      </el-card>
    </div>

    <div v-if="!loading && !shown.length" class="tip" style="padding: 18px 0">
      没有任何曲线。若这里是 MySQL/PG/ES 等已运行的集群，通常是还没接入 exporter——点上方「一键接入 exporter 抓取」后再刷新。
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { k8sApi } from '../../api'
import { kbApi, KB_METRICS, type KBCluster } from '../../api/kb'
import MetricChart from '../MetricChart.vue'
import { seriesColors } from '../../utils/chart-palette'
import { kbMetricSelectors, kbRangeSeries, fmtMetric, type KbChart } from '../../utils/kb-metrics'

const props = defineProps<{ cluster: KBCluster; writable: boolean }>()

const RANGES = [
  { v: '1h', l: '近 1 小时' },
  { v: '6h', l: '近 6 小时' },
  { v: '24h', l: '近 24 小时' },
  { v: '7d', l: '近 7 天' },
]
// 一次刷新最多并发几个查询：全刷 20+ 张图时别把 Prometheus 打满
const POOL = 5

const range = ref('1h')
const group = ref('')
const hideEmpty = ref(true)
const loading = ref(false)
const charts = ref<KbChart[]>([])
const total = ref(0)
const hint = ref('') // 引擎无模板 / 未接 exporter 之类的说明

let seq = 0

const shown = computed(() =>
  charts.value.filter((c) => (!group.value || c.group === group.value) && (!hideEmpty.value || c.last !== null)),
)
const note = computed(() => {
  const parts = [hint.value]
  if (hideEmpty.value) {
    const empty = charts.value.filter((c) => c.last === null && (!group.value || c.group === group.value)).length
    if (empty) parts.push(`已隐藏 ${empty} 项无数据指标`)
  }
  return parts.filter(Boolean).join('；')
})

watch(
  () => [props.cluster?.namespace, props.cluster?.name, props.cluster?.engine],
  () => {
    charts.value = []
    hint.value = ''
    if (props.cluster) load()
  },
  { immediate: true },
)

async function load() {
  if (!props.cluster) return
  const c = props.cluster
  const defs = KB_METRICS[c.engine]
  if (!defs) {
    charts.value = []
    total.value = 0
    hint.value = '该引擎暂无内置指标模板'
    return
  }
  const sel = kbMetricSelectors(c)
  hint.value = sel.hasExporters ? '' : '未发现组件 headless Service：引擎指标取不到数，只有资源类曲线'
  const my = ++seq
  loading.value = true
  const jobs = defs
    .map((d) => ({ d, promql: d.promql(sel.sel, sel.pod) }))
    .filter((x) => !(x.d.group !== '资源' && !sel.hasExporters)) // 没接 exporter 时不刷引擎指标
  total.value = jobs.length
  const out: KbChart[] = []
  let cursor = 0
  async function worker() {
    while (cursor < jobs.length) {
      const { d, promql } = jobs[cursor++]
      let chart: KbChart = { title: d.title, group: d.group, promql, unit: d.unit, series: [], total: 0, last: null }
      try {
        const res = await k8sApi.monitorQueryRange(promql, range.value)
        const { series, total: n } = kbRangeSeries(res || [])
        chart = { ...chart, series, total: n, last: series[0]?.data.at(-1)?.[1] ?? null }
      } catch { /* 单个指标失败不影响其它图 */ }
      out.push(chart)
    }
  }
  try {
    await Promise.all(Array.from({ length: Math.min(POOL, jobs.length) }, worker))
  } finally {
    if (my === seq) {
      out.sort((a, b) => (a.group === b.group ? a.title.localeCompare(b.title) : a.group.localeCompare(b.group)))
      charts.value = out
      loading.value = false
    }
  }
}

// 单位挂在序列上，MetricChart 的 tooltip 才能带出 核/B/s 这类量纲
// 色板与 MetricChart 用同一份：卡片下方列出的当前值要带同色圆点
function withUnit(c: KbChart) {
  const colors = seriesColors(c.series)
  return c.series.map((s, i) => ({ ...s, unit: c.unit, color: colors[i] }))
}

async function access() {
  if (!props.cluster) return
  try {
    const r = await kbApi.monitorAccess(props.cluster.namespace, props.cluster.name)
    const n = r?.created?.length || 0
    if (!n) {
      ElMessage.info('没有带 exporter 端口的组件，无需接入')
    } else if (r.warning) {
      // 说清楚"建出来了但 Prometheus 不会加载"，否则用户只能对着空图表猜
      ElMessage.warning(`已提交 ${n} 条抓取配置，但${r.warning}`)
    } else {
      ElMessage.success(`已接入 ${n} 条抓取配置，约 1 分钟后出曲线${r.note ? '（' + r.note + '）' : ''}`)
    }
    load()
  } catch { /* 拦截器已提示 */ }
}
</script>

<style scoped>
.ops-bar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.tip { font-size: 13px; color: var(--el-text-color-secondary); }
.chart-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(420px, 1fr)); gap: 10px; margin-top: 10px; }
.chart-card :deep(.el-card__body) { padding: 6px 10px 8px; }
.chart-head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.chart-head .name { font-size: 13px; font-weight: 600; }
.chart-head .val { font-size: 13px; color: var(--el-text-color-primary); font-variant-numeric: tabular-nums; }
.chart-head .val.empty { color: var(--el-text-color-disabled); }
.vals { display: flex; flex-wrap: wrap; gap: 2px 14px; margin-top: 2px; }
.val-item { font-size: 13px; color: var(--el-text-color-regular); display: inline-flex; align-items: center; gap: 4px; }
.val-item i { width: 8px; height: 8px; border-radius: 50%; display: inline-block; }
.val.multi { font-size: 13px; color: var(--kc-mute); }
.cap { font-size: 13px; color: var(--kc-mute); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cap.mono { font-family: ui-monospace, Consolas, monospace; }
</style>
