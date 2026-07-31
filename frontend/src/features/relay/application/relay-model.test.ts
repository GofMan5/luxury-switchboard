import { describe, expect, it } from 'vitest'
import type { RelaySnapshot } from '../domain/relay'
import type { RelayPort } from './relay-port'
import { RelayModel } from './relay-model'

class FakeRelayPort implements RelayPort {
  snapshot: RelaySnapshot = { state: 'live', address: 'http://127.0.0.1:8798', port: 8798 }
  listener: ((snapshot: RelaySnapshot) => void) | null = null

  async status() { return this.snapshot }
  async start() {
    this.snapshot = { state: 'live', address: 'http://127.0.0.1:8798', port: 8798 }
    this.listener?.(this.snapshot)
    return this.snapshot
  }
  async stop() {
    this.snapshot = { state: 'stopped', address: '', port: 0 }
    this.listener?.(this.snapshot)
    return this.snapshot
  }
  subscribe(listener: (snapshot: RelaySnapshot) => void) {
    this.listener = listener
    return () => { this.listener = null }
  }
}

describe('RelayModel', () => {
  it('loads and toggles the real relay port state', async () => {
    const port = new FakeRelayPort()
    const model = new RelayModel(port)
    await model.connect()
    expect(model.snapshot().snapshot.state).toBe('live')
    await model.toggle()
    expect(model.snapshot()).toMatchObject({ pending: false, snapshot: { state: 'stopped' } })
    model.dispose()
  })
})
