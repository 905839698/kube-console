<template>
  <div>
    <el-form label-width="110px" size="small">
      <el-form-item label="名称"><el-input v-model="o.metadata.name" /></el-form-item>
      <el-form-item label="命名空间"><el-input v-model="o.metadata.namespace" /></el-form-item>
      <el-form-item label="路由条目">
        <div class="form-tip">定义 path → 后端 Service 的转发规则（应用层路由，非 K8s 原生资源，保存为 annotations 便于网关/Ingress 参考）。</div>
        <div v-for="(r, i) in entries" :key="i" class="rule-card">
          <div class="rule-header">
            <span>路由 {{ i + 1 }}</span>
            <el-button size="small" type="danger" text @click="entries.splice(i, 1)"><el-icon><Delete /></el-icon></el-button>
          </div>
          <div class="kv-row">
            <el-input v-model="r.path" placeholder="路径，如 /api 或 /" size="small" style="width: 26%" />
            <span class="sep">→</span>
            <el-input v-model="r.service" placeholder="目标 Service 名称" size="small" style="width: 24%" />
            <el-input-number v-model="r.port" :min="1" :max="65535" size="small" style="width: 18%" placeholder="端口" />
          </div>
          <div class="kv-row">
            <el-select v-model="r.protocol" size="small" style="width: 20%">
              <el-option v-for="t in ['HTTP', 'HTTPS', 'TCP', 'UDP']" :key="t" :label="t" :value="t" />
            </el-select>
            <el-input v-model="r.host" placeholder="主机名（可选，如 app.example.com）" size="small" style="width: 42%" />
          </div>
        </div>
        <el-button size="small" type="primary" plain @click="addEntry"><el-icon><Plus /></el-icon>添加路由</el-button>
      </el-form-item>
    </el-form>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import { Delete, Plus } from '@element-plus/icons-vue'

const props = defineProps<{ modelValue: any }>()
// computed 而非 const 快照：父级（ObjectEditor）替换 modelValue 后，
// 表单必须跟着指向新对象，否则继续写一个没人引用的旧对象（编辑静默丢失）
const o = computed(() => props.modelValue)

interface Entry { path: string; service: string; port: number; protocol: string; host: string }

// 路由条目存于 annotations（应用层路由抽象，避免伪造不存在 spec 的原生资源）
const ANN_KEY = 'console.kube.io/routes'
const entries = reactive<Entry[]>([])

// 从 annotations 解析条目
function initEntries() {
  entries.length = 0
  try {
    const raw = o.value?.metadata?.annotations?.[ANN_KEY]
    if (raw) entries.push(...(JSON.parse(raw) || []))
  } catch {
    entries.length = 0
  }
  if (entries.length === 0) addEntry()
}
initEntries()

// modelValue 被整体替换（如 YAML tab 重新同步）→ 按新对象重新解析
watch(() => props.modelValue, (nv, ov) => {
  if (nv && nv !== ov) initEntries()
})

// 条目变更 → 写回 annotations（防抖由 ObjectEditor 的 deep watch 接管，这里同步即可）
watch(entries, () => {
  if (!o.value) return
  o.value.metadata = o.value.metadata || {}
  o.value.metadata.annotations = o.value.metadata.annotations || {}
  if (entries.length) {
    o.value.metadata.annotations[ANN_KEY] = JSON.stringify(entries)
  } else {
    delete o.value.metadata.annotations[ANN_KEY]
  }
}, { deep: true })

function addEntry() {
  entries.push({ path: '/', service: '', port: 80, protocol: 'HTTP', host: '' })
}
</script>

<style scoped>
.rule-card { border: 1px solid #e4e7ed; border-radius: 6px; padding: 12px; width: 100%; margin-bottom: 8px; }
.rule-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; font-weight: 600; }
.kv-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; }
.sep { color: #909399; }
.form-tip { font-size: 12px; color: #909399; margin-bottom: 8px; line-height: 1.6; }
</style>
