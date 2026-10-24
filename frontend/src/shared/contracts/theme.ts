/**
 * A theme another slice contributes to the appearance slice — today, a plugin
 * that declared `ui:theme`. The appearance slice validates the tokens again
 * before any of them reaches the document, so a contribution is a proposal,
 * not a write.
 */
export interface ThemeContribution {
  /** `plugin:<pluginId>` — the value Appearance.theme stores when it is chosen. */
  readonly id: string
  readonly name: string
  readonly scheme: 'dark' | 'light'
  /** CSS custom properties (`--bg-canvas` …) and their values. */
  readonly tokens: Readonly<Record<string, string>>
}

/** A stylesheet another slice asks the document to carry, scoped by owner so
 * it can be removed with that owner. */
export interface StyleContribution {
  readonly owner: string
  readonly css: string
}
