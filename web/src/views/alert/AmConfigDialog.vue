<template>
  <el-dialog v-model="visible" title="Alertmanager 配置" width="860px" :close-on-click-modal="false" @open="onOpen">
    <el-tabs v-model="innerTab">
      <!-- 连接配置 -->
      <el-tab-pane label="连接配置" name="conn">
        <el-alert type="info" :closable="false" style="margin-bottom: 12px"
          title="按集群接入 Alertmanager。默认经 kube-apiserver service proxy 访问（无需对外暴露 AM）；代理不可达时可改填直连地址。" />
        <el-form label-width="130px">
          <el-form-item label="集群">
            <el-input :model-value="clusterStore.current || '（未选择集群）'" disabled style="width: 320px" />
          </el-form-item>
          <el-form-item label="AM 命名空间">
            <el-input v-model="form.namespace" placeholder="如 kuboard-system" style="width: 320px" />
          </el-form-item>
          <el-form-item label="AM 服务名">
            <el-input v-model="form.service" placeholder="如 alertmanager-kuboard-alertmanager" style="width: 320px" />
          </el-form-item>
          <el-form-item label="AM 端口">
            <el-input-number v-model="form.port" :min="1" :max="65535" style="width: 160px" />
          </el-form-item>
          <el-form-item label="直连地址">
            <el-input v-model="form.directURL" placeholder="http://节点IP:NodePort（可选，优先于代理）" style="width: 420px" />
          </el-form-item>
          <el-form-item label="跳过 TLS 校验">
            <el-switch v-model="form.insecure" />
            <span class="hint">仅直连自签证书时开启</span>
          </el-form-item>
          <el-form-item label="主配置 Secret">
            <el-input v-model="form.configSecret" placeholder="ns/name，如 monitoring/alertmanager-main-generated" style="width: 420px" />
            <div class="hint">填写后可在「配置 YAML」页在线编辑 alertmanager.yaml。operator 生成的 Secret（*-generated）会在 AlertmanagerConfig CR 变化时被覆盖。</div>
          </el-form-item>
          <el-form-item>
            <el-button type="primary" :loading="saving" @click="save">保存</el-button>
            <el-button :loading="testing" @click="test">测试连通</el-button>
            <el-button v-if="hasConfig" type="danger" plain :loading="removing" @click="remove">删除本集群配置</el-button>
          </el-form-item>
        </el-form>
      </el-tab-pane>

      <!-- 主配置 YAML -->
      <el-tab-pane label="配置 YAML" name="yaml">
        <el-alert type="warning" :closable="false" style="margin-bottom: 12px"
          title="直接编辑 Alertmanager 主配置（含通知接收器/路由）。保存后 AM 热加载；由 prometheus-operator 生成的 Secret（*-generated）在 AlertmanagerConfig CR 变化时会被覆盖。" />
        <div class="yaml-toolbar">
          <el-select v-model="snippet" placeholder="插入接收器模板（注释片段）" style="width: 280px" @change="insertSnippet">
            <el-option label="钉钉（webhook 适配器）" value="dingtalk" />
            <el-option label="邮件（SMTP）" value="email" />
            <el-option label="企业微信" value="wecom" />
            <el-option label="Slack" value="slack" />
            <el-option label="恢复通知开启示例" value="send_resolved" />
          </el-select>
          <el-input v-model="secretRef" placeholder="Secret（ns/name）" style="width: 340px; margin-left: 12px">
            <template #append><el-button :loading="loadingYaml" @click="loadYaml">加载</el-button></template>
          </el-input>
          <el-button type="primary" :loading="savingYaml" style="margin-left: 12px" @click="saveYaml">保存配置</el-button>
        </div>
        <div class="yaml-wrap">
          <YamlEditor v-model="yamlText" />
        </div>
      </el-tab-pane>
    </el-tabs>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { amApi } from '../../api'
import { useClusterStore } from '../../store/cluster'
import YamlEditor from '../../components/YamlEditor.vue'
import { confirmDelete } from '../../utils/confirm'

const props = defineProps<{ modelValue: boolean }>()
const emit = defineEmits(['update:modelValue', 'saved'])

const visible = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})

const clusterStore = useClusterStore()
const innerTab = ref<'conn' | 'yaml'>('conn')
const form = reactive({ namespace: '', service: '', port: 9093, directURL: '', insecure: false, configSecret: '' })
const hasConfig = ref(false)
const saving = ref(false)
const testing = ref(false)
const removing = ref(false)

const secretRef = ref('')
const yamlText = ref('')
const loadingYaml = ref(false)
const savingYaml = ref(false)
const snippet = ref('')

