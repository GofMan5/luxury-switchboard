import { describe, expect, it } from 'vitest'
import { FrameDecoder } from './frame-decoder'

describe('FrameDecoder', () => {
  it('keeps partial frames and emits complete NDJSON messages', () => {
    const decoder = new FrameDecoder()
    expect(decoder.push('{"v":1,"type":"event","topic":"relay.')).toEqual([])
    expect(decoder.push('changed","seq":1}\n')).toEqual([
      { v: 1, type: 'event', topic: 'relay.changed', seq: 1 },
    ])
  })

  it('rejects unknown protocol versions', () => {
    const decoder = new FrameDecoder()
    expect(() => decoder.push('{"v":2,"type":"event","topic":"x","seq":1}\n')).toThrow(
      'invalid protocol frame',
    )
  })
})
