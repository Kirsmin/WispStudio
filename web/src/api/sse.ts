// 极简 SSE 解析器：只处理 id / event / data 三种字段。
// 服务端每帧格式固定为 id、event、data 三行加空行。

export interface SSEFrame {
  id: string
  event: string
  data: string
}

export class SSEParser {
  private buffer = ''

  // push 接收一段解码后的文本，返回其中已经完整的帧。
  push(chunk: string): SSEFrame[] {
    // 服务端使用 \n；这里兼容代理或中间层改写出的 \r\n。
    this.buffer = (this.buffer + chunk).replace(/\r\n/g, '\n')
    const frames: SSEFrame[] = []
    let index = this.buffer.indexOf('\n\n')
    while (index >= 0) {
      const block = this.buffer.slice(0, index)
      this.buffer = this.buffer.slice(index + 2)
      const frame = parseFrame(block)
      if (frame) frames.push(frame)
      index = this.buffer.indexOf('\n\n')
    }
    return frames
  }
}

function parseFrame(block: string): SSEFrame | null {
  if (!block.trim()) return null
  let event = 'message'
  let id = ''
  const data: string[] = []
  for (const line of block.split('\n')) {
    if (!line || line.startsWith(':')) continue
    const colon = line.indexOf(':')
    const field = colon === -1 ? line : line.slice(0, colon)
    let value = colon === -1 ? '' : line.slice(colon + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    if (field === 'event') event = value
    else if (field === 'id') id = value
    else if (field === 'data') data.push(value)
  }
  if (data.length === 0) return null
  return { id, event, data: data.join('\n') }
}
