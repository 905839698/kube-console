// 删除操作二次确认：必须完整输入资源名称（或数量/ID）后才能点「删除」，防误删
import { ElMessageBox } from 'element-plus'

/**
 * 删除二次确认。
 * @param expected 需要用户完整输入的确认串（资源名称 / 数量 / ID）
 * @param opts.title  框标题，默认「删除确认」
 * @param opts.warning 附加的风险说明（保留原有各处提示文案）
 */
export async function confirmDelete(
  expected: string,
  opts?: { title?: string; warning?: string },
): Promise<void> {
  const message = opts?.warning
    ? `${opts.warning}（输入 ${expected} 以确认删除）`
    : `将删除「${expected}」，此操作不可恢复。输入 ${expected} 以确认删除：`
  await ElMessageBox.prompt(message, opts?.title || '删除确认', {
    type: 'warning',
    confirmButtonText: '删除',
    cancelButtonText: '取消',
    inputPlaceholder: expected,
    inputValidator: (v: string) => (v === expected ? true : `输入不匹配，请完整输入 ${expected}`),
  })
}

/** 批量删除确认：输入数量 */
export async function confirmDeleteCount(count: number, opts?: { title?: string; warning?: string }): Promise<void> {
  await confirmDelete(String(count), opts)
}
