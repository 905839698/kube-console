// 列表排序工具：数值列与运行时长的正确排序

/** 解析中文运行时长（"10个月"、"45分钟"、"3天"）为秒数，无法解析返回 0 */
export function parseDuration(s: string | undefined | null): number {
  if (!s) return 0
  const m = s.match(/(\d+)\s*(秒|分钟|小时|天|个月|年)/)
  if (!m) return 0
  const n = parseInt(m[1], 10)
  switch (m[2]) {
    case '秒':
      return n
    case '分钟':
      return n * 60
    case '小时':
      return n * 3600
    case '天':
      return n * 86400
    case '个月':
      return n * 86400 * 30
    case '年':
      return n * 86400 * 365
    default:
      return 0
  }
}

/** 运行时长列排序方法：按解析后的秒数比较 */
export function sortByDuration(a: string, b: string): number {
  return parseDuration(a) - parseDuration(b)
}

/** 数值列排序方法（字符串数字按数值比较） */
export function sortByNumber(a: number | string | undefined, b: number | string | undefined): number {
  return (Number(a) || 0) - (Number(b) || 0)
}
