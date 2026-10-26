# Writing plugins for Luxury Switchboard

A plugin is **one JSON file** that can restyle the whole window, add its own
pages, palette commands, status readouts and notifications, read the relay's
live state and — when you allow it — change it. Since 1.0.66 it can also
shape how the relay routes, limits and retries requests, talk to web services,
keep its own documents, add widgets, tabs and buttons to the built-in screens,
and draw on a canvas at full frame rate: enough to build anything, Tetris
included. It is installed from **Plugins** in the sidebar (*Install from
file*, *Install from link…* — a GitHub repository works —, *Paste JSON*, or
*Load from folder…* while you develop), reviewed on one screen, and runs with
exactly the permissions you tick there.

This guide is the complete reference. The **Developer reference** panel on the
Plugins page shows the same tables, generated from the code that enforces
them, so it never drifts from the build you are running.

| Also see | |
| --- | --- |
| [`docs/plugins/switchboard.d.ts`](plugins/switchboard.d.ts) | Types for every call, node and package field, for `// @ts-check` in your editor. |
| [`docs/plugins/plugin.schema.json`](plugins/plugin.schema.json) | JSON Schema for `plugin.json` (completion and checks in the editor). |
| [`.agents/skills/switchboard-plugin/`](../.agents/skills/switchboard-plugin/SKILL.md) | The skill AI agents load to write plugins; [`REFERENCE.md`](../.agents/skills/switchboard-plugin/REFERENCE.md) lists every limit and message on one page. |
| [`docs/plugins/examples/`](plugins/examples/) | The twelve built-in examples, Tetris included. |

