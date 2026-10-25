import { THEME_TOKENS, THEME_TOKEN_PROPERTIES, type ThemeToken } from '../domain/canvas'
import type { ToneSpec } from '../domain/specs'
import type { FilesPort, PickedFile, ThemeInfo, ThemePort } from '../application/ports'

/** `files.open` through a hidden file input; `files.save` as a download. */
export const browserFiles: FilesPort = {
  open(spec, signal) {
    return new Promise<PickedFile | null>((resolve) => {
      if (signal.aborted) { resolve(null); return }
      const input = document.createElement('input')
      input.type = 'file'
      if (spec.accept.length) input.accept = spec.accept.join(',')
      const finish = (file: File | null) => {
        signal.removeEventListener('abort', abort)
        resolve(file ? { name: file.name, size: file.size, read: () => file.text() } : null)
      }
      const abort = () => finish(null)
      signal.addEventListener('abort', abort, { once: true })
      input.addEventListener('change', () => finish(input.files?.[0] ?? null), { once: true })
      input.addEventListener('cancel', () => finish(null), { once: true })
      input.click()
    })
  },
  save(spec) {
    const url = URL.createObjectURL(new Blob([spec.text], { type: spec.mime }))
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = spec.name
    anchor.click()
    // The download has its own copy once started.
    setTimeout(() => URL.revokeObjectURL(url), 0)
    // ponytail: a download cannot report a cancelled save dialog, so this is
    // always true; use the Tauri dialog plugin if plugins need the answer.
    return Promise.resolve(true)
  },
}

let audio: AudioContext | null = null

/** Plays one short tone; the host has already checked and rate-limited it. */
export function webAudioTone(tone: ToneSpec): void {
  if (typeof AudioContext === 'undefined') return
  audio ??= new AudioContext()
  const at = audio.currentTime
  const end = at + tone.durationMs / 1000
  const oscillator = audio.createOscillator()
  const gain = audio.createGain()
  oscillator.type = tone.type
  oscillator.frequency.value = tone.frequency
  gain.gain.setValueAtTime(tone.volume, at)
  // A short fade avoids the click of a hard stop.
  gain.gain.linearRampToValueAtTime(0, end)
  oscillator.connect(gain).connect(audio.destination)
  oscillator.start(at)
  oscillator.stop(end)
}

function readTheme(): ThemeInfo {
  const style = getComputedStyle(document.documentElement)
  const tokens = Object.fromEntries(THEME_TOKENS.map((token) => [token, style.getPropertyValue(THEME_TOKEN_PROPERTIES[token]).trim()])) as Record<ThemeToken, string>
  return { mode: document.documentElement.dataset.scheme === 'light' ? 'light' : 'dark', tokens }
}

/** The window's colours. Theme changes rewrite the root's attributes or the
 * token stylesheet in `<head>`; listeners run only when a colour changed. */
export const documentTheme: ThemePort = {
  read: readTheme,
  subscribe(listener) {
    let last = JSON.stringify(readTheme())
    let queued = false
    const check = () => {
      queued = false
      const next = JSON.stringify(readTheme())
      if (next === last) return
      last = next
      listener()
    }
    const observer = new MutationObserver(() => {
      if (queued) return
      queued = true
      queueMicrotask(check)
    })
    observer.observe(document.documentElement, { attributes: true })
    observer.observe(document.head, { childList: true, subtree: true, characterData: true })
    return () => observer.disconnect()
  },
}
