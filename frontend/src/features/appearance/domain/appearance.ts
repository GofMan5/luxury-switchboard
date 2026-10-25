import type { ThemeContribution } from '../../../shared/contracts/theme'
import { baseTheme, builtinTheme, DEFAULT_THEME_ID, SYSTEM_THEME_ID, type ThemeDefinition } from './themes'
import { safeTokens, tokenProblem, TOKEN_LIMIT } from '../../../shared/contracts/tokens'

export type Density = 'compact' | 'comfortable' | 'spacious'
export type TextSize = 12 | 13 | 14
export type UiFont = 'plex' | 'geist' | 'system'
export type MonoFont = 'plex' | 'jetbrains' | 'system'
export type RadiusScale = 'sharp' | 'default' | 'round'

/** The persisted personalization record, mirrored from the control plane's
 * appearance slice. Every field always has a value: an older or partial
 * record reads through {@link normalizeAppearance}. */
export interface Appearance {
  readonly theme: string
  /** Empty: the theme's own accent. Otherwise a #rrggbb override. */
  readonly accent: string
  readonly density: Density
  readonly textSize: TextSize
  readonly uiFont: UiFont
  readonly monoFont: MonoFont
  readonly radius: RadiusScale
  /** Custom-property overrides applied after the theme and the controls. */
  readonly tokens: Readonly<Record<string, string>>
  /** The user's own stylesheet, applied last. Safe mode skips it. */
  readonly customCss: string
}

export const APPEARANCE_DEFAULTS: Appearance = {
  theme: DEFAULT_THEME_ID,
  accent: '',
  density: 'comfortable',
  textSize: 13,
  uiFont: 'plex',
  monoFont: 'plex',
  radius: 'default',
  tokens: {},
  customCss: '',
}

export const CUSTOM_CSS_BYTES = 32 * 1024
const THEME_ID = /^[a-z0-9][a-z0-9:._-]{0,95}$/u
const HEX = /^#[0-9a-f]{6}$/iu
const DENSITIES: readonly Density[] = ['compact', 'comfortable', 'spacious']
const UI_FONTS: readonly UiFont[] = ['plex', 'geist', 'system']
const MONO_FONTS: readonly MonoFont[] = ['plex', 'jetbrains', 'system']
const RADII: readonly RadiusScale[] = ['sharp', 'default', 'round']
const TEXT_SIZES: readonly TextSize[] = [12, 13, 14]

function pick<T>(allowed: readonly T[], value: unknown, fallback: T): T {
  return allowed.includes(value as T) ? (value as T) : fallback
}

/** Reads a record from the wire or local cache; anything unknown or missing
 * takes the default rather than failing the whole record. */
export function normalizeAppearance(raw: unknown): Appearance {
  const value = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>
  const theme = typeof value.theme === 'string' && THEME_ID.test(value.theme) ? value.theme : APPEARANCE_DEFAULTS.theme
  const accent = typeof value.accent === 'string' && HEX.test(value.accent) ? value.accent.toLowerCase() : ''
  const tokens = value.tokens && typeof value.tokens === 'object' ? safeTokens(value.tokens as Record<string, string>) : {}
  const customCss = typeof value.customCss === 'string' ? value.customCss : ''
  return {
    theme,
    accent,
    density: pick(DENSITIES, value.density, APPEARANCE_DEFAULTS.density),
    textSize: pick(TEXT_SIZES, value.textSize, APPEARANCE_DEFAULTS.textSize),
    uiFont: pick(UI_FONTS, value.uiFont, APPEARANCE_DEFAULTS.uiFont),
    monoFont: pick(MONO_FONTS, value.monoFont, APPEARANCE_DEFAULTS.monoFont),
    radius: pick(RADII, value.radius, APPEARANCE_DEFAULTS.radius),
    tokens,
    customCss,
  }
}

/** The first reason this record would be refused, or '' when it is valid.
 * Mirrors the control plane's validation so the editor can say it first. */
export function appearanceProblem(appearance: Appearance): string {
  if (!THEME_ID.test(appearance.theme)) return 'The theme id is not valid'
  if (appearance.accent && !HEX.test(appearance.accent)) return 'The accent must be a #rrggbb colour'
  const entries = Object.entries(appearance.tokens)
  if (entries.length > TOKEN_LIMIT) return `At most ${TOKEN_LIMIT} token overrides`
  for (const [name, value] of entries) {
    const problem = tokenProblem(name, value)
    if (problem) return problem
  }
  return customCssProblem(appearance.customCss)
}

