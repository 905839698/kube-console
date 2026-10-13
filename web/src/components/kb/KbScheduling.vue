<template>
  <div class="sched">
    <div class="sec">同组实例打散</div>
    <el-radio-group v-model="state.spread" size="small">
      <el-radio-button value="">不打散</el-radio-button>
      <el-radio-button value="preferred">尽量分到不同节点</el-radio-button>
      <el-radio-button value="required">必须分到不同节点</el-radio-button>
    </el-radio-group>
    <div class="sub-tip">
      按主机名生成实例间反亲和（选择器是 <span class="mono">app.kubernetes.io/instance=&lt;集群名&gt;</span>）。
      副本数超过能落的节点数时，「必须」会让 Pod 一直 Pending —— 不知道节点分布就先选「尽量」。
    </div>

    <div class="sec">节点标签选择（nodeSelector，必须匹配）</div>
    <KvEditor v-model="state.nodeSelector" key-placeholder="标签键，如 node-role.kubernetes.io/db" value-placeholder="标签值，如 true" />

    <div class="sec">容忍污点（tolerations）</div>
    <div v-for="(t, i) in state.tolerations" :key="i" class="tol-row">
      <el-input v-model="t.key" size="small" placeholder="污点键，如 dedicated" style="width: 32%" />
      <el-select v-model="t.operator" size="small" style="width: 100px">
        <el-option v-for="o in KB_TOLERATION_OPS" :key="o" :value="o" :label="o" />
      </el-select>
      <el-input v-if="t.operator === 'Equal'" v-model="t.value" size="small" placeholder="值，如 db" style="width: 22%" />
      <el-select v-model="t.effect" size="small" style="width: 150px">
        <el-option v-for="e in KB_TAINT_EFFECTS" :key="e" :value="e" :label="e || '（不限效果）'" />
      </el-select>
      <el-button size="small" text type="danger" @click="state.tolerations.splice(i, 1)">移除</el-button>
    </div>
    <el-button size="small" @click="state.tolerations.push(newToleration())">+ 添加容忍</el-button>
    <div class="sub-tip">
      键留空 + Exists = 容忍所有污点（确实要"落到任何节点"再用）。现有污点清单可在「集群 → 节点」详情里看。
    </div>

    <div class="sec">节点亲和 · 必须满足（requiredDuringScheduling）</div>
    <MatchRows v-model="state.nodeRequired" empty-text="不设置：不额外要求节点" />
    <div class="sec">节点亲和 · 尽量满足（preferredDuringScheduling，权重 100）</div>
    <MatchRows v-model="state.nodePreferred" empty-text="不设置" />

    <div class="summary">本次调度设置：{{ summary }}</div>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import KvEditor from '../forms/KvEditor.vue'
import MatchRows from './KbSchedulingMatch.vue'
import {
  KB_TAINT_EFFECTS, KB_TOLERATION_OPS, cloneScheduling, emptyScheduling, kbSchedulingSummary,
  newToleration, type KBSchedulingState,
} from '../../utils/kb-scheduling'

const props = defineProps<{ modelValue?: KBSchedulingState | null }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: KBSchedulingState): void }>()

// 单向数据流：挂载时从 props 播种一次，之后只 emit 不回灌。
// 绝不能再 watch props.modelValue 往 state 里写——emit 出去的对象经父组件
// v-model 回流，一旦回灌就会形成「子改 → emit → 父写 → props 变 → 子再改」
// 的递归更新级联；dev 会被 Vue 的递归保护拦下，生产构建保护被编译掉，
// 无界循环直接卡死页面（必须分到不同节点一点就死的根因）。
// 弹窗配了 destroy-on-close，每次打开都是新实例重新播种，父组件的重置能到达。
const state = reactive<KBSchedulingState>(props.modelValue ? cloneScheduling(props.modelValue) : emptyScheduling())
watch(state, () => {
  emit('update:modelValue', cloneScheduling(state))
}, { deep: true })

const summary = computed(() => kbSchedulingSummary(state))
</script>

<style scoped>
.sched { width: 100%; }
.sec { font-weight: 600; margin: 12px 0 6px; }
.sub-tip { font-size: 13px; color: #909399; line-height: 1.6; margin-top: 4px; }
.summary { margin-top: 10px; font-size: 13px; color: #606266; background: var(--el-fill-color-light); padding: 6px 8px; border-radius: 4px; }
.mono { font-family: var(--el-font-family-mono, monospace); }
.tol-row { display: flex; gap: 8px; margin-bottom: 8px; align-items: center; }
</style>
