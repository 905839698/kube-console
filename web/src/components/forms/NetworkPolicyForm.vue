<template>
  <div>
    <el-form label-width="110px" size="small">
      <el-form-item label="名称"><el-input v-model="o.metadata.name" /></el-form-item>
      <el-form-item label="命名空间"><el-input v-model="o.metadata.namespace" /></el-form-item>
      <el-form-item label="Pod 选择器">
        <KvEditor v-model="o.spec.podSelector.matchLabels" key-placeholder="key" value-placeholder="value" />
      </el-form-item>
      <el-form-item label="策略类型">
        <el-checkbox-group v-model="policyTypes">
          <el-checkbox value="Ingress">入站</el-checkbox>
          <el-checkbox value="Egress">出站</el-checkbox>
        </el-checkbox-group>
      </el-form-item>

      <!-- 入站规则 -->
      <el-form-item label="入站规则">
        <div v-for="(rule, i) in o.spec.ingress || []" :key="'in' + i" class="rule-card">
          <div class="rule-header">
            <span>入站规则 {{ i + 1 }}</span>
            <el-button size="small" type="danger" text @click="o.spec.ingress.splice(i, 1)"><el-icon><Delete /></el-icon></el-button>
          </div>
          <div class="rule-body">
            <div class="rule-section">
              <div class="rule-label">允许来源 (from)</div>
              <div v-for="(f, fi) in rule.from || []" :key="fi" class="kv-row">
                <el-select v-model="f.kind" size="small" style="width: 30%">
                  <el-option v-for="t in ['PodSelector', 'NamespaceSelector', 'IPBlock']" :key="t" :label="t" :value="t" />
                </el-select>
                <el-input v-if="f.kind === 'IPBlock'" v-model="f.ipBlock.cidr" placeholder="如 10.0.0.0/8" size="small" style="width: 35%" />
                <KvEditor v-else-if="f.kind" :model-value="f[f.kind.toLowerCase()]?.matchLabels || {}" @update:model-value="(v) => (f[f.kind.toLowerCase()] = { matchLabels: v })" />
                <el-button size="small" type="danger" text @click="rule.from.splice(fi, 1)"><el-icon><Delete /></el-icon></el-button>
              </div>
              <el-button size="small" type="primary" plain @click="addFrom(rule)"><el-icon><Plus /></el-icon>添加来源</el-button>
            </div>
            <div class="rule-section">
              <div class="rule-label">端口</div>
              <div v-for="(p, pi) in rule.ports || []" :key="pi" class="kv-row">
                <el-input-number v-model="p.port" :min="1" :max="65535" size="small" style="width: 30%" placeholder="端口" />
                <el-select v-model="p.protocol" size="small" style="width: 25%">
                  <el-option v-for="t in ['TCP', 'UDP', 'SCTP']" :key="t" :label="t" :value="t" />
                </el-select>
                <el-button size="small" type="danger" text @click="rule.ports.splice(pi, 1)"><el-icon><Delete /></el-icon></el-button>
              </div>
              <el-button size="small" type="primary" plain @click="addPort(rule)"><el-icon><Plus /></el-icon>添加端口</el-button>
            </div>
          </div>
        </div>
        <el-button size="small" type="primary" plain @click="addRule('ingress')"><el-icon><Plus /></el-icon>添加入站规则</el-button>
      </el-form-item>

      <!-- 出站规则 -->
      <el-form-item label="出站规则">
        <div v-for="(rule, i) in o.spec.egress || []" :key="'eg' + i" class="rule-card">
          <div class="rule-header">
            <span>出站规则 {{ i + 1 }}</span>
            <el-button size="small" type="danger" text @click="o.spec.egress.splice(i, 1)"><el-icon><Delete /></el-icon></el-button>
          </div>
          <div class="rule-body">
            <div class="rule-section">
              <div class="rule-label">目标 (to)</div>
              <div v-for="(f, fi) in rule.to || []" :key="fi" class="kv-row">
                <el-select v-model="f.kind" size="small" style="width: 30%">
                  <el-option v-for="t in ['PodSelector', 'NamespaceSelector', 'IPBlock']" :key="t" :label="t" :value="t" />
                </el-select>
                <el-input v-if="f.kind === 'IPBlock'" v-model="f.ipBlock.cidr" placeholder="如 0.0.0.0/0" size="small" style="width: 35%" />
                <KvEditor v-else-if="f.kind" :model-value="f[f.kind.toLowerCase()]?.matchLabels || {}" @update:model-value="(v) => (f[f.kind.toLowerCase()] = { matchLabels: v })" />
                <el-button size="small" type="danger" text @click="rule.to.splice(fi, 1)"><el-icon><Delete /></el-icon></el-button>
              </div>
              <el-button size="small" type="primary" plain @click="addTo(rule)"><el-icon><Plus /></el-icon>添加目标</el-button>
            </div>
          </div>
        </div>
        <el-button size="small" type="primary" plain @click="addRule('egress')"><el-icon><Plus /></el-icon>添加出站规则</el-button>
      </el-form-item>
    </el-form>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Delete, Plus } from '@element-plus/icons-vue'
import KvEditor from './KvEditor.vue'

const props = defineProps<{ modelValue: any }>()
const emit = defineEmits(['update:modelValue'])
const o = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})

const policyTypes = computed<string[]>({
  get: () => (o.value.spec?.policyTypes || []).slice(),
  set: (v) => {
    o.value.spec = o.value.spec || {}
    o.value.spec.policyTypes = v
  },
})

function addRule(dir: 'ingress' | 'egress') {
  o.value.spec = o.value.spec || {}
  o.value.spec[dir] = o.value.spec[dir] || []
  o.value.spec[dir].push({ from: dir === 'ingress' ? [] : undefined, to: dir === 'egress' ? [] : undefined, ports: [] })
  if (!o.value.spec.policyTypes) o.value.spec.policyTypes = [dir === 'ingress' ? 'Ingress' : 'Egress']
}

function addFrom(rule: any) {
  rule.from = rule.from || []
  rule.from.push({ kind: 'PodSelector', podSelector: { matchLabels: {} } })
}

function addTo(rule: any) {
  rule.to = rule.to || []
  rule.to.push({ kind: 'PodSelector', podSelector: { matchLabels: {} } })
}

function addPort(rule: any) {
  rule.ports = rule.ports || []
  rule.ports.push({ port: 80, protocol: 'TCP' })
}
</script>

<style scoped>
.rule-card { border: 1px solid #e4e7ed; border-radius: 6px; padding: 12px; width: 100%; margin-bottom: 8px; }
.rule-header { display: flex; justify-content: space-between; margin-bottom: 8px; font-weight: 600; }
.rule-body { display: flex; gap: 16px; }
.rule-section { flex: 1; }
.rule-label { font-size: 12px; color: #909399; margin-bottom: 6px; }
.kv-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; }
</style>
