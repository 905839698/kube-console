<template>
  <div ref="editorEl" class="yaml-editor" />
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { EditorView, basicSetup } from 'codemirror'
import { EditorState } from '@codemirror/state'
import { keymap } from '@codemirror/view'
import { yaml } from '@codemirror/lang-yaml'
import { oneDark } from '@codemirror/theme-one-dark'

const props = defineProps<{ modelValue?: string; readonly?: boolean; dark?: boolean }>()
const emit = defineEmits(['update:modelValue'])

const editorEl = ref<HTMLElement>()
let view: EditorView | null = null

const theme = [
  EditorView.theme({
    '&': { height: '100%', fontSize: '14px' },
    '.cm-scroller': { fontFamily: 'Consolas, Menlo, monospace', lineHeight: '1.6' },
  }),
]

// Ctrl/Cmd+A：CodeMirror 默认键位没有 selectAll，浏览器会把它抢去选整页 DOM，
// 导致只读 YAML（Helm manifest 等）无法全选复制。这里显式绑定为全选文档。
const selectAllKeymap = keymap.of([
  {
    key: 'Mod-a',
    run: (v) => {
      v.dispatch({ selection: { anchor: 0, head: v.state.doc.length } })
      v.focus()
      return true
    },
  },
])

function build() {
  if (!editorEl.value) return
  const dark = props.dark !== false
  view = new EditorView({
    parent: editorEl.value,
    state: EditorState.create({
      doc: props.modelValue || '',
      extensions: [
        selectAllKeymap,
        basicSetup,
        yaml(),
        ...(dark ? [oneDark] : []),
        ...theme,
        EditorView.editable.of(!props.readonly),
        EditorView.updateListener.of((update) => {
          if (update.docChanged) {
            emit('update:modelValue', update.state.doc.toString())
          }
        }),
      ],
    }),
  })
}

watch(
  () => props.modelValue,
  (v) => {
    if (view && v !== view.state.doc.toString()) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: v || '' } })
    }
  },
)

watch(
  () => props.readonly,
  () => {
    view?.destroy()
    view = null
    build()
  },
)

onMounted(build)
onBeforeUnmount(() => view?.destroy())
</script>

<style scoped>
.yaml-editor { height: 100%; min-height: 300px; border: 1px solid var(--el-border-color); border-radius: 4px; overflow: hidden; }
.yaml-editor :deep(.cm-editor) { height: 100%; }
/* 浅色主题下 CodeMirror 搜索面板（Ctrl+F）默认可见性差，补基础样式 */
.yaml-editor :deep(.cm-panels) { background: var(--el-fill-color-light); border-bottom: 1px solid var(--el-border-color); }
.yaml-editor :deep(.cm-panel.cm-search) { font-size: 13px; color: var(--el-text-color-primary); }
.yaml-editor :deep(.cm-textfield) { border: 1px solid var(--el-border-color); border-radius: 3px; background: #fff; }
.yaml-editor :deep(.cm-button) { border: 1px solid var(--el-border-color); border-radius: 3px; background: #fff; }
.yaml-editor :deep(.cm-panel.cm-search [name=close]) { color: var(--el-text-color-secondary); }
</style>
