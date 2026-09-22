<template>
  <el-dialog
    :model-value="modelValue"
    title="容器内日志采集（Fluent Bit Sidecar）"
    width="640px"
    @update:model-value="(v: boolean) => emit('update:modelValue', v)"
  >
    <div v-loading="loading">
      <div class="status-row">
        当前状态：
        <el-tag v-if="enabled" type="success" size="small">采集中</el-tag>
        <el-tag v-else type="info" size="small">未开启</el-tag>
        <span v-if="enabled && form.logPath" class="status-path">{{ form.logPath }}</span>
      </div>

      <el-alert type="info" :closable="false" class="tips">
        <div class="tip-line">注入 fluent-bit Sidecar 与共享 emptyDir 卷，tail 指定日志文件写入集群 Elasticsearch（沿用「平台管理-日志源配置」）。</div>
        <div class="tip-line">保存将修改 Pod 模板并触发滚动更新；日志目录会被 emptyDir 挂载覆盖，目录内镜像自带文件将不可见。</div>
        <div class="tip-line">容器需把日志文件写到该目录下（新写入的行才会被采集）。</div>
      </el-alert>

      <el-form label-width="110px" size="default">
        <el-form-item label="目标容器" required>
          <el-select v-model="form.container" style="width: 100%">
            <el-option v-for="n in appContainers" :key="n" :label="n" :value="n" />
          </el-select>
        </el-form-item>
        <el-form-item label="日志文件路径" required>
          <el-input v-model="form.logPath" placeholder="容器内绝对路径，支持通配：/var/log/app/*.log" />
        </el-form-item>
        <el-form-item label="日志格式">
          <el-radio-group v-model="form.format">
            <el-radio value="text">单行文本</el-radio>
            <el-radio value="json">JSON（字段展开）</el-radio>
          </el-radio-group>
          <div class="tip-line">单行文本写入行日志索引（与标准输出同库）；JSON 写入独立的 JSON 索引（默认 logstash-&lt;命名空间&gt;）。前缀在「平台管理-日志源配置」调整。</div>
        </el-form-item>
        <el-form-item label="Sidecar 镜像">
          <el-input v-model="form.image" placeholder="fluent/fluent-bit:3.2（留空用默认）" />
        </el-form-item>
      </el-form>
    </div>

    <template #footer>
      <el-button v-if="enabled" type="danger" plain :disabled="noWrite" @click="doDisable">关闭采集</el-button>
      <el-button v-if="enabled" @click="goSearch">查看已采集日志</el-button>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :loading="saving" :disabled="noWrite || !valid" @click="doSave">
        {{ enabled ? '更新采集配置' : '开启采集' }}
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { k8sApi } from '../api'

const props = defineProps<{
  modelValue: boolean
  kind: string
  namespace: string
  name: string
  containers: string[]
  noWrite: boolean
}>()
const emit = defineEmits(['update:modelValue', 'saved'])

const router = useRouter()
const loading = ref(false)
const saving = ref(false)
const enabled = ref(false)
const form = reactive({ container: '', logPath: '', format: 'text', image: '' })

// fluent-bit 是注入的采集容器，不作为采集目标
const appContainers = computed(() => props.containers.filter((n) => n !== 'fluent-bit'))
const valid = computed(() => !!form.container && form.logPath.startsWith('/'))

async function load() {
  loading.value = true
  try {
    const st = await k8sApi.logCollection(props.kind, props.namespace, props.name)
    enabled.value = st.enabled
    if (st.config) Object.assign(form, st.config)
  } catch {
    enabled.value = false
  } finally {
    loading.value = false
  }
  if (!form.container) form.container = appContainers.value[0] || ''
}

async function doSave() {
  saving.value = true
  try {
    await k8sApi.saveLogCollection(props.kind, props.namespace, props.name, { ...form })
    ElMessage.success('采集配置已保存，Pod 将滚动更新')
    emit('update:modelValue', false)
    emit('saved')
  } catch {
    /* 拦截器已提示 */
  } finally {
    saving.value = false
  }
}

async function doDisable() {
  try {
    await ElMessageBox.confirm(`确定关闭 ${props.name} 的日志采集？将移除 fluent-bit Sidecar 并触发滚动更新。`, '关闭采集', { type: 'warning' })
  } catch {
    return
  }
  await k8sApi.removeLogCollection(props.kind, props.namespace, props.name)
  ElMessage.success('已关闭采集，Pod 将滚动更新')
  emit('update:modelValue', false)
  emit('saved')
}

// 跳转日志检索并预填过滤条件（采集文档的 container 字段 = 目标容器名）
// 单行文本与行日志同索引（来源=文本文件），JSON 走独立索引（来源=JSON 采集）
function goSearch() {
  emit('update:modelValue', false)
  router.push({
    path: '/logsearch',
    query: { namespace: props.namespace, container: form.container, source: form.format === 'json' ? 'json' : 'file' },
  })
}

watch(() => props.modelValue, (v) => {
  if (v) load()
})
</script>

<style scoped>
.status-row { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; font-size: 13px; }
.status-path { font-family: ui-monospace, Consolas, monospace; color: #606266; font-size: 12px; word-break: break-all; }
.tips { margin-bottom: 14px; }
.tip-line { font-size: 12px; line-height: 1.7; }
</style>
