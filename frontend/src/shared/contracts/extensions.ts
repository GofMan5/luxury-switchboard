import type { ReactNode } from 'react'
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
  /** A hint such as "Ctrl+Shift+L". Display-only unless {@link bind} is set. */
  readonly shortcut?: string
  /** The shell binds {@link shortcut} as a global key for this command. Only
   * for owners that cannot listen to the keyboard themselves (plugins); the
   * shell's own keys always win and a combination binds at most once. */
  readonly bind?: boolean
  readonly run: () => void | Promise<void>
}

/** A short count or label on a sidebar entry, such as "3" or "new". */
export interface BadgeContribution {
  readonly text: string
  readonly tone?: ContributionTone
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
  readonly badge?: BadgeContribution
}

/** Where a screen hosts contributed content. */
export type SlotScope = 'overview' | 'activity' | 'provider' | 'settings'

/**
 * What a slot is shown for. `id` is the request id (activity) or provider id
 * (provider) — an identifier only, never the record itself, so no endpoint or
 * credential detail can reach a contributor through a slot.
 */
export interface SlotContext {
  readonly scope: SlotScope
  readonly id?: string
}

/**
 * A framed area a screen shows: an Overview widget, an inspector tab
 * (activity, provider) or a Settings → Plugins section. The shell draws the
 * frame, the "From <owner>" label and an error boundary around `render`.
 */
export interface SlotContribution {
  /** Unique across owners: `<owner>:<surface>`. */
  readonly id: string
  /** Display name for the "From …" label. */
  readonly owner: string
  readonly scope: SlotScope
  readonly title: string
  /** Lower first; ties keep title order. */
  readonly order?: number
  /** Overview widgets only: spans the full row. */
  readonly wide?: boolean
  readonly description?: string
  render(context: SlotContext): ReactNode
}

export type ActionScope = 'activity' | 'provider' | 'key' | 'header'

/** A button a screen shows next to its own: an inspector action, an API-key
 * row action, or a runtime-bar header button. */
export interface ActionContribution {
  /** Unique across owners: `<owner>:<action>`. */
  readonly id: string
  /** Display name, shown in the button's description. */
  readonly owner: string
  readonly scope: ActionScope
  readonly title: string
  /** A lucide icon name from the curated set; unknown names fall back to a
   * neutral icon. Header buttons are icon-only, so they always show one. */
  readonly icon?: string
  readonly tone?: 'neutral' | 'danger'
  /** `targetId` is the request, provider or key id; absent for `header`. */
  run(targetId?: string): void
}

/**
 * One owner's arrangement of the built-in rail. Ids are route ids
 * (`overview`, `keys`, …). The shell ignores unknown ids and never hides
 * `plugins` or `settings`, so the way back is always on screen.
 */
export interface LayoutContribution {
  /** Display name for "Layout customised by …". */
  readonly owner: string
  readonly hidden: readonly string[]
  /** Built-in ids in the wanted order; unlisted items keep their place after these within each section. */
  readonly order: readonly string[]
  /** Opened once at start, if the operator has not navigated yet. */
  readonly startPage?: string
  /** Drops this layout. The owner removes its contribution, so the rail returns to the default. */
  reset(): void
}

export interface OwnerContributions {
  readonly themes: readonly ThemeContribution[]
  readonly styles: readonly StyleContribution[]
  readonly commands: readonly CommandContribution[]
  readonly status: readonly StatusContribution[]
  readonly pages: readonly PageContribution[]
  readonly slots: readonly SlotContribution[]
  readonly actions: readonly ActionContribution[]
  /** At most one owner arranges the rail; see {@link ExtensionSnapshot.layout}. */
  readonly layout?: LayoutContribution
}

export interface ExtensionSnapshot extends OwnerContributions {
  /** Bumps on every change so consumers can memo on one number. */
  readonly revision: number
  /** The first owner that contributed a layout (still holding it) wins; a
   * later owner's layout is ignored until the first one clears. */
  readonly layout?: LayoutContribution
  /** Owner key (`plugin:<id>`) of {@link layout}, for owners that must refuse a second layout. */
  readonly layoutOwner?: string
}

