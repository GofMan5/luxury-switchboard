import { afterEach, describe, expect, it, vi } from 'vitest'
import type { TunnelClientEvent } from '../domain/client'
import type { ClientsPort } from './clients-port'
import { ClientsModel } from './clients-model'

class FakeClientsPort implements ClientsPort {
  eventsForClient: readonly TunnelClientEvent[] = []
  eventPromises = new Map<string, Promise<readonly TunnelClientEvent[]>>()
  listCalls = 0
  listener: (() => void) | undefined
  async list() { this.listCalls++; return [{ ip: '127.0.0.2', actualRpm: 1, count: 2, active: 0, queued: 0, lastSeen: new Date(0).toISOString(), state: 'idle' }] }
  async events(ip: string) { return this.eventPromises.get(ip) ?? this.eventsForClient }
  subscribe(listener: () => void) { this.listener = listener; return () => { this.listener = undefined } }
}

afterEach(() => vi.useRealTimers())

describe('ClientsModel', () => {
  it('keeps an open client inspector live during refresh', async () => {
    const port = new FakeClientsPort()
    const model = new ClientsModel(port)
    await model.connect()
    await model.select('127.0.0.2')
    port.eventsForClient = [{ id: 'event-1', ip: '127.0.0.2', time: new Date(0).toISOString(), state: 'completed', method: 'POST', path: '/v1/responses', model: 'public-model', status: 200, latencyMs: 10, bytesIn: 1, bytesOut: 2 }]
    await model.refresh()
    expect(model.snapshot()).toMatchObject({ selectedIp: '127.0.0.2', events: [{ id: 'event-1' }] })
    model.dispose()
  })

  it('does not attach late events to a newly selected client', async () => {
    const port = new FakeClientsPort()
    const model = new ClientsModel(port)
    await model.connect()
    await model.select('client-a')
    let resolveA!: (events: readonly TunnelClientEvent[]) => void
    port.eventPromises.set('client-a', new Promise((resolve) => { resolveA = resolve }))
    const refreshing = model.refresh()
    port.eventsForClient = [{ id: 'event-b', ip: 'client-b', time: new Date(0).toISOString(), state: 'completed', method: 'POST', path: '/v1/responses', model: 'public', status: 200, latencyMs: 1, bytesIn: 1, bytesOut: 1 }]
    await model.select('client-b')
    resolveA([{ id: 'event-a', ip: 'client-a', time: new Date(0).toISOString(), state: 'completed', method: 'POST', path: '/v1/responses', model: 'public', status: 200, latencyMs: 1, bytesIn: 1, bytesOut: 1 }])
    await refreshing
    expect(model.snapshot()).toMatchObject({ selectedIp: 'client-b', events: [{ id: 'event-b' }] })
    model.dispose()
  })

  it('refreshes during sustained activity instead of waiting for a quiet gap', async () => {
    vi.useFakeTimers()
    const port = new FakeClientsPort()
    const model = new ClientsModel(port)
    await model.connect()
    for (let index = 0; index < 5; index++) {
      port.listener?.()
      await vi.advanceTimersByTimeAsync(100)
    }
    expect(port.listCalls).toBeGreaterThan(1)
    expect(port.listCalls).toBeLessThanOrEqual(4)
    model.dispose()
  })
})
