import type { AppearanceCache, AppearancePort } from '../application/appearance-port'
import { normalizeAppearance, type Appearance } from '../domain/appearance'

const CACHE_KEY = 'switchboard.appearance'

/** Browser storage, when this profile really has one. A blocked, sandboxed or
 * stubbed profile can expose `localStorage` without working methods; the
 * cache is a courtesy and must never stop the shell from starting. */
function storage(): Storage | null {
  try {
    const value = window.localStorage
    return typeof value?.getItem === 'function' && typeof value.setItem === 'function' ? value : null
  } catch { return null }
}

export const localAppearanceCache: AppearanceCache = {
  read() {
    try {
      const raw = storage()?.getItem(CACHE_KEY)
      return raw ? normalizeAppearance(JSON.parse(raw)) : null
    } catch { return null }
  },
  write(appearance) {
    try { storage()?.setItem(CACHE_KEY, JSON.stringify(appearance)) } catch { /* a full or blocked profile only costs the first-frame theme */ }
  },
}

/** The fallback store for a control plane that does not advertise
 * `appearance.manage`: the browser profile holds the record. */
export class LocalAppearancePort implements AppearancePort {
  readonly durable = false

  get(): Promise<Appearance> {
    return Promise.resolve(localAppearanceCache.read() ?? normalizeAppearance({}))
  }

  update(appearance: Appearance): Promise<Appearance> {
    const normalized = normalizeAppearance(appearance)
    localAppearanceCache.write(normalized)
    return Promise.resolve(normalized)
  }

  subscribe(): () => void {
    return () => undefined
  }
}
