<template>
  <div>
    <div class="ops-bar">
      <el-button size="small" :disabled="!writable" type="primary" @click="openCreateBackup">立即备份</el-button>
      <el-button size="small" :disabled="!writable" @click="openSchedule">备份计划</el-button>
      <el-button size="small" :disabled="!writable" @click="openRestore">恢复到新集群</el-button>
      <el-button size="small" :disabled="!writable" @click="openRebuild">重建实例</el-button>
      <el-button size="small" :loading="loading" @click="loadAll">刷新</el-button>
      <span class="tip">{{ repoNote }}</span>
      <!-- 没仓库时备份一定失败，直接给到配置入口，别让人去猜是谁的锅 -->
      <el-button v-if="!repos.length" size="small" text type="primary" @click="gotoRepos">配置备份仓库</el-button>
    </div>

    <!-- 备份计划概览 -->
    <div class="sec-title">备份策略与计划</div>
    <el-table border :data="policies" size="small" v-loading="loading">
      <el-table-column prop="component" label="组件" width="140" />
      <el-table-column label="可用备份方法" min-width="260">
        <template #default="{ row }">
          <el-tag v-for="m in row.methods" :key="m.name" size="small" effect="plain" style="margin-right: 4px">
            {{ m.name }}<span v-if="m.snapshot" class="tip">（快照）</span>
          </el-tag>
          <span v-if="!row.methods?.length" class="tip">-</span>
        </template>
      </el-table-column>
      <el-table-column label="计划" min-width="280">
        <template #default="{ row }">
          <template v-if="scheduleOf(row.component)?.items?.length">
            <div v-for="it in scheduleOf(row.component)!.items" :key="it.name" class="sched-line">
              <el-tag size="small" :type="it.enabled ? 'success' : 'info'">{{ it.enabled ? '启用' : '停用' }}</el-tag>
              <span class="mono">{{ it.method }}</span> @ <span class="mono">{{ it.cronExpression }}</span>
              <span class="tip">保留 {{ it.retention || '默认' }}</span>
            </div>
          </template>
          <span v-else class="tip">未配置定时计划（可点上方「备份计划」开启）</span>
        </template>
      </el-table-column>
      <el-table-column prop="repoName" label="仓库" width="120">
        <template #default="{ row }"><span v-if="row.repoName">{{ row.repoName }}</span><span v-else class="tip">默认仓库</span></template>
      </el-table-column>
    </el-table>

    <!-- 备份记录 -->
    <div class="sec-title">备份记录</div>
    <el-table border :data="backups" size="small" v-loading="loading">
      <el-table-column prop="name" label="备份" min-width="220" show-overflow-tooltip />
      <el-table-column prop="method" label="方法" width="130" />
      <el-table-column label="状态" width="110">
        <template #default="{ row }">
          <el-tag size="small" :type="phaseType(row.phase)">{{ row.phase || '-' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="totalSize" label="大小" width="90" />
      <el-table-column prop="createdAt" label="创建" width="160" />
      <el-table-column prop="completedAt" label="完成" width="160" />
      <el-table-column prop="expiration" label="过期" width="160" />
      <el-table-column label="操作" width="118" fixed="right">
        <template #default="{ row }">
          <el-button size="small" text type="primary" @click="showDetail(row)">详情</el-button>
          <el-button size="small" text type="danger" :disabled="!writable" @click="delBackup(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 恢复记录 -->
    <div class="sec-title">恢复任务</div>
    <el-table border :data="restores" size="small" v-loading="loading">
      <el-table-column prop="name" label="任务" min-width="220" show-overflow-tooltip />
      <el-table-column prop="backup" label="来源备份" min-width="180" show-overflow-tooltip />
      <el-table-column label="状态" width="110">
        <template #default="{ row }">
          <el-tag size="small" :type="phaseType(row.phase)">{{ row.phase || '-' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="duration" label="耗时" width="100" />
      <el-table-column prop="createdAt" label="开始" width="160" />
      <el-table-column prop="message" label="信息" min-width="200" show-overflow-tooltip />
    </el-table>

    <!-- 立即备份 -->
    <el-dialog v-model="createDlg" title="立即备份" width="480px">
      <el-form label-width="96px" size="small">
        <el-form-item label="组件" required>
          <el-select v-model="create.component" style="width: 240px" @change="onCreateCompChange">
            <el-option v-for="p in policies" :key="p.component" :label="p.component" :value="p.component" />
          </el-select>
        </el-form-item>
        <el-form-item label="备份方法">
          <el-select v-model="create.method" style="width: 240px">
            <el-option v-for="m in curMethods" :key="m.name" :label="m.name" :value="m.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="保留期">
          <el-input v-model="create.retention" placeholder="如 7d（留空用策略默认）" style="width: 240px" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createDlg = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="doCreateBackup">创建备份</el-button>
      </template>
    </el-dialog>

    <!-- 备份计划编辑 -->
    <el-dialog v-model="schedDlg" title="备份计划（启停 / cron / 保留期）" width="640px">
      <el-form label-width="96px" size="small">
        <el-form-item label="组件" required>
          <el-select v-model="schedComp" style="width: 240px" @change="onSchedCompChange">
            <el-option v-for="s in schedules" :key="s.component" :label="s.component" :value="s.component" />
          </el-select>
        </el-form-item>
      </el-form>
      <el-table border :data="schedItems" size="small">
        <el-table-column prop="method" label="方法" width="150" />
        <el-table-column label="启用" width="70">
          <template #default="{ row }"><el-switch v-model="row.enabled" /></template>
        </el-table-column>
        <el-table-column label="cron（5 段）" min-width="180">
          <template #default="{ row }"><el-input v-model="row.cronExpression" size="small" placeholder="0 18 * * 0" /></template>
        </el-table-column>
        <el-table-column label="保留期" width="120">
          <template #default="{ row }"><el-input v-model="row.retention" size="small" placeholder="7d" /></template>
        </el-table-column>
      </el-table>
      <template #footer>
        <el-button @click="schedDlg = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="doSaveSchedule">保存</el-button>
      </template>
    </el-dialog>

    <!-- 恢复到新集群 -->
    <el-dialog v-model="restoreDlg" title="用备份恢复出新集群" width="520px">
      <el-form label-width="110px" size="small">
        <el-form-item label="新集群名" required>
          <el-input v-model="restore.name" :placeholder="`如 ${cluster.name}-restored`" style="width: 300px" />
        </el-form-item>
        <el-form-item label="组件" required>
          <el-select v-model="restore.component" style="width: 240px">
            <el-option v-for="p in policies" :key="p.component" :label="p.component" :value="p.component" />
          </el-select>
          <div class="tip">备份按组件生成；多组件集群的其余组件不会带数据</div>
        </el-form-item>
        <el-form-item label="来源备份" required>
          <el-select v-model="restore.backupName" filterable style="width: 300px">
            <el-option v-for="b in doneBackups" :key="b.name" :label="`${b.name}（${b.method}，${b.createdAt}）`" :value="b.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="卷恢复策略">
          <el-select v-model="restore.volumeRestorePolicy" style="width: 200px">
            <el-option label="Parallel（并行）" value="Parallel" />
            <el-option label="Sequential（串行）" value="Sequential" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="restoreDlg = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="doRestore">恢复</el-button>
      </template>
    </el-dialog>

    <!-- 重建实例 -->
    <el-dialog v-model="rebuildDlg" title="用备份重建组件实例" width="480px">
      <el-form label-width="110px" size="small">
        <el-form-item label="组件" required>
          <el-select v-model="rebuild.component" style="width: 240px">
            <el-option v-for="c in cluster.components" :key="c.name" :label="c.name" :value="c.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="来源备份" required>
          <el-select v-model="rebuild.backupName" filterable style="width: 300px">
            <el-option v-for="b in doneBackups" :key="b.name" :label="`${b.name}（${b.method}，${b.createdAt}）`" :value="b.name" />
          </el-select>
        </el-form-item>
        <el-form-item label="原地重建">
          <el-switch v-model="rebuild.inPlace" />
          <span class="tip" style="margin-left: 8px">关闭则先起新实例、就绪后下线旧实例</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="rebuildDlg = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="doRebuild">重建</el-button>
      </template>
    </el-dialog>

    <!-- 备份详情 -->
    <el-drawer v-model="detailDlg" :title="`备份：${cur?.name}`" size="46%">
      <div v-if="cur">
        <el-descriptions :column="2" border size="small">
          <el-descriptions-item label="状态"><el-tag size="small" :type="phaseType(cur.phase)">{{ cur.phase }}</el-tag></el-descriptions-item>
          <el-descriptions-item label="大小">{{ cur.totalSize || '-' }}</el-descriptions-item>
          <el-descriptions-item label="方法">{{ cur.method }}</el-descriptions-item>
          <el-descriptions-item label="仓库">{{ cur.repo || '-' }}</el-descriptions-item>
          <el-descriptions-item label="开始">{{ cur.startedAt || '-' }}</el-descriptions-item>
          <el-descriptions-item label="完成">{{ cur.completedAt || '-' }}</el-descriptions-item>
          <el-descriptions-item label="过期时间">{{ cur.expiration || '-' }}</el-descriptions-item>
          <el-descriptions-item label="卷快照">{{ cur.volumeSnapshots || 0 }}</el-descriptions-item>
        </el-descriptions>
        <div v-if="cur.failureReason" class="err-box" style="margin-top: 10px">{{ cur.failureReason }}</div>
        <div class="sec-title">执行明细</div>
        <el-table border :data="cur.actions || []" size="small">
          <el-table-column prop="name" label="Action" min-width="150" />
          <el-table-column prop="phase" label="状态" width="90" />
          <el-table-column prop="targetPod" label="目标 Pod" min-width="160" show-overflow-tooltip />
          <el-table-column prop="totalSize" label="大小" width="90" />
          <el-table-column prop="failureReason" label="原因" min-width="180" show-overflow-tooltip />
        </el-table>
      </div>
    </el-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { confirmDelete } from '../../utils/confirm'
import {
  kbApi,
  type KBCluster,
  type KBBackupPolicyView,
  type KBBackupView,
  type KBBackupDetail,
  type KBRestoreView,
  type KBScheduleItem,
  type KBScheduleView,
  type KBRepoView,
} from '../../api/kb'

const props = defineProps<{ cluster: KBCluster; writable: boolean }>()
const router = useRouter()
const gotoRepos = () => router.push('/kb/repos')
const emit = defineEmits<{ (e: 'changed'): void }>()

const loading = ref(false)
const submitting = ref(false)
const policies = ref<KBBackupPolicyView[]>([])
const schedules = ref<KBScheduleView[]>([])
const backups = ref<KBBackupView[]>([])
const restores = ref<KBRestoreView[]>([])
const repos = ref<KBRepoView[]>([])

const repoNote = computed(() => {
  if (!repos.value.length) return '未配置 BackupRepo（备份仓库），无法备份——需管理员先创建'
  // KubeBlocks 1.0.x 的 BackupRepo 就绪态是 Ready（0.9 时代叫 Available）
  const bad = repos.value.filter((r) => r.phase !== 'Available' && r.phase !== 'Ready' && r.phase !== '')
  return bad.length ? `存在不可用的备份仓库（${bad.map((r) => r.name).join(', ')}），备份可能失败` : ''
})
const doneBackups = computed(() => backups.value.filter((b) => b.phase === 'Completed'))
const curMethods = computed(() => policies.value.find((p) => p.component === create.component)?.methods || [])
const scheduleOf = (comp: string) => schedules.value.find((s) => s.component === comp)

async function loadAll() {
  const { namespace, name } = props.cluster
  loading.value = true
  try {
    const [p, s, b, r, repo] = await Promise.all([
      kbApi.backupPolicies(namespace, name),
      kbApi.backupSchedule(namespace, name).catch(() => [] as KBScheduleView[]),
      kbApi.backups(namespace, name),
      kbApi.restores(namespace, name).catch(() => [] as KBRestoreView[]),
      kbApi.backupRepos().catch(() => [] as KBRepoView[]),
    ])
    policies.value = p || []
    schedules.value = s || []
    backups.value = b || []
    restores.value = r || []
    repos.value = repo || []
  } catch { /* 拦截器已提示 */ } finally { loading.value = false }
}
onMounted(loadAll)
watch(() => props.cluster.name, loadAll)

function phaseType(p: string) {
  if (p === 'Completed' || p === 'Succeed' || p === 'Available') return 'success'
  if (p === 'Running' || p === 'Pending' || p === 'ConditionTrue') return 'warning'
  if (p === 'Failed' || p === 'Aborted' || p === 'Error') return 'danger'
  return 'info'
}

// ---------- 立即备份 ----------
const createDlg = ref(false)
const create = reactive({ component: '', method: '', retention: '' })
function openCreateBackup() {
  create.component = policies.value[0]?.component || props.cluster.components[0]?.name || ''
  onCreateCompChange()
  create.retention = ''
  createDlg.value = true
}
function onCreateCompChange() { create.method = curMethods.value[0]?.name || '' }
async function doCreateBackup() {
  submitting.value = true
  try {
    await kbApi.createBackup(props.cluster.namespace, props.cluster.name, {
      component: create.component, method: create.method, retention: create.retention,
    })
    ElMessage.success('备份任务已提交')
    createDlg.value = false
    loadAll()
  } catch { /* 拦截器已提示 */ } finally { submitting.value = false }
}

// ---------- 备份计划 ----------
const schedDlg = ref(false)
const schedComp = ref('')
const schedItems = ref<KBScheduleItem[]>([])
function openSchedule() {
  schedComp.value = schedules.value[0]?.component || ''
  onSchedCompChange()
  schedDlg.value = true
}
function onSchedCompChange() {
  schedItems.value = (scheduleOf(schedComp.value)?.items || []).map((i) => ({ ...i }))
}
async function doSaveSchedule() {
  submitting.value = true
  try {
    await kbApi.updateBackupSchedule(props.cluster.namespace, props.cluster.name, {
      component: schedComp.value, items: schedItems.value,
    })
    ElMessage.success('备份计划已更新')
    schedDlg.value = false
    loadAll()
  } catch { /* 拦截器已提示 */ } finally { submitting.value = false }
}

// ---------- 恢复到新集群 ----------
const restoreDlg = ref(false)
const restore = reactive({
  name: '', component: '', backupName: '', volumeRestorePolicy: 'Parallel',
})
function openRestore() {
  restore.name = `${props.cluster.name}-restored`
  restore.component = policies.value[0]?.component || ''
  restore.backupName = doneBackups.value[0]?.name || ''
  restoreDlg.value = true
}
async function doRestore() {
  if (!restore.name || !restore.backupName) { ElMessage.warning('新集群名与来源备份必填'); return }
  submitting.value = true
  try {
    const res = await kbApi.restoreCreate(props.cluster.namespace, props.cluster.name, { ...restore })
    ElMessage.success(`恢复集群 ${res.cluster} 已创建，数据面由 operator 接管`)
    restoreDlg.value = false
    emit('changed')
    loadAll()
  } catch { /* 拦截器已提示 */ } finally { submitting.value = false }
}

// ---------- 重建实例 ----------
const rebuildDlg = ref(false)
const rebuild = reactive({ component: '', backupName: '', inPlace: false })
function openRebuild() {
  rebuild.component = props.cluster.components[0]?.name || ''
  rebuild.backupName = doneBackups.value[0]?.name || ''
  rebuildDlg.value = true
}
async function doRebuild() {
  if (!rebuild.backupName) { ElMessage.warning('请选择来源备份'); return }
  submitting.value = true
  try {
    await kbApi.rebuild(props.cluster.namespace, props.cluster.name, { ...rebuild })
    ElMessage.success('重建工单已下发，进度见「运维」页')
    rebuildDlg.value = false
  } catch { /* 拦截器已提示 */ } finally { submitting.value = false }
}

// ---------- 详情 / 删除 ----------
const detailDlg = ref(false)
const cur = ref<KBBackupDetail | null>(null)
async function showDetail(row: KBBackupView) {
  try {
    cur.value = await kbApi.backupDetail(props.cluster.namespace, props.cluster.name, row.name)
    detailDlg.value = true
  } catch { /* 拦截器已提示 */ }
}
async function delBackup(row: KBBackupView) {
  // 备份是出事时的最后一道保险，删错一次就少一个恢复点：要求完整输入备份名
  try {
    await confirmDelete(row.name, {
      title: '删除备份',
      warning: `删除 ${row.name}（${row.component || '-'}，${row.totalSize || row.phase}）后该恢复点永久失效`,
    })
  } catch { return }
  await kbApi.deleteBackup(props.cluster.namespace, props.cluster.name, row.name)
  ElMessage.success('已删除')
  loadAll()
}
</script>

<style scoped>
.ops-bar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.tip { font-size: 13px; color: var(--el-text-color-secondary); }
.mono { font-family: ui-monospace, Consolas, monospace; font-size: 13px; }
.sec-title { font-weight: 600; margin: 14px 0 6px; font-size: 13px; }
.sched-line { display: flex; align-items: center; gap: 6px; line-height: 22px; }
.err-box { background: var(--kc-tint-coral); color: var(--kc-crit-text); padding: 8px 10px; border-radius: 4px; font-size: 13px; white-space: pre-wrap; }
</style>
