<template>
  <div>
    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <div class="header-left">
            <span>Nacos 微服务管理</span>
            <el-tag v-if="clusterCfg" size="small" :type="clusterCfg.enabled ? 'success' : 'info'">
              {{ clusterCfg.enabled ? '同步已启用' : '同步未启用' }}
            </el-tag>
            <el-tag v-if="clusterCfg?.injectEnabled" size="small" type="success" effect="plain">Pod 注入已开启</el-tag>
            <el-tooltip v-if="syncErr" :content="syncErr"><el-tag size="small" type="danger">同步异常</el-tag></el-tooltip>
          </div>
          <div class="header-right">
            <el-select v-model="cluster" style="width: 200px" @change="load">
              <el-option v-for="cl in clusterStore.clusters" :key="cl.name" :label="cl.name" :value="cl.name" />
            </el-select>
            <el-button :icon="Refresh" circle @click="load" style="margin-left: 12px" />
            <el-button :disabled="!clusterCfg?.enabled" :loading="syncing" @click="syncNow">立即同步</el-button>
            <el-button type="primary" @click="openCfg">连接设置</el-button>
          </div>
        </div>
      </template>

      <template v-if="clusterCfg">
        <el-tabs v-model="tab">
          <!-- 命名空间同步 -->
          <el-tab-pane :label="`命名空间同步 (${mappings.length})`" name="sync">
            <el-alert type="info" :closable="false" style="margin-bottom: 10px"
              title="每个 K8s 命名空间自动创建同名 Nacos 命名空间与用户（角色 ROLE_<ns>，rw 权限），并在命名空间内维护 nacos-config ConfigMap 与 nacos-credentials Secret 供 Pod 注入。默认 5 分钟自动同步一次。" />
            <el-table :data="mappings" size="default" stripe>
              <el-table-column prop="k8sNamespace" label="K8s 命名空间" min-width="150" />
              <el-table-column prop="nacosNamespaceId" label="Nacos 命名空间" min-width="150" />
              <el-table-column prop="username" label="Nacos 用户" min-width="130" />
              <el-table-column label="密码（注入凭据）" min-width="200">
                <template #default="{ row }">
                  <span class="pwd">{{ row._show ? row.password : '••••••••••••' }}</span>
                  <el-button link type="primary" size="small" @click="row._show = !row._show">{{ row._show ? '隐藏' : '显示' }}</el-button>
                  <el-button link size="small" @click="copyPwd(row)">复制</el-button>
                </template>
              </el-table-column>
              <el-table-column label="状态" width="130">
                <template #default="{ row }">
                  <el-tooltip v-if="row.status === 'error' && row.error" :content="row.error">
                    <el-tag size="small" type="danger">error</el-tag>
                  </el-tooltip>
                  <el-tag v-else size="small" :type="row.status === 'synced' ? 'success' : 'warning'">
                    {{ row.status === 'synced' ? '已同步' : row.status === 'deleted-in-k8s' ? 'K8s 已删除' : row.status }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column label="最后同步" width="170">
                <template #default="{ row }">{{ fmtTime(row.syncedAt) }}</template>
              </el-table-column>
              <el-table-column label="操作" width="110" fixed="right" align="center">
                <template #default="{ row }">
                  <el-button link type="warning" size="small" :disabled="row.status === 'deleted-in-k8s'" @click="resetPwd(row)">重置密码</el-button>
                </template>
              </el-table-column>
            </el-table>
          </el-tab-pane>

          <!-- Nacos 资源（服务发现与配置管理已迁至「微服务」菜单） -->
          <el-tab-pane label="Nacos 资源" name="resources" lazy>
            <el-row :gutter="16">
              <el-col :span="12">
                <el-card shadow="never">
                  <template #header>命名空间</template>
                  <el-table :data="nsList" size="small" v-loading="resLoading">
                    <el-table-column label="名称" min-width="120">
                      <template #default="{ row }">{{ row.customNamespaceId || row.namespaceId || 'public' }}</template>
                    </el-table-column>
                    <el-table-column prop="configCount" label="配置数" width="70" align="center" />
                  </el-table>
                </el-card>
              </el-col>
              <el-col :span="12">
                <el-card shadow="never">
                  <template #header>用户</template>
                  <div class="tag-list" v-loading="resLoading">
                    <el-tag v-for="u in userList" :key="u" size="small" class="user-tag">{{ u }}</el-tag>
                    <span v-if="!userList.length" class="muted">无</span>
                  </div>
                </el-card>
              </el-col>
            </el-row>
            <el-alert type="info" :closable="false" style="margin-top: 12px"
              title="服务发现与配置管理已迁移到左侧「微服务」菜单，并与顶栏命名空间选择联动。" />
          </el-tab-pane>
        </el-tabs>
      </template>

      <el-empty v-else description="当前集群尚未接入 Nacos">
        <el-button type="primary" @click="openCfg">接入配置</el-button>
      </el-empty>
    </el-card>

    <!-- 连接配置对话框 -->
    <el-dialog v-model="cfgDlg" title="Nacos 连接配置" width="640px" :close-on-click-modal="false">
      <el-form label-width="130px">
        <el-form-item label="集群">
          <el-input :model-value="cluster" disabled />
        </el-form-item>
        <el-form-item label="Nacos 地址" required>
          <el-input v-model="form.addr" placeholder="http://nacos.mid-platform:8848" />
        </el-form-item>
        <el-form-item label="管理用户名">
          <el-input v-model="form.adminUsername" placeholder="nacos（未开启鉴权可留空）" />
        </el-form-item>
        <el-form-item label="管理密码">
          <el-input v-model="form.adminPassword" type="password" show-password placeholder="留空保持不变" />
        </el-form-item>
        <el-form-item label="启用 ns 同步">
          <el-switch v-model="form.enabled" />
          <span class="hint">按 K8s 命名空间自动创建 Nacos 命名空间/用户并维护注入凭据</span>
        </el-form-item>
        <el-divider content-position="left">Pod 自动注入（Admission Webhook）</el-divider>
        <el-form-item label="启用注入">
          <el-switch v-model="form.injectEnabled" />
          <span class="hint">维护 MutatingWebhookConfiguration，注入的 ns 创建的 Pod 自动追加 envFrom</span>
        </el-form-item>
        <el-form-item label="自动打标签">
          <el-switch v-model="form.autoLabel" />
          <span class="hint">同步时给命名空间打 nacos-injection=enabled（webhook 按该标签选择命名空间）</span>
        </el-form-item>
        <template v-if="form.injectEnabled">
          <el-form-item label="回调模式">
            <el-radio-group v-model="form.webhookMode">
              <el-radio-button value="url">URL（控制台在集群外）</el-radio-button>
              <el-radio-button value="service">Service（集群内部署）</el-radio-button>
            </el-radio-group>
          </el-form-item>
          <el-form-item v-if="form.webhookMode === 'url'" label="Webhook URL">
            <el-input v-model="form.webhookURL" placeholder="https://kube-console.example.com:9443/inject（kube-apiserver 必须可达）" />
          </el-form-item>
          <template v-else>
            <el-form-item label="Service 命名空间">
              <el-input v-model="form.webhookServiceNS" placeholder="kube-console" />
            </el-form-item>
            <el-form-item label="Service 名称">
              <el-input v-model="form.webhookServiceName" placeholder="kube-console-server" />
            </el-form-item>
            <el-form-item label="Service 端口">
              <el-input-number v-model="form.webhookServicePort" :min="1" :max="65535" />
            </el-form-item>
          </template>
        </template>
        <el-form-item>
          <el-button :loading="testing" @click="test">测试连接</el-button>
          <el-button type="primary" :loading="saving" @click="save">保存</el-button>
          <el-button v-if="clusterCfg" type="danger" plain @click="removeCfg">删除本集群配置</el-button>
        </el-form-item>
      </el-form>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, watch } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  nacosApi,
  type NacosConfigItem,
  type NacosMappingItem,
  type NacosNsInfo,
} from '../api'
import { useClusterStore } from '../store/cluster'

