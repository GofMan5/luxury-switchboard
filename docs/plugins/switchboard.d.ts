/**
 * Type declarations for Luxury Switchboard plugin scripts (package schema 1).
 *
 * The script of a plugin runs in its own Web Worker. Inside it the only way to
 * reach the window or the relay is the global `switchboard`, declared here.
 * Add these two lines to the top of `main.js` so your editor completes and
 * checks every call:
 *
 *     // @ts-check
 *     /// <reference path="./switchboard.d.ts" />
 *
 * (Copy this file next to `main.js`, or point the path at `docs/plugins/`.)
 *
 * Every limit and error code here is the one the host enforces. The guide is
 * docs/PLUGINS.md, and the agent skill is .agents/skills/switchboard-plugin/.
 *
 * Every call is a message to the host. A call that is refused rejects with a
 * {@link Switchboard.PluginError}: an `Error` whose `code` names the reason.
 * Arguments must be plain JSON data; anything else rejects before it is sent.
 * Except for `ui.render`, a call's arguments are at most 300 KiB (`too_large`).
 */
declare namespace Switchboard {
  // ───────────────────────────── basics ─────────────────────────────

  /** The permissions a package can declare. The operator grants a subset at install time. */
  type Permission =
    | 'ui:theme'
    | 'ui:css'
    | 'ui:pages'
    | 'ui:status'
    | 'ui:commands'
    | 'ui:notify'
    | 'ui:clipboard'
    | 'control:read'
    | 'control:write'
    | 'control:sensitive'
    | 'storage'

  type Tone = 'neutral' | 'info' | 'success' | 'warning' | 'danger'

  /**
   * Why a call was refused (`error.code`):
   * - `permission_denied`: the plugin was not granted the permission the call needs.
   * - `not_allowed`: no plugin may use this command or topic.
   * - `invalid_argument`: an argument has the wrong shape. The message names the field.
   * - `invalid_tree`: `ui.render` got a tree that does not check. The message gives the path.
   * - `not_found`: no such page, or no such route.
   * - `limit`: too many pages, commands, status readouts or topics.
   * - `busy`: 16 control calls are already in flight, or a confirm is already open.
   * - `rate_limited`: more than 6 notifications, or more than 120 storage writes, in a minute.
   * - `too_large`: a value, clipboard text or call is over its size limit.
   * - `plugin_storage_full`: storage would go over 64 keys or 64 KiB.
   * - `needs_gesture`: `ui.confirm` or `clipboard.writeText` was called more than 10 s
   *   after the operator last used the plugin's page or command.
   * - `conflict`: a command shortcut is already bound.
   * - `failed`: anything else, such as a control-plane error or a stopped plugin.
   *
   * A control call can also reject with the relay's own error codes (for example
   * `provider_not_found`).
   */
  type ErrorCode =
    | 'permission_denied'
    | 'not_allowed'
    | 'invalid_argument'
    | 'invalid_tree'
    | 'not_found'
    | 'limit'
    | 'busy'
    | 'rate_limited'
    | 'too_large'
    | 'plugin_storage_full'
    | 'needs_gesture'
    | 'conflict'
    | 'failed'
    | (string & {})

  /** What every refused call rejects with. */
  interface PluginError extends Error {
    readonly code: ErrorCode
  }

  /** Icon names for pages and buttons. An unknown name shows the puzzle piece. */
  type Icon =
    | 'activity' | 'chart' | 'bell' | 'box' | 'clock' | 'cpu' | 'database' | 'gauge'
    | 'globe' | 'layers' | 'dashboard' | 'list' | 'puzzle' | 'shield' | 'terminal' | 'zap'

  // ───────────────────────────── settings ─────────────────────────────

  /** The value of a setting: a string for text and select, a number, or a boolean for a toggle. */
  type SettingValue = string | number | boolean

  interface SettingsApi {
    /** The current value of `key`, or `undefined` if the package declares no such setting. */
    get(key: string): SettingValue | undefined
    /** Every setting's current value, by key. The object is frozen and replaced on each change. */
    all(): Readonly<Record<string, SettingValue>>
    /**
     * Runs `handler` each time the operator saves different values. `changed` lists the
     * keys whose value changed. Returns a function that stops listening.
     * Settings are read-only to the plugin: only the operator changes them.
     */
    onChange(handler: (values: Readonly<Record<string, SettingValue>>, changed: readonly string[]) => void | Promise<void>): () => void
  }

