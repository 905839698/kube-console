<template>
  <div class="kv-editor">
    <div v-for="(pair, idx) in pairs" :key="idx" class="kv-row">
      <el-input v-model="pair.k" :placeholder="keyPlaceholder" size="small" style="width: 30%" class="kv-key" @input="sync" />
      <el-input
        v-if="multiline"
        v-model="pair.v"
        :placeholder="valuePlaceholder || '值'"
        type="textarea"
        :autosize="{ minRows: 1, maxRows: 12 }"
        size="small"
        style="width: 60%"
        class="kv-value"
        @input="sync"
      />
      <el-input v-else v-model="pair.v" :placeholder="valuePlaceholder || '值'" size="small" style="width: 60%" class="kv-value" @input="sync" />
      <el-button size="small" type="danger" text @click="remove(idx)">
        <el-icon><Delete /></el-icon>
      </el-button>
    </div>
    <el-button size="small" type="primary" plain @click="add">
      <el-icon><Plus /></el-icon>&nbsp;添加
    </el-button>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { Delete, Plus } from '@element-plus/icons-vue'

const props = defineProps<{
  modelValue: Record<string, string>
  keyPlaceholder?: string
  valuePlaceholder?: string
  /** 多行值模式：值用 textarea 自动高度，长文本（多行配置）自适应展开 */
  multiline?: boolean
}>()
const emit = defineEmits(['update:modelValue'])

const pairs = ref<{ k: string; v: string }[]>([])

function syncFrom() {
  const next = Object.entries(props.modelValue || {}).map(([k, v]) => ({ k, v: String(v ?? '') }))
  // 「改标签名」时先全选删除 key：空 key 行不在父对象里（sync 只输出非空 key），
  // 直接重建会让正在编辑的行消失、value 静默丢失——保留有值的空 key 行
  for (const p of pairs.value) {
    if (!p.k.trim() && p.v && !next.some((n) => !n.k && n.v === p.v)) {
      next.push({ k: '', v: p.v })
    }
  }
  pairs.value = next.length ? next : [{ k: '', v: '' }]
}

watch(
  () => props.modelValue,
  () => syncFrom(),
  { deep: true },
)
syncFrom()

function sync() {
  const out: Record<string, string> = {}
  for (const p of pairs.value) {
    if (p.k.trim()) out[p.k.trim()] = p.v
  }
  emit('update:modelValue', out)
}

function add() {
  pairs.value.push({ k: '', v: '' })
}

function remove(idx: number) {
  pairs.value.splice(idx, 1)
  if (pairs.value.length === 0) pairs.value = [{ k: '', v: '' }]
  sync()
}
</script>

<style scoped>
.kv-editor { width: 100%; }
.kv-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: flex-start; }
.kv-key { flex-shrink: 0; }
.kv-value { flex: 1; }
</style>
