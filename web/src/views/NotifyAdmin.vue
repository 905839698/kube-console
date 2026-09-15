<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>通知渠道（CI 执行事件 → 钉钉 / Webhook）</span>
        <div class="header-right">
          <el-button :icon="Refresh" circle @click="load" />
          <el-button type="primary" size="default" @click="openCreate"><el-icon><Plus /></el-icon>&nbsp;新建渠道</el-button>
        </div>
      </div>
    </template>

    <el-tabs v-model="tab">
      <el-tab-pane label="通知渠道" name="channels">
        <el-table border :data="channels" size="small" stripe>
          <el-table-column prop="name" label="名称" min-width="140" />
          <el-table-column prop="type" label="类型" width="110" align="center">
            <template #default="{ row }">
              <el-tag size="small" :type="row.type === 'dingtalk' ? 'primary' : 'info'">{{ row.type === 'dingtalk' ? '钉钉机器人' : '通用 Webhook' }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="webhook" label="Webhook" min-width="260" show-overflow-tooltip />
          <el-table-column prop="minSeverity" label="最低级别" width="90" align="center" />
          <el-table-column label="启用" width="70" align="center">
            <template #default="{ row }"><el-tag size="small" :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '是' : '否' }}</el-tag></template>
          </el-table-column>
          <el-table-column label="操作" width="170" fixed="right">
            <template #default="{ row }">
              <el-button size="small" text type="primary" @click="testCh(row)">测试</el-button>
              <el-button size="small" text @click="openEdit(row)">编辑</el-button>
              <el-button size="small" text type="danger" @click="removeCh(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
        <el-alert type="info" :closable="false" style="margin-top: 10px"
          title="本页渠道用于 CI 流水线执行事件（成功/失败/待审批）的通知推送；K8s 告警通知已迁移至 Alertmanager（见「告警」页的实时告警/静默/配置）。钉钉机器人需在安全设置里开启「加签」并填入密钥。" />
      </el-tab-pane>

      <el-tab-pane label="通知记录" name="logs">
        <el-table border :data="logs" size="small" stripe v-loading="logsLoading">
          <el-table-column prop="sentAt" label="时间" width="170">
            <template #default="{ row }">{{ fmtTime(row.sentAt) }}</template>
          </el-table-column>
          <el-table-column prop="channelName" label="渠道" width="130" />
          <el-table-column prop="cluster" label="集群" width="110" />
          <el-table-column prop="alertName" label="告警" min-width="180" show-overflow-tooltip />
          <el-table-column prop="severity" label="级别" width="90" align="center">
            <template #default="{ row }">
              <el-tag size="small" :type="row.severity === 'critical' ? 'danger' : 'warning'">{{ row.severity }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="结果" width="80" align="center">
            <template #default="{ row }"><el-tag size="small" :type="row.ok ? 'success' : 'danger'">{{ row.ok ? '成功' : '失败' }}</el-tag></template>
          </el-table-column>
          <el-table-column type="expand">
            <template #default="{ row }">
              <pre class="msg-pre">{{ row.message }}</pre>
              <div v-if="row.error" class="msg-err">{{ row.error }}</div>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>
    </el-tabs>

    <!-- 渠道编辑 -->
    <el-dialog v-model="dlgVisible" :title="form.id ? '编辑渠道' : '新建渠道'" width="520px">
      <el-form label-width="100px" size="small">
        <el-form-item label="名称"><el-input v-model="form.name" /></el-form-item>
        <el-form-item label="类型">
          <el-select v-model="form.type" style="width: 100%">
            <el-option label="钉钉机器人" value="dingtalk" />
            <el-option label="通用 Webhook" value="webhook" />
          </el-select>
        </el-form-item>
        <el-form-item label="Webhook"><el-input v-model="form.webhook" placeholder="https://oapi.dingtalk.com/robot/send?access_token=..." /></el-form-item>
        <el-form-item label="加签密钥" v-if="form.type === 'dingtalk'"><el-input v-model="form.secret" placeholder="SEC 开头，留空保持不变" /></el-form-item>
        <el-form-item label="最低级别">
          <el-select v-model="form.minSeverity" style="width: 100%">
            <el-option v-for="s in ['info', 'warning', 'critical']" :key="s" :label="s" :value="s" />
          </el-select>
        </el-form-item>
        <el-form-item label="启用"><el-switch v-model="form.enabled" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlgVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </el-card>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { notifyApi, type NotifyChannelItem, type NotifyLogItem } from '../api'
import { confirmDelete } from '../utils/confirm'

const tab = ref('channels')
const channels = ref<NotifyChannelItem[]>([])
const logs = ref<NotifyLogItem[]>([])
const logsLoading = ref(false)
const dlgVisible = ref(false)
const saving = ref(false)
const form = reactive<Partial<NotifyChannelItem> & { secret?: string }>({ type: 'dingtalk', minSeverity: 'warning', enabled: true })

function fmtTime(s: string) {
  return s ? s.replace('T', ' ').slice(0, 19) : ''
}

async function load() {
  channels.value = await notifyApi.channels()
  if (tab.value === 'logs') {
    logsLoading.value = true
    try {
      logs.value = (await notifyApi.logs(1, 100)).items
    } finally {
      logsLoading.value = false
    }
  }
}

function openCreate() {
  Object.assign(form, { id: undefined, name: '', type: 'dingtalk', webhook: '', secret: '', minSeverity: 'warning', enabled: true })
  dlgVisible.value = true
}

function openEdit(row: NotifyChannelItem) {
  Object.assign(form, row, { secret: '' })
  dlgVisible.value = true
}

async function save() {
  saving.value = true
  try {
    await notifyApi.saveChannel(form)
    ElMessage.success('已保存')
    dlgVisible.value = false
    await load()
  } finally {
    saving.value = false
  }
}

async function testCh(row: NotifyChannelItem) {
  await notifyApi.testChannel(row.id)
  ElMessage.success('测试消息已发送，请到钉钉/接收端确认')
}

async function removeCh(row: NotifyChannelItem) {
  await confirmDelete(row.name, { title: '删除通知渠道' })
  await notifyApi.deleteChannel(row.id)
  await load()
}

onMounted(load)
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.msg-pre { margin: 0; padding: 8px 16px; font: 12px/1.6 ui-monospace, Consolas, monospace; white-space: pre-wrap; }
.msg-err { padding: 4px 16px; color: #f56c6c; font-size: 12px; }
</style>
