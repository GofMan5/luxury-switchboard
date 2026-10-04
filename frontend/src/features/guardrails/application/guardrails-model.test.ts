import { describe, expect, it, vi } from 'vitest'
import type { GuardrailMode, GuardrailRecord, GuardrailStatus } from '../domain/guardrail'
import type { GuardrailsPort } from './guardrails-port'
import { GuardrailsModel } from './guardrails-model'

function record(id: string, overrides: Partial<GuardrailRecord> = {}): GuardrailRecord {
  return {
    id,
    at: new Date(0).toISOString(),
    verdict: 'alert',
    severity: 'high',
    providerId: 'privatka',
    providerName: 'Privatka',
    model: 'claude-opus-5',
    findings: [{ ruleId: 'dl-curl-pipe-sh', category: 'download-exec', severity: 'high', match: 'curl x | sh', excerpt: '', source: 'tool_call:sh', description: 'Shell pipe' }],
    ...overrides,
  }
}

class FakeGuardrailsPort implements GuardrailsPort {
  statusValue: GuardrailStatus = { mode: 'monitor', providerModes: {}, ruleCount: 105, indicatorCount: 8, ruleSetVersion: 4, findingCount: 0 }
  records: readonly GuardrailRecord[] = []
  statusCalls = 0
  findingsCalls = 0
  lastLimit = 0
  clearCalls = 0
  modeWrites: GuardrailMode[] = []
  failStatus = false
  failMode = false
  failClear = false
  emit: ((value: GuardrailRecord) => void) | undefined
  emitStatus: (() => void) | undefined

  async status() {
    this.statusCalls++
    if (this.failStatus) throw new Error('unavailable')
    return this.statusValue
  }

  async findings(limit: number) {
    this.findingsCalls++
    this.lastLimit = limit
    return this.records
  }

  async clear() {
    this.clearCalls++
    if (this.failClear) throw new Error('unavailable')
    this.records = []
  }

  async setMode(mode: GuardrailMode) {
    if (this.failMode) throw new Error('unavailable')
    this.modeWrites.push(mode)
    this.statusValue = { ...this.statusValue, mode }
    return mode
  }

  subscribe(listener: (value: GuardrailRecord) => void) {
    this.emit = listener
    return () => { this.emit = undefined }
  }

  subscribeStatus(listener: () => void) {
    this.emitStatus = listener
    return () => { this.emitStatus = undefined }
  }
}

