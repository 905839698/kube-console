<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>镜像仓库（Harbor）</span>
        <el-button size="small" @click="openSettings">连接设置</el-button>
      </div>
    </template>

    <template v-if="configured">
      <el-steps :active="step" simple style="margin-bottom: 12px">
        <el-step title="选择项目" />
        <el-step title="选择镜像" />
        <el-step title="查看 Tag" />
      </el-steps>
      <div class="toolbar">
        <el-input v-model="search" :placeholder="step === 0 ? '搜索项目' : '搜索镜像'" clearable style="width: 240px" @keyup.enter="reload" />
        <el-button :icon="Refresh" circle @click="reload" />
        <el-link v-if="step > 0" @click="step = 0; search = ''" style="margin-left: 10px">← 返回项目列表</el-link>
      </div>

      <el-table v-if="step === 0" :data="projects" v-loading="loading" size="small" stripe>
        <el-table-column prop="name" label="项目" min-width="200">
          <template #default="{ row }">
            <el-link type="primary" @click="openProject(row.name)">{{ row.name }}</el-link>
          </template>
        </el-table-column>
        <el-table-column prop="repo_count" label="仓库数" width="100" align="center" />
      </el-table>

      <el-table v-else-if="step === 1" :data="repos" v-loading="loading" size="small" stripe>
        <el-table-column prop="name" label="镜像仓库" min-width="280">
          <template #default="{ row }">
            <el-link type="primary" @click="openRepo(row.name)">{{ row.name }}</el-link>
          </template>
        </el-table-column>
        <el-table-column prop="artifact_count" label="制品数" width="90" align="center" />
        <el-table-column prop="pull_count" label="拉取次数" width="100" align="center" />
        <el-table-column prop="update_time" label="更新时间" width="170">
          <template #default="{ row }">{{ fmtTime(row.update_time) }}</template>
        </el-table-column>
      </el-table>

      <template v-else-if="step === 2">
        <div class="tag-header">
          镜像：<code>{{ fullRepo }}</code>
          <el-tag v-for="t in topTags" :key="t" size="small" style="margin-left: 6px">{{ t }}</el-tag>
        </div>
        <el-table :data="artifacts" v-loading="loading" size="small" stripe>
          <el-table-column label="Tag" min-width="150">
            <template #default="{ row }">
              <el-link v-for="t in row.tags" :key="t.name" style="margin-right: 8px" @click="copyRef(t.name)">{{ t.name }}</el-link>
              <span v-if="!row.tags?.length" class="untagged">(无 tag)</span>
            </template>
          </el-table-column>
          <el-table-column label="漏洞" width="190">
            <template #default="{ row }">
              <template v-if="scanMap[row.digest]">
                <el-tag v-if="scanMap[row.digest].scanStatus !== 'Success'" size="small" :type="scanMap[row.digest].scanStatus === 'Error' ? 'danger' : 'primary'">
                  {{ scanMap[row.digest].scanStatus }}
                </el-tag>
                <template v-else>
                  <el-tag v-if="scanMap[row.digest].counts.Critical" size="small" type="danger" style="margin-right: 4px">C {{ scanMap[row.digest].counts.Critical }}</el-tag>
                  <el-tag v-if="scanMap[row.digest].counts.High" size="small" type="warning" style="margin-right: 4px">H {{ scanMap[row.digest].counts.High }}</el-tag>
                  <el-tag v-if="scanMap[row.digest].counts.Medium" size="small" type="info">M {{ scanMap[row.digest].counts.Medium }}</el-tag>
                  <el-tag v-if="!scanMap[row.digest].counts.Critical && !scanMap[row.digest].counts.High && !scanMap[row.digest].counts.Medium" size="small" type="success">无高危</el-tag>
                </template>
              </template>
              <span v-else class="untagged">未扫描</span>
            </template>
          </el-table-column>
          <el-table-column prop="digest" label="Digest" min-width="180" show-overflow-tooltip />
          <el-table-column label="大小" width="100" align="right">
            <template #default="{ row }">{{ fmtSize(row.size) }}</template>
          </el-table-column>
          <el-table-column label="推送时间" width="160">
            <template #default="{ row }">{{ fmtTime(row.push_time) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="210" fixed="right">
            <template #default="{ row }">
              <el-button v-if="row.tags?.length" size="small" text type="primary" @click="copyRef(row.tags[0].name)">复制地址</el-button>
              <el-button size="small" text type="warning" @click="triggerScan(row)">扫描</el-button>
              <el-button size="small" text type="primary" @click="openVulns(row)">漏洞</el-button>
            </template>
          </el-table-column>
        </el-table>
      </template>
    </template>
    <el-empty v-else description="尚未配置镜像仓库连接">
      <el-button type="primary" @click="openSettings">立即配置</el-button>
    </el-empty>

    <!-- 漏洞报告 -->
    <el-dialog v-model="vulnDlg" :title="`漏洞报告：${vulnRefLabel}`" width="900px" top="4vh">
      <div style="display: flex; gap: 10px; align-items: center; margin-bottom: 10px; flex-wrap: wrap">
        <template v-if="vulnReport">
          <el-tag type="danger">Critical {{ countSev('Critical') }}</el-tag>
          <el-tag type="warning">High {{ countSev('High') }}</el-tag>
          <el-tag type="primary">Medium {{ countSev('Medium') }}</el-tag>
          <el-tag type="info">Low {{ countSev('Low') }}</el-tag>
          <span class="untagged">扫描器 {{ vulnReport.scanner || 'Trivy' }} · {{ fmtTime(vulnReport.generatedAt) }}</span>
        </template>
        <el-select v-model="vulnFilter" size="small" style="width: 140px; margin-left: auto" clearable placeholder="按级别筛选">
          <el-option v-for="s0 in ['Critical', 'High', 'Medium', 'Low', 'Unknown']" :key="s0" :label="s0" :value="s0" />
        </el-select>
        <el-button size="small" @click="reloadVulns">刷新</el-button>
      </div>
      <el-table v-if="vulnReport" :data="filteredVulns" size="small" border max-height="55vh">
        <el-table-column label="级别" width="100" align="center">
          <template #default="{ row }">
            <el-tag size="small" :type="sevTag(row.severity)">{{ row.severity }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="CVE / 漏洞 ID" width="200">
          <template #default="{ row }">
            <el-link v-if="row.primaryURL" :href="row.primaryURL" target="_blank" type="primary">{{ row.id }}</el-link>
            <span v-else class="mono">{{ row.id }}</span>
          </template>
        </el-table-column>
        <el-table-column label="CVSS" width="80" align="center">
          <template #default="{ row }">
            <span v-if="row.cvssScore" :style="{ color: row.cvssScore >= 9 ? '#f56c6c' : row.cvssScore >= 7 ? '#e6a23c' : '#909399' }">{{ row.cvssScore.toFixed(1) }}</span>
            <span v-else>-</span>
          </template>
        </el-table-column>
        <el-table-column prop="pkgName" label="受影响包" min-width="160" show-overflow-tooltip />
        <el-table-column label="版本" width="190">
          <template #default="{ row }">
            <span class="mono">{{ row.installedVersion }}</span>
            <span v-if="row.fixedVersion" style="color: #67c23a"> → {{ row.fixedVersion }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="title" label="标题 / 描述" min-width="240" show-overflow-tooltip>
          <template #default="{ row }">{{ row.title || row.description || '-' }}</template>
        </el-table-column>
      </el-table>
      <el-empty v-else-if="!vulnLoading" description="尚无扫描报告：先点「扫描」触发，完成后查看" />
      <div v-if="vulnLoading" style="text-align: center; padding: 20px; color: #909399">加载中...</div>
    </el-dialog>

    <!-- 连接设置 -->
    <el-dialog v-model="cfgVisible" title="Harbor 连接设置" width="480px">
      <el-form label-width="100px" size="small">
        <el-form-item label="地址"><el-input v-model="cfg.url" placeholder="https://harbor.cqyun.pt.site:8443" /></el-form-item>
        <el-form-item label="用户名"><el-input v-model="cfg.username" /></el-form-item>
        <el-form-item label="密码"><el-input v-model="cfg.password" type="password" show-password placeholder="留空保持不变" /></el-form-item>
        <el-form-item label="跳过证书校验"><el-switch v-model="cfg.insecure" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="testCfg" :loading="testing">测试连接</el-button>
        <el-button type="primary" :loading="saving" @click="saveCfg">保存</el-button>
      </template>
    </el-dialog>
  </el-card>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { registryApi, type RegProject, type RegRepository, type RegArtifact } from '../api'

const configured = ref(false)
const step = ref(0)
const search = ref('')
const loading = ref(false)
const projects = ref<RegProject[]>([])
const repos = ref<RegRepository[]>([])
const artifacts = ref<RegArtifact[]>([])
const curProject = ref('')
const fullRepo = ref('')

const cfgVisible = ref(false)
// ---- 漏洞扫描 ----
const scanMap = reactive<Record<string, { scanStatus: string; severity: string; counts: Record<string, number> }>>({})
let scanTimer: ReturnType<typeof setInterval> | undefined
const vulnDlg = ref(false)
const vulnLoading = ref(false)
const vulnReport = ref<{ scanner?: string; generatedAt?: string; items: any[] } | null>(null)
const vulnFilter = ref('')
const vulnRefLabel = ref('')
const vulnTarget = reactive<{ project: string; repo: string; reference: string }>({ project: '', repo: '', reference: '' })

const filteredVulns = computed(() =>
  (vulnReport.value?.items || []).filter((v) => !vulnFilter.value || v.severity === vulnFilter.value),
)
function countSev(sev: string) {
  return (vulnReport.value?.items || []).filter((v) => v.severity === sev).length
}
function sevTag(s: string) {
  if (s === 'Critical') return 'danger'
  if (s === 'High') return 'warning'
  if (s === 'Medium') return 'primary'
  return 'info'
}

function repoPart() {
  return fullRepo.value.slice(curProject.value.length + 1)
}

// 扫描状态快速拉取（进入制品列表时对前 20 个制品并发查询概览）
async function loadScanOverviews() {
  for (const a of artifacts.value.slice(0, 20)) {
    if (!a.digest || scanMap[a.digest]?.scanStatus === 'Success') continue
    const ref0 = a.tags?.[0]?.name || a.digest
    registryApi.scan('get', curProject.value, repoPart(), ref0).then((ov: any) => {
      if (ov && ov.scanStatus) scanMap[a.digest] = ov
    }).catch(() => {})
  }
}

async function triggerScan(row: any) {
  const ref0 = row.tags?.[0]?.name || row.digest
  try {
    await registryApi.scan('post', curProject.value, repoPart(), ref0)
    ElMessage.success('扫描已触发，正在轮询状态...')
  } catch {
    return
  }
  scanMap[row.digest] = { scanStatus: 'Running', severity: '', counts: {} }
  if (scanTimer) clearInterval(scanTimer)
  scanTimer = setInterval(async () => {
    try {
      const ov = await registryApi.scan('get', curProject.value, repoPart(), ref0)
      scanMap[row.digest] = ov
      if (ov.scanStatus === 'Success' || ov.scanStatus === 'Error') {
        clearInterval(scanTimer!)
        scanTimer = undefined
        if (ov.scanStatus === 'Success') ElMessage.success('扫描完成，可查看漏洞报告')
        else ElMessage.error('扫描失败，请到 Harbor UI 查看扫描器日志')
      }
    } catch {
      clearInterval(scanTimer!)
      scanTimer = undefined
    }
  }, 8000)
}

async function openVulns(row: any) {
  vulnTarget.project = curProject.value
  vulnTarget.repo = fullRepo.value.slice(curProject.value.length + 1)
  vulnTarget.reference = row.tags?.[0]?.name || row.digest
  vulnRefLabel.value = `${fullRepo.value}:${vulnTarget.reference}`
  vulnDlg.value = true
  await reloadVulns()
}
async function reloadVulns() {
  vulnLoading.value = true
  try {
    vulnReport.value = await registryApi.vulns(vulnTarget.project, vulnTarget.repo, vulnTarget.reference)
  } catch (e: any) {
    vulnReport.value = null
    ElMessage.warning(e?.message || '报告读取失败（可能尚未扫描完成）')
  } finally {
    vulnLoading.value = false
  }
}
const saving = ref(false)
const testing = ref(false)
const cfg = reactive<{ url: string; username: string; password: string; insecure: boolean }>({ url: '', username: '', password: '', insecure: true })

const topTags = computed(() => artifacts.value.flatMap((a) => a.tags || []).slice(0, 5).map((t) => t.name))

function fmtTime(s?: string) {
  return s ? s.replace('T', ' ').slice(0, 19) : ''
}
function fmtSize(n?: number) {
  if (!n) return '-'
  if (n > 1 << 30) return (n / (1 << 30)).toFixed(2) + ' GiB'
  if (n > 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MiB'
  return (n / 1024).toFixed(0) + ' KiB'
}
async function copyRef(tag: string) {
  const ref = `${cfg.url.replace(/^https?:\/\//, '')}/${fullRepo.value}:${tag}`
  try {
    await navigator.clipboard.writeText(ref)
    ElMessage.success(`已复制 ${ref}`)
  } catch {
    ElMessage.info(ref)
  }
}

async function reload() {
  loading.value = true
  try {
    if (step.value === 0) projects.value = await registryApi.projects(search.value)
    else if (step.value === 1 && curProject.value) repos.value = await registryApi.repos(curProject.value, search.value)
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

function openProject(p: string) {
  curProject.value = p
  step.value = 1
  search.value = ''
  reload()
}
async function openRepo(name: string) {
  fullRepo.value = name
  loading.value = true
  step.value = 2
  try {
    artifacts.value = await registryApi.tags(curProject.value, name.slice(curProject.value.length + 1))
    for (const k of Object.keys(scanMap)) delete scanMap[k]
    loadScanOverviews()
  } finally {
    loading.value = false
  }
}

function openSettings() {
  cfgVisible.value = true
  registryApi.getConfig().then((c) => Object.assign(cfg, c, { password: '' })).catch(() => {})
}
async function saveCfg() {
  saving.value = true
  try {
    await registryApi.saveConfig(cfg)
    ElMessage.success('已保存')
    cfgVisible.value = false
    await init()
  } finally {
    saving.value = false
  }
}
async function testCfg() {
  testing.value = true
  try {
    await registryApi.saveConfig(cfg)
    const r = await registryApi.test()
    ElMessage.success(`连接成功，共 ${r.projects} 个项目`)
  } catch {
    /* 拦截器已提示 */
  } finally {
    testing.value = false
  }
}

async function init() {
  try {
    await registryApi.getConfig()
    configured.value = true
    await reload()
  } catch {
    configured.value = false
  }
}

onMounted(init)
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.toolbar { display: flex; gap: 8px; align-items: center; margin-bottom: 10px; }
.tag-header { font-size: 13px; margin-bottom: 8px; display: flex; align-items: center; gap: 4px; flex-wrap: wrap; }
.untagged { color: #909399; }
</style>
