// 表单工具：路径取值/设值、对象清理、YAML 互转
import { load as yamlLoad, dump as yamlDump } from 'js-yaml'

/** 按点路径取值（"spec.ports"），支持 a.b[0].c 形式 */
export function getPath(obj: any, path: string): any {
  const parts = path.replace(/\[(\d+)\]/g, '.$1').split('.')
  let cur = obj
  for (const p of parts) {
    if (cur == null) return undefined
    cur = cur[p]
  }
  return cur
}

/** 按点路径设值 */
export function setPath(obj: any, path: string, value: any): any {
  const parts = path.replace(/\[(\d+)\]/g, '.$1').split('.')
  let cur = obj
  for (let i = 0; i < parts.length - 1; i++) {
    if (typeof cur[parts[i]] !== 'object' || cur[parts[i]] === null) {
      cur[parts[i]] = {}
    }
    cur = cur[parts[i]]
  }
  cur[parts[parts.length - 1]] = value
  return obj
}

/** 递归移除空值（undefined/null/空字符串/空对象/空数组），用于生成干净的 YAML */
export function cleanEmpty(obj: any): any {
  if (Array.isArray(obj)) {
    const arr = obj.map(cleanEmpty).filter((v) => v !== undefined && v !== null)
    return arr.length > 0 ? arr : undefined
  }
  if (obj && typeof obj === 'object') {
    const out: Record<string, any> = {}
    for (const [k, v] of Object.entries(obj)) {
      const c = cleanEmpty(v)
      if (c !== undefined && c !== null && !(typeof c === 'string' && c === '')) {
        out[k] = c
      }
    }
    return Object.keys(out).length > 0 ? out : undefined
  }
  return obj
}

/** YAML 字符串 -> 对象 */
export function parseYaml(text: string): Record<string, any> {
  try {
    return (yamlLoad(text) as Record<string, any>) || {}
  } catch (e) {
    throw new Error('YAML 解析失败: ' + (e as Error).message)
  }
}

/** 对象 -> YAML 字符串（清理空值） */
export function dumpYaml(obj: any): string {
  return yamlDump(cleanEmpty(obj), { lineWidth: 120, noRefs: true })
}

/** 对象 -> JSON 字符串 */
export function toJson(obj: any): string {
  return JSON.stringify(cleanEmpty(obj), null, 2)
}

/** 生成对象深层拷贝 */
export function clone<T>(obj: T): T {
  return JSON.parse(JSON.stringify(obj))
}

/** 简单字符串数组转行分隔文本 */
export function listToText(list: string[] | undefined): string {
  return (list || []).join('\n')
}

/** 行分隔文本转字符串数组 */
export function textToList(text: string): string[] {
  return text
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
}