  // ───────────────────────────── pages ─────────────────────────────

  interface PageSpec {
    /** `^[a-z0-9][a-z0-9_-]{0,47}$`, unique within the plugin. */
    readonly id: string
    /** The sidebar label: 1–48 characters. */
    readonly title: string
    /** Lowercase letters only (`^[a-z]{1,24}$`), otherwise `invalid_argument`. */
    readonly icon?: Icon
  }

  interface StatusSpec {
    /** 1–40 characters. */
    readonly text: string
    readonly tone?: Tone
    /** At most 200 characters. The host adds "— <plugin name>" to it. */
    readonly tooltip?: string
  }

  interface BadgeSpec {
    /** 1–8 characters on one line, such as a count. */
    readonly text: string
    readonly tone?: Tone
  }

  interface ConfirmSpec {
    /** 1–120 characters. */
    readonly title: string
    /** At most 1000 characters. */
    readonly body?: string
    /** The label of the confirm button: 1–24 characters. The default is "Continue". */
    readonly confirmText?: string
    /** `danger` gives the confirm button a red style. The default is `neutral`. */
    readonly tone?: 'neutral' | 'danger'
  }

  /** What a page sends to `ui.onEvent` handlers. The object is frozen. */
  interface PageEvent {
    /**
     * - `action`: a button or a table row was pressed, or Enter was pressed in a field that has `action`.
     * - `change`: a field committed a new value. `select`, `segmented`, `toggle` and `tabs` commit at once;
     *   `input` and `textarea` commit on blur, and on Enter (Ctrl/⌘+Enter in a textarea) when they have no `action`.
     * - `show` and `hide`: the page was opened or left.
     */
    readonly kind: 'action' | 'change' | 'show' | 'hide'
    /** The page the event came from. */
    readonly pageId: string
    /** The `action` of the button, field or table (`rowAction`), for `kind: 'action'`. */
    readonly action?: string
    /** The `name` of the field that changed, or of the field where Enter was pressed. */
    readonly name?: string
    /** The new value of that field. */
    readonly value?: string | boolean
    /** For a table `rowAction`: the row's `id`, or its index when it has none. */
    readonly row?: string | number
    /**
     * The current value of every named field on the page. A toggle gives a boolean.
     * Every other field gives a string, and that includes `input` with `kind: 'number'`,
     * so convert it with `Number(...)`.
     */
    readonly form: Readonly<Record<string, string | boolean>>
  }

  // ───────────────────────────── commands ─────────────────────────────

  type ShortcutKey =
    | 'A' | 'B' | 'C' | 'D' | 'E' | 'F' | 'G' | 'H' | 'I' | 'J' | 'K' | 'L' | 'M'
    | 'N' | 'O' | 'P' | 'Q' | 'R' | 'S' | 'T' | 'U' | 'V' | 'W' | 'X' | 'Y' | 'Z'
    | '0' | '1' | '2' | '3' | '4' | '5' | '6' | '7' | '8' | '9'
    | 'F1' | 'F2' | 'F3' | 'F4' | 'F5' | 'F6' | 'F7' | 'F8' | 'F9' | 'F10' | 'F11' | 'F12'

  /**
   * A global keyboard shortcut. Use Alt+Key, Ctrl+Shift+Key or Ctrl+Alt+Key, where Key is A–Z,
   * 0–9 or F1–F12. Shift may be added to Alt+Key and Ctrl+Alt+Key. The host accepts any case and
   * order; this type lists the canonical spelling. On macOS, Ctrl also matches ⌘.
   *
   * These are reserved: Ctrl+Alt+Shift+S, Alt+F4, Alt+1…Alt+9 and
   * Ctrl+Shift+C/I/J/N/Q/R/T/V/W/Z.
   */
  type Shortcut =
    | `Alt+${ShortcutKey}`
    | `Alt+Shift+${ShortcutKey}`
    | `Ctrl+Alt+${ShortcutKey}`
    | `Ctrl+Alt+Shift+${ShortcutKey}`
    | `Ctrl+Shift+${ShortcutKey}`

