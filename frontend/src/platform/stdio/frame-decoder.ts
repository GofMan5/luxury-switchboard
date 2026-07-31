import type { IncomingFrame } from '../../shared/contracts/protocol'
import { PROTOCOL_VERSION } from '../../shared/contracts/protocol'

const MAX_BUFFER_CHARS = 1_048_576

export class FrameDecoder {
  #buffer = ''

  push(chunk: string): IncomingFrame[] {
    this.#buffer += chunk
    if (this.#buffer.length > MAX_BUFFER_CHARS) {
      this.#buffer = ''
      throw new Error('Sidecar protocol frame exceeded the limit')
    }
    const lines = this.#buffer.split(/\r?\n/u)
    this.#buffer = lines.pop() ?? ''
    return lines.flatMap((line) => this.#decode(line))
  }

  flush(): IncomingFrame[] {
    const line = this.#buffer
    this.#buffer = ''
    return this.#decode(line)
  }

  #decode(line: string): IncomingFrame[] {
    if (!line.trim()) return []
    const value: unknown = JSON.parse(line)
    if (!isIncomingFrame(value)) {
      throw new Error('Sidecar returned an invalid protocol frame')
    }
    return [value]
  }
}

function isIncomingFrame(value: unknown): value is IncomingFrame {
  if (!value || typeof value !== 'object') return false
  const frame = value as Record<string, unknown>
  if (frame.v !== PROTOCOL_VERSION) return false
  if (frame.type === 'result') {
    return typeof frame.id === 'string' && typeof frame.ok === 'boolean'
  }
  return (
    frame.type === 'event' &&
    typeof frame.topic === 'string' &&
    Number.isSafeInteger(frame.seq)
  )
}
