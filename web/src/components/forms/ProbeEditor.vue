<template>
  <div>
    <el-checkbox v-model="p.enabled">{{ label }}</el-checkbox>
    <template v-if="p.enabled">
      <div class="probe-row">
        <el-select v-model="p.type" size="small" style="width: 130px">
          <el-option v-for="o in ['httpGet', 'tcpSocket', 'exec']" :key="o" :label="o" :value="o" />
        </el-select>
        <el-input v-if="p.type === 'httpGet'" v-model="p.path" placeholder="/health" size="small" style="width: 130px" />
        <el-input-number v-if="p.type !== 'exec'" v-model="p.port" :min="1" :max="65535" size="small" style="width: 110px" />
        <el-input v-else v-model="p.command" placeholder="如 cat /tmp/healthy" size="small" style="width: 200px" />
        <template v-if="!titleOnly">
          <span class="probe-label">初始延迟</span>
          <el-input-number v-model="p.initialDelaySeconds" :min="0" :max="3600" size="small" style="width: 90px" />
          <span class="probe-label">间隔(s)</span>
          <el-input-number v-model="p.periodSeconds" :min="1" :max="3600" size="small" style="width: 90px" />
        </template>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ modelValue: any; label?: string; titleOnly?: boolean }>()
const emit = defineEmits(['update:modelValue'])

const label = computed(() => props.label || '启用探针')

const p = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})
</script>

<style scoped>
.probe-row { display: flex; gap: 8px; align-items: center; margin-top: 8px; flex-wrap: wrap; }
.probe-label { font-size: 12px; color: #909399; }
</style>
