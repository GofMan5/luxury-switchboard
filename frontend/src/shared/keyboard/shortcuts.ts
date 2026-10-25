/**
 * Global key combinations an owner outside the shell (a plugin) may bind.
 *
 * A combination is written `Ctrl+Alt+Shift+Key` in any case and order; it is
 * kept in that canonical order. Keys are matched by physical position
 * (`KeyboardEvent.code`), so a binding works the same in every keyboard
 * layout. On macOS `Ctrl` also matches ⌘, as the shell's own keys do.
 *
 * Plain typing, editing keys and the shell's own combinations are never
 * available: a binding needs Alt, or Ctrl together with Shift or Alt.
 */
export interface Shortcut {
  /** Canonical text, such as `Ctrl+Shift+L`. */
  readonly text: string
  readonly ctrl: boolean
  readonly alt: boolean
  readonly shift: boolean
  /** `KeyboardEvent.code`: `KeyL`, `Digit4`, `F6`. */
  readonly code: string
}

export type ShortcutResult = { readonly ok: true; readonly shortcut: Shortcut } | { readonly ok: false; readonly problem: string }

export const SHORTCUT_FORMAT = 'Alt+Key, Ctrl+Shift+Key or Ctrl+Alt+Key, where Key is A-Z, 0-9 or F1-F12'

/** Combinations the shell, the safe-mode escape and the webview keep. */
const RESERVED = new Set([
  'Ctrl+Alt+Shift+S',
  'Alt+F4',
  'Ctrl+Shift+C', 'Ctrl+Shift+I', 'Ctrl+Shift+J', 'Ctrl+Shift+N', 'Ctrl+Shift+Q', 'Ctrl+Shift+R',
  'Ctrl+Shift+T', 'Ctrl+Shift+V', 'Ctrl+Shift+W', 'Ctrl+Shift+Z',
  ...Array.from({ length: 9 }, (_, index) => `Alt+${index + 1}`),
])

export function parseShortcut(value: string): ShortcutResult {
  const parts = value.split('+').map((part) => part.trim())
  if (value.length > 32 || parts.length < 2 || parts.some((part) => part === '')) return { ok: false, problem: `Write a shortcut as ${SHORTCUT_FORMAT}.` }
  const modifiers = new Set<string>()
  for (const part of parts.slice(0, -1)) {
    const modifier = part.toLowerCase()
    if (modifier !== 'ctrl' && modifier !== 'alt' && modifier !== 'shift') return { ok: false, problem: `"${part}" is not a modifier; use Ctrl, Alt or Shift.` }
    if (modifiers.has(modifier)) return { ok: false, problem: `"${part}" is listed twice.` }
    modifiers.add(modifier)
  }
  const key = parts.at(-1)?.toUpperCase() ?? ''
  const code = /^[A-Z]$/u.test(key) ? `Key${key}` : /^[0-9]$/u.test(key) ? `Digit${key}` : /^F(?:[1-9]|1[0-2])$/u.test(key) ? key : ''
  if (!code) return { ok: false, problem: `"${parts.at(-1)}" is not a key a shortcut can use; use A-Z, 0-9 or F1-F12.` }
  const ctrl = modifiers.has('ctrl')
  const alt = modifiers.has('alt')
  const shift = modifiers.has('shift')
  if (!alt && !(ctrl && shift)) return { ok: false, problem: `A shortcut needs Alt, or Ctrl with Shift, so typing and editing keys stay free: ${SHORTCUT_FORMAT}.` }
  const text = [ctrl ? 'Ctrl' : '', alt ? 'Alt' : '', shift ? 'Shift' : '', key].filter(Boolean).join('+')
  if (RESERVED.has(text)) return { ok: false, problem: `${text} is reserved by the Switchboard.` }
  return { ok: true, shortcut: { text, ctrl, alt, shift, code } }
}

/** Whether a key press is exactly this combination. */
export function matchesShortcut(shortcut: Shortcut, event: Pick<KeyboardEvent, 'ctrlKey' | 'metaKey' | 'altKey' | 'shiftKey' | 'code'>): boolean {
  return event.code === shortcut.code
    && (event.ctrlKey || event.metaKey) === shortcut.ctrl
    && event.altKey === shortcut.alt
    && event.shiftKey === shortcut.shift
}
