// RBAC 表单的 rules 序列化工具（逗号文本 <-> 数组）
const text = (arr: string[] | undefined) => (arr || []).join(', ')

/** 表单保存前：text 字段转数组 */
export function serializeRules(obj: any): any {
  for (const r of obj.rules || []) {
    if (r.apiGroupsText !== undefined) {
      r.apiGroups = r.apiGroupsText ? r.apiGroupsText.split(',').map((s: string) => s.trim()) : ['*']
      r.resources = r.resourcesText ? r.resourcesText.split(',').map((s: string) => s.trim()) : []
      r.verbs = r.verbsText ? r.verbsText.split(',').map((s: string) => s.trim()) : []
      if (r.resourceNamesText) r.resourceNames = r.resourceNamesText.split(',').map((s: string) => s.trim())
      delete r.apiGroupsText
      delete r.resourcesText
      delete r.verbsText
      delete r.resourceNamesText
    }
  }
  return obj
}

/** 表单加载前：数组转 text 字段 */
export function deserializeRules(obj: any): any {
  for (const r of obj.rules || []) {
    r.apiGroupsText = text(r.apiGroups)
    r.resourcesText = text(r.resources)
    r.verbsText = text(r.verbs)
    r.resourceNamesText = text(r.resourceNames)
  }
  return obj
}
