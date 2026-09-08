<!-- 流水线设计器：左=节点面板，中=画布，右=属性面板，下=YAML/校验结果。
     工具栏：校验/生成 YAML/保存/运行 + JSON 导入导出 + 复制到项目。
     未保存修改：路由离开确认 + beforeunload。 -->
<template>
  <div class="designer" v-loading="!loaded">
    <el-alert v-if="loadError" type="error" :closable="false" style="margin-bottom: 8px"
      :title="loadError">
      <el-button size="small" @click="reload(pid)">重试</el-button>
    </el-alert>

    <div class="toolbar" v-if="loaded">
      <el-button size="small" @click="goBack">← 返回流水线列表</el-button>
      <span class="t-title">流水线设计</span>
      <el-tag v-if="latestVersion != null" type="primary" size="small">v{{ latestVersion }}</el-tag>
      <el-tag v-if="dirty" type="warning" size="small">未保存</el-tag>
      <el-button size="small" :loading="busy === 'validate'" :disabled="!!busy || !!loadError" @click="doValidate">校验</el-button>
      <el-button size="small" :loading="busy === 'compile'" :disabled="!!busy || !!loadError" @click="doCompile">生成 YAML</el-button>
      <el-button size="small" type="primary" :loading="busy === 'save'" :disabled="!!busy || !!loadError" @click="doSave">保存</el-button>
      <el-button size="small" type="success" plain :loading="busy === 'run'" :disabled="!!busy || !!loadError" @click="doRun">运行</el-button>
      <el-dropdown size="small" @command="onMore">
        <el-button size="small">更多<el-icon class="el-icon--right"><ArrowDown /></el-icon></el-button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="export">导出 JSON</el-dropdown-item>
            <el-dropdown-item command="import">导入 JSON</el-dropdown-item>
            <el-dropdown-item command="copy">复制流水线</el-dropdown-item>
            <el-dropdown-item command="duplicate">复制到其他项目</el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
      <el-button size="small" @click="openScheduleDlg">定时任务</el-button>
      <el-button size="small" @click="openWebhookDlg">Webhook 触发</el-button>
      <input ref="fileRef" type="file" accept="application/json,.json" style="display: none" @change="onImportFile" />
    </div>

    <div class="body" v-if="loaded">
      <NodePanel :graph="graph" @update="(n) => updateCanvas(n, graph.edges)" />
      <div class="canvas-box">
        <CiCanvas :nodes="graph.nodes" :edges="graph.edges" :selected="selectedNode"
          @change="updateCanvas" @select="selectNode" />
      </div>
      <PropertyPanel :node="selectedNode" :graph="graph" @params="onNodeParams" />
    </div>

    <div class="footer" v-if="loaded">
      <pre class="yaml-box">{{ yaml || '// 点击「生成 YAML」预览编译产物' }}</pre>
      <div v-if="errors.length" class="err-box">
        <div v-for="(e, i) in errors" :key="i" class="err-line">• {{ e }}</div>
      </div>
    </div>

    <!-- 运行对话框：分支 / Tag -->
    <el-dialog v-model="runDlg" title="运行流水线" width="460px" :close-on-click-modal="false">
      <div class="run-tip">选择分支或 tag（留空按流水线默认 {{ runRefs.def || 'main' }} 运行；可手输）</div>
      <el-radio-group v-model="runMode" style="margin-bottom: 10px" @change="runRef = ''">
        <el-radio-button value="branch">分支</el-radio-button>
        <el-radio-button value="tag">Tag</el-radio-button>
      </el-radio-group>
      <el-select
        v-model="runRef" filterable allow-create default-first-option clearable
        :placeholder="runMode === 'branch' ? '如 feature/xxx（留空=默认）' : '如 v1.2.0（留空=默认）'"
        style="width: 100%"
      >
        <el-option v-for="r in (runMode === 'branch' ? runRefs.branches : runRefs.tags)" :key="r" :label="r" :value="r" />
      </el-select>
      <div v-if="runRefs.err" class="run-err">{{ runRefs.err }}</div>
      <template #footer>
        <el-button @click="runDlg = false">取消</el-button>
        <el-button type="primary" :loading="runConfirming" @click="doRunConfirm">运 行</el-button>
      </template>
    </el-dialog>

    <!-- 复制到其他项目 -->
    <el-dialog v-model="dupDlg" :title="`复制到其他项目 — ${meta?.name ?? ''}`" width="460px">
      <div class="dup-tip">复制源流水线的最新版本（DSL 整体复制到目标项目；凭证引用按名称解析，请确保目标项目可用同名凭证）。</div>
      <el-form label-width="100px" size="small">
        <el-form-item label="目标项目" required>
          <el-select v-model="dupTarget" style="width: 100%" placeholder="选择目标项目">
            <el-option v-for="p in dupProjects" :key="p.id" :label="p.displayName ? `${p.name}（${p.displayName}）` : p.name" :value="p.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="新名称">
          <el-input v-model="dupName" :placeholder="(meta?.name ?? '') + '-copy'" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dupDlg = false">取消</el-button>
        <el-button type="primary" :loading="dupBusy" :disabled="!dupTarget" @click="doDuplicate">复制</el-button>
      </template>
    </el-dialog>

    <!-- 定时任务 -->
    <el-dialog v-model="schedDlg" :title="`定时任务 — ${meta?.name ?? ''}`" width="640px">
      <el-table :data="schedules" size="small" border style="margin-bottom: 12px">
        <el-table-column prop="cron" label="Cron" width="140">
          <template #default="{ row }"><span class="mono">{{ row.cron }}</span></template>
        </el-table-column>
        <el-table-column label="下次触发" width="160">
          <template #default="{ row }">{{ row.nextRunAt ? new Date(row.nextRunAt).toLocaleString('zh-CN', { hour12: false }) : '—' }}</template>
        </el-table-column>
        <el-table-column label="状态" width="80" align="center">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" size="small" :disabled="!canWrite" @change="(v: boolean) => toggleSchedule(row, v)" />
          </template>
        </el-table-column>
        <el-table-column label="最近错误" min-width="140" show-overflow-tooltip>
          <template #default="{ row }"><span :style="{ color: row.lastError ? '#f56c6c' : '' }">{{ row.lastError || '—' }}</span></template>
        </el-table-column>
        <el-table-column label="操作" width="70" align="center">
          <template #default="{ row }">
            <el-button size="small" text type="danger" :disabled="!canWrite" @click="delSchedule(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <el-form :inline="true" size="small" @submit.prevent="addSchedule">
        <el-form-item label="Cron（5 段）">
          <el-input v-model="newCron" placeholder="如 0 2 * * *（每天 02:00）" style="width: 240px" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :disabled="!canWrite || !newCron.trim()" @click="addSchedule">添加</el-button>
        </el-form-item>
      </el-form>
      <div class="cfg-tip">支持 5 段 cron（分 时 日 月 周）；到点以最新版本触发，triggerType=schedule。</div>
    </el-dialog>

    <!-- Webhook 触发 -->
    <el-dialog v-model="hookDlg" :title="`Webhook 触发 — ${meta?.name ?? ''}`" width="680px">
      <el-table :data="webhooks" size="small" border style="margin-bottom: 12px">
        <el-table-column label="回调地址" min-width="300" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="mono">{{ row.url || ('/api/ci/webhook/' + row.token) }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="branch" label="分支过滤" width="110">
          <template #default="{ row }">{{ row.branch || '全部' }}</template>
        </el-table-column>
        <el-table-column label="启用" width="70" align="center">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" size="small" :disabled="!canWrite" @change="(v: boolean) => toggleWebhook(row, v)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="70" align="center">
          <template #default="{ row }">
            <el-button size="small" text type="danger" :disabled="!canWrite" @click="delWebhook(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <el-form :inline="true" size="small" @submit.prevent="addWebhook">
        <el-form-item label="分支过滤（可选）">
          <el-input v-model="newHookBranch" placeholder="留空 = 全部分支" style="width: 180px" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :disabled="!canWrite" @click="addWebhook">生成 Webhook</el-button>
        </el-form-item>
      </el-form>
      <div class="cfg-tip">GitLab：项目 → Settings → Webhooks → 填回调地址（凭据在 URL 中）。push / tag / MR(open、reopen、update) 触发，同事件自动去重。</div>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowDown } from '@element-plus/icons-vue'
