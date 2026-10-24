import { describe, expect, it } from 'vitest'
import { RUNTIME_LIMITS, commandSpec, notifySpec, pageSpec, parseWorkerMessage, statusSpec, storageKey } from './protocol'
import { normalizeDetail, normalizeList, normalizeSummary, normalizeValues, splitPageKey } from './plugin'

describe('parseWorkerMessage', () => {
  it('reads every message the worker may send', () => {
    expect(parseWorkerMessage({ type: 'ready' })).toEqual({ type: 'ready' })
    expect(parseWorkerMessage({ type: 'fail', message: 'boom' })).toEqual({ type: 'fail', message: 'boom' })
    expect(parseWorkerMessage({ type: 'log', level: 'warn', text: 'careful' })).toEqual({ type: 'log', level: 'warn', text: 'careful' })
    expect(parseWorkerMessage({ type: 'pong', seq: 3 })).toEqual({ type: 'pong', seq: 3 })
    expect(parseWorkerMessage({ type: 'call', id: 1, op: 'ui.render' })).toEqual({ type: 'call', id: 1, op: 'ui.render', args: {} })
  })

  it.each([
    null, 'ready', [], { type: 'eval' }, { type: 'fail' }, { type: 'log', level: 'trace', text: 'x' }, { type: 'pong', seq: 1.5 },
    { type: 'call', id: -1, op: 'notify' }, { type: 'call', id: 1, op: 'plugins.install' }, { type: 'call', id: 1, op: 'notify', args: [] },
  ])('refuses %j', (data) => {
    expect(parseWorkerMessage(data)).toBeNull()
  })

  it('clips long log text', () => {
    const message = parseWorkerMessage({ type: 'log', level: 'info', text: 'x'.repeat(RUNTIME_LIMITS.logText + 50) })
    expect(message).toMatchObject({ type: 'log' })
    expect((message as { text: string }).text).toHaveLength(RUNTIME_LIMITS.logText)
  })
})

describe('call arguments', () => {
  it('checks page ids, titles and icon names', () => {
    expect(pageSpec({ id: 'board', title: 'Board', icon: 'gauge' })).toEqual({ id: 'board', title: 'Board', icon: 'gauge' })
    expect(() => pageSpec({ id: 'Board', title: 'x' })).toThrow(expect.objectContaining({ code: 'invalid_argument' }))
    expect(() => pageSpec({ id: 'board', title: ' ' })).toThrow('title must be a non-empty string.')
    expect(() => pageSpec({ id: 'board', title: 'x', icon: '<svg>' })).toThrow('icon must be one of the documented icon names.')
  })

  it('checks commands', () => {
    expect(commandSpec({ id: 'go.north', title: 'Go north', keywords: ['switch'], shortcut: 'Alt+N' })).toEqual({ id: 'go.north', title: 'Go north', keywords: ['switch'], shortcut: 'Alt+N' })
    expect(() => commandSpec({ id: 'go', title: 'x'.repeat(RUNTIME_LIMITS.commandTitle + 1) })).toThrow(`title is longer than ${RUNTIME_LIMITS.commandTitle} characters.`)
    expect(() => commandSpec({ id: 'go', title: 'Go', keywords: Array(9).fill('a') })).toThrow('keywords must be up to 8 words of at most 32 characters.')
  })

  it('checks status entries and lets null clear one', () => {
    expect(statusSpec({ id: 'rpm', status: null })).toEqual({ id: 'rpm', status: null })
    expect(statusSpec({ id: 'rpm', status: { text: '12 rpm', tone: 'success' } })).toEqual({ id: 'rpm', status: { text: '12 rpm', tone: 'success' } })
    expect(() => statusSpec({ id: 'rpm', status: { text: 'x', tone: 'pink' } })).toThrow('status.tone must be one of neutral, info, success, warning, danger.')
  })

  it('checks notifications and storage keys', () => {
    expect(notifySpec({ title: 'Done' })).toEqual({ title: 'Done', body: '', tone: 'info' })
    expect(storageKey({ key: 'prefs:period' })).toBe('prefs:period')
    expect(() => storageKey({ key: '../x' })).toThrow(expect.objectContaining({ code: 'invalid_argument' }))
  })
})

describe('control-plane shapes', () => {
  const raw = { id: 'acme.tools', name: 'Acme', version: '1', permissions: ['ui:pages', 7], granted: ['ui:pages'], enabled: true, sizeBytes: 'big', hasScript: 1 }

  it('normalizes a summary and refuses one without an id', () => {
    expect(normalizeSummary(raw)).toMatchObject({ id: 'acme.tools', author: '', permissions: ['ui:pages'], enabled: true, sizeBytes: 0, hasScript: false })
    expect(() => normalizeSummary({ name: 'x' })).toThrow('The control plane returned a plugin without an id')
    expect(normalizeList({ plugins: [raw] })).toHaveLength(1)
    expect(normalizeList({})).toEqual([])
  })

  it('normalizes a detail, keeping only string tokens and a known scheme', () => {
    const detail = normalizeDetail({ plugin: raw, script: 'x', theme: { name: 'Ink', scheme: 'neon', tokens: { '--accent': 'red', '--n': 5 } } })
    expect(detail).toMatchObject({ script: 'x', css: '', theme: { name: 'Ink', scheme: 'dark', tokens: { '--accent': 'red' } } })
    expect(normalizeDetail({ plugin: raw }).theme).toBeNull()
  })

  it('keeps only string storage values', () => {
    expect(normalizeValues({ values: { a: '1', b: 2 } })).toEqual({ a: '1' })
  })

  it('splits page keys only at a single slash', () => {
    expect(splitPageKey('acme.tools/board')).toEqual({ pluginId: 'acme.tools', pageId: 'board' })
    for (const key of ['board', '/board', 'acme/', 'a/b/c']) expect(splitPageKey(key)).toBeNull()
  })
})
