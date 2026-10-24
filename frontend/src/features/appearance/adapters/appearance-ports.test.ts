// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { stubLocalStorage } from '../../../test/memory-storage'
import type { ControlPlaneSession } from '../../../platform/stdio/session'
import { APPEARANCE_DEFAULTS } from '../domain/appearance'
import { LocalAppearancePort, localAppearanceCache } from './local-appearance'
import { StdioAppearancePort } from './stdio-appearance-port'

function session(call: ControlPlaneSession['call'], subscribe: ControlPlaneSession['subscribe'] = () => () => undefined): ControlPlaneSession {
  return { call, subscribe, start: async () => undefined, stop: async () => undefined }
}

describe('StdioAppearancePort', () => {
  it('reads and writes the bare record and normalizes what comes back', async () => {
    const call = vi.fn<ControlPlaneSession['call']>().mockResolvedValue({ theme: 'ink', density: 'nonsense' })
    const port = new StdioAppearancePort(session(call as ControlPlaneSession['call']))
    const controller = new AbortController()
    expect(await port.get(controller.signal)).toEqual({ ...APPEARANCE_DEFAULTS, theme: 'ink' })
    expect(call).toHaveBeenCalledWith('appearance.get', undefined, controller.signal)
    await port.update({ ...APPEARANCE_DEFAULTS, theme: 'moss' })
    expect(call).toHaveBeenLastCalledWith('appearance.update', { ...APPEARANCE_DEFAULTS, theme: 'moss' }, undefined)
    expect(port.durable).toBe(true)
  })

  it('forwards appearance.changed events as normalized records', () => {
    let handler: ((event: { payload: unknown }) => void) | undefined
    const subscribe = vi.fn((_topic: string, listener: (event: { payload: unknown }) => void) => { handler = listener; return () => undefined })
    const port = new StdioAppearancePort(session(vi.fn() as unknown as ControlPlaneSession['call'], subscribe as unknown as ControlPlaneSession['subscribe']))
    const listener = vi.fn()
    port.subscribe(listener)
    expect(subscribe).toHaveBeenCalledWith('appearance.changed', expect.any(Function))
    handler?.({ payload: { theme: 'paper' } })
    expect(listener).toHaveBeenCalledWith({ ...APPEARANCE_DEFAULTS, theme: 'paper' })
  })
})

describe('LocalAppearancePort', () => {
  beforeEach(() => { stubLocalStorage() })

  it('keeps the record in the browser profile and says it is not durable', async () => {
    const port = new LocalAppearancePort()
    expect(port.durable).toBe(false)
    expect(await port.get()).toEqual(APPEARANCE_DEFAULTS)
    await port.update({ ...APPEARANCE_DEFAULTS, theme: 'porcelain' })
    expect((await port.get()).theme).toBe('porcelain')
    expect(localAppearanceCache.read()?.theme).toBe('porcelain')
  })

  it('treats a corrupt cache as absent', () => {
    window.localStorage.setItem('switchboard.appearance', '{broken')
    expect(localAppearanceCache.read()).toBeNull()
  })
})
