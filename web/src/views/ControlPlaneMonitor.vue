<!-- 控制平面组件监控（可观测性）：从集群总览迁出。
     全部组件平摊展开：每个组件一张卡片，头部给状态与实例数，卡内给指标瓦片 + 趋势图；
     时间档位与刷新是全局的（改档位即整页重查）。
     详情按组件逐个查询，并发限 3、先到先渲染，避免一次性把 Prometheus 打满。 -->
<template>
  <div class="cp-toolbar">
    <span class="kc-section-title">控制平面组件</span>
    <span class="dim">{{ items.length }} 个组件 · 全部展开</span>
    <div class="cp-tools">
      <el-radio-group v-model="range" size="small" @change="loadAll">
        <el-radio-button value="1h">1 小时</el-radio-button>
        <el-radio-button value="6h">6 小时</el-radio-button>
        <el-radio-button value="24h">24 小时</el-radio-button>
      </el-radio-group>
      <el-button size="small" :icon="Refresh" circle :loading="loading" @click="loadAll" />
    </div>
  </div>

  <el-alert v-if="promIssue" type="warning" :closable="false" style="margin-bottom: 12px" :title="promIssue" />

  <el-card v-for="cp in items" :key="cp.name" shadow="never" class="cp-block">
    <template #header>
      <div class="d-head">
        <span class="kc-section-title">{{ view(cp).title }}</span>
        <el-tag size="small" :type="cpTagType(view(cp))">{{ cpTagText(view(cp)) }}</el-tag>
        <span class="dim mono">{{ cp.name }}</span>
        <span v-if="view(cp).total > 0" class="dim">实例 {{ view(cp).ready }}/{{ view(cp).total }}</span>
        <span v-if="view(cp).total > 0" class="d-health" :class="view(cp).ready < view(cp).total ? 'txt-bad' : 'txt-ok'">
          {{ view(cp).ready < view(cp).total ? `${view(cp).total - view(cp).ready} 个实例不可用` : '实例全部就绪' }}
        </span>
      </div>
    </template>

    <div v-loading="!!pending[cp.name]" class="detail">
      <div v-if="view(cp).desc" class="d-desc">{{ view(cp).desc }}</div>

      <div v-if="view(cp).cards?.length" class="d-cards">
        <div v-for="c in view(cp).cards" :key="c.label" class="d-card">
          <div class="cp-h-label">{{ c.label }}</div>
          <div class="d-card-value mono">{{ fmtCard(c) }}</div>
        </div>
      </div>
      <div v-else-if="!pending[cp.name]" class="cp-empty">
        {{ view(cp).total === 0 ? '未接入 Prometheus 采集' : '已接入，暂无可用指标' }}
      </div>

      <div v-if="view(cp).charts?.length" class="kc-chart-grid">
        <div v-for="ch in view(cp).charts" :key="ch.name" class="kc-chart-card">
          <div class="kc-chart-title">{{ ch.name }}<span class="dim">{{ ch.unit }}</span></div>
          <MetricChart :series="[{ name: ch.name, data: ch.points as [number, number][], color: MC.primary, unit: ch.unit }]"
            height="200px" :y-axis-name="ch.unit" />
        </div>
      </div>
    </div>
  </el-card>

  <el-card v-if="!items.length && !loading" shadow="never">
    <el-empty :image-size="56" description="暂无控制面组件数据（Prometheus 未采集 kube-apiserver / etcd 等组件指标时此处为空）" />
  </el-card>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { METRIC_COLORS as MC } from '../utils/chart-palette'
import { Refresh } from '@element-plus/icons-vue'
import { k8sApi, type ControlPlaneSummary, type ControlPlaneDetail, type ControlCard } from '../api'
import MetricChart from '../components/MetricChart.vue'

type CpView = ControlPlaneSummary & Partial<ControlPlaneDetail>

const items = ref<ControlPlaneSummary[]>([])
const details = reactive<Record<string, ControlPlaneDetail>>({})
const pending = reactive<Record<string, boolean>>({})
const loading = ref(false)
const promIssue = ref('')
const range = ref('6h')

