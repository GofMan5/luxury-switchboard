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
 * Except for `ui.render` and `ui.draw`, a call's arguments are at most 300 KiB (`too_large`).
 *
 * APIs marked "1.0.66" need that app version: set `"minAppVersion": "1.0.66"`
 * in a package that uses them.
 */
declare namespace Switchboard {
  // ───────────────────────────── basics ─────────────────────────────

  /**
   * The permissions a package can declare (at most 24). The operator grants a subset at install time.
   * `control:sensitive`, `net:fetch` and `relay:content` start unticked, with a warning.
   * Every permission except `ui:theme` and `ui:css` needs a `script`.
   */
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
    /** 1.0.66: Overview widgets, inspector tabs, Settings sections, row/inspector actions and header buttons. */
    | 'ui:slots'
    /** 1.0.66: hide or reorder built-in sidebar items and choose the start page. */
    | 'ui:layout'
    /** 1.0.66: modal dialogs that hold a plugin tree. */
    | 'ui:dialogs'
    /** 1.0.66: short sound tones. */
    | 'ui:sound'
    /** 1.0.66: open a text file the operator picks, and save a file. */
    | 'files'
    /** 1.0.66: the plugin-to-plugin message bus. */
    | 'bus'
    /** 1.0.66: HTTPS requests to the hosts in `network.hosts`. Unticked by default. */
    | 'net:fetch'
    /** 1.0.66: request rules and custom retry rules for the relay. */
    | 'relay:rules'
    /**
     * 1.0.66: reads and can change the prompts and answers that pass through your local relay.
     * Never applies to the public tunnel. Unticked by default.
     */
    | 'relay:content'

  type Tone = 'neutral' | 'info' | 'success' | 'warning' | 'danger'

  /**
   * Why a call was refused (`error.code`):
   * - `permission_denied`: the plugin was not granted the permission the call needs.
   * - `not_allowed`: no plugin may use this command or topic.
   * - `invalid_argument`: an argument has the wrong shape. The message names the field.
   * - `invalid_tree`: `ui.render` got a tree that does not check. The message gives the path.
   * - `not_found`: no such page, surface or route.
   * - `limit`: too many pages, commands, status readouts, topics, surfaces, actions or hooks.
   * - `busy`: 16 control calls are already in flight, or a confirm or dialog is already open.
   * - `rate_limited`: over a per-minute budget (6 notifications, 120 storage writes,
   *   300 collection writes, 60 fetches, 120 bus messages), or 4 fetches already in flight.
   *   An over-budget call fails at once; nothing is queued.
   * - `too_large`: a value, clipboard text, file, document, fetch body or call is over its size limit.
   * - `plugin_storage_full`: storage would go over 64 keys or 64 KiB, or collections over 2 MiB.
   * - `needs_gesture`: a call that needs the operator (`ui.confirm`, `ui.dialog`,
   *   `clipboard.writeText`, `files.open`, `files.save`) came more than 10 s after the
   *   operator last used the plugin.
   * - `conflict`: a command shortcut is already bound, or another plugin holds the layout.
   * - `not_allowed` (fetch): the URL is not `https`, has a port other than 443, or its host is not in `network.hosts`.
   * - `blocked_address` (fetch): the host resolved to a loopback, private, link-local, CGNAT,
   *   multicast or unspecified address, `::/8`, `2002::/16` or an IPv4-mapped form of one.
   * - `missing_secret` (fetch): a `{{secret:KEY}}` placeholder names a secret with no value.
   * - `invalid_plugin` (fetch): a malformed request: an unknown method, `timeoutMs` out of range,
   *   more than 32 headers, a header over 8 KiB, a bad header name, or a line break in a value.
   * - `timeout` / `network` (fetch): the request took too long, or the connection failed.
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
    | 'blocked_address'
    | 'missing_secret'
    | 'invalid_plugin'
    | 'timeout'
    | 'network'
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
    /**
     * The current value of `key`, or `undefined` if the package declares no such setting.
     * A `secret` setting is always `undefined`: its value never reaches the script.
     */
    get(key: string): SettingValue | undefined
    /** Every setting's current value, by key, without secrets. The object is frozen and replaced on each change. */
    all(): Readonly<Record<string, SettingValue>>
    /**
     * 1.0.66: the keys of the `secret` settings the operator has filled in — names only, never values.
     * Use one in `net.fetch` as `{{secret:KEY}}`.
     */
    readonly secrets: readonly string[]
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

  /** Where a slot sits. `overview` widgets, `activity`/`provider` inspector tabs, `settings` sections. */
  type SlotScope = 'overview' | 'activity' | 'provider' | 'settings'
  /** What a slot is shown for. `id` is the request id (`activity`) or the provider id (`provider`). */
  interface SlotContext { readonly scope: SlotScope; readonly id?: string }

