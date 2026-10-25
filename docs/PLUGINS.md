# Writing plugins for Luxury Switchboard

A plugin is **one JSON file** that can restyle the whole window, add its own
pages, palette commands, status readouts and notifications, read the relay's
live state and — when you allow it — change it. It is installed from
**Plugins** in the sidebar (*Install from file* or *Paste JSON*), reviewed on
one screen, and runs with exactly the permissions you tick there.

This guide is the complete reference. The **Developer reference** panel on the
Plugins page shows the same tables, generated from the code that enforces
them, so it never drifts from the build you are running.

| Also see | |
| --- | --- |
| [`docs/plugins/switchboard.d.ts`](plugins/switchboard.d.ts) | Types for every call, node and package field, for `// @ts-check` in your editor. |
| [`docs/plugins/plugin.schema.json`](plugins/plugin.schema.json) | JSON Schema for `plugin.json` (completion and checks in the editor). |
| [`.agents/skills/switchboard-plugin/`](../.agents/skills/switchboard-plugin/SKILL.md) | The skill AI agents load to write plugins; [`REFERENCE.md`](../.agents/skills/switchboard-plugin/REFERENCE.md) lists every limit and message on one page. |
| [`docs/plugins/examples/`](plugins/examples/) | The three built-in examples as installable files. |

