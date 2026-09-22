<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>日志检索（Elasticsearch 历史日志）</span>
        <div class="header-right">
          <el-button :icon="Refresh" circle size="small" @click="doSearch(1)" />
          <el-button v-if="userStore.isAdmin" size="small" @click="cfgVisible = true">日志源配置</el-button>
        </div>
      </div>
    </template>

    <el-form inline size="default" @submit.prevent="doSearch(1)">
      <el-form-item label="命名空间">
        <el-select v-model="q.namespace" clearable filterable placeholder="全部" style="width: 180px">
          <el-option v-for="n in nsOptions" :key="n" :label="n" :value="n" />
        </el-select>
      </el-form-item>
      <el-form-item label="Pod">
        <el-input v-model="q.pod" placeholder="Pod 名称（可选）" clearable style="width: 190px" />
      </el-form-item>
      <el-form-item label="关键字">
        <el-input v-model="q.keyword" placeholder="如 TimeoutException" clearable style="width: 220px" @keyup.enter="doSearch(1)" />
      </el-form-item>
      <el-form-item label="级别">
        <el-select v-model="q.level" clearable placeholder="全部" style="width: 110px">
          <el-option v-for="l in ['error', 'warn', 'info']" :key="l" :label="l" :value="l" />
        </el-select>
      </el-form-item>
      <el-form-item label="来源">
        <el-select v-model="q.source" style="width: 130px">
          <el-option label="全部" value="" />
          <el-option label="标准输出" value="stdout" />
          <el-option label="文本文件" value="file" />
          <el-option label="JSON 采集" value="json" />
        </el-select>
      </el-form-item>
      <el-form-item label="时间范围">
        <el-select v-model="q.minutes" style="width: 120px">
          <el-option v-for="m in [15, 60, 360, 1440, 4320, 10080]" :key="m" :label="fmtRange(m)" :value="m" />
          <el-option label="自定义…" :value="-1" />
        </el-select>
      </el-form-item>
      <el-form-item v-if="q.minutes === -1" label="起止时间">
        <el-date-picker
          v-model="range"
          type="datetimerange"
          range-separator="至"
          start-placeholder="开始时间"
          end-placeholder="结束时间"
          :shortcuts="rangeShortcuts"
          style="width: 380px"
        />
      </el-form-item>
      <el-form-item>
        <el-button type="primary" :loading="loading" @click="doSearch(1)">检索</el-button>
      </el-form-item>
    </el-form>

    <div v-if="result" class="result-layout">
      <aside class="facet" v-if="result.podFacet.length">
        <div class="facet-title">Pod 分布 (Top)</div>
        <div
          v-for="f in result.podFacet"
          :key="f.pod"
          class="facet-row"
          :class="{ active: q.pod === f.pod }"
          @click="togglePod(f.pod)"
        >
          <span class="facet-name">{{ f.pod }}</span>
          <el-tag size="small" type="info">{{ f.count }}</el-tag>
        </div>
      </aside>
      <div class="logs">
        <div class="logs-meta">
          共 {{ result.total }} 条 · {{ result.took }}ms
          <el-button size="small" text type="primary" @click="downloadCsv" :disabled="!result.items.length">导出 CSV</el-button>
        </div>
        <div v-loading="loading" class="log-list">
          <div v-for="(h, i) in result.items" :key="i" class="log-row">
            <span class="log-time">{{ fmtTime(h.timestamp) }}</span>
            <el-tag v-if="isErr(h.level)" size="small" type="danger">ERROR</el-tag>
            <el-tag v-else-if="isWarn(h.level)" size="small" type="warning">WARN</el-tag>
            <span class="log-pod" :title="h.namespace + '/' + h.pod">{{ h.pod }}</span>
            <pre class="log-msg">{{ h.message }}</pre>
          </div>
          <el-empty v-if="!loading && !result.items.length" description="无匹配日志" :image-size="60" />
        </div>
        <el-pagination
          v-model:current-page="page"
          :page-size="q.size || 50"
          :total="result.total"
          layout="prev, pager, next, sizes"
          :page-sizes="[50, 100, 200]"
          background
          style="margin-top: 12px; justify-content: flex-end"
          @current-change="doSearch(page)"
          @size-change="(s: number) => { q.size = s; doSearch(1) }"
        />
      </div>
    </div>
    <el-empty v-else description="输入条件后检索（依赖 fluentd→ES 日志链路）" />

    <!-- 日志源配置（管理员，与事件归档共用） -->
    <LogSourceDialog v-model="cfgVisible" title="日志源配置（Elasticsearch）" />
  </el-card>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { logsApi, k8sApi, type LogSearchResult } from '../api'
import { useClusterStore } from '../store/cluster'
import { useUserStore } from '../store/user'
import LogSourceDialog from '../components/LogSourceDialog.vue'

