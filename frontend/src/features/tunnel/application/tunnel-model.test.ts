import { describe, expect, it } from 'vitest'
import type { TunnelSnapshot } from '../domain/tunnel'
import type { TunnelPort } from './tunnel-port'
import { TunnelModel } from './tunnel-model'

const stopped: TunnelSnapshot = { state: 'stopped', port: 8797, address: '', rpmPerIp: 0, contextLimitKiB: 0, brandResponse: '', publisherProfile: '', tokenConfigured: true }
const online: TunnelSnapshot = { ...stopped, state: 'online', address: 'http://127.0.0.1:8797/v1' }

class FakeTunnelPort implements TunnelPort {
  listener: ((snapshot: TunnelSnapshot) => void) | null = null
  getPromise: Promise<TunnelSnapshot> = Promise.resolve(stopped)
  async get() { return this.getPromise }
  async configure() { return stopped }
  async start() { return online }
  async stop() { return stopped }
  async rotate() { return 'token' }
  async reveal() { return 'token' }
  subscribe(listener: (snapshot: TunnelSnapshot) => void) { this.listener = listener; return () => { this.listener = null } }
}

describe('TunnelModel', () => {
  it('does not overwrite a live event with a stale initial snapshot', async () => {
    const port = new FakeTunnelPort()
    let resolveGet!: (snapshot: TunnelSnapshot) => void
    port.getPromise = new Promise((resolve) => { resolveGet = resolve })
    const model = new TunnelModel(port)
    const connecting = model.connect()
    port.listener?.(online)
    resolveGet(stopped)
    await connecting
    expect(model.snapshot().snapshot).toMatchObject({ state: 'online' })
    model.dispose()
  })
})
