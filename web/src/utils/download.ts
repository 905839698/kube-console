// 前端文本文件下载（Blob + a[download]）
export function downloadText(text: string, filename: string, mime = 'text/plain;charset=utf-8') {
  const blob = new Blob([text], { type: mime })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = filename
  a.click()
  // 延迟释放：浏览器对 blob 的读取是异步的，同步 revoke 在部分浏览器
  // （Safari/Firefox 历史版本）会导致下载被取消/空文件
  setTimeout(() => URL.revokeObjectURL(a.href), 1000)
}
