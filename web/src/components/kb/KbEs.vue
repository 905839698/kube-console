<template>
  <div>
    <div class="ops-bar">
      <el-tag :type="healthType" size="large">{{ health ? health.status : '—' }}</el-tag>
      <span class="meta" v-if="health">
        节点 {{ health.nodes }}（数据 {{ health.dataNodes }}）· 分片 {{ health.activeShards }}
        <template v-if="health.unassignedShards">· <b class="warn">未分配 {{ health.unassignedShards }}</b></template>
        <template v-if="health.relocatingShards">· 迁移中 {{ health.relocatingShards }}</template>
        · 可用 {{ health.activeShardsPercent }}%
      </span>
      <el-button size="small" :loading="loading" @click="load">刷新</el-button>
      <span class="tip">索引与查询在 es 容器内 curl 127.0.0.1:9200，认证用容器内的 $ELASTIC_PASSWORD。</span>
    </div>
    <div v-if="err" class="err-box">{{ err }}</div>

    <el-table
      border :data="indices" size="small" v-loading="loading" style="margin-top: 10px"
      highlight-current-row @current-change="pick"
    >
      <el-table-column prop="index" label="索引" min-width="200" sortable show-overflow-tooltip />
      <el-table-column label="健康" width="80">
        <template #default="{ row }"><el-tag :type="row.health === 'green' ? 'success' : row.health === 'yellow' ? 'warning' : 'danger'" size="small">{{ row.health }}</el-tag></template>
      </el-table-column>
      <el-table-column prop="status" label="状态" width="80" />
      <el-table-column label="分片" width="90">
        <template #default="{ row }">{{ row.primaries }}/{{ row.replicas }}</template>
      </el-table-column>
      <el-table-column prop="docsCount" label="文档数" width="110" sortable />
      <el-table-column prop="docsDeleted" label="已删" width="90" />
      <el-table-column prop="storeSize" label="存储" width="100" />
      <el-table-column prop="segmentsCount" label="段" width="80" />
      <el-table-column label="操作" width="90">
        <template #default="{ row }">
          <el-button size="small" type="danger" :disabled="!writable" @click="drop(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-card v-if="detail" shadow="never" style="margin-top: 12px">
      <template #header>
        <span class="head">{{ detailFor }} · 定义</span>
        <el-tag v-for="a in detail.aliases" :key="a" size="small" style="margin-left: 6px">别名 {{ a }}</el-tag>
      </template>
      <el-descriptions :column="2" size="small" border>
        <el-descriptions-item v-for="(v, k) in pickedSettings" :key="k" :label="k">{{ v }}</el-descriptions-item>
      </el-descriptions>
      <el-table border :data="detail.fields" size="small" max-height="260" style="margin-top: 10px">
        <el-table-column prop="name" label="字段" min-width="220" show-overflow-tooltip />
        <el-table-column prop="type" label="类型" width="140" />
      </el-table>
    </el-card>

    <el-divider content-position="left">查询</el-divider>
    <div class="ops-bar">
      <el-input v-model="q.index" placeholder="索引或别名，留空=全部" size="small" style="width: 240px" />
      <el-input-number v-model="q.size" :min="1" :max="200" size="small" style="width: 110px" />
      <el-button size="small" type="primary" :loading="querying" @click="search">执行</el-button>
      <el-checkbox v-model="q.showRaw" size="small">看原始返回</el-checkbox>
      <span class="tip">返回条数由服务端收口到 ≤200。</span>
    </div>
    <el-input v-model="q.dsl" type="textarea" :rows="7" class="mono" style="margin-top: 8px" placeholder='{"query":{"match":{"message":"timeout"}},"sort":[{"@timestamp":{"order":"desc"}}]}' />
    <div v-if="hits" class="meta" style="margin-top: 8px">命中 {{ hits.total }} 条 · 耗时 {{ hits.tookMs }}ms · 返回 {{ hits.docs.length }} 条</div>
    <el-table v-if="hits && !q.showRaw" border :data="hits.docs" size="small" style="margin-top: 8px" max-height="380">
      <el-table-column v-for="col in docCols" :key="col" :prop="col" :label="col" min-width="160" show-overflow-tooltip>
        <template #default="{ row }">{{ cell(row[col]) }}</template>
      </el-table-column>
    </el-table>
    <pre v-if="q.showRaw && raw" class="out-box">{{ raw }}</pre>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { kbApi, kbMatchesEngine, type KBCluster, type KBESHealth, type KBESIndex, type KBESIndexDetail, type KBESHits } from '../../api/kb'

