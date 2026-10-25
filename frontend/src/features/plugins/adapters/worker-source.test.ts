import { describe, expect, it } from 'vitest'
import type { BootContext } from '../application/ports'
import { workerSource } from './worker-source'

const BOOT: BootContext = {
  plugin: { id: 'acme.tools', name: 'Acme tools', version: '1.0.0', granted: ['net:fetch', 'bus', 'relay:content', 'ui:dialogs'] },
  app: { version: '1.0.66', capabilities: [] },
  settings: { limit: 5 },
  secrets: ['token'],
}

type Posted = Record<string, unknown>
const tick = () => new Promise((resolve) => setTimeout(resolve, 0))

/** Runs the whole worker program against a stand-in global, the way a Worker
 * would, and records what it posts. */
function boot(script: string) {
  const posted: Posted[] = []
  const listeners = new Map<string, (event: unknown) => void>()
  const scope = {
    postMessage: (message: Posted) => { posted.push(message) },
    addEventListener: (type: string, listener: (event: unknown) => void) => { listeners.set(type, listener) },
  }
  new Function('self', workerSource(script, BOOT).text)(scope)
  const send = (data: unknown) => listeners.get('message')?.({ data })
  const calls = (op: string) => posted.filter((message) => message.type === 'call' && message.op === op)
  const answer = (op: string, value: unknown) => {
    const call = calls(op).at(-1)
    if (!call) throw new Error(`no ${op} call`)
    send({ type: 'result', id: call.id, ok: true, value })
  }
  return { posted, send, calls, answer, scope: scope as Record<string, unknown> }
}

