// 表单系统类型定义
import type { Component } from 'vue'

/** 表单字段定义（SchemaForm 渲染器使用） */
export interface FieldDef {
  /** 对象路径，如 "spec.ports" */
  key: string
  label: string
  type: 'text' | 'number' | 'select' | 'bool' | 'kv' | 'list' | 'multi-select' | 'strings'
  options?: string[]
  placeholder?: string
  required?: boolean
  /** list 类型时子元素字段 */
  items?: FieldDef[]
  /** kv 类型的值占位 */
  kvValuePlaceholder?: string
  /** kv 类型的值用多行 textarea（长文本/多行配置自适应展开） */
  multiline?: boolean
  help?: string
}

/** 表单模块：kind -> 可视化表单（parse/build 负责与对象互转） */
export interface FormModule {
  title: string
  /** 表单组件内已自带标签/注解编辑（如 WorkloadForm），ObjectEditor 不再重复挂 MetaEditor */
  metaInForm?: boolean
  /** 注解承载业务数据（如路由表单），MetaEditor 仅显示标签 */
  noAnnotations?: boolean
  /** 表单渲染组件（v-model 绑定 formData） */
  component: Component
  /** 对象 -> 表单数据 */
  parse(obj: Record<string, any>): any
  /** 表单数据 -> 对象（基于原始对象合并，保留表单未覆盖的字段） */
  build(base: Record<string, any>, formData: any): Record<string, any>
}

/** 资源类型 -> 表单模块注册表 */
export type FormRegistry = Record<string, FormModule>
