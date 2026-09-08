<!-- 容器镜像漏洞徽标：自动拉取 Harbor 扫描概览，点击弹出 CVE 明细（供 Pod 详情等复用） -->
<template>
  <span v-if="loading" class="vchip">
    <el-tag size="small" type="info" effect="plain">...</el-tag>
  </span>
  <span v-else class="vchip" @click="openReport">
    <template v-if="overview">
      <el-tooltip :content="tooltip" placement="top">
        <el-tag v-if="overview.scanStatus !== 'Success'" size="small" :type="overview.scanStatus === 'Error' ? 'danger' : 'primary'" effect="plain">
          {{ overview.scanStatus }}
        </el-tag>
        <el-tag v-else size="small" :type="badgeType" effect="plain" style="cursor: pointer">
          CVE C{{ c }}·H{{ h }}·M{{ m }}
        </el-tag>
      </el-tooltip>
    </template>
    <!-- 不适用（镜像无 registry 前缀 / 不在已配置仓库 / 未配置仓库）：降级为灰标签，不弹错 -->
    <el-tooltip v-else :content="reason || '未配置镜像仓库或镜像不在仓库中'" placement="top">
      <el-tag size="small" type="info" effect="plain">CVE —</el-tag>
    </el-tooltip>
  </span>

  <el-dialog v-model="dlg" :title="`漏洞报告：${image}`" width="880px" top="4vh">
    <div style="display: flex; gap: 10px; align-items: center; margin-bottom: 10px; flex-wrap: wrap">
      <template v-if="report">
        <el-tag type="danger">Critical {{ countSev('Critical') }}</el-tag>
        <el-tag type="warning">High {{ countSev('High') }}</el-tag>
        <el-tag type="primary">Medium {{ countSev('Medium') }}</el-tag>
        <el-tag type="info">Low {{ countSev('Low') }}</el-tag>
        <span class="scan-meta">扫描器 {{ report.scanner || 'Trivy' }} · {{ fmtTime(report.generatedAt) }}</span>
      </template>
      <el-select v-model="filter" size="small" style="width: 130px; margin-left: auto" clearable placeholder="按级别筛选">
        <el-option v-for="s0 in ['Critical', 'High', 'Medium', 'Low', 'Unknown']" :key="s0" :label="s0" :value="s0" />
      </el-select>
      <el-button size="small" :loading="loading" @click="fetchReport">刷新</el-button>
    </div>
    <el-table v-if="report" :data="filtered" size="small" border max-height="52vh">
      <el-table-column label="级别" width="100" align="center">
        <template #default="{ row }"><el-tag size="small" :type="sevTag(row.severity)">{{ row.severity }}</el-tag></template>
      </el-table-column>
      <el-table-column label="CVE ID" width="190">
        <template #default="{ row }">
          <el-link v-if="row.primaryURL" :href="row.primaryURL" target="_blank" type="primary">{{ row.id }}</el-link>
          <span v-else class="mono">{{ row.id }}</span>
        </template>
      </el-table-column>
      <el-table-column prop="pkgName" label="受影响包" min-width="150" show-overflow-tooltip />
      <el-table-column label="版本" width="190">
        <template #default="{ row }">
          <span class="mono">{{ row.installedVersion }}</span>
          <span v-if="row.fixedVersion" style="color: #67c23a"> → {{ row.fixedVersion }}</span>
        </template>
      </el-table-column>
      <el-table-column prop="title" label="标题 / 描述" min-width="230" show-overflow-tooltip>
        <template #default="{ row }">{{ row.title || row.description || '-' }}</template>
      </el-table-column>
    </el-table>
    <el-empty v-else-if="!loading" description="尚无扫描报告（可在「平台管理-镜像仓库」对该 Tag 触发扫描）" :image-size="60" />
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { registryApi } from '../api'

const props = defineProps<{ image: string }>()

const loading = ref(false)
const reason = ref('')
const overview = ref<any>(null)
const report = ref<any>(null)
const dlg = ref(false)
const filter = ref('')

const c = computed(() => overview.value?.counts?.Critical ?? 0)
const h = computed(() => overview.value?.counts?.High ?? 0)
const m = computed(() => overview.value?.counts?.Medium ?? 0)
const badgeType = computed(() => (c.value > 0 ? 'danger' : h.value > 0 ? 'warning' : 'success'))
const tooltip = computed(() =>
  overview.value?.scanStatus === 'Success' ? '点击查看 CVE 明细' : `扫描状态：${overview.value?.scanStatus || '未知'}`,
)

const filtered = computed(() => (report.value?.items || []).filter((v: any) => !filter.value || v.severity === filter.value))
function countSev(sev: string) {
  return (report.value?.items || []).filter((v: any) => v.severity === sev).length
}
function sevTag(s: string) {
  if (s === 'Critical') return 'danger'
  if (s === 'High') return 'warning'
  if (s === 'Medium') return 'primary'
  return 'info'
}
function fmtTime(s?: string) {
  return s ? s.replace('T', ' ').slice(0, 19) : ''
}

async function fetchReport() {
  const img = (props.image || '').trim()
  // 无 registry 前缀（如 nginx:1.27）的镜像不属于已配置仓库，不发请求直接降级
  if (!img.includes('/')) {
    reason.value = '镜像无仓库前缀，不在已配置的镜像仓库中'
    loading.value = false
    return
  }
  loading.value = true
  try {
    const r = await registryApi.imageVulnsQuiet(img)
    overview.value = r?.overview ?? null
    report.value = r?.report ?? null
    reason.value = ''
  } catch (e: any) {
    overview.value = null
    reason.value = e?.message || '查询失败'
  } finally {
    loading.value = false
  }
}
function openReport() {
  dlg.value = true
  if (!report.value) fetchReport()
}

onMounted(fetchReport)
</script>

<style scoped>
.vchip { display: inline-block; }
.mono { font-family: ui-monospace, Consolas, monospace; font-size: 12px; }
.scan-meta { color: #909399; font-size: 12px; }
</style>
