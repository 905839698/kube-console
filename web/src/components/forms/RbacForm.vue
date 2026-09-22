<template>
  <div>
    <el-form label-width="110px" size="small">
      <el-form-item label="名称"><el-input v-model="o.metadata.name" /></el-form-item>
      <el-form-item label="命名空间" v-if="namespaced"><el-input v-model="o.metadata.namespace" /></el-form-item>

      <!-- Role / ClusterRole：rules 编辑器 -->
      <template v-if="kind === 'roles' || kind === 'clusterroles'">
        <el-form-item label="规则">
          <div v-for="(r, i) in o.rules || []" :key="i" class="rule-card">
            <div class="rule-header">
              <span>规则 {{ i + 1 }}</span>
              <el-button size="small" type="danger" text @click="o.rules.splice(i, 1)"><el-icon><Delete /></el-icon></el-button>
            </div>
            <div class="kv-row">
              <span class="rule-label">API 组（逗号分隔）</span>
              <el-input v-model="r.apiGroupsText" placeholder="如 apps, batch（* 表示全部）" size="small" />
            </div>
            <div class="kv-row">
              <span class="rule-label">资源（逗号分隔）</span>
              <el-input v-model="r.resourcesText" placeholder="如 deployments, pods" size="small" />
            </div>
            <div class="kv-row">
              <span class="rule-label">动作（逗号分隔）</span>
              <el-input v-model="r.verbsText" placeholder="如 get, list, watch, create, update, patch, delete" size="small" />
            </div>
            <div class="kv-row">
              <span class="rule-label">资源名称（可选）</span>
              <el-input v-model="r.resourceNamesText" placeholder="逗号分隔，留空表示全部" size="small" />
            </div>
          </div>
          <el-button size="small" type="primary" plain @click="addRule"><el-icon><Plus /></el-icon>添加规则</el-button>
        </el-form-item>
      </template>

      <!-- RoleBinding / ClusterRoleBinding -->
      <template v-else>
        <el-form-item label="引用角色类型">
          <el-select v-model="o.roleRef.kind" style="width: 180px">
            <el-option v-for="t in ['Role', 'ClusterRole']" :key="t" :label="t" :value="t" />
          </el-select>
        </el-form-item>
        <el-form-item label="角色名称"><el-input v-model="o.roleRef.name" /></el-form-item>
        <el-form-item label="主体 (subjects)">
          <div v-for="(s, i) in o.subjects || []" :key="i" class="kv-row">
            <el-select v-model="s.kind" size="small" style="width: 22%" @change="onSubjectKind(s)">
              <el-option v-for="t in ['User', 'Group', 'ServiceAccount']" :key="t" :label="t" :value="t" />
            </el-select>
            <el-input v-model="s.name" placeholder="名称" size="small" style="width: 30%" />
            <el-input v-if="s.kind === 'ServiceAccount'" v-model="s.namespace" placeholder="命名空间" size="small" style="width: 22%" />
            <el-button size="small" type="danger" text @click="o.subjects.splice(i, 1)"><el-icon><Delete /></el-icon></el-button>
          </div>
          <el-button size="small" type="primary" plain @click="addSubject"><el-icon><Plus /></el-icon>添加主体</el-button>
        </el-form-item>
      </template>
    </el-form>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Delete, Plus } from '@element-plus/icons-vue'

const props = defineProps<{ modelValue: any; kind: string; namespaced?: boolean }>()
const emit = defineEmits(['update:modelValue'])
const o = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
})

function addRule() {
  o.value.rules = o.value.rules || []
  o.value.rules.push({ apiGroupsText: '', resourcesText: '', verbsText: '', resourceNamesText: '' })
}

function addSubject() {
  o.value.subjects = o.value.subjects || []
  o.value.subjects.push({ kind: 'ServiceAccount', name: '', namespace: '' })
}

// namespace 只对 ServiceAccount 合法：切到 User/Group 时残留的 namespace 会让 API 拒绝整个绑定
function onSubjectKind(s: any) {
  if (s.kind !== 'ServiceAccount') delete s.namespace
  else if (s.namespace == null) s.namespace = ''
}
</script>
