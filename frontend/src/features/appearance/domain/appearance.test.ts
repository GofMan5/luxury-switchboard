import { describe, expect, it } from 'vitest'
import { COLOR_TOKENS, TOKEN_LIMIT, tokenProblem } from '../../../shared/contracts/tokens'
import {
  APPEARANCE_DEFAULTS,
  appearanceProblem,
  computeTokens,
  customCssProblem,
  normalizeAppearance,
  resolveTheme,
  themeChoices,
  themeControls,
  tokensStylesheet,
  UI_FONT_STACKS,
} from './appearance'
import { parseAppearanceFile, serializeAppearance } from './appearance-file'
import { BUILTIN_THEMES, builtinTheme } from './themes'

describe('builtin themes', () => {
  it('define every public colour token, so no theme leaves a hole for the defaults to show through', () => {
    for (const theme of BUILTIN_THEMES) {
      for (const token of COLOR_TOKENS) expect(theme.tokens[token.name], `${theme.id} ${token.name}`).toBeTruthy()
    }
  })

  it('ships several dark themes and at least one light one', () => {
    expect(BUILTIN_THEMES.filter((theme) => theme.scheme === 'dark').length).toBeGreaterThanOrEqual(3)
    expect(BUILTIN_THEMES.some((theme) => theme.scheme === 'light')).toBe(true)
  })
})

describe('normalizeAppearance', () => {
  it('fills a partial or older record with defaults instead of refusing it', () => {
    expect(normalizeAppearance({ theme: 'ink' })).toEqual({ ...APPEARANCE_DEFAULTS, theme: 'ink' })
    expect(normalizeAppearance(null)).toEqual(APPEARANCE_DEFAULTS)
  })

  it('drops unknown choices, bad accents and unsafe tokens field by field', () => {
    const appearance = normalizeAppearance({ density: 'huge', textSize: 40, accent: 'red', radius: 'round', tokens: { '--accent': '#00ff00', '--x': 'url(http://evil)' } })
    expect(appearance.density).toBe('comfortable')
    expect(appearance.textSize).toBe(13)
    expect(appearance.accent).toBe('')
    expect(appearance.radius).toBe('round')
    expect(appearance.tokens).toEqual({ '--accent': '#00ff00' })
  })

  it('lower-cases the accent so equal colours compare equal', () => {
    expect(normalizeAppearance({ accent: '#FF8A3D' }).accent).toBe('#ff8a3d')
  })
})

describe('appearanceProblem', () => {
  it('accepts the defaults', () => {
    expect(appearanceProblem(APPEARANCE_DEFAULTS)).toBe('')
  })

  it('refuses token values that could escape their declaration or load a remote resource', () => {
    expect(appearanceProblem({ ...APPEARANCE_DEFAULTS, tokens: { '--accent': 'red; } body { display: none' } })).toMatch(/cannot carry/u)
    expect(appearanceProblem({ ...APPEARANCE_DEFAULTS, tokens: { '--bg-canvas': 'url(https://example.test/x.png)' } })).toMatch(/cannot carry/u)
    expect(appearanceProblem({ ...APPEARANCE_DEFAULTS, tokens: { NotAToken: '#fff' } })).toMatch(/not a valid custom property/u)
  })

  it('caps the number of overrides', () => {
    const tokens = Object.fromEntries(Array.from({ length: TOKEN_LIMIT + 1 }, (_, index) => [`--t${index}`, '1px']))
    expect(appearanceProblem({ ...APPEARANCE_DEFAULTS, tokens })).toMatch(/At most/u)
  })

  it('refuses custom CSS that imports, closes its style element or is too large', () => {
    expect(customCssProblem('@import url(x.css);')).toMatch(/@import/u)
    expect(customCssProblem('</style><script>')).toMatch(/close/u)
    expect(customCssProblem('a'.repeat(33 * 1024))).toMatch(/larger/u)
    expect(customCssProblem('.page-header h1 { letter-spacing: 0; }')).toBe('')
  })
})

