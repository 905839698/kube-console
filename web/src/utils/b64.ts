// Secret data 的 base64 ↔ 文本转换：两个方向必须互为逆运算。
// 历史实现解码用 atob（按 latin1 逐字节成串）、编码按 UTF-8，导致含非 ASCII 的值
// 显示为乱码，且在表单里保存一次就把 Secret 内容改坏（字节数膨胀）。

// b64ToText 严格按 UTF-8 解码；非法 base64 或非 UTF-8（二进制）内容返回 null。
export function b64ToText(v: string): string | null {
  try {
    const bin = atob(v)
    const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0))
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes)
  } catch {
    return null
  }
}

// textToB64 把文本按 UTF-8 编码为 base64（b64ToText 的逆运算）。
export function textToB64(s: string): string {
  const bytes = new TextEncoder().encode(s)
  let bin = ''
  for (const b of bytes) bin += String.fromCharCode(b)
  return btoa(bin)
}
