// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { ServicesContext, type AppServices } from '../../../app/services'
import { ClientsModel } from '../application/clients-model'
import ClientsPage from './ClientsPage'

afterEach(cleanup)
describe('bounded client list', () => {
  it('discloses truncation without replacing the full client count by a subset', async () => {
    const client = { ip: '127.0.0.2', actualRpm: 1, count: 2, active: 0, queued: 0, refused: 0, lastSeen: new Date(0).toISOString(), state: 'idle' as const, banned: false, note: '' }
    const model = new ClientsModel({ list: async () => ({ clients: [client], available: 2_000 }), events: async () => [], saveProfile: async () => ({ clients: [client], available: 2_000 }), subscribe: () => () => {} })
    await model.connect()
    render(<ServicesContext value={{ clients: model } as AppServices}><ClientsPage /></ServicesContext>)
    expect(screen.getByText('Connected clients').parentElement?.querySelector('strong')?.textContent).toBe((2_000).toLocaleString())
    expect(screen.getByText(/Newest 1 of/).textContent).toContain('Load metrics cover this subset')
    expect(screen.getByRole('button', { name: 'Open logs and notes for 127.0.0.2' })).toBeTruthy()
    model.dispose()
  })
})