  interface CommandSpec {
    /** `^[a-z0-9][a-z0-9_.-]{0,47}$`, unique within the plugin. */
    readonly id: string
    /** The palette label: 1–80 characters. */
    readonly title: string
    /** A sub-group shown after the plugin's name: 1–32 characters. */
    readonly group?: string
    /** Up to 8 extra search words, each 1–32 characters. */
    readonly keywords?: readonly string[]
    /**
     * A real global key binding. It runs the command, and it does not fire inside dialogs.
     * - `invalid_argument` "shortcut: …": the shortcut is badly formed or reserved.
     * - `conflict`: another command already holds the shortcut.
     */
    readonly shortcut?: Shortcut
  }

  interface NotifySpec {
    /** 1–120 characters. */
    readonly title: string
    /** At most 1000 characters. The host adds "— <plugin name>". */
    readonly body?: string
    /** The default is `info`. */
    readonly tone?: Tone
  }

  // ───────────────────────────── control plane ─────────────────────────────

  /** Commands that need `control:read`. */
  type ReadMethod =
    | 'relay.status' | 'providers.list' | 'providers.health' | 'keys.list' | 'activity.list'
    | 'activity.summary' | 'history.recent' | 'history.stats' | 'analytics.report'
    | 'analytics.prices.get' | 'settings.get' | 'guardrails.status' | 'guardrails.findings'
    | 'notifications.list' | 'routes.list' | 'tunnel.get' | 'clients.list' | 'clients.events'
    | 'codex.status' | 'codex.quota' | 'codex.quota.all' | 'updates.status' | 'appearance.get'

  /** Commands that need `control:write`. */
  type WriteMethod =
    | 'relay.start' | 'relay.stop' | 'providers.activate' | 'routes.upsert' | 'routes.upsertMany'
    | 'routes.delete' | 'routes.remove' | 'models.discover' | 'models.test' | 'keys.check'
    | 'keys.reset' | 'keys.move' | 'guardrails.clear' | 'notifications.clear'
    | 'analytics.prices.set' | 'analytics.prices.remove' | 'analytics.prices.setCurrency'
    | 'clients.profile' | 'codex.wake' | 'updates.check' | 'tunnel.privacy_test'

  /** Commands that need `control:sensitive`. The operator must tick it deliberately at install time. */
  type SensitiveMethod =
    | 'providers.add' | 'providers.update' | 'providers.delete' | 'keys.add' | 'keys.addMany'
    | 'keys.update' | 'keys.remove' | 'settings.update' | 'tunnel.configure' | 'tunnel.start'
    | 'tunnel.stop' | 'tunnel.rotate' | 'tunnel.reveal' | 'codex.login.start' | 'codex.login.status'
    | 'codex.login.cancel' | 'codex.login.device.start' | 'codex.import.json' | 'codex.import.files'
    | 'codex.logout'

  /**
   * Every control-plane command a plugin can send. These are refused with `not_allowed`:
   * `plugins.*`, `system.*`, `backup.*`, `appearance.update`, `updates.install`, and any
   * command not listed here.
   */
  type ControlMethod = ReadMethod | WriteMethod | SensitiveMethod

  /** Live topics. Each one needs `control:read`. */
  type Topic =
    | 'activity.changed' | 'appearance.changed' | 'clients.changed' | 'codex.changed'
    | 'guardrails.changed' | 'guardrails.finding' | 'keys.changed' | 'models.tested'
    | 'notifications.raised' | 'providers.changed' | 'providers.health' | 'relay.changed'
    | 'routes.changed' | 'settings.changed' | 'tunnel.changed' | 'updates.installProgress'
    | 'updates.stateChanged'

  /** Pages of the window that `navigate` accepts. `settings` works only where the Settings page is shown. */
  type BuiltInRoute =
    | 'overview' | 'activity' | 'insights' | 'providers' | 'keys' | 'routes' | 'tests'
    | 'guardrails' | 'tunnel' | 'clients' | 'appearance' | 'plugins' | 'settings'

  /** A built-in page, or a plugin page as returned by `ui.registerPage` (`plugin/<pluginId>/<pageId>`). */
  type Route = BuiltInRoute | `plugin/${string}/${string}`