  /**
   * What a page, a slot surface or a dialog sends to `ui.onEvent` handlers. The object is frozen.
   * A click on a registered action or header button arrives here too (1.0.66): `kind: 'action'`,
   * `action` is the action's id, `scope` says where it was clicked, `target` names the row, and
   * `pageId` is `""`.
   */
  interface PageEvent {
    /**
     * - `action`: a button or a table row was pressed, Enter was pressed in a field that has `action`,
     *   or a registered action or header button was clicked.
     * - `change`: a field committed a new value. `select`, `segmented`, `toggle`, `tabs`, `slider` and
     *   `color` commit at once; `input` and `textarea` commit on blur, and on Enter (Ctrl/⌘+Enter in a
     *   textarea) when they have no `action`.
     * - `show` and `hide`: the page or surface was opened or left. An inspector tab's `show` carries
     *   `context`, and it comes again when the inspector moves to another request or provider.
     */
    readonly kind: 'action' | 'change' | 'show' | 'hide'
    /**
     * Where the event came from: a page id, a surface id (`widget:<id>`, `inspector:<id>`,
     * `settings:<id>`), a dialog (`dialog:<handle id>`), or `""` for a registered action.
     */
    readonly pageId: string
    /** The `action` of the button, field or table (`rowAction`), or the id of a registered action. */
    readonly action?: string
    /** The `name` of the field that changed, or of the field where Enter was pressed. */
    readonly name?: string
    /** The new value of that field. A `slider` gives a number. */
    readonly value?: string | number | boolean
    /** For a table `rowAction`: the row's `id`, or its index when it has none. */
    readonly row?: string | number
    /** 1.0.66: for an inspector tab, what it is showing. */
    readonly context?: SlotContext
    /** 1.0.66: for a registered action, where it was clicked. */
    readonly scope?: 'activity' | 'provider' | 'key' | 'header'
    /** 1.0.66: for a registered action: the request, provider or key id. Absent for `header`. */
    readonly target?: string
    /**
     * The current value of every named field on the page. A toggle gives a boolean and a slider a
     * number. Every other field gives a string, and that includes `input` with `kind: 'number'`,
     * so convert it with `Number(...)`.
     */
    readonly form: Readonly<Record<string, string | number | boolean>>
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
   * 1.0.66: commands that need `relay:rules` (the same calls as `switchboard.rules`). The
   * Switchboard records the plugin as the rule's owner. `rules.reorder*` stays with the operator.
   */
  type RulesMethod =
    | 'rules.list' | 'rules.stats' | 'rules.test' | 'rules.recentErrors'
    | 'rules.saveRequestRule' | 'rules.deleteRequestRule' | 'rules.saveRetryRule' | 'rules.deleteRetryRule'

  /**
   * Every control-plane command a plugin can send. These are refused with `not_allowed`:
   * `plugins.*`, `system.*`, `backup.*`, `relayhooks.*`, `rules.reorder*`, `appearance.update`,
   * `updates.install`, and any command not listed here.
   */
  type ControlMethod = ReadMethod | WriteMethod | SensitiveMethod | RulesMethod

  /** Live topics. Each one needs `control:read`, except `rules.changed`, which needs `relay:rules`. */
  type Topic =
    | 'activity.changed' | 'appearance.changed' | 'clients.changed' | 'codex.changed'
    | 'guardrails.changed' | 'guardrails.finding' | 'keys.changed' | 'models.tested'
    | 'notifications.raised' | 'providers.changed' | 'providers.health' | 'relay.changed'
    | 'routes.changed' | 'settings.changed' | 'tunnel.changed' | 'updates.installProgress'
    | 'updates.stateChanged' | 'rules.changed'

  /**
   * Pages of the window that `navigate` accepts. `settings` works only where the Settings page is
   * shown, and `rules` (1.0.66) only where the sidecar offers rules.
   */
  type BuiltInRoute =
    | 'overview' | 'activity' | 'insights' | 'providers' | 'keys' | 'routes' | 'rules' | 'tests'
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

  /**
   * 1.0.66. Formatted text, at most 20000 characters: headings, emphasis, code, lists and links
   * (links follow the `link` node's rules). It is drawn as elements, never as HTML, so raw HTML
   * shows as text.
   */
  interface MarkdownNode extends NodeBase { readonly type: 'markdown'; readonly text: string }
  /**
   * 1.0.66. An embedded picture: `src` is a `data:image/(png|jpeg|gif|webp);base64,` URL of at most
   * 256 KiB. Remote URLs are refused. `alt` is required. `width`/`height` are pixels.
   */
  interface ImageNode extends NodeBase { readonly type: 'image'; readonly src: string; readonly alt: string; readonly width?: number; readonly height?: number }
  /** 1.0.66. A range control. `max` must be above `min`, `step` above 0. Its form value is a number. */
  interface SliderNode extends NodeBase {
    readonly type: 'slider'; readonly name: Name; readonly label: string
    readonly min: number; readonly max: number; readonly step: number; readonly value: number; readonly disabled?: boolean
  }
  /** 1.0.66. A colour picker. `value` is `#rrggbb`; so is its form value. */
  interface ColorNode extends NodeBase { readonly type: 'color'; readonly name: Name; readonly label: string; readonly value: string; readonly disabled?: boolean }
  /** 1.0.66. A section that folds open and shut. It starts shut unless `open` is true. */
  interface DetailsNode extends NodeBase { readonly type: 'details'; readonly summary: string; readonly open?: boolean; readonly children?: readonly Child[] }
  /**
   * 1.0.66. A drawing surface, painted with `ui.draw(surfaceId, id, ops)`. Needs `ui:pages` like
   * any node.
   * - `id` is a {@link Name}, unique on the surface. `width`/`height` are 1–2048 canvas units.
   * - `label` is required: screen readers announce it.
   * - `keys`: while the canvas has focus, plain keys and Shift+key go to `ui.onInput` and do
   *   nothing else. Tab, Ctrl/Alt/⌘ chords and the safe-mode chord always reach the window.
   * - `pointer`: pointer and wheel events go to `ui.onInput` (moves at most 60 a second).
   * - `frames`: `ui.onFrame` runs while this canvas is visible.
   * - `fit`: `contain` (the default) scales the canvas down to fit, keeping its shape; `none`
   *   draws it at its own size.
   * Re-rendering the tree with the same canvas `id` keeps its picture.
   */
  interface CanvasNode extends NodeBase {
    readonly type: 'canvas'; readonly id: Name; readonly width: number; readonly height: number; readonly label: string
    readonly keys?: boolean; readonly pointer?: boolean; readonly frames?: boolean; readonly fit?: 'contain' | 'none'
  }

