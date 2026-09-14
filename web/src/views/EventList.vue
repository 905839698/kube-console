<template>
  <div>
    <div class="toolbar">
      <el-radio-group v-model="mode" size="small">
        <el-radio-button value="live">实时（近 1h）</el-radio-button>
        <el-radio-button value="archive">归档检索（ES）</el-radio-button>
      </el-radio-group>
      <template v-if="mode === 'live'">
        <NamespaceSelect v-model="nsModel" multiple size="small" width="260px" />
        <el-select v-model="typeFilter" style="width: 130px" @change="load">
          <el-option label="全部类型" value="" />
          <el-option label="Warning（异常）" value="Warning" />
          <el-option label="Normal（常规）" value="Normal" />
        </el-select>
        <el-input v-model="keyword" placeholder="搜索对象 / 原因 / 消息" clearable style="width: 240px" @keyup.enter="load" />
        <el-button type="primary" plain @click="load">查询</el-button>
        <el-button :icon="Refresh" circle @click="load" />
        <el-switch v-model="auto" active-text="自动刷新" style="margin-left: 8px" />
        <span v-if="lastLoaded" class="updated">更新于 {{ lastLoaded }}</span>
      </template>
      <template v-else>
        <NamespaceSelect v-model="nsModel" multiple size="small" width="220px" />
        <el-select v-model="archiveType" style="width: 130px">
          <el-option label="全部类型" value="" />
          <el-option label="Warning（异常）" value="Warning" />
          <el-option label="Normal（常规）" value="Normal" />
        </el-select>
        <el-input v-model="archiveReason" placeholder="原因，如 OOMKilled" clearable style="width: 160px" />
        <el-input v-model="archiveKeyword" placeholder="搜索消息 / 原因 / 对象" clearable style="width: 220px" @keyup.enter="loadArchive(1)" />
        <el-date-picker v-model="archiveRange" type="datetimerange" start-placeholder="开始" end-placeholder="结束"
          style="width: 340px" :shortcuts="SHORTCUTS" value-format="x" />
        <el-button type="primary" plain :loading="loading" @click="loadArchive(1)">检索归档</el-button>
        <el-button v-if="userStore.isAdmin" @click="cfgVisible = true">日志源配置</el-button>
        <span class="updated" v-if="archiveTotal >= 0">共 {{ archiveTotal }} 条 · {{ archiveTook }}ms</span>
      </template>
    </div>

    <!-- 实时视图 -->
    <template v-if="mode === 'live'">
      <el-table border :data="items" v-loading="loading" size="small" :default-sort="{ prop: 'lastAt', order: 'descending' }">
        <el-table-column label="类型" width="100">
          <template #default="{ row }">
            <el-tag :type="row.type === 'Warning' ? 'danger' : 'info'" size="small">{{ row.type === 'Warning' ? 'Warning' : 'Normal' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="reason" label="原因" width="130" />
        <el-table-column prop="object" label="对象" min-width="200" show-overflow-tooltip />
        <el-table-column prop="message" label="消息" min-width="380" show-overflow-tooltip />
        <el-table-column prop="namespace" label="命名空间" width="130" show-overflow-tooltip />
        <el-table-column prop="count" label="次数" width="70" align="center" sortable />
        <el-table-column prop="lastAt" label="最后发生" width="170" sortable />
      </el-table>
      <div v-if="!loading && !items.length" class="empty">当前过滤条件下没有事件</div>
      <el-alert type="info" :closable="false" style="margin-top: 12px"
        title="K8s 事件默认约 1 小时过期，更早的事件请在「归档检索」中查询（需管理员开启事件归档）。" />
    </template>

    <!-- 归档视图 -->
    <template v-else>
      <el-table border :data="archiveItems" v-loading="loading" size="small">
        <el-table-column label="类型" width="100">
          <template #default="{ row }">
            <el-tag :type="row.type === 'Warning' ? 'danger' : 'info'" size="small">{{ row.type === 'Warning' ? 'Warning' : 'Normal' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="reason" label="原因" width="130" />
        <el-table-column prop="object" label="对象" min-width="200" show-overflow-tooltip />
        <el-table-column prop="message" label="消息" min-width="380" show-overflow-tooltip />
        <el-table-column prop="namespace" label="命名空间" width="130" show-overflow-tooltip />
        <el-table-column prop="count" label="次数" width="70" align="center" sortable />
        <el-table-column label="最后发生" width="170">
          <template #default="{ row }">{{ fmtTime(row.lastAt) }}</template>
        </el-table-column>
      </el-table>
      <el-empty v-if="!loading && !archiveItems.length && archiveSearched" description="归档中无匹配事件（需管理员在日志源配置中开启「事件归档」）" :image-size="60" />
      <div class="pager" v-if="archiveTotal > archiveSize">
        <el-pagination v-model:current-page="archivePage" :page-size="archiveSize" :total="archiveTotal"
          layout="total, prev, pager, next" background small @current-change="loadArchive" />
      </div>
    </template>

    <!-- 日志源 / 事件归档配置（管理员） -->
    <LogSourceDialog v-model="cfgVisible" title="日志源 / 事件归档配置（Elasticsearch）" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { eventsApi, eventsApiArchive, type EventItemEx, type EventArchiveHitItem } from '../api'
import { useNamespaceStore } from '../store/namespace'
import { useUserStore } from '../store/user'
import { nsParam } from '../api'
import { useAutoRefresh } from '../composables/useAutoRefresh'
import NamespaceSelect from '../components/NamespaceSelect.vue'
import LogSourceDialog from '../components/LogSourceDialog.vue'

const userStore = useUserStore()
const nsStore = useNamespaceStore()
const nsModel = computed({
  get: () => nsStore.selected,
  set: (v) => nsStore.select(v as string[]),
})

const mode = ref<'live' | 'archive'>('live')
const items = ref<EventItemEx[]>([])
const loading = ref(false)
const typeFilter = ref('')
const keyword = ref('')
const auto = ref(false)
const lastLoaded = ref('')
// 自动刷新（30s）：页面隐藏时自动暂停，回前台立即刷新
useAutoRefresh(() => { if (auto.value && mode.value === 'live') load() }, 30000, auto)

async function load() {
  loading.value = true
  try {
    items.value = await eventsApi.list(nsParam(nsStore.selected), typeFilter.value, keyword.value.trim())
    lastLoaded.value = new Date().toLocaleTimeString()
  } finally {
    loading.value = false
  }
}

// ------------------- 归档检索 -------------------
const archiveType = ref('')
const archiveReason = ref('')
const archiveKeyword = ref('')
const archiveRange = ref<[number, number] | null>(null)
const archiveItems = ref<EventArchiveHitItem[]>([])
const archivePage = ref(1)
const archiveSize = 50
const archiveTotal = ref(-1)
const archiveTook = ref(0)
const archiveSearched = ref(false)

const SHORTCUTS = [
  { text: '最近 1 小时', value: () => { const e = new Date(); return [new Date(e.getTime() - 3600e3), e] } },
  { text: '最近 24 小时', value: () => { const e = new Date(); return [new Date(e.getTime() - 86400e3), e] } },
  { text: '最近 7 天', value: () => { const e = new Date(); return [new Date(e.getTime() - 7 * 86400e3), e] } },
  { text: '最近 30 天', value: () => { const e = new Date(); return [new Date(e.getTime() - 30 * 86400e3), e] } },
]

async function loadArchive(p = 1) {
  loading.value = true
  archiveSearched.value = true
  archivePage.value = p
  try {
    const [start, end] = archiveRange.value || [Date.now() - 86400e3, Date.now()]
    const out = await eventsApiArchive.search({
      namespace: nsParam(nsStore.selected) || undefined,
      type: archiveType.value || undefined,
      reason: archiveReason.value || undefined,
      keyword: archiveKeyword.value || undefined,
      start: String(start),
      end: String(end),
      page: p,
      size: archiveSize,
    })
    archiveItems.value = out.items || []
    archiveTotal.value = out.total
    archiveTook.value = out.took
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

function fmtTime(ts?: string): string {
  if (!ts) return '—'
  const d = new Date(ts)
  if (isNaN(d.getTime())) return ts
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

onMounted(load)
</script>

<style scoped>
.toolbar { display: flex; gap: 8px; margin-bottom: 12px; align-items: center; flex-wrap: wrap; }
.updated { color: #909399; font-size: 12px; }
.empty { text-align: center; color: #909399; padding: 40px 0; }
.pager { margin-top: 12px; display: flex; justify-content: flex-end; }
</style>
