# Switchboard plugin reference

Every value here is enforced by the app. If anything differs, trust `docs/plugins/switchboard.d.ts`, `frontend/src/features/plugins/domain/*.ts` and `frontend/src/features/plugins/application/plugin-host.ts`. Lengths count code points (an emoji counts as 1). "Bytes" means UTF-8 bytes.

Contents: [Package](#package) · [Settings](#settings) · [Permissions](#permissions) · [Worker API](#worker-api) · [Gesture rule](#gesture-rule) · [Shortcuts](#shortcuts) · [Page tree](#page-tree) · [Page events](#page-events) · [Control plane](#control-plane) · [Storage](#storage) · [1.0.66 APIs](#1066-apis) · [Runtime budgets and stops](#runtime-budgets-and-stops) · [Error codes](#error-codes) · [Restyling the shell](#restyling-the-shell) · [Tooling](#tooling)

Everything marked **1.0.66** needs `"minAppVersion": "1.0.66"`. On an older app, a missing method fails as a TypeError, and an unknown node, permission or setting type is refused.

## Package

A package is one JSON object (schema 1). Any field not listed below gives `Unknown field "x".`. The validator reports every problem at once.

| Field | Rule | Message when wrong |
| --- | --- | --- |
| `schema` | `1` | `"schema" must be 1.` |
| `id` | `^[a-z0-9][a-z0-9.-]{1,62}[a-z0-9]$` | `"id" must be 3–64 characters of lowercase letters, digits, dots and dashes, starting and ending with a letter or digit.` |
| `name` | required, ≤ 64 | `"name" is required.` |
| `version` | `^[0-9A-Za-z.+-]{1,32}$` | `"version" must be 1–32 characters of letters, digits, dots, plus and minus signs (e.g. "1.2.0").` |
| `author` | ≤ 64, optional | |
| `description` | ≤ 280, optional, may hold line breaks | |
| `homepage` | `https://` only, ≤ 200 bytes, no credentials (`@`). It is shown as a link and never fetched. | `"homepage" must be an https:// address of at most 200 characters.` |
| `minAppVersion` | `^\d{1,4}\.\d{1,4}\.\d{1,4}$` | `"minAppVersion" must be a version such as "1.0.65".` |
| `permissions` | a list, possibly empty, with no duplicates, at most 24 | `"permissions" must be a list (it may be empty).` · `"permissions" lists more than 24 entries.` · `Unknown permission "x".` · `Permission "x" is listed twice.` |
| `network` (1.0.66) | `{ "hosts": [...] }`, present **exactly** when `net:fetch` is listed. 1–16 lowercase DNS names ≤ 253 characters, optionally `*.` + name (subdomains only). No scheme, port, path or IP; never `localhost`, `*.local` or `*.internal`. | `"net:fetch" needs "network": { "hosts": [...] } naming the hosts it may reach.` · `"network" applies only with the "net:fetch" permission.` · `"network.hosts" must list 1 to 16 hosts.` · `"network.hosts[i]" must be a bare host name, without a scheme, port or path.` · `… must be a lowercase DNS name such as "api.example.com" or "*.example.com".` · `… names this machine or the local network, which plugins cannot reach.` · `… must be a name, not an IP address.` · `… must have a domain, such as "example.com".` · `… repeats "h".` · `Unknown field "network.x".` |
| `settings` | see [Settings](#settings) | `"settings" need a "script" to read them.` |
| `theme` | `{ name ≤ 48, scheme: "dark"\|"light", tokens }`, needs `ui:theme` | `A theme needs the "ui:theme" permission.` · `"theme.scheme" must be "dark" or "light".` · `"theme.tokens" has more than 96 entries.` |
| `css` | ≤ 64 KiB, needs `ui:css`, no `@import` or `</style>` | `A stylesheet needs the "ui:css" permission.` · `"css" may not contain @import or </style>.` |
| `script` | ≤ 128 KiB. Every permission except `ui:theme` and `ui:css` needs one. | `"ui:pages", "storage" need a "script".` (or `"x" needs a "script".` for one) |

- **Tokens:** at most 96. Each name matches `^--[a-z][a-z0-9-]{0,47}$`. Each value is at most 120 bytes and contains none of the following:
  - `; { } < > \` or backticks;
  - control characters;
  - `url(`, `expression(`, `@import` or `javascript:`.
- **Whole package:** the stored JSON must fit the 256 KiB frame, so the payload is at most 258048 bytes. Otherwise you get `This package is too large to load back`.
- **Count:** at most 24 plugins can be installed.
- **Bad JSON:** a file that does not parse gives `The file is not valid JSON.`.
- **`minAppVersion`:**
  - Installing on an older app fails with `invalid_plugin` "Plugin minAppVersion needs Luxury Switchboard X or later".
  - A stored plugin that is too new stays off with "Needs Luxury Switchboard X or later; this is Y. Update the app or install an older version of the plugin."
  - An empty app version (dev builds) passes.
- **Backend install codes:**

| Code | Meaning |
| --- | --- |
| `invalid_plugin` | The package failed a check. |
| `plugin_not_found` | "Plugin is not installed" |
| `plugin_limit` | "At most 24 plugins can be installed" |
| `plugin_too_large` | The package is over the size limit. |
| `plugin_storage_full` | "Plugin storage is full" |
| `secure_storage_unavailable` | Secure storage cannot be reached. |
| `plugin_save_failed` | The package could not be saved. |

## Settings

The operator edits settings from **Settings** on the plugin's row. The install review shows "N settings you can change". The plugin cannot write them.

- **Count:** at most 24.
- **`key`:** `^[a-z][A-Za-z0-9_]{0,31}$`, unique.
- **`label`:** 1–64 characters.
- **`description`:** ≤ 200 characters.
- **Control characters:** not allowed in any string.

| `type` | `default` | Extra fields (nothing else is allowed) |
| --- | --- | --- |
| `text` | string, one line, ≤ `maxLength` | `placeholder` ≤ 64; `maxLength` a whole number 1–256 (the default is 256) |
| `number` | finite number within `min`/`max` | `placeholder` ≤ 64; `min`; `max` (`min` ≤ `max`) |
| `toggle` | boolean | none |
| `select` | one of the option values | `options`: 1–32 of `{ value ≤ 64, label ≤ 64 }`, with unique values |
| `secret` (1.0.66) | **none**: `"settings[i].default" does not apply to a secret setting.` | none. At most 8 per package (`"settings" has more than 8 secret entries.`). |

**Secrets (1.0.66):**

- **Values:** 1–4096 characters, with no line breaks or control characters.
- **The operator's form** is write-only: *Set* / *Not set*, with **Replace** and **Clear**. The value is never shown again.
- **The script** never sees the value:
  - `switchboard.settings.secrets` is a frozen list of the keys that are set;
  - `get`/`all` leave secrets out.
- **Using a secret:**
  - write `{{secret:key}}` in a `net.fetch` header value or body;
  - never in the URL (`not_allowed`);
  - an unset key gives `missing_secret`;
  - an echoed value comes back as `[secret]`.
- **Storage:** encrypted; left out of backup exports; removed with the plugin, or when an upgrade drops the key.

**Validator messages:**

- `"settings" must be a list.`
- `"settings" has more than 24 entries.`
- `"settings[i]" must be an object with key, label, type and default.`
- `"settings[i].key" repeats "k".`
- `"settings[i].type" must be text, number, toggle or select.`
- `"settings[i].min" does not apply to a text setting.`
- `"settings[i].default" is required.`
- `"settings[i].default" must be one of the option values.`
- `"settings[i].min" is larger than "settings[i].max".`
- `"settings[i].options" must list 1 to 32 choices.`
- `"settings[i].options[j].value" repeats "v".`
- `"settings[i].maxLength" must be a whole number from 1 to 256.`
- `"<path>" is required.`
- `"<path>" is longer than N characters.`
- `"<path>" contains control characters.`
- `"<path>" must be a string.`
- `"<path>" must be a number.`
- `Unknown field "settings[i].x".`

**How values are stored and delivered:**

- **Storage:** each value is stored as JSON under the storage key `setting:<key>`. A value equal to the default is removed. A stored value that no longer validates reads as the default.
- **The operator's form** refuses bad input with one of these messages:
  - "Use at most N characters, without line breaks."
  - "Enter a number from A to B" (or "of at least A" / "of at most B")
  - "Choose on or off."
  - "Choose one of the options."
- **In the script:**
  - `switchboard.settings.get(key)` returns the value, or `undefined` for an undeclared key.
  - `settings.all()` returns a frozen object.
  - `settings.onChange((values, changed) => …)` fires only when a value really changed. It returns an unsubscribe function.
  - The values are already there when the script starts.

## Permissions

| Permission | Risk | Default | Calls it unlocks |
| --- | --- | --- | --- |
| `ui:theme` | low | on | `theme` field (no script needed) |
| `ui:css` | medium | on | `css` field (no script needed) |
| `ui:pages` | low | on | `ui.registerPage`, `ui.unregisterPage`, `ui.render`, `ui.setBadge` |
| `ui:status` | low | on | `ui.setStatus` |
| `ui:commands` | low | on | `commands.register`, `commands.unregister` |
| `ui:notify` | low | on | `notify`, `ui.confirm` |
| `ui:clipboard` | low | on | `clipboard.writeText`. It writes only and can never read the clipboard. |
| `control:read` | medium | on | read commands and every topic |
| `control:write` | high | on | write commands |
| `control:sensitive` | high | **off** | credentials, settings, tunnel and Codex sign-in |
| `storage` | low | on | `storage.*`, and (1.0.66) `db.collection(…)` |
| `ui:slots` (1.0.66) | low | on | `ui.registerWidget`, `registerInspectorTab`, `registerSettingsSection`, `registerAction`, `registerHeaderButton`, `unregister`, `render` of a surface |
| `ui:layout` (1.0.66) | low | on | `ui.setLayout`, `ui.resetLayout` |
| `ui:dialogs` (1.0.66) | low | on | `ui.dialog` |
| `ui:sound` (1.0.66) | low | on | `ui.sound.tone` |
| `files` (1.0.66) | medium | on | `files.open`, `files.save` |
| `bus` (1.0.66) | low | on | `bus.publish`, `bus.subscribe` |
| `net:fetch` (1.0.66) | high | **off** | `net.fetch`; needs `network.hosts` |
| `relay:rules` (1.0.66) | high | on | `rules.*` and the `rules.changed` topic |
| `relay:content` (1.0.66) | high | **off** | `relay.onRequest`, `relay.onResponse` |

- **Warnings:** `control:sensitive`, `net:fetch` and `relay:content` show a warning on the review screen, and the operator must tick them on purpose.
- **Canvas:** a `canvas` node, `ui.draw`, `ui.onFrame` and `ui.onInput` need only `ui:pages`, or `ui:slots` on a slot surface.
- **No permission needed:** `navigate`, `settings.*`, `ui.onEvent`, `has`, `onActivate`, `log` and `console`. In 1.0.66 the same holds for `ui.theme` and `ui.onTheme`.
- **Missing grant:** gives `permission_denied` `This needs the "<perm>" permission, which the plugin was not granted.`, and the warning is logged.
- **Upgrades:** an upgrade keeps the previous grant decisions. Permissions that are new in the upgrade start at their defaults.

## Worker API

All calls return Promises. Arguments must be plain JSON data; otherwise the call throws a TypeError, `Arguments must be plain JSON data: …`, before anything is sent. Except for `ui.render`, a call's arguments are at most 300 KiB. A bigger call fails with `too_large` "The arguments are N KiB; a call carries at most 300 KiB."

| Call | Limits | Errors |
| --- | --- | --- |
| `ui.registerPage({ id, title, icon? })` → `{ route }` | `id` `^[a-z0-9][a-z0-9_-]{0,47}$`; `title` 1–48; `icon` `^[a-z]{1,24}$`; at most 8 pages. Re-registering keeps the tree and the badge. | `limit` "A plugin can register at most 8 pages." · "icon must be one of the documented icon names." |
| `ui.unregisterPage(id)` | An unknown id does nothing. | |
| `ui.render(pageId, tree)` | See [Page tree](#page-tree). The whole page is replaced. | `not_found` `Register page "x" before rendering it.` · `invalid_tree` `<path> <problem>.` (also shown on the page; the previous tree stays) |
| `ui.onEvent([pageId,] handler)` → unsubscribe | | |
| `ui.setStatus(id, { text, tone?, tooltip? } \| null)` | `id` `^[a-z0-9][a-z0-9_.-]{0,47}$`; `text` 1–40; `tooltip` ≤ 200 (the host appends "\n— <plugin name>"); at most 3 readouts. | `limit` "A plugin can show at most 3 status readouts." · "status must be { text, tone?, tooltip? } or null." |
| `ui.setBadge(pageId, { text, tone? } \| null)` | `text` 1–8 characters on one line. Setting the same badge again does nothing. | `not_found` `Register page "x" before giving it a badge.` · "badge must be { text, tone? } or null." · "badge.text must be one line." |
| `ui.confirm({ title, body?, confirmText?, tone? })` → boolean | `title` 1–120; `body` ≤ 1000; `confirmText` 1–24 (the default is "Continue"); `tone` `neutral` or `danger`. It needs a [gesture](#gesture-rule), and only one confirm can be open at a time. | `needs_gesture` · `busy` "The plugin is already asking the operator something." · "tone must be neutral or danger." |
| `clipboard.writeText(text)` | ≤ 32768 characters. It needs a [gesture](#gesture-rule). | "text must be a string." · `too_large` "Clipboard text is at most 32768 characters." · `failed` "The clipboard refused the text: …" · `needs_gesture` |
| `commands.register({ id, title, group?, keywords?, shortcut? }, handler)` → `{ id: "plugin:<pluginId>:<id>" }` | `id` `^[a-z0-9][a-z0-9_.-]{0,47}$`; `title` 1–80; `group` 1–32; up to 8 `keywords` of 1–32 characters each; at most 32 commands. The palette group is "<plugin name> · <group>" (or just the plugin name), and the search words gain `plugin` and the plugin name. Registering an id again replaces the command; if that is refused, the old handler stays. | `limit` "A plugin can register at most 32 commands." · "keywords must be up to 8 words of at most 32 characters." · `invalid_argument` `shortcut: <problem>` · `conflict` `<shortcut> is already bound to "<title>" (<group>).` |
| `commands.unregister(id)` | It also releases the shortcut. | |
| `notify({ title, body?, tone? })` | `title` 1–120; `body` ≤ 1000 (the host appends "\n— <name>"); `tone` defaults to `info`; at most 6 a minute. | `rate_limited` "A plugin can raise at most 6 notifications a minute." |
| `control.call(method, payload?)` | See [Control plane](#control-plane). At most 16 calls in flight. | `busy` "A plugin can have at most 16 control calls in flight." |
| `events.on(topic, handler)` → `Promise<unsubscribe>` | At most 24 topics. | `limit` "A plugin can follow at most 24 topics." |
| `storage.*` | See [Storage](#storage). | |
| `navigate(route)` | A leading `#/` is ignored, and the route is ≤ 160 characters. See the list of routes below. | `not_found` `"<route>" is not a page in this window.` |
| `log.*`, `console.*` | At most 100 lines per 5 s; each line is cut at 2000 characters; the last 200 lines are kept. | Extra lines are dropped, and the log reports "N log lines were dropped (more than 100 in 5 seconds)." |
| `plugin`, `app`, `has(perm)`, `onActivate(fn)` | `app.version` is, for example, "1.0.65". `app.capabilities` lists what the sidecar advertises. | |
| `settings.secrets` (1.0.66) | A frozen list of the secret keys the operator has set. Values never reach the script. | |
| 1.0.66: `ui.register*`, `ui.unregister`, `ui.setLayout`, `ui.resetLayout`, `ui.draw`, `ui.onFrame`, `ui.onInput`, `ui.dialog`, `ui.sound.tone`, `ui.theme`, `ui.onTheme`, `db.collection`, `files.*`, `bus.*`, `net.fetch`, `rules.*`, `relay.onRequest`, `relay.onResponse` | See [1.0.66 APIs](#1066-apis). `ui.draw`, `ui.sound.tone` and `bus.publish` failures are logged, not thrown, except for permission errors. `onFrame`, `onInput`, `onTheme` and `bus.subscribe` return a sync unsubscribe. | |

- **Field messages:**
  - `<field> must be a non-empty string.`
  - `<field> is longer than N characters.`
  - `<field> must be one of neutral, info, success, warning, danger.`
  - `id must be a page id: …` and `id must be a command id: …`
- **Errors in handlers:** an uncaught error in any handler counts as a *fault*. It is logged with its stack, and the plugin keeps running. If a command handler throws, the operator also gets the toast "<title> failed" (once per run).
- **Routes:**
  - `overview`, `activity`, `insights`, `providers`, `keys`, `routes`, `tests`, `guardrails`, `tunnel`, `clients`, `appearance`, `plugins`;
  - `settings`, only where that page is shown;
  - `plugin/<pluginId>/<pageId>`.

## Gesture rule

`ui.confirm` and `clipboard.writeText` work only within **10 seconds** after the operator acted on the plugin. Since 1.0.66 the same holds for `ui.dialog`, `files.open` and `files.save`. Each of these counts as acting on it:

- any page event, including `show` and `hide`, and any slot-surface or dialog event;
- running one of the plugin's commands, from the palette or by its shortcut;
- answering a confirm or a dialog;
- (1.0.66) clicking one of its registered actions or header buttons;
- (1.0.66) key or pointer input on its canvas.

Outside that window you get `needs_gesture`, with one of these messages:

- "Asking the operator only works within 10 seconds of the operator using the plugin's page or command."
- "Writing to the clipboard only works within 10 seconds of the operator using the plugin's page or command."

Call these two only from `ui.onEvent` or command handlers, never from timers or topic handlers.

## Shortcuts

- **Forms:** `Alt+Key`, `Alt+Shift+Key`, `Ctrl+Alt+Key`, `Ctrl+Alt+Shift+Key` and `Ctrl+Shift+Key`.
- **Key:** `A`–`Z`, `0`–`9` or `F1`–`F12`.
- **Spelling:** any case and order is accepted, and the shortcut is stored in the order Ctrl+Alt+Shift+Key.
- **Matching:**
  - Keys are matched by physical position, so a shortcut works the same in every keyboard layout.
  - On macOS, Ctrl also matches ⌘.
  - AltGr never fires a shortcut.
  - Shortcuts do not fire inside modal dialogs or on key repeat.
- **Reserved:** `Ctrl+Alt+Shift+S` (safe mode), `Alt+F4`, `Alt+1`…`Alt+9`, `Ctrl+Shift+C/I/J/N/Q/R/T/V/W/Z`.

The problems that follow `shortcut: `:

- `Write a shortcut as Alt+Key, Ctrl+Shift+Key or Ctrl+Alt+Key, where Key is A-Z, 0-9 or F1-F12.`
- `"<p>" is not a modifier; use Ctrl, Alt or Shift.`
- `"<p>" is listed twice.`
- `"<k>" is not a key a shortcut can use; use A-Z, 0-9 or F1-F12.`
- `A shortcut needs Alt, or Ctrl with Shift, so typing and editing keys stay free: …`
- `<shortcut> is reserved by the Switchboard.`

## Page tree

A tree is one node. **Node fields are strict:** an unknown field on any node, item, column, option or tab is refused, with a message like `tree.children[2].varient is not a field of a "button" node.`. Every node also takes `type` and an optional `key` (≤ 64 characters), which keeps the node's state stable while its siblings change. `null` and `false` children are skipped, so `cond ? node : null` works. Text is always plain text; there is no HTML anywhere.

| Node | Fields (default) |
| --- | --- |
| `stack` | `gap` (`md`), `children` |
| `row` | `gap` (`md`), `align` `start`/`center`/`end`/`between` (`center`), `wrap` (`true`), `children` |
| `grid` | `columns` 1–6 (`2`, collapses on narrow windows), `gap` (`md`), `children` |
| `panel` | `title`, `subtitle`, `actions` (nodes in the header), `children` |
| `heading` | `text`, `level` 1–3 (`2`) |
| `text` | `text` ≤ 4096, `tone` `default`/`muted`/`neutral`/`info`/`success`/`warning`/`danger` (`default`), `mono`, `size` `sm`/`md`/`lg`, `weight` `regular`/`strong` |
| `metric` | `label`, `value` (scalar), `detail`, `tone` `success`/`warning`/`danger` |
| `metrics` | `items` ≤ 12 of `{ label, value, detail?, tone? }` |
| `badge` | `text`, `tone` (`neutral`) |
| `dot` | `tone`, `text` |
| `table` | `columns` 1–16 of `{ key, label?, align left/right, mono, width (number, "Npx", "N%" or "auto"), format text/number/percent/bytes/duration }`; `rows` ≤ 500; `empty`; `rowAction` |
| `list` | `items` ≤ 500 of `{ title, detail?, meta?, tone? }`, `empty` |
| `code` | `text` ≤ 32 KiB, monospace and scrollable |
| `kv` | `items` ≤ 64 of `{ key, value }` |
| `button` | `text`, `action`, `variant` `default`/`primary`/`danger`/`ghost`, `disabled`, `icon` |
| `input` | `name`, `label`, `hint`, `error`, `required`, `disabled`, `value`, `placeholder`, `kind` `text`/`number`/`search`, `maxLength` 1–4096 (`256`), `action`, `min`/`max`/`step` |
| `textarea` | the same as `input` without `kind`/`min`/`max`/`step`, plus `rows` 2–12 (`4`); `maxLength` defaults to `1000` |
| `select` | `name`, `label`, `value` (the first option), `options` ≤ 100 (strings or `{ value, label }`), `disabled` |
| `segmented` | like `select`, with at most 6 options |
| `toggle` | `name`, `label`, `value` (boolean), `disabled` |
| `tabs` | `name`, `label?`, `value` (the open tab id), `tabs` 1–8 of `{ id, label, children }` with unique ids |
| `progress` | `value` 0–1 (clamped), `indeterminate`, `label`, `tone` (`info`) |
| `chart` | `kind` `line`/`bars`, `label`, `values` 1–200 finite numbers, `labels` (one per value), `min`/`max` (`max` > `min`), `tone` (`info`), `height` 48–240 (`96`) |
| `callout` | `tone` (`info`), `title`, `text`, `actions` |
| `divider` | — |
| `empty` | `title`, `hint` |
| `link` | `text`, `href` (`https://` without credentials; it opens in the system browser) |
| `markdown` (1.0.66) | `text` ≤ 20000: headings, emphasis, code, lists and links (link rules apply). Drawn as elements; raw HTML shows as text. |
| `image` (1.0.66) | `src` a `data:image/(png\|jpeg\|gif\|webp);base64,` URL ≤ 256 KiB (no remote URLs), `alt` (required), `width`, `height` |
| `slider` (1.0.66) | `name`, `label`, `min`, `max` (> `min`), `step` (> 0), `value`, `disabled`. Its form value is a **number**. |
| `color` (1.0.66) | `name`, `label`, `value` `#rrggbb`, `disabled` |
| `details` (1.0.66) | `summary`, `open` (`false`), `children` |
| `canvas` (1.0.66) | `id` (a name, unique on the surface), `width`/`height` 1–2048 canvas units, `label` (required, announced), `keys`, `pointer`, `frames`, `fit` `contain`/`none` (`contain`). Re-rendering with the same `id` keeps the picture. See [Canvas](#canvas). |

- **Names:** `name`, `action`, column `key`, `rowAction` and tab `id` match `^[A-Za-z0-9_.:-]{1,64}$`.
- **Labels and titles:** at most 120 characters.
- **`input` numbers:** `min`, `max` and `step` apply only to `kind: "number"`. `max` must be ≥ `min`, and `step` must be > 0.
- **Table rows** are open records:
  - Each row is a flat object keyed by column key, and each cell is a string, number, boolean or `null`.
  - `id`, a string or number unique in the table, names the row.
  - Other keys are ignored, but at least one row must hold at least one column key.
  - A missing cell reads —.
  - Columns whose format is not `text` align right by default.
  - The `empty` text defaults to "Nothing to show yet.".
- **Formats:**
  - `percent` shows 0.25 as 25%.
  - `bytes` shows B, KiB, MiB, GiB or TiB.
  - `duration` takes milliseconds and shows ms, s, "Xm YYs" or "Xh YYm".
- **Icons:** `activity`, `chart`, `bell`, `box`, `clock`, `cpu`, `database`, `gauge`, `globe`, `layers`, `dashboard`, `list`, `puzzle`, `shield`, `terminal`, `zap`. An unknown name shows the puzzle piece.
- **Tree limits:**
  - 2000 nodes, where rows, items, options, tabs and chart values count too;
  - 24 levels deep;
  - 256 KiB of JSON;
  - text 4096 characters, code 32 KiB, labels 120;
  - 500 rows, 16 columns, 100 options, 8 tabs, 200 chart points.

**Tree problems** read `<path> <problem>.`. The "is not a field of" check runs first.

- `is not a field of a "<type>" node`
- `must be an object`
- `must be a list`
- `has N entries; at most M`
- `must be a string`
- `must not be empty`
- `is longer than N characters`
- `must be true or false`
- `must be a whole number from A to B`
- `must be one of …`
- `must be a finite number`
- `must be 1–64 letters, digits, dots, colons, dashes or underscores`
- `needs at least one column` (or `option`, `tab`, `value`)
- `must be the id of one of the tabs`
- `only applies to kind "number"`
- `repeats the id of rows[n]` (or `tabs[n]`)
- `has none of the column keys (a, b); rows are flat objects keyed by column key`
- `"x" is not a node type (use one of …)`
- `nests deeper than 24 levels`
- `takes the page over 2000 nodes`
- `The tree is not plain data (it has a cycle or a value JSON cannot hold).`
- `The tree is N KiB; a page takes at most 256 KiB.`

## Page events

An event is `{ kind, pageId, action?, name?, value?, row?, form }`, and the object is frozen.

In 1.0.66:

- **Surfaces.** On a slot surface, `pageId` is the surface (`widget:<id>`, `inspector:<id>` or `settings:<id>`). An inspector `show` also carries `context: { scope, id }`.
- **Dialogs.** In a dialog, `pageId` is `dialog:<id>`.
- **Actions and header buttons** arrive as `{ kind: 'action', pageId: '', action: <id>, scope, target?, form: {} }`. `target` is the request, provider or key id; a header button has none.

| `kind` | When |
| --- | --- |
| `action` | A button is pressed, a row is clicked (`action` = `rowAction`, `row` = the row's `id`, or its index when it has none), or Enter is pressed in an `input` that has `action` (Ctrl/⌘+Enter in a `textarea`). |
| `change` | A field commits. `select`, `segmented`, `toggle` and `tabs` commit at once. `input` and `textarea` commit on blur, and on Enter when they have no `action`. |
| `show`, `hide` | The page was opened or left. |

`form` holds every named field's current value:

- a toggle gives a boolean;
- a `slider` gives a number;
- every other field, a number input included, gives a string.

## Control plane

`control.call(method, payload)` reaches an allowlist only.

- **`control:read`:**
  - `relay.status`;
  - `providers.list`, which returns `{ activeId, providers }`;
  - `providers.health`, `keys.list`;
  - `activity.list`, `activity.summary`;
  - `history.recent`, `history.stats`;
  - `analytics.report`, `analytics.prices.get`;
  - `settings.get`;
  - `guardrails.status`, `guardrails.findings`;
  - `notifications.list`, `routes.list`, `tunnel.get`;
  - `clients.list`, `clients.events`;
  - `codex.status`, `codex.quota`, `codex.quota.all`;
  - `updates.status`, `appearance.get`.
- **`control:write`:**
  - `relay.start`, `relay.stop`;
  - `providers.activate`;
  - `routes.upsert`, `routes.upsertMany`, `routes.delete`, `routes.remove`;
  - `models.discover`, `models.test`;
  - `keys.check`, `keys.reset`, `keys.move`;
  - `guardrails.clear`, `notifications.clear`;
  - `analytics.prices.set`, `analytics.prices.remove`, `analytics.prices.setCurrency`;
  - `clients.profile`, `codex.wake`, `updates.check`, `tunnel.privacy_test`.
- **`control:sensitive`:**
  - `providers.add`, `providers.update`, `providers.delete`;
  - `keys.add`, `keys.addMany`, `keys.update`, `keys.remove`;
  - `settings.update`;
  - `tunnel.configure`, `tunnel.start`, `tunnel.stop`, `tunnel.rotate`, `tunnel.reveal`;
  - `codex.login.start`, `codex.login.status`, `codex.login.cancel`, `codex.login.device.start`;
  - `codex.import.json`, `codex.import.files`, `codex.logout`.
- **Topics** (each needs `control:read`):
  - `activity.changed`, `appearance.changed`, `clients.changed`, `codex.changed`;
  - `guardrails.changed`, `guardrails.finding`;
  - `keys.changed`, `models.tested`, `notifications.raised`;
  - `providers.changed`, `providers.health`;
  - `relay.changed`, `routes.changed`, `settings.changed`, `tunnel.changed`;
  - `updates.installProgress`, `updates.stateChanged`.
- **`relay:rules` (1.0.66):**
  - `rules.list`, `rules.stats`, `rules.test`, `rules.recentErrors`;
  - `rules.saveRequestRule`, `rules.deleteRequestRule`;
  - `rules.saveRetryRule`, `rules.deleteRetryRule`;
  - the topic `rules.changed`.
- **Refusals:**
  - `not_allowed` `"<m>" is never available to plugins.` covers `plugins.*`, `system.*`, `backup.*`, `appearance.update` and `updates.install`, and (1.0.66) `rules.reorder*` and `relayhooks.*`.
  - A method that is not listed gives `not_allowed` `"<m>" is not a command plugins can use.`.
  - A missing grant gives `permission_denied` `"<m>" needs the "<perm>" permission, which this plugin was not granted.`.
  - An unknown topic gives `not_allowed` `"<t>" is not a topic plugins can follow.`.
  - Relay errors keep their own codes (for example `provider_not_found`).

Check `switchboard.app.capabilities` before calling something capability-gated, such as `codex.quota`.

## Storage

Every storage call needs `storage`. The store is encrypted on disk, kept across upgrades and removed with the plugin.

- **Keys:**
  - Keys match `^[A-Za-z0-9._:-]{1,64}$`.
  - A key that starts with `setting:` gives `invalid_argument` `Keys starting with "setting:" hold the operator's settings; read them with switchboard.settings.`.
- **Values:**
  - Values are strings of at most 8 KiB.
  - A non-string value gives "Stored values are strings; use JSON.stringify for anything else.".
  - A bigger value gives `too_large` "A stored value is at most 8 KiB.".
- **Capacity:** at most 64 keys and 64 KiB in total; beyond that you get `plugin_storage_full`.
- **`get(key)`:** returns the string or `null`.
- **`all()`:** returns the plugin's own keys, without settings.
- **`setMany(values)`:**
  - It writes 1–64 keys at once, and `null` removes a key.
  - It is atomic: every key changes or none do.
  - Messages:
    - "values must be an object of keys to strings or null."
    - "values must hold 1 to 64 keys."
    - `values["k"] must be a string or null; use JSON.stringify for anything else.`
    - `too_large` `values["k"] is over 8 KiB.`
- **`clear()`:** removes every key except the `setting:*` keys.
- **Order and rate:**
  - Calls run in order, so a read never sees half of a batch.
  - At most 120 writes a minute, and a `setMany` or `clear` counts once.
  - Over the limit you get `rate_limited` "A plugin can write its storage at most 120 times a minute; batch changes with storage.setMany.".

## 1.0.66 APIs

Full signatures are in `docs/plugins/switchboard.d.ts` and the prose in `docs/PLUGINS.md`.

### Slots and layout

| Call | Where it shows | Limit | Resolves |
| --- | --- | --- | --- |
| `ui.registerWidget({ id, title, order?, wide? })` | Overview. Lower `order` comes first; `wide` spans the row. | 4 | `{ surface: "widget:<id>" }` |
| `ui.registerInspectorTab({ id, title, scope: 'activity'\|'provider' })` | A tab in the request or provider inspector. | 4 | `{ surface: "inspector:<id>" }` |
| `ui.registerSettingsSection({ id, title, description? ≤ 200 })` | Settings → Plugins. | 2 | `{ surface: "settings:<id>" }` |
| `ui.registerAction({ id, title, scope: 'activity'\|'provider'\|'key', tone?, icon? })` | A button in an inspector or on an API-key row. | 8 | — |
| `ui.registerHeaderButton({ id, title, icon })` | An icon button in the top bar; `title` is its tooltip. | 2 | — |
| `ui.unregister(surfaceOrActionId)` | Removes any of the above. | | |
| `ui.setLayout({ hidden?, order?, startPage? })`, `ui.resetLayout()` [`ui:layout`] | The sidebar. `plugins` and `settings` are never hidden; unknown ids are ignored. One plugin holds the layout (`conflict`). | | |

- **Ids and titles.** Ids match `^[a-z0-9][a-z0-9_-]{0,47}$`; titles are 1–48 characters. Registering an id again updates it.
- **Drawing.** Draw a surface with `ui.render(surface, tree)`.
- **Removal.** Everything goes when the plugin stops, and the layout also goes in safe mode.

### Canvas

- **`ui.draw(surface, canvasId, ops)`.** Fire-and-forget; it replaces the whole picture.
  - At most 10000 ops and 512 KiB per draw.
  - An unknown op or field rejects the draw (it is logged), and the previous picture stays.
- **Ops:**
  - `clear{color?}`;
  - `rect{x,y,w,h,fill?,stroke?,lineWidth?,radius?}`;
  - `line{x1,y1,x2,y2,stroke,lineWidth?}`;
  - `circle{x,y,r,fill?,stroke?,lineWidth?}`;
  - `path{points ≤ 800 numbers,closed?,fill?,stroke?,lineWidth?}`;
  - `text{x,y,text ≤ 200,color,size 6–96,align?,baseline?,weight? normal/bold,font? sans/mono}`;
  - `save`, `restore`;
  - `translate{x,y}`, `scale{x,y}`, `rotate{angle (radians)}`;
  - `alpha{value 0–1}`.
- **Colours:**
  - `#rgb`, `#rgba`, `#rrggbb`, `#rrggbbaa`, `rgb()`, `rgba()`;
  - or `token:<name>`, where the name is one of `background`, `surface`, `text`, `muted`, `border`, `accent`, `success`, `warning`, `danger`, `info`.
- **`ui.onFrame((dtMs, tMs) => …)`.**
  - It runs while a `frames: true` canvas of the plugin is visible.
  - The next frame waits for the handler, or for 250 ms.
  - It returns a sync unsubscribe.
- **`ui.onInput(e => …)`:**
  - `keydown`/`keyup` give `{ key, code, repeat, shift, alt, ctrl, meta }` from a focused `keys: true` canvas;
  - `pointerdown`/`pointerup`/`pointermove`/`wheel` give `{ x, y, button, buttons, deltaY? }` in canvas units, with moves capped at 60 a second;
  - `focus`/`blur` have no extra fields.
  - Every event has `surface` and `canvas`.
  - Tab, Ctrl/Alt/⌘ chords and the safe-mode chord never reach a canvas.
  - It returns a sync unsubscribe.
- **Budget.** Draw, frame and input messages get 240 a second per plugin, outside the flood limit. Extra draws are dropped.

### Dialogs, sound, theme

- **`ui.dialog({ title, tree, actions, size? })` → `{ id, update(tree), close(), result }`** [`ui:dialogs`]
  - `title` is 1–120 characters.
  - `actions` are `{ id, label ≤ 24, tone?: 'primary'|'neutral'|'danger' }`.
  - `size` is `small`, `medium` (the default) or `large`.
  - `result` resolves `{ action: string|null, form }`.
  - Its events use `pageId: "dialog:<id>"`.
  - It needs a gesture, and one can be open per plugin (`busy`).
- **`ui.sound.tone({ frequency, durationMs, type?, volume? })`** [`ui:sound`]
  - `frequency` is 40–4000 Hz and `durationMs` 10–2000.
  - `type` is `sine`, `square`, `triangle` or `sawtooth`.
  - `volume` is scaled to at most 0.3.
  - Tones beyond 20 a second are dropped.
  - It returns void.
- **`ui.theme()` → `{ mode: 'dark'|'light', tokens }`, `ui.onTheme(fn)`.** No permission is needed.

### Collections, files, bus

- **`db.collection(name)`** [`storage`]
  - **Members:**
    - `get(id)` → doc or `null`;
    - `put(id, doc)` (any JSON ≤ 64 KiB except `null`, which gives `invalid_argument`: use `delete`);
    - `delete(id)` → boolean;
    - `list({ prefix?, after?, limit 1–200 (50) })` → `{ items: [{ id, doc }], next? }` (a page can hold fewer than `limit` items with `next` set when the reply's byte budget is reached);
    - `clear()` → count;
    - `stats()` → `{ collections: [{ name, count, bytes }], bytes, limitBytes }`.
  - **Names:** collections `^[a-z0-9][a-z0-9_-]{0,31}$`, ≤ 16; ids `^[A-Za-z0-9._:-]{1,128}$`.
  - **Limits:** 2 MiB per plugin (`plugin_storage_full`); 300 writes a minute. An over-limit write fails at once with `rate_limited`; nothing is queued.
- **`files.open({ accept?: ['.json'], maxBytes? ≤ 2 MiB })` → `{ name, text, size } | null`, `files.save({ name, text ≤ 5 MiB, mime? })` → boolean** [`files`]
  - Both need a gesture.
  - A file over `maxBytes` gives `too_large`.
- **`bus.publish(channel, payload)`, `bus.subscribe(channel, (payload, from) => …)` → sync unsubscribe** [`bus`]
  - Channels match `^[a-z0-9][a-z0-9._:-]{0,63}$`.
  - Payloads are JSON ≤ 32 KiB, at most 120 publishes a minute.
  - Messages go to other running subscribers only, and are not stored.
  - Treat payloads as untrusted.

### Network

`net.fetch(url, { method?, headers?, body?, timeoutMs? })` → `{ status, ok, headers, text, json() }` [`net:fetch`]

- **Destinations:**
  - `https://`, port 443, to a host in `network.hosts`; anything else gives `not_allowed`;
  - a host that resolves to loopback, private, link-local, CGNAT, multicast, unspecified, `::/8` or 6to4 `2002::/16` addresses, IPv4-mapped forms included, gives `blocked_address`, checked when connecting.
- **Redirects:** none; a 3xx comes back as it is.
- **Headers:** at most 32 request headers, each ≤ 8 KiB (`invalid_plugin`). Hop-by-hop, `keep-alive`, `expect`, `user-agent`, `cookie`, `host`, `proxy-*`, `x-switchboard-*` and `x-forwarded-*` are dropped. The User-Agent is `LuxurySwitchboard-Plugin/<version> (<plugin id>)`.
- **Secrets:** `{{secret:key}}` works in header values and the body only. In the URL it is **not** replaced and is sent as written.
- **Limits:**
  - request body ≤ 64 KiB;
  - response ≤ 192 KiB (`too_large`), decoded as UTF-8, with at most 32 lower-case headers;
  - 60 a minute and 4 at once; a fifth concurrent request fails at once with `rate_limited` and is not queued;
  - `timeoutMs` 1000–30000 (15000; `timeout`).
- **Other failures:** `network`, `missing_secret`, and `invalid_plugin` for a method outside GET/POST/PUT/PATCH/DELETE, a bad `timeoutMs`, too many or oversized headers, an invalid header name, or a header that holds CR, LF, NUL or invalid UTF-8 after secrets are filled.
- **Choose services that take the credential in a header or body:**
  - Slack `chat.postMessage` with `Authorization: Bearer {{secret:token}}`;
  - Discord `POST /api/v10/channels/<id>/messages` with `Authorization: Bot {{secret:token}}`.
  - Incoming-webhook URLs carry their secret in the path, so they cannot work.

### Relay rules

`switchboard.rules` [`relay:rules`] is the same as `control.call('rules.…')`. The host stamps the owner, so a plugin saves or deletes only its own rules (`permission_denied`). Its rules are deleted with the plugin.

- **`saveRequestRule(rule)`** saves a rule of this shape:
  - `{ id, name, enabled, match: { models: globs ≤ 16, endpoints }, effects }`.
  - `id` matches `^[a-z0-9][a-z0-9_-]{0,47}$`; `name` is 1–60 characters.
  - Endpoints are `responses`, `chat`, `messages`, `completions`, `embeddings`, `images`, `models` or `other`; `[]` means any.
  - `effects` holds:
    - `provider`;
    - `model` (1–200);
    - `block{status 400|403|429|503, message 1–200}`;
    - `rateLimit{requests 1–100000, windowSeconds 1–86400}`;
    - `budget{requests?, tokens?, period 'hour'|'day'}`;
    - `headers` (≤ 8, `^X-[A-Za-z0-9-]{1,62}$`, never `X-Switchboard-*`, `X-Forwarded-*` or `X-Real-IP`; values ≤ 256);
    - `tags` (≤ 8, `^[a-z0-9][a-z0-9_.-]{0,31}$`);
    - `stop`.
  - Rules apply to the **local relay only** and run in order:
    - `provider`, `model` and `block` take the first match;
    - tags add up;
    - every limit and budget is enforced, and going over answers 429 with `Retry-After`;
    - counters live in memory.
  - At most 64 request rules.
- **`saveRetryRule(rule)`** saves a rule of this shape:
  - `{ id, name, enabled, match: { statuses ≤ 16 (400–599), contains ≤ 8 (2–120), errorCodes ≤ 8, providers, models }, action, maxAttempts 1–10, delayMs 0–60000 }`.
  - At least one of `statuses`, `contains` and `errorCodes` must be non-empty.
  - `action` is `retry`, `rotate_key`, `switch_provider` or `fail`.
  - The first match wins; otherwise the built-in handling runs, and its patterns are never shown.
  - A retry rule applies after the content-policy check, so it never retries a refusal, and never to 2xx answers.
  - At most 64 retry rules.
- **Other calls:**
  - `deleteRequestRule(id)` and `deleteRetryRule(id)` give `{ deleted: true }`.
  - `list()` gives `{ requestRules, retryRules }`, with `owner`.
  - `stats()` gives `{ rules: { [id]: { hits, lastHitAt?, windowRequests, windowTokens, windowResetsAt? } } }`.
  - `test({ model, path, status?, errorText?, errorCode?, providerId? })` gives `{ requestRules, effects, retryRule? }`.
  - `recentErrors(limit?)` gives `{ errors: [{ signature, status, errorCode, detail, providerName, model, count, lastAt, coveredBy? }] }`.

### Relay hooks

**`relay.onRequest({ models?, endpoints?, timeoutMs? 100–5000 (2000) }, handler)`** [`relay:content`] resolves a remover.

- **Request.** The handler gets `{ activityId, model, endpoint, method, path, bytes, body? | bodyOmitted }`. `body` is present when the request is ≤ 192 KiB.
- **Decision.** It returns one of:
  - `{ action: 'continue' }`;
  - `{ action: 'rewrite', body?, model?, tags? }`, where the body must be valid JSON ≤ 192 KiB or it is ignored;
  - `{ action: 'block', block: { status, message } }`.
- **Order and timeouts.**
  - Hooks run in registration order, within a 5 s total.
  - They are fail-open: a timeout lets the request through.
  - After 3 timeouts in a row the hook is removed.

**`relay.onResponse({ models?, endpoints? }, (request, response) => …)`** runs after the client has its answer. `response` is `{ status, errorCode?, latencyMs, tokens?, body? }`.

Both kinds of hook follow these rules:

- they run on the **local relay only**, never on the tunnel or model tests;
- at most 8 hooks in the app, and 2 per plugin per phase (`limit`);
- bodies are never logged.

### Dev folder

**Plugins → Load from folder…** takes a source folder's absolute path (`plugin.json` + `main.js` + `style.css`) and links it (`plugins.dev.link`):

- **Reload.** The folder is checked every second; on a change it is validated and the plugin restarts.
- **Broken saves.** A save that fails validation keeps the last good version and shows the problems.
- **Grants.** New permissions arrive ungranted.
- **The row** shows a *Dev* badge, the folder, any problem and **Unlink**.
- **Limits.** At most 4 folders (`plugin_dev_limit`); one folder links one plugin (`plugin_dev_linked`). Links survive restarts.
- **Leaving.** **Unlink**, or installing a package file over the linked plugin, ends the link.

## Runtime budgets and stops

- **Start:** the script body must finish within 10 s. Otherwise you get "The script did not finish starting within 10 seconds.".
  - A throw while starting gives "The script failed while starting: …".
  - A worker that cannot start gives "The script could not be started: …".
- **Liveness:** the host pings every 5 s. After 20 s without an answer it stops the plugin with "Stopped because the script stopped responding for 20 seconds (an endless loop?).".
- **Message rate:** more than 500 messages in 5 s stops the plugin with "Stopped for flooding the window: more than 500 messages in 5 seconds.".
- **Protocol:** an unknown message gives "Stopped for sending a message outside the plugin protocol.".
- **When a plugin stops**, its pending calls reject with "The plugin was stopped.", and everything it added goes with it.
- **Blocked globals:** each one throws "`<name>` is not available to plugins. Use switchboard.control.call() to reach the Switchboard."
  - `fetch`, `XMLHttpRequest`, `WebSocket`, `EventSource`, `importScripts`, `indexedDB`, `caches`;
  - `Worker`, `SharedWorker`, `BroadcastChannel`, `WebTransport`, `WebSocketStream`, `RTCPeerConnection`;
  - `Notification`, `fetchLater`, `postMessage`;
  - `navigator.storage`, `navigator.serviceWorker`, `navigator.sendBeacon` and `navigator.locks`.

## Error codes

| Code | Meaning |
| --- | --- |
| `permission_denied` | The permission was not granted. |
| `not_allowed` | No plugin may use this command or topic. In 1.0.66 it also covers a `net.fetch` URL that is not `https://`/443 or names a host outside `network.hosts`. |
| `invalid_argument` | An argument has the wrong shape; the message names the field. |
| `invalid_tree` | A `render` tree failed its check; the message gives the path. |
| `not_found` | Unknown page, surface or route. |
| `limit` | Too many pages (8), commands (32), status readouts (3) or topics (24). In 1.0.66: widgets (4), inspector tabs (4), settings sections (2), actions (8), header buttons (2), collections (16), rules (64 + 64) or hooks (8 in the app, 2 per plugin per phase). |
| `busy` | 16 control calls are in flight, or a confirm or (1.0.66) a dialog is already open. |
| `rate_limited` | More than 6 notifications, or more than 120 storage writes, a minute. In 1.0.66: more than 300 collection writes or 120 bus publishes a minute, or more than 60 fetches a minute or 4 at once. These fail at once; nothing is queued. |
| `too_large` | A value, clipboard text or call is over its limit. In 1.0.66 also a document (64 KiB), bus payload (32 KiB), opened file, saved file, or fetch request or response. |
| `plugin_storage_full` | Storage would go over 64 keys or 64 KiB, or (1.0.66) collections would go over 2 MiB. |
| `needs_gesture` | `confirm`, `clipboard.writeText`, or (1.0.66) `ui.dialog` or `files.*` came outside the 10 s window. |
| `conflict` | The shortcut is already bound, or (1.0.66) another plugin holds the layout. |
| `blocked_address` (1.0.66) | A fetch host resolved to a loopback, private, link-local, CGNAT, multicast or unspecified address. |
| `missing_secret` (1.0.66) | A fetch used `{{secret:key}}` for a secret the operator has not set. |
| `invalid_plugin` (1.0.66) | A fetch request is malformed: method, `timeoutMs`, more than 32 headers, a header over 8 KiB, an invalid header name, or CR/LF/NUL or invalid UTF-8 after secrets are filled. |
| `timeout` (1.0.66) | A fetch passed its `timeoutMs`. |
| `network` (1.0.66) | A fetch could not connect, or the connection failed. |
| `failed` | Anything else, such as the clipboard refusing or a stopped plugin. |

## Restyling the shell

These hooks are public API. They are added to, never renamed. CSS Module class names (`X_y__a1b2`) change on every build, so never target them.

- **On `<html>`:** `data-route`, `data-scheme`, `data-theme` (`plugin:<id>` while your theme is active), `data-density` and `data-animations`.
- **`[data-ui="…"]` parts:**
  - `shell`, `sidebar`, `nav-item`, `nav-badge`, `runtime-bar`;
  - `status-readout`, with `[data-owner="plugin:<id>"]` on a plugin's own readouts;
  - `command-palette`, `palette-option`;
  - `toast`, `notification`;
  - `panel`, `metric`, `pill`, `empty-state`.
- **Global classes:** `.page-header`, `.page-body`, `.button`, `.button-primary`, `.status-dot[data-tone]`, `.ui-scrim` and `.ui-modal`.
- **Plugin pages:** `section[data-plugin-page="<pluginId>"][data-page-id="<pageId>"]`.
- **Tokens:** the public names are in `frontend/src/shared/contracts/tokens.ts` and under **Plugins → Developer reference**.
- **Precedence:** built-in theme < Appearance controls < a contributed theme < the user's overrides.
  - Stylesheets come in this order: the theme, then each plugin `style[data-sb-owner]`, then `#sb-user-css`.
  - Safe mode (Ctrl+Alt+Shift+S, or `?safe=1`) drops all of them.
- **Scoping:** scope your CSS, for example `:root[data-theme="plugin:<id>"] …` or `[data-plugin-page="<id>"] …`.

## Tooling

From the repo root, run `pnpm plugin <command>` (which runs `node scripts/plugin.mjs`). Exit codes: 0 ok, 1 problems or a crash, 2 usage.

| Command | What it does |
| --- | --- |
| `init <dir> [--kind page\|command\|theme] [--id <id>] [--name <name>]` | Scaffolds from `docs/plugins/templates/<kind>/`. It refuses a non-empty directory. |
| `validate <package.json\|dir> [--json]` | Runs the app's own checks. It prints `✓ <id> <version> is valid` and the sizes, or one `✗` line per problem. A syntax error is reported as `main.js line N: SyntaxError: …`. With `--json`: `{ ok, problems, notes, id, name, version, sizes: { scriptBytes, scriptLimit, cssBytes, cssLimit, tokens, tokenLimit, settings, settingLimit, packageBytes } }`. |
| `pack <dir> [-o\|--out <file>]` | Writes `<dir>/dist/<id>-<version>.json`. Nothing is written if a check fails. |
| `run <package\|dir> [--seconds 3] [--grant all\|perm,perm] [--event pageId:action\|actionId] [--command id] [--key key] [--setting key=value] [--fetch prefix=body] [--dialog actionId] [--confirm yes\|no] [--app-version x.y.z] [--json]` | Runs the real host against fixture data. **NOT a sandbox**: only run code you trust. With `--json` the output has: `ok`, `problems`, `notes`, `state`, `reason`, `faults`, `grants`, `pages`, `commands`, `status`, `themes`, `styles`, `storage`, `notifications`, `confirms`, `clipboard`, `navigations`, `refusedTrees` and `logs`. Version 1.0.66 adds `draws`, `frames`, `tones`, `tonesDropped`, `slots`, `surfaces`, `layout`, `dialogs`, `fetches`, `bus`, `hooks`, `hookDecisions` and `files`. |

1.0.66 `run` flags:

- **`--event <actionId>`** clicks a registered action or header button (`pageId: ''`); **`--event widget:<id>:<action>`** clicks inside a widget or other surface.
- **`--key <key>`** sends a key to every canvas.
- **`--fetch <url prefix>=<body>`** answers matching `net.fetch` calls with `200` and that body. Without a match a fetch fails with `not_allowed`; nothing goes to the network.
- **`--dialog <actionId>`** answers every `ui.dialog` with that action.
- **`--setting <secret>=x`** marks a secret as set; plugins never read its value.
- **`--app-version 1.0.66`** is needed until this checkout is 1.0.66.

Stand-ins: canvases get 5 frames and report their draws; collections, the bus, rules and the layout run in memory; `files.open` returns `null` and `files.save` records the name; relay hooks run once against a fixture request and report each decision (`hookDecisions`). The bus does not deliver a plugin's own publishes, so a lone plugin's `subscribe` never fires.

Events, commands, actions and canvas input count as gestures.

- **Source folder:** a source folder holds `plugin.json`, which is the package without `script` and `css`, plus `main.js` and/or `style.css`.
  - `plugin.json` refuses `"script"`, `"css"` and `"$schema"`.
  - For editor completion, map `plugin.json` to `docs/plugins/plugin.schema.json` with VS Code's `json.schemas` setting.
- **Type checking:** start `main.js` with `// @ts-check` and `/// <reference path="…/docs/plugins/switchboard.d.ts" />`.
