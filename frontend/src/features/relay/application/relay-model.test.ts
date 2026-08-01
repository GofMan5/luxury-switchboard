import { describe, expect, it } from 'vitest'
import type { RelaySnapshot } from '../domain/relay'
import type { RelayPort } from './relay-port'
import { RelayModel } from './relay-model'

class FakeRelayPort implements RelayPort {
  snapshot: RelaySnapshot = { state: 'live', address: 'http://127.0.0.1:8798', port: 8798 }
  listener: ((snapshot: RelaySnapshot) => void) | null = null
  statusPromise: Promise<RelaySnapshot> | null = null
  startCalls = 0
  stopCalls = 0

  async status() { return this.statusPromise ?? this.snapshot }
  async start() {
    this.startCalls++
    this.snapshot = { state: 'live', address: 'http://127.0.0.1:8798', port: 8798 }
    this.listener?.(this.snapshot)
    return this.snapshot
  }
  async stop() {
    this.stopCalls++
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

  it('does not overwrite a live event with a stale status response', async () => {
    const port = new FakeRelayPort()
    let resolveStatus!: (snapshot: RelaySnapshot) => void
    port.statusPromise = new Promise((resolve) => { resolveStatus = resolve })
    const model = new RelayModel(port)
    const connecting = model.connect()
    port.listener?.({ state: 'live', address: 'http://127.0.0.1:9000', port: 9000 })
    resolveStatus({ state: 'stopped', address: '', port: 0 })
    await connecting
    expect(model.snapshot().snapshot).toMatchObject({ state: 'live', port: 9000 })
    model.dispose()
  })

  it('retries cleanup after a failed stop instead of starting a second listener', async () => {
    const port = new FakeRelayPort()
    port.snapshot = { state: 'error', address: 'http://127.0.0.1:8798', port: 8798, error: 'Relay could not stop cleanly' }
    const model = new RelayModel(port)
    await model.connect()
    await model.toggle()
    expect(port.stopCalls).toBe(1)
    expect(port.startCalls).toBe(0)
    model.dispose()
  })
})