const userStore = useUserStore()
const clusterStore = useClusterStore()
const route = useRoute()

const nsOptions = ref<string[]>([])
const loading = ref(false)
const page = ref(1)
const result = ref<LogSearchResult>()
const q = reactive<{ namespace: string; pod: string; container: string; source: string; keyword: string; level: string; minutes: number; size: number }>({
  namespace: '', pod: '', container: '', source: '', keyword: '', level: '', minutes: 360, size: 50,
})

const cfgVisible = ref(false)

// 自定义绝对时间范围（q.minutes === -1 时生效）；快捷项贴合排障习惯
const range = ref<[Date, Date] | null>(null)
const rangeShortcuts = [
  {
    text: '最近 1 小时',
    value: () => { const end = new Date(); return [new Date(end.getTime() - 3600e3), end] },
  },
  {
    text: '今天',
    value: () => { const d = new Date(); d.setHours(0, 0, 0, 0); return [d, new Date()] },
  },
  {
    text: '昨天',
    value: () => {
      const end = new Date(); end.setHours(0, 0, 0, 0)
      return [new Date(end.getTime() - 86400e3), end]
    },
  },
  {
    text: '最近 7 天',
    value: () => { const end = new Date(); return [new Date(end.getTime() - 7 * 86400e3), end] },
  },
]

function fmtRange(m: number) {
  if (m < 60) return `${m} 分钟`
  if (m < 1440) return `${m / 60} 小时`
  return `${m / 1440} 天`
}
function fmtTime(ts: string) {
  return ts ? ts.replace('T', ' ').replace('Z', '').slice(0, 19) : ''
}
const isErr = (l?: string) => !!l && /error|err|e$/i.test(l)
const isWarn = (l?: string) => !!l && /warn/i.test(l)

function togglePod(pod: string) {
  q.pod = q.pod === pod ? '' : pod
  doSearch(1)
}

async function doSearch(p = 1) {
  if (q.minutes === -1 && !range.value) {
    ElMessage.warning('请选择起止时间（或切回相对时间范围）')
    return
  }
  loading.value = true
  page.value = p
  try {
    const payload: Record<string, any> = { ...q, page: p }
    if (q.minutes === -1 && range.value) {
      payload.from = new Date(range.value[0]).toISOString()
      payload.to = new Date(range.value[1]).toISOString()
      delete payload.minutes
    }
    result.value = await logsApi.search(payload)
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

function downloadCsv() {
  const rows = [['timestamp', 'namespace', 'pod', 'container', 'level', 'message']]
  for (const h of result.value?.items || []) {
    rows.push([h.timestamp, h.namespace, h.pod, h.container, h.level, h.message.replace(/[\r\n]+/g, ' ')])
  }
  const csv = rows.map((r) => r.map((c) => `"${String(c).replace(/"/g, '""')}"`).join(',')).join('\n')
  const blob = new Blob(['\ufeff' + csv], { type: 'text/csv' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `logs-${Date.now()}.csv`
  a.click()
}

onMounted(async () => {
  // 支持从工作负载"查看已采集日志"跳转预填过滤条件
  let prefilled = false
  for (const k of ['namespace', 'pod', 'container', 'keyword', 'source'] as const) {
    const v = route.query[k]
    if (typeof v === 'string' && v) {
      q[k] = v
      prefilled = true
    }
  }
  if (prefilled) doSearch(1)
  try {
    nsOptions.value = (await k8sApi.namespaces()).map((n: any) => n.name)
  } catch { /* ignore */ }
})
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.result-layout { display: flex; gap: 12px; margin-top: 8px; }
.facet { width: 240px; flex-shrink: 0; border-right: 1px solid var(--kc-border); padding-right: 10px; max-height: 60vh; overflow: auto; }
.facet-title { font-size: 12px; color: #909399; margin-bottom: 6px; }
.facet-row { display: flex; justify-content: space-between; align-items: center; padding: 4px 6px; border-radius: 4px; cursor: pointer; font-size: 12px; }
.facet-row:hover { background: #f5f7fa; }
.facet-row.active { background: var(--el-color-primary-light-9); }
.facet-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.logs { flex: 1; min-width: 0; }
.logs-meta { color: #909399; font-size: 12px; margin-bottom: 8px; display: flex; gap: 10px; align-items: center; }
.log-list { max-height: 62vh; overflow: auto; }
.log-row { display: flex; gap: 8px; align-items: flex-start; padding: 4px 6px; border-bottom: 1px solid #f2f3f5; font: 12px/1.5 ui-monospace, Consolas, monospace; }
.log-time { color: #909399; flex-shrink: 0; }
.log-pod { color: #606266; flex-shrink: 0; max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.log-msg { margin: 0; white-space: pre-wrap; word-break: break-all; flex: 1; font: inherit; }
.form-tip { color: #909399; font-size: 12px; line-height: 1.5; margin-top: 4px; }
</style>
