<!-- 控制平面组件监控（可观测性）：从集群总览迁出。
     列表 = 每组件就绪状态 + 1 个概要指标（轻量，手动刷新）；
     点开抽屉 = 该组件全部指标卡 + 趋势图（趋势按 1h/6h/24h 档位查询，
     step 由后端放宽，只在点开时付费——对 Prometheus 的压力比旧总览整块拉取更低）。 -->
<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>控制平面组件</span>
        <div>
          <span class="dim" style="margin-right: 12px">API Server / etcd / 调度器 / 控制器 / DNS / proxy / Prometheus</span>
          <el-button size="small" :icon="Refresh" circle :loading="loading" @click="load" />
        </div>
      </div>
    </template>

    <el-alert v-if="promIssue" type="warning" :closable="false" style="margin-bottom: 12px"
      :title="promIssue" />

    <div v-loading="loading" class="cp-grid">
      <el-card v-for="cp in items" :key="cp.name" shadow="hover" class="cp-item" @click="openDetail(cp)">
        <div class="cp-head">
          <span class="cp-name">{{ cp.title }}</span>
          <el-tag size="small" :type="cpTagType(cp)">{{ cpTagText(cp) }}</el-tag>
        </div>
        <div class="cp-sub">{{ cp.name }}<span v-if="cp.total > 0" class="dim"> · 实例 {{ cp.ready }}/{{ cp.total }}</span></div>
        <div v-if="cp.cards.length" class="cp-headline">
          <div class="cp-h-label">{{ cp.cards[0].label }}</div>
          <div class="cp-h-value">{{ fmtCard(cp.cards[0]) }}</div>
        </div>
        <div v-else class="cp-empty">{{ cp.total === 0 ? '未接入 Prometheus 采集' : '已接入，概要暂无数据' }}</div>
        <div v-if="cp.cards.length && cp.total > 0" class="cp-health" :class="cp.ready < cp.total ? 'txt-bad' : 'txt-ok'">
          {{ cp.ready < cp.total ? `${cp.total - cp.ready} 个实例不可用` : '实例全部就绪' }}
        </div>
      </el-card>
      <div v-if="!items.length && !loading" class="cp-empty-all">
        暂无控制面组件数据（Prometheus 未采集 kube-apiserver/etcd 等组件指标时此处为空）
      </div>
    </div>
  </el-card>

  <!-- 详情抽屉：点开才查询 -->
  <el-drawer v-model="drawer" size="62%" :title="detail?.title || '组件详情'">
    <div v-loading="detailLoading" class="detail">
      <template v-if="detail">
        <div class="d-sub">
          <span class="dim mono">{{ detail.name }}</span>
          <el-tag size="small" :type="cpTagType(detail)" style="margin-left: 8px">{{ cpTagText(detail) }}</el-tag>
          <span v-if="detail.total > 0" class="dim" style="margin-left: 8px">实例 {{ detail.ready }}/{{ detail.total }}</span>
        </div>
        <div v-if="detail.desc" class="d-desc">{{ detail.desc }}</div>

        <div class="d-cards" v-if="detail.cards.length">
          <div v-for="c in detail.cards" :key="c.label" class="d-card">
            <div class="cp-h-label">{{ c.label }}</div>
            <div class="d-card-value">{{ fmtCard(c) }}</div>
          </div>
        </div>
        <div v-else-if="!detailLoading" class="cp-empty">该组件未采集到可用指标。</div>

        <template v-if="detail.charts.length">
          <div class="d-toolbar">
            <span class="sec-title" style="margin: 0">趋势</span>
            <el-radio-group v-model="range" size="small" @change="loadDetail">
              <el-radio-button value="1h">1 小时</el-radio-button>
              <el-radio-button value="6h">6 小时</el-radio-button>
              <el-radio-button value="24h">24 小时</el-radio-button>
            </el-radio-group>
          </div>
          <el-row :gutter="16">
            <el-col v-for="ch in detail.charts" :key="ch.name" :span="12">
              <el-card shadow="never" class="d-chart">
                <template #header>{{ ch.name }}</template>
                <MetricChart :series="[{ name: ch.name, data: ch.points as [number, number][], color: '#409eff', unit: ch.unit }]"
                  height="220px" :y-axis-name="ch.unit" />
              </el-card>
            </el-col>
          </el-row>
        </template>
      </template>
    </div>
  </el-drawer>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { k8sApi, type ControlPlaneSummary, type ControlPlaneDetail, type ControlCard } from '../api'
import MetricChart from '../components/MetricChart.vue'

const items = ref<ControlPlaneSummary[]>([])
const loading = ref(false)
const promIssue = ref('')

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
onMounted(load)

// ---- 详情 ----
const drawer = ref(false)
const detailLoading = ref(false)
const detail = ref<ControlPlaneDetail | null>(null)
const range = ref('6h')
let current = ''

async function openDetail(cp: ControlPlaneSummary) {
  current = cp.name
  range.value = '6h'
  detail.value = { ...cp, desc: '', range: '6h', charts: [] } as ControlPlaneDetail
  drawer.value = true
  await loadDetail()
}

async function loadDetail() {
  detailLoading.value = true
  try {
    const d = await k8sApi.controlPlaneDetail(current, range.value)
    if (d.name === current) detail.value = d
  } catch { /* http 层已 toast；保持已渲染的卡片不闪空 */ } finally {
    detailLoading.value = false
  }
}

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
  return `${Number(c.value).toFixed(c.dec)}${c.unit ? ' ' + c.unit : ''}`
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.dim { color: var(--el-text-color-secondary); }
.mono { font-family: Consolas, 'Courier New', monospace; }
.cp-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 12px; min-height: 120px; }
.cp-item { cursor: pointer; }
.cp-item:hover { border-color: var(--el-color-primary); }
.cp-head { display: flex; justify-content: space-between; align-items: center; }
.cp-name { font-weight: 600; font-size: 14px; }
.cp-sub { margin: 2px 0 8px; }
.cp-headline { margin-top: 4px; }
.cp-h-label { color: var(--el-text-color-secondary); }
.cp-h-value { font-size: 18px; font-weight: 600; }
.cp-health { margin-top: 6px; }
.cp-empty { color: var(--el-text-color-secondary); margin-top: 8px; }
.cp-empty-all { grid-column: 1 / -1; text-align: center; color: var(--el-text-color-secondary); padding: 40px 0; }
.txt-ok { color: var(--el-color-success); }
.txt-bad { color: var(--el-color-danger); }
.detail { min-height: 200px; }
.d-sub { display: flex; align-items: center; }
.d-desc { color: var(--el-text-color-secondary); margin: 8px 0 12px; }
.d-cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 10px; margin-bottom: 8px; }
.d-card { background: var(--el-fill-color-light); border-radius: 6px; padding: 10px 12px; }
.d-card-value { font-size: 17px; font-weight: 600; }
.d-toolbar { display: flex; justify-content: space-between; align-items: center; margin: 14px 0 10px; }
.sec-title { font-weight: 600; }
.d-chart { margin-bottom: 16px; }
</style>