  // ───────────────────────────── page tree ─────────────────────────────
  //
  // A page is one tree of these nodes. The host checks the whole tree and refuses an
  // unknown field on any node or record with
  //   `tree.children[2].varient is not a field of a "button" node.`
  // `null` and `false` children are skipped, so `cond ? node : null` works.
  // Tree limits: 2000 nodes (rows, items, options, tabs and chart points count), 24 levels,
  // 256 KiB of JSON, text 4096 characters, code 32 KiB, labels 120 characters.

  /** A value the host formats: numbers are grouped, booleans read Yes or No, and `null` reads —. */
  type Scalar = string | number | boolean | null
  type Gap = 'none' | 'sm' | 'md' | 'lg'
  /** A child slot: `null` and `false` are skipped. */
  type Child = UiNode | null | false
  /** Field names, actions, column keys and tab ids: `^[A-Za-z0-9_.:-]{1,64}$`. */
  type Name = string

  interface NodeBase {
    /** A stable identity (at most 64 characters) that keeps a node's state while siblings come and go. */
    readonly key?: string
  }

  /** Children one above another. The default gap is `md`. */
  interface StackNode extends NodeBase { readonly type: 'stack'; readonly gap?: Gap; readonly children?: readonly Child[] }
  /** Children side by side. Defaults: gap `md`, align `center`, wrap `true`. */
  interface RowNode extends NodeBase { readonly type: 'row'; readonly gap?: Gap; readonly align?: 'start' | 'center' | 'end' | 'between'; readonly wrap?: boolean; readonly children?: readonly Child[] }
  /** Equal columns, 1–6 of them (the default is 2). They collapse on narrow windows. */
  interface GridNode extends NodeBase { readonly type: 'grid'; readonly columns?: 1 | 2 | 3 | 4 | 5 | 6; readonly gap?: Gap; readonly children?: readonly Child[] }
  /** A titled card. `actions` (usually buttons) sit in its header. */
  interface PanelNode extends NodeBase { readonly type: 'panel'; readonly title?: string; readonly subtitle?: string; readonly actions?: readonly Child[]; readonly children?: readonly Child[] }
  /** The default level is 2. */
  interface HeadingNode extends NodeBase { readonly type: 'heading'; readonly text: string; readonly level?: 1 | 2 | 3 }
  /** A paragraph. Defaults: tone `default`, size `md`, weight `regular`. */
  interface TextNode extends NodeBase {
    readonly type: 'text'; readonly text: string; readonly tone?: Tone | 'default' | 'muted'
    readonly mono?: boolean; readonly size?: 'sm' | 'md' | 'lg'; readonly weight?: 'regular' | 'strong'
  }
  interface MetricItem { readonly label: string; readonly value: Scalar; readonly detail?: string; readonly tone?: 'success' | 'warning' | 'danger' }
  /** One figure with a label. */
  interface MetricNode extends NodeBase, MetricItem { readonly type: 'metric' }
  /** A strip of up to 12 figures. */
  interface MetricsNode extends NodeBase { readonly type: 'metrics'; readonly items: readonly MetricItem[] }
  /** The default tone is `neutral`. */
  interface BadgeNode extends NodeBase { readonly type: 'badge'; readonly text: string; readonly tone?: Tone }
  /** A status dot, with an optional label. */
  interface DotNode extends NodeBase { readonly type: 'dot'; readonly tone?: Tone; readonly text?: string }