const clusterStore = useClusterStore()
const cluster = ref(clusterStore.current || '')
const clusterCfg = ref<NacosConfigItem>()
const mappings = ref<NacosMappingItem[]>([])
const syncErr = ref('')
const tab = ref('sync')

const cfgDlg = ref(false)
const saving = ref(false)
const testing = ref(false)
const syncing = ref(false)
const form = reactive<Partial<NacosConfigItem> & { adminPassword?: string }>({
  addr: '', adminUsername: '', adminPassword: '', enabled: true,
  injectEnabled: false, autoLabel: true, webhookMode: 'url', webhookURL: '',
  webhookServiceNS: 'kube-console', webhookServiceName: 'kube-console-server', webhookServicePort: 9443,
})

const resLoading = ref(false)
const nsList = ref<NacosNsInfo[]>([])
const userList = ref<string[]>([])

async function load() {
  if (!cluster.value) return
  try {
    const [cfgs, st] = await Promise.all([nacosApi.configs(), nacosApi.status()])
    clusterCfg.value = (cfgs || []).find((c) => c.clusterName === cluster.value)
    mappings.value = (st.mappings || [])
      .filter((m) => m.clusterName === cluster.value)
      .map((m) => ({ ...m, _show: false })) as NacosMappingItem[]
    syncErr.value = st.sync?.[cluster.value] || ''
  } catch {
    /* 拦截器已提示 */
  }
}