const SNIPPETS: Record<string, string> = {
  dingtalk: `# 钉钉：AM 原生不支持钉钉机器人，需先部署 webhook 适配器（如 timonwong/prometheus-webhook-dingtalk），
# 然后把下方片段并入 receivers:，并在 route 的 receiver: 中引用该名字（dingtalk-webhook）。
# - name: 'dingtalk-webhook'
#   webhook_configs:
#     - url: 'http://dingtalk-webhook.monitoring.svc:8060/dingtalk/<机器人名>/send'
#       send_resolved: true
`,
  email: `# 邮件接收器：把下方片段并入全局与 receivers:（并调整 SMTP 地址/账号）。
# global:
#   smtp_smarthost: 'smtp.example.com:465'
#   smtp_from: 'alert@example.com'
#   smtp_auth_username: 'alert@example.com'
#   smtp_auth_password: '******'
# receivers:
#   - name: 'email'
#     email_configs:
#       - to: 'ops@example.com'
#         send_resolved: true
`,
  wecom: `# 企业微信：把下方片段并入 receivers:（企业微信 API 已原生支持，调整 corp_id 与 to_party）。
# receivers:
#   - name: 'wecom'
#     wechat_configs:
#       - corp_id: 'your-corp-id'
#         agent_id: 'your-agent-id'
#         api_secret: '******'
#         to_party: '2'
#         send_resolved: true
`,
  slack: `# Slack：把下方片段并入 receivers:（调整 api_url 与 channel）。
# receivers:
#   - name: 'slack'
#     slack_configs:
#       - api_url: 'https://hooks.slack.com/services/XXX/YYY/ZZZ'
#         channel: '#alerts'
#         send_resolved: true
`,
  send_resolved: `# 恢复通知：在 receiver 的各 *_configs 中加 send_resolved: true（默认 false，即只发触发不发恢复）。
# 例：
# receivers:
#   - name: 'default'
#     webhook_configs:
#       - url: 'http://...'
#         send_resolved: true
`,
}

function insertSnippet(key: string) {
  const text = SNIPPETS[key]
  if (!text) return
  yamlText.value = (yamlText.value ? yamlText.value.replace(/\s*$/, '\n\n') : '') + text
  snippet.value = ''
}

async function onOpen() {
  innerTab.value = 'conn'
  await loadConfig()
}

async function loadConfig() {
  hasConfig.value = false
  form.namespace = ''
  form.service = ''
  form.port = 9093
  form.directURL = ''
  form.insecure = false
  form.configSecret = ''
  try {
    const list = await amApi.configs()
    const mine = (list || []).find((c) => c.clusterName === clusterStore.current)
    if (mine) {
      Object.assign(form, {
        namespace: mine.namespace, service: mine.service, port: mine.port || 9093,
        directURL: mine.directURL || '', insecure: !!mine.insecure, configSecret: mine.configSecret || '',
      })
      hasConfig.value = true
    }
  } catch {
    /* 拦截器已提示 */
  }
}

async function save() {
  saving.value = true
  try {
    await amApi.saveConfig({ ...form, clusterName: clusterStore.current || '' })
    ElMessage.success('已保存')
    hasConfig.value = true
    emit('saved')
  } catch {
    /* 拦截器已提示 */
  } finally {
    saving.value = false
  }
}

async function test() {
  testing.value = true
  try {
    await amApi.testConfig(clusterStore.current || '')
    ElMessage.success('Alertmanager 连通正常')
  } catch {
    /* 拦截器已提示 */
  } finally {
    testing.value = false
  }
}

async function remove() {
  try {
    await confirmDelete(clusterStore.current, { title: '删除 Alertmanager 配置' })
  } catch {
    return
  }
  removing.value = true
  try {
    await amApi.deleteConfig(clusterStore.current || '')
    ElMessage.success('已删除')
    hasConfig.value = false
    emit('saved')
  } catch {
    /* 拦截器已提示 */
  } finally {
    removing.value = false
  }
}

async function loadYaml() {
  if (!secretRef.value && !form.configSecret) {
    ElMessage.warning('请先填写主配置 Secret（ns/name）')
    return
  }
  loadingYaml.value = true
  try {
    const out = await amApi.configYaml(secretRef.value || form.configSecret)
    secretRef.value = out.secret
    yamlText.value = out.yaml
  } catch {
    /* 拦截器已提示 */
  } finally {
    loadingYaml.value = false
  }
}

async function saveYaml() {
  const ref = secretRef.value || form.configSecret
  if (!ref) {
    ElMessage.warning('请先填写主配置 Secret（ns/name）')
    return
  }
  savingYaml.value = true
  try {
    await amApi.saveConfigYaml(ref, yamlText.value)
    ElMessage.success('配置已写回 Secret，Alertmanager 将自动热加载')
  } catch {
    /* 拦截器已提示 */
  } finally {
    savingYaml.value = false
  }
}

watch(() => props.modelValue, (v) => {
  if (!v) {
    yamlText.value = ''
    snippet.value = ''
    innerTab.value = 'conn'
  }
})
</script>

<style scoped>
.hint { color: #909399; font-size: 12px; line-height: 1.5; }
.yaml-toolbar { display: flex; align-items: center; margin-bottom: 10px; flex-wrap: wrap; }
.yaml-wrap { height: 420px; border: 1px solid var(--kc-border, #dcdfe6); border-radius: 4px; overflow: hidden; }
</style>
