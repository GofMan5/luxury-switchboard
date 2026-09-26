import { describe, expect, it } from 'vitest'
import type { AddApiKey, ApiKey, UpdateApiKey } from '../domain/api-key'
import { ApiKeysModel } from './api-keys-model'
import type { ApiKeysPort } from './api-keys-port'

const key: ApiKey = { id: 'key-1', providerId: 'echo', label: 'Primary', priority: 0, rpm: 30, pinned: false, proxyConfigured: false, cooldownMs: 0, blockedModels: 0, retries429: 0, startsInWindow: 0, authStreak: 0, lastOutcome: '' }

class Port implements ApiKeysPort {
  listener: ((providerId: string) => void) | undefined
  finishAdd: ((value: ApiKey) => void) | undefined
  list: ApiKeysPort['list'] = async () => []
  add = async (_value: AddApiKey) => new Promise<ApiKey>((resolve) => { this.finishAdd = resolve; this.listener?.('echo') })
  addMany: ApiKeysPort['addMany'] = async () => ({ added: 0, duplicate: [], rejected: [] })
  update = async (_value: UpdateApiKey) => key
  remove = async () => undefined
  move = async () => undefined
  reset = async () => undefined
  checkPool: ApiKeysPort['checkPool'] = async () => ({ checked: 0, rejected: 0, reachable: true })
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

  it('reports what a bulk import added and skipped, and never sends an empty batch', async () => {
    const port = new Port()
    let batches = 0
    port.addMany = async (value) => { batches += 1; return { added: value.entries.length - 1, duplicate: [1], rejected: [] } }
    const model = new ApiKeysModel(port)
    await model.load('echo')
    const report = await model.importKeys({
      providerId: 'echo', rpm: 30, proxyUrl: '',
      entries: [{ label: 'One', secret: 'one' }, { label: 'Again', secret: 'one' }],
    })
    expect(report).toEqual({ added: 1, duplicate: [1], rejected: [] })
    expect(model.snapshot().pendingId).toBe('')
    expect(await model.importKeys({ providerId: 'echo', rpm: 30, proxyUrl: '', entries: [] })).toBeNull()
    expect(batches).toBe(1)
  })

  it('does not show keys from the previous provider while switching', async () => {
    let finishSwitch!: (value: readonly ApiKey[]) => void
    const port = new Port()
    port.list = async (providerId: string) => providerId === 'echo' ? [key] : await new Promise<readonly ApiKey[]>((resolve) => { finishSwitch = resolve })
    const model = new ApiKeysModel(port)
    await model.load('echo')
    const switching = model.load('other')
    expect(model.snapshot()).toMatchObject({ providerId: 'other', phase: 'loading', keys: [] })
    finishSwitch([])
    await switching
  })
})
