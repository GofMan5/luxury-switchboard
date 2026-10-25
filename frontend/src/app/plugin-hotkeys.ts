import type { CommandContribution } from '../shared/contracts/extensions'
import { matchesShortcut, parseShortcut, type Shortcut } from '../shared/keyboard/shortcuts'

export interface ShortcutBinding {
  readonly shortcut: Shortcut
  readonly command: CommandContribution
}

/** The global keys owners asked the shell to bind. A combination binds at most
 * once — the first command that claims it keeps it — and a shortcut that does
 * not parse binds nothing, so it stays a display hint. */
export function shortcutBindings(commands: readonly CommandContribution[]): readonly ShortcutBinding[] {
  const taken = new Set<string>()
  const bindings: ShortcutBinding[] = []
  for (const command of commands) {
    if (!command.bind || !command.shortcut) continue
    const parsed = parseShortcut(command.shortcut)
    if (!parsed.ok || taken.has(parsed.shortcut.text)) continue
    taken.add(parsed.shortcut.text)
    bindings.push({ shortcut: parsed.shortcut, command })
  }
  return bindings
}

type HotkeyEvent = Pick<KeyboardEvent, 'ctrlKey' | 'metaKey' | 'altKey' | 'shiftKey' | 'code' | 'repeat' | 'defaultPrevented' | 'target'>
  & { readonly getModifierState?: (key: string) => boolean }

/** The bound command this key press runs, if any. A held key fires once, a
 * key something else already handled is left alone, and AltGr — which Windows
 * reports as Ctrl+Alt while it types `@`, `€` or `ą` — never triggers a binding.
 * Inside a modal dialog nothing fires: a plugin's key must not act behind a
 * confirmation, its own permission review included. */
export function boundCommand(bindings: readonly ShortcutBinding[], event: HotkeyEvent): CommandContribution | undefined {
  if (bindings.length === 0 || event.repeat || event.defaultPrevented) return undefined
  if (event.getModifierState?.('AltGraph')) return undefined
  if (isElement(event.target) && event.target.closest('[aria-modal="true"]')) return undefined
  return bindings.find((binding) => matchesShortcut(binding.shortcut, event))?.command
}

function isElement(target: EventTarget | null): target is Element {
  return typeof Element !== 'undefined' && target instanceof Element
}
