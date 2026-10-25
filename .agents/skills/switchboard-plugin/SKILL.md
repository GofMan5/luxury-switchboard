---
name: switchboard-plugin
description: Writes, fixes and reviews Luxury Switchboard plugins — single-file JSON packages that add pages, Overview widgets, inspector tabs, actions, dialogs, canvas games (even Tetris), commands, readouts, notifications, settings and secrets, themes and CSS, collections, files, a plugin bus, allowlisted HTTPS fetch, relay request/retry rules and relay hooks, scripted against the sandboxed `switchboard` worker API. Covers `pnpm plugin init/validate/run/pack`, dev-folder hot reload, the UI node tree, permissions, limits and validator messages. Use when the user asks to create, extend, debug or review a Switchboard plugin or theme; mentions plugin.json, main.js, switchboard.ui.render, ui.draw, net.fetch, switchboard.rules, switchboard.relay, control.call, docs/PLUGINS.md or an error such as invalid_tree, permission_denied, needs_gesture, blocked_address or "is not a field of"; or wants to add a feature to Luxury Switchboard without changing the app.
---

# Switchboard plugins

A plugin is one JSON package. Its script runs in a Web Worker with no DOM and no direct network; the global `switchboard` is its only way out. Pages, widgets and dialogs are JSON node trees that the window draws with its own components; a `canvas` node is painted with display lists (`ui.draw`). Everything marked 1.0.66 needs `"minAppVersion": "1.0.66"`.

| What you need | Where it is |
| --- | --- |
| Every field, limit and error | [REFERENCE.md](REFERENCE.md) |
| Types | `docs/plugins/switchboard.d.ts` |
| Guide | `docs/PLUGINS.md` |
| Examples | `docs/plugins/examples/` |

Never invent a field; check it in the `.d.ts` file.

## Workflow (from the repo root)

