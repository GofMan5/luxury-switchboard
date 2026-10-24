// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { stubLocalStorage } from '../../../test/memory-storage'
import { APPEARANCE_DEFAULTS } from '../domain/appearance'
import { applyAppearance, restoreLastTheme } from './apply-appearance'

const base = { themes: [], styles: [], prefersLight: false, safe: false }

beforeEach(() => { stubLocalStorage() })

afterEach(() => {
  document.head.innerHTML = ''
  for (const name of ['theme', 'scheme', 'density']) delete document.documentElement.dataset[name]
})

describe('applyAppearance', () => {
  it('marks the root and writes the token sheet', () => {
    applyAppearance({ ...base, appearance: { ...APPEARANCE_DEFAULTS, theme: 'paper', density: 'compact' } })
    const root = document.documentElement
    expect(root.dataset).toMatchObject({ theme: 'paper', scheme: 'light', density: 'compact' })
    expect(document.getElementById('sb-theme')?.textContent).toContain('color-scheme: light;')
  })

  it('orders contributed stylesheets before the user\'s own, and removes ones no longer contributed', () => {
    const appearance = { ...APPEARANCE_DEFAULTS, customCss: 'h1 { color: red; }' }
    applyAppearance({ ...base, appearance, styles: [{ owner: 'plugin:a', css: '.a{}' }, { owner: 'plugin:b', css: '.b{}' }] })
    const order = () => [...document.head.querySelectorAll('style')].map((element) => element.id || element.getAttribute('data-sb-owner'))
    expect(order()).toEqual(['sb-theme', 'plugin:a', 'plugin:b', 'sb-user-css'])
    applyAppearance({ ...base, appearance, styles: [{ owner: 'plugin:b', css: '.b{}' }] })
    expect(order()).toEqual(['sb-theme', 'plugin:b', 'sb-user-css'])
    expect(document.getElementById('sb-user-css')?.textContent).toBe('h1 { color: red; }')
  })

  it('safe mode drops user CSS, overrides, contributed themes and styles', () => {
    const appearance = { ...APPEARANCE_DEFAULTS, theme: 'plugin:dusk', tokens: { '--row-height': '20px' }, customCss: 'body { display: none; }' }
    applyAppearance({ ...base, safe: true, appearance, themes: [{ id: 'plugin:dusk', name: 'Dusk', scheme: 'dark', tokens: {} }], styles: [{ owner: 'plugin:a', css: '.a{}' }] })
    expect(document.documentElement.dataset.theme).toBe('graphite')
    expect(document.getElementById('sb-theme')?.textContent).toContain('--row-height: 34px;')
    expect(document.getElementById('sb-user-css')?.textContent).toBe('')
    expect(document.querySelector('style[data-sb-owner]')).toBeNull()
  })

  it('replays the last theme before the first render', () => {
    applyAppearance({ ...base, appearance: { ...APPEARANCE_DEFAULTS, theme: 'porcelain' } })
    document.head.innerHTML = ''
    delete document.documentElement.dataset.scheme
    restoreLastTheme()
    expect(document.getElementById('sb-theme')?.textContent).toContain('color-scheme: light;')
    expect(document.documentElement.dataset.scheme).toBe('light')
  })

  it('ignores a cached sheet that is not one it wrote', () => {
    window.localStorage.setItem('switchboard.theme-css', 'body { display: none }')
    restoreLastTheme()
    expect(document.getElementById('sb-theme')).toBeNull()
  })
})