import {
  ciApi, type CIGraph, type CIPNode, type CIProject, type CISchedule, type CIWebhook,
} from '../../../api/ci'
import { useUserStore } from '../../../store/user'
import CiCanvas from './CiCanvas.vue'
import NodePanel from './NodePanel.vue'
import PropertyPanel from './PropertyPanel.vue'
import { normalizeDuplicateIds } from '../graphUtils'
import { ciDesignDirty, setCiDesignGuard } from '../dirtyGuard'

const EMPTY_GRAPH: CIGraph = { name: 'unnamed', version: 1, nodes: [], edges: [] }

const route = useRoute()
const router = useRouter()
const pid = computed(() => Number(route.params.id))

// ---- 画布状态（原 zustand store 的职责） ----
const graph = ref<CIGraph>(EMPTY_GRAPH)
const selectedNode = ref<CIPNode | null>(null)
const latestVersion = ref<number | null>(null)
const loaded = ref(false)
const loadError = ref('')
const snapshot = ref('')
const yaml = ref('')
const errors = ref<string[]>([])
const busy = ref<'' | 'validate' | 'compile' | 'save' | 'run'>('')
const meta = ref<{ projectId: number; name: string; description?: string } | null>(null)
let loadSeq = 0

const dirty = computed(() => loaded.value && !loadError.value && JSON.stringify(graph.value) !== snapshot.value)
ciDesignDirty.value = dirty.value
watch(dirty, (v) => { ciDesignDirty.value = v })