describe('GuardrailsModel', () => {
  it('loads the rule set state and the findings together', async () => {
    const port = new FakeGuardrailsPort()
    port.records = [record('a'), record('b')]
    const model = new GuardrailsModel(port)
    await model.connect()
    expect(model.snapshot()).toMatchObject({ phase: 'ready', findings: [{ id: 'a' }, { id: 'b' }], error: '' })
    expect(model.snapshot().status).toMatchObject({ mode: 'monitor', providerModes: {}, ruleCount: 105 })
    model.dispose()
  })

  it('shows a new finding without reloading the whole list', async () => {
    // A provider answering badly in a loop must not turn into a reload storm, so the
    // event payload is used as it arrives.
    const port = new FakeGuardrailsPort()
    const model = new GuardrailsModel(port)
    await model.connect()
    const before = port.findingsCalls
    port.emit?.(record('fresh'))
    expect(model.snapshot().findings).toMatchObject([{ id: 'fresh' }])
    expect(model.snapshot().status?.findingCount).toBe(1)
    expect(port.findingsCalls).toBe(before)
    model.dispose()
  })

  it('ignores a finding that arrives twice', async () => {
    // The same record can be replayed across a reconnect, and a duplicate row would
    // read as a second attack rather than one.
    const port = new FakeGuardrailsPort()
    const model = new GuardrailsModel(port)
    await model.connect()
    port.emit?.(record('same'))
    port.emit?.(record('same'))
    expect(model.snapshot().findings).toHaveLength(1)
    expect(model.snapshot().status?.findingCount).toBe(1)
    model.dispose()
  })

  it('replaces a row the control plane keeps folding instead of adding another', async () => {
    // The control plane keeps one row for "part of this answer was never read" per
    // provider and raises its count. Each update arrives under the same id, so it has
    // to refresh that row rather than be dropped as a duplicate or stack up beside it.
    const port = new FakeGuardrailsPort()
    const model = new GuardrailsModel(port)
    await model.connect()
    const partial = { id: 'gr_2', severity: 'low' as const, findings: [{ ruleId: 'proto-inspection-truncated', category: 'protocol', severity: 'low' as const, match: '', excerpt: '', source: 'inspection', description: 'Partly unread' }] }
    port.emit?.(record('gr_1'))
    port.emit?.(record('gr_2', { ...partial, occurrences: 1 }))
    port.emit?.(record('gr_2', { ...partial, occurrences: 2, at: new Date(60_000).toISOString() }))
    const findings = model.snapshot().findings
    expect(findings).toHaveLength(2)
    expect(findings[0]).toMatchObject({ id: 'gr_2', occurrences: 2 })
    // The real detection is still there, and the count did not double-count one row.
    expect(findings[1]?.id).toBe('gr_1')
    expect(model.snapshot().status?.findingCount).toBe(2)
    model.dispose()
  })

  it('bounds what it holds when a provider keeps sending payloads', async () => {
    const port = new FakeGuardrailsPort()
    const model = new GuardrailsModel(port)
    await model.connect()
    for (let index = 0; index < 260; index++) port.emit?.(record(`r${index}`))
    const findings = model.snapshot().findings
    expect(findings).toHaveLength(200)
    // Newest first: the most recent payload is the one the operator needs to see.
    expect(findings[0]?.id).toBe('r259')
    model.dispose()
  })

  it('applies a mode change immediately and never asks for a restart', async () => {
    const port = new FakeGuardrailsPort()
    const model = new GuardrailsModel(port)
    await model.connect()
    expect(await model.setMode('block')).toBe(true)
    expect(port.modeWrites).toEqual(['block'])
    expect(model.snapshot().status?.mode).toBe('block')
    expect(model.snapshot().pending).toBe(false)
    model.dispose()
  })

  it('does not write a mode that is already active', async () => {
    const port = new FakeGuardrailsPort()
    const model = new GuardrailsModel(port)
    await model.connect()
    expect(await model.setMode('monitor')).toBe(true)
    expect(port.modeWrites).toEqual([])
    model.dispose()
  })

  it('rejects an unrecognised mode instead of sending it', async () => {
    const port = new FakeGuardrailsPort()
    const model = new GuardrailsModel(port)
    await model.connect()
    expect(await model.setMode('blocking' as GuardrailMode)).toBe(false)
    expect(port.modeWrites).toEqual([])
    expect(model.snapshot().error).toContain('not recognised')
    model.clearError()
    expect(model.snapshot().error).toBe('')
    model.dispose()
  })

  it('keeps the active mode when the write fails', async () => {
    const port = new FakeGuardrailsPort()
    const model = new GuardrailsModel(port)
    await model.connect()
    port.failMode = true
    expect(await model.setMode('block')).toBe(false)
    expect(model.snapshot().status?.mode).toBe('monitor')
    expect(model.snapshot().error).toContain('could not be saved')
    expect(model.snapshot().pending).toBe(false)
    model.dispose()
  })

  it('empties the list and the count when findings are cleared', async () => {
    const port = new FakeGuardrailsPort()
    port.records = [record('a')]
    port.statusValue = { ...port.statusValue, findingCount: 1 }
    const model = new GuardrailsModel(port)
    await model.connect()
    expect(await model.clear()).toBe(true)
    expect(model.snapshot().findings).toEqual([])
    expect(model.snapshot().status?.findingCount).toBe(0)
    model.dispose()
  })

  it('keeps the findings visible when clearing fails', async () => {
    const port = new FakeGuardrailsPort()
    port.records = [record('a')]
    const model = new GuardrailsModel(port)
    await model.connect()
    port.failClear = true
    expect(await model.clear()).toBe(false)
    expect(model.snapshot().findings).toMatchObject([{ id: 'a' }])
    expect(model.snapshot().error).toContain('could not be cleared')
    model.dispose()
  })

  it('reports an error instead of pretending nothing is wrong', async () => {
    const port = new FakeGuardrailsPort()
    port.failStatus = true
    const model = new GuardrailsModel(port)
    await model.connect()
    expect(model.snapshot()).toMatchObject({ phase: 'error', error: 'The guardrails are unavailable' })
    model.dispose()
  })

  it('reloads when the control plane says the state changed', async () => {
    const port = new FakeGuardrailsPort()
    const model = new GuardrailsModel(port)
    await model.connect()
    port.records = [record('after-clear')]
    port.emitStatus?.()
    await vi.waitFor(() => expect(model.snapshot().findings).toMatchObject([{ id: 'after-clear' }]))
    model.dispose()
  })

  it('drops a stale reload so it cannot overwrite a newer one', async () => {
    const port = new FakeGuardrailsPort()
    const model = new GuardrailsModel(port)
    await model.connect()
    port.records = [record('stale')]
    const stale = model.refresh()
    port.records = [record('fresh')]
    await model.refresh()
    await stale
    expect(model.snapshot().findings).toMatchObject([{ id: 'fresh' }])
    model.dispose()
  })

  it('stops listening once disposed', async () => {
    const port = new FakeGuardrailsPort()
    const model = new GuardrailsModel(port)
    await model.connect()
    model.dispose()
    expect(port.emit).toBeUndefined()
    expect(port.emitStatus).toBeUndefined()
  })
})
