import { describe, expect, it } from 'vitest'
import { checkPackage, isHomepage, needsNewerApp } from './manifest'
import { CallError, badgeSpec, clipboardText, commandSpec, confirmSpec, storageBatch } from './protocol'
import { SETTING_LIMIT, acceptValue, checkSettings, isSettingStorageKey, settingStorageKey, settingValue, settingValues, valueProblem, type PluginSetting } from './settings'

function check(raw: unknown) {
  const problems: string[] = []
  return { settings: checkSettings(raw, problems), problems }
}

const NUMBER: PluginSetting = { key: 'limit', label: 'Limit', description: '', type: 'number', default: 5, placeholder: '', min: 1, max: 100 }
const TEXT: PluginSetting = { key: 'name', label: 'Name', description: '', type: 'text', default: '', placeholder: '', maxLength: 4 }
const SELECT: PluginSetting = { key: 'period', label: 'Period', description: '', type: 'select', default: '1h', options: [{ value: '1h', label: 'Hour' }, { value: '24h', label: 'Day' }] }
const TOGGLE: PluginSetting = { key: 'compact', label: 'Compact', description: '', type: 'toggle', default: false }

describe('checkSettings', () => {
  it('fills the defaults each type implies', () => {
    expect(check([
      { key: 'name', label: 'Name', type: 'text', default: 'x' },
      { key: 'limit', label: 'Limit', type: 'number', default: 5, min: 1 },
      { key: 'compact', label: 'Compact', type: 'toggle', default: true, description: 'Tighter rows.' },
      { key: 'period', label: 'Period', type: 'select', default: 'a', options: [{ value: 'a', label: 'A' }] },
    ])).toEqual({
      problems: [],
      settings: [
        { key: 'name', label: 'Name', description: '', type: 'text', default: 'x', placeholder: '', maxLength: 256 },
        { key: 'limit', label: 'Limit', description: '', type: 'number', default: 5, placeholder: '', min: 1 },
        { key: 'compact', label: 'Compact', description: 'Tighter rows.', type: 'toggle', default: true },
        { key: 'period', label: 'Period', description: '', type: 'select', default: 'a', options: [{ value: 'a', label: 'A' }] },
      ],
    })
  })

  it.each([
    [{ key: 'Bad', label: 'L', type: 'toggle', default: true }, '"settings[0].key" must start with a lowercase letter, then up to 31 letters, digits or underscores.'],
    [{ key: 'k', label: ' ', type: 'toggle', default: true }, '"settings[0].label" is required.'],
    [{ key: 'k', label: 'L', type: 'color', default: '#fff' }, '"settings[0].type" must be text, number, toggle, select or secret.'],
    [{ key: 'k', label: 'L', type: 'toggle', default: true, min: 1 }, '"settings[0].min" does not apply to a toggle setting.'],
    [{ key: 'k', label: 'L', type: 'toggle', default: true, secret: true }, 'Unknown field "settings[0].secret".'],
    [{ key: 'k', label: 'L', type: 'toggle' }, '"settings[0].default" is required.'],
    [{ key: 'k', label: 'L', type: 'number', default: 0, min: 5, max: 1 }, '"settings[0].min" is larger than "settings[0].max".'],
    [{ key: 'k', label: 'L', type: 'number', default: 0, min: 1 }, '"settings[0].default" must be a number of at least 1.'],
    [{ key: 'k', label: 'L', type: 'text', default: 'toolong', maxLength: 3 }, '"settings[0].default" must be text of at most 3 characters without control characters.'],
    [{ key: 'k', label: 'L', type: 'text', default: 'a\nb' }, '"settings[0].default" must be text of at most 256 characters without control characters.'],
    [{ key: 'k', label: 'L', type: 'text', default: '', maxLength: 1000 }, '"settings[0].maxLength" must be a whole number from 1 to 256.'],
    [{ key: 'k', label: 'L', type: 'select', default: 'a', options: [] }, '"settings[0].options" must list 1 to 32 choices.'],
    [{ key: 'k', label: 'L', type: 'select', default: 'a', options: [{ value: 'a', label: 'A' }, { value: 'a', label: 'B' }] }, '"settings[0].options[1].value" repeats "a".'],
    [{ key: 'k', label: 'L', type: 'select', default: 'z', options: [{ value: 'a', label: 'A' }] }, '"settings[0].default" must be one of the option values.'],
  ])('refuses %j', (entry, problem) => {
    expect(check([entry])).toEqual({ settings: null, problems: [problem] })
  })

  it('reports every broken entry, repeated keys and an over-long list', () => {
    const ok = { key: 'a', label: 'A', type: 'toggle', default: true }
    expect(check([ok, ok, { key: 'b' }]).problems).toEqual([
      '"settings[1].key" repeats "a".',
      '"settings[2].label" is required.',
    ])
    const many = Array.from({ length: SETTING_LIMIT + 1 }, (_, index) => ({ key: `k${index}`, label: 'L', type: 'toggle', default: true }))
    expect(check(many).problems).toEqual([`"settings" has more than ${SETTING_LIMIT} entries.`])
    expect(check({}).problems).toEqual(['"settings" must be a list.'])
  })

  it('counts characters, not UTF-16 units, as the control plane does', () => {
    expect(check([{ key: 'k', label: 'L', type: 'text', default: '😀😀', maxLength: 2 }]).problems).toEqual([])
  })
})

