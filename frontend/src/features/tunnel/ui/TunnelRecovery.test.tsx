// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { ServicesContext, type AppServices } from '../../../app/services'
import { TunnelModel } from '../application/tunnel-model'
import type { TunnelPort } from '../application/tunnel-port'
import type { TunnelSnapshot } from '../domain/tunnel'
import TunnelPage from './TunnelPage'

afterEach(cleanup)

it('shows an initial tunnel failure and retries the snapshot without starting public traffic', async () => {
  const stopped: TunnelSnapshot = { state: 'stopped', port: 8797, address: '', rpmPerIp: 60, contextLimitKiB: 0, brandResponse: '', tokenConfigured: true }
  let finish!: (snapshot: TunnelSnapshot) => void
  const get = vi.fn<TunnelPort['get']>().mockRejectedValueOnce(new Error('offline'))
    .mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
  const start = vi.fn(async () => stopped)
  const port: TunnelPort = {
    get, start, stop: async () => stopped, configure: async () => stopped,
    rotate: async () => '', reveal: async () => '', privacyTest: async () => { throw new Error('not requested') }, subscribe: vi.fn(() => () => {}),
  }
  const model = new TunnelModel(port)
  await model.connect()
  const navigate = vi.fn()
  render(<ServicesContext.Provider value={{ tunnel: model } as AppServices}>
    <nav aria-label="Main navigation"><button type="button" onClick={navigate}>Activity</button></nav>
    <TunnelPage />
  </ServicesContext.Provider>)
  expect(screen.getByRole('alert').textContent).toContain('Tunnel is unavailable')
  expect(screen.queryByText(/Loading secure gateway/)).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: 'Activity' }))
  expect(navigate).toHaveBeenCalledOnce()
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(screen.getByRole('status').textContent).toContain('Loading secure gateway')
  expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
  await act(async () => { finish(stopped) })
  expect(screen.getByRole('heading', { name: 'Stopped' })).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Start' })).toBeTruthy()
  expect(screen.queryByRole('alert')).toBeNull()
  expect(get).toHaveBeenCalledTimes(2)
  expect(start).not.toHaveBeenCalled()
  expect(port.subscribe).toHaveBeenCalledTimes(1)
  model.dispose()
})
