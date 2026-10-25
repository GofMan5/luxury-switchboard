import { describe, expect, it, vi } from 'vitest'
import type { ControlPlaneSession } from '../../../platform/stdio/session'
import type { RequestRule, RetryRule } from '../domain/rules'
import { StdioRulesPort } from './stdio-rules-port'

const requestRule: RequestRule = { id: 'a', name: 'A', enabled: true, match: { models: [], endpoints: [] }, effects: { tags: ['x'] } }
const retryRule: RetryRule = { id: 'r', name: 'R', enabled: true, match: { statuses: [503], contains: [], errorCodes: [], providers: [], models: [] }, action: 'fail', maxAttempts: 1, delayMs: 0 }

function session(answers: Record<string, unknown>) {
  const listeners = new Map<string, (frame: unknown) => void>()
  const call = vi.fn(async (method: string, _payload?: unknown) => answers[method])
  const fake = {
    call,
    subscribe: vi.fn((topic: string, listener: (frame: unknown) => void) => { listeners.set(topic, listener); return () => listeners.delete(topic) }),
  } as unknown as ControlPlaneSession
  return { session: fake, call, listeners }
}

describe('StdioRulesPort', () => {
  it('sends each command with the documented payload', async () => {
    const fake = session({
      'rules.list': { requestRules: [requestRule], retryRules: [retryRule] },
      'rules.saveRequestRule': requestRule,
      'rules.deleteRequestRule': { deleted: true },
      'rules.reorderRequestRules': { requestRules: [requestRule] },
      'rules.saveRetryRule': retryRule,
      'rules.deleteRetryRule': { deleted: true },
      'rules.reorderRetryRules': { retryRules: [retryRule] },
      'rules.stats': { rules: { a: { hits: 1, windowRequests: 0, windowTokens: 0 } } },
      'rules.test': { requestRules: ['a'], effects: {}, retryRule: 'r' },
      'rules.recentErrors': { errors: [] },
    })
    const port = new StdioRulesPort(fake.session)
    expect(await port.list()).toEqual({ requestRules: [requestRule], retryRules: [retryRule] })
    expect(await port.saveRequestRule(requestRule)).toEqual(requestRule)
    await port.deleteRequestRule('a')
    expect(await port.reorderRequestRules(['a'])).toEqual([requestRule])
    expect(await port.saveRetryRule(retryRule)).toEqual(retryRule)
    await port.deleteRetryRule('r')
    expect(await port.reorderRetryRules(['r'])).toEqual([retryRule])
    expect((await port.stats()).a?.hits).toBe(1)
    expect(await port.test({ model: 'm', path: '/v1/x', status: 503 })).toEqual({ requestRules: ['a'], effects: {}, retryRule: 'r' })
    expect(await port.recentErrors(50)).toEqual([])

    expect(fake.call.mock.calls.map(([method, payload]) => [method, payload])).toEqual([
      ['rules.list', {}],
      ['rules.saveRequestRule', { rule: requestRule }],
      ['rules.deleteRequestRule', { id: 'a' }],
      ['rules.reorderRequestRules', { ids: ['a'] }],
      ['rules.saveRetryRule', { rule: retryRule }],
      ['rules.deleteRetryRule', { id: 'r' }],
      ['rules.reorderRetryRules', { ids: ['r'] }],
      ['rules.stats', {}],
      ['rules.test', { model: 'm', path: '/v1/x', status: 503 }],
      ['rules.recentErrors', { limit: 50 }],
    ])
    // The operator never names an actor: the control plane treats that as theirs.
    expect(JSON.stringify(fake.call.mock.calls)).not.toContain('actor')
  })

  it('reads missing lists as empty', async () => {
    const port = new StdioRulesPort(session({ 'rules.list': {}, 'rules.stats': {}, 'rules.recentErrors': {}, 'rules.reorderRetryRules': {} }).session)
    expect(await port.list()).toEqual({ requestRules: [], retryRules: [] })
    expect(await port.stats()).toEqual({})
    expect(await port.recentErrors(1)).toEqual([])
    expect(await port.reorderRetryRules([])).toEqual([])
  })

  it('follows rules.changed', () => {
    const fake = session({})
    const listener = vi.fn()
    const stop = new StdioRulesPort(fake.session).subscribe(listener)
    fake.listeners.get('rules.changed')?.({ v: 1, type: 'event', topic: 'rules.changed', seq: 1, payload: {} })
    expect(listener).toHaveBeenCalledTimes(1)
    stop()
    expect(fake.listeners.has('rules.changed')).toBe(false)
  })
})