  interface TableColumn {
    /** The row field this column shows. */
    readonly key: Name
    readonly label?: string
    /** The default is `right` for numeric formats and `left` for `text`. */
    readonly align?: 'left' | 'right'
    readonly mono?: boolean
    /** A pixel number, "120px", "20%" or "auto" (the default). */
    readonly width?: number | string
    /**
     * How numbers are written. The default is `text`.
     * - `percent`: 0.25 is shown as 25%.
     * - `bytes`: shown as B, KiB, MiB, GiB or TiB.
     * - `duration`: the value is in milliseconds.
     */
    readonly format?: 'text' | 'number' | 'percent' | 'bytes' | 'duration'
  }
  /**
   * A flat object keyed by column key. Each cell is a {@link Scalar}, and a missing cell reads —.
   * `id` (a string or finite number, unique in the table) names the row in `rowAction` events.
   * Other keys are ignored. At least one row must have at least one column key, otherwise:
   * `rows[0] has none of the column keys (a, b); rows are flat objects keyed by column key.`
   */
  interface TableRow { readonly id?: string | number; readonly [field: string]: unknown }
  /**
   * Up to 16 columns and 500 rows. With `rowAction`, each row is a button that sends
   * `{ kind: 'action', action: rowAction, row: id ?? index }`. When there are no rows,
   * `empty` is shown, and it defaults to "Nothing to show yet.".
   */
  interface TableNode extends NodeBase { readonly type: 'table'; readonly columns: readonly TableColumn[]; readonly rows: readonly TableRow[]; readonly empty?: string; readonly rowAction?: Name }
  interface ListItem { readonly title: string; readonly detail?: string; readonly meta?: string; readonly tone?: Tone }
  /** Up to 500 items. */
  interface ListNode extends NodeBase { readonly type: 'list'; readonly items: readonly ListItem[]; readonly empty?: string }
  /** Preformatted text, at most 32 KiB. */
  interface CodeNode extends NodeBase { readonly type: 'code'; readonly text: string }
  interface KvItem { readonly key: string; readonly value: Scalar }
  /** Up to 64 label/value pairs. */
  interface KvNode extends NodeBase { readonly type: 'kv'; readonly items: readonly KvItem[] }
  /** Sends `{ kind: 'action', action }`. The default variant is `default`. */
  interface ButtonNode extends NodeBase {
    readonly type: 'button'; readonly text: string; readonly action: Name
    readonly variant?: 'default' | 'primary' | 'danger' | 'ghost'; readonly disabled?: boolean; readonly icon?: Icon
  }
  interface FieldBase extends NodeBase {
    /** The key of this field in `event.form`. */
    readonly name: Name
    readonly label: string
    readonly hint?: string
    /** Marks the field invalid and shows this message under it. */
    readonly error?: string
    readonly required?: boolean
    readonly disabled?: boolean
    /** The starting value. A number or boolean is written as text. */
    readonly value?: string | number | boolean
    readonly placeholder?: string
    /** With `action` set, Enter sends `{ kind: 'action', action, name, value }`. */
    readonly action?: Name
  }
  /**
   * A one-line field. `min`, `max` and `step` apply only when `kind` is `number`; `max` must not be
   * below `min`, and `step` must be above 0. `maxLength` is 1–4096; the default is 256.
   */
  interface InputNode extends FieldBase {
    readonly type: 'input'; readonly kind?: 'text' | 'number' | 'search'; readonly maxLength?: number
    readonly min?: number; readonly max?: number; readonly step?: number
  }
  /** `rows` is 2–12 (the default is 4). `maxLength` is 1–4096 (the default is 1000). */
  interface TextareaNode extends FieldBase { readonly type: 'textarea'; readonly rows?: number; readonly maxLength?: number }
  /** A string option uses the same text as its value and its label. */
  type Option = string | { readonly value: string; readonly label?: string }
  /** A dropdown with up to 100 options. The default value is the first option. */
  interface SelectNode extends NodeBase { readonly type: 'select'; readonly name: Name; readonly label: string; readonly value?: string; readonly options: readonly Option[]; readonly disabled?: boolean }
  /** A row of 1–6 buttons that work as a choice. The default value is the first option. */
  interface SegmentedNode extends NodeBase { readonly type: 'segmented'; readonly name: Name; readonly label: string; readonly value?: string; readonly options: readonly Option[]; readonly disabled?: boolean }
  /** An on/off switch. Its form value is a boolean. */
  interface ToggleNode extends NodeBase { readonly type: 'toggle'; readonly name: Name; readonly label: string; readonly value?: boolean; readonly disabled?: boolean }
  interface Tab { readonly id: Name; readonly label: string; readonly children?: readonly Child[] }
  /** Up to 8 tabs with unique ids. `value` is the open tab's id (the default is the first). Switching tabs sends a change. */
  interface TabsNode extends NodeBase { readonly type: 'tabs'; readonly name: Name; readonly label?: string; readonly value?: Name; readonly tabs: readonly Tab[] }
  /** `value` is 0–1 and is clamped. It may be left out when `indeterminate` is true. The default tone is `info`. */
  interface ProgressNode extends NodeBase { readonly type: 'progress'; readonly value?: number; readonly indeterminate?: boolean; readonly label?: string; readonly tone?: Tone }
  /**
   * 1–200 finite values. `labels`, when given, has one entry per value. `max` must be above `min`.
   * `height` is 48–240 px (the default is 96). The default kind is `line` and the default tone is `info`.
   */
  interface ChartNode extends NodeBase {
    readonly type: 'chart'; readonly kind?: 'line' | 'bars'; readonly label: string; readonly values: readonly number[]
    readonly labels?: readonly string[]; readonly min?: number; readonly max?: number; readonly tone?: Tone; readonly height?: number
  }
  /** A highlighted message, with optional buttons. The default tone is `info`. */
  interface CalloutNode extends NodeBase { readonly type: 'callout'; readonly tone?: Tone; readonly title?: string; readonly text: string; readonly actions?: readonly Child[] }
  interface DividerNode extends NodeBase { readonly type: 'divider' }
  /** An empty state. */
  interface EmptyNode extends NodeBase { readonly type: 'empty'; readonly title: string; readonly hint?: string }
  /** An external link. It must be `https://` without credentials, and it opens outside the app. */
  interface LinkNode extends NodeBase { readonly type: 'link'; readonly text: string; readonly href: string }

