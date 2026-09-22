// RBAC 表单的 rules 序列化工具（逗号文本 <-> 数组）
const text = (arr: string[] | undefined) => (arr || []).join(', ')

/** 表单保存前：text 字段转数组 */
export function serializeRules(obj: any): any {
  for (const r of obj.rules || []) {
    if (r.apiGroupsText !== undefined) {
      const list = (t: string) => t.split(',').map((x: string) => x.trim()).filter(Boolean)
      r.apiGroups = r.apiGroupsText ? list(r.apiGroupsText) : ['*']
      r.resources = r.resourcesText ? list(r.resourcesText) : []
      r.verbs = r.verbsText ? list(r.verbsText) : []
      // 显式清空：留空 = 不限制资源名，必须移除原对象里残留的 resourceNames（不能静默保留）
      if (r.resourceNamesText) r.resourceNames = list(r.resourceNamesText)
      else delete r.resourceNames
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
