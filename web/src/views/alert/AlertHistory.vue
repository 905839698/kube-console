<template>
  <div v-loading="loading">
    <!-- 统计卡片 -->
    <el-row :gutter="16">
      <el-col :span="5"><el-card shadow="never" class="stat-card">
        <div class="stat-num">{{ stats.fired }}</div><div class="stat-label">{{ stats.days }} 天内触发</div></el-card></el-col>
      <el-col :span="5"><el-card shadow="never" class="stat-card">
        <div class="stat-num success">{{ stats.resolved }}</div><div class="stat-label">{{ stats.days }} 天内恢复</div></el-card></el-col>
      <el-col :span="5"><el-card shadow="never" class="stat-card" :class="{ 'stat-firing': stats.active > 0 }">
        <div class="stat-num" :class="{ danger: stats.active > 0 }">{{ stats.active }}</div><div class="stat-label">当前触发中</div></el-card></el-col>
      <el-col :span="5"><el-card shadow="never" class="stat-card">
        <div class="stat-num">{{ stats.avgDurationHours }}h</div><div class="stat-label">平均持续时长</div></el-card></el-col>
      <el-col :span="4"><el-card shadow="never" class="stat-card">
        <div class="stat-top">
          <div v-for="t in stats.top || []" :key="t.alertName" class="top-row">
            <span class="top-name" :title="t.alertName">{{ t.alertName }}</span>
            <span class="top-count">{{ t.count }}</span>
          </div>
          <span v-if="!stats.top?.length" class="muted">暂无数据</span>
        </div><div class="stat-label">Top 告警</div></el-card></el-col>
    </el-row>

    <el-alert v-if="syncErrs.length" type="warning" :closable="false" style="margin-top: 16px">
        <div v-for="e in syncErrs" :key="e.cluster" class="sync-err-row">
          归档同步异常（{{ e.cluster }}）：{{ e.err }} —— 该集群「当前触发中」可能不是最新
        </div>
      </el-alert>

      <el-card shadow="never" style="margin-top: 16px">
        <template #header>
          <div class="card-header">
            <div class="header-left">
              <span>告警历史（Alertmanager 轮询归档）</span>
              <span class="muted sub-note">恢复判定有 10 分钟宽限：告警刚恢复时仍会短暂显示为触发中</span>
            </div>
          <div class="header-right">
            <el-select v-model="days" style="width: 110px" @change="load">
              <el-option label="近 1 天" :value="1" />
              <el-option label="近 7 天" :value="7" />
              <el-option label="近 30 天" :value="30" />
              <el-option label="近 90 天" :value="90" />
            </el-select>
            <el-select v-model="stateFilter" style="width: 110px; margin-left: 12px" @change="load">
              <el-option label="全部状态" value="" />
              <el-option label="触发中" value="firing" />
              <el-option label="已恢复" value="resolved" />
            </el-select>
            <el-select v-model="clusterFilter" style="width: 150px; margin-left: 12px" @change="load">
              <el-option label="全部集群" value="" />
              <el-option v-for="c in clusterStore.clusters" :key="c.name" :label="c.name" :value="c.name" />
            </el-select>
            <el-input v-model="nameFilter" placeholder="搜索告警名" clearable style="width: 180px; margin-left: 12px" @keyup.enter="load">
              <template #prefix><el-icon><Search /></el-icon></template>
            </el-input>
            <el-button :icon="Refresh" circle @click="load" style="margin-left: 12px" />
          </div>
        </div>
      </template>

      <el-table border :data="items" size="default" stripe>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="row.state === 'firing' ? 'danger' : 'success'" size="small">
              {{ row.state === 'firing' ? '触发中' : '已恢复' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="cluster" label="集群" width="140" show-overflow-tooltip />
        <el-table-column prop="alertName" label="告警名称" min-width="200" show-overflow-tooltip />
        <el-table-column label="级别" width="90">
          <template #default="{ row }">
            <el-tag :type="row.severity === 'critical' ? 'danger' : row.severity === 'warning' ? 'warning' : 'info'"
              size="small" effect="plain">{{ sevLabel(row.severity) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="命名空间" width="140" show-overflow-tooltip>
          <template #default="{ row }">{{ row.namespace || '集群级' }}</template>
        </el-table-column>
        <el-table-column label="触发时间" width="170">
          <template #default="{ row }">{{ fmtTime(row.startedAt) }}</template>
        </el-table-column>
        <el-table-column label="恢复时间" width="170">
          <template #default="{ row }">{{ row.resolvedAt ? fmtTime(row.resolvedAt) : '—' }}</template>
        </el-table-column>
        <el-table-column label="持续" width="100">
          <template #default="{ row }">{{ fmtDuration(row.startedAt, row.resolvedAt) }}</template>
        </el-table-column>
      </el-table>

      <div class="pager">
        <el-pagination v-model:current-page="page" :page-size="size" :total="total"
          layout="total, prev, pager, next, sizes" :page-sizes="[20, 50, 100]" background small
          @current-change="load" @update:page-size="onSizeChange" />
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { Search, Refresh } from '@element-plus/icons-vue'
import { alertEventApi, type AlertEventItem, type AlertEventStatsItem } from '../../api'
import { useClusterStore } from '../../store/cluster'

const clusterStore = useClusterStore()
const items = ref<AlertEventItem[]>([])
const stats = ref<AlertEventStatsItem>({ days: 7, fired: 0, resolved: 0, active: 0, avgDurationHours: 0, top: [] })
// 归档轮询异常（cluster -> 最近错误）：有错误时「当前触发中」会静默过期，必须显式提示
const syncErrs = ref<{ cluster: string; err: string }[]>([])
const loading = ref(false)
const days = ref(7)
const stateFilter = ref('')
const clusterFilter = ref('')
const nameFilter = ref('')
const page = ref(1)
const size = ref(20)
const total = ref(0)

// 每页条数变化：EP 2.14 无 size 监听时下拉选择直接失效（弹回原值）
function onSizeChange(sz: number) {
  size.value = sz
  page.value = 1
  load()
}

async function load() {
  loading.value = true
  try {
    const [list, stat, sync] = await Promise.all([
      alertEventApi.list({
        state: stateFilter.value || undefined,
        cluster: clusterFilter.value || undefined,
        name: nameFilter.value || undefined,
        days: days.value,
        page: page.value,
        size: size.value,
      }),
      alertEventApi.stats(days.value),
      alertEventApi.syncStatus().catch(() => null),
    ])
    items.value = list.items || []
    total.value = list.total
    stats.value = stat
    syncErrs.value = sync
      ? Object.entries(sync.status || {}).filter(([, e]) => e).map(([cluster, err]) => ({ cluster, err }))
      : []
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

function sevLabel(s?: string) {
  return s === 'critical' ? '严重' : s === 'warning' ? '警告' : s ? s : '无'
}
function fmtTime(ts?: string | null): string {
  if (!ts) return '—'
  const d = new Date(ts)
  if (isNaN(d.getTime())) return ts
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}
function fmtDuration(start: string, end?: string | null): string {
  if (!end) return '—'
  const sec = Math.max(0, (new Date(end).getTime() - new Date(start).getTime()) / 1000)
  if (sec < 60) return `${Math.floor(sec)}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m`
  if (sec < 86400) return `${(sec / 3600).toFixed(1)}h`
  return `${(sec / 86400).toFixed(1)}d`
}

onMounted(load)
</script>

<style scoped>
.stat-card { text-align: center; }
.stat-num { font-size: 30px; font-weight: 700; line-height: 1.2; }
.stat-num.danger { color: #f56c6c; }
.stat-num.success { color: #67c23a; }
.stat-label { color: #909399; font-size: 13px; margin-top: 6px; }
.stat-card.stat-firing { border-color: #f56c6c; }
.stat-top { min-height: 36px; display: flex; flex-direction: column; justify-content: center; }
.top-row { display: flex; justify-content: space-between; font-size: 12px; line-height: 1.7; }
.top-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 90px; }
.top-count { color: #909399; }
.muted { color: #c0c4cc; font-size: 12px; }
.sub-note { margin-left: 8px; }
.sync-err-row { font-size: 12px; line-height: 1.8; }
.card-header { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 8px; }
.header-left { display: flex; align-items: center; gap: 12px; }
.header-right { display: flex; align-items: center; flex-wrap: wrap; }
.pager { margin-top: 14px; display: flex; justify-content: flex-end; }
</style>
