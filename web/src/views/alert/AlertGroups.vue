<template>
  <el-card shadow="never" style="margin-top: 16px">
    <template #header>
      <div class="card-header">
        <div>
          <span>告警分组</span>
          <span class="muted sub">共 {{ rows.length }} 个分组 · 点击「查看规则」定位到该分组规则</span>
        </div>
        <el-input v-model="search" placeholder="搜索分组名" clearable style="width: 240px">
          <template #prefix><el-icon><Search /></el-icon></template>
        </el-input>
      </div>
    </template>

    <el-table border :data="filtered" size="default" stripe :default-sort="{ prop: 'alertRules', order: 'descending' }">
      <el-table-column prop="name" label="分组名" min-width="280">
        <template #default="{ row }">
          <span class="group-name">{{ row.name }}</span>
        </template>
      </el-table-column>
      <el-table-column label="告警规则" width="110" align="center" sortable prop="alertRules">
        <template #default="{ row }">
          <el-tag :type="row.alertRules > 0 ? '' : 'info'" size="small" effect="plain">{{ row.alertRules }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="记录规则" width="110" align="center" sortable prop="recRules">
        <template #default="{ row }">
          <span class="muted">{{ row.recRules }}</span>
        </template>
      </el-table-column>
      <el-table-column label="触发中" width="100" align="center">
        <template #default="{ row }">
          <el-tag v-if="row.firing > 0" type="danger" size="small">{{ row.firing }}</el-tag>
          <span v-else class="muted">0</span>
        </template>
      </el-table-column>
      <el-table-column label="严重级别" min-width="160">
        <template #default="{ row }">
          <el-tag
            v-for="sv in row.severities"
            :key="sv"
            :type="sevTag(sv)"
            size="small"
            effect="plain"
            class="sev-tag"
          >{{ sevLabel(sv) }}</el-tag>
          <span v-if="!row.severities?.length" class="muted">—</span>
        </template>
      </el-table-column>
      <el-table-column label="评估间隔" width="110" align="center">
        <template #default="{ row }">
          <span class="muted">{{ row.interval }}</span>
        </template>
      </el-table-column>
      <el-table-column label="最近评估" width="180">
        <template #default="{ row }">
          <span class="muted">{{ fmtTime(row.lastEval) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="120" align="center" fixed="right">
        <template #default="{ row }">
          <el-button
            v-if="row.alertRules > 0"
            link
            type="primary"
            size="small"
            @click="emit('viewRules', row.name)"
          >查看规则</el-button>
          <span v-else class="muted">—</span>
        </template>
      </el-table-column>
    </el-table>
  </el-card>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import { Search } from '@element-plus/icons-vue'
import type { AlertRulesResponse, GroupSummary } from '../../api'

const props = defineProps<{ data?: AlertRulesResponse }>()
const emit = defineEmits<{ (e: 'viewRules', group: string): void }>()

const search = ref('')

const rows = computed<GroupSummary[]>(() => props.data?.groupList || [])

const filtered = computed(() => {
  const kw = search.value.trim().toLowerCase()
  return rows.value.filter((g) => !kw || g.name.toLowerCase().includes(kw))
})

function sevTag(s: string) {
  return s === 'critical' ? 'danger' : s === 'warning' ? 'warning' : s === 'info' ? 'info' : 'info'
}
function sevLabel(s: string) {
  return s === 'critical' ? '严重' : s === 'warning' ? '警告' : s === 'info' ? '信息' : '无'
}
function fmtTime(ts?: string): string {
  if (!ts) return '—'
  const d = new Date(ts)
  if (isNaN(d.getTime())) return ts
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; gap: 8px; flex-wrap: wrap; }
.sub { margin-left: 10px; font-size: 12px; }
.group-name { font-weight: 600; font-family: monospace; font-size: 13px; }
.muted { color: #c0c4cc; }
.sev-tag { margin-right: 4px; }
</style>
