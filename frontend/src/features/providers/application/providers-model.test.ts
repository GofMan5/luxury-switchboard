import { describe, expect, it } from 'vitest'
import type { Provider, ProviderCatalog, ProviderInput } from '../domain/provider'
import type { ProvidersPort } from './providers-port'
import { ProvidersModel } from './providers-model'

const configured: readonly Provider[] = [
  { id: 'local', name: 'Local', baseUrl: 'http://127.0.0.1:8799', authMode: 'passthrough', authHeader: '', dialect: 'auto', modelsPath: '/v1/models', rpm: 0, cacheTtl: '0s', enabled: true, keyConfigured: false, keyCount: 0, builtin: true },
  { id: 'echo', name: 'Echo', baseUrl: 'https://example.invalid/v1', authMode: 'bearer', authHeader: '', dialect: 'auto', modelsPath: '/v1/models', rpm: 120, cacheTtl: '1h0m0s', enabled: true, keyConfigured: true, keyCount: 1, builtin: true },
]

class FakeProvidersPort implements ProvidersPort {
  activeId = 'echo'
  listener: (() => void) | null = null

  async list(): Promise<ProviderCatalog> { return { activeId: this.activeId, providers: configured } }
  async activate(id: string): Promise<Provider> {
    this.activeId = id
    this.listener?.()
    return configured.find((provider) => provider.id === id) ?? configured[0]
  }
  async add(value: ProviderInput): Promise<Provider> { return { id: 'custom', ...value, cacheTtl: value.cache1h ? '1h0m0s' : '0s', keyConfigured: false, keyCount: 0, builtin: false } }
  async update(id: string, value: ProviderInput): Promise<Provider> { return { id, ...value, cacheTtl: value.cache1h ? '1h0m0s' : '0s', keyConfigured: false, keyCount: 0, builtin: false } }
  async delete(): Promise<void> {}
  subscribe(listener: () => void) {
    this.listener = listener
    return () => { this.listener = null }
  }
}

describe('ProvidersModel', () => {
  it('refreshes authoritative state after activation', async () => {
    const port = new FakeProvidersPort()
    const model = new ProvidersModel(port)
    await model.connect()
    await model.activate('local')
    expect(model.snapshot()).toMatchObject({ phase: 'ready', catalog: { activeId: 'local' }, pendingId: '' })
    model.dispose()
  })
})
