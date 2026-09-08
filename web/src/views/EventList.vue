<template>
  <div>
    <div class="toolbar">
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
    </div>

    <el-table :data="items" v-loading="loading" size="small" :default-sort="{ prop: 'lastAt', order: 'descending' }">
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
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { eventsApi, type EventItemEx } from '../api'
import { useNamespaceStore } from '../store/namespace'
import { nsParam } from '../api'
import { useAutoRefresh } from '../composables/useAutoRefresh'
import NamespaceSelect from '../components/NamespaceSelect.vue'

const nsStore = useNamespaceStore()
const nsModel = computed({
  get: () => nsStore.selected,
  set: (v) => nsStore.select(v as string[]),
})

const items = ref<EventItemEx[]>([])
const loading = ref(false)
const typeFilter = ref('')
const keyword = ref('')
const auto = ref(false)
const lastLoaded = ref('')
// 自动刷新（30s）：页面隐藏时自动暂停，回前台立即刷新
useAutoRefresh(() => { if (auto.value) load() }, 30000, auto)

async function load() {
  loading.value = true
  try {
    items.value = await eventsApi.list(nsParam(nsStore.selected), typeFilter.value, keyword.value.trim())
    lastLoaded.value = new Date().toLocaleTimeString()
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.toolbar { display: flex; gap: 8px; margin-bottom: 12px; align-items: center; flex-wrap: wrap; }
.updated { color: #909399; font-size: 12px; }
.empty { text-align: center; color: #909399; padding: 40px 0; }
</style>