describe('setting values', () => {
  it('accepts only what the declaration allows', () => {
    expect(acceptValue(NUMBER, 7)).toBe(7)
    expect(acceptValue(NUMBER, 0)).toBeNull()
    expect(acceptValue(NUMBER, '7')).toBeNull()
    expect(acceptValue(NUMBER, Number.NaN)).toBeNull()
    expect(acceptValue(TEXT, 'abcd')).toBe('abcd')
    expect(acceptValue(TEXT, 'abcde')).toBeNull()
    expect(acceptValue(TEXT, 'a\tb')).toBeNull()
    expect(acceptValue(SELECT, '24h')).toBe('24h')
    expect(acceptValue(SELECT, '7d')).toBeNull()
    expect(acceptValue(TOGGLE, true)).toBe(true)
    expect(acceptValue(TOGGLE, 'true')).toBeNull()
  })

  it('explains a refused value in the operator’s words', () => {
    expect(valueProblem(NUMBER, 500)).toBe('Enter a number from 1 to 100.')
    expect(valueProblem(TEXT, 'abcde')).toBe('Use at most 4 characters, without line breaks.')
    expect(valueProblem(SELECT, 'x')).toBe('Choose one of the options.')
    expect(valueProblem(NUMBER, 50)).toBe('')
  })

  it('falls back to the default for a missing, corrupt or no-longer-valid value', () => {
    expect(settingValue(NUMBER, undefined)).toBe(5)
    expect(settingValue(NUMBER, '42')).toBe(42)
    expect(settingValue(NUMBER, '{')).toBe(5)
    expect(settingValue(NUMBER, '1000')).toBe(5)
    expect(settingValue(SELECT, '"gone"')).toBe('1h')
    expect(settingValues([NUMBER, TOGGLE], { 'setting:limit': '9', 'setting:compact': 'true', other: 'x' })).toEqual({ limit: 9, compact: true })
  })

  it('keeps setting keys apart from the plugin’s own storage', () => {
    expect(settingStorageKey('limit')).toBe('setting:limit')
    expect(isSettingStorageKey('setting:limit')).toBe(true)
    expect(isSettingStorageKey('settings')).toBe(false)
  })
})

