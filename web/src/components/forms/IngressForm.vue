<template>
  <div>
    <el-form label-width="110px" size="small">
      <el-form-item label="名称"><el-input v-model="o.metadata.name" /></el-form-item>
      <el-form-item label="命名空间"><el-input v-model="o.metadata.namespace" /></el-form-item>
      <el-form-item label="IngressClass">
        <el-input v-model="o.spec.ingressClassName" placeholder="如 nginx / higress" />
      </el-form-item>
      <el-form-item label="默认后端">
        <div class="kv-row">
          <el-input :model-value="dbService.name || ''" @input="(v) => setDbField('name', v)" placeholder="Service（可选）" size="small" style="width: 52%" />
          <el-input-number :model-value="dbService.port" @change="(v) => setDbField('port', v)" :min="1" :max="65535" size="small" style="width: 30%" placeholder="端口" />
          <el-button v-if="o.spec?.defaultBackend" size="small" type="danger" text @click="clearDb">清除</el-button>
        </div>
      </el-form-item>
      <el-form-item label="路由规则">
        <div v-for="(rule, i) in o.spec.rules || []" :key="i" class="rule-card">
          <div class="rule-header">
            <span>规则 {{ i + 1 }}</span>
            <el-button size="small" type="danger" text @click="o.spec.rules.splice(i, 1)"><el-icon><Delete /></el-icon>删除</el-button>
          </div>
          <el-form-item label="域名"><el-input v-model="rule.host" placeholder="如 app.example.com" /></el-form-item>
          <el-form-item label="路径">
            <div v-for="(path, pi) in rule.http?.paths || []" :key="pi" class="kv-row">
              <el-input v-model="path.path" placeholder="如 /api" size="small" style="width: 24%" />
              <el-select v-model="path.pathType" size="small" style="width: 17%">
                <el-option v-for="t in ['Prefix', 'Exact', 'ImplementationSpecific']" :key="t" :label="t" :value="t" />
              </el-select>
              <!-- backend 支持 service / resource 两种类型（如 higress McpBridge） -->
              <el-select :model-value="backendType(path)" size="small" style="width: 13%" @change="switchBackend(path, $event)">
                <el-option label="Service" value="service" />
                <el-option label="Resource" value="resource" />
              </el-select>
              <template v-if="backendType(path) === 'service'">
                <el-input v-model="path.backend.service.name" placeholder="Service" size="small" style="width: 20%" />
                <el-input-number v-model="path.backend.service.port.number" :min="1" :max="65535" size="small" style="width: 14%" placeholder="端口" />
              </template>
              <template v-else>
                <el-input v-model="path.backend.resource.kind" placeholder="Kind" size="small" style="width: 15%" />
                <el-input v-model="path.backend.resource.name" placeholder="名称" size="small" style="width: 19%" />
              </template>
              <el-button size="small" type="danger" text @click="rule.http.paths.splice(pi, 1)"><el-icon><Delete /></el-icon></el-button>
            </div>
            <el-button size="small" type="primary" plain @click="addPath(rule)"><el-icon><Plus /></el-icon>添加路径</el-button>
          </el-form-item>
        </div>
        <el-button size="small" type="primary" plain @click="addRule"><el-icon><Plus /></el-icon>添加规则</el-button>
      </el-form-item>
      <el-form-item label="TLS">
        <div v-for="(tls, ti) in o.spec.tls || []" :key="ti" class="kv-row">
          <el-input :model-value="hostsStr(tls)" placeholder="域名(逗号分隔)" size="small" style="width: 40%" @update:model-value="setHosts(tls, $event)" />
          <el-input v-model="tls.secretName" placeholder="Secret 名称" size="small" style="width: 30%" />
          <el-button size="small" type="danger" text @click="o.spec.tls.splice(ti, 1)"><el-icon><Delete /></el-icon></el-button>
        </div>
        <el-button size="small" type="primary" plain @click="addTls"><el-icon><Plus /></el-icon>添加 TLS</el-button>
      </el-form-item>
    </el-form>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { Delete, Plus } from '@element-plus/icons-vue'

const props = defineProps<{ modelValue: any }>()
const emit = defineEmits(['update:modelValue'])
const o = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})

// 默认后端（spec.defaultBackend.service）
const dbService = computed(() => o.value.spec?.defaultBackend?.service || {})
function setDbField(k: string, v: any) {
  o.value.spec = o.value.spec || {}
  o.value.spec.defaultBackend = o.value.spec.defaultBackend || {}
  o.value.spec.defaultBackend.service = o.value.spec.defaultBackend.service || {}
  o.value.spec.defaultBackend.service[k] = v
}
function clearDb() {
  if (o.value.spec) delete o.value.spec.defaultBackend
}

// backend 类型：resource 优先（如 higress McpBridge），否则 service
function backendType(path: any): 'service' | 'resource' {
  return path?.backend?.resource ? 'resource' : 'service'
}

// 切换 backend 类型时转换数据结构
function switchBackend(path: any, type: string) {
  if (!path.backend) path.backend = {}
  if (type === 'resource' && !path.backend.resource) {
    delete path.backend.service
    path.backend.resource = { apiGroup: 'networking.higress.io', kind: '', name: '' }
  }
  if (type === 'service' && !path.backend.service) {
    delete path.backend.resource
    path.backend.service = { name: '', port: { number: 80 } }
  }
}

// 挂载时归一化数据结构，避免渲染崩溃（backend 缺失等）
onMounted(() => {
  const obj = props.modelValue
  if (obj?.spec?.rules) {
    for (const rule of obj.spec.rules) {
      for (const path of rule.http?.paths || []) {
        if (!path.backend) path.backend = {}
        if (path.backend.resource) {
          path.backend.resource = { apiGroup: path.backend.resource.apiGroup || '', kind: path.backend.resource.kind || '', name: path.backend.resource.name || '' }
        } else {
          path.backend.service = path.backend.service || { name: '', port: { number: 80 } }
          if (!path.backend.service.port) path.backend.service.port = { number: 80 }
        }
      }
    }
  }
})

// TLS hosts 为数组，用逗号分隔字符串编辑
function hostsStr(tls: any): string {
  return Array.isArray(tls.hosts) ? tls.hosts.join(',') : (tls.hosts || '')
}

function setHosts(tls: any, v: string) {
  tls.hosts = v.split(',').map((s: string) => s.trim()).filter(Boolean)
}

function addRule() {
  o.value.spec = o.value.spec || {}
  o.value.spec.rules = o.value.spec.rules || []
  o.value.spec.rules.push({ host: '', http: { paths: [] } })
}

function addPath(rule: any) {
  rule.http = rule.http || { paths: [] }
  rule.http.paths.push({ path: '/', pathType: 'Prefix', backend: { service: { name: '', port: { number: 80 } } } })
}

function addTls() {
  o.value.spec = o.value.spec || {}
  o.value.spec.tls = o.value.spec.tls || []
  o.value.spec.tls.push({ hosts: [], secretName: '' })
}
</script>

<style scoped>
.rule-card { border: 1px solid #e4e7ed; border-radius: 6px; padding: 12px; width: 100%; margin-bottom: 8px; }
.rule-header { display: flex; justify-content: space-between; margin-bottom: 8px; font-weight: 600; }
.kv-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; }
</style>