describe('tokenProblem', () => {
  it('needs a value and a bounded one', () => {
    expect(tokenProblem('--accent', '')).toMatch(/needs a value/u)
    expect(tokenProblem('--accent', 'x'.repeat(121))).toMatch(/longer/u)
  })

  it('refuses control characters, as the control plane does', () => {
    expect(tokenProblem('--accent', '#fff\tred')).toMatch(/cannot carry/u)
    expect(tokenProblem('--accent', '#fff\u007f')).toMatch(/cannot carry/u)
    expect(tokenProblem('--shadow-float', '0 8px 24px color-mix(in srgb, #000 40%, transparent)')).toBe('')
  })
})

describe('resolveTheme', () => {
  const plugin = { id: 'plugin:dusk', name: 'Dusk', scheme: 'dark' as const, tokens: { '--accent': '#9b8cf5', '--bg-canvas': 'url(bad)' } }

  it('follows the system scheme for "system"', () => {
    expect(resolveTheme('system', [], true).scheme).toBe('light')
    expect(resolveTheme('system', [], false).scheme).toBe('dark')
  })

  it('layers a contributed theme over the base of its scheme and filters its tokens again', () => {
    const theme = resolveTheme('plugin:dusk', [plugin], false)
    expect(theme.tokens['--accent']).toBe('#9b8cf5')
    expect(theme.tokens['--bg-canvas']).toBe(builtinTheme('graphite')?.tokens['--bg-canvas'])
  })

  it('falls back to the default when nobody provides the theme any more', () => {
    expect(resolveTheme('plugin:gone', [], false).id).toBe('graphite')
  })

  it('lists builtins first, then contributions, with a swatch for each', () => {
    const choices = themeChoices([plugin], BUILTIN_THEMES)
    expect(choices.at(-1)).toMatchObject({ id: 'plugin:dusk', builtin: false })
    expect(choices.every((choice) => choice.swatch.length === 4)).toBe(true)
  })
})

