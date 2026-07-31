import { describe, expect, it } from 'vitest'
import type { SharedSnapshot } from '../domain/snapshot'
import type { SharedPort } from './shared-port'
import { SharedModel } from './shared-model'

const snapshot = (state: 'running' | 'paused'): SharedSnapshot => ({ available: true, revision: state === 'running' ? 1 : 2, tunnels: [{ position: 0, name: 'Ваш коннект', state }], error: '' })

class FakeSharedPort implements SharedPort {
  listPromise: Promise<SharedSnapshot> = Promise.resolve(snapshot('running'))
  async list() { return this.listPromise }
  async control() { return snapshot('paused') }
}

describe('SharedModel', () => {
  it('does not overwrite a control result with an older poll', async () => {
    let resolveList!: (value: SharedSnapshot) => void
    const port = new FakeSharedPort()
    port.listPromise = new Promise((resolve) => { resolveList = resolve })
    const model = new SharedModel(port)
    const refreshing = model.refresh()
    await model.control(0, 'pause')
    resolveList(snapshot('running'))
    await refreshing
    expect(model.snapshot().snapshot.tunnels[0]?.state).toBe('paused')
    model.dispose()
  })
})
