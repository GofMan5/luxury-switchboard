// @vitest-environment jsdom

import { act, render, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { ControlPlaneSession } from '../platform/stdio/session'

const createSession = vi.hoisted(() => vi.fn())
vi.mock('../platform/stdio/create-session', () => ({ createControlPlaneSession: createSession }))

import { ServicesProvider } from './ServicesProvider'

describe('ServicesProvider', () => {
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
})