  /** One node of a plugin page. Unknown `type` values and unknown fields are refused. */
  type UiNode =
    | StackNode | RowNode | GridNode | PanelNode | HeadingNode | TextNode | MetricNode | MetricsNode
    | BadgeNode | DotNode | TableNode | ListNode | CodeNode | KvNode | ButtonNode | InputNode | TextareaNode
    | SelectNode | SegmentedNode | ToggleNode | TabsNode | ProgressNode | ChartNode | CalloutNode
    | DividerNode | EmptyNode | LinkNode
    | MarkdownNode | ImageNode | SliderNode | ColorNode | DetailsNode | CanvasNode

  // ───────────────────────────── canvas ─────────────────────────────

  /**
   * A colour: `#rgb`, `#rgba`, `#rrggbb`, `#rrggbbaa`, `rgb(…)`/`rgba(…)`, or `token:<name>` for a
   * {@link ThemeTokens} entry (for example `token:accent`), so the picture follows the theme.
   */
  type Color = string

  /**
   * One drawing instruction. A draw is at most 10000 ops and 512 KiB of JSON. An unknown op or
   * field rejects the whole draw (it is logged). `save`/`restore` keep and bring back the
   * transform and alpha.
   */
  type DrawOp =
    | { readonly op: 'clear'; readonly color?: Color }
    | { readonly op: 'rect'; readonly x: number; readonly y: number; readonly w: number; readonly h: number; readonly fill?: Color; readonly stroke?: Color; readonly lineWidth?: number; readonly radius?: number }
    | { readonly op: 'line'; readonly x1: number; readonly y1: number; readonly x2: number; readonly y2: number; readonly stroke: Color; readonly lineWidth?: number }
    | { readonly op: 'circle'; readonly x: number; readonly y: number; readonly r: number; readonly fill?: Color; readonly stroke?: Color; readonly lineWidth?: number }
    /** `points` is x, y, x, y, …: at most 800 numbers. */
    | { readonly op: 'path'; readonly points: readonly number[]; readonly closed?: boolean; readonly fill?: Color; readonly stroke?: Color; readonly lineWidth?: number }
    /** `text` is at most 200 characters; `size` is 6–96. */
    | {
      readonly op: 'text'; readonly x: number; readonly y: number; readonly text: string; readonly color: Color; readonly size: number
      readonly align?: 'left' | 'center' | 'right'; readonly baseline?: 'top' | 'middle' | 'alphabetic' | 'bottom'
      readonly weight?: 'normal' | 'bold'; readonly font?: 'sans' | 'mono'
    }
    | { readonly op: 'save' }
    | { readonly op: 'restore' }
    | { readonly op: 'translate'; readonly x: number; readonly y: number }
    | { readonly op: 'scale'; readonly x: number; readonly y: number }
    /** Radians. */
    | { readonly op: 'rotate'; readonly angle: number }
    /** 0–1, multiplies everything drawn after it. */
    | { readonly op: 'alpha'; readonly value: number }

  /** What a canvas sends to `ui.onInput`. `surface` is the page or surface id; `canvas` is the node `id`. */
  type CanvasInput =
    | {
      readonly surface: string; readonly canvas: string; readonly kind: 'keydown' | 'keyup'
      /** `KeyboardEvent.key` (for example "ArrowLeft", "a", " "). */
      readonly key: string
      /** `KeyboardEvent.code`, the physical key (for example "KeyA", "Space"). */
      readonly code: string
      readonly repeat: boolean; readonly shift: boolean; readonly alt: boolean; readonly ctrl: boolean; readonly meta: boolean
    }
    | {
      readonly surface: string; readonly canvas: string; readonly kind: 'pointerdown' | 'pointerup' | 'pointermove' | 'wheel'
      /** In canvas units, whatever the canvas is scaled to on screen. */
      readonly x: number; readonly y: number
      readonly button: number; readonly buttons: number
      /** For `wheel`. */
      readonly deltaY?: number
    }
    | { readonly surface: string; readonly canvas: string; readonly kind: 'focus' | 'blur' }

