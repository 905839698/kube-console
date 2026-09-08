<template>
  <div>
    <div class="toolbar">
      <el-select v-model="filterUser" clearable filterable placeholder="按用户筛选" style="width: 180px" @change="load(1)">
        <el-option v-for="u in userNames" :key="u" :label="u" :value="u" />
      </el-select>
      <el-input v-model="keyword" placeholder="搜索路径/查询参数" clearable style="width: 240px" @keyup.enter="load(1)" />
      <el-button type="primary" plain @click="load(1)">查询</el-button>
      <el-button :icon="Refresh" circle @click="load()" />
    </div>

    <el-table :data="items" v-loading="loading" size="small">
      <el-table-column prop="createdAt" label="时间" width="170" />
      <el-table-column prop="username" label="用户" width="120" />
      <el-table-column label="操作" width="80">
        <template #default="{ row }">
          <el-tag :type="methodTag(row.method)" size="small" effect="plain">{{ row.method }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="路径" min-width="280">
        <template #default="{ row }">
          <span class="path">{{ row.path }}<template v-if="row.query">?{{ row.query }}</template></span>
        </template>
      </el-table-column>
      <el-table-column prop="cluster" label="集群" width="100" />
      <el-table-column label="状态" width="80" align="center">
        <template #default="{ row }">
          <span :class="row.status < 400 ? 'ok' : 'err'">{{ row.status }}</span>
        </template>
      </el-table-column>
      <el-table-column prop="latencyMs" label="耗时(ms)" width="90" align="center" sortable />
      <el-table-column prop="ip" label="来源 IP" width="130" />
      <el-table-column type="expand">
        <template #default="{ row }">
          <pre class="audit-body">{{ row.body || '（无请求体记录）' }}</pre>
        </template>
      </el-table-column>
    </el-table>

    <div class="pager">
      <el-pagination
        v-model:current-page="page"
        :page-size="size"
        :total="total"
        layout="total, prev, pager, next, sizes"
        :page-sizes="[20, 50, 100]"
        @current-change="load()"
        @size-change="load(1)"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { auditApi, userApi, type AuditItem } from '../api'

const items = ref<AuditItem[]>([])
const loading = ref(false)
const total = ref(0)
const page = ref(1)
const size = ref(20)
const keyword = ref('')
const filterUser = ref('')
const userNames = ref<string[]>([])

function methodTag(method: string): 'success' | 'warning' | 'danger' {
  if (method === 'POST') return 'success'
  if (method === 'PUT') return 'warning'
  return 'danger'
}

async function load(toPage?: number) {
  if (toPage) page.value = toPage
  loading.value = true
  try {
    const res = await auditApi.list({ page: page.value, size: size.value, username: filterUser.value, q: keyword.value })
    items.value = res.items
    total.value = res.total
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  await load()
  userApi
    .list()
    .then((u) => (userNames.value = u.map((x) => x.username)))
    .catch(() => {})
})
</script>

<style scoped>
.audit-body { margin: 0; padding: 8px 16px; font: 12px/1.6 ui-monospace, Consolas, monospace; white-space: pre-wrap; word-break: break-all; color: #606266; }
</style>
<style scoped>
.toolbar { display: flex; gap: 8px; margin-bottom: 12px; }
.path { font-family: monospace; font-size: 12px; word-break: break-all; }
.ok { color: #67c23a; }
.err { color: #f56c6c; font-weight: 600; }
.pager { margin-top: 12px; display: flex; justify-content: flex-end; }
</style>
