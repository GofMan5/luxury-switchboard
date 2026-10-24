import type { StyleContribution, ThemeContribution } from './theme'

export type ContributionTone = 'neutral' | 'info' | 'success' | 'warning' | 'danger'

/** An entry in the command palette (Ctrl+K). */
export interface CommandContribution {
  /** Unique across owners: `<owner>:<command>`. */
  readonly id: string
  readonly title: string
  /** Palette section heading: "Navigate", "Relay", "Appearance", a plugin name… */
  readonly group: string
  readonly keywords?: readonly string[]
  /** Display-only hint such as "Ctrl+Shift+L"; the owner binds its own keys. */
  readonly shortcut?: string
  readonly run: () => void | Promise<void>
}

/** A short readout in the runtime bar, right of the route. */
export interface StatusContribution {
  readonly id: string
  readonly text: string
  readonly tone?: ContributionTone
  readonly tooltip?: string
}

/** A workspace page in the sidebar's Plugins section. */
export interface PageContribution {
  /** `<pluginId>/<pageId>`; the route is `#plugin/<id>`. */
  readonly id: string
  readonly title: string
  /** A lucide icon name from the curated set the shell knows; unknown names
   * fall back to a neutral puzzle icon. */
  readonly icon?: string
}

export interface OwnerContributions {
  readonly themes: readonly ThemeContribution[]
  readonly styles: readonly StyleContribution[]
  readonly commands: readonly CommandContribution[]
  readonly status: readonly StatusContribution[]
  readonly pages: readonly PageContribution[]
}

export interface ExtensionSnapshot extends OwnerContributions {
  /** Bumps on every change so consumers can memo on one number. */
  readonly revision: number
}

const EMPTY: OwnerContributions = { themes: [], styles: [], commands: [], status: [], pages: [] }

/**
 * Where slices meet without importing each other: a slice publishes what it
 * contributes under its own owner key, and the shell, the palette and the
 * appearance slice read the merged snapshot. Owners replace their own entries
 * wholesale and can never touch another owner's.
 */
export class ExtensionPoints {
  #owners = new Map<string, OwnerContributions>()
  #snapshot: ExtensionSnapshot = { ...EMPTY, revision: 0 }
  #listeners = new Set<() => void>()

  snapshot = (): ExtensionSnapshot => this.#snapshot

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => { this.#listeners.delete(listener) }
  }

  /** Replaces the given kinds for this owner; kinds left out stay as they were. */
  contribute(owner: string, contributions: Partial<OwnerContributions>): void {
    const current = this.#owners.get(owner) ?? EMPTY
    this.#owners.set(owner, { ...current, ...contributions })
    this.#publish()
  }

  clear(owner: string): void {
    if (!this.#owners.delete(owner)) return
    this.#publish()
  }

  #publish(): void {
    const merged = { themes: [] as ThemeContribution[], styles: [] as StyleContribution[], commands: [] as CommandContribution[], status: [] as StatusContribution[], pages: [] as PageContribution[] }
    for (const contribution of this.#owners.values()) {
      merged.themes.push(...contribution.themes)
      merged.styles.push(...contribution.styles)
      merged.commands.push(...contribution.commands)
      merged.status.push(...contribution.status)
      merged.pages.push(...contribution.pages)
    }
    this.#snapshot = { ...merged, revision: this.#snapshot.revision + 1 }
    for (const listener of this.#listeners) listener()
  }
}
