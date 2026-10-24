import { vi } from 'vitest'

/** A fresh in-memory Storage on window.localStorage. Node's own experimental
 * global shadows jsdom's when no storage file is configured, leaving tests a
 * localStorage without methods. */
export function stubLocalStorage(): Storage {
  const values = new Map<string, string>()
  const storage: Storage = {
    get length() { return values.size },
    clear: () => values.clear(),
    getItem: (key) => values.get(key) ?? null,
    key: (index) => [...values.keys()][index] ?? null,
    removeItem: (key) => { values.delete(key) },
    setItem: (key, value) => { values.set(key, String(value)) },
  }
  vi.stubGlobal('localStorage', storage)
  Object.defineProperty(window, 'localStorage', { configurable: true, value: storage })
  return storage
}
