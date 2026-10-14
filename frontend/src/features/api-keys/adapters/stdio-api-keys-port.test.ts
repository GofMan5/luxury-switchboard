import { describe, expect, it, vi } from 'vitest'
import type { ControlPlaneSession } from '../../../platform/stdio/session'
import { StdioApiKeysPort } from './stdio-api-keys-port'

describe('StdioApiKeysPort', () => {
  it('keeps the available pool count instead of presenting a trimmed list as the whole pool', async () => {
    const result = { keys: [], available: 500 }
    const call = vi.fn<ControlPlaneSession['call']>().mockResolvedValue(result)
    const session: ControlPlaneSession = { call: call as ControlPlaneSession['call'], start: async () => undefined, stop: async () => undefined, subscribe: () => () => undefined }
    const controller = new AbortController()
    expect(await new StdioApiKeysPort(session).list('echo', controller.signal)).toEqual(result)
    expect(call).toHaveBeenCalledWith('keys.list', { providerId: 'echo' }, controller.signal)
  })
})