const EMPTY: OwnerContributions = { themes: [], styles: [], commands: [], status: [], pages: [], slots: [], actions: [] }

/**
 * Where slices meet without importing each other: a slice publishes what it
 * contributes under its own owner key, and the shell, the palette and the
 * appearance slice read the merged snapshot. Owners replace their own entries
 * wholesale and can never touch another owner's.
 */
export class ExtensionPoints {
  #owners = new Map<string, OwnerContributions>()
  /** Owners holding a layout, in the order they claimed it. */
  #layoutClaims: string[] = []
  #snapshot: ExtensionSnapshot = { ...EMPTY, revision: 0 }
  #listeners = new Set<() => void>()
  #styleHolds = 0

  snapshot = (): ExtensionSnapshot => this.#snapshot

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener)
    return () => { this.#listeners.delete(listener) }
  }

  /** Replaces the given kinds for this owner; kinds left out stay as they were. */
  contribute(owner: string, contributions: Partial<OwnerContributions>): void {
    const current = this.#owners.get(owner) ?? EMPTY
    this.#owners.set(owner, { ...current, ...contributions })
    if ('layout' in contributions) {
      const claimed = this.#layoutClaims.includes(owner)
      if (!contributions.layout) this.#layoutClaims = this.#layoutClaims.filter((claim) => claim !== owner)
      else if (!claimed) this.#layoutClaims.push(owner)
    }
    this.#publish()
  }

  clear(owner: string): void {
    if (!this.#owners.delete(owner)) return
    this.#layoutClaims = this.#layoutClaims.filter((claim) => claim !== owner)
    this.#publish()
  }

  /**
   * Withholds every contributed stylesheet until the returned release runs.
   * For screens a stylesheet must not be able to restyle — a permission
   * review, where hiding one warning would change what the operator agrees
   * to. Holds nest; releasing twice is harmless.
   */
  holdStyles(): () => void {
    this.#styleHolds += 1
    if (this.#styleHolds === 1) this.#publish()
    let released = false
    return () => {
      if (released) return
      released = true
      this.#styleHolds -= 1
      if (this.#styleHolds === 0) this.#publish()
    }
  }

  #publish(): void {
    const merged = { themes: [] as ThemeContribution[], styles: [] as StyleContribution[], commands: [] as CommandContribution[], status: [] as StatusContribution[], pages: [] as PageContribution[], slots: [] as SlotContribution[], actions: [] as ActionContribution[] }
    for (const contribution of this.#owners.values()) {
      merged.themes.push(...contribution.themes)
      if (this.#styleHolds === 0) merged.styles.push(...contribution.styles)
      merged.commands.push(...contribution.commands)
      merged.status.push(...contribution.status)
      merged.pages.push(...contribution.pages)
      merged.slots.push(...contribution.slots)
      merged.actions.push(...contribution.actions)
    }
    const layoutOwner = this.#layoutClaims[0]
    const layout = layoutOwner ? this.#owners.get(layoutOwner)?.layout : undefined
    this.#snapshot = { ...merged, ...(layout ? { layout, layoutOwner } : {}), revision: this.#snapshot.revision + 1 }
    for (const listener of this.#listeners) listener()
  }
}

/** A scope's slots in display order: `order` first (absent last), then title. */
export function slotsFor(snapshot: Pick<ExtensionSnapshot, 'slots'>, scope: SlotScope): readonly SlotContribution[] {
  return snapshot.slots
    .filter((slot) => slot.scope === scope)
    .sort((a, b) => (a.order ?? Number.MAX_SAFE_INTEGER) - (b.order ?? Number.MAX_SAFE_INTEGER) || a.title.localeCompare(b.title))
}

/** A scope's actions, in contribution order. */
export function actionsFor(snapshot: Pick<ExtensionSnapshot, 'actions'>, scope: ActionScope): readonly ActionContribution[] {
  return snapshot.actions.filter((action) => action.scope === scope)
}