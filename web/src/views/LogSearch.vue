<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>日志检索（Elasticsearch 历史日志）</span>
        <el-button v-if="userStore.isAdmin" size="small" @click="cfgVisible = true">日志源配置</el-button>
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
      <el-form-item label="时间范围">
        <el-select v-model="q.minutes" style="width: 120px">
          <el-option v-for="m in [15, 60, 360, 1440, 4320, 10080]" :key="m" :label="fmtRange(m)" :value="m" />
        </el-select>
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

    <!-- 日志源配置（管理员） -->
    <el-dialog v-model="cfgVisible" title="日志源配置（Elasticsearch）" width="560px">
      <el-form label-width="110px" size="small">
        <el-form-item label="集群">
          <el-select v-model="cfg.clusterName" style="width: 100%">
            <el-option v-for="cl in clusters" :key="cl" :label="cl" :value="cl" />
          </el-select>
        </el-form-item>
        <el-form-item label="ES 命名空间"><el-input v-model="cfg.namespace" placeholder="如 prd-public-service" /></el-form-item>
        <el-form-item label="ES 服务名"><el-input v-model="cfg.service" placeholder="如 elasticsearch-es-http" /></el-form-item>
        <el-form-item label="ES 端口"><el-input-number v-model="cfg.port" :min="1" :max="65535" /></el-form-item>
        <el-form-item label="直连地址">
          <el-input v-model="cfg.directURL" placeholder="如 http://节点IP:NodePort（可选）" />
          <div class="form-tip">留空则经 apiserver 服务代理访问。ES 开启安全认证（填了用户名）时必须走直连——代理会剥离认证头导致 401。做法：把 ES 服务改为 NodePort，填 http://任一节点IP:nodePort。</div>
        </el-form-item>
        <el-form-item label="索引前缀">
          <el-input v-model="cfg.indexPrefix" placeholder="如 logstash- 或 k8s-{namespace}-（空=logstash-*）" />
          <div class="form-tip">索引按命名空间拆分时用 {namespace} 占位符，如 k8s-{namespace}-：选命名空间检索时自动展开为 k8s-dev*，不限命名空间时展开为 k8s-*。</div>
        </el-form-item>
        <el-form-item label="用户名"><el-input v-model="cfg.username" placeholder="ES basic auth（可选）" /></el-form-item>
        <el-form-item label="密码"><el-input v-model="cfg.password" type="password" show-password placeholder="留空保持不变" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="testSource" :loading="testing">测试连通</el-button>
        <el-button @click="cfgVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveSource">保存</el-button>
      </template>
    </el-dialog>
  </el-card>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { logsApi, clusterApi, k8sApi, type LogSearchResult, type LogSourceItem } from '../api'
import { useClusterStore } from '../store/cluster'
import { useUserStore } from '../store/user'

const userStore = useUserStore()
const clusterStore = useClusterStore()

const clusters = ref<string[]>([])
const nsOptions = ref<string[]>([])
const loading = ref(false)
const page = ref(1)
const result = ref<LogSearchResult>()
const q = reactive<{ namespace: string; pod: string; container: string; keyword: string; level: string; minutes: number; size: number }>({
  namespace: '', pod: '', container: '', keyword: '', level: '', minutes: 360, size: 50,
})

const cfgVisible = ref(false)
const saving = ref(false)
const testing = ref(false)
const cfg = reactive<Partial<LogSourceItem> & { password?: string }>({ clusterName: '', namespace: '', service: '', port: 9200, directURL: '', indexPrefix: '', username: '', password: '' })

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
  loading.value = true
  page.value = p
  try {
    result.value = await logsApi.search({ ...q, page: p })
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

async function loadConfig() {
  try {
    const list = await logsApi.sources()
    const cur = list.find((s) => s.clusterName === clusterStore.current)
    if (cur) Object.assign(cfg, cur, { password: '' })
  } catch { /* ignore */ }
}

async function saveSource() {
  saving.value = true
  try {
    await logsApi.saveSource({ ...cfg, clusterName: cfg.clusterName || clusterStore.current || '' })
    ElMessage.success('已保存')
    cfgVisible.value = false
  } finally {
    saving.value = false
  }
}

async function testSource() {
  testing.value = true
  try {
    await logsApi.testSource({ ...cfg, clusterName: cfg.clusterName || clusterStore.current || '' })
    ElMessage.success('连通成功')
  } catch {
    /* 拦截器已提示 */
  } finally {
    testing.value = false
  }
}

onMounted(async () => {
  try {
    const list = await clusterApi.list()
    clusters.value = list.map((c) => c.name)
  } catch { /* ignore */ }
  try {
    nsOptions.value = (await k8sApi.namespaces()).map((n: any) => n.name)
  } catch { /* ignore */ }
  loadConfig()
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
