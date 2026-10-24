# Writing plugins for Luxury Switchboard

A plugin is **one JSON file** that can restyle the whole window, add its own
pages, palette commands, status readouts and notifications, read the relay's
live state and — when you allow it — change it. It is installed from
**Plugins** in the sidebar (*Install from file* or *Paste JSON*), reviewed on
one screen, and runs with exactly the permissions you tick there.

This guide is the complete reference. The **Developer reference** panel on the
Plugins page shows the same tables, generated from the code that enforces
them, so it never drifts from the build you are running.

- [Quick start](#quick-start)
- [How a plugin runs](#how-a-plugin-runs)
- [The package file](#the-package-file)
- [Permissions](#permissions)
- [Themes and stylesheets](#themes-and-stylesheets)
- [The script API](#the-script-api)
- [Pages and nodes](#pages-and-nodes)
- [Talking to the control plane](#talking-to-the-control-plane)
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
> `.js` file while you work and generate the package with a few lines of
> Node: `JSON.stringify({ ...manifest, script: fs.readFileSync('main.js', 'utf8') }, null, 2)`.

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
| `permissions` | string[] | yes | What the plugin asks for (may be empty). Unknown or repeated entries are refused. |
| `theme` | object | no | `{ name, scheme, tokens }` — needs `ui:theme`. See [Themes](#themes-and-stylesheets). |
| `css` | string | no | Up to 64 KiB — needs `ui:css`. No `@import`, no `</style`. |
| `script` | string | no | Up to 128 KiB of JavaScript. Required when any script permission is declared. |

Unknown top-level fields are refused rather than ignored, so a typo
(`"permisions"`) is caught at install time. Every problem in a file is listed
at once. The whole package, with its envelope, must also fit the control
plane's 256 KiB frame: a package close to every individual limit at once is
refused with a clear size message. At most **24** plugins can be installed.

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
| `ui:notify` | low | yes | Notifications in the feed and as toasts. |
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

Stable hooks on `<html>`: `data-theme`, `data-scheme`, `data-density`. The
global classes `.page-header`, `.page-body`, `.button`, `.button-primary`,
`.status-dot[data-tone]`, `.ui-modal` and `.ui-scrim` are stable too; CSS
Module class names (`PluginsPage_plugin__a1b2`) are build output and **will**
change between versions — never target them.

`@import` is refused, and the window's Content-Security-Policy blocks remote
fonts and images, so a stylesheet cannot phone home.

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
| `switchboard.onActivate(handler)` | Runs once the host has acknowledged start-up. Rarely needed: top-level code already runs first. |

### Pages — `ui:pages`

| Member | Description |
| --- | --- |
| `ui.registerPage({ id, title, icon? })` | Adds a sidebar entry under *Extensions* at `#plugin/<plugin-id>/<id>`. `id`: `^[a-z0-9][a-z0-9_-]{0,47}$`; `title` ≤ 48 characters; `icon` is one of the names below. Registering an existing id updates its title/icon and keeps its content. Resolves `{ route }`. Up to 8 pages. |
| `ui.unregisterPage(id)` | Removes the page. |
| `ui.render(pageId, tree)` | Replaces the page's content with a [node tree](#pages-and-nodes). An invalid tree rejects with `invalid_tree` and a path to the problem (`tree.children[2].rows[14].latency is longer than…`), which is also shown on the page. |
| `ui.onEvent(pageId?, handler)` | Called when the operator uses the page. Without a `pageId` the handler hears every page. Returns an unsubscribe function. |

Icons: `activity`, `chart`, `bell`, `box`, `clock`, `cpu`, `database`,
`gauge`, `globe`, `layers`, `dashboard`, `list`, `puzzle`, `shield`,
`terminal`, `zap`. Unknown names fall back to `puzzle`.

A page event is:

```ts
{
  kind: 'action' | 'change' | 'show' | 'hide',
  action?: string,           // the button/input/table action that fired
  name?: string,             // the control that changed
  value?: string | boolean,  // its new value
  row?: string | number,     // table row id (or index) for a row action
  form: { [name]: string | boolean }  // every named control's current value
}
```

`show` and `hide` fire when the operator opens and leaves the page: refresh
on `show`, stop polling on `hide`. Controls keep the operator's edits between
renders as long as their `name` (and `key`, when you set one) stay the same.

### Status bar — `ui:status`

`ui.setStatus(id, { text, tone?, tooltip? } | null)` shows a short readout
(`text` ≤ 40 characters, `tooltip` ≤ 200) in the runtime bar; `null` removes
it. Up to 3 per plugin. The tooltip always names the plugin, so a readout
cannot pass itself off as the relay's own. Tones: `neutral`, `info`,
`success`, `warning`, `danger`.

### Commands — `ui:commands`

`commands.register({ id, title, group?, keywords?, shortcut? }, handler)` adds
a palette command (Ctrl+K). `id`: `^[a-z0-9][a-z0-9_.-]{0,47}$`; `title`
≤ 80 characters; `group` ≤ 32 characters (shown as *Plugin name · group*);
up to 8 `keywords` of ≤ 32 characters; `shortcut` is a display hint of ≤ 24
characters. Registering the same id again replaces the command and its
handler. Up to 32 commands. `commands.unregister(id)` removes one.

### Notifications — `ui:notify`

`notify({ title, body?, tone? })` raises a notification in the feed and as a
toast. `title` ≤ 120 characters, `body` ≤ 1000; the body is signed with the
plugin's name. At most **6 per minute**; beyond that the call rejects with
`rate_limited` — collapse repeated events into one notification.

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
| `storage.all()` | Every key and value. |

Keys: `^[A-Za-z0-9._:-]{1,64}$`. Up to 64 keys and 64 KiB in total per
plugin, stored encrypted by the control plane. A full store rejects with
`plugin_storage_full`. Storage survives restarts and upgrades and is deleted
with the plugin.

### Anywhere

| Member | Description |
| --- | --- |
| `navigate(route)` | Opens a page of this window: `overview`, `activity`, `providers`, `settings`, `plugin/<id>/<page>`… A route that does not exist rejects with `not_found`. |
| `console.log/info/warn/error/debug` | Written to the plugin's log (**Plugins → Details**), up to 100 lines per 5 seconds. Uncaught errors in handlers are counted as *faults* and logged with their stack; the plugin keeps running. |

## Pages and nodes

A tree is one node; containers hold `children`. Every node may carry a `key`
(≤ 64 characters) to keep a control's state stable when siblings change. Text
is always text: there is no HTML anywhere.

| Node | Properties (defaults in brackets) |
| --- | --- |
| `stack` | `gap` (`md`), `children` — vertical. |
| `row` | `gap` (`md`), `align`: `start`/`center`/`end`/`between` (`center`), `wrap` (`true`), `children`. |
| `grid` | `columns` 1–6 (`2`; collapses on narrow windows), `gap` (`md`), `children`. |
| `panel` | `title`, `subtitle`, `actions` (nodes in the header), `children`. |
| `heading` | `text`, `level` 1–3 (`2`). |
| `text` | `text` (≤ 4096), `tone`: `default`/`muted`/`info`/`success`/`warning`/`danger`, `mono`, `size`: `sm`/`md`/`lg`, `weight`: `regular`/`strong`. |
| `metric` | `label`, `value`, `detail`, `tone`: `success`/`warning`/`danger`. |
| `metrics` | `items`: up to 12 metrics in one strip. |
| `badge` | `text`, `tone`. |
| `dot` | `tone`, `text` — a status dot with a label. |
| `table` | `columns` (1–16 of `{ key, label, align: left/right, mono, width: 120 / "120px" / "20%" / "auto" }`), `rows` (≤ 500 objects; cells are strings, numbers, booleans or `null`; an `id` field names the row), `empty`, `rowAction` — clicking a row sends `{ kind: 'action', action: rowAction, row }`. |
| `list` | `items` (≤ 500 of `{ title, detail, meta, tone }`), `empty`. |
| `code` | `text` (≤ 32 KiB), monospace and scrollable. |
| `kv` | `items` (≤ 64 of `{ key, value }`) — a definition list. |
| `button` | `text`, `action`, `variant`: `default`/`primary`/`danger`/`ghost`, `disabled`, `icon`. |
| `input` | `name`, `label`, `value`, `placeholder`, `kind`: `text`/`number`/`search`, `maxLength` (`256`), `action` — Enter sends that action — `disabled`. |
| `select` | `name`, `label`, `value`, `options` (≤ 100 strings or `{ value, label }`), `disabled`. |
| `segmented` | Like `select`, up to 6 options, shown as a segmented control. |
| `toggle` | `name`, `label`, `value` (boolean), `disabled`. |
| `progress` | `value` 0–1, `label`, `tone` (`info`). |
| `divider` | — |
| `empty` | `title`, `hint` — the standard empty state. |
| `link` | `text`, `href` — **https only**, opened in the system browser. |

Gaps: `none`, `sm`, `md`, `lg`. Labels and titles are ≤ 120 characters, field
names and actions match `^[A-Za-z0-9_.:-]{1,64}$`. A whole tree is at most
2000 nodes (rows, items and options included), 24 levels deep and 256 KiB of
JSON. `null` and `false` children are skipped, so
`cond && { type: 'badge', … }` works.

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

## Limits

| What | Limit |
| --- | --- |
| Installed plugins | 24 |
| Script / stylesheet | 128 KiB / 64 KiB |
| Theme tokens | 96, each value ≤ 120 bytes |
| Pages / commands / status readouts per plugin | 8 / 32 / 3 |
| Followed topics / control calls in flight | 24 / 16 |
| Notifications | 6 per minute |
| Page tree | 2000 nodes, 24 levels, 256 KiB |
| Arguments of one call | 300 KiB of JSON |
| Storage | 64 keys, 8 KiB per value, 64 KiB total |
| Messages to the window | 500 per 5 seconds |
| Start-up | 10 seconds for the top-level script |
| Responsiveness | a worker that does not answer pings for 20 seconds is stopped |
| Log | 100 lines per 5 seconds; the last 200 are kept for this session |

## Errors

Rejected calls carry `error.code` and `error.message`:

| Code | Meaning |
| --- | --- |
| `permission_denied` | The permission this needs was not granted. |
| `not_allowed` | The method or topic is not available to plugins at all. |
| `invalid_argument` | An argument has the wrong shape; the message names the field. |
| `invalid_tree` | `ui.render` got a tree it cannot draw; the message has the path. |
| `not_found` | Unknown page or route. |
| `limit` | Too many pages, commands, status readouts or topics. |
| `busy` | 16 control calls are already in flight. |
| `rate_limited` | More than 6 notifications in a minute. |
| `too_large` | A stored value or the call's arguments are too large. |
| `plugin_storage_full` | Storage would exceed 64 keys / 64 KiB. |
| anything else | The control plane's own error code for that command (e.g. `provider_not_found`), with its message. |

When **installing**, the control plane answers `invalid_plugin`,
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
   the plugin's code runs, `fetch`, `XMLHttpRequest`, `WebSocket`,
   `EventSource`, `importScripts`, `indexedDB`, `caches`, nested workers,
   `BroadcastChannel`, `WebTransport`, `RTCPeerConnection`, `Notification`,
   `navigator.storage`/`serviceWorker`/`sendBeacon` and raw `postMessage` are
   sealed. The window's Content-Security-Policy is the real network wall:
   `connect-src` names only the IPC origin.
2. **Every call is checked** in the window, against the operator's grants and
   an explicit method allowlist, with argument validation and byte budgets.
   The Go control plane is the third wall: it validates packages and storage
   again and never exposes credentials to any reader.
3. **No self-escalation.** `plugins.*` is unreachable, so a plugin cannot
   install, enable, regrant or remove plugins — including itself.
4. **No impersonation.** Commands are grouped under the plugin's name,
   notifications and status tooltips are signed with it, and plugin pages
   show which plugin they belong to.
5. **No markup.** Pages are data rendered by the window's components; links
   must be `https://` and open in the system browser.
6. **Bounded.** Floods, endless loops, oversized trees and runaway logging
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
| [`latency-board.json`](plugins/examples/latency-board.json) | A live page: metrics, a table, a refresh button, a period switch, a toggle, `show`/`hide` and event-driven refresh with a debounce. Read-only. |
| [`quick-switch.json`](plugins/examples/quick-switch.json) | Palette commands kept in step with the provider list, relay start/stop, and notifications for each outcome. |

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| *"ui:pages" needs a "script"* on install | A permission other than `ui:theme`/`ui:css` was declared without a script. |
| Crashed: *did not finish starting within 10 seconds* | Do not `await` long work at the top level; start it without `await` or from `onActivate`. |
| Crashed: *stopped responding* | A synchronous loop never yields. Split work across `await`s. |
| Crashed: *flooding the window* | Batch renders: re-render on a debounce instead of once per event. |
| A call rejects with `permission_denied` | The operator unticked it. Check `switchboard.has()` and degrade gracefully. |
| A page says *The plugin sent a page this window cannot draw* | Its last `render` was invalid (the previous tree stays); the message has the exact path. |
| The stylesheet broke the window | Ctrl+Alt+Shift+S, then disable the plugin. |
| Nothing in the log | `console.*` is captured; open **Details** on the plugin's row. |
