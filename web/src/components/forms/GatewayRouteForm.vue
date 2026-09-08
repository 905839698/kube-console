<template>
  <div>
    <!-- Gateway：监听器 -->
    <template v-if="kind === 'gateways'">
      <el-form label-width="110px" size="small">
        <el-form-item label="名称"><el-input v-model="o.metadata.name" /></el-form-item>
        <el-form-item label="命名空间"><el-input v-model="o.metadata.namespace" /></el-form-item>
        <el-form-item label="GatewayClass">
          <el-input v-model="o.spec.gatewayClassName" placeholder="如 higress / istio" />
        </el-form-item>
        <el-form-item label="监听器">
          <div v-for="(l, i) in o.spec.listeners || []" :key="i" class="rule-card">
            <div class="rule-header">
              <span>监听器 {{ i + 1 }}</span>
              <el-button size="small" type="danger" text @click="o.spec.listeners.splice(i, 1)"><el-icon><Delete /></el-icon></el-button>
            </div>
            <div class="kv-row">
              <el-input v-model="l.name" placeholder="名称（如 http）" size="small" style="width: 22%" />
              <el-select v-model="l.protocol" size="small" style="width: 20%">
                <el-option v-for="t in ['HTTP', 'HTTPS', 'TCP', 'UDP', 'TLS']" :key="t" :label="t" :value="t" />
              </el-select>
              <el-input-number v-model="l.port" :min="1" :max="65535" size="small" style="width: 18%" placeholder="端口" />
              <el-input v-model="l.hostname" placeholder="主机名（如 *.example.com）" size="small" style="width: 30%" />
            </div>
            <!-- HTTPS / TLS 监听器需要引用证书 Secret -->
            <div v-if="l.protocol === 'HTTPS' || l.protocol === 'TLS'" class="kv-row" style="margin-top: 6px">
              <span class="listener-label">证书 Secret</span>
              <el-input
                :model-value="l.tls?.certificateRefs?.[0]?.name || ''"
                placeholder="TLS 证书 Secret 名称（必填）"
                size="small"
                style="width: 40%"
                @update:model-value="(v) => setListenerCert(l, v)"
              />
              <span class="listener-label">允许路由来源</span>
              <el-select
                :model-value="l.allowedRoutes?.namespaces?.from || 'Same'"
                size="small"
                style="width: 22%"
                @update:model-value="(v) => setAllowedRoutes(l, v)"
              >
                <el-option label="同命名空间 (Same)" value="Same" />
                <el-option label="全部命名空间 (All)" value="All" />
              </el-select>
            </div>
          </div>
          <el-button size="small" type="primary" plain @click="addListener"><el-icon><Plus /></el-icon>添加监听器</el-button>
        </el-form-item>
      </el-form>
    </template>

    <!-- HTTPRoute / GRPCRoute / TLSRoute / TCPRoutes / UDPRoutes -->
    <template v-else>
      <el-form label-width="110px" size="small">
        <el-form-item label="名称"><el-input v-model="o.metadata.name" /></el-form-item>
        <el-form-item label="命名空间"><el-input v-model="o.metadata.namespace" /></el-form-item>
        <!-- TCPRoutes / UDPRoutes 无 hostnames，仅 backendRefs -->
        <template v-if="kind === 'tcproutes' || kind === 'udproutes'">
          <el-form-item label="协议">
            <el-select v-model="protocolText">
              <el-option v-for="p in (kind === 'tcproutes' ? ['TCP'] : ['UDP'])" :key="p" :label="p" :value="p" />
            </el-select>
            <span class="form-hint">{{ kind === 'tcproutes' ? 'TCPRoute（v1alpha2）' : 'UDPRoute（v1alpha2）' }}</span>
          </el-form-item>
        </template>
        <el-form-item v-else label="主机名（逗号分隔）">
          <el-input v-model="hostnamesText" placeholder="如 app.example.com" />
        </el-form-item>
        <el-form-item label="路由规则">
          <div v-for="(r, i) in o.spec.rules || []" :key="i" class="rule-card">
            <div class="rule-header">
              <span>规则 {{ i + 1 }}</span>
              <el-button size="small" type="danger" text @click="o.spec.rules.splice(i, 1)"><el-icon><Delete /></el-icon></el-button>
            </div>
            <!-- TCPRoutes / UDPRoutes：仅后端 -->
            <template v-if="kind === 'tcproutes' || kind === 'udproutes'">
              <div class="rule-label">后端服务</div>
              <div v-for="(b, bi) in r.backendRefs || []" :key="bi" class="kv-row">
                <el-input v-model="b.name" placeholder="Service 名称" size="small" style="width: 24%" />
                <el-select v-model="b.namespace" size="small" style="width: 20%">
                  <el-option v-for="n in namespaces" :key="n" :label="n" :value="n" />
                </el-select>
                <el-input-number v-model="b.port" :min="1" :max="65535" size="small" style="width: 16%" placeholder="端口" />
                <el-input-number v-model="b.weight" :min="0" :max="1000" size="small" style="width: 14%" placeholder="权重" />
                <el-button size="small" type="danger" text @click="r.backendRefs.splice(bi, 1)"><el-icon><Delete /></el-icon></el-button>
              </div>
              <el-button size="small" type="primary" plain @click="addBackend(r)"><el-icon><Plus /></el-icon>添加后端</el-button>
            </template>
            <!-- HTTPRoute / GRPCRoute / TLSRoute 匹配条件（TCPRoutes/UDPRoutes 无匹配，仅后端） -->
            <template v-if="kind === 'httproutes'">
              <div class="rule-label">匹配条件（path）</div>
              <div v-for="(m, mi) in r.matches || []" :key="mi" class="kv-row">
                <el-input v-model="m.path.value" placeholder="如 /api" size="small" style="width: 28%" />
                <el-select v-model="m.path.type" size="small" style="width: 20%">
                  <el-option v-for="t in ['PathPrefix', 'Exact', 'RegularExpression']" :key="t" :label="t" :value="t" />
                </el-select>
                <el-button size="small" type="danger" text @click="r.matches.splice(mi, 1)"><el-icon><Delete /></el-icon></el-button>
              </div>
              <el-button size="small" type="primary" plain @click="addMatch(r)"><el-icon><Plus /></el-icon>添加匹配</el-button>
            </template>
            <!-- GRPCRoute 匹配 -->
            <template v-else-if="kind === 'grpcroutes'">
              <div class="rule-label">匹配条件（method）</div>
              <div v-for="(m, mi) in r.matches || []" :key="mi" class="kv-row">
                <el-input v-model="m.method.service" placeholder="service（如 helloworld.Greeter）" size="small" style="width: 40%" />
                <el-input v-model="m.method.method" placeholder="method（如 SayHello）" size="small" style="width: 30%" />
                <el-button size="small" type="danger" text @click="r.matches.splice(mi, 1)"><el-icon><Delete /></el-icon></el-button>
              </div>
              <el-button size="small" type="primary" plain @click="addGrpcMatch(r)"><el-icon><Plus /></el-icon>添加匹配</el-button>
            </template>
            <!-- TLSRoute 匹配 -->
            <template v-else-if="kind === 'tlsroutes'">
              <div class="rule-label">SNI（逗号分隔）</div>
              <el-input v-model="r.snisText" placeholder="如 api.example.com" size="small" />
            </template>
            <!-- 后端（HTTP/GRPC/TLS 路由；TCP/UDP 后端已在上方单独渲染） -->
            <template v-if="kind === 'httproutes' || kind === 'grpcroutes' || kind === 'tlsroutes'">
              <div class="rule-label" style="margin-top: 8px">后端服务</div>
              <div v-for="(b, bi) in r.backendRefs || []" :key="bi" class="kv-row">
                <el-input v-model="b.name" placeholder="Service 名称" size="small" style="width: 24%" />
                <el-select v-model="b.namespace" size="small" style="width: 20%">
                  <el-option v-for="n in namespaces" :key="n" :label="n" :value="n" />
                </el-select>
                <el-input-number v-model="b.port" :min="1" :max="65535" size="small" style="width: 16%" placeholder="端口" />
                <el-input-number v-model="b.weight" :min="0" :max="1000" size="small" style="width: 14%" placeholder="权重" />
                <el-button size="small" type="danger" text @click="r.backendRefs.splice(bi, 1)"><el-icon><Delete /></el-icon></el-button>
              </div>
              <el-button size="small" type="primary" plain @click="addBackend(r)"><el-icon><Plus /></el-icon>添加后端</el-button>
            </template>
          </div>
          <el-button size="small" type="primary" plain @click="addRule"><el-icon><Plus /></el-icon>添加规则</el-button>
        </el-form-item>
      </el-form>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Delete, Plus } from '@element-plus/icons-vue'
