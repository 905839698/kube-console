<template>
  <el-radio-group v-model="range" size="small" @change="emit('change', range)">
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

function onSelect(v: string) {
  range.value = v
  emit('update:modelValue', v)
  emit('change', v)
}
</script>