// 概要先行渲染（列表接口就有状态与 1 个指标），详情到达后覆盖同名组件
function view(cp: ControlPlaneSummary): CpView {
  return details[cp.name] || cp
}

async function load() {
  loading.value = true
  promIssue.value = ''
  try {
    // 先探 Prometheus：不可达时给明确原因，而不是满屏"未接入"
    const chk = await k8sApi.monitorCheck()
    if (chk.status !== 'ok') promIssue.value = `Prometheus 未接入或探测失败（${chk.prometheus}）：组件状态与指标可能不完整。`
    items.value = await k8sApi.controlPlane()
  } catch {
    promIssue.value = '控制面数据读取失败（Prometheus 不可达或集群 RBAC 不足），请检查监控接入。'
    items.value = []
  } finally {
    loading.value = false
  }
}

async function loadAll() {
  const r = range.value
  for (const k of Object.keys(details)) delete details[k]
  items.value.forEach((cp) => { pending[cp.name] = true })
  const queue = [...items.value]
  // 并发限 3：一次 7 个组件 × 十余条 range 查询会把 Prometheus 打满
  await Promise.all(Array.from({ length: Math.min(3, queue.length) }, async () => {
    for (let cp = queue.shift(); cp; cp = queue.shift()) {
      try {
        const d = await k8sApi.controlPlaneDetail(cp.name, r)
        if (r === range.value) details[cp.name] = d
      } catch { /* http 层已 toast；该卡留概要 */ } finally {
        pending[cp.name] = false
      }
    }
  }))
}

onMounted(async () => {
  await load()
  void loadAll()
})

// ---- 展示 ----
type CpLike = { ready: number; total: number }
function cpTagType(cp: CpLike) {
  if (cp.total === 0) return 'info'
  if (cp.ready === 0) return 'danger'
  if (cp.ready < cp.total) return 'warning'
  return 'success'
}
function cpTagText(cp: CpLike) {
  if (cp.total === 0) return '未接入'
  if (cp.ready === 0) return '不可用'
  if (cp.ready < cp.total) return '部分异常'
  return '正常'
}
function fmtCard(c: ControlCard) {
  if (c.value == null || Number.isNaN(Number(c.value))) return '--'
  return `${Number(c.value).toFixed(c.dec)}${c.unit ? ' ' + c.unit : ''}`
}
</script>

<style scoped>
.cp-toolbar { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.cp-tools { margin-left: auto; display: flex; align-items: center; gap: 10px; }
.dim { color: var(--kc-mute); }
.mono { font-family: var(--kc-mono); }
.cp-block + .cp-block { margin-top: 12px; }
.cp-block :deep(.el-card__header) { background: var(--kc-surface-2); }
.d-head { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.d-health { margin-left: auto; font-size: 13px; font-weight: 600; }
.detail { min-height: 96px; }
.d-desc { color: var(--kc-ink-2); font-size: 13px; margin: 2px 0 14px; }
.d-cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 10px; }
.d-card { background: var(--kc-surface-2); border: 1px solid var(--kc-line); border-radius: var(--kc-radius-md); padding: 10px 12px; }
.cp-h-label { color: var(--kc-mute); font-size: 13px; }
.d-card-value { font-size: 19px; font-weight: 700; letter-spacing: -0.02em; color: var(--kc-ink); }
.cp-empty { color: var(--kc-mute); font-size: 13px; }
.txt-ok { color: var(--kc-ok-text); }
.txt-bad { color: var(--kc-crit-text); }
.kc-chart-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 12px; margin-top: 14px; }
.kc-chart-card { border: 1px solid var(--kc-line); border-radius: var(--kc-radius-md); background: var(--kc-surface); padding: 12px 14px; }
.kc-chart-title { display: flex; align-items: center; gap: 8px; font-size: 13px; font-weight: 600; color: var(--kc-ink-2); margin-bottom: 6px; }
.kc-chart-title .dim { margin-left: auto; font-family: var(--kc-mono); font-size: 13px; }
</style>