import { k8sApi } from '../../api'

const props = defineProps<{ modelValue: any; kind: string }>()
const emit = defineEmits(['update:modelValue'])
const o = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})

const namespaces = ref<string[]>([])
const hostnamesText = computed<string>({
  get: () => (o.value.spec?.hostnames || []).join(', '),
  set: (v) => {
    o.value.spec = o.value.spec || {}
    o.value.spec.hostnames = v ? v.split(',').map((s) => s.trim()).filter(Boolean) : undefined
  },
})

// TCPRoutes / UDPRoutes 协议（写回所有规则；TCP/UDP 路由协议单一）
const protocolText = computed<string>({
  get: () => (o.value.spec?.rules?.[0]?.protocol as string) || (props.kind === 'tcproutes' ? 'TCP' : 'UDP'),
  set: (v) => {
    o.value.spec = o.value.spec || {}
    o.value.spec.rules = o.value.spec.rules || []
    for (const r of o.value.spec.rules) r.protocol = v
  },
})

// HTTPS/TLS 监听器：证书 Secret 引用与 allowedRoutes
function setListenerCert(l: any, name: string) {
  if (!name) {
    delete l.tls
    return
  }
  l.tls = l.tls || { certificateRefs: [] }
  l.tls.certificateRefs = l.tls.certificateRefs?.length ? l.tls.certificateRefs : [{}]
  l.tls.certificateRefs[0] = { ...l.tls.certificateRefs[0], kind: 'Secret', name }
}
function setAllowedRoutes(l: any, from: string) {
  l.allowedRoutes = l.allowedRoutes || { namespaces: {} }
  l.allowedRoutes.namespaces = { from }
}