export function customCssProblem(css: string): string {
  if (new TextEncoder().encode(css).length > CUSTOM_CSS_BYTES) return `Custom CSS is larger than ${CUSTOM_CSS_BYTES / 1024} KiB`
  if (/@import/iu.test(css)) return 'Custom CSS cannot @import other stylesheets'
  if (/<\/style/iu.test(css)) return 'Custom CSS cannot close its own style element'
  return ''
}

export interface ThemeChoice {
  readonly id: string
  readonly name: string
  readonly scheme: 'dark' | 'light'
  readonly builtin: boolean
  readonly swatch: readonly string[]
}

/** Resolves the theme a record names against the builtins and the themes
 * other slices contributed. An id nobody provides any more falls back to the
 * default rather than to a half-styled window. */
export function resolveTheme(id: string, contributed: readonly ThemeContribution[], prefersLight: boolean): ThemeDefinition {
  if (id === SYSTEM_THEME_ID) return baseTheme(prefersLight ? 'light' : 'dark')
  const builtin = builtinTheme(id)
  if (builtin) return builtin
  const contribution = contributed.find((theme) => theme.id === id)
  if (!contribution) return builtinTheme(DEFAULT_THEME_ID) as ThemeDefinition
  const base = baseTheme(contribution.scheme)
  const own = safeTokens(contribution.tokens)
  return {
    id: contribution.id,
    name: contribution.name,
    scheme: contribution.scheme,
    builtin: false,
    tokens: { ...base.tokens, ...own },
    own,
  }
}

const DENSITY_TOKENS: Record<Density, Record<string, string>> = {
  compact: { '--row-height': '28px', '--control-height': '28px', '--control-height-sm': '22px', '--gutter': '16px', '--gap': '12px', '--pad': '12px', '--nav-item': '28px' },
  comfortable: { '--row-height': '34px', '--control-height': '30px', '--control-height-sm': '24px', '--gutter': '24px', '--gap': '16px', '--pad': '14px', '--nav-item': '30px' },
  spacious: { '--row-height': '40px', '--control-height': '34px', '--control-height-sm': '28px', '--gutter': '32px', '--gap': '20px', '--pad': '18px', '--nav-item': '34px' },
}

const RADIUS_TOKENS: Record<RadiusScale, Record<string, string>> = {
  sharp: { '--radius-control': '3px', '--radius-panel': '4px', '--radius-modal': '6px' },
  default: { '--radius-control': '6px', '--radius-panel': '8px', '--radius-modal': '12px' },
  round: { '--radius-control': '9px', '--radius-panel': '12px', '--radius-modal': '16px' },
}

export const UI_FONT_STACKS: Record<UiFont, string> = {
  plex: "'IBM Plex Sans Variable', 'IBM Plex Sans', 'Segoe UI Variable Text', 'Segoe UI', system-ui, sans-serif",
  geist: "'Geist Variable', 'Geist', 'Segoe UI Variable Text', 'Segoe UI', system-ui, sans-serif",
  system: "'Segoe UI Variable Text', 'Segoe UI', -apple-system, BlinkMacSystemFont, system-ui, sans-serif",
}

export const MONO_FONT_STACKS: Record<MonoFont, string> = {
  plex: "'IBM Plex Mono', 'Cascadia Mono', Consolas, ui-monospace, monospace",
  jetbrains: "'JetBrains Mono Variable', 'JetBrains Mono', 'Cascadia Mono', Consolas, ui-monospace, monospace",
  system: "'Cascadia Mono', Consolas, ui-monospace, SFMono-Regular, Menlo, monospace",
}

