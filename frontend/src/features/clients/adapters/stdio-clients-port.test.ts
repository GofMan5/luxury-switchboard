import { describe, expect, it } from 'vitest'
import type { ControlPlaneSession } from '../../../platform/stdio/session'
import { ClientsModel } from '../application/clients-model'
import { StdioClientsPort } from './stdio-clients-port'
import type { TunnelClient } from '../domain/client'

const client: TunnelClient = { ip: '127.0.0.2', actualRpm: 1, count: 2, active: 0, queued: 0, refused: 0, lastSeen: new Date(0).toISOString(), state: 'idle', banned: false, note: '' }

describe('bounded clients replies', () => {
  it('preserves the total rather than reporting a frame-sized subset as the whole list', async () => {
    const session = {
      call: async (method: string) => ({ clients: [{ ...client, banned: method === 'clients.profile' }], available: 2_000 }),
      subscribe: () => () => {},
    } as unknown as ControlPlaneSession
    const model = new ClientsModel(new StdioClientsPort(session))
    await model.connect()
    expect(model.snapshot()).toMatchObject({ available: 2_000, clients: [client] })
    expect(await model.saveProfile({ ip: client.ip, banned: true, note: '' })).toBe(true)
    expect(model.snapshot()).toMatchObject({ available: 2_000, clients: [{ banned: true }], pendingIp: '', error: '' })
    model.dispose()
  })
})
