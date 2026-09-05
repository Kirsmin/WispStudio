export interface SSEMessage {
  event: string
  data: string
}

function parseBlock(block: string): SSEMessage | null {
  let event = 'message'
  const data: string[] = []

  for (const line of block.split(/\r?\n/)) {
    if (!line || line.startsWith(':')) continue
    if (line.startsWith('event:')) {
      event = line.slice(6).trim() || 'message'
      continue
    }
    if (line === 'data') {
      data.push('')
      continue
    }
    if (line.startsWith('data:')) {
      let value = line.slice(5)
      if (value.startsWith(' ')) value = value.slice(1)
      data.push(value)
    }
  }

  if (data.length === 0) return null
  return { event, data: data.join('\n') }
}

/** Incremental SSE decoder that handles CRLF and chunk boundaries. */
export class SSEDecoder {
  private buffer = ''

  feed(text: string): SSEMessage[] {
    this.buffer += text
    const result: SSEMessage[] = []

    while (true) {
      const match = /\r?\n\r?\n/.exec(this.buffer)
      if (!match || match.index == null) break
      const block = this.buffer.slice(0, match.index)
      this.buffer = this.buffer.slice(match.index + match[0].length)
      const parsed = parseBlock(block)
      if (parsed) result.push(parsed)
    }
    return result
  }

  flush(): SSEMessage[] {
    const block = this.buffer.trimEnd()
    this.buffer = ''
    if (!block) return []
    const parsed = parseBlock(block)
    return parsed ? [parsed] : []
  }
}

export function parseJSON<T>(value: string): T | null {
  try {
    return JSON.parse(value) as T
  } catch {
    return null
  }
}
