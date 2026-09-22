<template>
  <div>
    <!-- 未配置 / 不可用降级 -->
    <el-empty v-if="st && !st.configured" description="当前集群尚未接入 Alertmanager">
      <div class="empty-actions">
        <el-button v-if="userStore.isAdmin" type="primary" @click="configDlg = true">接入配置</el-button>
        <span v-else class="muted">请联系管理员在告警页完成接入配置</span>
      </div>
    </el-empty>
    <template v-else>
      <el-alert v-if="st && !st.ok" type="error" :closable="false" style="margin-bottom: 12px"
        :title="`Alertmanager 连接异常：${st.error || '连通测试失败'}（规则视图仍走 Prometheus，不受影响）`" />

      <!-- 统计 -->
      <el-row :gutter="16">
        <el-col :span="6"><el-card shadow="never" class="stat-card" :class="{ 'stat-firing': stats.firing > 0 }">
          <div class="stat-num" :class="{ danger: stats.firing > 0 }">{{ stats.firing }}</div>
          <div class="stat-label">触发中</div></el-card></el-col>
        <el-col :span="6"><el-card shadow="never" class="stat-card">
          <div class="stat-num warning">{{ stats.suppressed }}</div>
          <div class="stat-label">已静默/抑制</div></el-card></el-col>
        <el-col :span="6"><el-card shadow="never" class="stat-card">
          <div class="stat-num">{{ stats.critical }}</div>
          <div class="stat-label">严重级别</div></el-card></el-col>
        <el-col :span="6"><el-card shadow="never" class="stat-card">
          <div class="stat-num">{{ filtered.length }}</div>
          <div class="stat-label">活动告警总数</div></el-card></el-col>
      </el-row>

      <el-card shadow="never" style="margin-top: 16px">
        <template #header>
          <div class="card-header">
            <div class="header-left">
              <span>实时告警（Alertmanager）</span>
              <el-tag size="small" type="info" class="eval-tag">30s 自动刷新</el-tag>
            </div>
            <div class="header-right">
              <el-input v-model="search" placeholder="搜索名称 / 摘要 / 标签" clearable style="width: 220px">
                <template #prefix><el-icon><Search /></el-icon></template>
              </el-input>
              <el-button :icon="Refresh" circle @click="load" style="margin-left: 12px" />
              <el-button v-if="userStore.isAdmin" @click="configDlg = true">Alertmanager 配置</el-button>
            </div>
          </div>
        </template>

        <el-table border :data="paged" size="default" stripe v-loading="loading" :default-sort="{ prop: 'startsAt', order: 'ascending' }">
          <el-table-column label="状态" width="110">
            <template #default="{ row }">
              <el-tag :type="row.status.state === 'suppressed' ? 'info' : 'danger'" size="small">
                {{ row.status.state === 'suppressed' ? '已静默' : '触发中' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="严重级别" width="110">
            <template #default="{ row }">
              <el-tag :type="sevTag(row.labels?.severity)" size="small" effect="plain">{{ sevLabel(row.labels?.severity) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="告警名称" min-width="200">
            <template #default="{ row }">
              <div class="alert-name"><el-icon class="fire-icon"><AlarmClock /></el-icon>{{ row.labels?.alertname || row.fingerprint }}</div>
              <div class="muted sub">{{ row.labels?.namespace || '集群级' }}</div>
            </template>
          </el-table-column>
          <el-table-column label="摘要 / 描述" min-width="300" show-overflow-tooltip>
            <template #default="{ row }">{{ row.annotations?.summary || row.annotations?.description || '—' }}</template>
          </el-table-column>
          <el-table-column label="实例" min-width="160" show-overflow-tooltip>
            <template #default="{ row }">{{ row.labels?.pod || row.labels?.node || row.labels?.instance || '—' }}</template>
          </el-table-column>
          <el-table-column label="开始时间" width="170">
            <template #default="{ row }">{{ fmtTime(row.startsAt) }}</template>
          </el-table-column>
          <el-table-column label="持续" width="100" sortable :sort-method="durSort">
            <template #default="{ row }">{{ fmtSince(row.startsAt) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="80" fixed="right" align="center">
            <template #default="{ row }">
              <el-button link type="warning" size="small" @click="openSilence(row)">静默</el-button>
            </template>
          </el-table-column>
        </el-table>

        <div class="pager">
          <el-pagination v-model:current-page="page" :page-size="pageSize" :total="filtered.length"
            layout="total, prev, pager, next" background small />
        </div>
      </el-card>
    </template>

    <!-- 静默创建 -->
    <el-dialog v-model="silenceDlg" title="创建静默" width="560px">
      <el-form label-width="110px">
        <el-form-item label="匹配器">
          <div class="matcher-row">
            <el-tag size="small">alertname</el-tag>
            <el-select v-model="silence.op" style="width: 90px">
              <el-option label="=" value="eq" />
              <el-option label="=~" value="re" />
            </el-select>
            <el-input v-model="silence.alertname" placeholder="告警名（支持正则）" style="flex: 1" />
          </div>
          <div class="matcher-row">
            <el-tag size="small" type="info">severity</el-tag>
            <el-select v-model="silence.severity" clearable placeholder="不限" style="width: 140px">
              <el-option label="critical" value="critical" />
              <el-option label="warning" value="warning" />
              <el-option label="info" value="info" />
            </el-select>
            <el-tag size="small" type="info" style="margin-left: 12px">namespace</el-tag>
            <el-input v-model="silence.namespace" clearable placeholder="命名空间（可选）" style="width: 180px" />
          </div>
        </el-form-item>
        <el-form-item label="静默时长">
          <el-select v-model="silence.duration" style="width: 200px">
            <el-option v-for="d in DURATIONS" :key="d.v" :label="d.l" :value="d.v" />
            <el-option label="自定义（小时）" value="custom" />
          </el-select>
          <el-input-number v-if="silence.duration === 'custom'" v-model="silence.customHours" :min="1" :max="8760"
            style="margin-left: 12px; width: 140px" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="silence.comment" type="textarea" :rows="2" placeholder="静默原因（显示在 AM 静默列表）" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="silenceDlg = false">取消</el-button>
        <el-button type="primary" :loading="silencing" @click="submitSilence">创建静默</el-button>
      </template>
    </el-dialog>

    <!-- AM 配置 -->
    <AmConfigDialog v-if="configDlg" v-model="configDlg" @saved="loadStatus" />
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, watch } from 'vue'
import { Search, Refresh, AlarmClock } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { amApi, type AMAlertItem, type AMStatusItem } from '../../api'
import { useClusterStore } from '../../store/cluster'
import { useUserStore } from '../../store/user'
import { useAutoRefresh } from '../../composables/useAutoRefresh'
import AmConfigDialog from './AmConfigDialog.vue'

const clusterStore = useClusterStore()
const userStore = useUserStore()
const st = ref<AMStatusItem>()
const items = ref<AMAlertItem[]>([])
const loading = ref(false)
const search = ref('')
const page = ref(1)
const pageSize = 20
const configDlg = ref(false)

const DURATIONS = [
  { l: '1 小时', v: '1h' }, { l: '4 小时', v: '4h' }, { l: '8 小时', v: '8h' },
  { l: '1 天', v: '1d' }, { l: '3 天', v: '3d' }, { l: '1 周', v: '1w' },
]

const filtered = computed(() => {
  const kw = search.value.trim().toLowerCase()
  if (!kw) return items.value
  return items.value.filter((a) =>
    `${a.labels?.alertname || ''} ${a.annotations?.summary || ''} ${a.labels?.namespace || ''} ${JSON.stringify(a.labels)}`
      .toLowerCase()
      .includes(kw),
  )
})

// 统计卡与「活动告警总数」同源于 filtered：
// 旧实现 firing/suppressed/critical 用未过滤的 items、总数用 filtered，
// 一搜索就出现「触发中 18 / 总数 5」的同页矛盾
const stats = computed(() => {
  const list = filtered.value
  return {
    firing: list.filter((a) => a.status.state !== 'suppressed').length,
    suppressed: list.filter((a) => a.status.state === 'suppressed').length,
    critical: list.filter((a) => a.labels?.severity === 'critical').length,
  }
})
const paged = computed(() => {
  const start = (page.value - 1) * pageSize
  return filtered.value.slice(start, start + pageSize)
})

// 持续时长排序（最早的置顶）
function durSort(a: AMAlertItem, b: AMAlertItem): number {
  const ta = new Date(a.startsAt || 0).getTime() || Number.MAX_SAFE_INTEGER
  const tb = new Date(b.startsAt || 0).getTime() || Number.MAX_SAFE_INTEGER
  return ta - tb
}

async function loadStatus() {
  try {
    st.value = await amApi.status()
  } catch {
    st.value = { configured: false, ok: false }
  }
}

async function load() {
  if (!clusterStore.current) return
  loading.value = true
  try {
    await loadStatus()
    if (st.value?.configured && st.value.ok) {
      items.value = (await amApi.alerts()) || []
    } else {
      items.value = []
    }
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}
useAutoRefresh(load, 30000)

// ------------------- 静默 -------------------
const silenceDlg = ref(false)
const silencing = ref(false)
const silence = reactive({ alertname: '', op: 'eq', severity: '', namespace: '', duration: '4h', customHours: 24, comment: '' })

function openSilence(row: AMAlertItem) {
  silence.alertname = row.labels?.alertname || ''
  silence.op = 'eq'
  silence.severity = row.labels?.severity || ''
  silence.namespace = row.labels?.namespace || ''
  silence.duration = '4h'
  silence.customHours = 24
  silence.comment = ''
  silenceDlg.value = true
}

function durToHours(v: string): number {
  const m: Record<string, number> = { '1h': 1, '4h': 4, '8h': 8, '1d': 24, '3d': 72, '1w': 168 }
  return m[v] ?? 4
}

async function submitSilence() {
  if (!silence.alertname.trim()) {
    ElMessage.warning('告警名不能为空')
    return
  }
  const matchers = [{
    name: 'alertname',
    value: silence.alertname.trim(),
    isRegex: silence.op === 're',
    isEqual: true,
  }]
  if (silence.severity) matchers.push({ name: 'severity', value: silence.severity, isRegex: false, isEqual: true })
  if (silence.namespace.trim()) matchers.push({ name: 'namespace', value: silence.namespace.trim(), isRegex: false, isEqual: true })
  const hours = silence.duration === 'custom' ? silence.customHours : durToHours(silence.duration)
  const now = new Date()
  const endsAt = new Date(now.getTime() + hours * 3600 * 1000)
  silencing.value = true
  try {
    await amApi.createSilence({
      matchers,
      startsAt: now.toISOString(),
      endsAt: endsAt.toISOString(),
      comment: silence.comment,
    })
    ElMessage.success(`已创建静默（${hours} 小时）`)
    silenceDlg.value = false
  } catch {
    /* 拦截器已提示 */
  } finally {
    silencing.value = false
  }
}

function sevTag(s?: string) {
  return s === 'critical' ? 'danger' : s === 'warning' ? 'warning' : 'info'
}
function sevLabel(s?: string) {
  return s === 'critical' ? '严重' : s === 'warning' ? '警告' : s ? s : '无'
}
function fmtTime(ts?: string): string {
  if (!ts || ts.startsWith('0001-')) return '—'
  const d = new Date(ts)
  if (isNaN(d.getTime())) return ts
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}
function fmtSince(ts?: string): string {
  if (!ts || ts.startsWith('0001-')) return '—'
  const t = new Date(ts).getTime()
  if (isNaN(t)) return '—'
  const sec = Math.max(0, Math.floor((Date.now() - t) / 1000))
  if (sec < 60) return `${sec}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m`
  if (sec < 86400) return `${Math.floor(sec / 3600)}h`
  return `${Math.floor(sec / 86400)}d`
}

watch(() => clusterStore.current, load)
onMounted(load)
</script>

<style scoped>
.empty-actions { display: flex; align-items: center; gap: 12px; }
.muted { color: #c0c4cc; }
.sub { font-size: 12px; margin-top: 2px; }
.stat-card { text-align: center; }
.stat-num { font-size: 30px; font-weight: 700; line-height: 1.2; }
.stat-num.danger { color: #f56c6c; }
.stat-num.warning { color: #e6a23c; }
.stat-label { color: #909399; font-size: 13px; margin-top: 6px; }
.stat-card.stat-firing { border-color: #f56c6c; }
.card-header { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 8px; }
.header-left { display: flex; align-items: center; gap: 12px; }
.header-right { display: flex; align-items: center; flex-wrap: wrap; }
.eval-tag { margin-left: 4px; }
.alert-name { font-weight: 600; display: flex; align-items: center; gap: 6px; }
.fire-icon { color: #f56c6c; }
.matcher-row { display: flex; align-items: center; gap: 8px; width: 100%; margin-bottom: 8px; }
.pager { margin-top: 14px; display: flex; justify-content: flex-end; }
</style>