const props = defineProps<{ cluster: KBCluster; writable: boolean }>()
const indices = ref<KBESIndex[]>([])
const health = ref<KBESHealth | null>(null)
const loading = ref(false)
const err = ref('')
const detail = ref<KBESIndexDetail | null>(null)
const detailFor = ref('')

const q = reactive({ index: '', size: 20, dsl: '{"query":{"match_all":{}}}', showRaw: false })
const hits = ref<KBESHits | null>(null)
const raw = ref('')
const querying = ref(false)

const healthType = computed(() => (health.value?.status === 'green' ? 'success' : health.value?.status === 'yellow' ? 'warning' : health.value ? 'danger' : 'info'))
// settings 里噪音项（uuid/版本/创建时间）不展示，只留运维关心的
const pickedSettings = computed(() => {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(detail.value?.settings || {})) {
    if (/uuid|version|creation_date|provided_name$/.test(k)) continue
    out[k] = v
  }
  return out
})
const docCols = computed(() => {
  const first = hits.value?.docs?.[0]
  if (!first) return [] as string[]
  const cols = Object.keys(first).filter((k) => k !== '_score')
  return ['_index', '_id', ...cols.filter((c) => c !== '_index' && c !== '_id')]
})

watch(() => props.cluster?.name, () => {
  indices.value = []
  health.value = null
  detail.value = null
  hits.value = null
  raw.value = ''
  err.value = ''
  if (props.cluster) load()
}, { immediate: true })

async function load() {
  if (!props.cluster) return
  // 双保险：同 KbKafka——防止任何残留状态把 ES 接口打到别的引擎集群上
  if (!kbMatchesEngine(props.cluster, 'elasticsearch')) {
    err.value = '当前集群不是 Elasticsearch 引擎，已跳过加载'
    return
  }
  loading.value = true
  err.value = ''
  try {
    const [h, list] = await Promise.all([
      kbApi.esHealth(props.cluster.namespace, props.cluster.name),
      kbApi.esIndices(props.cluster.namespace, props.cluster.name),
    ])
    health.value = h
    indices.value = list
  } catch (e: any) {
    err.value = e?.message || '取索引失败'
  } finally { loading.value = false }
}

async function pick(row: KBESIndex | null) {
  if (!row) return
  q.index = row.index
  try {
    detail.value = await kbApi.esIndex(props.cluster.namespace, props.cluster.name, row.index)
    detailFor.value = row.index
  } catch (e: any) {
    detail.value = null
    err.value = e?.message || '取索引定义失败'
  }
}

function cell(v: any) {
  if (v === null || v === undefined) return '-'
  return typeof v === 'object' ? JSON.stringify(v) : String(v)
}

async function drop(row: KBESIndex) {
  try {
    await ElMessageBox.confirm(`删除索引 ${row.index}（${row.docsCount} 条文档、${row.storeSize}）？数据不可恢复。`, '删除索引', { type: 'warning', confirmButtonText: '确认删除' })
  } catch { return }
  try {
    await kbApi.esDeleteIndex(props.cluster.namespace, props.cluster.name, row.index)
    ElMessage.success('已删除')
    if (detailFor.value === row.index) { detail.value = null; detailFor.value = '' }
    load()
  } catch { /* 拦截器已提示 */ }
}

async function search() {
  querying.value = true
  err.value = ''
  try {
    const r = await kbApi.esSearch(props.cluster.namespace, props.cluster.name, {
      index: q.index.trim(), size: q.size, body: q.dsl.trim() || '{"query":{"match_all":{}}}',
    })
    hits.value = r.hits
    raw.value = pretty(r.raw)
  } catch (e: any) {
    hits.value = null
    err.value = e?.message || '查询失败'
  } finally { querying.value = false }
}

function pretty(s: string) {
  try {
    return JSON.stringify(JSON.parse(s), null, 2)
  } catch {
    return s
  }
}
</script>

<style scoped>
.ops-bar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.tip { font-size: 12px; color: #9ca3af; }
.meta { font-size: 12px; color: #6b7280; }
.warn { color: #b45309; }
.head { font-weight: 600; }
.err-box { margin-top: 8px; padding: 8px 10px; background: #fef2f2; color: #b91c1c; font-size: 12px; white-space: pre-wrap; border-radius: 4px; }
.out-box { margin-top: 10px; max-height: 340px; overflow: auto; background: #f8fafc; border: 1px solid #e5e7eb; padding: 8px; font-size: 12px; white-space: pre-wrap; }
</style>
