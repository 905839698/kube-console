<template>
  <div>
    <el-form label-width="130px" size="small">
      <el-form-item label="名称"><el-input v-model="o.metadata.name" /></el-form-item>
      <el-form-item label="命名空间"><el-input v-model="o.metadata.namespace" /></el-form-item>
      <el-form-item label="目标类型">
        <el-select v-model="o.spec.scaleTargetRef.kind" style="width: 180px">
          <el-option v-for="t in ['Deployment', 'StatefulSet', 'ReplicaSet']" :key="t" :label="t" :value="t" />
        </el-select>
      </el-form-item>
      <el-form-item label="目标名称"><el-input v-model="o.spec.scaleTargetRef.name" /></el-form-item>
      <el-form-item label="最小副本">
        <el-input-number v-model="o.spec.minReplicas" :min="0" :max="500" style="width: 150px" />
      </el-form-item>
      <el-form-item label="最大副本">
        <el-input-number v-model="o.spec.maxReplicas" :min="1" :max="500" style="width: 150px" />
      </el-form-item>
      <el-form-item label="扩容指标">
        <div v-for="(m, i) in metrics" :key="i" class="kv-row">
          <el-select v-model="m.resource.name" size="small" style="width: 160px">
            <el-option v-for="t in ['cpu', 'memory']" :key="t" :label="t" :value="t" />
          </el-select>
          <el-select v-model="m.resource.target.type" size="small" style="width: 140px">
            <el-option v-for="t in ['Utilization', 'AverageValue']" :key="t" :label="t" :value="t" />
          </el-select>
          <el-input-number
            v-if="m.resource.target.type === 'Utilization'"
            v-model="m.resource.target.averageUtilization"
            :min="1"
            :max="100"
            size="small"
            style="width: 130px"
          />
          <el-input v-else v-model="m.resource.target.averageValue" placeholder="如 100m" size="small" style="width: 130px" />
          <span class="unit">%</span>
          <el-button size="small" type="danger" text @click="removeMetric(i)"><el-icon><Delete /></el-icon></el-button>
        </div>
        <el-button size="small" type="primary" plain @click="addMetric"><el-icon><Plus /></el-icon>添加指标</el-button>
      </el-form-item>
    </el-form>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Delete, Plus } from '@element-plus/icons-vue'

const props = defineProps<{ modelValue: any }>()
const emit = defineEmits(['update:modelValue'])
const o = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})

const metrics = computed<any[]>({
  get: () => (o.value.spec?.metrics || []).filter((m: any) => m.type === 'Resource'),
  set: () => {},
})

function addMetric() {
  o.value.spec = o.value.spec || {}
  o.value.spec.metrics = o.value.spec.metrics || []
  o.value.spec.metrics.push({
    type: 'Resource',
    resource: { name: 'cpu', target: { type: 'Utilization', averageUtilization: 80 } },
  })
}

function removeMetric(idx: number) {
  const list = o.value.spec?.metrics || []
  // 按类型过滤后定位原数组下标
  let count = 0
  for (let i = 0; i < list.length; i++) {
    if (list[i].type === 'Resource') {
      if (count === idx) {
        list.splice(i, 1)
        break
      }
      count++
    }
  }
}
</script>

<style scoped>
.kv-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; }
.unit { color: #909399; font-size: 12px; }
</style>