  /** One node of a plugin page. Unknown `type` values and unknown fields are refused. */
  type UiNode =
    | StackNode | RowNode | GridNode | PanelNode | HeadingNode | TextNode | MetricNode | MetricsNode
    | BadgeNode | DotNode | TableNode | ListNode | CodeNode | KvNode | ButtonNode | InputNode | TextareaNode
    | SelectNode | SegmentedNode | ToggleNode | TabsNode | ProgressNode | ChartNode | CalloutNode
    | DividerNode | EmptyNode | LinkNode

  // ───────────────────────────── the API ─────────────────────────────

  interface UiApi {
    /**
     * Adds a page to the sidebar, or updates its title and icon. Re-registering keeps the page's tree
     * and badge. Draw it with {@link UiApi.render}.
     * Needs `ui:pages`. A plugin can have at most 8 pages (`limit`).
     * @returns the route to pass to `switchboard.navigate`.
     */
    registerPage(spec: PageSpec): Promise<{ readonly route: string }>
    /** Removes a page. Removing an unknown page does nothing. Needs `ui:pages`. */
    unregisterPage(id: string): Promise<void>
    /**
     * Replaces the page's whole tree. Register the page first (`not_found`).
     * A tree that does not check is refused with `invalid_tree`. The problem, with its path, is also
     * shown on the page. Needs `ui:pages`.
     */
    render(pageId: string, tree: UiNode): Promise<void>
    /** Calls `handler` for events on every page of the plugin. Returns a function that stops listening. */
    onEvent(handler: (event: PageEvent) => void | Promise<void>): () => void
    /** Calls `handler` for events on one page. Returns a function that stops listening. */
    onEvent(pageId: string, handler: (event: PageEvent) => void | Promise<void>): () => void
    /**
     * Shows a short readout in the runtime bar, or removes it when `status` is `null`.
     * `id` matches `^[a-z0-9][a-z0-9_.-]{0,47}$`. Needs `ui:status`. A plugin can show at most 3 (`limit`).
     */
    setStatus(id: string, status: StatusSpec | null): Promise<void>
    /**
     * Shows a small badge on a page's sidebar entry, or removes it when `badge` is `null`.
     * Setting the same badge again does nothing. Needs `ui:pages`. The page must be registered (`not_found`).
     */
    setBadge(pageId: string, badge: BadgeSpec | null): Promise<void>
    /**
     * Asks the operator to confirm in a dialog that names the plugin, and resolves `true` when they do.
     * Needs `ui:notify`.
     * - The call must come within 10 s of the operator using the plugin's page or command
     *   (`needs_gesture`). Answering a confirm counts as using the plugin.
     * - Only one confirm can be open at a time (`busy`).
     */
    confirm(spec: ConfirmSpec): Promise<boolean>
  }

