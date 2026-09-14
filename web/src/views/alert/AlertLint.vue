<template>
  <el-card shadow="never" style="margin-top: 16px">
    <template #header>
      <div class="card-header">
        <div>
          <span>规则检查</span>
          <span class="muted sub">扫描 {{ rules.length }} 条告警规则的配置缺陷（缺失严重级 / 命名空间 / 摘要 / Runbook）</span>
        </div>
        <el-radio-group v-model="onlyIssues" size="default">
          <el-radio-button :value="false">全部</el-radio-button>
          <el-radio-button :value="true">仅问题规则</el-radio-button>
        </el-radio-group>
      </div>
    </template>

    <!-- 问题分类统计 -->
    <el-row :gutter="12" class="issue-row">
      <el-col :span="6">
        <el-card shadow="never" class="issue-card" :class="{ warn: issues.count('severity') > 0 }">
          <div class="issue-num">{{ issues.count('severity') }}</div>
          <div class="issue-label">缺严重级 severity</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="never" class="issue-card" :class="{ warn: issues.count('namespace') > 0 }">
          <div class="issue-num">{{ issues.count('namespace') }}</div>
          <div class="issue-label">缺命名空间 namespace</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="never" class="issue-card" :class="{ warn: issues.count('summary') > 0 }">
          <div class="issue-num">{{ issues.count('summary') }}</div>
          <div class="issue-label">缺摘要 summary</div>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="never" class="issue-card" :class="{ warn: issues.count('runbook') > 0 }">
          <div class="issue-num">{{ issues.count('runbook') }}</div>
          <div class="issue-label">缺 Runbook</div>
        </el-card>
      </el-col>
    </el-row>

    <el-table border :data="filtered" size="default" stripe>
      <el-table-column prop="alertName" label="规则名" min-width="240">
        <template #default="{ row }">
          <div class="rule-name">{{ row.alertName }}</div>
          <div class="muted sub2">{{ row.group }}</div>
        </template>
      </el-table-column>
      <el-table-column label="严重级" width="100">
        <template #default="{ row }">
          <el-tag v-if="row.hasSeverity" :type="sevTag(row.severity)" size="small" effect="plain">{{ sevLabel(row.severity) }}</el-tag>
          <el-tag v-else type="danger" size="small">缺失</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="命名空间" width="150">
        <template #default="{ row }">
          <span v-if="row.hasNamespace">{{ row.namespace || '实例级' }}</span>
          <el-tag v-else type="danger" size="small">缺失</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="摘要" width="90" align="center">
        <template #default="{ row }">
          <el-icon v-if="row.hasSummary" class="ok"><CircleCheck /></el-icon>
          <el-tag v-else type="danger" size="small">缺失</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="Runbook" width="100" align="center">
        <template #default="{ row }">
          <a v-if="row.hasRunbook" :href="row.runbook" target="_blank" rel="noopener" class="link">打开</a>
          <el-tag v-else type="warning" size="small">未配置</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="问题" min-width="260">
        <template #default="{ row }">
          <el-tag v-for="t in issueTexts(row)" :key="t" type="danger" effect="plain" size="small" class="issue-tag">{{ t }}</el-tag>
          <span v-if="!issueTexts(row).length" class="muted">配置完整</span>
        </template>
      </el-table-column>
    </el-table>

    <el-empty v-if="!filtered.length" description="没有符合筛选条件的规则" :image-size="80" />
  </el-card>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import { CircleCheck } from '@element-plus/icons-vue'
import type { AlertRule } from '../../api'

const props = defineProps<{ rules: AlertRule[] }>()
const onlyIssues = ref(false)

type IssueKey = 'severity' | 'namespace' | 'summary' | 'runbook'

function hasIssue(r: AlertRule, k: IssueKey): boolean {
  return !r['has' + k.charAt(0).toUpperCase() + k.slice(1)]
}

const issues = {
  count(k: IssueKey): number {
    return props.rules.filter((r) => hasIssue(r, k)).length
  },
}

function issueTexts(r: AlertRule): string[] {
  const t: string[] = []
  if (!r.hasSeverity) t.push('缺严重级')
  if (!r.hasNamespace) t.push('缺命名空间')
  if (!r.hasSummary) t.push('缺摘要')
  if (!r.hasRunbook) t.push('缺 Runbook')
  return t
}

const filtered = computed(() => {
  const list = props.rules
  if (!onlyIssues.value) return list
  return list.filter((r) => issueTexts(r).length > 0)
})

function sevTag(s: string) {
  return s === 'critical' ? 'danger' : s === 'warning' ? 'warning' : s === 'info' ? 'info' : 'info'
}
function sevLabel(s: string) {
  return s === 'critical' ? '严重' : s === 'warning' ? '警告' : s === 'info' ? '信息' : '无'
}
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; gap: 8px; flex-wrap: wrap; }
.sub { margin-left: 10px; font-size: 12px; }
.sub2 { font-size: 12px; margin-top: 2px; }
.muted { color: #c0c4cc; }
.rule-name { font-weight: 600; font-family: monospace; font-size: 13px; }

.issue-row { margin-bottom: 14px; }
.issue-card { text-align: center; }
.issue-num { font-size: 26px; font-weight: 700; color: #67c23a; }
.issue-card.warn .issue-num { color: #f56c6c; }
.issue-label { color: #909399; font-size: 12px; margin-top: 4px; }

.ok { color: #67c23a; }
.link { color: var(--el-color-primary); }
.issue-tag { margin-right: 4px; }
</style>
