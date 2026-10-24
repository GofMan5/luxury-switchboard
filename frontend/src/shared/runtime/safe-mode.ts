/**
 * Safe mode is the way back from a stylesheet or plugin that broke the
 * window: the page starts without custom CSS, custom tokens or plugin code,
 * while the managers that can undo the change keep working. It is read once
 * per document load from the URL, so it is a launch fact rather than mutable
 * state — entering or leaving it reloads the window.
 */
const SAFE_PARAM = 'safe'

export const safeMode: boolean = typeof window !== 'undefined'
  && new URLSearchParams(window.location.search).has(SAFE_PARAM)

/** Reloads the window with safe mode on or off, keeping every other query
 * parameter (the dev fixture flag among them) and the current route. */
export function restartInSafeMode(enabled: boolean): void {
  const url = new URL(window.location.href)
  if (enabled) url.searchParams.set(SAFE_PARAM, '1')
  else url.searchParams.delete(SAFE_PARAM)
  window.location.replace(url.toString())
}

/** Ctrl+Alt+Shift+S works even when custom CSS hid every control on screen. */
export function isSafeModeChord(event: KeyboardEvent): boolean {
  return event.ctrlKey && event.altKey && event.shiftKey && event.code === 'KeyS'
}