describe('computeTokens', () => {
  const graphite = builtinTheme('graphite')!

  it('derives the accent family and flips the label colour with the accent lightness', () => {
    expect(computeTokens({ ...APPEARANCE_DEFAULTS, accent: '#d7dae0' }, graphite)['--text-on-action']).toBe('#121212')
    expect(computeTokens({ ...APPEARANCE_DEFAULTS, accent: '#1f3fae' }, graphite)['--text-on-action']).toBe('#ffffff')
    expect(computeTokens({ ...APPEARANCE_DEFAULTS, accent: '#5ea3f7' }, graphite)['--focus-ring']).toContain('#5ea3f7')
  })

  it('scales the type ramp from the base size and the density from its preset', () => {
    const tokens = computeTokens({ ...APPEARANCE_DEFAULTS, textSize: 14, density: 'compact' }, graphite)
    expect(tokens['--fs-base']).toBe('14px')
    expect(tokens['--fs-xs']).toBe('12px')
    expect(tokens['--row-height']).toBe('28px')
  })

  it('applies user overrides last, and leaves them out when asked (safe mode)', () => {
    const appearance = { ...APPEARANCE_DEFAULTS, tokens: { '--row-height': '26px' } }
    expect(computeTokens(appearance, graphite)['--row-height']).toBe('26px')
    expect(computeTokens(appearance, graphite, false)['--row-height']).toBe('34px')
  })

  describe('with a contributed theme', () => {
    const contribution = {
      id: 'plugin:slab',
      name: 'Slab',
      scheme: 'dark' as const,
      tokens: {
        '--accent': '#9b8cf5',
        '--radius-control': '0px',
        '--radius-panel': '0px',
        '--font-ui': 'Georgia, serif',
        '--fs-base': '15px',
        '--nav-item': '40px',
        '--focus-ring': '#ffffff',
        '--accent-line': '#9b8cf5',
      },
    }
    const slab = resolveTheme('plugin:slab', [contribution], false)

    it('keeps the control-owned tokens the theme set while each control is at its default', () => {
      const tokens = computeTokens(APPEARANCE_DEFAULTS, slab)
      expect(tokens['--radius-control']).toBe('0px')
      expect(tokens['--radius-panel']).toBe('0px')
      // What the theme left out still comes from the control.
      expect(tokens['--radius-modal']).toBe('12px')
      expect(tokens['--font-ui']).toBe('Georgia, serif')
      expect(tokens['--fs-base']).toBe('15px')
      expect(tokens['--fs-sm']).toBe('12px')
      expect(tokens['--nav-item']).toBe('40px')
      expect(tokens['--row-height']).toBe('34px')
    })

    it('hands the tokens back to a control the user moved off its default', () => {
      const tokens = computeTokens({ ...APPEARANCE_DEFAULTS, radius: 'round', uiFont: 'system', textSize: 12, density: 'compact' }, slab)
      expect(tokens['--radius-control']).toBe('9px')
      expect(tokens['--font-ui']).toBe(UI_FONT_STACKS.system)
      expect(tokens['--fs-base']).toBe('12px')
      expect(tokens['--nav-item']).toBe('28px')
    })

    it('keeps the accent shades it set until the user picks an accent', () => {
      expect(computeTokens(APPEARANCE_DEFAULTS, slab)['--focus-ring']).toBe('#ffffff')
      expect(computeTokens(APPEARANCE_DEFAULTS, slab)['--accent-line']).toBe('#9b8cf5')
      // Shades it did not set derive from its accent as usual.
      expect(computeTokens(APPEARANCE_DEFAULTS, slab)['--action-primary']).toBe('#9b8cf5')
      const picked = computeTokens({ ...APPEARANCE_DEFAULTS, accent: '#5ea3f7' }, slab)
      expect(picked['--focus-ring']).toContain('#5ea3f7')
      expect(picked['--accent-line']).toContain('#5ea3f7')
    })

    it('still lets the user override any of them by name', () => {
      expect(computeTokens({ ...APPEARANCE_DEFAULTS, tokens: { '--radius-control': '4px' } }, slab)['--radius-control']).toBe('4px')
    })

    it('names the controls the theme sets, and none for a builtin', () => {
      expect(themeControls(slab)).toEqual(['accent', 'density', 'textSize', 'uiFont', 'radius'])
      expect(themeControls(graphite)).toEqual([])
      expect(themeControls(resolveTheme('plugin:dusk', [{ id: 'plugin:dusk', name: 'Dusk', scheme: 'dark', tokens: { '--accent': '#9b8cf5' } }], false))).toEqual([])
    })

    it('does not let a token the safety filter refused hold a control', () => {
      const sneaky = resolveTheme('plugin:x', [{ id: 'plugin:x', name: 'X', scheme: 'dark', tokens: { '--radius-panel': 'url(x)' } }], false)
      expect(computeTokens(APPEARANCE_DEFAULTS, sneaky)['--radius-panel']).toBe('8px')
      expect(themeControls(sneaky)).toEqual([])
    })
  })

  it('writes one doubled-specificity root rule carrying the scheme', () => {
    expect(tokensStylesheet({ '--accent': '#fff' }, 'light')).toBe(':root:root {\n  color-scheme: light;\n  --accent: #fff;\n}\n')
  })
})

describe('appearance file', () => {
  it('round-trips a record', () => {
    const appearance = { ...APPEARANCE_DEFAULTS, theme: 'paper', accent: '#45c1b6', tokens: { '--row-height': '30px' }, customCss: 'h1 { margin: 0; }' }
    expect(parseAppearanceFile(serializeAppearance(appearance))).toEqual({ ok: true, appearance })
  })

  it('refuses other JSON, other versions and invalid records instead of half-applying them', () => {
    expect(parseAppearanceFile('{nope')).toMatchObject({ ok: false, error: expect.stringMatching(/not valid JSON/u) })
    expect(parseAppearanceFile(JSON.stringify({ format: 'something-else', version: 1, appearance: {} }))).toMatchObject({ ok: false })
    expect(parseAppearanceFile(JSON.stringify({ format: 'luxury-switchboard.appearance', version: 9, appearance: {} }))).toMatchObject({ ok: false, error: expect.stringMatching(/version 9/u) })
    const unsafe = JSON.stringify({ format: 'luxury-switchboard.appearance', version: 1, appearance: { tokens: { '--accent': 'url(x)' } } })
    expect(parseAppearanceFile(unsafe)).toMatchObject({ ok: false, error: expect.stringMatching(/cannot carry/u) })
  })
})