  // ───────────────────────────── theme ─────────────────────────────

  /** The window's current colours, as CSS colour strings. */
  interface ThemeTokens {
    readonly background: string; readonly surface: string; readonly text: string; readonly muted: string; readonly border: string
    readonly accent: string; readonly success: string; readonly warning: string; readonly danger: string; readonly info: string
  }
  interface ThemeInfo { readonly mode: 'dark' | 'light'; readonly tokens: ThemeTokens }

  // ───────────────────────────── slots and dialogs ─────────────────────────────

  interface WidgetSpec {
    /** `^[a-z0-9][a-z0-9_-]{0,47}$`, unique within the plugin. */
    readonly id: string
    /** 1–48 characters, shown in the widget's frame. */
    readonly title: string
    /** Lower comes first. */
    readonly order?: number
    /** Span the whole Overview row. */
    readonly wide?: boolean
  }
  interface InspectorTabSpec {
    readonly id: string
    readonly title: string
    /** `activity`: the request inspector. `provider`: the provider inspector. */
    readonly scope: 'activity' | 'provider'
  }
  interface SettingsSectionSpec {
    readonly id: string
    readonly title: string
    /** At most 200 characters. */
    readonly description?: string
  }
  interface ActionSpec {
    /** `^[a-z0-9][a-z0-9_-]{0,47}$`, unique within the plugin. */
    readonly id: string
    /** The button label: 1–48 characters. */
    readonly title: string
    /** `activity`: the request inspector. `provider`: the provider inspector. `key`: an API key row. */
    readonly scope: 'activity' | 'provider' | 'key'
    readonly tone?: 'neutral' | 'danger'
    readonly icon?: Icon
  }
  interface HeaderButtonSpec {
    readonly id: string
    /** The tooltip and accessible name. */
    readonly title: string
    /** Header buttons show only their icon. */
    readonly icon: Icon
  }
  interface LayoutSpec {
    /** Built-in sidebar items to hide. `plugins` and `settings` are never hidden; unknown ids are ignored. */
    readonly hidden?: readonly BuiltInRoute[]
    /** Built-in sidebar items in the order you want them; the rest keep their place after these. */
    readonly order?: readonly BuiltInRoute[]
    /** The page the window opens on, unless the operator has already moved. */
    readonly startPage?: Route
  }

  interface DialogAction {
    /** A {@link Name}. It is `result.action` when the operator presses it. */
    readonly id: Name
    /** 1–24 characters. */
    readonly label: string
    /** The default is `neutral`. */
    readonly tone?: 'primary' | 'neutral' | 'danger'
  }
  interface DialogSpec {
    /** 1–120 characters, shown with the plugin's name. */
    readonly title: string
    readonly tree: UiNode
    /** The footer buttons. Any of them closes the dialog with its `id`. */
    readonly actions: readonly DialogAction[]
    /** The default is `medium`. */
    readonly size?: 'small' | 'medium' | 'large'
  }
  interface DialogResult {
    /** The pressed footer action, or `null` when the dialog was dismissed or closed by `close()`. */
    readonly action: string | null
    /** The dialog's named fields when it closed, like `PageEvent.form`. */
    readonly form: Readonly<Record<string, string | number | boolean>>
  }
  interface DialogHandle {
    /** Events from the dialog's tree arrive at `ui.onEvent` with `pageId: "dialog:<id>"`. */
    readonly id: string
    /** Replaces the dialog's tree, for wizards and live validation. */
    update(tree: UiNode): Promise<void>
    /** Closes the dialog; `result` resolves with `action: null`. */
    close(): Promise<void>
    /** Settles once, when the dialog closes. */
    readonly result: Promise<DialogResult>
  }

  interface ToneSpec {
    /** 40–4000 Hz. */
    readonly frequency: number
    /** 10–2000 ms. */
    readonly durationMs: number
    /** The default is `sine`. */
    readonly type?: 'sine' | 'square' | 'triangle' | 'sawtooth'
    /** 0–1 (the default is 1). The window scales it to at most 0.3. */
    readonly volume?: number
  }

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
     * Replaces the whole tree of a page or a slot surface (`widget:<id>`, `inspector:<id>`,
     * `settings:<id>`). Register it first (`not_found`).
     * A tree that does not check is refused with `invalid_tree`. The problem, with its path, is also
     * shown on the page. Needs `ui:pages` for a page and `ui:slots` for a surface.
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

    // ── 1.0.66: slots (`ui:slots`) ──
    // Surfaces are drawn with `render(surface, tree)` and send events like pages. The window frames
    // each one with "From <plugin name>". Slots and actions go when the plugin stops.

