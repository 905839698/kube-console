// 列表行点击跳转/选中详情前，先判断用户是否正在选中文本（拖选复制、双击选中单词），
// 避免"想复制却被带进详情页"
export function textSelected(): boolean {
  const sel = window.getSelection()
  return !!sel && !sel.isCollapsed && String(sel).trim() !== ''
}
