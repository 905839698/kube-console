<template>
  <el-select
    v-model="model"
    :multiple="multiple"
    collapse-tags
    collapse-tags-tooltip
    clearable
    filterable
    :size="size"
    :placeholder="placeholder"
    :style="{ width: width || '200px' }"
    @change="onChange"
  >
    <el-option v-if="multiple" label="全部命名空间" value="__all__" />
    <el-option v-for="n in namespaces" :key="n" :label="n" :value="n" />
  </el-select>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { k8sApi } from '../api'

const props = defineProps<{
  modelValue?: string | string[]
  placeholder?: string
  multiple?: boolean
  size?: 'small' | 'default' | 'large'
  width?: string
}>()
const emit = defineEmits(['update:modelValue', 'change'])

const namespaces = ref<string[]>([])
// 多选模式下 '*' 归一化为 '__all__'（显示"全部命名空间"标签）
const model = ref<any>(
  props.multiple ? normalizeIn(props.modelValue) : props.modelValue || '',
)
// 上次外部值是否处于"全部"态：用于区分"全选态点击具体项"与"点击全部项"
let prevAll = props.multiple ? normalizeIn(props.modelValue).includes('__all__') : false

function normalizeIn(v: any): string[] {
  const raw = Array.isArray(v) ? v : []
  if (raw.length === 1 && raw[0] === '*') return ['__all__']
  return [...raw]
}

onMounted(async () => {
  try {
    const items = await k8sApi.namespaces()
    namespaces.value = items.map((i) => i.name)
    // 单选时默认选择 default（若尚未选择）
    if (!props.multiple && !model.value && namespaces.value.includes('default')) {
      model.value = 'default'
      emit('update:modelValue', 'default')
      emit('change', 'default')
    }
  } catch {
    /* 集群不可用时静默 */
  }
})

watch(
  () => props.modelValue,
  (v) => {
    if (props.multiple) {
      const norm = normalizeIn(v)
      prevAll = norm.includes('__all__')
      if (JSON.stringify(norm) !== JSON.stringify(model.value || [])) model.value = [...norm]
    } else if (v !== model.value) {
      model.value = v || ''
    }
  },
)

function onChange(v: string | string[]) {
  // 单选：原样透传；多选：'全部命名空间'为独占项
  if (props.multiple) {
    const arr = (v as string[]) || []
    if (arr.includes('__all__') && arr.length > 1 && prevAll) {
      // 全选态下点击具体命名空间 → 退出"全部"，仅保留具体选择
      model.value = arr.filter((x) => x !== '__all__')
    } else if (arr.includes('__all__')) {
      // 点击"全部命名空间"或已全选 → 独占全选
      model.value = ['__all__']
    } else {
      model.value = [...arr]
    }
    emit('update:modelValue', [...model.value])
    emit('change', [...model.value])
    return
  }
  emit('update:modelValue', v)
  emit('change', v)
}
</script>