describe('workerSource', () => {
  it('keeps the prelude free of characters that would break the string', () => {
    const { text } = workerSource('', BOOT)
    const prelude = text.slice(0, text.indexOf('__switchboardStart(async'))
    expect(prelude).not.toMatch(/[`\\]|\$\{/u)
  })

  it('starts, seals the network and reports ready', async () => {
    const worker = boot('')
    await tick()
    expect(worker.posted).toContainEqual({ type: 'ready' })
    expect(() => (worker.scope.fetch as () => void)()).toThrow(/not available to plugins/u)
  })

  it('wraps net.fetch answers in a response object', async () => {
    const worker = boot('self.result = switchboard.net.fetch("https://api.example.com/x", { method: "POST" })')
    await tick()
    expect(worker.calls('net.fetch')[0]?.args).toEqual({ method: 'POST', url: 'https://api.example.com/x' })
    worker.answer('net.fetch', { status: 201, headers: { 'content-type': 'application/json' }, body: '{"a":1}', redirected: false })
    const response = await (worker.scope.result as Promise<{ ok: boolean; status: number; text: string; json(): unknown; headers: unknown }>)
    expect(response).toMatchObject({ ok: true, status: 201, text: '{"a":1}', headers: { 'content-type': 'application/json' } })
    expect(response.json()).toEqual({ a: 1 })
    expect(Object.isFrozen(response)).toBe(true)
  })

  it('answers request hooks and fails open when a handler throws', async () => {
    const worker = boot(`
      self.off = switchboard.relay.onRequest({ models: ['gpt-*'], timeoutMs: 500 }, (request) => {
        if (request.model === 'bad') throw new Error('boom')
        return { action: 'rewrite', model: 'gpt-mini', tags: [request.endpoint] }
      })
      self.seen = []
      switchboard.relay.onResponse({ timeoutMs: 9 }, (request, response) => { self.seen.push([request.model, response.status]) })`)
    await tick()
    const [request, response] = worker.calls('relay.register').map((call) => call.args)
    expect(request).toEqual({ models: ['gpt-*'], timeoutMs: 500, hookId: 'h1', phase: 'request' })
    expect(response).toEqual({ hookId: 'h2', phase: 'response' })

    worker.send({ type: 'hook', kind: 'invoke', hookId: 'h1', callId: 'c1', request: { model: 'gpt-4', endpoint: 'chat' } })
    worker.send({ type: 'hook', kind: 'invoke', hookId: 'h1', callId: 'c2', request: { model: 'bad' } })
    worker.send({ type: 'hook', kind: 'invoke', hookId: 'h9', callId: 'c3', request: {} })
    worker.send({ type: 'hook', kind: 'observe', hookId: 'h2', request: { model: 'gpt-4' }, response: { status: 200 } })
    await tick()
    const results = worker.posted.filter((message) => message.type === 'hookResult')
    expect(results).toEqual([
      { type: 'hookResult', callId: 'c2', decision: { action: 'continue' } },
      { type: 'hookResult', callId: 'c3', decision: { action: 'continue' } },
      { type: 'hookResult', callId: 'c1', decision: { action: 'rewrite', model: 'gpt-mini', tags: ['chat'] } },
    ])
    expect(worker.posted.some((message) => message.type === 'fault')).toBe(true)
    expect(worker.scope.seen).toEqual([['gpt-4', 200]])

    for (const call of worker.calls('relay.register')) worker.send({ type: 'result', id: call.id, ok: true, value: null })
    const off = await (worker.scope.off as Promise<() => Promise<void>>)
    const removing = off()
    expect(off()).toBe(removing)
    worker.answer('relay.unregister', null)
    await removing
    worker.send({ type: 'hook', kind: 'invoke', hookId: 'h1', callId: 'c4', request: { model: 'gpt-4' } })
    expect(worker.posted.at(-1)).toEqual({ type: 'hookResult', callId: 'c4', decision: { action: 'continue' } })
  })

  it('follows a bus channel once and stops synchronously', async () => {
    const worker = boot(`
      self.got = []
      const a = switchboard.bus.subscribe('news', (payload, from) => { self.got.push(['a', payload, from]) })
      const b = switchboard.bus.subscribe('news', (payload) => { self.got.push(['b', payload]) })
      self.stop = () => { a(); b() }`)
    await tick()
    expect(worker.calls('bus.subscribe')).toHaveLength(1)
    worker.send({ type: 'bus', channel: 'news', payload: { n: 1 }, from: 'other.plugin' })
    expect(worker.scope.got).toEqual([['a', { n: 1 }, 'other.plugin'], ['b', { n: 1 }]])
    ;(worker.scope.stop as () => void)()
    expect(worker.calls('bus.unsubscribe')).toHaveLength(1)
    worker.send({ type: 'bus', channel: 'news', payload: 2, from: 'other.plugin' })
    expect(worker.scope.got).toHaveLength(2)
  })

  it('acknowledges every frame and settles dialog results', async () => {
    const worker = boot(`
      self.frames = []
      switchboard.ui.onFrame((dt, t) => { self.frames.push([dt, t]); throw new Error('slow') })
      self.dialog = switchboard.ui.dialog({ title: 'Hi', tree: { type: 'text', text: 'x' }, actions: [] })`)
    await tick()
    worker.send({ type: 'frame', dtMs: 16, tMs: 32 })
    expect(worker.scope.frames).toEqual([[16, 32]])
    expect(worker.posted.at(-1)).toEqual({ type: 'frameDone' })

    worker.answer('ui.dialog', { id: 'd1' })
    const handle = await (worker.scope.dialog as Promise<{ id: string; result: Promise<unknown>; update(tree: unknown): Promise<void> }>)
    expect(handle.id).toBe('d1')
    void handle.update({ type: 'text', text: 'y' })
    expect(worker.calls('ui.dialogUpdate')[0]?.args).toEqual({ id: 'd1', tree: { type: 'text', text: 'y' } })
    worker.send({ type: 'dialog', id: 'd1', result: { action: 'ok', form: { name: 'A' } } })
    await expect(handle.result).resolves.toEqual({ action: 'ok', form: { name: 'A' } })
  })

  it('reports secret keys and refreshes them with settings', async () => {
    const worker = boot('self.settings = switchboard.settings')
    await tick()
    const settings = worker.scope.settings as { secrets: readonly string[] }
    expect(settings.secrets).toEqual(['token'])
    worker.send({ type: 'settings', values: { limit: 5 }, secrets: ['token', 'other'] })
    expect(settings.secrets).toEqual(['token', 'other'])
    expect(Object.isFrozen(settings.secrets)).toBe(true)
  })

  it('sends collection, file and rule calls with the documented arguments', async () => {
    const worker = boot(`
      const notes = switchboard.db.collection('notes')
      notes.put('a', { t: 1 }); notes.list({ limit: 2, collection: 'other' }); notes.delete('a')
      switchboard.files.open(); switchboard.rules.recentErrors(5); switchboard.rules.list()
      switchboard.ui.sound.tone({ frequency: 440, durationMs: 50 })
      switchboard.ui.draw('widget:w', 'c', [{ op: 'clear' }])`)
    await tick()
    expect(worker.calls('db.put')[0]?.args).toEqual({ collection: 'notes', docId: 'a', doc: { t: 1 } })
    expect(worker.calls('db.list')[0]?.args).toEqual({ limit: 2, collection: 'notes' })
    expect(worker.calls('db.delete')[0]?.args).toEqual({ collection: 'notes', docId: 'a' })
    expect(worker.calls('files.open')[0]?.args).toEqual({})
    expect(worker.calls('control.call').map((call) => call.args)).toEqual([
      { method: 'rules.recentErrors', payload: { limit: 5 } },
      { method: 'rules.list', payload: {} },
    ])
    expect(worker.posted).toContainEqual({ type: 'tone', spec: { frequency: 440, durationMs: 50 } })
    expect(worker.posted).toContainEqual({ type: 'draw', surface: 'widget:w', canvas: 'c', ops: [{ op: 'clear' }] })
  })
})
