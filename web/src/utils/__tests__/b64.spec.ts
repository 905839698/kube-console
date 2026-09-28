// Secret base64 ↔ 文本编解码：两方向必须互逆（历史实现解码按 latin1，非 ASCII 值显示乱码且保存即损坏）
import { describe, expect, it } from 'vitest'
import { b64ToText, textToB64 } from '../b64'

describe('b64 编解码', () => {
  it('UTF-8 文本往返一致（中文与制表符）', () => {
    for (const s of ['hello', '中文密码🔐', '────────', 'a\tb\nc']) {
      expect(b64ToText(textToB64(s))).toBe(s)
    }
  })

  it('解码与旧编码方式对 UTF-8 内容结果一致', () => {
    // 旧版 encode：btoa(unescape(encodeURIComponent(v))) —— 与 textToB64 等价
    const legacy = (v: string) => btoa(unescape(encodeURIComponent(v)))
    expect(b64ToText(legacy('────────'))).toBe('────────')
  })

  it('非法 base64 与非 UTF-8 二进制返回 null', () => {
    expect(b64ToText('!!!not-base64!!!')).toBeNull()
    expect(b64ToText('/w==')).toBeNull() // 0xFF 不是合法 UTF-8 起始序列
  })

  it('空串按空文本处理', () => {
    expect(b64ToText('')).toBe('')
    expect(textToB64('')).toBe('')
  })
})