    /** An Overview widget. At most 4 (`limit`). Draw it with `render("widget:<id>", tree)`. */
    registerWidget(spec: WidgetSpec): Promise<{ readonly surface: string }>
    /**
     * A tab in the Activity request inspector or the Provider inspector. At most 4 (`limit`).
     * Its `show` event carries `context: { scope, id }`; render for that request or provider.
     */
    registerInspectorTab(spec: InspectorTabSpec): Promise<{ readonly surface: string }>
    /** A section under Settings → Plugins. At most 2 (`limit`). */
    registerSettingsSection(spec: SettingsSectionSpec): Promise<{ readonly surface: string }>
    /**
     * A button in an inspector or on an API key row. At most 8 (`limit`). A click arrives at
     * `onEvent` as `{ kind: 'action', action: id, scope, target }` and counts as a gesture.
     */
    registerAction(spec: ActionSpec): Promise<void>
    /** An icon button in the top runtime bar. At most 2 (`limit`). A click arrives with `scope: 'header'`. */
    registerHeaderButton(spec: HeaderButtonSpec): Promise<void>
    /** Removes a surface (`widget:<id>`, …) or an action or header button (its `id`). Unknown ids do nothing. */
    unregister(surfaceOrActionId: string): Promise<void>

    // ── 1.0.66: layout (`ui:layout`) ──

    /**
     * Hides or reorders built-in sidebar items and picks the start page. Only one plugin can hold the
     * layout (`conflict`). The operator can reset it on the Plugins page; it also resets when the
     * plugin stops and in safe mode.
     */
    setLayout(layout: LayoutSpec): Promise<void>
    /** Gives the layout back. */
    resetLayout(): Promise<void>

    // ── 1.0.66: dialogs (`ui:dialogs`) ──

    /**
     * Opens a modal dialog that holds a plugin tree, such as a form or a wizard. It names the plugin.
     * Needs a gesture (`needs_gesture`). One open at a time per plugin (`busy`).
     */
    dialog(spec: DialogSpec): Promise<DialogHandle>

    // ── 1.0.66: sound (`ui:sound`) ──

    readonly sound: {
      /** Plays one tone. Fire-and-forget: tones over 20 a second are dropped. */
      tone(spec: ToneSpec): void
    }

    // ── 1.0.66: theme (no permission) ──

    /** The window's current mode and colours. */
    theme(): Promise<ThemeInfo>
    /** Runs `handler` when the operator changes the theme. Returns a function that stops listening. */
    onTheme(handler: (theme: ThemeInfo) => void): () => void

    // ── 1.0.66: canvas (`ui:pages` or `ui:slots`, wherever the canvas sits) ──

    /**
     * Replaces the picture of a canvas node with `ops`. Fire-and-forget: a draw for a canvas that is
     * not on screen, or over the budget (240 draw, frame and input messages a second), is dropped.
     * Draws do not count toward the window's flood limit.
     */
    draw(surfaceId: string, canvasId: string, ops: readonly DrawOp[]): void
    /**
     * Runs `handler` once per display frame while a `frames: true` canvas of the plugin is visible.
     * `dtMs` is the time since the last frame and `tMs` a running clock. The next frame comes only
     * after the handler returns (or after 250 ms), so a slow handler slows the frame rate instead of
     * queueing frames. Returns a function that stops listening.
     */
    onFrame(handler: (dtMs: number, tMs: number) => void): () => void
    /**
     * Runs `handler` for keyboard, pointer and focus events of canvases with `keys`/`pointer`.
     * Key and pointer input counts as a gesture. Returns a function that stops listening.
     */
    onInput(handler: (event: CanvasInput) => void): () => void
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
     * Follows a live topic. Each topic needs `control:read` (`rules.changed` needs `relay:rules`); a
     * topic that is not listed gives `not_allowed`.
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

  // ───────────────────────────── 1.0.66: data ─────────────────────────────

  /**
   * A named set of JSON documents (`storage`). Every plugin holds at most 16 collections and 2 MiB in
   * all (`plugin_storage_full`). Encrypted on disk in the plugin's own file, removed with the plugin.
   * - Collection names match `^[a-z0-9][a-z0-9_-]{0,31}$`.
   * - Document ids match `^[A-Za-z0-9._:-]{1,128}$`.
   * - A document is any JSON value except `null` (`invalid_argument`), at most 64 KiB (`too_large`).
   * - At most 300 writes a minute (`rate_limited`).
   */
  interface Collection<T = unknown> {
    /** The document, or `null`. */
    get(id: string): Promise<T | null>
    /** Inserts or replaces the document. `null` is refused: use `delete`. */
    put(id: string, doc: T): Promise<void>
    /** Resolves `true` if there was a document to delete. */
    delete(id: string): Promise<boolean>
    /**
     * Documents ordered by id. `limit` is 1–200 (the default is 50). When there are more,
     * pass `next` back as `after`. A page can hold fewer than `limit` documents and still have
     * `next` (the message byte budget), so loop on `next`, not on the count.
     */
    list(options?: { readonly prefix?: string; readonly after?: string; readonly limit?: number }): Promise<{ readonly items: readonly { readonly id: string; readonly doc: T }[]; readonly next?: string }>
    /** Removes every document of this collection and resolves how many there were. */
    clear(): Promise<number>
    /** Usage of every collection of the plugin. */
    stats(): Promise<CollectionStats>
  }
  interface CollectionStats {
    readonly collections: readonly { readonly name: string; readonly count: number; readonly bytes: number }[]
    readonly bytes: number
    readonly limitBytes: number
  }
  interface DbApi {
    /** Opens a collection (no call is made until you use it). */
    collection<T = unknown>(name: string): Collection<T>
  }

