import type { Appearance } from '../domain/appearance'

/** Where the appearance record lives. The control plane's encrypted store is
 * the durable home; an older sidecar without the `appearance.manage`
 * capability gets the browser-storage port instead, so personalization still
 * works there — it simply stays on this machine's profile. */
export interface AppearancePort {
  readonly durable: boolean
  get(signal?: AbortSignal): Promise<Appearance>
  update(appearance: Appearance, signal?: AbortSignal): Promise<Appearance>
  subscribe(listener: (appearance: Appearance) => void): () => void
}

/** The last applied record, read synchronously before the first frame so a
 * cold start paints in the user's theme instead of flashing the default. */
export interface AppearanceCache {
  read(): Appearance | null
  write(appearance: Appearance): void
}
