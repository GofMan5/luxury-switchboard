// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// The hook is a standalone classic script — no imports, no exports — so the
// test executes the exact file the shell ships instead of a re-typed twin:
// Vite's ?raw loads the bytes from disk without going through Node types.
import hookSource from '../../public/boot-hook.js?raw'
import entryHTML from '../../index.html?raw'

type BootWindow = typeof window & { __dshBootReady?: () => void }

function loadHook(): void {
  new Function(hookSource)()
}

function sentinel(): HTMLElement | null {
  return document.getElementById('boot-sentinel')
}

function failureNotice(): HTMLElement | null {
  const node = sentinel()
  return node && node.textContent !== null && /failed to start/i.test(node.textContent) ? node : null
}

describe('boot sentinel', () => {
  beforeEach(() => {
    document.body.innerHTML = '<div id="root"><div id="boot-sentinel" role="status" aria-live="polite"><p>Starting Luxury Switchboard…</p></div></div>'
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  afterEach(() => {
    // Whatever state a test left, its hook instance stands down: a stale
    // pending listener would otherwise fail the window in a later test.
    ;(window as BootWindow).__dshBootReady?.()
    delete (window as BootWindow).__dshBootReady
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('the entry HTML carries the sentinel the hook needs, with no inline code', () => {
    // The whole defense dies if the hook is inlined: the CSP that stops
    // inline scripts is what makes this file the only thing that can run
    // when the bundle is dead. The stylesheet rides along for the same
    // reason — the bundle's own CSS never loads in that scenario.
    expect(entryHTML).toContain('<script src="/boot-hook.js"></script>')
    expect(entryHTML).toContain('<link rel="stylesheet" href="/boot.css" />')
    expect(entryHTML).toContain('<div id="boot-sentinel"')
    expect(entryHTML).toContain('Starting Luxury Switchboard…')
    expect(entryHTML).not.toMatch(/\son[a-z]+\s*=/iu)
  })

  it('keeps the window lit when the bundle never signals readiness', () => {
    vi.useFakeTimers()
    loadHook()
    expect(failureNotice()).toBeNull()
    // 15s of silence from a bundle that never commits must become a
    // sentence, not a black window.
    vi.advanceTimersByTime(15_000)
    expect(failureNotice()).not.toBeNull()
    expect(sentinel()?.getAttribute('data-state')).toBe('failed')
  })

  it('reports a script error before the first frame instead of a black window', () => {
    loadHook()
    window.dispatchEvent(new Event('error'))
    expect(failureNotice()).not.toBeNull()
    // The owner gets the reason in the console; the screen states the fact
    // and nothing else, same policy as the workspace boundary fallback.
    expect(console.error).toHaveBeenCalledWith(expect.stringContaining('never reached its first frame'), expect.anything())
  })

  it('treats an unhandled rejection before the first frame the same way', () => {
    loadHook()
    window.dispatchEvent(new Event('unhandledrejection'))
    expect(failureNotice()).not.toBeNull()
  })

  it('stands down once the app reports its first frame, and stays down', () => {
    vi.useFakeTimers()
    loadHook()
    ;(window as BootWindow).__dshBootReady!()
    // The pending message must not cover the app's window, and a later
    // script error must not overwrite a working session with a boot notice.
    expect(sentinel()).toBeNull()
    vi.advanceTimersByTime(60_000)
    window.dispatchEvent(new Event('error'))
    expect(sentinel()).toBeNull()
  })

  it('rebuilds the notice when the first commit already wiped the message', () => {
    // main.tsx removes the pending message before render; if the first
    // commit then dies, there is no sentinel left to rewrite — the hook has
    // to put one back somewhere visible.
    document.body.innerHTML = '<div id="root"></div>'
    loadHook()
    window.dispatchEvent(new Event('error'))
    expect(failureNotice()).not.toBeNull()
    expect(document.getElementById('root')?.contains(failureNotice())).toBe(true)
  })
})