/** Relative luminance of a #rrggbb colour, for picking text on the accent. */
export function luminance(hex: string): number {
  const channel = (offset: number) => {
    const value = Number.parseInt(hex.slice(offset, offset + 2), 16) / 255
    return value <= 0.039_28 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel(1) + 0.7152 * channel(3) + 0.0722 * channel(5)
}

/** The appearance controls that compute tokens a theme may also set. */
export type ThemeControl = 'accent' | 'density' | 'textSize' | 'uiFont' | 'monoFont' | 'radius'

const FONT_SIZE_TOKENS = ['--fs-xs', '--fs-sm', '--fs-base', '--fs-md', '--fs-lg', '--fs-xl', '--fs-2xl'] as const
const ACCENT_DERIVED_TOKENS = ['--action-primary', '--action-hover', '--action-pressed', '--text-on-action', '--accent-soft', '--accent-line', '--focus-ring', '--selection'] as const

/** Which tokens each control owns. `--accent` itself is not listed: the
 * theme's accent is already the fallback whenever the user picked none. */
const CONTROL_TOKENS: Readonly<Record<ThemeControl, readonly string[]>> = {
  accent: ACCENT_DERIVED_TOKENS,
  density: Object.keys(DENSITY_TOKENS.comfortable),
  textSize: FONT_SIZE_TOKENS,
  uiFont: ['--font-ui'],
  monoFont: ['--font-mono'],
  radius: Object.keys(RADIUS_TOKENS.default),
}

const CONTROLS = Object.keys(CONTROL_TOKENS) as ThemeControl[]

/** The controls whose tokens a contributed theme sets itself, in a stable
 * order. Empty for builtins, which are designed around the defaults. */
export function themeControls(theme: ThemeDefinition): ThemeControl[] {
  const own = theme.own
  if (!own) return []
  return CONTROLS.filter((control) => CONTROL_TOKENS[control].some((name) => own[name] !== undefined))
}

/** The control-owned tokens a contributed theme set and still holds: the user
 * left that control at its default (for the accent: picked none). Choosing
 * any other value hands the tokens back to the control. */
function heldThemeTokens(appearance: Appearance, theme: ThemeDefinition): Record<string, string> {
  const own = theme.own
  const held: Record<string, string> = {}
  if (!own) return held
  for (const control of CONTROLS) {
    if (appearance[control] !== APPEARANCE_DEFAULTS[control]) continue
    for (const name of CONTROL_TOKENS[control]) {
      if (own[name] !== undefined) held[name] = own[name]
    }
  }
  return held
}

/** Every custom property the document carries for this record, in the order
 * they win: theme, controls and derived accent shades, the control-owned
 * tokens a contributed theme set while their control is at its default, then
 * the user's own overrides. */
export function computeTokens(appearance: Appearance, theme: ThemeDefinition, includeOverrides = true): Record<string, string> {
  const tokens: Record<string, string> = { ...theme.tokens }
  const accent = appearance.accent || tokens['--accent'] || '#ff8a3d'
  const lightScheme = theme.scheme === 'light'
  const onAccent = HEX.test(accent) && luminance(accent) > 0.4 ? '#121212' : '#ffffff'
  const base = appearance.textSize
  Object.assign(tokens, DENSITY_TOKENS[appearance.density], RADIUS_TOKENS[appearance.radius], {
    '--accent': accent,
    '--action-primary': accent,
    '--action-hover': `color-mix(in oklab, ${accent} 86%, ${lightScheme ? '#000' : '#fff'})`,
    '--action-pressed': `color-mix(in oklab, ${accent} 82%, ${lightScheme ? '#fff' : '#000'})`,
    '--text-on-action': onAccent,
    '--accent-soft': `color-mix(in srgb, ${accent} ${lightScheme ? 12 : 14}%, transparent)`,
    '--accent-line': `color-mix(in srgb, ${accent} 45%, transparent)`,
    '--focus-ring': `color-mix(in srgb, ${accent} 80%, ${lightScheme ? '#000' : '#fff'})`,
    '--selection': `color-mix(in srgb, ${accent} ${lightScheme ? 24 : 32}%, transparent)`,
    '--font-ui': UI_FONT_STACKS[appearance.uiFont],
    '--font-mono': MONO_FONT_STACKS[appearance.monoFont],
    '--fs-xs': `${base - 2}px`,
    '--fs-sm': `${base - 1}px`,
    '--fs-base': `${base}px`,
    '--fs-md': `${base + 1}px`,
    '--fs-lg': `${base + 3}px`,
    '--fs-xl': `${base + 5}px`,
    '--fs-2xl': `${base + 7}px`,
  }, heldThemeTokens(appearance, theme))
  if (includeOverrides) Object.assign(tokens, safeTokens(appearance.tokens))
  return tokens
}

/** One rule on the root, doubled for specificity so it wins over the
 * stylesheet's defaults regardless of injection order. */
export function tokensStylesheet(tokens: Readonly<Record<string, string>>, scheme: 'dark' | 'light'): string {
  const declarations = Object.entries(tokens).map(([name, value]) => `  ${name}: ${value};`)
  return `:root:root {\n  color-scheme: ${scheme};\n${declarations.join('\n')}\n}\n`
}

export function themeChoices(contributed: readonly ThemeContribution[], builtins: readonly ThemeDefinition[]): ThemeChoice[] {
  const swatch = (tokens: Readonly<Record<string, string>>) => [tokens['--bg-canvas'], tokens['--bg-surface'], tokens['--text-primary'], tokens['--accent']].filter((value): value is string => Boolean(value))
  return [
    ...builtins.map((theme) => ({ id: theme.id, name: theme.name, scheme: theme.scheme, builtin: true, swatch: swatch(theme.tokens) })),
    ...contributed.map((theme) => {
      const resolved = resolveTheme(theme.id, contributed, false)
      return { id: theme.id, name: theme.name, scheme: theme.scheme, builtin: false, swatch: swatch(resolved.tokens) }
    }),
  ]
}