  interface CommandsApi {
    /**
     * Adds a command to the palette (Ctrl+K), or replaces it. The handler runs when the operator picks
     * the command or presses its shortcut. That counts as a gesture, so the handler may call
     * `ui.confirm` or `clipboard.writeText`. If the handler throws, the operator sees "<title> failed".
     * Needs `ui:commands`. A plugin can have at most 32 commands (`limit`). A shortcut that is already
     * held gives `conflict`. When the registration is refused, the previous handler stays.
     * @returns the global id `plugin:<pluginId>:<id>`.
     */
    register(spec: CommandSpec, handler: () => void | Promise<void>): Promise<{ readonly id: string }>
    /** Removes a command and its shortcut. Needs `ui:commands`. */
    unregister(id: string): Promise<void>
  }

  interface ClipboardApi {
    /**
     * Puts text on the clipboard. Plugins can never read the clipboard. Needs `ui:clipboard`.
     * - The call must come within 10 s of the operator using the plugin (`needs_gesture`).
     * - The text is at most 32768 characters (`too_large`).
     * - If the system refuses, the call fails with `failed`.
     */
    writeText(text: string): Promise<void>
  }

  interface ControlApi {
    /**
     * Sends one control-plane command. Each command needs `control:read`, `control:write` or
     * `control:sensitive` (otherwise `permission_denied`). Commands that are not listed give `not_allowed`.
     * At most 16 calls can be in flight (`busy`). The result is the command's JSON result.
     */
    call<T = unknown>(method: ControlMethod, payload?: unknown): Promise<T>
  }

  interface EventsApi {
    /**
     * Follows a live topic. Each topic needs `control:read`; a topic that is not listed gives `not_allowed`.
     * A plugin can follow at most 24 topics (`limit`).
     * @returns a function that stops this handler. When the last handler stops, the topic is unsubscribed.
     */
    on<T = unknown>(topic: Topic, handler: (payload: T) => void | Promise<void>): Promise<() => void>
  }

  /**
   * The plugin's own key/value store. It is encrypted on disk, is kept across updates and is
   * removed with the plugin. Every method needs `storage`.
   * - Keys match `^[A-Za-z0-9._:-]{1,64}$`. Keys that start with `setting:` are refused
   *   (`invalid_argument`) because they hold the operator's settings.
   * - Values are strings of at most 8 KiB (`too_large`).
   * - In total, at most 64 keys and 64 KiB (`plugin_storage_full`).
   * - Writes run in order. At most 120 writes a minute (`rate_limited`); a `setMany` counts once.
   */
  interface StorageApi {
    /** The stored string, or `null`. */
    get(key: string): Promise<string | null>
    /** Stores a string. Use `JSON.stringify` for anything else. */
    set(key: string, value: string): Promise<void>
    remove(key: string): Promise<void>
    /** Every key the plugin stored (settings are not included). */
    all(): Promise<Readonly<Record<string, string>>>
    /**
     * Writes 1–64 keys at once. All of them change or none do. Use `null` to remove a key.
     */
    setMany(values: Readonly<Record<string, string | null>>): Promise<void>
    /** Removes every key the plugin stored. The operator's settings stay. */
    clear(): Promise<void>
  }

  /** Lines go to the plugin's log on the Plugins page. At most 100 lines per 5 s; each line is cut at 2000 characters. */
  interface Logger {
    log(...values: unknown[]): void
    info(...values: unknown[]): void
    debug(...values: unknown[]): void
    warn(...values: unknown[]): void
    error(...values: unknown[]): void
  }

