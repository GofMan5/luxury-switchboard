import { describe, expect, it } from 'vitest'
import type { AddApiKey, ApiKey, UpdateApiKey } from '../domain/api-key'
import { ApiKeysModel } from './api-keys-model'
import type { ApiKeysPort } from './api-keys-port'

const key: ApiKey = { id: 'key-1', providerId: 'echo', label: 'Primary', priority: 0, rpm: 30, pinned: false, proxyConfigured: false, cooldownMs: 0, blockedModels: 0, retries429: 0, startsInWindow: 0 }

class Port implements ApiKeysPort {
  listener: ((providerId: string) => void) | undefined
  finishAdd: ((value: ApiKey) => void) | undefined
  list = async () => []
  add = async (_value: AddApiKey) => new Promise<ApiKey>((resolve) => { this.finishAdd = resolve; this.listener?.('echo') })
  update = async (_value: UpdateApiKey) => key
  remove = async () => undefined
  move = async () => undefined
  reset = async () => undefined
  subscribe = (listener: (providerId: string) => void) => { this.listener = listener; return () => undefined }
}

describe('ApiKeysModel', () => {
  it('keeps a mutation pending while its change event refreshes the list', async () => {
    const port = new Port()
    const model = new ApiKeysModel(port)
    await model.load('echo')
    const mutation = model.add({ providerId: 'echo', label: 'Primary', secret: 'secret', rpm: 30, proxyUrl: '' })
    await Promise.resolve()
    await Promise.resolve()
    expect(model.snapshot().pendingId).toBe('new')
    port.finishAdd?.(key)
    expect(await mutation).toBe(true)
    expect(model.snapshot().pendingId).toBe('')
  })
})