function updateCanvas(nodes: CIPNode[], edges: CIGraph['edges']) {
  graph.value = { ...graph.value, nodes, edges }
}
function selectNode(n: CIPNode | null) { selectedNode.value = n }
function onNodeParams(params: Record<string, unknown>) {
  const n = selectedNode.value
  if (!n) return
  graph.value = { ...graph.value, nodes: graph.value.nodes.map((x) => (x.id === n.id ? { ...x, params } : x)) }
}

// 路由守卫注册：脏时离开弹确认
setCiDesignGuard(async (msg) => {
  try {
    await ElMessageBox.confirm(msg, '有未保存的修改', { confirmButtonText: '离开', cancelButtonText: '留在此页', type: 'warning' })
    return true
  } catch { return false }
})
onBeforeUnmount(() => setCiDesignGuard(null))
// 浏览器刷新/关闭拦截
function onBeforeUnload(e: BeforeUnloadEvent) {
  if (dirty.value) { e.preventDefault(); e.returnValue = '' }
}
onMounted(() => window.addEventListener('beforeunload', onBeforeUnload))
onBeforeUnmount(() => window.removeEventListener('beforeunload', onBeforeUnload))

// 进入页面加载已有 DSL（竞态守卫：快速切换时晚返回的旧响应不得覆盖新图）
async function reload(id: number) {
  const seq = ++loadSeq
  loaded.value = false
  loadError.value = ''
  meta.value = null
  yaml.value = ''
  errors.value = []
  graph.value = EMPTY_GRAPH
  selectedNode.value = null
  latestVersion.value = null
  try {
    const d = await ciApi.pipeline(id)
    if (seq !== loadSeq) return
    meta.value = { projectId: d.pipeline.projectId, name: d.pipeline.name, description: d.pipeline.description }
    const raw = d.graph ?? { name: 'unnamed', version: 1, nodes: [], edges: [] }
    const fix = normalizeDuplicateIds(raw)
    if (fix.changed) ElMessage.warning('检测到重复的节点 id（历史数据），已自动修复；点击「保存」后生效')
    const g: CIGraph = fix.changed
      ? { ...raw, nodes: fix.nodes, edges: fix.edges }
      : raw
    graph.value = g
    latestVersion.value = d.latestVersion?.version ?? null
    snapshot.value = JSON.stringify(g)
    loaded.value = true
  } catch (e: any) {
    if (seq !== loadSeq) return
    graph.value = EMPTY_GRAPH
    snapshot.value = JSON.stringify(EMPTY_GRAPH)
    loaded.value = true
    loadError.value = e?.message || '流水线加载失败'
  }
}
onMounted(() => { if (pid.value) void reload(pid.value) })
watch(pid, (v) => { if (v) void reload(v) })

// ---- 校验 / 编译 / 保存 / 运行（互斥防重复提交） ----
async function doValidate() {
  busy.value = 'validate'
  try {
    const res = await ciApi.validate(pid.value, graph.value)
    errors.value = res.valid ? [] : res.errors
    if (res.valid) ElMessage.success('校验通过')
  } catch (e: any) {
    errors.value = [e?.message || '校验请求失败']
  } finally { busy.value = '' }
}
async function doCompile() {
  busy.value = 'compile'
  try {
    yaml.value = await ciApi.compile(pid.value, graph.value)
  } catch (e: any) {
    errors.value = [e?.message || '编译失败']
  } finally { busy.value = '' }
}
async function save(): Promise<number | null> {
  const v = await ciApi.saveVersion(pid.value, graph.value)
  latestVersion.value = v.version
  snapshot.value = JSON.stringify(graph.value)
  return v.version
}
async function doSave() {
  busy.value = 'save'
  try {
    const v = await save()
    if (v != null) ElMessage.success(`已保存为版本 v${v}`)
  } catch (e: any) {
    ElMessage.error(e?.message || '保存失败')
  } finally { busy.value = '' }
}
// 运行：画布与最近保存不一致（含从未保存）先自动保存
async function doRun() {
  busy.value = 'run'
  try {
    if (latestVersion.value == null || dirty.value) await save()
    busy.value = ''
    openRunModal()
  } catch (e: any) {
    ElMessage.error(e?.message || '保存失败，未触发执行')
    busy.value = ''
  }
}