- [Quick start](#quick-start)
- [How a plugin runs](#how-a-plugin-runs)
- [The package file](#the-package-file)
- [Permissions](#permissions)
- [Settings](#settings)
- [Themes and stylesheets](#themes-and-stylesheets)
- [Restyling the shell](#restyling-the-shell)
- [The script API](#the-script-api)
- [Pages and nodes](#pages-and-nodes)
- [Talking to the control plane](#talking-to-the-control-plane)
- [Tooling](#tooling)
- [Limits](#limits)
- [Errors](#errors)
- [Lifecycle, upgrades and storage](#lifecycle-upgrades-and-storage)
- [Safe mode](#safe-mode)
- [Security model](#security-model)
- [Examples](#examples)
- [Troubleshooting](#troubleshooting)

---

## Quick start

Save this as `hello.json` and install it from **Plugins → Install from file**:

```json
{
  "schema": 1,
  "id": "me.hello",
  "name": "Hello",
  "version": "0.1.0",
  "author": "You",
  "description": "A page that counts requests and a palette command.",
  "permissions": ["ui:pages", "ui:commands", "control:read"],
  "script": "await switchboard.ui.registerPage({ id: 'main', title: 'Hello', icon: 'zap' });\n\nasync function draw() {\n  const s = await switchboard.control.call('activity.summary');\n  await switchboard.ui.render('main', {\n    type: 'stack',\n    children: [\n      { type: 'metrics', items: [\n        { label: 'Requests', value: s.requests },\n        { label: 'Success', value: s.successRate.toFixed(1) + '%' }\n      ] },\n      { type: 'button', text: 'Refresh', action: 'refresh', variant: 'primary' }\n    ]\n  });\n}\n\nswitchboard.ui.onEvent('main', (event) => { if (event.action === 'refresh') draw(); });\nawait switchboard.commands.register({ id: 'open', title: 'Open Hello' }, () => switchboard.navigate('plugin/me.hello/main'));\nawait draw();\n"
}
```

A **Hello** entry appears under *Extensions* in the sidebar and **Open Hello**
in the command palette (Ctrl+K). Edit the file, install it again, and the new
version replaces the old one in place.

> Writing a script inside a JSON string is awkward. Keep the script in a
> `main.js` next to a `plugin.json` while you work: `pnpm plugin init`
> scaffolds that folder, `pnpm plugin validate` and `pnpm plugin run` check
> it, and `pnpm plugin pack` joins it into the one installable file. See
> [Tooling](#tooling).

## How a plugin runs

```
 package.json ──install──▶ control plane (encrypted, validated)
                                │ plugins.get
                                ▼
                       window: PluginsModel
           ┌────────────────────┼─────────────────────┐
     theme → Appearance   css → <style>        script → Worker (sealed)
                                                     │ postMessage
                                                     ▼
                                            PluginHost (checks every call)
                                     pages · commands · status · notify
                                     control.call · events · storage
```

- **The control plane stores packages**, encrypted with the rest of your
  configuration. It validates every field again on install, so a hand-made
  request cannot sneak in what the window would refuse.
- **Themes and stylesheets** are handed to the Appearance slice, which checks
  every token again before anything reaches the document.
- **Scripts run in a dedicated Web Worker**: their own thread, no DOM, no
  network, no storage of their own. The only way out is the frozen
  `switchboard` object, and every call on it becomes a message the host
  checks against what you granted.
- **Pages are data, not markup.** A plugin sends a tree of nodes; the window
  renders it with its own components. Plugin pages follow your theme, density
  and fonts and cannot inject HTML or script.

## The package file

| Field | Type | Required | Rules |
| --- | --- | --- | --- |
| `schema` | number | yes | Always `1`. |
| `id` | string | yes | 3–64 characters: lowercase letters, digits, `.` and `-`, starting and ending with a letter or digit (`^[a-z0-9][a-z0-9.-]{1,62}[a-z0-9]$`). Reverse-domain style (`me.tools.latency`) avoids clashes. The id is the identity: installing a package with an existing id **upgrades** that plugin. |
| `name` | string | yes | Up to 64 characters, no control characters. |
| `version` | string | yes | 1–32 characters of letters, digits, `.`, `+`, `-` (e.g. `1.2.0`). Shown, not compared. |
| `author` | string | no | Up to 64 characters. |
| `description` | string | no | Up to 280 characters; line breaks allowed. |
| `homepage` | string | no | An `https://` address of at most 200 characters, without credentials. Shown as a link on the plugin's row; never fetched. |
| `minAppVersion` | string | no | The oldest Luxury Switchboard the plugin runs on, `major.minor.patch` (e.g. `1.0.65`). See below. |
| `permissions` | string[] | yes | What the plugin asks for (may be empty). Unknown or repeated entries are refused. |
| `settings` | object[] | no | Up to 24 options the operator edits — needs a `script`. See [Settings](#settings). |
| `theme` | object | no | `{ name, scheme, tokens }` — needs `ui:theme`. See [Themes](#themes-and-stylesheets). |
| `css` | string | no | Up to 64 KiB — needs `ui:css`. No `@import`, no `</style`. |
| `script` | string | no | Up to 128 KiB of JavaScript. Required when any script permission is declared. |

Unknown top-level fields are refused rather than ignored, so a typo
(`"permisions"`) is caught at install time. Every problem in a file is listed
at once. The whole package, with its envelope, must also fit the control
plane's 256 KiB frame: a package close to every individual limit at once is
refused with a clear size message. At most **24** plugins can be installed.

**`minAppVersion`** is compared with the running app's version, number by
number. Installing on an older app is refused (*Plugin minAppVersion needs
Luxury Switchboard X or later*). A plugin that is already installed and then
meets an older app — after a restore, say — stays off and its row says
*Needs Luxury Switchboard X or later; this is Y.* A development build that
reports no version accepts any value. `switchboard.app.version` gives a
script the same number.

## Permissions

You declare what the plugin needs; the operator grants any subset on the
review screen, and can change that later (**Permissions** on the plugin's
row — the plugin restarts with the new set). Code always checks
`switchboard.has(permission)` rather than assuming.

| Permission | Impact | Ticked by default | What it allows |
| --- | --- | --- | --- |
| `ui:theme` | low | yes | Adds a colour theme you can pick in Appearance. |
| `ui:css` | medium | yes | Restyles the whole window while enabled. |
| `ui:pages` | low | yes | Adds sidebar pages built from the standard controls. |
| `ui:status` | low | yes | Short readouts in the runtime bar at the top. |
| `ui:commands` | low | yes | Actions in the command palette; they run only when chosen. |
| `ui:notify` | low | yes | Notifications in the feed and as toasts, and dialogs that ask you to confirm the plugin's own actions (`ui.confirm`). |
| `ui:clipboard` | low | yes | Puts text on your clipboard when you press one of its buttons or run one of its commands (`clipboard.writeText`). It can never read the clipboard. |
| `control:read` | medium | yes | Reads providers, routes, key metadata, activity, history, analytics and settings, and follows their live events. Credentials stay write-only. |
| `control:write` | **high** | yes | Starts/stops the relay, switches the active provider, edits routes, runs model tests, clears findings — everything on those pages except credentials. |
| `control:sensitive` | **high** | **no** | Adds/edits/removes providers and API keys, changes settings, controls the public tunnel and reveals its token, signs Codex accounts in or out. Grant only to plugins you wrote or fully trust. |
| `storage` | low | yes | Up to 64 KiB of its own data on this machine. |

Every permission except `ui:theme` and `ui:css` needs a `script`. A refused
call rejects with `code: "permission_denied"`, and the refusal is written to
the plugin's log so the operator can see what it tried.

On an **upgrade**, permissions the plugin already had keep their previous
grant; permissions that are new in this version start at their default and
are shown on the review screen.

## Settings

`settings` declares options the **operator** edits — a threshold, a refresh
interval, a display mode — so nobody has to edit the script to tune it. The
review screen shows *N settings you can change*; afterwards **Settings** on
the plugin's row opens a form built from the declaration, with the right
control for each type. Settings need a `script` but **no permission**.

```json
"settings": [
  { "key": "threshold", "label": "Slow above (ms)", "type": "number", "default": 1500, "min": 100, "max": 60000 },
  { "key": "onlyErrors", "label": "Only show failures", "type": "toggle", "default": false },
  { "key": "label", "label": "Badge label", "type": "text", "default": "", "placeholder": "e.g. EU", "maxLength": 12 },
  { "key": "mode", "label": "Mode", "type": "select", "default": "fast",
    "options": [{ "value": "fast", "label": "Fast" }, { "value": "full", "label": "Full" }] }
]
```

| Field | Rules |
| --- | --- |
| `key` | `^[a-z][A-Za-z0-9_]{0,31}$`, unique in the list. |
| `label` | 1–64 characters. |
| `description` | Optional, up to 200 characters, shown under the control. |
| `type` | `text`, `number`, `toggle` or `select`. |
| `default` | Required, and must itself be valid: a one-line string within `maxLength`, a finite number within `min`/`max`, a boolean, or one of the option values. |
| `placeholder` | `text` and `number` only; up to 64 characters. |
| `maxLength` | `text` only; a whole number 1–256 (default 256). Text settings are always one line. |
| `min`, `max` | `number` only; finite, `min` ≤ `max`. Either may be left out. |
| `options` | `select` only; 1–32 `{ value, label }`, each up to 64 characters, values unique. |

At most **24** settings. Each type takes only its own fields — `min` on a
text setting is refused (*"settings[0].min" does not apply to a text
setting.*), and so is any unknown field. No string may hold control
characters.

In the script, values are ready before the first line runs:

```js
let threshold = switchboard.settings.get('threshold');          // 1500, or what the operator chose
const all = switchboard.settings.all();                          // frozen { threshold, onlyErrors, … }
switchboard.settings.onChange((values, changed) => {             // only on a real change
  if (changed.includes('threshold')) { threshold = values.threshold; redraw(); }
});
```

- **Live delivery.** When the operator saves, a running plugin receives the
  new values straight away — no restart. `onChange` fires only when at least
  one value really changed, with `changed` listing those keys; `get` and
  `all` return the new values from then on.
- **Read-only.** A plugin cannot change its settings. `get` of an undeclared
  key returns `undefined`.
- **Storage.** Each value is stored as JSON under the storage key
  `setting:<key>`; a value equal to the default is stored as nothing, so a
  new default in an upgrade reaches it. The `setting:` prefix is
  **reserved**: `storage.set`, `remove` and `setMany` refuse it
  (`invalid_argument`), `storage.all()` leaves those keys out, and
  `storage.clear()` keeps them. A stored value that no longer fits the
  declaration (an upgrade narrowed `max`, say) reads as the default.
  Non-default values share the plugin's 64-key / 64 KiB storage budget.
- The operator's form refuses invalid input with *Use at most N characters,
  without line breaks.*, *Enter a number from A to B*, *Choose on or off.* or
  *Choose one of the options.*

## Themes and stylesheets

### Themes (`ui:theme`)

```json
"theme": {
  "name": "Dusk",
  "scheme": "dark",
  "tokens": { "--bg-canvas": "#17141f", "--accent": "#b99cf5" }
}
```

- `name` (≤ 48 characters) is what Appearance lists; `scheme` is `"dark"` or
  `"light"` and tells the window which built-in palette to start from.
- `tokens` is up to 96 CSS custom properties. Names match
  `^--[a-z][a-z0-9-]{0,47}$`; values are at most 120 bytes and may not
  contain `; { } < > \` or backticks, control characters, `url(`,
  `expression(`, `@import` or `javascript:`. A token is a value, never a way
  out of its declaration or a request for a remote resource.
- Tokens you leave out keep the scheme's built-in value, so a theme can be
  three lines.
- The theme appears in **Appearance → Theme** with the id `plugin:<id>`. It is
  never selected for you. Disabling or removing the plugin makes Appearance
  fall back to the built-in theme of the same scheme.

The public token names are listed in **Plugins → Developer reference → Theme
tokens** and in `frontend/src/shared/contracts/tokens.ts`: surfaces
(`--bg-chrome`, `--bg-canvas`, `--bg-surface`, `--bg-elevated`, `--bg-raised`,
`--bg-hover`, `--bg-selected`, `--bg-nav-selected`, `--bg-field`), borders
(`--border-subtle`, `--border-strong`, `--border-field`), text
(`--text-primary`, `--text-secondary`, `--text-muted`, `--text-disabled`,
`--text-danger`), `--accent`, status (`--status-info`, `--status-success`,
`--status-warning`, `--status-danger`, `--status-neutral`), `--shadow-float`,
`--scrim`, plus the derived tokens (`--action-*`, `--accent-soft`,
`--focus-ring`, `--selection`, fonts, `--fs-*` sizes, `--row-height`,
`--control-height*`, `--gutter`, `--gap`, `--pad`, `--radius-*`). These names
are an API: they are only ever added to, never renamed.

### Stylesheets (`ui:css`)

The `css` string is added to the document while the plugin is enabled and
removed when it is disabled or removed. It applies to the **whole window**, so
scope it:

```css
/* Only while this plugin's theme is the active one: */
:root[data-theme="plugin:me.dusk"] .page-header h1 { letter-spacing: -0.025em; }

/* Only on this plugin's own pages (data-page-id narrows it to one page): */
[data-plugin-page="me.hello"] h1 { letter-spacing: -0.02em; }
```

Target only the stable hooks listed in [Restyling the shell](#restyling-the-shell).
CSS Module class names (`PluginsPage_plugin__a1b2`) are build output and
**will** change between versions — never target them.

`@import` is refused, and the window's Content-Security-Policy blocks remote
fonts and images, so a stylesheet cannot phone home.

## Restyling the shell

A `ui:css` stylesheet can restyle the entire window, not only plugin pages.
These selectors are an API: they are added to, and changed only with a note
here. The full catalogue, with a one-line description of each part, is
**Plugins → Developer reference → Shell parts**
(`frontend/src/shared/contracts/ui-parts.ts`).

**On `<html>`**

| Attribute | Value |
| --- | --- |
| `data-route` | The open page: `overview`, `providers`, `plugin/<id>/<page>`… |
| `data-theme` | The active theme id; `plugin:<id>` while your theme is selected. |
| `data-scheme` | `dark` or `light`. |
| `data-density` | The Appearance density. |
| `data-animations` | `on` or `off` — honour `off` by dropping your transitions. |

**`[data-ui="…"]` parts** (the main ones; extra attributes in brackets)

| Area | Parts |
| --- | --- |
| Frame | `shell` [`data-sidebar`], `sidebar`, `sidebar-brand`, `palette-trigger`, `nav`, `nav-section` [`data-section`], `nav-section-label`, `nav-item` [`data-route`, `data-active`, `aria-current`], `nav-badge` [`data-tone`], `sidebar-footer`, `sidebar-toggle`, `update-pill`, `app-version` |
| Runtime bar | `workspace`, `runtime-bar`, `relay-state` [`data-state`], `relay-address`, `active-route`, `status-readout` [`data-tone`, `data-owner`], `status-overflow`, `runtime-actions`, `safe-mode-badge`, `workspace-content` [`data-route`], `workspace-error` |
| Palette | `palette-scrim`, `command-palette`, `palette-search`, `palette-results`, `palette-group`, `palette-option` [`data-command`, `aria-selected`], `palette-footer` |
| Notifications | `notifications-bell`, `notifications-badge`, `notifications-panel`, `notification` [`data-severity`], `toast-stack`, `toast` [`data-severity`] |
| Building blocks | `panel`, `panel-header`, `panel-title`, `panel-actions`, `metric-strip`, `metric` [`data-tone`], `segmented`, `pill` [`data-tone`], `empty-state` |

Your own readouts carry `data-owner="plugin:<id>"`, and your pages are
`section[data-plugin-page="<id>"][data-page-id="<page>"]`. The global classes
`.page-header`, `.page-body`, `.button`, `.button-primary`,
`.status-dot[data-tone]`, `.ui-modal` and `.ui-scrim` are stable too.

```css
/* A denser rail and a quieter runtime bar, only under this plugin's theme. */
:root[data-theme="plugin:me.dusk"] [data-ui="nav-item"] { border-radius: var(--radius-sm); }
:root[data-theme="plugin:me.dusk"] [data-ui="runtime-bar"] { border-bottom-color: transparent; }
/* Your own readout, wherever it appears. */
[data-ui="status-readout"][data-owner="plugin:me.dusk"] { font-variant-numeric: tabular-nums; }
/* The providers page only. */
:root[data-route="providers"] .page-header h1 { letter-spacing: -0.02em; }
```

**Precedence.** The built-in theme loses to the Appearance controls, which
lose to a selected plugin theme, which loses to the operator's own token
overrides and custom CSS. In the document head that is: the theme sheet,
then each plugin stylesheet (`style[data-sb-owner="plugin:<id>"]`), then the
operator's `#sb-user-css` — last, so the person at the keyboard always wins.
[Safe mode](#safe-mode) drops every plugin theme and stylesheet along with
the operator's CSS.

## The script API

A script is the body of an `async` function: top-level `await` works, and the
plugin counts as *running* once that body finishes (it has 10 seconds). Every
method that talks to the window returns a Promise that rejects with an
`Error` carrying `code` and `message` (see [Errors](#errors)). Arguments must
be plain JSON data.

### Identity

| Member | Description |
| --- | --- |
| `switchboard.plugin` | `{ id, name, version, granted }` — this plugin, and what it was granted. |
| `switchboard.app` | `{ version, capabilities }` — the Switchboard version and the control plane's advertised capabilities (e.g. `codex.quota`). Check a capability before calling a command that depends on it. |
| `switchboard.has(permission)` | `true` when that permission was granted. |
| `switchboard.onActivate(handler)` | Runs once the host has acknowledged start-up (on the next tick if it already has). Rarely needed: top-level code already runs first. |
| `switchboard.settings` | `get(key)`, `all()`, `onChange(handler)` — the operator's [settings](#settings). No permission needed. |

### Pages — `ui:pages`

| Member | Description |
| --- | --- |
| `ui.registerPage({ id, title, icon? })` | Adds a sidebar entry under *Extensions* at `#plugin/<plugin-id>/<id>`. `id`: `^[a-z0-9][a-z0-9_-]{0,47}$`; `title` ≤ 48 characters; `icon` is one of the names below. Registering an existing id updates its title/icon and keeps its content. Resolves `{ route }`. Up to 8 pages. |
| `ui.unregisterPage(id)` | Removes the page (an unknown id does nothing). |
| `ui.render(pageId, tree)` | Replaces the page's content with a [node tree](#pages-and-nodes). Rendering a page you have not registered rejects with `not_found`. An invalid tree rejects with `invalid_tree` and a path to the problem (`tree.children[2].rows[14].latency is longer than…`), which is also shown on the page; the previous tree stays. |
| `ui.setBadge(pageId, { text, tone? } \| null)` | Puts a small count or flag on the page's sidebar entry (a dot when the rail is folded); `null` clears it. `text` is 1–8 characters on one line; `tone` defaults to `neutral`. Re-registering the page keeps the badge. |
| `ui.onEvent(pageId?, handler)` | Called when the operator uses the page. Without a `pageId` the handler hears every page. Returns an unsubscribe function. |

Icons: `activity`, `chart`, `bell`, `box`, `clock`, `cpu`, `database`,
`gauge`, `globe`, `layers`, `dashboard`, `list`, `puzzle`, `shield`,
`terminal`, `zap`. Unknown names fall back to `puzzle`.

A page event is:

```ts
{
  kind: 'action' | 'change' | 'show' | 'hide',
  pageId: string,            // the page it came from
  action?: string,           // the button/input/table action that fired
  name?: string,             // the control that changed
  value?: string | boolean,  // its new value
  row?: string | number,     // table row id (or index) for a row action
  form: { [name]: string | boolean }  // every named control's current value
}
```

`form` holds strings for every field — a number `input` included, so parse
it — and booleans for toggles. `show` and `hide` fire when the operator opens
and leaves the page: refresh on `show`, stop polling on `hide`. Controls keep
the operator's edits between renders as long as their `name` (and `key`,
when you set one) stay the same.

### The gesture rule

Two calls reach the operator directly, so they only work **within 10 seconds
of the operator using the plugin**: `ui.confirm` and `clipboard.writeText`.
Any page event (an action, a change, even `show`/`hide`), running one of the
plugin's commands (from the palette or by its shortcut) and answering a
confirm all count. Outside that window the call rejects with
`needs_gesture` — *Writing to the clipboard only works within 10 seconds of
the operator using the plugin's page or command.* Call them from
`ui.onEvent` and command handlers, never from timers or event-topic
handlers.

### Status bar — `ui:status`

`ui.setStatus(id, { text, tone?, tooltip? } | null)` shows a short readout
(`text` ≤ 40 characters, `tooltip` ≤ 200) in the runtime bar; `null` removes
it. Up to 3 per plugin. The tooltip always names the plugin, so a readout
cannot pass itself off as the relay's own. Tones: `neutral`, `info`,
`success`, `warning`, `danger`.

### Commands — `ui:commands`

`commands.register({ id, title, group?, keywords?, shortcut? }, handler)` adds
a palette command (Ctrl+K) and resolves `{ id: "plugin:<plugin-id>:<id>" }`.
`id`: `^[a-z0-9][a-z0-9_.-]{0,47}$`; `title` ≤ 80 characters; `group`
≤ 32 characters (shown as *Plugin name · group*); up to 8 `keywords` of
≤ 32 characters (the plugin's name is always searchable too). Registering the
same id again replaces the command and its handler; if the new registration
is refused, the old one stays. Up to 32 commands.
`commands.unregister(id)` removes one, with its shortcut. If a handler
throws, the operator sees a *<title> failed* toast and the error is logged.

`shortcut` is a **real key binding**: pressing it anywhere in the window
runs the command, and the palette shows it next to the title.

| Rule | |
| --- | --- |
| Forms | `Alt+Key`, `Alt+Shift+Key`, `Ctrl+Alt+Key`, `Ctrl+Alt+Shift+Key`, `Ctrl+Shift+Key` — never a bare key or plain `Ctrl+Key`, so typing and editing keys stay free. |
| Key | `A`–`Z`, `0`–`9`, `F1`–`F12`. |
| Spelling | Any case and order (`shift+ctrl+k`); stored as `Ctrl+Alt+Shift+Key`. |
| Matching | By physical key, so it works in every keyboard layout. On macOS, Ctrl also matches ⌘. AltGr never fires one. Nothing fires inside a dialog or on key repeat. |
| Reserved | `Ctrl+Alt+Shift+S` (safe mode), `Alt+F4`, `Alt+1`…`Alt+9`, `Ctrl+Shift+C/I/J/N/Q/R/T/V/W/Z`. |

A malformed or reserved shortcut rejects with `invalid_argument`
(*shortcut: "Win" is not a modifier; use Ctrl, Alt or Shift.* ·
*shortcut: Alt+1 is reserved by the Switchboard.*); one that another
plugin or another of your commands already holds rejects with `conflict`
(*Alt+L is already bound to "Open latency board" (Latency board).*), and
the command you were replacing keeps its previous handler. Bindings go
when the plugin stops. Choose an uncommon combination and keep the
command usable from the palette without it.

### Notifications and confirmations — `ui:notify`

`notify({ title, body?, tone? })` raises a notification in the feed and as a
toast. `title` ≤ 120 characters, `body` ≤ 1000; the body is signed with the
plugin's name; `tone` defaults to `info`. At most **6 per minute**; beyond
that the call rejects with `rate_limited` — collapse repeated events into
one notification.

`ui.confirm({ title, body?, confirmText?, tone? })` asks the operator before
the plugin does something consequential, and resolves `true` (confirmed) or
`false` (cancelled or dismissed). `title` ≤ 120 characters, `body` ≤ 1000,
`confirmText` ≤ 24 (default *Continue*), `tone` `neutral` or `danger`. The
dialog names the plugin. It obeys [the gesture rule](#the-gesture-rule), and
only one question can be open at a time (`busy` otherwise).

```js
switchboard.ui.onEvent('board', async (event) => {
  if (event.action !== 'clear') return;
  const ok = await switchboard.ui.confirm({ title: 'Clear every guardrail finding?', confirmText: 'Clear', tone: 'danger' });
  if (ok) await switchboard.control.call('guardrails.clear');            // needs control:write
});
```

### Clipboard — `ui:clipboard`

`clipboard.writeText(text)` puts up to 32768 characters on the clipboard. It
obeys [the gesture rule](#the-gesture-rule); there is no way to read the
clipboard. If the system refuses, the call rejects with `failed`.

### Control plane — `control:read` / `control:write` / `control:sensitive`

| Member | Description |
| --- | --- |
| `control.call(method, payload?)` | Sends one control-plane command and resolves its result — the same data the built-in pages use. See [the method table](#talking-to-the-control-plane). Up to 16 calls in flight. |
| `events.on(topic, handler)` | Follows a live topic (needs `control:read`). Resolves an unsubscribe function once the subscription is live. Up to 24 topics. |

### Storage — `storage`

| Member | Description |
| --- | --- |
| `storage.get(key)` | The stored string, or `null`. |
| `storage.set(key, value)` | Stores a string (≤ 8 KiB). Use `JSON.stringify` for anything else. |
| `storage.remove(key)` | Deletes a key. |
| `storage.all()` | Every key and value the plugin stored (settings are not included). |
| `storage.setMany(values)` | Writes 1–64 keys in **one atomic write**: `{ key: "string", other: null }` — `null` removes a key. Either every change lands or none does. |
| `storage.clear()` | Removes every key the plugin stored. The operator's settings stay. |

Keys: `^[A-Za-z0-9._:-]{1,64}$`; keys starting with `setting:` are
[reserved for settings](#settings) and refused. Up to 64 keys and 64 KiB in
total per plugin, stored encrypted by the control plane. A full store rejects
with `plugin_storage_full`. Calls run in the order you make them, so a `get`
after a `set` sees the new value. At most **120 writes a minute** —
`setMany` and `clear` count once, so batch related changes:

```js
await switchboard.storage.setMany({ lastRun: String(Date.now()), cache: JSON.stringify(rows), stale: null });
```

Storage survives restarts and upgrades and is deleted with the plugin.

### Anywhere

| Member | Description |
| --- | --- |
| `navigate(route)` | Opens a page of this window: `overview`, `activity`, `insights`, `providers`, `keys`, `routes`, `tests`, `guardrails`, `tunnel`, `clients`, `appearance`, `plugins`, `settings`, or `plugin/<id>/<page>`. A leading `#/` is ignored. A route the window does not show rejects with `not_found`. No permission needed. |
| `log.debug/info/warn/error(…)`, `console.*` | Written to the plugin's log (**Plugins → Details**, last 200 lines), up to 100 lines per 5 seconds, each cut at 2000 characters. Uncaught errors in handlers are counted as *faults* and logged with their stack; the plugin keeps running. Refused calls (`permission_denied`, `not_allowed`) are logged as warnings. |

## Pages and nodes

A tree is one node; containers hold `children`. Every node may carry a `key`
(≤ 64 characters) to keep a control's state stable when siblings change. Text
is always text: there is no HTML anywhere.

> **Node fields are strict.** Every node, metric, column, list or kv item,
> option and tab accepts exactly the fields listed below — anything else is
> refused with its path (`tree.children[0].varient is not a field of a
> "button" node.`), so a typo cannot silently do nothing.
> **Table rows are the exception:** they are open records keyed by column
> key and extra keys are ignored, but the rows must use at least one of the
> column keys — rows that share none with the columns are a shape mistake
> (`{ id, cells: {…} }`), not a table of blanks
> (`tree.rows[0] has none of the column keys (name, rpm); rows are flat
> objects keyed by column key.`).

| Node | Properties (defaults in brackets) |
| --- | --- |
| `stack` | `gap` (`md`), `children` — vertical. |
| `row` | `gap` (`md`), `align`: `start`/`center`/`end`/`between` (`center`), `wrap` (`true`), `children`. |
| `grid` | `columns` 1–6 (`2`; collapses on narrow windows), `gap` (`md`), `children`. |
| `panel` | `title`, `subtitle`, `actions` (nodes in the header), `children`. |
| `heading` | `text`, `level` 1–3 (`2`). |
| `text` | `text` (≤ 4096), `tone`: `default`/`muted`/`neutral`/`info`/`success`/`warning`/`danger` (`default`), `mono`, `size`: `sm`/`md`/`lg`, `weight`: `regular`/`strong`. |
| `metric` | `label`, `value` (a string, number, boolean or `null`), `detail`, `tone`: `success`/`warning`/`danger`. |
| `metrics` | `items`: up to 12 `{ label, value, detail?, tone? }` in one strip. |
| `badge` | `text`, `tone` (`neutral`). |
| `dot` | `tone`, `text` — a status dot with a label. |
| `table` | `columns` (1–16 of `{ key, label, align: left/right, mono, width: 120 / "120px" / "20%" / "auto", format: text/number/percent/bytes/duration }`), `rows` (≤ 500; see below), `empty` (*Nothing to show yet.*), `rowAction` — clicking a row sends `{ kind: 'action', action: rowAction, row }`. |
| `list` | `items` (≤ 500 of `{ title, detail, meta, tone }`), `empty`. |
| `code` | `text` (≤ 32 KiB), monospace and scrollable. |
| `kv` | `items` (≤ 64 of `{ key, value }`) — a definition list. |
| `button` | `text`, `action`, `variant`: `default`/`primary`/`danger`/`ghost`, `disabled`, `icon`. |
| `input` | `name`, `label`, `hint` (help under the field), `error` (shown under the field and marks it invalid; a number field also reports its own `min`/`max` problem), `required`, `disabled`, `value`, `placeholder`, `kind`: `text`/`number`/`search`, `maxLength` 1–4096 (`256`), `action` — Enter sends that action — and, for `kind: "number"` only, `min`, `max`, `step`. |
| `textarea` | `name`, `label`, `hint`, `error`, `required`, `disabled`, `value`, `placeholder`, `rows` 2–12 (`4`), `maxLength` 1–4096 (`1000`), `action` — Ctrl/⌘+Enter sends it. |
| `select` | `name`, `label`, `value` (the first option), `options` (≤ 100 strings or `{ value, label }`), `disabled`. |
| `segmented` | Like `select`, up to 6 options, shown as a segmented control. |
| `toggle` | `name`, `label`, `value` (boolean), `disabled`. |
| `tabs` | `name`, `label`, `value` (the open tab's id), `tabs`: 1–8 `{ id, label, children }` with unique ids. Switching sends a `change` event; only the open tab's children are shown. |
| `progress` | `value` 0–1 (clamped), `indeterminate`, `label`, `tone` (`info`). |
| `chart` | `kind`: `line`/`bars`, `label` (its accessible name), `values` (1–200 finite numbers), `labels` (one per value, for tooltips), `min`/`max` (`max` > `min`; default: the data's range — bars also include zero), `tone` (`info`), `height` 48–240 (`96`). |
| `callout` | `tone` (`info`), `title`, `text`, `actions` (nodes) — an inline notice. |
| `divider` | — |
| `empty` | `title`, `hint` — the standard empty state. |
| `link` | `text`, `href` — **https only**, without credentials, opened in the system browser. |

Gaps: `none`, `sm`, `md`, `lg`. Labels and titles are ≤ 120 characters, field
names, actions, column keys and tab ids match `^[A-Za-z0-9_.:-]{1,64}$`.
Lengths count characters (code points), not bytes. A whole tree is at most
2000 nodes (rows, items, options, tabs and chart points included), 24 levels
deep and 256 KiB of JSON. `null` and `false` children are skipped, so
`cond && { type: 'badge', … }` works.

**Table rows** are flat objects: each cell sits under its column's `key` and
is a string, number, boolean or `null`; an `id` (string or number, unique in
the table) names the row for `rowAction`, otherwise the row's index is sent.
Other keys are ignored. Numbers are grouped (`12,480`), booleans read
*Yes*/*No* and a missing or `null` cell reads —. `format` turns a number
into `percent` (0.25 → 25%), `bytes` (B…TiB) or `duration` (milliseconds →
`840 ms`, `1.234 s`, `2m 05s`, `1h 02m`). Columns other than `format: "text"`
align right unless `align` says otherwise.

`input` and `textarea` send their `change` when the operator leaves them, or
on Enter when they have no `action`; `select`, `segmented`, `toggle` and
`tabs` send it at once.

```js
await switchboard.ui.render('main', {
  type: 'panel', title: 'Providers', actions: [{ type: 'button', text: 'Refresh', action: 'refresh', icon: 'zap' }],
  children: [{
    type: 'table', rowAction: 'open',
    columns: [{ key: 'name', label: 'Provider' }, { key: 'rpm', label: 'RPM', align: 'right', mono: true, width: 80 }],
    rows: providers.map((p) => ({ id: p.id, name: p.name, rpm: p.rpm })),
  }],
});
switchboard.ui.onEvent('main', (event) => {
  if (event.action === 'open') switchboard.navigate('providers');
});
```

## Talking to the control plane

`control.call` reaches an explicit allowlist. A method that is not listed is
refused with `not_allowed`, so a newer sidecar's commands stay out of reach
until someone decides where they belong.

**`control:read`** — `relay.status`, `providers.list`, `providers.health`,
`keys.list`, `activity.list`, `activity.summary`, `history.recent`,
`history.stats`, `analytics.report`, `analytics.prices.get`, `settings.get`,
`guardrails.status`, `guardrails.findings`, `notifications.list`,
`routes.list`, `tunnel.get`, `clients.list`, `clients.events`,
`codex.status`, `codex.quota`, `codex.quota.all`, `updates.status`,
`appearance.get`.

**`control:write`** — `relay.start`, `relay.stop`, `providers.activate`,
`routes.upsert`, `routes.upsertMany`, `routes.delete`, `routes.remove`,
`models.discover`, `models.test`, `keys.check`, `keys.reset`, `keys.move`,
`guardrails.clear`, `notifications.clear`, `analytics.prices.set`,
`analytics.prices.remove`, `analytics.prices.setCurrency`,
`clients.profile`, `codex.wake`, `updates.check`, `tunnel.privacy_test`.

**`control:sensitive`** — `providers.add`, `providers.update`,
`providers.delete`, `keys.add`, `keys.addMany`, `keys.update`, `keys.remove`,
`settings.update`, `tunnel.configure`, `tunnel.start`, `tunnel.stop`,
`tunnel.rotate`, `tunnel.reveal`, `codex.login.start`, `codex.login.status`,
`codex.login.cancel`, `codex.login.device.start`, `codex.import.json`,
`codex.import.files`, `codex.logout`.

**Never available**, whatever is granted: `plugins.*` (a plugin could grant
itself more), `system.*` (handshake and lifecycle), `backup.*` (the plaintext
credential export), `appearance.update` (the operator's own appearance) and
`updates.install`.

**Topics** for `events.on` (`control:read`): `activity.changed`,
`appearance.changed`, `clients.changed`, `codex.changed`,
`guardrails.changed`, `guardrails.finding`, `keys.changed`, `models.tested`,
`notifications.raised`, `providers.changed`, `providers.health`,
`relay.changed`, `routes.changed`, `settings.changed`, `tunnel.changed`,
`updates.installProgress`, `updates.stateChanged`.

Payloads and results are exactly what the control plane sends the built-in
pages; the shapes live in each slice's `frontend/src/features/<slice>/domain`
types. A few common ones:

```js
const { activeId, providers } = await switchboard.control.call('providers.list');
// providers: [{ id, name, … }] — aliases and status only, never a credential
const s = await switchboard.control.call('activity.summary');
// { requests, active, queued, successRate (percent), p95Ms, rpm }
const { requests, available } = await switchboard.control.call('history.recent', { period: '24h', limit: 200 });
await switchboard.control.call('providers.activate', { id: 'north-relay' });   // control:write
const off = await switchboard.events.on('activity.changed', (request) => { … });
```

Credentials are write-only everywhere: no read command returns an API key,
and the plaintext backup is never reachable.

## Tooling

`pnpm plugin <command>` (from the repository root; it is
`node scripts/plugin.mjs`) applies the app's own rules — it loads them from
`frontend/src`, so it cannot disagree with the build. Exit codes: `0` ok,
`1` the plugin has problems or crashed, `2` the command was misused.

| Command | What it does |
| --- | --- |
| `init <dir> [--kind page\|command\|theme] [--id acme.hello] [--name "Hello"]` | Scaffolds a source folder from `docs/plugins/templates/<kind>/` that already passes `validate`. Refuses a folder that has files. |
| `validate <package.json\|dir> [--json]` | Every install check, a compile check of the script (*main.js line N: SyntaxError: …*) and the byte budgets. Prints `✓ <id> <version> is valid` with the sizes, or one `✗` line per problem. |
| `pack <dir> [-o\|--out <file>]` | Validates, then writes the installable package (default `<dir>/dist/<id>-<version>.json`). Nothing is written on failure. |
| `run <package.json\|dir> [options]` | Starts the plugin in the app's own host against the dev fixture control plane and reports its pages (with their trees), commands, status, notifications, confirms, clipboard writes, storage, log and faults. |

`run` options: `--seconds <n>` (default 3), `--grant all|<perm,perm>`
(default: everything declared), `--event <pageId>:<action>` (shows the page
and presses the action; repeatable), `--command <id>` (repeatable),
`--setting <key>=<value>` (JSON or plain text; repeatable),
`--confirm yes|no` (the answer `ui.confirm` gets; default yes),
`--app-version <x.y.z>` and `--json`. Events and commands count as gestures,
so confirm and clipboard calls behave as they would in the window.

> `run` is **not a sandbox**: the script runs inside your Node process.
> Only run code you trust.

**A source folder** is `plugin.json` — the package without `script` and
`css` (both refused there, and so is `$schema`) — plus `main.js` and/or
`style.css`:

```text
my-plugin/
  plugin.json   { "schema": 1, "id": "me.hello", …, "permissions": [...] }
  main.js       // @ts-check
                /// <reference path="../docs/plugins/switchboard.d.ts" />
  style.css
```

- **Types.** Start `main.js` with `// @ts-check` and a
  `/// <reference path=".../docs/plugins/switchboard.d.ts" />` line, and the
  editor checks every call, node and event against
  [`switchboard.d.ts`](plugins/switchboard.d.ts).
- **Schema.** Map `plugin.json` to
  [`docs/plugins/plugin.schema.json`](plugins/plugin.schema.json) with
  VS Code's `json.schemas` setting for completion and inline errors.
- **Loop.** `init` → edit → `validate` → `run --event main:refresh` →
  `pack` → **Plugins → Install from file**. Installing the same `id` again
  upgrades in place.

## Limits

| What | Limit |
| --- | --- |
| Installed plugins | 24 |
| Package file | 512 KiB on disk; 256 KiB as one command on the wire |
| Script / stylesheet | 128 KiB / 64 KiB |
| Theme tokens | 96, each value ≤ 120 bytes |
| Settings | 24 per plugin |
| Pages / commands / status readouts per plugin | 8 / 32 / 3 |
| Followed topics / control calls in flight | 24 / 16 |
| Notifications | 6 per minute |
| Confirm dialogs | one open at a time; only within 10 s of an operator gesture |
| Clipboard | 32768 characters per write; only within 10 s of an operator gesture |
| Page badge | 1–8 characters |
| Page tree | 2000 nodes, 24 levels, 256 KiB |
| Arguments of one call | 300 KiB of JSON |
| Storage | 64 keys, 8 KiB per value, 64 KiB total; 120 writes a minute |
| Messages to the window | 500 per 5 seconds |
| Start-up | 10 seconds for the top-level script |
| Responsiveness | pinged every 5 seconds; a worker that does not answer for 20 seconds is stopped |
| Log | 100 lines per 5 seconds, 2000 characters a line; the last 200 are kept for this session |

## Errors

Rejected calls carry `error.code` and `error.message`:

| Code | Meaning |
| --- | --- |
| `permission_denied` | The permission this needs was not granted. |
| `not_allowed` | The method or topic is not available to plugins at all. |
| `invalid_argument` | An argument has the wrong shape; the message names the field. |
| `invalid_tree` | `ui.render` got a tree it cannot draw; the message has the path. |
| `not_found` | Unknown page or route. |
| `limit` | Too many pages (8), commands (32), status readouts (3) or topics (24). |
| `busy` | 16 control calls are already in flight, or a `ui.confirm` is already open. |
| `rate_limited` | More than 6 notifications, or more than 120 storage writes, in a minute. |
| `too_large` | A stored value, clipboard text or the call's arguments are too large. |
| `plugin_storage_full` | Storage would exceed 64 keys / 64 KiB. |
| `needs_gesture` | `ui.confirm` or `clipboard.writeText` outside [the gesture window](#the-gesture-rule). |
| `conflict` | The command's shortcut is already bound. |
| `failed` | Anything else — the clipboard refused, or the plugin was stopped (*The plugin was stopped.*). |
| anything else | The control plane's own error code for that command (e.g. `provider_not_found`), with its message. |

When **installing**, the control plane answers `invalid_plugin` (including
*Plugin minAppVersion needs Luxury Switchboard X or later*),
`plugin_too_large`, `plugin_limit`, `plugin_save_failed` or
`secure_storage_unavailable`; the review dialog explains each in words.

## Lifecycle, upgrades and storage

| State | Meaning |
| --- | --- |
| **Disabled** | Switched off on its row. Nothing of it is loaded. |
| **Active** | Enabled, no script: its theme and stylesheet are applied. |
| **Starting** | The script's top-level code is running. |
| **Running** | The script is live. |
| **Crashed** | The script threw during start-up, broke a rule (flooding, a stuck loop) or its worker failed. The reason is on the row; **Restart** tries again. |
| **Stopped** | Enabled but not started — safe mode, or still loading. |

- **Everything a plugin adds is owned by it.** Disabling, removing,
  restarting or crashing takes back its pages, commands, status readouts,
  stylesheet and theme in one step; an open plugin page shows that the plugin
  stopped instead of stale content.
- **Restart** happens automatically when the plugin is upgraded or its grants
  change, and manually from its row. A restart is a fresh worker: in-memory
  state is gone, `storage` is not.
- **Upgrading** is installing a package with the same `id`: storage and the
  install date are kept, previous grants carry over and the plugin is enabled.
- **Removing** deletes the package and its storage from the control plane.
- There is no auto-update and no remote install: plugins arrive only as files
  the operator chooses.

## Safe mode

A stylesheet that hides every control, or a plugin that misbehaves, is never
a dead end. Press **Ctrl+Alt+Shift+S** (or start the window with `?safe=1`)
to reload in **safe mode**: no custom CSS, no plugin stylesheets, themes or
scripts — while the Plugins and Appearance pages keep working, so you can
disable or remove the culprit. The runtime bar shows *Safe mode*, and the
Plugins and Appearance pages offer *Leave safe mode*.

## Security model

What the operator should know, and plugin authors should design for:

1. **Isolation.** Scripts run in a Web Worker created from a `blob:` URL:
   another thread with no DOM and no access to the window's objects. Before
   the plugin's code runs, `fetch`, `fetchLater`, `XMLHttpRequest`,
   `WebSocket`, `WebSocketStream`, `EventSource`, `importScripts`,
   `indexedDB`, `caches`, nested workers (`Worker`, `SharedWorker`),
   `BroadcastChannel`, `WebTransport`, `RTCPeerConnection`, `Notification`,
   `navigator.storage`/`serviceWorker`/`sendBeacon`/`locks` and raw
   `postMessage` are sealed; touching one throws *`<name>` is not available
   to plugins. Use switchboard.control.call() to reach the Switchboard.* The
   window's Content-Security-Policy is the real network wall: `connect-src`
   names only the IPC origin.
2. **Every call is checked** in the window, against the operator's grants and
   an explicit method allowlist, with argument validation and byte budgets.
   The Go control plane is the third wall: it validates packages and storage
   again and never exposes credentials to any reader.
3. **No self-escalation.** `plugins.*` is unreachable, so a plugin cannot
   install, enable, regrant or remove plugins — including itself.
4. **No impersonation.** Commands are grouped under the plugin's name,
   notifications, confirm dialogs and status tooltips are signed with it, and
   plugin pages show which plugin they belong to. Shortcuts cannot take the
   window's own keys or plain typing keys.
5. **Nothing behind the operator's back.** Confirm dialogs and clipboard
   writes need [a recent gesture](#the-gesture-rule), and the clipboard can
   only be written, never read.
6. **No markup.** Pages are data rendered by the window's components; node
   fields are strict, and links must be `https://` and open in the system
   browser.
7. **Bounded.** Floods, endless loops, oversized trees and runaway logging
   stop the plugin rather than the window.

A plugin granted `control:write` can change how your relay routes traffic,
and one granted `control:sensitive` can manage credentials and the public
tunnel. Only install plugins from people you trust, and grant the least that
works.

## Examples

Three commented examples ship with the app (**Plugins → Examples**) and live
in [`docs/plugins/examples`](plugins/examples). A test keeps those files
identical to the built-in copies.

| File | Shows |
| --- | --- |
| [`dusk-theme.json`](plugins/examples/dusk-theme.json) | A theme plus a stylesheet scoped to it. No script. |
| [`latency-board.json`](plugins/examples/latency-board.json) | A live page: metrics, a table with `duration`/`number` columns, a refresh button, a period switch, a toggle, `show`/`hide` and event-driven refresh with a debounce. Also a sidebar badge counting slow requests, two operator settings (`settings.get`/`onChange`) and a palette command on `Alt+L` that falls back to no shortcut on `conflict`. Read-only. |
| [`quick-switch.json`](plugins/examples/quick-switch.json) | Palette commands kept in step with the provider list, relay start/stop, and notifications for each outcome. |

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| *"ui:pages" needs a "script"* on install | A permission other than `ui:theme`/`ui:css` was declared without a script. |
| Crashed: *did not finish starting within 10 seconds* | Do not `await` long work at the top level; start it without `await` or from `onActivate`. |
| Crashed: *stopped responding* | A synchronous loop never yields. Split work across `await`s. |
| Crashed: *flooding the window* | Batch renders: re-render on a debounce instead of once per event. |
| A call rejects with `permission_denied` | The operator unticked it. Check `switchboard.has()` and degrade gracefully. |
| `confirm` or `clipboard.writeText` rejects with `needs_gesture` | Call it from a page event or command handler, within 10 seconds of the operator using the plugin — never from a timer or a live event. |
| `commands.register` rejects with `conflict` | Another command holds that shortcut. Register again without `shortcut` so the command stays in the palette. |
| A page says *The plugin sent a page this window cannot draw* | Its last `render` was invalid (the previous tree stays); the message has the exact path. |
| The stylesheet broke the window | Ctrl+Alt+Shift+S, then disable the plugin. |
| Nothing in the log | `console.*` is captured; open **Details** on the plugin's row. |
