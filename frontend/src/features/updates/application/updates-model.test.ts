import { describe, expect, it, vi } from 'vitest'
import { UpdatesModel } from './updates-model'
import type { UpdateCheck } from '../domain/update'

function port(answer: UpdateCheck) {
  return { check: vi.fn(async () => answer) }
}

const newer: UpdateCheck = { current: '1.0.38', latest: '1.0.39', url: 'https://example.test/rel', newer: true, reachable: true, checkedAt: '2026-09-29T12:00:00Z' }

describe('UpdatesModel', () => {
  it('publishes the check the backend answered', async () => {
    const updates = new UpdatesModel(port(newer))
    await updates.check()
    expect(updates.snapshot().check?.newer).toBe(true)
    expect(updates.snapshot().check?.latest).toBe('1.0.39')
  })

  it('stays silent when the check itself fails', async () => {
    const updates = new UpdatesModel({ check: vi.fn(async () => { throw new Error('offline') }) })
    await updates.check()
    expect(updates.snapshot().check).toBeNull()
  })
})