  /** `files`. Both calls need a gesture (`needs_gesture`). */
  interface FilesApi {
    /**
     * Lets the operator pick one text file. Resolves `null` when they cancel.
     * `accept` lists extensions such as `['.json', '.txt']`. `maxBytes` is at most 2 MiB (the
     * default); a bigger file gives `too_large`. The text is read as UTF-8.
     */
    open(options?: { readonly accept?: readonly string[]; readonly maxBytes?: number }): Promise<{ readonly name: string; readonly text: string; readonly size: number } | null>
    /**
     * Offers `text` as a download named `name`. At most 5 MiB (`too_large`). Resolves `false`
     * when the operator cancels.
     */
    save(file: { readonly name: string; readonly text: string; readonly mime?: string }): Promise<boolean>
  }

  /**
   * `bus`: messages between plugins. A channel matches `^[a-z0-9][a-z0-9._:-]{0,63}$`; a payload is
   * JSON of at most 32 KiB. At most 120 publishes a minute (`rate_limited`). Only other plugins
   * hear a message: the sender never gets its own.
   */
  interface BusApi {
    publish(channel: string, payload: unknown): Promise<void>
    /** `from` is the sending plugin's id. Returns a function that stops listening. */
    subscribe<T = unknown>(channel: string, handler: (payload: T, from: string) => void | Promise<void>): () => void
  }

  // ───────────────────────────── 1.0.66: network ─────────────────────────────

  interface FetchInit {
    /** The default is `GET`. */
    readonly method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
    /**
     * Request headers: at most 32, each at most 8 KiB. `{{secret:KEY}}` in a value is replaced by
     * that secret. Host, Content-Length, Connection, Keep-Alive, Cookie, Expect, User-Agent, Proxy-*,
     * Transfer-Encoding, Upgrade, TE, Trailer, X-Switchboard-* and X-Forwarded-* are dropped. The User-Agent is always `LuxurySwitchboard-Plugin/<appVersion> (<pluginId>)`.
     */
    readonly headers?: Readonly<Record<string, string>>
    /** At most 64 KiB. `{{secret:KEY}}` is replaced here too. */
    readonly body?: string
    /** 1000–30000 (the default is 15000). */
    readonly timeoutMs?: number
  }
  interface FetchResponse {
    readonly status: number
    /** `status` is 200–299. */
    readonly ok: boolean
    /** Lower-case names, at most 32. Secret values are replaced with `[secret]`. */
    readonly headers: Readonly<Record<string, string>>
    /** The body as UTF-8 text (at most 192 KiB). Secret values are replaced with `[secret]`. */
    readonly text: string
    /** `JSON.parse(text)`. */
    json<T = unknown>(): T
  }
  interface NetApi {
    /**
     * Sends one HTTPS request from the Switchboard (not from the window), so it needs no CORS.
     * Needs `net:fetch`.
     * - The URL is `https://`, port 443 or none, and its host is in the package's `network.hosts`
     *   (otherwise `not_allowed`). `{{secret:KEY}}` is **not** replaced in the URL.
     * - Hosts that resolve to loopback, private, link-local, CGNAT, multicast or unspecified
     *   addresses (including `::/8`, `2002::/16` and IPv4-mapped forms) are refused when
     *   connecting (`blocked_address`).
     * - Redirects are not followed: a 3xx comes back as it is, without `location`.
     * - The response body is at most 192 KiB (`too_large`).
     * - At most 60 requests a minute and 4 at once; a 5th concurrent request fails at once with
     *   `rate_limited`.
     * - Other failures: `missing_secret`, `invalid_plugin`, `timeout`, `network`.
     */
    fetch(url: string, init?: FetchInit): Promise<FetchResponse>
  }

  // ───────────────────────────── 1.0.66: relay rules ─────────────────────────────

  /** How the relay classifies a request path. */
  type Endpoint = 'responses' | 'chat' | 'messages' | 'completions' | 'embeddings' | 'images' | 'models' | 'other'