- [What plugins can do](#what-plugins-can-do)
- [Quick start](#quick-start)
- [How a plugin runs](#how-a-plugin-runs)
- [The package file](#the-package-file)
- [Permissions](#permissions)
- [Settings](#settings)
- [Themes and stylesheets](#themes-and-stylesheets)
- [Restyling the shell](#restyling-the-shell)
- [The script API](#the-script-api)
- [Pages and nodes](#pages-and-nodes)
- [Slots, actions and layout](#slots-actions-and-layout)
- [Canvas, frames and input](#canvas-frames-and-input)
- [Dialogs, sound and theme](#dialogs-sound-and-theme)
- [Data: collections, files and the bus](#data-collections-files-and-the-bus)
- [Network and secrets](#network-and-secrets)
- [Relay rules](#relay-rules)
- [Relay hooks](#relay-hooks)
- [Talking to the control plane](#talking-to-the-control-plane)
- [Tooling](#tooling)
- [Recipes](#recipes)
- [Limits](#limits)
- [Errors](#errors)
- [Lifecycle, upgrades and storage](#lifecycle-upgrades-and-storage)
- [Safe mode](#safe-mode)
- [Security model](#security-model)
- [Examples](#examples)
- [Troubleshooting](#troubleshooting)

---

## What plugins can do

Thirty capabilities, each behind a permission the operator grants. Items
1–30 need Luxury Switchboard **1.0.66** or later: set
`"minAppVersion": "1.0.66"` in a plugin that uses them.

**Relay and reliability**

1. **Pin a provider.** A [request rule](#relay-rules) sends matching
   requests to one provider (`relay:rules`).
2. **Rewrite the upstream model.** A request rule replaces the model name
   sent upstream (`relay:rules`).
3. **Block.** A request rule answers at once with your status and message
   (`relay:rules`).
4. **Rate limit.** A request rule allows N requests per window
   (`relay:rules`).
5. **Budgets.** A request rule caps requests and/or tokens per hour or day
   (`relay:rules`).
6. **Tags.** A request rule labels requests; tags show and are searchable in
   Activity (`relay:rules`).
7. **Extra headers.** A request rule adds `X-` headers upstream, never
   forbidden ones (`relay:rules`).
8. **Custom retry rules.** Status, error text and error code → retry, rotate
   key, switch provider or fail fast (`relay:rules`).
9. **Recent errors.** Failures grouped by cause, with *Create retry rule*;
   the built-in retry patterns stay hidden (`relay:rules`, and the **Rules**
   page for the operator).
10. **Rule tester and counters.** What a request would match, plus hit and
    budget counters (`relay:rules`).
11. **Request hook.** Read, rewrite or block a request body before it is
    routed — **local relay only** (`relay:content`).
12. **Response observer.** The finished answer's status, timing, tokens and
    buffered body — **local relay only** (`relay:content`).

**Network and data**

13. **`net.fetch`.** HTTPS requests to the hosts the package lists, made by
    the Switchboard with SSRF protection (`net:fetch`).
14. **Secret settings.** Tokens the operator enters; the Switchboard puts
    them into requests, and the script never sees them (`type: "secret"`).
15. **Collections.** A document store of up to 2 MiB (`storage`).
16. **Open a file.** Import a text file the operator picks (`files`).
17. **Save a file.** Offer a download (`files`).
18. **Message bus.** Plugins talk to each other (`bus`).

**UI and shell**

19. **Overview widgets** (`ui:slots`).
20. **Row and inspector actions** on Activity requests, providers and API
    keys (`ui:slots`).
21. **Inspector tabs** in the Activity and Provider inspectors (`ui:slots`).
22. **Settings sections** under Settings → *Plugins* (`ui:slots`).
23. **Header buttons** in the top runtime bar (`ui:slots`).
24. **Layout.** Hide or reorder built-in sidebar items and choose the start
    page (`ui:layout`).
25. **Dialogs.** Modal forms and wizards built from plugin trees
    (`ui:dialogs`).
26. **Canvas.** A `canvas` node painted with drawing operations
    (`ui:pages`).
27. **Frames and input.** A frame loop plus keyboard and pointer input for
    canvases (`ui:pages`).
28. **Sound.** Short tones (`ui:sound`).
29. **Theme tokens.** Read the window's colours and follow changes
    (`ui.theme()`, `ui.onTheme`, no permission).
30. **Richer nodes and dev mode.** `markdown`, `image`, `slider`, `color`
    and `details` nodes, a dev-folder link with hot reload, and examples
    including a playable **Tetris**.

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
| `minAppVersion` | string | no | The oldest Luxury Switchboard the plugin runs on, `major.minor.patch` (e.g. `1.0.66`). Set `1.0.66` when you use anything from [What plugins can do](#what-plugins-can-do). See below. |
| `permissions` | string[] | yes | What the plugin asks for (may be empty, at most 24). Unknown or repeated entries are refused. |
| `settings` | object[] | no | Up to 24 options the operator edits, up to 8 of them secrets. Needs a `script`. See [Settings](#settings). |
| `network` | object | with `net:fetch` | `{ "hosts": [...] }`: required exactly when `net:fetch` is declared and refused otherwise. 1–16 lowercase DNS names (≤ 253 characters each), optionally starting with `*.` for any subdomain (`*.example.com` matches `api.example.com`, not `example.com`). No scheme, port, path or IP address. `localhost`, `*.local` and `*.internal` are refused. See [Network and secrets](#network-and-secrets). |
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
| `storage` | low | yes | Up to 64 KiB of key–value data and 2 MiB of [collections](#collections--storage), on this machine. |
| `ui:slots` | low | yes | Overview widgets, inspector tabs, Settings sections, row/inspector actions and header buttons, each marked with the plugin's name. |
| `ui:layout` | low | yes | Hides or reorders built-in sidebar items and picks the start page. *Plugins* and *Settings* always stay. |
| `ui:dialogs` | low | yes | Modal dialogs built from the standard controls, opened only after you use the plugin. |
| `ui:sound` | low | yes | Short tones, quietened by the window. |
| `files` | medium | yes | Opens a text file **you pick**, and offers files to save. Never reads anything you did not choose. |
| `bus` | low | yes | Sends messages to, and receives them from, other installed plugins. |
| `net:fetch` | **high** | **no** | Sends HTTPS requests to the hosts listed on the review screen, with any secrets you entered. It can send them data the plugin can read. |
| `relay:rules` | **high** | yes | Adds request rules (pin a provider, rewrite the model, block, rate-limit, budget, tag, add headers) and retry rules, and reads recent errors. Only its own rules; you can edit or disable any of them on the Rules page. |
| `relay:content` | **high** | **no** | **Reads and can change the prompts and answers that pass through your local relay. Never applies to the public tunnel.** |

> **Warnings the review screen repeats**
>
> - **`net:fetch`** lets the plugin send what it can see (activity,
>   settings, its own data) to the listed hosts. Check the host list.
> - **`relay:content`** sees every prompt and answer of matching local
>   requests and can rewrite them. The public tunnel is never affected,
>   whatever the plugin does.
> - **`control:sensitive`** reaches credentials, settings and the public
>   tunnel. Grant it only to plugins you wrote or fully trust.
>
> These three start **unticked**; the operator has to tick them on purpose.

Every permission except `ui:theme` and `ui:css` needs a `script`. A refused
call rejects with `code: "permission_denied"`, and the refusal is written to
the plugin's log so the operator can see what it tried.

On an **upgrade**, permissions the plugin already had keep their previous
grant; permissions that are new in this version start at their default and
are shown on the review screen. A version that adds a host to
`network.hosts` counts as asking for `net:fetch` again: it starts unticked
and marked **New**, so a new destination always needs your approval.

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
| `type` | `text`, `number`, `toggle`, `select` or `secret` (1.0.66). |
| `default` | Required (except for `secret`, which has none), and must itself be valid: a one-line string within `maxLength`, a finite number within `min`/`max`, a boolean, or one of the option values. |
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

### Secret settings (1.0.66)

```json
{ "key": "token", "type": "secret", "label": "Bot token", "description": "From your chat app's bot settings." }
```

A `secret` is a value the operator enters and **the script never sees**: an
API token, a password, a signing key. It can only travel in request headers
and bodies, never in the URL. A service that puts its credential in the URL
(an incoming-webhook address, say) cannot be used with a secret.

- **No `default`, `placeholder` or other type fields.** At most **8** per
  package. A value is 1–4096 characters.
- **The operator's form** shows a write-only field: *Set* or *Not set*,
  with **Replace** and **Clear**. A saved value is never shown again, to
  anyone.
- **The script** only learns which secrets are set:
  `switchboard.settings.secrets` is a frozen list of keys that have a value.
  `settings.get(key)` and `settings.all()` never include secrets.
- **Using a secret.** Write `{{secret:KEY}}` in a header value or the body of a
  [`net.fetch`](#network-and-secrets) request. The Switchboard substitutes it
  on its own side, and replaces the value with `[secret]` wherever it shows
  up in the response headers or body: raw, base64 (including inside a longer
  run such as `Basic base64(user:secret)`), hex, percent-encoded or
  JSON-escaped (`\/` and `\uXXXX` forms too). A secret shorter than four
  bytes is replaced only in its raw form, so ordinary text survives. An unset key fails the request with `missing_secret`.
- **Storage.** Encrypted with the plugin, never in plaintext. Secrets are
  **left out of backup exports**, so re-enter them after a restore. They are
  removed with the plugin, and when an upgrade stops declaring that key.

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
| `switchboard.settings` | `get(key)`, `all()`, `onChange(handler)` — the operator's [settings](#settings). `secrets` lists the [secret settings](#secret-settings-1066) that have a value. No permission needed. |

### Pages — `ui:pages`

| Member | Description |
| --- | --- |
| `ui.registerPage({ id, title, icon? })` | Adds a sidebar entry under *Extensions* at `#plugin/<plugin-id>/<id>`. `id`: `^[a-z0-9][a-z0-9_-]{0,47}$`; `title` ≤ 48 characters; `icon` is one of the names below. Registering an existing id updates its title/icon and keeps its content. Resolves `{ route }`. Up to 8 pages. |
| `ui.unregisterPage(id)` | Removes the page (an unknown id does nothing). |
| `ui.render(pageId, tree)` | Replaces the page's content with a [node tree](#pages-and-nodes). Also renders [surfaces](#slots-actions-and-layout) (`widget:<id>`, `inspector:<id>`, `settings:<id>`). Rendering a page you have not registered rejects with `not_found`. An invalid tree rejects with `invalid_tree` and a path to the problem (`tree.children[2].rows[14].latency is longer than…`), which is also shown on the page; the previous tree stays. |
| `ui.setBadge(pageId, { text, tone? } \| null)` | Puts a small count or flag on the page's sidebar entry (a dot when the rail is folded); `null` clears it. `text` is 1–8 characters on one line; `tone` defaults to `neutral`. Re-registering the page keeps the badge. |
| `ui.onEvent(pageId?, handler)` | Called when the operator uses the page. Without a `pageId` the handler hears every page. Returns an unsubscribe function. |

Icons: `activity`, `chart`, `bell`, `box`, `clock`, `cpu`, `database`,
`gauge`, `globe`, `layers`, `dashboard`, `list`, `puzzle`, `shield`,
`terminal`, `zap`. Unknown names fall back to `puzzle`.

A page event is:

```ts
{
  kind: 'action' | 'change' | 'show' | 'hide',
  pageId: string,            // the page or surface it came from: 'main', 'widget:load', 'dialog:<id>', '' for actions
  action?: string,           // the button/input/table action that fired, or a registered action's id
  name?: string,             // the control that changed
  value?: string | number | boolean,  // its new value (a number from a slider)
  row?: string | number,     // table row id (or index) for a row action
  form: { [name]: string | number | boolean },  // every named control's current value
  context?: { scope: 'overview' | 'activity' | 'provider' | 'settings', id?: string },  // 1.0.66: where a surface is shown
  scope?: 'activity' | 'provider' | 'key' | 'header',  // 1.0.66: a registered action's scope
  target?: string            // 1.0.66: the request, provider or key id the action was used on
}
```

`form` holds strings for every text field — a number `input` included, so parse
it — booleans for toggles and numbers for sliders. `show` and `hide` fire when the operator opens
and leaves the page: refresh on `show`, stop polling on `hide`. Controls keep
the operator's edits between renders as long as their `name` (and `key`,
when you set one) stay the same.

### The gesture rule

Some calls reach the operator directly, so they only work **within 10 seconds
of the operator using the plugin**: `ui.confirm`, `clipboard.writeText`, and
since 1.0.66 `ui.dialog`, `files.open` and `files.save`.
Any page event (an action, a change, even `show`/`hide`), running one of the
plugin's commands (from the palette or by its shortcut), accepting a confirm,
completing a file pick or save, answering a dialog, clicking one of its registered actions or header buttons,
and key or pointer input on its canvas all count. Outside that window the call rejects with
`needs_gesture` — *Writing to the clipboard only works within 10 seconds of
the operator using the plugin's page or command.* Call them from
`ui.onEvent` and command handlers, never from timers or event-topic
handlers. Declining a confirm or cancelling a file pick does not count: the
operator saying *no* never opens the window for another prompt.

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
| `navigate(route)` | Opens a page of this window: `overview`, `activity`, `insights`, `providers`, `keys`, `routes`, `rules`, `tests`, `guardrails`, `tunnel`, `clients`, `appearance`, `plugins`, `settings`, or `plugin/<id>/<page>`. `#/x`, `/x` and `x` are the same route. A route the window does not show rejects with `not_found`. No permission needed. |
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
| `markdown` | 1.0.66. `text` (≤ 20000): headings, emphasis, code, lists and links (with the `link` rules). Drawn as elements; raw HTML shows as text. |
| `image` | 1.0.66. `src` — a `data:image/png\|jpeg\|gif\|webp;base64,` URL of at most 256 KiB, never a remote address — `alt` (required), `width`, `height`. |
| `slider` | 1.0.66. `name`, `label`, `min`, `max` (> `min`), `step` (> 0), `value`, `disabled`. Sends `change` with a **number**. |
| `color` | 1.0.66. `name`, `label`, `value` (`#rrggbb`), `disabled`. Sends `change` with `#rrggbb`. |
| `details` | 1.0.66. `summary`, `open` (`false`), `children` — a section that folds open and shut. |
| `canvas` | 1.0.66. `id`, `width`/`height` 1–2048, `label` (required), `keys`, `pointer`, `frames`, `fit`: `contain`/`none` (`contain`). See [Canvas, frames and input](#canvas-frames-and-input). |

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
on Enter when they have no `action`; `select`, `segmented`, `toggle`,
`tabs`, `slider` and `color` send it at once.

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

## Slots, actions and layout

### Slots — `ui:slots`

A plugin can put its own content on the built-in screens. Each piece is a
**surface**: register it, then draw it with `ui.render(surface, tree)`
exactly like a page. Events arrive at `ui.onEvent` with
`pageId` set to the surface. The window frames every surface with
*From <plugin name>* and an error boundary, collapses empty areas, and
removes everything when the plugin stops.

| Call | Where | Limit | Resolves |
| --- | --- | --- | --- |
| `ui.registerWidget({ id, title, order?, wide? })` | Overview, below the built-in cards. `wide` spans the row; lower `order` comes first. | 4 | `{ surface: "widget:<id>" }` |
| `ui.registerInspectorTab({ id, title, scope })` | A tab in the Activity request inspector (`scope: "activity"`) or the Provider inspector (`"provider"`). | 4 | `{ surface: "inspector:<id>" }` |
| `ui.registerSettingsSection({ id, title, description? })` | Settings → *Plugins*. | 2 | `{ surface: "settings:<id>" }` |
| `ui.registerAction({ id, title, scope, tone?, icon? })` | A button in the Activity inspector (`activity`), the Provider inspector (`provider`) or on an API-key row (`key`). | 8 | — |
| `ui.registerHeaderButton({ id, title, icon })` | An icon button in the top runtime bar; `title` is its tooltip and accessible name. | 2 | — |
| `ui.unregister(surfaceOrActionId)` | Removes any of the above. | | |

Ids match `^[a-z0-9][a-z0-9_-]{0,47}$`; titles are 1–48 characters.
Registering the same id again updates it.

**An inspector tab draws for one request or provider.** Its `show` event
carries `context: { scope, id }`; render for that id each time:

```js
const { surface } = await switchboard.ui.registerInspectorTab({ id: 'cost', title: 'Cost', scope: 'activity' });
switchboard.ui.onEvent(surface, async (event) => {
  if (event.kind !== 'show' || !event.context?.id) return;
  await switchboard.ui.render(surface, { type: 'kv', items: [{ key: 'Request', value: event.context.id }] });
});
```

**Actions and header buttons** arrive as
`{ kind: 'action', pageId: '', action: <id>, scope, target? }`, where `target`
is the request, provider or key id (none for `header`). A click counts as a
[gesture](#the-gesture-rule), so it can open a dialog or save a file.

### Layout — `ui:layout`

```js
await switchboard.ui.setLayout({ hidden: ['tests', 'clients'], order: ['overview', 'activity', 'rules'], startPage: 'activity' });
```

- **`hidden`** — built-in sidebar items to hide.
- **`order`** — items to put first, in order; the rest keep their place after them.
- **`startPage`** — the page the window opens on.

Ids are the [`navigate`](#anywhere) route names. Unknown ids are ignored.

- **Plugins and Settings can never be hidden**, so the operator can always
  undo a layout.
- Only one plugin holds the layout at a time; a second one gets `conflict`.
- The Plugins page shows *Layout customised by <plugin>* with **Reset
  layout**.
- The layout goes away when the plugin stops, is disabled or removed, and in
  [safe mode](#safe-mode).
- `ui.resetLayout()` gives it back.

## Canvas, frames and input

A `canvas` node is a drawing surface on a page or slot surface. The script
paints it with a **display list**: `ui.draw(surface, canvasId, ops)` replaces
the whole picture. Draws are fire-and-forget.

```js
await switchboard.ui.render('game', { type: 'canvas', id: 'board', width: 200, height: 400, label: 'Game board', keys: true, frames: true });
switchboard.ui.draw('game', 'board', [
  { op: 'clear', color: 'token:background' },
  { op: 'rect', x: 20, y: 20, w: 20, h: 20, fill: 'token:accent', radius: 3 },
  { op: 'text', x: 100, y: 380, text: 'Score 0', color: 'token:text', size: 14, align: 'center' },
]);
```

| Op | Fields |
| --- | --- |
| `clear` | `color?` — fills everything (transparent without one). |
| `rect` | `x, y, w, h`, `fill?`, `stroke?`, `lineWidth?`, `radius?` |
| `line` | `x1, y1, x2, y2`, `stroke`, `lineWidth?` |
| `circle` | `x, y, r`, `fill?`, `stroke?`, `lineWidth?` |
| `path` | `points` (x, y, x, y, …; ≤ 800 numbers), `closed?`, `fill?`, `stroke?`, `lineWidth?` |
| `text` | `x, y, text` (≤ 200), `color`, `size` 6–96, `align?` `left`/`center`/`right`, `baseline?` `top`/`middle`/`alphabetic`/`bottom`, `weight?` `normal`/`bold`, `font?` `sans`/`mono` |
| `save`, `restore` | Keep and bring back the transform and alpha. |
| `translate`, `scale` | `x, y` |
| `rotate` | `angle` (radians) |
| `alpha` | `value` 0–1 |

- **Colours** are `#rgb`, `#rgba`, `#rrggbb`, `#rrggbbaa`, `rgb()`/`rgba()`,
  or `token:<name>` for a [theme token](#theme-tokens) such as
  `token:accent`, so the picture follows the operator's theme.
- **Limits.** A draw is at most 10000 ops and 512 KiB. An unknown op or
  field rejects that draw and logs it, and the previous picture stays.
- **Scaling.** Coordinates are canvas units. `fit: "contain"` scales the
  canvas to the space available, sharp on high-DPI screens.
- **Re-rendering.** A tree with the same canvas `id` keeps the picture.

**Frames.** `ui.onFrame((dtMs, tMs) => …)` runs once per display frame
while a `frames: true` canvas of the plugin is on screen, and stops while it
is hidden. The next frame comes only after your handler returns (or after
250 ms), so a slow frame lowers the frame rate instead of piling up. Use `dtMs`
for movement so speed does not depend on the frame rate.

**Input.** `ui.onInput(event => …)` receives:

- `keydown`/`keyup` with `key`, `code`, `repeat`, `shift`, `alt`, `ctrl`
  and `meta`, from a focused canvas with `keys: true`;
- `pointerdown`/`pointerup`/`pointermove`/`wheel` with `x`, `y` (in canvas
  units), `button`, `buttons` and `deltaY`, from a canvas with `pointer: true`.
  Moves arrive at most 60 a second;
- `focus`/`blur`.

Every event names its `surface` and `canvas`. While focused, a canvas
captures plain keys and Shift+key. **Tab, Ctrl/Alt/⌘ chords and the safe-mode
chord always reach the window**, so the operator can never be trapped. An
unfocused canvas shows *Click to focus*.

**Budget.** Draw, frame and input messages share their own allowance of 240 a
second per plugin and do not count toward the window's flood limit. Extra
draws are dropped, never crashed. One draw per frame is plenty.

## Dialogs, sound and theme

### Dialogs — `ui:dialogs`

```js
const dialog = await switchboard.ui.dialog({
  title: 'New budget', size: 'small',
  tree: { type: 'stack', children: [{ type: 'input', name: 'limit', label: 'Requests a day', kind: 'number', value: '1000' }] },
  actions: [{ id: 'cancel', label: 'Cancel' }, { id: 'save', label: 'Save', tone: 'primary' }],
});
const { action, form } = await dialog.result;   // action is null when dismissed
if (action === 'save') { /* use form.limit */ }
```

- **Gesture and limits.** A dialog needs a [gesture](#the-gesture-rule).
  One can be open at a time per plugin (`busy`).
- **Chrome.** The dialog names the plugin.
- **Events.** Its tree sends events to `ui.onEvent` with
  `pageId: "dialog:<id>"`.
- **Wizards.** `dialog.update(tree)` swaps the content for the next step
  or live validation.
- **Closing.** `dialog.close()` closes it with `action: null`. Any footer
  action closes it with that action's `id`.

### Sound — `ui:sound`

`ui.sound.tone({ frequency, durationMs, type?, volume? })` plays one tone:

- `frequency` is 40–4000 Hz; `durationMs` is 10–2000;
- `type` is `sine` (the default), `square`, `triangle` or `sawtooth`;
- the window scales `volume` (0–1) to at most 0.3;
- tones beyond 20 a second are dropped.

### Theme tokens

`ui.theme()` resolves the window's `{ mode, tokens }`. `mode` is `dark` or
`light`; `tokens` holds `background`, `surface`, `text`, `muted`, `border`,
`accent`, `success`, `warning`, `danger` and `info` as CSS colours.
`ui.onTheme(handler)` runs when the operator changes the theme. No permission
is needed. On a canvas, `token:<name>` colours already follow the theme
without either call.

## Data: collections, files and the bus

### Collections — `storage`

A document store next to the small key–value `storage`:

```js
const scores = switchboard.db.collection('scores');
await scores.put('best', { score: 1200, at: Date.now() });
const best = await scores.get('best');                 // the document, or null
const page = await scores.list({ prefix: 'run:', limit: 50 });   // { items: [{ id, doc }], next? }
if (page.next) await scores.list({ prefix: 'run:', after: page.next });
```

| Member | |
| --- | --- |
| `get(id)` | The document or `null`. |
| `put(id, doc)` | Inserts or replaces any JSON value of at most 64 KiB except `null` (use `delete`). |
| `delete(id)` | Resolves `true` if a document was removed. |
| `list({ prefix?, after?, limit? })` | Documents ordered by id. `limit` is 1–200 (the default is 50). A page can hold fewer than `limit` documents and still return `next` when it reaches the message byte budget, so loop on `next`, not on the count. Pass `next` back as `after`. |
| `clear()` | Removes the collection's documents and resolves how many. |
| `stats()` | `{ collections: [{ name, count, bytes }], bytes, limitBytes }` for the whole plugin. |

- **Names.** Collections match `^[a-z0-9][a-z0-9_-]{0,31}$`, up to 16 per
  plugin. Document ids match `^[A-Za-z0-9._:-]{1,128}$`.
- **Size.** 2 MiB per plugin in all; a full store gives
  `plugin_storage_full`.
- **Writes.** 300 a minute; one more fails at once with `rate_limited`.
- **Storage.** Each plugin has its own encrypted file, deleted with the
  plugin.

### Files — `files`

```js
const file = await switchboard.files.open({ accept: ['.json'], maxBytes: 256 * 1024 });  // null if cancelled
if (file) importRules(JSON.parse(file.text));
await switchboard.files.save({ name: 'rules.json', text: JSON.stringify(rules, null, 2), mime: 'application/json' });
```

- **`open`** lets the operator pick one text file and resolves
  `{ name, text, size }`, or `null` when they cancel. `maxBytes` is at most
  2 MiB (the default); a larger file gives `too_large`.
- **`save`** offers a download of at most 5 MiB. It resolves `false` when
  cancelled.
- **Gesture.** Both need a [gesture](#the-gesture-rule).
- **Scope.** The plugin only ever sees the one file the operator chose.

### Bus — `bus`

```js
const off = switchboard.bus.subscribe('budget.alert', (payload, from) => { /* from = the sender's plugin id */ });
await switchboard.bus.publish('budget.alert', { rule: 'daily', used: 0.9 });
```

- **Channels** match `^[a-z0-9][a-z0-9._:-]{0,63}$`.
- **Payloads** are JSON of at most 32 KiB.
- **Rate.** At most 120 publishes a minute.
- **Delivery.** Every other running plugin with `bus` that subscribed
  receives the message. The sender does not. Messages are not stored: a
  plugin that is not running misses them.
- **Trust.** Treat payloads as untrusted input; `from` tells you who sent
  them.

## Network and secrets

### `net.fetch` — `net:fetch`

```json
"permissions": ["net:fetch"],
"network": { "hosts": ["slack.com"] },
"settings": [
  { "key": "token", "type": "secret", "label": "Slack bot token", "description": "xoxb-… with chat:write." },
  { "key": "channel", "type": "text", "label": "Channel ID", "default": "" }
]
```

```js
const res = await switchboard.net.fetch('https://slack.com/api/chat.postMessage', {
  method: 'POST',                          // GET (default), POST, PUT, PATCH or DELETE
  headers: { authorization: 'Bearer {{secret:token}}', 'content-type': 'application/json; charset=utf-8' },
  body: JSON.stringify({ channel: switchboard.settings.get('channel'), text: 'Relay is down' }),
});
if (!res.ok) switchboard.log.warn('Slack said', res.status, res.text);
```

The Switchboard itself makes the request (not the window), with these rules:

- **Allowed destinations.** `https://` only, port 443. The host must be in
  the package's `network.hosts`, which the operator saw on the review
  screen; anything else gives `not_allowed`.
- **SSRF protection.** A host that resolves to a loopback, private,
  link-local, CGNAT, multicast or unspecified address is refused when
  connecting (`blocked_address`), and so are `::/8`, 6to4 (`2002::/16`) and
  IPv4-mapped forms of those addresses. This holds even when a listed name points
  there, or changes to point there later. The plugin cannot reach your
  machine, your network or the relay this way.
- **No redirects.** A 3xx comes back as it is, without its `location`.
- **Headers.** At most 32 request headers of at most 8 KiB each. `host`,
  `content-length`, `connection`, `keep-alive`, `cookie`, `expect`,
  `user-agent`, `proxy-*`, `transfer-encoding`, `upgrade`, `te`, `trailer`,
  `x-switchboard-*` and `x-forwarded-*` are dropped. The User-Agent is
  always `LuxurySwitchboard-Plugin/<version> (<plugin id>)`.
- **Secrets.** Write `{{secret:KEY}}` in a header value or the body; it
  works nowhere else, **not even in the URL**. A secret that is not set gives
  `missing_secret`. Any secret value that comes back in the response is
  shown as `[secret]`.
- **Size and rate.** The request body is at most 64 KiB. The response body
  is at most 192 KiB (`too_large`) and is decoded as UTF-8.
- **Concurrency.** At most 60 requests a minute and 4 at once. A fifth
  request while four are in flight fails at once with `rate_limited`; it is
  not queued.
- **Timeout.** `timeoutMs` is 1000–30000 (default 15000; `timeout`).
- **Other failures.** Network failures give `network`. A malformed request
  gives `invalid_plugin`: an unknown method, `timeoutMs` out of range, more
  than 32 headers, a header over 8 KiB, an invalid header name, or a value
  that contains a line break once its secret is filled in.
- **The result** is `{ status, ok, headers, text, json() }`: lower-case
  header names, at most 32.

> **Warning.** `net:fetch` can send anything the plugin can read to the
> listed hosts. It starts unticked. Grant it only when the host list makes
> sense for what the plugin says it does.

**Choose APIs that take the secret in a header or the body.** Slack and Discord
*incoming-webhook* URLs carry their secret in the path, which a plugin cannot
fill in. Use Slack's `chat.postMessage` (`Authorization: Bearer
{{secret:token}}`) or Discord's channel-message API (`Authorization: Bot
{{secret:token}}`) instead. The [status widget example](#examples) shows a
secret header end to end.

## Relay rules

Rules change how **your local relay** treats requests. The operator manages
them on the **Rules** page; a plugin with `relay:rules` manages its own through
`switchboard.rules`. The Switchboard records the plugin as each rule's owner:

- a plugin can change or delete **only its own** rules;
- the operator can edit or disable any rule, and reorder them;
- a plugin's rules are deleted when the plugin is removed;
- `rules.reorder*` is never available to plugins.

### The Rules page (operator)

**Rules** sits in the sidebar's routing group, after *Routes*, whenever the
sidecar advertises `rules.manage`. It has three tabs:

- **Request rules.** The ordered list, with up/down buttons and an enable
  switch on each row. A row shows a summary, hit/budget/window counters and
  its owner (a plugin badge for plugin rules). The editor covers every effect,
  and a tester shows which rules a model and path would match.
- **Retry rules.** Custom rules only, each with an editor and a tester. The
  built-in retry patterns are never listed.
- **Recent errors.** Failures grouped by status, code and redacted detail.
  Each group has *Create retry rule*, prefilled from the group, or shows
  *Covered by <rule>* when a custom rule already handles it.

The Activity inspector shows each request's tags and, for a failed request,
*Create retry rule*. Activity search also matches tags.

### Request rules

```js
await switchboard.rules.saveRequestRule({
  id: 'mini-daily', name: 'Cap mini models', enabled: true,
  match: { models: ['gpt-4o-mini*'], endpoints: ['chat', 'responses'] },
  effects: { budget: { requests: 2000, period: 'day' }, tags: ['mini'] },
});
```

| Field | Rules |
| --- | --- |
| `id` | `^[a-z0-9][a-z0-9_-]{0,47}$`. Saving an existing id replaces the rule. |
| `name` | 1–60 characters. |
| `match.models` | Up to 16 globs (`*` wildcard, case-insensitive, ≤ 200 characters). `[]` matches any model. |
| `match.endpoints` | `responses`, `chat`, `messages`, `completions`, `embeddings`, `images`, `models`, `other`. `[]` matches any. |
| `effects.provider` | A provider id. Matching requests go there while it exists and is enabled; otherwise routing is normal. |
| `effects.model` | Rewrites the model sent upstream (1–200 characters). |
| `effects.block` | `{ status: 400 \| 403 \| 429 \| 503, message }`: the caller gets `{"error":{"message":…,"type":"switchboard_rule"}}`. The message is 1–200 characters of plain text. |
| `effects.rateLimit` | `{ requests: 1–100000, windowSeconds: 1–86400 }`. |
| `effects.budget` | `{ requests?: 1–10000000, tokens?: 1–10000000000, period: "hour" \| "day" }`. |
| `effects.headers` | Up to 8 extra upstream headers. Names match `^X-[A-Za-z0-9-]{1,62}$` and are never `X-Switchboard-*`, `X-Forwarded-*`, `X-Real-IP`, `X-Stainless-*` or credential-like (containing auth, token, key, secret, session, cookie, password or signature). A header the client or provider already set is never replaced. Values are ≤ 256 printable ASCII characters. |
| `effects.tags` | Up to 8 tags (`^[a-z0-9][a-z0-9_.-]{0,31}$`), shown on the request in Activity and found by Activity search. |
| `effects.stop` | Later rules are not evaluated. |

**How rules apply:**

- Rules run in order, and every enabled matching rule contributes until one
  says `stop`.
- `provider`, `model` and `block` take the first match. Tags add up.
  Headers keep the first value per name.
- Every matching rate limit and budget is enforced. Going over one answers
  `429` with `Retry-After`.
- **Budget windows** reset on the local hour or at local midnight. **Hit and
  budget counters live in memory** and start again when the app restarts.
- At most 64 request rules in all.

> Request rules apply **only to the local relay**. They never touch the public
> tunnel or model tests, so a plugin cannot use them to change what tunnel
> clients get.

### Retry rules

```js
await switchboard.rules.saveRetryRule({
  id: 'overloaded', name: 'Retry overloaded upstreams', enabled: true,
  match: { statuses: [529, 503], contains: ['overloaded'], errorCodes: [], providers: [], models: [] },
  action: 'retry', maxAttempts: 3, delayMs: 2000,
});
```

| Field | Rules |
| --- | --- |
| `match.statuses` | Up to 16 statuses, 400–599. `[]` matches any error status. |
| `match.contains` | Up to 8 snippets of 2–120 characters, matched case-insensitively in the error text. |
| `match.errorCodes` | Up to 8 relay error codes, such as `transport`, `stream_incomplete` or `upstream_status`. |
| `match.providers`, `match.models` | Up to 16 provider ids and 16 model globs. `[]` matches any. |
| `action` | `retry`, `rotate_key` (try the provider's next API key), `switch_provider` (try the next provider; on the tunnel it acts as `retry`), or `fail` (stop at once with the generic error). |
| `maxAttempts` | 1–10 extra attempts this rule may spend on one request, within the relay's own ceiling. Ignored for `fail`. |
| `delayMs` | 0–60000. `0` uses the normal backoff and honours `Retry-After`. |

At least one of `statuses`, `contains` and `errorCodes` must be non-empty.

**How retry rules apply:**

- The first matching rule wins. When none matches, the built-in retry
  handling runs as before.
- **The built-in patterns are never shown**, to plugins or on the Rules
  page.
- Retry rules apply to the local relay and to tunnel requests, because a
  retry reveals nothing.
- They are checked only after the content-policy check, so **a content
  refusal is never retried**, and they are never consulted for successful
  answers.
- At most 64 retry rules in all.

### Errors, tester and counters

| Call | Resolves |
| --- | --- |
| `rules.recentErrors(limit?)` | `{ errors: [{ signature, status, errorCode, detail, providerName, model, count, lastAt, coveredBy? }] }`: recent failures grouped by status, code and redacted detail. `coveredBy` names the custom retry rule that already handles a group. `limit` is 1–100 (the default is 50). |
| `rules.test({ model, path, status?, errorText?, errorCode?, providerId? })` | `{ requestRules, effects, retryRule? }`: which request rules match (in order), their merged effects, and the retry rule an error would hit. Nothing is sent. |
| `rules.stats()` | `{ rules: { [id]: { hits, lastHitAt?, windowRequests, windowTokens, windowResetsAt? } } }`. |
| `rules.list()` | `{ requestRules, retryRules }`: every rule, with `owner`. |
| `rules.deleteRequestRule(id)`, `rules.deleteRetryRule(id)` | `{ deleted: true }`. |

The same calls are available as `control.call('rules.…')`. The live topic
`rules.changed` (needs `relay:rules`) fires when any rule changes.

## Relay hooks

> **`relay:content` reads and can change the prompts and answers that pass
> through your local relay. It never applies to the public tunnel.** The
> permission starts unticked.

```js
const remove = await switchboard.relay.onRequest({ models: ['gpt-*'], endpoints: ['chat'], timeoutMs: 500 }, (request) => {
  if (!request.body) return { action: 'continue' };        // bodyOmitted: too large to read or rewrite
  const body = JSON.parse(request.body);
  body.messages?.unshift({ role: 'system', content: 'Answer in British English.' });
  return { action: 'rewrite', body: JSON.stringify(body), tags: ['prefixed'] };
});
```

### `relay.onRequest(filter, handler)`

Runs before routing, for each matching local request.

- **Request.** The handler gets `{ activityId, model, endpoint, method, path,
  bytes, body? }`. `body` is the request text when it is at most 192 KiB;
  otherwise `bodyOmitted: true` and it cannot be rewritten. `activityId` is the
  same ID `relay.onResponse` gets and the request's row in Activity shows, so a
  plugin can pair the two. A request that never reaches Activity (for example
  one the route cannot serve) leaves its ID unused, so `relay.onResponse` may
  never arrive for an `activityId`: do not hold state for it forever.
- **Decision.** The handler returns one of:
  - `{ action: 'continue' }`;
  - `{ action: 'rewrite', body?, model?, tags? }`. The body must be valid
    JSON of at most 192 KiB, or it is ignored. Up to 8 tags;
  - `{ action: 'block', block: { status, message } }`, answered like a
    request-rule block.
- **Order.** Hooks run one after another in the order they registered,
  inside a 5-second budget shared by all of them. A hook still running when
  that shared budget runs out counts as timed out; a client that hangs up does
  not count against any hook.
- **Timeouts.** `timeoutMs` is 100–5000 (the default is 2000). A hook that
  times out lets the request through unchanged (**fail-open**). After 3
  timeouts in a row it is removed.

### `relay.onResponse(filter, handler)`

Runs after the client already has its answer, so it can never delay or change
it.

- The handler gets the request metadata and
  `{ status, errorCode?, latencyMs, tokens?, body? }`.
- `body` is present only when the relay buffered the answer and it is at most
  192 KiB.

### Limits and guarantees

- **Never on the tunnel.** Hooks never run for the public tunnel or for model
  tests.
- **Capacity.** At most 8 hooks run in the whole app, and 2 per plugin per
  phase (`limit`).
- **Removal.** Both calls resolve a function that removes the hook. Hooks also
  go when the plugin stops or loses the permission.
- **Logging.** Bodies never reach any log.
- **Speed.** Keep handlers fast: the operator's requests wait for them.

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

Without `control:sensitive`, provider results and the `providers.changed`
topic leave out `baseUrl`, `authHeader`, `modelsPath` and `chatPath`: a
read-only plugin sees which providers exist, never where they point.

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

**`relay:rules`** (1.0.66) — `rules.list`, `rules.stats`, `rules.test`,
`rules.recentErrors`, `rules.saveRequestRule`, `rules.deleteRequestRule`,
`rules.saveRetryRule`, `rules.deleteRetryRule`. These are the same calls as
[`switchboard.rules`](#relay-rules), and the Switchboard stamps the owner
either way.

**Never available**, whatever is granted:

- `plugins.*`, because a plugin could grant itself more;
- `system.*` (handshake and lifecycle);
- `backup.*` (the plaintext credential export);
- `appearance.update` (the operator's own appearance);
- `updates.install`;
- `rules.reorder*`, because the operator decides the rule order;
- `relayhooks.*`, used only by the window on the plugin's behalf.

**Topics** for `events.on` (`control:read`): `activity.changed`,
`appearance.changed`, `clients.changed`, `codex.changed`,
`guardrails.changed`, `guardrails.finding`, `keys.changed`, `models.tested`,
`notifications.raised`, `providers.changed`, `providers.health`,
`relay.changed`, `routes.changed`, `settings.changed`, `tunnel.changed`,
`updates.installProgress`, `updates.stateChanged`; and `rules.changed`
(needs `relay:rules`).

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

`run` options:

| Option | |
| --- | --- |
| `--seconds <n>` | How long to let it run (default 3). |
| `--grant all|<perm,perm>` | What to grant (default: every declared permission). |
| `--event <pageId>:<action>` | Shows the page, then presses the action. Repeatable. |
| `--event <surface>:<action>` | Presses an action on a slot, such as `widget:status:refresh`. |
| `--event <actionId>` | Clicks a registered row action or header button; the event has `pageId: ''` and a fictional `target`. |
| `--command <id>` | Runs one of its palette commands. Repeatable. |
| `--key <key>` | Presses a key (`KeyboardEvent.key`: `Enter`, `ArrowLeft`, `x`) on the first canvas with `keys: true`. Repeatable. |
| `--setting <key>=<value>` | A setting value, as JSON or plain text. Repeatable. For a `secret` it only marks the secret as set; the value is never used. |
| `--fetch <url-prefix>=<body>` | Answers `net.fetch` for URLs starting with the prefix with `200` and this body. Repeatable. |
| `--dialog <actionId>` | The footer action every `ui.dialog` answers (default: dismissed). |
| `--confirm yes|no` | The answer `ui.confirm` gets (default yes). |
| `--app-version <x.y.z>` | The app version the plugin sees (default: this checkout's). |
| `--json` | Prints the report as JSON. |

Events, actions and commands count as gestures, so confirm, dialog, file and
clipboard calls behave as they would in the window — slot events
(`settings:budget:reset`) included.

The 1.0.66 APIs are stand-ins with the window's checks:

- **Network.** `net.fetch` is never sent. It answers `not_allowed` unless a
  `--fetch` prefix matches, and a host outside `network.hosts` is still
  refused.
- **Canvas.** Draw lists are checked and counted; each canvas gets 5 frames.
- **In memory.** Collections, the bus and the fixture's rules live in memory.
  The bus is local and, as in the window, never delivers to the sender.
- **Files.** `files.open` is cancelled; `files.save` is recorded.
- **Relay hooks.** Each hook sees one fictional chat request; the report shows
  the decision (`rewrite … tags=…`, `observed`).
- **Reported.** Slots, the layout, dialogs and tones appear in the report.

Until this checkout is 1.0.66, pass `--app-version 1.0.66` to plugins that
require it; otherwise `run` reports *not started … needs Luxury Switchboard
1.0.66 or newer*.

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

### Dev folder with hot reload (1.0.66)

To skip the pack-and-install step, link the source folder itself: **Plugins →
Load from folder…** takes the folder's absolute path.

- **Reloading.** The Switchboard checks the folder every second. When
  `plugin.json`, `main.js` or `style.css` changes, it packs the folder exactly
  as `pnpm plugin pack` would, validates it and upgrades the plugin in place.
  Save in your editor and the plugin restarts with the new code.
- **Problems.** A problem (a syntax error, an invalid field) keeps the
  **last good version** running and shows the problem on the plugin's row.
- **New permissions.** Permissions you add while developing arrive
  **ungranted**. Grant them from **Permissions** on the row, so a change
  in the folder can never give itself more. Adding a host to
  `network.hosts` likewise withdraws `net:fetch` until you grant it again.
- **Scope.** The plugin's row shows a **Dev** badge, the folder and
  **Unlink**. At most 4 folders can be linked, and links survive restarts.
  Only that folder is read, never anything outside it.
- **Linking over an installed plugin.** Linking a folder whose `id` is already
  installed (and not already linked to that folder) starts it **ungranted**:
  grant its permissions again from the row.
- **Leaving.** Unlinking keeps the last loaded version installed as a normal
  plugin. Installing a package file over a linked plugin also ends the link.

### Install from a link (1.0.67)

**Plugins → Install from link…** takes the address of a package file or of a
source folder and opens the same review as a file: nothing is installed
until you confirm, and the review shows where the package came from.

| Link | What is read |
| --- | --- |
| `github.com/<owner>/<repo>` | The folder at the root of the default branch. |
| `github.com/<owner>/<repo>/tree/<ref>/<dir>` | That folder. |
| `github.com/<owner>/<repo>/blob/<ref>/<file>` or `/raw/…` | That file; a link to `plugin.json` means its folder. |
| `github.com/<owner>/<repo>/releases/download/<tag>/<asset>` | The release asset, a packed package file. |
| `raw.githubusercontent.com/…` | That file, or its folder for `plugin.json` / a trailing `/`. |
| Any other `https://` address | A package file, or a folder when the path ends in `/` or `plugin.json`. |

A folder is `plugin.json` plus `main.js` and `style.css` when present —
exactly what `pnpm plugin pack` assembles, so a repository laid out like
[`docs/plugins/examples/snippets`](plugins/examples/snippets) installs as it
is. `https://` may be left out. A branch whose name contains `/` cannot be
told apart from a folder in a GitHub page link: use its
`raw.githubusercontent.com` link instead.

The download runs in the sidecar with the `net.fetch` rules: public HTTPS on
the standard port only, no user name or password in the link, no private or
local addresses (redirects included, at most 5), 256 KiB per file and
30 seconds in all. Nothing is sent but a plain `GET` with the app's user
agent.

**Updating.** The plugin remembers the link it came from (shown as
*Installed from* in Details). **Update from link** on its row fills that link
in and opens the same review: nothing changes until you confirm, and new
permissions arrive ungranted. Upgrades are never automatic. Installing from a
file or linking a dev folder forgets the link, and so does a link with a
query or fragment (`?token=…`), which may carry a credential: such a plugin
has no **Update from link**.

### Save package

**Save package** on a plugin's row saves the installed package as
`<id>-<version>.json`, in the format `pnpm plugin pack` writes, so it installs
again anywhere with **Install from file**. It holds the manifest, script and
stylesheet only: never setting values, secrets, storage or collections, and
never the link it came from.

## Recipes

**Rate-limit one client's model and tag it**

```js
await switchboard.rules.saveRequestRule({ id: 'gpt4-cap', name: 'GPT-4 class: 30/min', enabled: true,
  match: { models: ['gpt-4*'], endpoints: [] }, effects: { rateLimit: { requests: 30, windowSeconds: 60 }, tags: ['capped'] } });
```

**Turn a recurring error into a retry rule.** List
`rules.recentErrors()`, pick a group without `coveredBy`, and save a retry rule
with its `status` and a distinctive snippet of its `detail`. The
[rule pack example](#examples) does this from a button.

**Notify a chat when the relay stops.** Follow `relay.changed` and
`net.fetch` a message API with `{{secret:token}}` in the header. Collapse
repeats with a cooldown kept in a variable. The [status widget
example](#examples) shows the secret header and every failure code.

**Show something on a request in the inspector.** Use `registerInspectorTab`,
render on its `show` event for `event.context.id`, and
`control.call('activity.list')` to find the record.

**Keep a high score.** Use `db.collection('scores').put('best', { score })`;
read it once at start.

**A game loop.** Use a `canvas` with `keys` and `frames`. Advance state in
`onFrame` using `dtMs`, `draw` once per frame, change direction in `onInput`,
and pause on `blur`. See [Tetris](#examples).

**Import and export settings.** Use `files.open({ accept: ['.json'] })` and
`files.save(...)` from a button handler, so both count as a gesture.

**Two plugins cooperating.** A budget plugin `bus.publish('budget.alert', …)`
and a notifier subscribes. Neither needs the other's permissions.

## Limits

| What | Limit |
| --- | --- |
| Installed plugins | 24 |
| Permissions per package | 24 |
| Package file | 512 KiB on disk; 256 KiB as one command on the wire |
| Script / stylesheet | 128 KiB / 64 KiB |
| Theme tokens | 96, each value ≤ 120 bytes |
| Settings | 24 per plugin, of which ≤ 8 secrets (≤ 4096 characters each) |
| Network hosts | 1–16 per package |
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
| Widgets / inspector tabs / settings sections | 4 / 4 / 2 |
| Actions / header buttons | 8 / 2 |
| Layout | one plugin at a time; Plugins and Settings always shown |
| Dialogs | one open per plugin; only within 10 s of an operator gesture |
| Canvas | 1–2048 × 1–2048 units; a draw ≤ 10000 ops and 512 KiB; `path` ≤ 800 numbers; `text` ≤ 200 characters |
| Draw + frame + input messages | 240 a second per plugin (separate from the 500 per 5 s) |
| Sound | 40–4000 Hz, 10–2000 ms, volume scaled to ≤ 0.3; 20 tones a second |
| Collections | 16 per plugin, 64 KiB per document, 2 MiB total, 300 writes a minute, `list` 1–200 |
| Files | open ≤ 2 MiB, save ≤ 5 MiB; only within 10 s of an operator gesture |
| Bus | 32 KiB per message, 120 a minute |
| `net.fetch` | HTTPS:443 to listed hosts; request ≤ 64 KiB, response ≤ 192 KiB; 60 a minute, 4 at once; timeout 1–30 s (15 s) |
| Request / retry rules | 64 / 64 in all |
| Relay hooks | 8 in the app, 2 per plugin per phase; 100–5000 ms each (2000) in a 5 s total; body ≤ 192 KiB; removed after 3 timeouts in a row |
| Markdown / image | 20000 characters / 256 KiB `data:` URL |
| Dev folders | 4 linked, checked every second |

## Errors

Rejected calls carry `error.code` and `error.message`:

| Code | Meaning |
| --- | --- |
| `permission_denied` | The permission this needs was not granted. |
| `not_allowed` | The method or topic is not available to plugins at all; for `net.fetch`, a URL that is not `https` on port 443 or whose host is not in `network.hosts`. |
| `invalid_argument` | An argument has the wrong shape; the message names the field. |
| `invalid_tree` | `ui.render` got a tree it cannot draw; the message has the path. |
| `not_found` | Unknown page, surface or route. |
| `limit` | Too many pages (8), commands (32), status readouts (3), topics (24), widgets, tabs, sections, actions, header buttons, rules or hooks. |
| `busy` | 16 control calls are already in flight, or a `ui.confirm` or dialog is already open. |
| `rate_limited` | Over a rate: notifications (6), storage writes (120), collection writes (300), bus messages (120) or fetches (60) a minute, or 4 fetches at once. |
| `too_large` | A stored value or document, clipboard text, a file, a fetch body or answer, a bus message or the call's arguments are too large. Oversized arguments, page trees (256 KiB), draws (512 KiB) and tones (1 KiB) are refused inside the plugin before anything is sent; a call over the 252 KiB sidecar payload budget is refused too. |
| `plugin_storage_full` | Storage would exceed 64 keys / 64 KiB, or collections 2 MiB. |
| `needs_gesture` | `ui.confirm`, `clipboard.writeText`, `ui.dialog`, `files.open` or `files.save` outside [the gesture window](#the-gesture-rule). |
| `conflict` | The command's shortcut is already bound, or another plugin holds the layout. |
| `blocked_address` | `net.fetch`: the host resolved to a loopback, private, documentation, reserved or otherwise internal address. |
| `invalid_plugin` | `net.fetch`: a malformed request — an unknown method, `timeoutMs` out of range, more than 32 headers, a header over 8 KiB (after secrets are filled in), a bad header name or a line break in a value. |
| `missing_secret` | `net.fetch`: a `{{secret:KEY}}` that is not declared or not set. |
| `timeout` | `net.fetch` ran out of time. |
| `network` | `net.fetch` could not connect or the connection failed. |
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
  restarting or crashing takes back in one step:
  - its pages, commands and status readouts;
  - its widgets, tabs, sections, actions and header buttons;
  - its layout, dialogs and relay hooks;
  - its stylesheet and theme.

  An open plugin page shows that the plugin stopped instead of stale
  content. Its **rules stay** while it is disabled, because they are the
  operator's relay configuration and can be switched off on the Rules page.
  They are deleted when the plugin is removed.
- **Restart** happens automatically when the plugin is upgraded or its grants
  change, and manually from its row. A restart is a fresh worker: in-memory
  state is gone, `storage` is not.
- **Upgrading** is installing a package with the same `id`: storage and the
  install date are kept, previous grants carry over and the plugin is enabled.
- **Removing** deletes the package, its storage, collections, secrets and
  rules from the control plane.
- **Backups** include packages, grants, settings and storage, but **never
  secrets**: re-enter them after a restore.
- There is no auto-update and no remote install: plugins arrive only as files
  the operator chooses.

## Safe mode

A stylesheet that hides every control, or a plugin that misbehaves, is never
a dead end. Press **Ctrl+Alt+Shift+S** (or start the window with `?safe=1`)
to reload in **safe mode**: no custom CSS, no plugin stylesheets, themes or
scripts — while the Plugins and Appearance pages keep working, so you can
disable or remove the culprit. The runtime bar shows *Safe mode*, and the
Plugins and Appearance pages offer *Leave safe mode*.

Because no script runs, safe mode also takes back all of the following, which
need a running plugin:

- the layout, so every sidebar item is back and the start page is
  *Overview*;
- widgets, tabs, sections, actions and header buttons;
- dialogs and relay hooks.

Request and retry rules keep working: they belong to the relay. Disable them
on the **Rules** page.

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
8. **Network only through the Switchboard.** `net.fetch` is the plugin's only
   way out:
   - HTTPS on port 443, to the hosts the operator saw on the review screen;
   - SSRF protection at connect time, so loopback, private, link-local,
     CGNAT, multicast and unspecified addresses are refused even after a DNS
     change;
   - no redirects, forbidden headers dropped, and a User-Agent that names
     the plugin;
   - capped sizes and rates.
9. **Secrets stay out of reach.** Secret settings are:
   - stored encrypted, never in plaintext;
   - never returned to the script or the window;
   - put into requests only by Go, and redacted from answers as `[secret]`;
   - left out of backup exports;
   - deleted with the plugin.
10. **The relay's protections are not for plugins to see or weaken.**
    - Built-in retry patterns are never exposed. Custom retry rules run only
      after the content-policy check, so a refusal is never retried.
    - Request rules and hooks touch only the **local relay**: the **public
      tunnel** and its privacy guarantees are unaffected.
    - Rules cannot add `X-Switchboard-*`, `X-Forwarded-*` or forbidden
      headers.
11. **The operator can always get back.** A layout can never hide *Plugins*
    or *Settings*. A canvas never captures Tab, Ctrl/Alt/⌘ chords or the
    safe-mode chord. Safe mode resets layout, slots, dialogs and hooks at
    once.
12. **Slots are signed.** Every widget, tab, section, action and header
    button is framed or labelled with the plugin's name, so it cannot pass
    itself off as the Switchboard's own.

A plugin granted `control:write` can change how your relay routes traffic,
and one granted `control:sensitive` can manage credentials and the public
tunnel. `relay:rules` can block or reroute local requests; `relay:content`
can read and change local prompts and answers; `net:fetch` can send what the
plugin reads to the listed hosts. Only install plugins from people you trust,
and grant the least that works.

## Examples

Every example lives in [`docs/plugins/examples`](plugins/examples), is
commented, and passes `pnpm plugin validate` and `pnpm plugin run`. All
twelve ship with the app (**Plugins → Examples**); installing one opens the
usual review. Together they use every permission except `control:sensitive`.

**Package files.** A test keeps them identical to the built-in copies.

| File | Shows |
| --- | --- |
| [`dusk-theme.json`](plugins/examples/dusk-theme.json) | A theme plus a stylesheet scoped to it. No script. |
| [`latency-board.json`](plugins/examples/latency-board.json) | A live page: metrics, a table with `duration`/`number` columns, a refresh button, a period switch, a toggle, `show`/`hide` and event-driven refresh with a debounce. Also a sidebar badge counting slow requests, two operator settings (`settings.get`/`onChange`) and a palette command on `Alt+L` that falls back to no shortcut on `conflict`. Read-only. |
| [`quick-switch.json`](plugins/examples/quick-switch.json) | Palette commands kept in step with the provider list, relay start/stop, and notifications for each outcome. |

**Source folders.** Each is a `plugin.json` plus `main.js`. The app builds
its copy from these files the way `pnpm plugin pack` does. Link one with
**Load from folder…** to experiment, or install it from a link to this
repository's folder.

| Folder | Shows | Try it |
| --- | --- | --- |
| [`usage-report`](plugins/examples/usage-report) | The analytics report as a page: `tabs`, a `segmented` period switch, `select` and `slider` controls, bar and line `chart`s, a formatted `table`, `details` with `markdown`, a CSV export through `files.save`, and a **Details** tab in the Activity inspector (`ui:slots`) that looks the request up on `show`. | `--event main:export` |
| [`daily-budget`](plugins/examples/daily-budget) | Counts finished requests from `activity.changed`, shows `Budget n/limit` in the status bar, notifies at the warning level and at the limit, publishes `budget.alert` on the `bus`, keeps the day in `storage`, and adds a Settings section with a reset behind `ui.confirm`. | `--setting limit=10 --event settings:budget:reset --confirm no` |
| [`snippets`](plugins/examples/snippets) | A prompt library in a collection: a form to add snippets, one palette command per snippet that copies it (`ui:clipboard`), and JSON import/export through `files.open`/`files.save`. | `--event main:export` |
| [`tetris`](plugins/examples/tetris) | A playable Tetris on a `canvas`: a frame loop with `dtMs`, arrow keys and space from `onInput`, pause on `blur`, theme-token colours, a line-clear tone, and the high score in a collection. | `--key ArrowLeft --key " "` |
| [`request-rules`](plugins/examples/request-rules) | A rule pack: a request rule (tag and rate-limit `draft-*` models) and a retry rule (`503`/`529` "overloaded" → next provider), installed and removed with one button. Its page lists recent errors that no rule covers yet. `relay:rules`. | `--event pack:install --event pack:remove` |
| [`relay-redactor`](plugins/examples/relay-redactor) | A `relay.onRequest` filter that replaces e-mail addresses and phone numbers in prompts with `[email]`/`[phone]` and tags the request, plus a `relay.onResponse` observer that logs only status codes. Local relay only, never the tunnel. **`relay:content` reads your prompts: only install it if you want this.** | (no options) |
| [`status-widget`](plugins/examples/status-widget) | An Overview widget fed by `net.fetch` with a **secret setting** sent as `{{secret:apiKey}}` in a header, and a clear state for each failure code. `ui:slots`, `net:fetch`. | `--setting apiKey=fictional-key --fetch 'https://status.example.com/={"status":"operational","components":[]}'` |
| [`overview-actions`](plugins/examples/overview-actions) | An Activity inspector action that pins a request into a collection, an Overview widget listing pins, and a header button that opens a `ui.dialog` with a danger action. `ui:slots`, `ui:dialogs`, `storage`. | `--event pin --event review --dialog clear` |
| [`layout`](plugins/examples/layout) | `ui.setLayout` per role (order, hidden pages, start page), `ui.resetLayout` from a palette command, and handling `conflict`. `ui:layout`. | `--setting role=publisher --command reset` |

**Templates.** `pnpm plugin init --kind page|command|theme` copies
[`docs/plugins/templates`](plugins/templates).

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
| A canvas stays blank | Draw to the right `surface` and canvas `id`, after `render`; check the log for a rejected draw (an unknown op or field). |
| `onFrame` never runs | The canvas needs `frames: true` and must be on screen (frames pause while it is hidden). |
| Keys do nothing | The canvas needs `keys: true` and focus: click it first. Tab and Ctrl/Alt chords never reach a canvas. |
| `net.fetch` rejects with `not_allowed` | The host is not in `network.hosts`, the URL is not `https://`, or it names a port other than 443. |
| `net.fetch` rejects with `blocked_address` | The name resolves to an internal address; plugins cannot reach local or private networks. |
| `missing_secret` | Declare the secret in `settings` and ask the operator to set it (**Settings** on the plugin's row). |
| A hook stopped running | It timed out three times in a row and was removed; restart the plugin after making it faster. |
| A dev folder does not reload | Its row shows the problem; the last good version keeps running until the folder validates. |