// ---- 运行对话框 ----
const runDlg = ref(false)
const runMode = ref<'branch' | 'tag'>('branch')
const runRef = ref('')
const runConfirming = ref(false)
const runRefs = reactive<{ branches: string[]; tags: string[]; def: string; err?: string }>({ branches: [], tags: [], def: 'main' })
function openRunModal() {
  runDlg.value = true
  runMode.value = 'branch'
  runRef.value = ''
  runRefs.branches = []
  runRefs.tags = []
  runRefs.err = undefined
  const git = graph.value.nodes.find((n) => n.type === 'git-clone')
  const url = (git?.params?.url as string) || ''
  runRefs.def = (git?.params?.branch as string) || 'main'
  if (!/^https?:\/\//i.test(url)) {
    runRefs.err = '流水线未配置 git 仓库地址，按默认分支运行'
    return
  }
  void ciApi.repoRefs(url, (git?.params?.credential as string) || undefined)
    .then((refs) => {
      runRefs.branches = refs.branches
      runRefs.tags = refs.tags
    })
    .catch((e: any) => { runRefs.err = `分支列表加载失败（${e?.message}），可手输` })
}
async function doRunConfirm() {
  const refV = runRef.value.trim()
  runConfirming.value = true
  try {
    const run = await ciApi.run(pid.value, refV ? { [runMode.value]: refV } : undefined)
    ElMessage.success(`已触发执行 #${run.runNo}${refV ? `（${runMode.value === 'branch' ? '分支' : 'tag'}: ${refV}）` : ''}`)
    runDlg.value = false
    router.push({ path: '/ci', query: { tab: 'runs', focus: String(run.id) } })
  } catch (e: any) {
    ElMessage.error(e?.message || '触发执行失败')
  } finally { runConfirming.value = false }
}

