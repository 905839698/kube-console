<template>
  <el-radio-group :model-value="range" size="small" @change="onSelect">
    <el-radio-button v-for="r in ['1h', '6h', '24h']" :key="r" :value="r">{{ r }}</el-radio-button>
  </el-radio-group>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'

const props = defineProps<{ modelValue?: string }>()
const emit = defineEmits(['change', 'update:modelValue'])

const range = ref(props.modelValue || '6h')

watch(
  () => props.modelValue,
  (v) => {
    if (v) range.value = v
  },
)

// 必须 emit update:modelValue：调用方都是 v-model + @change="load"，
// 旧模板从不回写父组件，切 24h 后 UI 变了但 load 仍按父组件的旧 range 查询
function onSelect(v: string) {
  range.value = v
  emit('update:modelValue', v)
  emit('change', v)
}
</script>