onMounted(async () => {
  try {
    namespaces.value = (await k8sApi.namespaces()).map((n) => n.name)
  } catch {
    namespaces.value = ['default']
  }
})

function addListener() {
  o.value.spec = o.value.spec || {}
  o.value.spec.listeners = o.value.spec.listeners || []
  o.value.spec.listeners.push({ name: 'http', protocol: 'HTTP', port: 80, hostname: '' })
}

function addRule() {
  o.value.spec = o.value.spec || {}
  o.value.spec.rules = o.value.spec.rules || []
  const rule: any = { backendRefs: [] }
  if (props.kind === 'httproutes') rule.matches = [{ path: { value: '/', type: 'PathPrefix' } }]
  if (props.kind === 'grpcroutes') rule.matches = [{ method: { service: '', method: '' } }]
  if (props.kind === 'tlsroutes') rule.snisText = ''
  if (props.kind === 'tcproutes') rule.protocol = 'TCP'
  if (props.kind === 'udproutes') rule.protocol = 'UDP'
  o.value.spec.rules.push(rule)
}

function addMatch(rule: any) {
  rule.matches = rule.matches || []
  rule.matches.push({ path: { value: '/', type: 'PathPrefix' } })
}

function addGrpcMatch(rule: any) {
  rule.matches = rule.matches || []
  rule.matches.push({ method: { service: '', method: '' } })
}

function addBackend(rule: any) {
  rule.backendRefs = rule.backendRefs || []
  rule.backendRefs.push({ name: '', namespace: 'default', port: 80, weight: 1 })
}
</script>

<style scoped>
.listener-label { font-size: 12px; color: #909399; }
.rule-card { border: 1px solid #e4e7ed; border-radius: 6px; padding: 12px; width: 100%; margin-bottom: 8px; }
.rule-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; font-weight: 600; }
.rule-label { font-size: 12px; color: #909399; margin-bottom: 6px; }
.kv-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; }
.form-hint { margin-left: 10px; font-size: 12px; color: #c0c4cc; }
</style>