  /**
   * A request rule (`relay:rules`). Rules apply to the **local relay** only, never to the public
   * tunnel or model tests. They run in order; every enabled rule that matches adds its effects until one
   * has `stop`. `provider`, `model` and `block` take the first match. `tags` add up. `headers`
   * keep the first value per name. Every matching rate limit and budget is enforced: over one, the
   * caller gets `429` with `Retry-After`.
   */
  interface RequestRule {
    /** `^[a-z0-9][a-z0-9_-]{0,47}$`. Saving the same id replaces the rule. */
    readonly id: string
    /** 1–60 characters. */
    readonly name: string
    readonly enabled: boolean
    /** Set by the Switchboard: `""` for the operator, `plugin:<id>` for a plugin. A plugin may change only its own rules. */
    readonly owner?: string
    readonly match: {
      /** At most 16 globs (`*` is the wildcard, case-insensitive, each ≤ 200 characters). `[]` is any model. */
      readonly models: readonly string[]
      /** `[]` is any endpoint. */
      readonly endpoints: readonly Endpoint[]
    }
    readonly effects: RuleEffects
  }
  interface RuleEffects {
    /** Route to this provider id when it is present and enabled; otherwise route normally. */
    readonly provider?: string
    /** Rewrite the upstream model: 1–200 characters. */
    readonly model?: string
    /** Answer at once with `{"error":{"message":…,"type":"switchboard_rule"}}`. `message` is 1–200 plain-text characters. */
    readonly block?: { readonly status: 400 | 403 | 429 | 503; readonly message: string }
    /** `requests` 1–100000 per `windowSeconds` 1–86400. */
    readonly rateLimit?: { readonly requests: number; readonly windowSeconds: number }
    /**
     * Requests (1–10000000) and/or tokens (1–10000000000) per hour or per day. Windows reset on the local
     * hour or at local midnight. Counters live in memory and restart with the app.
     */
    readonly budget?: { readonly requests?: number; readonly tokens?: number; readonly period: 'hour' | 'day' }
    /**
     * Extra upstream headers: at most 8. Names match `^X-[A-Za-z0-9-]{1,62}$` and are never
     * `X-Switchboard-*`, `X-Forwarded-*`, `X-Real-IP`, `X-Stainless-*` or credential-like (auth, token, key, secret,
     * session, cookie, password, signature). A header already on the request is never replaced.
     * Values are at most 256 printable ASCII characters.
     */
    readonly headers?: Readonly<Record<string, string>>
    /** Up to 8 tags (`^[a-z0-9][a-z0-9_.-]{0,31}$`), shown and searchable in Activity. */
    readonly tags?: readonly string[]
    /** Stop evaluating later rules. */
    readonly stop?: boolean
  }

  /**
   * A custom retry rule (`relay:rules`). It applies to the local relay and to tunnel requests,
   * after the content-policy check, so a refusal is never retried. It is never consulted for 2xx answers.
   * The first matching rule wins; built-in retry handling runs when none matches. The built-in
   * patterns are never shown.
   */
  interface RetryRule {
    readonly id: string
    readonly name: string
    readonly enabled: boolean
    readonly owner?: string
    /** At least one of `statuses`, `contains` and `errorCodes` must be non-empty. */
    readonly match: {
      /** At most 16, each 400–599. `[]` is any error status. */
      readonly statuses: readonly number[]
      /** At most 8, each 2–120 characters, matched case-insensitively in the error text. */
      readonly contains: readonly string[]
      /** At most 8 relay error codes, such as `transport`, `stream_incomplete`, `upstream_status`. */
      readonly errorCodes: readonly string[]
      /** At most 16 provider ids. `[]` is any provider. */
      readonly providers: readonly string[]
      /** At most 16 model globs. `[]` is any model. */
      readonly models: readonly string[]
    }
    /**
     * - `retry`: try again.
     * - `rotate_key`: try again with the next API key of the provider.
     * - `switch_provider`: try the next provider (on the tunnel this acts as `retry`).
     * - `fail`: stop at once with the generic error.
     */
    readonly action: 'retry' | 'rotate_key' | 'switch_provider' | 'fail'
    /** 1–10 extra attempts this rule may spend on one request, within the relay's own ceiling. Ignored for `fail`. */
    readonly maxAttempts: number
    /** 0–60000. `0` uses the normal backoff (and honours `Retry-After`). */
    readonly delayMs: number
  }
  /** A failure group from recent activity. `detail` is already redacted. */
  interface RecentError {
    readonly signature: string
    readonly status: number
    readonly errorCode: string
    readonly detail: string
    readonly providerName: string
    readonly model: string
    readonly count: number
    readonly lastAt: string
    /** The custom retry rule that already handles it. */
    readonly coveredBy?: string
  }
  interface RuleStats {
    readonly hits: number
    readonly lastHitAt?: string
    readonly windowRequests: number
    readonly windowTokens: number
    readonly windowResetsAt?: string
  }
  interface RuleTestInput {
    readonly model: string
    /** A request path such as `/v1/chat/completions`. */
    readonly path: string
    readonly status?: number
    readonly errorText?: string
    readonly errorCode?: string
    readonly providerId?: string
  }
  interface RuleTestResult {
    /** Matching request rule ids, in order. */
    readonly requestRules: readonly string[]
    /** Their merged effects. */
    readonly effects: RuleEffects
    /** The retry rule that would handle the error, if any. */
    readonly retryRule?: string
  }
  /**
   * Needs `relay:rules`. The Switchboard records the plugin as each rule's owner. A plugin can change
   * or delete only its own rules (`permission_denied`). The operator can edit or disable any rule,
   * and the plugin's rules are deleted when the plugin is removed. At most 64 request rules and 64
   * retry rules in all (`limit`). Reordering is the operator's.
   */
  interface RulesApi {
    list(): Promise<{ readonly requestRules: readonly RequestRule[]; readonly retryRules: readonly RetryRule[] }>
    saveRequestRule(rule: RequestRule): Promise<RequestRule>
    deleteRequestRule(id: string): Promise<{ readonly deleted: true }>
    saveRetryRule(rule: RetryRule): Promise<RetryRule>
    deleteRetryRule(id: string): Promise<{ readonly deleted: true }>
    /** Which rules a request (or an error) would match. Nothing is sent. */
    test(input: RuleTestInput): Promise<RuleTestResult>
    /** Hit and budget counters, by rule id. */
    stats(): Promise<{ readonly rules: Readonly<Record<string, RuleStats>> }>
    /** Recent failures grouped by status, code and detail. `limit` is 1–100 (the default is 50). */
    recentErrors(limit?: number): Promise<{ readonly errors: readonly RecentError[] }>
  }