  interface Api {
    /** This plugin, as installed. `granted` is what the operator allowed, which can be less than the package declares. */
    readonly plugin: { readonly id: string; readonly name: string; readonly version: string; readonly granted: readonly Permission[] }
    /** The running app: its version (for example "1.0.65") and the capabilities the sidecar advertises. */
    readonly app: { readonly version: string; readonly capabilities: readonly string[] }
    /** Whether the operator granted `permission`. Check it before using an optional feature. */
    has(permission: Permission): boolean
    /**
     * Runs `handler` once the whole script has finished starting and the plugin is active.
     * If the plugin is already active, the handler runs on the next microtask.
     */
    onActivate(handler: () => void | Promise<void>): void
    readonly ui: UiApi
    /** The operator's values for the package's `settings`. They are available as soon as the script starts. */
    readonly settings: SettingsApi
    readonly clipboard: ClipboardApi
    readonly commands: CommandsApi
    /**
     * Raises a notification in the feed and as a toast. Needs `ui:notify`.
     * At most 6 a minute (`rate_limited`).
     */
    notify(spec: NotifySpec): Promise<void>
    readonly control: ControlApi
    readonly events: EventsApi
    readonly storage: StorageApi
    /**
     * Opens a page of the window. A leading `#/` is ignored. A route that is unknown, or longer than
     * 160 characters, gives `not_found`.
     */
    navigate(route: Route): Promise<void>
    /** The plugin's log. `console` writes to the same log. */
    readonly log: Logger
  }

  // ───────────────────────────── the package ─────────────────────────────

  interface SettingBase {
    /** `^[a-z][A-Za-z0-9_]{0,31}$`, unique. */
    readonly key: string
    /** 1–64 characters. */
    readonly label: string
    /** At most 200 characters. */
    readonly description?: string
  }
  /** `maxLength` is 1–256 (the default is 256). `placeholder` is at most 64 characters. The value is one line. */
  interface TextSetting extends SettingBase { readonly type: 'text'; readonly default: string; readonly placeholder?: string; readonly maxLength?: number }
  /** A finite number. The default must lie within `min`/`max`. */
  interface NumberSetting extends SettingBase { readonly type: 'number'; readonly default: number; readonly placeholder?: string; readonly min?: number; readonly max?: number }
  interface ToggleSetting extends SettingBase { readonly type: 'toggle'; readonly default: boolean }
  /** 1–32 options with unique values. Each value and label is at most 64 characters. `default` must be one of the values. */
  interface SelectSetting extends SettingBase { readonly type: 'select'; readonly default: string; readonly options: readonly { readonly value: string; readonly label: string }[] }
  /** One operator-editable option. Up to 24 per package. A package with settings needs a `script`. */
  type Setting = TextSetting | NumberSetting | ToggleSetting | SelectSetting

  /**
   * A packed plugin: one JSON file (schema 1). Unknown fields are refused. The machine-readable form
   * is docs/plugins/plugin.schema.json. In a source folder, `plugin.json` holds everything except
   * `script` (from `main.js`) and `css` (from `style.css`); `pnpm plugin pack` joins them.
   */
  interface Package {
    readonly schema: 1
    /** `^[a-z0-9][a-z0-9.-]{1,62}[a-z0-9]$`, for example "acme.latency-board". */
    readonly id: string
    /** 1–64 characters. */
    readonly name: string
    /** `^[0-9A-Za-z.+-]{1,32}$`, for example "1.2.0". */
    readonly version: string
    /** At most 64 characters. */
    readonly author?: string
    /** At most 280 characters. Line breaks are allowed. */
    readonly description?: string
    /** An `https://` address of at most 200 bytes, without credentials. It is shown as a link and never fetched. */
    readonly homepage?: string
    /** The oldest app version the plugin runs on, `major.minor.patch` (for example "1.0.65"). */
    readonly minAppVersion?: string
    /** What the plugin asks for. Each permission appears at most once, and the list may be empty. */
    readonly permissions: readonly Permission[]
    readonly settings?: readonly Setting[]
    /**
     * A colour theme the operator can pick in Appearance. It needs `ui:theme`.
     * It holds at most 96 tokens; each name matches `^--[a-z][a-z0-9-]{0,47}$` and each value is at most 120 bytes.
     */
    readonly theme?: { readonly name: string; readonly scheme: 'dark' | 'light'; readonly tokens: Readonly<Record<string, string>> }
    /** A stylesheet for the whole window. It needs `ui:css`, is at most 64 KiB, and has no `@import` or `</style>`. */
    readonly css?: string
    /** The worker script, at most 128 KiB. Needed for every permission except `ui:theme` and `ui:css`. */
    readonly script?: string
  }
}

/** The plugin API. It exists only inside a plugin's worker. */
declare const switchboard: Switchboard.Api