function openCfg() {
  Object.assign(form, {
    addr: clusterCfg.value?.addr || '', adminUsername: clusterCfg.value?.adminUsername || '', adminPassword: '',
    enabled: clusterCfg.value?.enabled ?? true,
    injectEnabled: clusterCfg.value?.injectEnabled ?? false,
    autoLabel: clusterCfg.value?.autoLabel ?? true,
    webhookMode: clusterCfg.value?.webhookMode || 'url',
    webhookURL: clusterCfg.value?.webhookURL || '',
    webhookServiceNS: clusterCfg.value?.webhookServiceNS || 'kube-console',
    webhookServiceName: clusterCfg.value?.webhookServiceName || 'kube-console-server',
    webhookServicePort: clusterCfg.value?.webhookServicePort || 9443,
  })
  cfgDlg.value = true
}

async function save() {
  if (!form.addr) {
    ElMessage.warning('Nacos 地址必填')
    return
  }
  saving.value = true
  try {
    await nacosApi.saveConfig({ ...form, clusterName: cluster.value, addr: form.addr })
    ElMessage.success('已保存')
    cfgDlg.value = false
    await load()
  } catch {
    /* 拦截器已提示 */
  } finally {
    saving.value = false
  }
}

async function test() {
  testing.value = true
  try {
    await nacosApi.saveConfig({ ...form, clusterName: cluster.value, addr: form.addr })
    await nacosApi.testConfig(cluster.value)
    ElMessage.success('Nacos 连通正常')
  } catch {
    /* 拦截器已提示 */
  } finally {
    testing.value = false
  }
}

async function removeCfg() {
  try {
    await ElMessageBox.confirm(`删除集群 ${cluster.value} 的 Nacos 配置？将同时移除注入 Webhook 与同步映射（Nacos 侧数据保留）。`, '删除配置', { type: 'warning' })
  } catch {
    return
  }
  await nacosApi.deleteConfig(cluster.value)
  ElMessage.success('已删除')
  clusterCfg.value = undefined
  load()
}

async function syncNow() {
  syncing.value = true
  try {
    await nacosApi.syncNow(cluster.value)
    ElMessage.success('同步完成')
    await load()
  } catch {
    /* 拦截器已提示 */
  } finally {
    syncing.value = false
  }
}

async function resetPwd(row: NacosMappingItem) {
  try {
    await ElMessageBox.confirm(
      `重置命名空间 ${row.k8sNamespace} 的 Nacos 用户密码？注入 Secret 会同步更新，已登录的应用需使用新密码重新连接。`,
      '重置密码', { type: 'warning' },
    )
  } catch {
    return
  }
  try {
    await nacosApi.resetPassword(cluster.value, row.k8sNamespace)
    ElMessage.success('密码已重置并同步到注入 Secret')
    await load()
  } catch {
    /* 拦截器已提示 */
  }
}

function copyPwd(row: NacosMappingItem) {
  navigator.clipboard.writeText(row.password)
  ElMessage.success('已复制')
}

// ---- 资源 ----
async function loadResources() {
  resLoading.value = true
  try {
    nsList.value = (await nacosApi.nacosNamespaces(cluster.value)) || []
    userList.value = (await nacosApi.users(cluster.value)) || []
  } catch {
    /* 拦截器已提示 */
  } finally {
    resLoading.value = false
  }
}

function fmtTime(ts?: string): string {
  if (!ts || ts.startsWith('0001-')) return '—'
  return ts.replace('T', ' ').slice(0, 19)
}

watch(tab, (v) => {
  if (v === 'resources') loadResources()
})
watch(cluster, load)

// 初始化：等集群列表就绪
if (!cluster.value && clusterStore.clusters.length) cluster.value = clusterStore.clusters[0].name
load()
clusterStore.clusters.length || clusterStore.load().then(() => {
  if (!cluster.value && clusterStore.clusters.length) cluster.value = clusterStore.clusters[0].name
  load()
})
</script>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 8px; }
.header-left { display: flex; align-items: center; gap: 10px; }
.header-right { display: flex; align-items: center; }
.toolbar { display: flex; gap: 10px; align-items: center; margin-bottom: 12px; }
.muted { color: #909399; font-size: 12px; }
.hint { color: #909399; font-size: 12px; margin-left: 12px; }
.pwd { font-family: Consolas, monospace; font-size: 12px; margin-right: 8px; }
.tag-list { display: flex; flex-wrap: wrap; gap: 6px; max-height: 260px; overflow-y: auto; }
.user-tag { font-family: Consolas, monospace; }
</style>
