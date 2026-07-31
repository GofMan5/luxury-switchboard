import { describe, expect, it } from 'vitest'
import { decodeIncomingFrame } from '../../shared/contracts/protocol'

describe('decodeIncomingFrame', () => {
  it('accepts a complete validated event from the Rust bridge', () => {
    expect(decodeIncomingFrame('{"v":1,"type":"event","topic":"relay.changed","seq":1}')).toEqual(
      { v: 1, type: 'event', topic: 'relay.changed', seq: 1 },
    )
  })

  it('rejects unknown protocol versions', () => {
    expect(() => decodeIncomingFrame('{"v":2,"type":"event","topic":"x","seq":1}')).toThrow(
      'invalid protocol frame',
    )
  })

  it('rejects malformed failures and negative event sequences', () => {
    expect(() => decodeIncomingFrame('{"v":1,"type":"result","id":"x","ok":false}')).toThrow('invalid protocol frame')
    expect(() => decodeIncomingFrame('{"v":1,"type":"event","topic":"x","seq":-1}')).toThrow('invalid protocol frame')
  })
})