describe('package metadata', () => {
  const base = { schema: 1, id: 'acme.tools', name: 'Acme', version: '1.0.0', permissions: [] }

  it('accepts an https homepage and a minimum app version', () => {
    const checked = checkPackage({ ...base, homepage: 'https://example.com/acme', minAppVersion: '1.0.65' })
    expect(checked.ok && checked.package).toMatchObject({ homepage: 'https://example.com/acme', minAppVersion: '1.0.65' })
  })

  it.each(['http://example.com', 'https://user@example.com', 'https://', 'javascript:alert(1)', 'https://exa mple.com'])('refuses the homepage %s', (homepage) => {
    expect(isHomepage(homepage)).toBe(false)
    expect(checkPackage({ ...base, homepage }).ok).toBe(false)
  })

  it('refuses settings without a script to read them', () => {
    const checked = checkPackage({ ...base, settings: [{ key: 'a', label: 'A', type: 'toggle', default: true }] })
    expect(checked.ok ? [] : checked.problems).toEqual(['"settings" need a "script" to read them.'])
  })

  it('compares app versions numerically and ignores an unknown running version', () => {
    expect(needsNewerApp('1.0.65', '1.0.64')).toBe(true)
    expect(needsNewerApp('1.0.65', '1.0.65')).toBe(false)
    expect(needsNewerApp('1.0.9', '1.0.10')).toBe(false)
    expect(needsNewerApp('2.0.0', '1.99.99')).toBe(true)
    expect(needsNewerApp('1.0.65', '')).toBe(false)
    expect(needsNewerApp(undefined, '1.0.0')).toBe(false)
  })
})

describe('new call arguments', () => {
  const code = (run: () => unknown) => {
    try { run() } catch (error) { return (error as CallError).code }
    return 'ok'
  }

  it('checks confirmations', () => {
    expect(confirmSpec({ title: 'Ban?' })).toEqual({ title: 'Ban?', body: '', confirmText: 'Continue', tone: 'neutral' })
    expect(confirmSpec({ title: 'Ban?', body: 'Now.', confirmText: 'Ban', tone: 'danger' })).toEqual({ title: 'Ban?', body: 'Now.', confirmText: 'Ban', tone: 'danger' })
    expect(code(() => confirmSpec({ title: 'Ban?', tone: 'loud' }))).toBe('invalid_argument')
    expect(code(() => confirmSpec({ title: 'Ban?', confirmText: 'x'.repeat(25) }))).toBe('invalid_argument')
  })

  it('checks badges', () => {
    expect(badgeSpec({ pageId: 'board', badge: { text: '12', tone: 'danger' } })).toEqual({ pageId: 'board', badge: { text: '12', tone: 'danger' } })
    expect(badgeSpec({ pageId: 'board' })).toEqual({ pageId: 'board', badge: null })
    expect(code(() => badgeSpec({ pageId: 'board', badge: { text: '123456789' } }))).toBe('invalid_argument')
    expect(code(() => badgeSpec({ pageId: 'board', badge: { text: 'a\nb' } }))).toBe('invalid_argument')
  })

  it('checks clipboard text', () => {
    expect(clipboardText({ text: 'sk-…' })).toBe('sk-…')
    expect(code(() => clipboardText({ text: 1 }))).toBe('invalid_argument')
    expect(code(() => clipboardText({ text: 'x'.repeat(32_769) }))).toBe('too_large')
  })

  it('checks storage batches', () => {
    expect(storageBatch({ values: { a: '1', b: null } })).toEqual({ a: '1', b: null })
    expect(code(() => storageBatch({ values: {} }))).toBe('invalid_argument')
    expect(code(() => storageBatch({ values: { a: 1 } }))).toBe('invalid_argument')
    expect(code(() => storageBatch({ values: { 'bad key': '1' } }))).toBe('invalid_argument')
    expect(code(() => storageBatch({ values: { a: 'x'.repeat(8193) } }))).toBe('too_large')
  })

  it('binds only a real shortcut', () => {
    expect(commandSpec({ id: 'open', title: 'Open', shortcut: 'ctrl+shift+b' })).toMatchObject({ shortcut: expect.any(String) })
    expect(code(() => commandSpec({ id: 'open', title: 'Open', shortcut: 'banana' }))).toBe('invalid_argument')
    expect(code(() => commandSpec({ id: 'open', title: 'Open', shortcut: 3 }))).toBe('invalid_argument')
  })
})
