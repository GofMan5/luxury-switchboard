// @vitest-environment jsdom

import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ControlPlaneSession } from '../platform/stdio/session'

const createSession = vi.hoisted(() => vi.fn())
vi.mock('../platform/stdio/create-session', () => ({ createControlPlaneSession: createSession }))

import { ServicesProvider } from './ServicesProvider'

describe('ServicesProvider', () => {
  beforeEach(() => createSession.mockReset())

  it('stops a session that finishes starting after the UI was disposed', async () => {
    let resolveSession!: (session: ControlPlaneSession) => void
    createSession.mockReturnValue(new Promise<ControlPlaneSession>((resolve) => { resolveSession = resolve }))
    const session: ControlPlaneSession = {
      start: vi.fn(async () => undefined),
      stop: vi.fn(async () => undefined),
      call: vi.fn(),
      subscribe: vi.fn(() => () => undefined),
    }
    const view = render(<ServicesProvider><div>ready</div></ServicesProvider>)
    view.unmount()
    await act(async () => { resolveSession(session) })
    await waitFor(() => expect(session.stop).toHaveBeenCalledOnce())
  })

  it('allows a failed initial sidecar connection to be retried', async () => {
    createSession
      .mockRejectedValueOnce(new Error('injected start failure'))
      .mockReturnValueOnce(new Promise<ControlPlaneSession>(() => undefined))
    const view = render(<ServicesProvider><div>ready</div></ServicesProvider>)
    fireEvent.click(await screen.findByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(createSession).toHaveBeenCalledTimes(2))
    view.unmount()
  })
})
