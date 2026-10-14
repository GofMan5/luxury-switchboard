import { expect, it } from 'vitest'
import type { ControlPlaneSession, EventListener } from '../../../platform/stdio/session'
import { StdioProvidersPort } from './stdio-providers-port'

it('invalidates catalog key counts on key changes and removes both listeners', () => {
  const listeners = new Map<string, EventListener>()
  const session = { subscribe: (topic: string, listener: EventListener) => {
    listeners.set(topic, listener)
    return () => listeners.delete(topic)
  } } as unknown as ControlPlaneSession
  let updates = 0
  const unsubscribe = new StdioProvidersPort(session).subscribe(() => updates++)
  listeners.get('keys.changed')?.({ v: 1, seq: 1, type: 'event', topic: 'keys.changed', payload: { providerId: 'fixture' } })
  expect(updates).toBe(1)
  unsubscribe()
  expect(listeners.size).toBe(0)
})