  // ───────────────────────────── 1.0.66: relay hooks ─────────────────────────────

  interface HookFilter {
    /** At most 16 model globs. Absent or `[]`: every model. */
    readonly models?: readonly string[]
    /** Absent or `[]`: every endpoint. */
    readonly endpoints?: readonly Endpoint[]
  }
  interface HookRequestInfo {
    readonly activityId: string
    readonly model: string
    readonly endpoint: Endpoint
    readonly method: string
    readonly path: string
    /** The body size in bytes. */
    readonly bytes: number
  }
  interface HookRequest extends HookRequestInfo {
    /** The request body as text, when it is at most 192 KiB. */
    readonly body?: string
    /** `true` when the body was too large to send; it cannot be rewritten. */
    readonly bodyOmitted?: true
  }
  type HookDecision =
    | { readonly action: 'continue' }
    /**
     * `body` (valid JSON, ≤ 192 KiB) replaces the request body; an invalid one is ignored.
     * `model` changes the routed model. `tags` (up to 8) are added to the activity record.
     */
    | { readonly action: 'rewrite'; readonly body?: string; readonly model?: string; readonly tags?: readonly string[] }
    /** Answers like a blocking request rule. */
    | { readonly action: 'block'; readonly block: { readonly status: 400 | 403 | 429 | 503; readonly message: string }; readonly tags?: readonly string[] }
  interface HookResponse {
    readonly status: number
    readonly errorCode?: string
    readonly latencyMs: number
    readonly tokens?: { readonly input: number; readonly output: number; readonly total: number }
    /** The answer, only when the relay buffered it and it is at most 192 KiB. */
    readonly body?: string
    readonly bodyOmitted?: true
  }
  /**
   * `relay:content`. Hooks see and change prompts and answers of the **local relay only**: never of
   * the public tunnel or model tests. At most 8 hooks run in the whole app, and 2 per plugin per
   * phase (`limit`). The plugin must be enabled and granted `relay:content`. Bodies never reach the logs.
   */
  interface RelayApi {
    /**
     * Runs `handler` before routing, for each matching request. Hooks run one after another in the
     * order they registered, inside a 5 s budget shared by all of them. `timeoutMs` is 100–5000
     * (the default is 2000). A hook that times out lets the request continue (fail-open); after 3
     * timeouts in a row it is removed. Resolves to a function that removes the hook.
     */
    onRequest(filter: HookFilter & { readonly timeoutMs?: number }, handler: (request: HookRequest) => HookDecision | Promise<HookDecision>): Promise<() => Promise<void>>
    /**
     * Runs `handler` after the client already has its answer, for each matching request. It cannot
     * change anything. Resolves to a function that removes the observer.
     */
    onResponse(filter: HookFilter, handler: (request: HookRequestInfo, response: HookResponse) => void | Promise<void>): Promise<() => Promise<void>>
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
    /** 1.0.66: document collections. Needs `storage`. */
    readonly db: DbApi
    /** 1.0.66: open and save files. Needs `files`. */
    readonly files: FilesApi
    /** 1.0.66: the message bus. Needs `bus`. */
    readonly bus: BusApi
    /** 1.0.66: HTTPS requests. Needs `net:fetch`. */
    readonly net: NetApi
    /** 1.0.66: request and retry rules. Needs `relay:rules`. */
    readonly rules: RulesApi
    /** 1.0.66: request hooks and response observers. Needs `relay:content`. */
    readonly relay: RelayApi
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
  /**
   * 1.0.66. A write-only value such as an API token or webhook URL. The operator enters it; the
   * script only sees its key in `settings.secrets` and uses it as `{{secret:KEY}}` in `net.fetch`.
   * No `default`. At most 8 per package; a value is at most 4096 characters. Secrets are encrypted,
   * never shown again, left out of backup exports, and removed with the plugin (or when a new
   * version drops the key).
   */
  interface SecretSetting extends SettingBase { readonly type: 'secret' }
  /** One operator-editable option. Up to 24 per package. A package with settings needs a `script`. */
  type Setting = TextSetting | NumberSetting | ToggleSetting | SelectSetting | SecretSetting

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
     * 1.0.66. Required exactly when `net:fetch` is declared. `hosts` lists 1–16 lowercase DNS names
     * (each ≤ 253 characters), optionally starting with `*.` for any subdomain — no scheme,
     * port, path or IP address. `localhost`, `*.local` and `*.internal` are refused.
     */
    readonly network?: { readonly hosts: readonly string[] }
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