// ---- 导入 / 导出 / 复制 ----
const fileRef = ref<HTMLInputElement | null>(null)
function onMore(cmd: string) {
  if (cmd === 'export') doExport()
  else if (cmd === 'import') fileRef.value?.click()
  else if (cmd === 'copy') void doCopy()
  else if (cmd === 'duplicate') void openDup()
}
function doExport() {
  const blob = new Blob([JSON.stringify(graph.value, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `${meta.value?.name ?? 'pipeline'}-dsl.json`
  a.click()
  URL.revokeObjectURL(url)
}
function onImportFile(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  ;(e.target as HTMLInputElement).value = ''
  if (!f) return
  const reader = new FileReader()
  reader.onload = () => {
    try {
      const g = JSON.parse(String(reader.result)) as CIGraph
      if (!Array.isArray(g.nodes) || !Array.isArray(g.edges)) throw new Error('缺少 nodes/edges')
      void ElMessageBox.confirm(`将用文件中的 ${g.nodes.length} 个节点覆盖当前画布（未保存的修改会丢失），确定导入？`, '导入 JSON', {
        confirmButtonText: '导入', cancelButtonText: '取消', type: 'warning',
      }).then(() => {
        const fix = normalizeDuplicateIds(g)
        graph.value = { name: g.name || graph.value.name, version: g.version || 1, nodes: fix.nodes, edges: fix.edges }
        if (fix.changed) ElMessage.warning('导入内容存在重复的节点 id，已自动修复')
        ElMessage.success('已导入，点击「保存」生效')
      }).catch(() => undefined)
    } catch (err: any) {
      ElMessage.error(`导入失败: ${err?.message || '格式错误'}`)
    }
  }
  reader.readAsText(f)
}
async function doCopy() {
  if (!meta.value) return
  try {
    const np = await ciApi.createPipeline(meta.value.projectId, `${meta.value.name}-copy`, meta.value.description ?? '')
    await ciApi.saveVersion(np.id, graph.value)
    ElMessage.success(`已复制为流水线 #${np.id}`)
    router.push(`/ci/pipelines/${np.id}/design`)
  } catch (e: any) {
    ElMessage.error(e?.message || '复制失败')
  }
}

// 复制到目标项目
const dupDlg = ref(false)
const dupBusy = ref(false)
const dupTarget = ref<number | undefined>()
const dupName = ref('')
const dupProjects = ref<CIProject[]>([])
async function openDup() {
  if (!meta.value) return
  dupDlg.value = true
  dupTarget.value = undefined
  dupName.value = ''
  try {
    const ps = await ciApi.projects()
    dupProjects.value = ps.filter((p) => p.id !== meta.value!.projectId)
  } catch (e: any) {
    ElMessage.error(e?.message || '项目列表加载失败')
  }
}
async function doDuplicate() {
  if (!dupTarget.value) return
  dupBusy.value = true
  try {
    await ciApi.duplicatePipeline(pid.value, { targetProjectId: dupTarget.value, name: dupName.value || undefined })
    ElMessage.success('已复制到目标项目')
    dupDlg.value = false
  } catch (e: any) {
    ElMessage.error(e?.message || '复制失败')
  } finally { dupBusy.value = false }
}

// ---- 定时任务 ----
const schedDlg = ref(false)
const schedules = ref<CISchedule[]>([])
const newCron = ref('')
const canWrite = computed(() => useUserStore().isAdmin)
async function openScheduleDlg() {
  schedDlg.value = true
  try { schedules.value = await ciApi.schedules(pid.value) } catch { schedules.value = [] }
}
async function addSchedule() {
  try {
    const sch = await ciApi.createSchedule({ pipelineId: pid.value, cron: newCron.value.trim() })
    schedules.value.unshift(sch)
    newCron.value = ''
    ElMessage.success('已添加定时任务')
  } catch (e: any) {
    ElMessage.error(e?.message || '添加失败')
  }
}
async function toggleSchedule(row: CISchedule, v: boolean) {
  try { await ciApi.updateSchedule(row.id, { enabled: v }) } catch { row.enabled = !v }
}
async function delSchedule(row: CISchedule) {
  await ElMessageBox.confirm(`删除定时任务 ${row.cron}？`, '删除', { type: 'warning' })
  await ciApi.deleteSchedule(row.id)
  schedules.value = schedules.value.filter((x) => x.id !== row.id)
}

// ---- Webhook ----
const hookDlg = ref(false)
const webhooks = ref<CIWebhook[]>([])
const newHookBranch = ref('')
async function openWebhookDlg() {
  hookDlg.value = true
  try { webhooks.value = await ciApi.webhooks(pid.value) } catch { webhooks.value = [] }
}
async function addWebhook() {
  try {
    const wh = await ciApi.createWebhook({ pipelineId: pid.value, branch: newHookBranch.value.trim() || undefined })
    webhooks.value.unshift(wh)
    newHookBranch.value = ''
    ElMessage.success('已生成 Webhook')
  } catch (e: any) {
    ElMessage.error(e?.message || '生成失败')
  }
}
async function toggleWebhook(row: CIWebhook, v: boolean) {
  try { await ciApi.updateWebhook(row.id, { enabled: v }) } catch { row.enabled = !v }
}
async function delWebhook(row: CIWebhook) {
  await ElMessageBox.confirm('删除该 Webhook？回调地址立即失效。', '删除', { type: 'warning' })
  await ciApi.deleteWebhook(row.id)
  webhooks.value = webhooks.value.filter((x) => x.id !== row.id)
}

function goBack() {
  if (meta.value) router.push({ path: '/ci', query: { tab: 'pipelines', project: String(meta.value.projectId) } })
  else router.push({ path: '/ci', query: { tab: 'pipelines' } })
}
</script>

<style scoped>
.designer { display: flex; flex-direction: column; height: calc(100vh - 100px); }
.toolbar { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; flex-wrap: wrap; }
.t-title { font-weight: 700; margin-right: 4px; }
.body { display: flex; flex: 1; min-height: 0; gap: 8px; }
.canvas-box { flex: 1; border: 1px solid #ebeef5; border-radius: 8px; overflow: hidden; }
.footer { margin-top: 8px; display: flex; gap: 8px; }
.yaml-box {
  flex: 1; margin: 0; background: #fafafa; padding: 8px; border-radius: 8px;
  max-height: 160px; overflow: auto; font-size: 12px; white-space: pre-wrap; word-break: break-all;
}
.err-box { width: 280px; background: #fef0f0; padding: 8px; border-radius: 8px; max-height: 160px; overflow: auto; }
.err-line { color: #cf1322; font-size: 12px; }
.run-tip { margin-bottom: 10px; color: #606266; font-size: 13px; }
.run-err { margin-top: 8px; color: #e6a23c; font-size: 12px; }
.dup-tip { font-size: 12px; color: #909399; margin-bottom: 12px; }
.mono { font-family: ui-monospace, Consolas, monospace; font-size: 12px; }
.cfg-tip { color: #909399; font-size: 12px; }
</style>
