/**
 * The stable styling surface of the window: every element listed here carries
 * `data-ui="<name>"`, and a theme, custom stylesheet or plugin stylesheet may
 * select it as `[data-ui='<name>']`. Class names are build output and change
 * without notice; these names, and the attributes listed with them, change
 * only with a note in the plugin documentation.
 *
 * The document root also carries `data-route` (the open page, `providers` or
 * `plugin/<plugin>/<page>`), `data-scheme` and `data-animations`.
 */
export interface UiPart {
  readonly name: string
  readonly description: string
  /** Attributes on the same element that a selector may rely on. */
  readonly attributes?: readonly string[]
  /** Present only in some states: the condition, in a few words. */
  readonly when?: string
}

export const UI_PARTS: readonly UiPart[] = [
  // The frame.
  { name: 'shell', description: 'The whole window: rail plus workspace.', attributes: ['data-sidebar'] },
  { name: 'sidebar', description: 'The navigation rail.' },
  { name: 'sidebar-brand', description: 'Product mark and name at the top of the rail.' },
  { name: 'palette-trigger', description: 'The "Search and run" button that opens the command palette.' },
  { name: 'nav', description: 'The scrolling list of navigation sections.' },
  { name: 'nav-section', description: 'One group of rail entries.', attributes: ['data-section'] },
  { name: 'nav-section-label', description: 'A section heading such as "Routing".', when: 'the section has a heading' },
  { name: 'nav-item', description: 'One rail entry.', attributes: ['data-route', 'data-active', 'aria-current'] },
  { name: 'nav-badge', description: 'A plugin page count on its rail entry; a dot when the rail is folded.', attributes: ['data-tone'], when: 'a page contributes a badge' },
  { name: 'sidebar-footer', description: 'Collapse toggle, update pill and version at the foot of the rail.' },
  { name: 'sidebar-toggle', description: 'The collapse/expand button.', attributes: ['aria-pressed'] },
  { name: 'update-pill', description: 'The "update available" button.', when: 'a newer release is out' },
  { name: 'app-version', description: 'The running version.' },
  { name: 'workspace', description: 'Everything right of the rail: runtime bar and page.' },
  { name: 'runtime-bar', description: 'The one-line instrument bar above every page.' },
  { name: 'relay-state', description: 'Relay status dot and label.', attributes: ['data-state'] },
  { name: 'relay-address', description: 'The loopback listener address.' },
  { name: 'active-route', description: 'The provider traffic routes to, with its health dot.' },
  { name: 'status-readout', description: 'A contributed runtime-bar readout.', attributes: ['data-tone', 'data-owner'], when: 'a slice or plugin contributes status' },
  { name: 'status-overflow', description: 'The focusable "+N" pill holding readouts that do not fit; its title lists them.', when: 'more readouts than fit' },
  { name: 'runtime-actions', description: 'Safe-mode badge, relay button and notifications bell.' },
  { name: 'safe-mode-badge', description: 'The "Safe mode" button that restarts normally.', when: 'the window runs in safe mode' },
  { name: 'workspace-content', description: 'The scrolling page container.', attributes: ['data-route'] },
  { name: 'workspace-error', description: 'The fallback a failed page renders.', when: 'a page failed to render' },
  // Command palette.
  { name: 'palette-scrim', description: 'The dimmed backdrop behind the palette.', when: 'the palette is open' },
  { name: 'command-palette', description: 'The palette dialog.', when: 'the palette is open' },
  { name: 'palette-search', description: 'The palette search field row.', when: 'the palette is open' },
  { name: 'palette-results', description: 'The result list.', when: 'the palette is open' },
  { name: 'palette-group', description: 'A group heading in the results.', when: 'the palette is open' },
  { name: 'palette-option', description: 'One command in the results.', attributes: ['data-command', 'aria-selected'], when: 'the palette is open' },
  { name: 'palette-footer', description: 'Key hints under the results.', when: 'the palette is open' },
  // Notifications.
  { name: 'notifications-bell', description: 'The bell button in the runtime bar.' },
  { name: 'notifications-badge', description: 'The unread count on the bell.', when: 'there are unread notifications' },
  { name: 'notifications-panel', description: 'The notification feed dialog.', when: 'the feed is open' },
  { name: 'notification', description: 'One entry in the feed.', attributes: ['data-severity'], when: 'the feed is open' },
  { name: 'toast-stack', description: 'The live toast region.', when: 'a toast is on screen' },
  { name: 'toast', description: 'One toast.', attributes: ['data-severity'], when: 'a toast is on screen' },
  // Page building blocks.
  { name: 'panel', description: 'A workspace card.' },
  { name: 'panel-header', description: 'A card header.' },
  { name: 'panel-title', description: 'A card title and subtitle.' },
  { name: 'panel-actions', description: 'A card header actions.', when: 'the card has actions' },
  { name: 'metric-strip', description: 'A row of metrics.' },
  { name: 'metric', description: 'One label/value/detail metric.', attributes: ['data-tone'] },
  { name: 'segmented', description: 'A one-of-few choice control.' },
  { name: 'pill', description: 'A small state chip.', attributes: ['data-tone'] },
  { name: 'empty-state', description: 'The "nothing here yet" block.' },
]
