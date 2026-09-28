// 折线图统一色板：调用方没显式给 series.color 时按序号取用
export const CHART_PALETTE = [
  '#409eff', '#67c23a', '#e6a23c', '#f56c6c', '#9254de',
  '#00c1de', '#ff7f50', '#87ceeb', '#7c889a', '#d4b106',
]

// 序列取色：显式色优先，缺色按序号补（越界回绕）。
// 单独成函数是为了能被测到——之前缺色时传的是空数组，等于清空 ECharts 调色板，
// 曲线一条都不画，而坐标轴/图例/tooltip 全都正常，极难从现象反推。
export function seriesColors(series: { color?: string }[]): string[] {
  return series.map((s, i) => s.color || CHART_PALETTE[i % CHART_PALETTE.length])
}
