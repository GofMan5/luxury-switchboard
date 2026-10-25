import { describe, expect, it, vi } from 'vitest'
import { ControlPlaneError } from '../../../shared/contracts/protocol'
import type { RecentError, RequestRule, RetryRule, RuleStats } from '../domain/rules'
import { RulesModel } from './rules-model'
import type { RulesPort } from './rules-port'

const request = (id: string, patch: Partial<RequestRule> = {}): RequestRule => ({
  id, name: id.toUpperCase(), enabled: true, match: { models: [], endpoints: [] }, effects: { tags: ['t'] }, ...patch,
})

const retry = (id: string): RetryRule => ({
  id, name: id.toUpperCase(), enabled: true,
  match: { statuses: [503], contains: [], errorCodes: [], providers: [], models: [] },
  action: 'retry', maxAttempts: 2, delayMs: 0,
})

/** An in-memory control plane: answers like the rules slice and lets a test
 * break any one method. */
function fakePort(seed: { requestRules?: RequestRule[]; retryRules?: RetryRule[] } = {}) {
  let requestRules = seed.requestRules ?? [request('a'), request('b'), request('c')]
  let retryRules = seed.retryRules ?? [retry('r1')]
  let changed: (() => void) | null = null
  const stats: Record<string, RuleStats> = { a: { hits: 4, windowRequests: 0, windowTokens: 0 } }
  const errors: RecentError[] = [{ signature: 's1', status: 503, errorCode: 'upstream_status', detail: 'busy', providerName: 'North', model: 'm', count: 2, lastAt: '2026-01-01T00:00:00Z' }]
  const port = {
    list: vi.fn(async () => ({ requestRules, retryRules })),
    saveRequestRule: vi.fn(async (rule: RequestRule) => {
      const index = requestRules.findIndex((entry) => entry.id === rule.id)
      requestRules = index < 0 ? [...requestRules, rule] : requestRules.map((entry) => (entry.id === rule.id ? rule : entry))
      return rule
    }),
    deleteRequestRule: vi.fn(async (id: string) => {
      if (!requestRules.some((rule) => rule.id === id)) throw new ControlPlaneError('rule_not_found', 'Rule not found')
      requestRules = requestRules.filter((rule) => rule.id !== id)
    }),
    reorderRequestRules: vi.fn(async (ids: readonly string[]) => {
      requestRules = ids.map((id) => requestRules.find((rule) => rule.id === id) as RequestRule)
      return requestRules
    }),
    saveRetryRule: vi.fn(async (rule: RetryRule) => {
      retryRules = [...retryRules.filter((entry) => entry.id !== rule.id), rule]
      return rule
    }),
    deleteRetryRule: vi.fn(async (id: string) => { retryRules = retryRules.filter((rule) => rule.id !== id) }),
    reorderRetryRules: vi.fn(async (ids: readonly string[]) => {
      retryRules = ids.map((id) => retryRules.find((rule) => rule.id === id) as RetryRule)
      return retryRules
    }),
    stats: vi.fn(async () => stats),
    test: vi.fn(async () => ({ requestRules: ['a'], effects: {} })),
    recentErrors: vi.fn(async () => errors),
    subscribe: vi.fn((listener: () => void) => { changed = listener; return () => { changed = null } }),
  } satisfies RulesPort
  return { port, emitChanged: () => changed?.(), stats, setRequestRules: (rules: RequestRule[]) => { requestRules = rules } }
}

const flush = () => new Promise((resolve) => setTimeout(resolve, 0))

