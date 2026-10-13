<template>
  <div>
    <div v-for="(m, i) in rows" :key="i" class="match-row">
      <el-input v-model="m.key" size="small" placeholder="节点标签键，如 topology.kubernetes.io/zone" style="width: 40%" />
      <el-select v-model="m.operator" size="small" style="width: 130px">
        <el-option v-for="o in KB_MATCH_OPS" :key="o" :value="o" :label="o" />
      </el-select>
      <el-input v-model="m.values" size="small" style="width: 32%" :disabled="noValues(m.operator)"
        :placeholder="noValues(m.operator) ? '该 operator 不看值' : '如 cn-zone-a, cn-zone-b'" />
      <el-button size="small" text type="danger" @click="remove(i)">移除</el-button>
    </div>
    <div v-if="!rows.length" class="tip">{{ emptyText }}</div>
    <el-button size="small" @click="add">+ 添加一条</el-button>
  </div>
</template>

<script setup lang="ts">
// 节点亲和的匹配表达式行编辑器（必须满足 / 尽量满足共用一套控件）
import { computed } from 'vue'
import { KB_MATCH_OPS, newMatch, type KBNodeMatchRow } from '../../utils/kb-scheduling'

const props = defineProps<{ modelValue: KBNodeMatchRow[]; emptyText?: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: KBNodeMatchRow[]): void }>()

const rows = computed(() => props.modelValue)
// Exists / DoesNotExist 只看键，给了值服务端会拒，这里先把输入框按住
const noValues = (op: string) => op === 'Exists' || op === 'DoesNotExist'
const add = () => emit('update:modelValue', [...rows.value, newMatch()])
const remove = (i: number) => {
  const next = [...rows.value]
  next.splice(i, 1)
  emit('update:modelValue', next)
}
</script>

<style scoped>
.tip { font-size: 13px; color: #909399; margin-bottom: 6px; }
.match-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; }
</style>
