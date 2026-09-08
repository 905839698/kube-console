<template>
  <div ref="editorEl" class="yaml-editor" />
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { EditorView, basicSetup } from 'codemirror'
import { EditorState } from '@codemirror/state'
import { yaml } from '@codemirror/lang-yaml'
import { oneDark } from '@codemirror/theme-one-dark'

const props = defineProps<{ modelValue?: string; readonly?: boolean; dark?: boolean }>()
const emit = defineEmits(['update:modelValue'])

const editorEl = ref<HTMLElement>()
let view: EditorView | null = null

const theme = [
  EditorView.theme({
    '&': { height: '100%', fontSize: '13px' },
    '.cm-scroller': { fontFamily: 'Consolas, Menlo, monospace', lineHeight: '1.6' },
  }),
]

function build() {
  if (!editorEl.value) return
  const dark = props.dark !== false
  view = new EditorView({
    parent: editorEl.value,
    state: EditorState.create({
      doc: props.modelValue || '',
      extensions: [
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
.yaml-editor { height: 100%; min-height: 300px; border: 1px solid #dcdfe6; border-radius: 4px; overflow: hidden; }
.yaml-editor :deep(.cm-editor) { height: 100%; }
</style>