1. Scaffold with `pnpm plugin init plugins/<name> --kind page|command|theme [--id acme.<name>] [--name "<Name>"]`. It writes `plugin.json` (the package without `script` and `css`) and `main.js` or `style.css`.
2. Begin `main.js` with `// @ts-check` and `/// <reference path="../../docs/plugins/switchboard.d.ts" />`. The file is the body of an async function, so top-level `await` works.
3. Run `pnpm plugin validate plugins/<name>`. It applies the app's own checks. Fix every `✗` line until it prints `✓ … is valid`; `--json` gives machine-readable output.
4. Run `pnpm plugin run plugins/<name> [--event <pageId>:<action>] [--command <id>] [--setting key=value] [--json]`. It drives the real host against fixture data and prints pages, trees, commands and logs. A fault or a refused tree exits 1. It is **not a sandbox**: run only code you wrote.
   - 1.0.66 flags: `--event <actionId>` (a row action or header button), `--event widget:<id>:<action>` (a slot), `--key <key>` (the first canvas with `keys: true`), `--fetch <url-prefix>=<body>` (mock answer), `--dialog <actionId>` (every dialog's answer), `--setting <secretKey>=x` (marks a secret as set).
   - Stand-ins: `net.fetch` is never sent and answers `not_allowed` without a matching `--fetch`; canvases get 5 frames; collections, bus and rules live in memory; `files.open` is cancelled; each relay hook sees one fictional chat request.
   - Add `--app-version 1.0.66` while the checkout is older.
5. Run `pnpm plugin pack plugins/<name>` to write `dist/<id>-<version>.json`. The operator installs that file from Plugins → Install.
   - Or link the folder: **Plugins → Load from folder…** reloads it every second while you edit.
   - A broken save keeps the last good version.
   - New permissions arrive ungranted.

## Hard rules

- Declare only the permissions the script uses, and gate optional ones with `switchboard.has(perm)`. `control:sensitive`, `net:fetch` and `relay:content` start unticked, and their review shows a warning.
- `fetch`, `XMLHttpRequest`, `WebSocket`, `importScripts`, `indexedDB` and `postMessage` throw.
  - Reach the app through `switchboard.control.call(method, payload)`.
  - Reach the internet only through `switchboard.net.fetch`: HTTPS:443, to the package's `network.hosts`, with no internal addresses and no redirects.
  - `plugins.*`, `system.*`, `backup.*`, `appearance.update`, `updates.install`, `rules.reorder*` and `relayhooks.*` are never allowed.
- Never put a secret in code or a `text` setting. Declare `{ "key", "type": "secret", "label" }` and write `{{secret:key}}` in a fetch header or body (never the URL); the script never sees the value.
- Never `net.fetch` at start-up; only on an event, a topic or a button, and debounce or cool down repeats.
- `ui.dialog`, `files.open` and `files.save` also need a gesture.
- Canvas games:
  - `draw` once per `onFrame`, and move by `dtMs`;
  - pause on `blur`;
  - use `token:<name>` colours.
  - Tab and Ctrl/Alt chords never reach a canvas.
- Relay hooks (`relay:content`):
  - keep them fast, because requests wait for them;
  - return `{ action: 'continue' }` when `body` is absent;
  - they are local relay only, fail-open, and removed after 3 timeouts in a row.
- Every `switchboard.*` call returns a Promise. `await` it and catch errors to read `e.code`. An uncaught error or rejection is a fault.
- `ui.render(pageId, tree)` replaces the whole page. Keep state in variables, build the tree with a pure `view()`, and render again after every change.
- `ui.confirm` and `clipboard.writeText` work only within 10 s of a page event or a command run. Call them from those handlers.
- Storage values are strings, so use `JSON.stringify`. Batch writes with `storage.setMany`; storage allows 120 writes a minute.
- Settings belong to the operator. Read them with `settings.get` and `settings.onChange`, and never write `setting:*` keys.
- Never put `"$schema"`, `"script"` or `"css"` in `plugin.json`. Point your editor at `docs/plugins/plugin.schema.json` instead.

## API cheat-sheet ([permission])

| Call | Notes |
| --- | --- |
| `ui.registerPage({id,title,icon?})` [ui:pages] | Returns `{route}`. At most 8 pages. |
| `ui.render(pageId, tree)` [ui:pages] | A bad tree gives `invalid_tree` with a path. |
| `ui.onEvent([pageId,] e => …)` | `e.kind` is action, change, show or hide. Also `e.action`, `e.name`, `e.value`, `e.row` and `e.form`. |
| `ui.setBadge(pageId, {text≤8,tone?}\|null)` [ui:pages] | A count on the sidebar entry. |
| `ui.setStatus(id, {text≤40,tone?,tooltip?}\|null)` [ui:status] | A runtime-bar readout. At most 3. |
| `ui.confirm({title,body?,confirmText?,tone?})` [ui:notify] | Resolves to a boolean. Needs a gesture. |
| `notify({title,body?,tone?})` [ui:notify] | At most 6 a minute. |
| `commands.register({id,title,group?,keywords?,shortcut?}, fn)` [ui:commands] | Shortcuts take the form `Alt+K`, `Ctrl+Shift+K` or `Ctrl+Alt+K`. |
| `clipboard.writeText(text)` [ui:clipboard] | Write-only. Needs a gesture. |
| `control.call(method, payload?)` [control:read/write/sensitive] | At most 16 calls in flight. |
| `events.on(topic, fn)` [control:read] | Resolves to an unsubscribe function. |
| `storage.get/set/remove/all/setMany/clear` [storage] | At most 64 keys, 8 KiB per value and 64 KiB in total. |
| `settings.get(key)`, `.all()`, `.onChange((values, changed) => …)` | Needs no permission. `settings.secrets` lists the secret names that are set (never values). |
| `navigate(route)`, `log.info/warn/error`, `plugin`, `app.version`, `has(perm)` | |
| **1.0.66** | |
| `ui.registerWidget/registerInspectorTab/registerSettingsSection({id,title,…})` [ui:slots] | Returns `{surface}` (`widget:<id>` and so on). Draw it with `ui.render(surface, tree)`. At most 4, 4 and 2. An inspector `show` event carries `context:{scope,id}`. |
| `ui.registerAction({id,title,scope:'activity'\|'provider'\|'key'})`, `ui.registerHeaderButton({id,title,icon})` [ui:slots] | A click is `{kind:'action', pageId:'', action:id, scope, target?}` and counts as a gesture. At most 8 and 2. |
| `ui.setLayout({hidden?,order?,startPage?})`, `ui.resetLayout()` [ui:layout] | Plugins and Settings are never hidden. One plugin at a time (`conflict`). |
| `ui.dialog({title,tree,actions,size?})` → handle [ui:dialogs] | `await handle.result` gives `{action\|null, form}`. Also `handle.update(tree)` and `handle.close()`. Events use `pageId 'dialog:<id>'`. Needs a gesture. |
| `ui.draw(surface, canvasId, ops)` | Fire-and-forget. At most 10000 ops; 240 draw, frame and input messages a second. |
| `ui.onFrame((dtMs,tMs)=>…)`, `ui.onInput(e=>…)` | Return a sync unsubscribe. Input kinds: keydown, keyup, pointer*, wheel, focus, blur. |
| `ui.sound.tone({frequency,durationMs,type?,volume?})` [ui:sound] | Returns void. |
| `ui.theme()`, `ui.onTheme(fn)` | `{mode, tokens}`. Needs no permission. |
| `db.collection(name).get/put/delete/list/clear/stats` [storage] | JSON documents ≤ 64 KiB, 2 MiB in all. `list({prefix,after,limit})` gives `{items:[{id,doc}], next?}`. |
| `files.open({accept?,maxBytes?})`, `files.save({name,text,mime?})` [files] | `open` gives `{name,text,size}` or null; `save` resolves a boolean. Both need a gesture. |
| `bus.publish(channel, payload)`, `bus.subscribe(channel, (payload, from)=>…)` [bus] | Other plugins only. ≤ 32 KiB, 120 a minute. |
| `net.fetch(url, {method?,headers?,body?,timeoutMs?})` [net:fetch] | `{status, ok, headers, text, json()}`. `{{secret:key}}` works in headers and body only. ≤ 32 headers of ≤ 8 KiB; `user-agent`, `cookie`, `host`… are dropped. |
| `rules.saveRequestRule/saveRetryRule/delete*/list/test/stats/recentErrors` [relay:rules] | Own rules only. Request rules affect the local relay only. |
| `relay.onRequest(filter, req => decision)`, `relay.onResponse(filter, (req, res) => …)` [relay:content] | Resolve a remover. Never on the tunnel. |

## Node cheat-sheet (each node has `type` and an optional `key`)

- **Layout:** `stack{gap,children}`, `row{gap,align,wrap,children}`, `grid{columns,gap,children}`, `panel{title,subtitle,actions,children}`, `tabs{name,value,tabs:[{id,label,children}]}` and `divider`.
- **Text:** `heading{text,level}`, `text{text,tone,mono,size,weight}`, `code{text}`, `link{text,href}`, `callout{tone,title,text,actions}` and `empty{title,hint}`.
- **Data:** `metric{label,value,detail,tone}`, `metrics{items}`, `kv{items:[{key,value}]}`, `badge{text,tone}`, `dot{tone,text}`, `progress{value 0–1,indeterminate,label,tone}`, `chart{kind,label,values,labels,min,max,tone,height}`, `list{items:[{title,detail,meta,tone}],empty}` and `table{columns:[{key,label,align,mono,width,format}],rows,empty,rowAction}`.
- **Controls:** `button{text,action,variant,disabled,icon}`, `input{name,label,value,placeholder,kind,action,min,max,step,maxLength,hint,error,required,disabled}`, `textarea{…,rows}`, `select{name,label,value,options}`, `segmented{name,label,value,options}` and `toggle{name,label,value}`.
- **1.0.66:**
  - `markdown{text}`;
  - `image{src:data-URL ≤256 KiB,alt,width,height}`;
  - `slider{name,label,min,max,step,value}`, which sends a number;
  - `color{name,label,value:#rrggbb}`;
  - `details{summary,open,children}`;
  - `canvas{id,width,height,label,keys,pointer,frames,fit}`.
- **Draw ops:**
  - `clear{color}`;
  - `rect{x,y,w,h,fill,stroke,lineWidth,radius}`;
  - `line{x1,y1,x2,y2,stroke,lineWidth}`;
  - `circle{x,y,r,fill,stroke}`;
  - `path{points,closed,fill,stroke}`;
  - `text{x,y,text,color,size,align,baseline,weight,font}`;
  - `save`, `restore`;
  - `translate{x,y}`, `scale{x,y}`, `rotate{angle}`;
  - `alpha{value}`.

  Colours can be `token:accent` and so on.

Children that are `null` or `false` are skipped, so `cond ? node : null` works.

## Common mistakes and the exact message

- **Unknown field on a node (fields are strict).** The message is `tree.children[2].varient is not a field of a "button" node.` Use only the fields above; for example, `heading` takes `text`, not `title`.
- **Table rows that are not flat objects keyed by column key.** The message is `tree.rows[0] has none of the column keys (name, state); rows are flat objects keyed by column key.` Write `{ id, name, state }`, not `{ cells: […] }`.
- **Missing permission.** `permission_denied`: `This needs the "ui:pages" permission, which the plugin was not granted.`
- **Rendering before registering the page.** `not_found`: `Register page "main" before rendering it.`
- **Clipboard or confirm called at start-up or from a timer.** `needs_gesture`: `… only works within 10 seconds of the operator using the plugin's page or command.`
- **Shortcut without Alt (or without Ctrl+Shift).** `shortcut: A shortcut needs Alt, or Ctrl with Shift, so typing and editing keys stay free: …`
- **Storing a value that is not a string.** `Stored values are strings; use JSON.stringify for anything else.`
- **Package mistakes.** `Unknown field "x".`, `"ui:pages", "storage" need a "script".` and `A theme needs the "ui:theme" permission.`
- **A number input's value is a string.** `e.form.limit` from a number input must be converted with `Number(...)`. A `slider` value is already a number.
- **`net.fetch` failures:**
  - a host that is missing from `network.hosts` gives `not_allowed`; a `{{secret:…}}` in the URL is sent literally, never filled in;
  - a host that resolves to a private address gives `blocked_address`;
  - an unset secret gives `missing_secret`;
  - a 5th request while 4 are in flight gives `rate_limited` at once (nothing queues);
  - an unknown method, >32 headers, a header >8 KiB or a bad header name gives `invalid_plugin`.
- **Collections.** `put(id, null)` is refused (use `delete`). `list` can return fewer than `limit` items with `next` set: loop on `next`.
- **Dev link.** Installing a package file over a dev-linked plugin ends the link.
- **Package mistakes (1.0.66):**
  - `net:fetch` without `network.hosts`, or `network` without `net:fetch`;
  - a `default` on a secret setting;
  - more than 8 secrets;
  - more than 24 permissions;
  - forgetting `minAppVersion: "1.0.66"`.
- **Canvas draw dropped.** The canvas is not on screen, or the 240 a second budget is spent. An unknown op is logged and the previous picture stays.

## Template: a page with a setting, a badge, a shortcut and the clipboard

```js
// @ts-check
/// <reference path="../../docs/plugins/switchboard.d.ts" />
const PAGE = 'main';
/** @type {{ id: string, name: string, state: string }[]} */
let rows = [];
await switchboard.ui.registerPage({ id: PAGE, title: 'Providers', icon: 'layers' });
/** @returns {Switchboard.UiNode} */
const view = () => ({ type: 'panel', title: 'Providers', actions: [{ type: 'button', text: 'Refresh', action: 'refresh' }],
  children: [{ type: 'table', columns: [{ key: 'name', label: 'Name' }, { key: 'state', label: 'State' }], rows, rowAction: 'copy' }] });
async function refresh() {
  /** @type {{ activeId: string, providers: { id: string, name: string }[] }} */
  const { providers, activeId } = await switchboard.control.call('providers.list');
  rows = providers.slice(0, Number(switchboard.settings.get('limit'))).map((p) => ({ id: p.id, name: p.name, state: p.id === activeId ? 'active' : '' }));
  await switchboard.ui.render(PAGE, view());
  await switchboard.ui.setBadge(PAGE, rows.length ? { text: String(rows.length) } : null);
}
switchboard.ui.onEvent(PAGE, async (e) => {
  if (e.action === 'refresh') await refresh();
  if (e.action === 'copy' && e.row !== undefined) await switchboard.clipboard.writeText(String(e.row));
});
switchboard.settings.onChange(() => refresh());
await switchboard.commands.register({ id: 'refresh', title: 'Refresh providers', shortcut: 'Alt+P' }, refresh);
await refresh();
```

`plugin.json` for this template: `"permissions": ["ui:pages", "ui:commands", "ui:clipboard", "control:read"]` and `"settings": [{ "key": "limit", "label": "Rows", "type": "number", "default": 50, "min": 1, "max": 500 }]`.
