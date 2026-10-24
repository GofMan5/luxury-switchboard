import type { StyleContribution, ThemeContribution } from '../../../shared/contracts/theme'
import { computeTokens, resolveTheme, tokensStylesheet, type Appearance } from '../domain/appearance'

const THEME_STYLE_ID = 'sb-theme'
const USER_STYLE_ID = 'sb-user-css'
const CONTRIBUTED_ATTR = 'data-sb-owner'
const LAST_THEME_KEY = 'switchboard.theme-css'

/** Paints the last resolved theme before React mounts, so a cold start never
 * flashes the default palette under a custom one. Only the token sheet is
 * replayed — user CSS and plugin styles wait for the live record. */
export function restoreLastTheme(): void {
  try {
    const css = window.localStorage.getItem(LAST_THEME_KEY)
    if (!css || !css.startsWith(':root:root {')) return
    write(styleElement(THEME_STYLE_ID), css)
    const scheme = /color-scheme: (dark|light);/u.exec(css)?.[1]
    if (scheme) document.documentElement.dataset.scheme = scheme
    const chrome = /--bg-chrome: (#[0-9a-f]{6});/iu.exec(css)?.[1]
    if (chrome) document.querySelector('meta[name="theme-color"]')?.setAttribute('content', chrome)
  } catch { /* storage is a courtesy */ }
}

export interface AppliedLayers {
  readonly appearance: Appearance
  readonly themes: readonly ThemeContribution[]
  readonly styles: readonly StyleContribution[]
  readonly prefersLight: boolean
  /** Safe mode: theme and controls only — no user CSS, token overrides,
   * contributed themes or contributed stylesheets. */
  readonly safe: boolean
}

function styleElement(id: string): HTMLStyleElement {
  const existing = document.getElementById(id)
  if (existing instanceof HTMLStyleElement) return existing
  const element = document.createElement('style')
  element.id = id
  document.head.append(element)
  return element
}

function write(element: HTMLStyleElement, css: string): void {
  if (element.textContent !== css) element.textContent = css
}

/**
 * Writes the appearance onto the document as three ordered layers: the
 * resolved theme with the controls' tokens, the stylesheets other slices
 * contributed (plugins), then the user's own CSS — last, because the person at
 * the keyboard outranks every package they installed.
 */
export function applyAppearance(layers: AppliedLayers): void {
  const { appearance, safe } = layers
  const theme = resolveTheme(appearance.theme, safe ? [] : layers.themes, layers.prefersLight)
  const tokens = computeTokens(appearance, theme, !safe)
  const root = document.documentElement
  root.dataset.theme = theme.id
  root.dataset.scheme = theme.scheme
  root.dataset.density = appearance.density
  const sheet = tokensStylesheet(tokens, theme.scheme)
  write(styleElement(THEME_STYLE_ID), sheet)
  try { window.localStorage.setItem(LAST_THEME_KEY, sheet) } catch { /* storage is a courtesy */ }

  const meta = document.querySelector('meta[name="theme-color"]')
  if (meta && tokens['--bg-chrome']) meta.setAttribute('content', tokens['--bg-chrome'])

  const wanted = safe ? [] : layers.styles
  const present = new Map<string, HTMLStyleElement>()
  for (const element of document.head.querySelectorAll<HTMLStyleElement>(`style[${CONTRIBUTED_ATTR}]`)) {
    present.set(element.getAttribute(CONTRIBUTED_ATTR) ?? '', element)
  }
  const user = styleElement(USER_STYLE_ID)
  for (const contribution of wanted) {
    let element = present.get(contribution.owner)
    present.delete(contribution.owner)
    if (!element) {
      element = document.createElement('style')
      element.setAttribute(CONTRIBUTED_ATTR, contribution.owner)
      document.head.insertBefore(element, user)
    }
    write(element, contribution.css)
  }
  for (const stale of present.values()) stale.remove()
  // The user's sheet stays the last element in the head so it wins ties.
  if (user.nextElementSibling) document.head.append(user)
  write(user, safe ? '' : appearance.customCss)
}