describe('RulesModel', () => {
  it('loads rules and counters, then follows rules.changed', async () => {
    const fake = fakePort()
    const model = new RulesModel(fake.port)
    const phases: string[] = []
    model.subscribe(() => phases.push(model.snapshot().phase))
    await model.connect()
    expect(model.snapshot()).toMatchObject({ phase: 'ready', loadError: '' })
    expect(model.snapshot().requestRules.map((rule) => rule.id)).toEqual(['a', 'b', 'c'])
    expect(model.snapshot().stats.a?.hits).toBe(4)
    expect(phases[0]).toBe('loading')

    fake.setRequestRules([request('z')])
    fake.emitChanged()
    await flush()
    expect(model.snapshot().requestRules.map((rule) => rule.id)).toEqual(['z'])

    model.dispose()
    expect(fake.emitChanged).not.toThrow()
  })

  it('subscribes once across reconnects', async () => {
    const fake = fakePort()
    const model = new RulesModel(fake.port)
    await model.connect()
    await model.connect()
    expect(fake.port.subscribe).toHaveBeenCalledTimes(1)
  })

  it('reports an unavailable list instead of an empty one', async () => {
    const fake = fakePort()
    fake.port.list.mockRejectedValueOnce(new ControlPlaneError('secure_storage_unavailable', 'Encrypted storage is unavailable'))
    const model = new RulesModel(fake.port)
    await model.load()
    expect(model.snapshot()).toMatchObject({ phase: 'error', loadError: 'Encrypted storage is unavailable', requestRules: [] })
    await model.load()
    expect(model.snapshot().phase).toBe('ready')
  })

  it('still loads the rules when the counters fail', async () => {
    const fake = fakePort()
    fake.port.stats.mockRejectedValueOnce(new Error('down'))
    const model = new RulesModel(fake.port)
    await model.load()
    expect(model.snapshot()).toMatchObject({ phase: 'ready', stats: {} })
    fake.port.stats.mockRejectedValueOnce(new Error('down'))
    await model.refreshStats()
    expect(model.snapshot().stats).toEqual({})
    await model.refreshStats()
    expect(model.snapshot().stats.a?.hits).toBe(4)
  })

  it('saves from the editor and returns the control plane\'s reason on refusal', async () => {
    const fake = fakePort()
    const model = new RulesModel(fake.port)
    await model.load()
    expect(await model.saveRequestRule(request('d'))).toEqual({ ok: true })
    expect(model.snapshot().requestRules.map((rule) => rule.id)).toEqual(['a', 'b', 'c', 'd'])
    expect(model.snapshot().pending).toBe('')

    fake.port.saveRetryRule.mockRejectedValueOnce(new ControlPlaneError('invalid_rule', 'Match on at least one status, phrase or error code.'))
    expect(await model.saveRetryRule(retry('r2'))).toEqual({ ok: false, message: 'Match on at least one status, phrase or error code.' })
    fake.port.saveRetryRule.mockRejectedValueOnce(new Error('socket closed'))
    expect(await model.saveRetryRule(retry('r2'))).toEqual({ ok: false, message: 'The rule could not be saved.' })
    // An editor refusal is not a list error.
    expect(model.snapshot().error).toBe('')
  })

  it('refuses a second change while one is in flight', async () => {
    const fake = fakePort()
    const model = new RulesModel(fake.port)
    await model.load()
    let release!: () => void
    fake.port.saveRequestRule.mockImplementationOnce(async (rule: RequestRule) => { await new Promise<void>((resolve) => { release = resolve }); return rule })
    const first = model.setRequestRuleEnabled(request('a'), false)
    expect(model.snapshot().pending).toBe('a')
    expect(await model.saveRequestRule(request('b'))).toEqual({ ok: false, message: 'Another change is still being saved.' })
    release()
    expect(await first).toBe(true)
    expect(model.snapshot().pending).toBe('')
  })

  it('toggles a rule with every other field kept, including a plugin owner', async () => {
    const owned = request('a', { owner: 'plugin:team-tags' })
    const fake = fakePort({ requestRules: [owned] })
    const model = new RulesModel(fake.port)
    await model.load()
    await model.setRequestRuleEnabled(owned, false)
    expect(fake.port.saveRequestRule).toHaveBeenCalledWith({ ...owned, enabled: false })
  })

  it('reorders one step and ignores a move past either end', async () => {
    const fake = fakePort()
    const model = new RulesModel(fake.port)
    await model.load()
    expect(await model.moveRequestRule('a', -1)).toBe(false)
    expect(fake.port.reorderRequestRules).not.toHaveBeenCalled()
    expect(await model.moveRequestRule('a', 1)).toBe(true)
    expect(fake.port.reorderRequestRules).toHaveBeenCalledWith(['b', 'a', 'c'])
    expect(model.snapshot().requestRules.map((rule) => rule.id)).toEqual(['b', 'a', 'c'])
  })

  it('shows a refused list change and the list as it now is', async () => {
    const fake = fakePort()
    const model = new RulesModel(fake.port)
    await model.load()
    fake.setRequestRules([request('a'), request('b')])
    expect(await model.deleteRequestRule('c')).toBe(false)
    expect(model.snapshot()).toMatchObject({ error: 'Rule not found', pending: '' })
    expect(model.snapshot().requestRules.map((rule) => rule.id)).toEqual(['a', 'b'])
    model.clearError()
    expect(model.snapshot().error).toBe('')
  })

  it('keeps unavailable history apart from an empty one', async () => {
    const fake = fakePort()
    const model = new RulesModel(fake.port)
    await model.load()
    fake.port.recentErrors.mockRejectedValueOnce(new ControlPlaneError('history_unavailable', 'Request history is unavailable'))
    await model.loadErrors()
    expect(model.snapshot()).toMatchObject({ errorsPhase: 'error', errorsMessage: 'Request history is unavailable', recentErrors: [] })
    await model.loadErrors()
    expect(model.snapshot()).toMatchObject({ errorsPhase: 'ready', errorsMessage: '' })
    expect(model.snapshot().recentErrors).toHaveLength(1)
    expect(fake.port.recentErrors).toHaveBeenLastCalledWith(50)
  })

  it('refreshes loaded recent errors after a change, so coverage stays current', async () => {
    const fake = fakePort()
    const model = new RulesModel(fake.port)
    await model.load()
    await model.loadErrors()
    fake.port.recentErrors.mockClear()
    await model.saveRetryRule(retry('r2'))
    expect(fake.port.recentErrors).toHaveBeenCalledTimes(1)
  })

  it('passes a test straight through with its signal', async () => {
    const fake = fakePort()
    const model = new RulesModel(fake.port)
    const controller = new AbortController()
    await expect(model.test({ model: 'm', path: '/v1/chat/completions' }, controller.signal)).resolves.toEqual({ requestRules: ['a'], effects: {} })
    expect(fake.port.test).toHaveBeenCalledWith({ model: 'm', path: '/v1/chat/completions' }, controller.signal)
  })
})
