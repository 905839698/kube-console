// Gateway Route 表单的序列化工具（TLSRoute snis 文本 <-> 数组）
/** 表单保存前：snisText 转数组 */
export function serializeRoute(obj: any): any {
  for (const r of obj.spec?.rules || []) {
    if (r.snisText !== undefined) {
      r.snis = r.snisText ? r.snisText.split(',').map((s: string) => s.trim()).filter(Boolean) : undefined
      delete r.snisText
    }
  }
  return obj
}

/** 表单加载前：数组转文本 */
export function deserializeRoute(obj: any): any {
  for (const r of obj.spec?.rules || []) {
    r.snisText = (r.snis || []).join(', ')
  }
  return obj
}
