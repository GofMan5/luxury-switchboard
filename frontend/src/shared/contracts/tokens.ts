/**
 * The public design-token catalog. Every name here is a CSS custom property
 * the stylesheet reads, a theme sets, a user may override in Appearance →
 * Tokens and a plugin theme may ship. The names are an API: renaming one
 * breaks every theme and stylesheet written against it, so additions only.
 */
export type TokenGroup = 'Surface' | 'Border' | 'Text' | 'Accent' | 'Status' | 'Effect'

export interface TokenSpec {
  readonly name: string
  readonly group: TokenGroup
  readonly description: string
}

export const COLOR_TOKENS = [
  { name: '--bg-chrome', group: 'Surface', description: 'Sidebar and runtime bar — the frame around the work.' },
  { name: '--bg-canvas', group: 'Surface', description: 'The workspace behind every panel.' },
  { name: '--bg-surface', group: 'Surface', description: 'Panels, tables and dialogs.' },
  { name: '--bg-elevated', group: 'Surface', description: 'Popovers, menus and toasts floating above the surface.' },
  { name: '--bg-raised', group: 'Surface', description: 'Inset blocks inside a panel: code, previews, wells.' },
  { name: '--bg-hover', group: 'Surface', description: 'Row and control hover.' },
  { name: '--bg-selected', group: 'Surface', description: 'Selected row, pressed segment, active choice.' },
  { name: '--bg-nav-selected', group: 'Surface', description: 'The current sidebar entry.' },
  { name: '--bg-field', group: 'Surface', description: 'Inputs, selects and text areas.' },
  { name: '--border-subtle', group: 'Border', description: 'Hairlines between rows and around panels.' },
  { name: '--border-strong', group: 'Border', description: 'Secondary buttons and separators that must read.' },
  { name: '--border-field', group: 'Border', description: 'Input outlines at rest.' },
  { name: '--text-primary', group: 'Text', description: 'Headings, values, the thing you came to read.' },
  { name: '--text-secondary', group: 'Text', description: 'Body copy and table cells.' },
  { name: '--text-muted', group: 'Text', description: 'Labels, captions, column headers (≥ 4.5:1 on canvas).' },
  { name: '--text-disabled', group: 'Text', description: 'Unavailable controls.' },
  { name: '--text-danger', group: 'Text', description: 'Error copy on a neutral surface.' },
  { name: '--accent', group: 'Accent', description: 'The one signal colour: primary action, focus, selection marker, live state.' },
  { name: '--status-info', group: 'Status', description: 'In-flight work.' },
  { name: '--status-success', group: 'Status', description: 'Healthy, completed, live.' },
  { name: '--status-warning', group: 'Status', description: 'Retrying, degraded, needs a look.' },
  { name: '--status-danger', group: 'Status', description: 'Failed, blocked, destructive.' },
  { name: '--status-neutral', group: 'Status', description: 'Stopped, cancelled, unknown.' },
  { name: '--shadow-float', group: 'Effect', description: 'Dialogs, popovers, toasts — offset shadow, never a halo.' },
  { name: '--scrim', group: 'Effect', description: 'The dimmer behind a modal dialog.' },
] as const satisfies readonly TokenSpec[]

export type ColorToken = (typeof COLOR_TOKENS)[number]['name']

/** Tokens the appearance controls compute (density, radius, type, fonts and
 * the accent's derived shades). They are still overridable by name. */
export const DERIVED_TOKENS = [
  { name: '--action-primary', group: 'Accent', description: 'Primary button fill (defaults to the accent).' },
  { name: '--action-hover', group: 'Accent', description: 'Primary button hover.' },
  { name: '--action-pressed', group: 'Accent', description: 'Primary button pressed.' },
  { name: '--text-on-action', group: 'Accent', description: 'Text on the primary fill; flips with the accent’s lightness.' },
  { name: '--accent-soft', group: 'Accent', description: 'Accent wash for selected rows and active markers.' },
  { name: '--focus-ring', group: 'Accent', description: 'Keyboard focus outline.' },
  { name: '--selection', group: 'Accent', description: 'Text selection background.' },
  { name: '--font-ui', group: 'Text', description: 'Interface typeface stack.' },
  { name: '--font-mono', group: 'Text', description: 'Data, code and measurement typeface stack.' },
  { name: '--fs-xs', group: 'Text', description: 'Badges, captions.' },
  { name: '--fs-sm', group: 'Text', description: 'Labels, column headers, secondary copy.' },
  { name: '--fs-base', group: 'Text', description: 'Body and table cells.' },
  { name: '--fs-md', group: 'Text', description: 'Panel titles, emphasized values.' },
  { name: '--fs-lg', group: 'Text', description: 'Dialog titles.' },
  { name: '--fs-xl', group: 'Text', description: 'Page titles.' },
  { name: '--fs-2xl', group: 'Text', description: 'Readout values.' },
  { name: '--row-height', group: 'Effect', description: 'Table row and list item height.' },
  { name: '--control-height', group: 'Effect', description: 'Buttons, inputs, selects.' },
  { name: '--control-height-sm', group: 'Effect', description: 'Inline row actions, segmented items.' },
  { name: '--gutter', group: 'Effect', description: 'Workspace side padding.' },
  { name: '--gap', group: 'Effect', description: 'Space between panels.' },
  { name: '--pad', group: 'Effect', description: 'Panel inner padding.' },
  { name: '--radius-control', group: 'Effect', description: 'Buttons, inputs, chips.' },
  { name: '--radius-panel', group: 'Effect', description: 'Panels and popovers.' },
  { name: '--radius-modal', group: 'Effect', description: 'Dialogs.' },
] as const satisfies readonly TokenSpec[]

export const TOKEN_NAME_PATTERN = /^--[a-z][a-z0-9-]{0,47}$/u
// eslint-disable-next-line no-control-regex -- control characters are exactly what this refuses
const FORBIDDEN_VALUE = /[;{}<>\\`\u0000-\u001f\u007f]|url\(|expression\(|@import|javascript:/iu
export const TOKEN_LIMIT = 96
export const TOKEN_VALUE_BYTES = 120

/** The same rule the control plane enforces: a token value is a value, never
 * a way out of the declaration it sits in or a request for a remote resource. */
export function tokenProblem(name: string, value: string): string {
  if (!TOKEN_NAME_PATTERN.test(name)) return `${name || 'A token'} is not a valid custom property name`
  const bytes = new TextEncoder().encode(value).length
  if (bytes === 0) return `${name} needs a value`
  if (bytes > TOKEN_VALUE_BYTES) return `${name} is longer than ${TOKEN_VALUE_BYTES} bytes`
  if (FORBIDDEN_VALUE.test(value)) return `${name} contains characters or functions a token cannot carry`
  return ''
}

/** Keeps the tokens that pass {@link tokenProblem}; a theme contributed from
 * outside the slice is filtered again here rather than trusted. */
export function safeTokens(tokens: Readonly<Record<string, string>>): Record<string, string> {
  const safe: Record<string, string> = {}
  let count = 0
  for (const [name, value] of Object.entries(tokens)) {
    if (count >= TOKEN_LIMIT) break
    if (typeof value !== 'string' || tokenProblem(name, value)) continue
    safe[name] = value
    count += 1
  }
  return safe
}
