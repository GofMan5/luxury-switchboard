import { describe, expect, it } from 'vitest'
import type { TunnelClientEvent } from '../domain/client'
import type { ClientsPort } from './clients-port'
import { ClientsModel } from './clients-model'

class FakeClientsPort implements ClientsPort {
  eventsForClient: readonly TunnelClientEvent[] = []
  async list() { return [{ ip: '127.0.0.2', actualRpm: 1, count: 2, active: 0, queued: 0, lastSeen: new Date(0).toISOString(), state: 'idle' }] }
  async events() { return this.eventsForClient }
  subscribe() { return () => undefined }
}

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
})
