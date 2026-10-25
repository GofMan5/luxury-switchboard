// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest'
import type { CommandContribution } from '../shared/contracts/extensions'
import { boundCommand, shortcutBindings } from './plugin-hotkeys'

afterEach(() => { document.body.replaceChildren() })

const command = (id: string, shortcut?: string, bind = true): CommandContribution => ({ id, title: id, group: 'Fixture', shortcut, bind, run: vi.fn() })

const press = (code: string, modifiers: { ctrl?: boolean; meta?: boolean; alt?: boolean; shift?: boolean } = {}, extra: Partial<{ repeat: boolean; defaultPrevented: boolean; target: EventTarget | null; altGraph: boolean }> = {}) => ({
  code,
  ctrlKey: modifiers.ctrl ?? false,
  metaKey: modifiers.meta ?? false,
  altKey: modifiers.alt ?? false,
  shiftKey: modifiers.shift ?? false,
  repeat: extra.repeat ?? false,
  defaultPrevented: extra.defaultPrevented ?? false,
  target: extra.target ?? null,
  getModifierState: (key: string) => key === 'AltGraph' && Boolean(extra.altGraph),
})

describe('shortcutBindings', () => {
  it('binds only commands that ask for it with a shortcut that parses', () => {
    const bindings = shortcutBindings([
      command('bound', 'ctrl+shift+l'),
      command('hint', 'Ctrl+Shift+H', false),
      command('none'),
      command('invalid', 'Ctrl+L'),
      command('reserved', 'Alt+1'),
    ])
    expect(bindings.map((binding) => [binding.command.id, binding.shortcut.text])).toEqual([['bound', 'Ctrl+Shift+L']])
  })

  it('gives a combination to the first command that claims it', () => {
    const bindings = shortcutBindings([command('first', 'Alt+P'), command('second', 'alt+p'), command('third', 'Alt+Q')])
    expect(bindings.map((binding) => binding.command.id)).toEqual(['first', 'third'])
  })
})

describe('boundCommand', () => {
  const bindings = shortcutBindings([command('lock', 'Ctrl+Shift+L'), command('ping', 'Alt+P')])

  it('finds the command whose combination was pressed exactly', () => {
    expect(boundCommand(bindings, press('KeyL', { ctrl: true, shift: true }))?.id).toBe('lock')
    expect(boundCommand(bindings, press('KeyL', { meta: true, shift: true }))?.id).toBe('lock')
    expect(boundCommand(bindings, press('KeyP', { alt: true }))?.id).toBe('ping')
    expect(boundCommand(bindings, press('KeyL', { ctrl: true }))).toBeUndefined()
    expect(boundCommand(bindings, press('KeyP', { alt: true, shift: true }))).toBeUndefined()
    expect(boundCommand(bindings, press('KeyP'))).toBeUndefined()
  })

  it('ignores held keys, handled keys and AltGr typing', () => {
    expect(boundCommand(bindings, press('KeyP', { alt: true }, { repeat: true }))).toBeUndefined()
    expect(boundCommand(bindings, press('KeyP', { alt: true }, { defaultPrevented: true }))).toBeUndefined()
    expect(boundCommand(bindings, press('KeyL', { ctrl: true, shift: true }, { altGraph: true }))).toBeUndefined()
  })

  it('fires nothing from inside a modal dialog', () => {
    const dialog = document.createElement('div')
    dialog.setAttribute('aria-modal', 'true')
    const input = document.createElement('input')
    dialog.append(input)
    document.body.append(dialog)
    expect(boundCommand(bindings, press('KeyP', { alt: true }, { target: input }))).toBeUndefined()
    expect(boundCommand(bindings, press('KeyP', { alt: true }, { target: document.body }))?.id).toBe('ping')
  })
})
