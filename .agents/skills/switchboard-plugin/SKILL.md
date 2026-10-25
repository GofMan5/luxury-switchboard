---
name: switchboard-plugin
description: Writes, fixes and reviews Luxury Switchboard plugins — single-file JSON packages (schema 1) that add sidebar pages, command-palette commands with keyboard shortcuts, runtime-bar readouts, badges, notifications, operator settings, colour themes and shell CSS, scripted against the sandboxed `switchboard` worker API and the relay control plane. Covers scaffolding with `pnpm plugin init`, validating, dry-running, packing and installing, the declarative UI node tree, permissions, limits and every validator message. Use when the user asks to create, extend, debug or review a Switchboard plugin, theme or plugin page; mentions plugin.json, main.js, switchboard.ui.render, switchboard.control.call, docs/PLUGINS.md or a plugin error such as invalid_tree, permission_denied, needs_gesture or "is not a field of"; or wants to restyle or add a feature to Luxury Switchboard without changing the app itself.
---

# Switchboard plugins

A plugin is one JSON package. Its script runs in a Web Worker with no DOM and no network; the global `switchboard` is its only way out. Pages are JSON node trees that the window draws with its own components.

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
5. Run `pnpm plugin pack plugins/<name>` to write `dist/<id>-<version>.json`. The operator installs that file from Plugins → Install.

## Hard rules

- Declare only the permissions the script uses, and gate optional ones with `switchboard.has(perm)`. `control:sensitive` is never granted by default.
- `fetch`, `XMLHttpRequest`, `WebSocket`, `importScripts`, `indexedDB` and `postMessage` throw. Reach the app only through `switchboard.control.call(method, payload)`. `plugins.*`, `system.*`, `backup.*`, `appearance.update` and `updates.install` are never allowed.
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
| `settings.get(key)`, `.all()`, `.onChange((values, changed) => …)` | Needs no permission. |
| `navigate(route)`, `log.info/warn/error`, `plugin`, `app.version`, `has(perm)` | |

## Node cheat-sheet (each node has `type` and an optional `key`)

- **Layout:** `stack{gap,children}`, `row{gap,align,wrap,children}`, `grid{columns,gap,children}`, `panel{title,subtitle,actions,children}`, `tabs{name,value,tabs:[{id,label,children}]}` and `divider`.
- **Text:** `heading{text,level}`, `text{text,tone,mono,size,weight}`, `code{text}`, `link{text,href}`, `callout{tone,title,text,actions}` and `empty{title,hint}`.
- **Data:** `metric{label,value,detail,tone}`, `metrics{items}`, `kv{items:[{key,value}]}`, `badge{text,tone}`, `dot{tone,text}`, `progress{value 0–1,indeterminate,label,tone}`, `chart{kind,label,values,labels,min,max,tone,height}`, `list{items:[{title,detail,meta,tone}],empty}` and `table{columns:[{key,label,align,mono,width,format}],rows,empty,rowAction}`.
- **Controls:** `button{text,action,variant,disabled,icon}`, `input{name,label,value,placeholder,kind,action,min,max,step,maxLength,hint,error,required,disabled}`, `textarea{…,rows}`, `select{name,label,value,options}`, `segmented{name,label,value,options}` and `toggle{name,label,value}`.

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
- **A number input's value is a string.** `e.form.limit` from a number input must be converted with `Number(...)`.

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
