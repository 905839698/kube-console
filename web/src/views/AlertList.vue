<template>
  <div v-loading="loading">
    <!-- 统计卡片 -->
    <el-row :gutter="16">
      <el-col :span="6">
        <el-card shadow="never" class="stat-card" :class="{ 'stat-firing': s.firing > 0 }">
          <div class="stat-num" :class="{ danger: s.firing > 0 }">{{ s.firing }}</div>
          <div class="stat-label">触发中 (Firing)</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="never" class="stat-card">
          <div class="stat-num warning">{{ s.pending }}</div>
          <div class="stat-label">待触发 (Pending)</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="never" class="stat-card">
          <div class="stat-num info">{{ s.inactive }}</div>
          <div class="stat-label">正常 (Inactive)</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="never" class="stat-card">
          <div class="stat-num">{{ s.total }}</div>
          <div class="stat-label">
            规则总数
            <el-tag v-for="(n, k) in s.bySeverity" :key="k" size="small" :type="sevTag(k)" class="sev-tag">{{ sevLabel(k) }} {{ n }}</el-tag>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <el-tabs v-model="tab" class="alert-tabs">
      <!-- 告警规则（原展示页） -->
      <el-tab-pane label="告警规则" name="rules">
    <!-- 列表 -->
    <el-card shadow="never" style="margin-top: 16px">
      <template #header>
        <div class="card-header">
          <div class="header-left">
            <span>告警规则</span>
            <el-tag size="small" type="info" class="eval-tag">最近评估 {{ fmtTime(data?.lastEval) }}</el-tag>
          </div>
          <div class="header-right">
            <el-radio-group v-model="stateFilter" size="default">
              <el-radio-button value="all">全部</el-radio-button>
              <el-radio-button value="firing">触发中</el-radio-button>
              <el-radio-button value="pending">待触发</el-radio-button>
              <el-radio-button value="inactive">正常</el-radio-button>
            </el-radio-group>
            <el-select v-model="sevFilter" placeholder="严重级别" clearable style="width: 130px; margin-left: 12px">
              <el-option label="严重 critical" value="critical" />
              <el-option label="警告 warning" value="warning" />
              <el-option label="信息 info" value="info" />
              <el-option label="无级别 none" value="none" />
            </el-select>
            <el-input v-model="search" placeholder="搜索名称 / 描述 / 命名空间" clearable style="width: 240px; margin-left: 12px">
              <template #prefix><el-icon><Search /></el-icon></template>
            </el-input>
            <el-button :icon="Refresh" circle @click="load" style="margin-left: 12px" />
          </div>
        </div>
      </template>

      <el-table :data="paged" size="default" stripe :default-sort="{ prop: 'state', order: 'ascending' }">
        <el-table-column label="状态" width="100" sortable :sort-method="stateSort">
          <template #default="{ row }">
            <el-tag :type="stateTag(row.state)" size="small">{{ stateLabel(row.state) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="严重级别" width="110">
          <template #default="{ row }">
            <el-tag :type="sevTag(row.severity)" size="small" effect="plain">{{ sevLabel(row.severity) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="alertName" label="告警名称" min-width="220">
          <template #default="{ row }">
            <div class="alert-name">
              <el-icon v-if="row.state === 'firing'" class="fire-icon"><AlarmClock /></el-icon>
              {{ row.alertName }}
            </div>
            <div class="alert-group">{{ row.group }}</div>
          </template>
        </el-table-column>
        <el-table-column prop="summary" label="摘要" min-width="320" show-overflow-tooltip>
          <template #default="{ row }">
            <span>{{ row.summary || '—' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="命名空间" width="160" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="row.namespace">{{ row.namespace }}</span>
            <span v-else class="muted">集群级</span>
          </template>
        </el-table-column>
        <el-table-column label="实例" width="90" align="center">
          <template #default="{ row }">
            <el-tag v-if="row.instances?.length" :type="row.state === 'firing' ? 'danger' : 'info'" size="small" effect="plain">{{ row.instances.length }}</el-tag>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
        <el-table-column label="持续要求" width="110" align="center">
          <template #default="{ row }">
            <span class="muted">{{ fmtDuration(row.duration) }}</span>
          </template>
        </el-table-column>

        <template #expand="{ row }">
          <div class="expand-body">
            <el-descriptions :column="1" border size="small" v-if="row.detail || row.runbook || row.query">
              <el-descriptions-item v-if="row.detail" label="描述">
                <div class="desc-text">{{ row.detail }}</div>
              </el-descriptions-item>
              <el-descriptions-item label="PromQL">
                <code class="promql">{{ row.query }}</code>
              </el-descriptions-item>
              <el-descriptions-item v-if="row.runbook" label="Runbook">
                <a :href="row.runbook" target="_blank" rel="noopener">{{ row.runbook }}</a>
              </el-descriptions-item>
            </el-descriptions>

            <div v-if="row.instances?.length" class="inst-section">
              <div class="inst-title">活动实例（{{ row.instances.length }}）</div>
              <el-table :data="row.instances" size="small" border>
                <el-table-column label="命名空间" min-width="140">
                  <template #default="{ row: i }">{{ i.labels?.namespace || '—' }}</template>
                </el-table-column>
                <el-table-column label="Pod / 节点" min-width="200">
                  <template #default="{ row: i }">
                    {{ i.labels?.pod || i.labels?.node || i.labels?.instance || '—' }}
                  </template>
                </el-table-column>
                <el-table-column label="触发时间" width="190">
                  <template #default="{ row: i }">{{ fmtTime(i.started) }}</template>
                </el-table-column>
                <el-table-column label="值" width="120">
                  <template #default="{ row: i }">
                    <span class="muted">{{ i.value || '—' }}</span>
                  </template>
                </el-table-column>
              </el-table>
            </div>
          </div>
        </template>
      </el-table>

      <div class="pager">
        <el-pagination
          v-model:current-page="page"
          :page-size="pageSize"
          :total="filtered.length"
          layout="total, prev, pager, next"
          background
          small
        />
      </div>
    </el-card>
      </el-tab-pane>

      <!-- 告警分组 -->
      <el-tab-pane :label="`告警分组 (${data?.groups ?? 0})`" name="groups">
        <AlertGroups :data="data" @view-rules="onViewGroupRules" />
      </el-tab-pane>

      <!-- 规则检查 -->
      <el-tab-pane label="规则检查" name="lint">
        <AlertLint :rules="data?.rules || []" />
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { Search, Refresh, AlarmClock } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { k8sApi } from '../api'
import { useClusterStore } from '../store/cluster'
import AlertGroups from './alert/AlertGroups.vue'
import AlertLint from './alert/AlertLint.vue'
import type { AlertRulesResponse, AlertRule, AlertSummary } from '../api'

const clusterStore = useClusterStore()
const data = ref<AlertRulesResponse>()
const loading = ref(false)
const tab = ref<'rules' | 'groups' | 'lint'>('rules')
const stateFilter = ref<'all' | 'firing' | 'pending' | 'inactive'>('all')
const sevFilter = ref('')
const search = ref('')
const page = ref(1)
const pageSize = 20

// 从分组页跳转：把该分组的规则作为搜索关键词带到规则页
function onViewGroupRules(group: string) {
  search.value = group
  stateFilter.value = 'all'
  sevFilter.value = ''
  page.value = 1
  tab.value = 'rules'
}

const s = computed<AlertSummary>(() => data.value?.summary ?? { firing: 0, pending: 0, inactive: 0, total: 0, bySeverity: {} })

const filtered = computed<AlertRule[]>(() => {
  const list = data.value?.rules || []
  const kw = search.value.trim().toLowerCase()
  return list.filter((r) => {
    if (stateFilter.value !== 'all' && r.state !== stateFilter.value) return false
    if (sevFilter.value && r.severity !== sevFilter.value) return false
    if (kw) {
      const hay = `${r.alertName} ${r.summary} ${r.namespace} ${r.group}`.toLowerCase()
      if (!hay.includes(kw)) return false
    }
    return true
  })
})

const paged = computed(() => {
  const start = (page.value - 1) * pageSize
  return filtered.value.slice(start, start + pageSize)
})

// 排序权重：先按状态（firing 置顶），再按严重级别
function stateSort(a: AlertRule, b: AlertRule): number {
  const stateW = { firing: 0, pending: 1, inactive: 2 } as const
  const sevW = { critical: 0, warning: 1, info: 2, none: 3 } as const
  const d = (stateW[a.state] ?? 9) - (stateW[b.state] ?? 9)
  if (d !== 0) return d
  return (sevW[a.severity] ?? 9) - (sevW[b.severity] ?? 9)
}

function stateTag(s: string) {
  return s === 'firing' ? 'danger' : s === 'pending' ? 'warning' : 'success'
}
function stateLabel(s: string) {
  return s === 'firing' ? '触发中' : s === 'pending' ? '待触发' : '正常'
}
function sevTag(s: string) {
  return s === 'critical' ? 'danger' : s === 'warning' ? 'warning' : s === 'info' ? 'info' : 'info'
}
function sevLabel(s: string) {
  return s === 'critical' ? '严重' : s === 'warning' ? '警告' : s === 'info' ? '信息' : '无'
}
function fmtDuration(sec: number): string {
  if (!sec || sec <= 0) return '即时'
  if (sec < 60) return `${sec}s`
  if (sec < 3600) return `${Math.round(sec / 60)}m`
  return `${(sec / 3600).toFixed(1)}h`
}
function fmtTime(ts?: string): string {
  if (!ts) return '—'
  const d = new Date(ts)
  if (isNaN(d.getTime())) return ts
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

async function load() {
  if (!clusterStore.current) {
    ElMessage.warning('请先选择集群')
    return
  }
  loading.value = true
  try {
    data.value = await k8sApi.monitorAlerts()
    page.value = 1
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.alert-tabs { margin-top: 4px; }
:deep(.el-tabs__content) { }
.stat-card { text-align: center; }
.stat-num { font-size: 30px; font-weight: 700; line-height: 1.2; }
.stat-num.danger { color: #f56c6c; }
.stat-num.warning { color: #e6a23c; }
.stat-num.info { color: #909399; }
.stat-label { color: #909399; font-size: 13px; margin-top: 6px; }
.stat-card.stat-firing { border-color: #f56c6c; }
.sev-tag { margin-left: 6px; }

.card-header { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 8px; }
.header-left { display: flex; align-items: center; gap: 12px; }
.header-right { display: flex; align-items: center; flex-wrap: wrap; }
.eval-tag { margin-left: 4px; }

.alert-name { font-weight: 600; display: flex; align-items: center; gap: 6px; }
.fire-icon { color: #f56c6c; }
.alert-group { color: #c0c4cc; font-size: 12px; margin-top: 2px; }
.muted { color: #c0c4cc; }

.expand-body { padding: 12px 16px; }
.desc-text { white-space: pre-wrap; line-height: 1.6; }
.promql { background: #f5f7fa; padding: 4px 8px; border-radius: 4px; font-family: monospace; font-size: 12px; word-break: break-all; display: block; }
.inst-section { margin-top: 14px; }
.inst-title { font-weight: 600; margin-bottom: 8px; font-size: 13px; }

.pager { margin-top: 14px; display: flex; justify-content: flex-end; }
</style>
