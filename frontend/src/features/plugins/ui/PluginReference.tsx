import { useState } from 'react'
import { COLOR_TOKENS, DERIVED_TOKENS } from '../../../shared/contracts/tokens'
import { PLUGIN_ICONS } from '../../../app/plugin-icons'
import { Pill, Segmented } from '../../../shared/ui/chrome'
import { CONTROL_TOPICS, methodsFor } from '../domain/control-access'
import { CSS_BYTES, SCRIPT_BYTES, PLUGIN_LIMIT } from '../domain/manifest'
import { PERMISSION_SPECS } from '../domain/permissions'
import { RUNTIME_LIMITS, STORAGE_KEYS, STORAGE_TOTAL_BYTES, STORAGE_VALUE_BYTES } from '../domain/protocol'
import { TREE_LIMITS } from '../domain/ui-tree'
import { RiskPill } from './PluginDialogs'
import styles from './PluginsPage.module.css'

type Section = 'package' | 'api' | 'nodes' | 'permissions' | 'tokens'

const SECTIONS: readonly { readonly id: Section; readonly label: string }[] = [
  { id: 'package', label: 'Package' },
  { id: 'api', label: 'Script API' },
  { id: 'nodes', label: 'Page nodes' },
  { id: 'permissions', label: 'Permissions' },
  { id: 'tokens', label: 'Theme tokens' },
]

interface Entry { readonly name: string; readonly text: string; readonly needs?: string }

const API: readonly { readonly group: string; readonly entries: readonly Entry[] }[] = [
  {
    group: 'Identity',
    entries: [
      { name: 'switchboard.plugin', text: '{ id, name, version, granted } — who the script is and what it was granted.' },
      { name: 'switchboard.app', text: '{ version, capabilities } — the Switchboard version and the control-plane capabilities.' },
      { name: 'switchboard.has(permission)', text: 'True when the permission was granted. Check before optional features.' },
      { name: 'switchboard.onActivate(handler)', text: 'Runs once the host has accepted the script; runs at once if it already has.' },
      { name: 'switchboard.log.{debug,info,warn,error}(…)', text: 'Writes to this plugin’s log. console.* lands in the same log.' },
    ],
  },
  {
    group: 'Pages',
    entries: [
      { name: 'ui.registerPage({ id, title, icon? })', text: `Adds a sidebar page at #plugin/<plugin-id>/<id>, up to ${RUNTIME_LIMITS.pages}.`, needs: 'ui:pages' },
      { name: 'ui.render(pageId, tree)', text: 'Replaces the page with a node tree. Fields keep what the operator typed until the tree changes their value.', needs: 'ui:pages' },
      { name: 'ui.onEvent(pageId?, handler)', text: 'handler({ kind: "action" | "change" | "show" | "hide", action, name, value, row, form }). Returns an unsubscribe function.', needs: 'ui:pages' },
      { name: 'ui.unregisterPage(id)', text: 'Removes the page from the sidebar.', needs: 'ui:pages' },
      { name: 'ui.setStatus(id, { text, tone?, tooltip? } | null)', text: `Shows up to ${RUNTIME_LIMITS.status} readouts in the runtime bar; null removes one.`, needs: 'ui:status' },
    ],
  },
  {
    group: 'Commands and notifications',
    entries: [
      { name: 'commands.register({ id, title, group?, keywords?, shortcut? }, handler)', text: `Adds a palette command (Ctrl+K), up to ${RUNTIME_LIMITS.commands}. Registering the same id again replaces it. The handler runs when the operator chooses it.`, needs: 'ui:commands' },
      { name: 'commands.unregister(id)', text: 'Removes a palette command.', needs: 'ui:commands' },
      { name: 'notify({ title, body?, tone? })', text: `Raises a notification; at most ${RUNTIME_LIMITS.notifyPerMinute} a minute.`, needs: 'ui:notify' },
      { name: 'navigate(route)', text: 'Opens a page: "providers", "activity", "plugin/<id>/<page>"…' },
    ],
  },
  {
    group: 'Control plane',
    entries: [
      { name: 'control.call(method, payload?)', text: 'Sends one control-plane command and resolves with its result. The Permissions tab lists every method by permission.', needs: 'control:*' },
      { name: 'events.on(topic, handler)', text: 'Follows a live topic; resolves with an unsubscribe function. handler(payload).', needs: 'control:read' },
    ],
  },
  {
    group: 'Storage',
    entries: [
      { name: 'storage.get(key)', text: 'Resolves with the stored string, or null.', needs: 'storage' },
      { name: 'storage.set(key, value)', text: `Stores a string up to ${STORAGE_VALUE_BYTES / 1024} KiB; ${STORAGE_KEYS} keys and ${STORAGE_TOTAL_BYTES / 1024} KiB per plugin.`, needs: 'storage' },
      { name: 'storage.remove(key)', text: 'Deletes a key.', needs: 'storage' },
      { name: 'storage.all()', text: 'Resolves with every key and value.', needs: 'storage' },
    ],
  },
]

const NODES: readonly Entry[] = [
  { name: 'stack', text: 'children, gap: none | sm | md | lg' },
  { name: 'row', text: 'children, gap, align: start | center | end | between, wrap' },
  { name: 'grid', text: 'children, columns (1–6), gap' },
  { name: 'panel', text: 'title, subtitle, actions (nodes), children' },
  { name: 'heading', text: 'text, level: 1 | 2 | 3' },
  { name: 'text', text: 'text, tone, mono, size: sm | md | lg, weight: regular | strong' },
  { name: 'metric / metrics', text: 'label, value, detail, tone — or items: [metric…] as one strip' },
  { name: 'badge / dot', text: 'text, tone: neutral | info | success | warning | danger' },
  { name: 'table', text: 'columns [{ key, label, align, mono, width }], rows, empty, rowAction (row id in the event)' },
  { name: 'list', text: 'items [{ title, detail, meta, tone }], empty' },
  { name: 'kv / code', text: 'items [{ key, value }] — or text, shown monospaced' },
  { name: 'button', text: 'text, action, variant: default | primary | danger | ghost, icon, disabled' },
  { name: 'input', text: 'name, label, value, placeholder, kind: text | number | search, maxLength, action (on Enter)' },
  { name: 'select / segmented', text: 'name, label, value, options [{ value, label }]' },
  { name: 'toggle', text: 'name, label, value: boolean' },
  { name: 'progress', text: 'value 0–1, label, tone' },
  { name: 'divider / empty / link', text: '— / title, hint / text, href (https only, opens the browser)' },
]

const PACKAGE: readonly Entry[] = [
  { name: 'schema', text: 'Always 1.' },
  { name: 'id', text: 'Lower-case letters, digits, dots and dashes, 3–64 characters: "acme.latency".' },
  { name: 'name, version', text: 'What the Plugins page shows. Installing the same id again is an update.' },
  { name: 'author, description', text: 'Optional; shown in the install review.' },
  { name: 'permissions', text: 'Everything the plugin may ask for. The operator decides what is granted.' },
  { name: 'theme', text: '{ name, scheme: "dark" | "light", tokens: { "--bg-canvas": "#101214", … } } — needs ui:theme.' },
  { name: 'css', text: `A stylesheet up to ${CSS_BYTES / 1024} KiB, applied while enabled — needs ui:css.` },
  { name: 'script', text: `Up to ${SCRIPT_BYTES / 1024} KiB of JavaScript run as an async function body in an isolated worker with \`switchboard\` in scope.` },
]

/** The in-app reference: the package format, the API, the page nodes, the
 * permission-to-method table and the theme tokens, all read from the same
 * tables the runtime enforces. */
export function PluginReference() {
  const [section, setSection] = useState<Section>('package')
  return (
    <div className={styles.reference}>
      <Segmented options={SECTIONS} value={section} onChange={setSection} label="Reference section" />
      {section === 'package' ? (
        <>
          <p className={styles.muted}>
            A plugin is one JSON file. Up to {PLUGIN_LIMIT} can be installed. The script runs in a sealed worker: no network,
            no DOM and no storage of its own — everything goes through <code>switchboard</code>, checked against what you granted.
          </p>
          <EntryList entries={PACKAGE} />
          <p className={styles.muted}>The full guide with worked examples is <code>docs/PLUGINS.md</code> in the repository.</p>
        </>
      ) : null}
      {section === 'api' ? API.map((group) => (
        <div key={group.group} className={styles.referenceGroup}>
          <h4>{group.group}</h4>
          <EntryList entries={group.entries} />
        </div>
      )) : null}
      {section === 'nodes' ? (
        <>
          <p className={styles.muted}>
            Every node is <code>{'{ type, key?, … }'}</code>. A tree holds at most {TREE_LIMITS.nodes} nodes, {TREE_LIMITS.depth} levels
            and {TREE_LIMITS.bytes / 1024} KiB. Icons: {Object.keys(PLUGIN_ICONS).join(', ')}.
          </p>
          <EntryList entries={NODES} />
        </>
      ) : null}
      {section === 'permissions' ? (
        <div className={styles.permissionTable}>
          {PERMISSION_SPECS.map((spec) => {
            const methods = methodsFor(spec.id)
            return (
              <div key={spec.id} className={styles.permissionRow}>
                <div className={styles.grantTitle}>
                  <code>{spec.id}</code>
                  <strong>{spec.title}</strong>
                  <RiskPill risk={spec.risk} />
                  {spec.grantedByDefault ? null : <Pill tone="warning">Unticked by default</Pill>}
                </div>
                <p>{spec.description}</p>
                {methods.length ? <p className={styles.methods}>{methods.join(' · ')}</p> : null}
                {spec.id === 'control:read' ? <p className={styles.methods}>Topics: {CONTROL_TOPICS.join(' · ')}</p> : null}
              </div>
            )
          })}
          <p className={styles.muted}>Never available to plugins: plugins.*, system.*, backup.*, appearance.update and updates.install.</p>
        </div>
      ) : null}
      {section === 'tokens' ? (
        <>
          <p className={styles.muted}>Theme tokens override these custom properties. Stylesheets can use them too: <code>var(--bg-surface)</code>.</p>
          <dl className={styles.tokenList}>
            {[...COLOR_TOKENS, ...DERIVED_TOKENS].map((token) => (
              <div key={token.name}>
                <dt>
                  {isColour(token.name) ? <span className={styles.swatch} style={{ background: `var(${token.name})` }} aria-hidden="true" /> : <span className={styles.swatch} data-empty="true" aria-hidden="true" />}
                  <code>{token.name}</code>
                </dt>
                <dd>{token.description}</dd>
              </div>
            ))}
          </dl>
        </>
      ) : null}
    </div>
  )
}

const NOT_COLOURS = new Set(['--shadow-float'])
const COLOURS = new Set<string>([...COLOR_TOKENS.map((token) => token.name), ...DERIVED_TOKENS.filter((token) => token.group === 'Accent').map((token) => token.name)])

function isColour(name: string): boolean {
  return COLOURS.has(name) && !NOT_COLOURS.has(name)
}

function EntryList({ entries }: { readonly entries: readonly Entry[] }) {
  return (
    <dl className={styles.entries}>
      {entries.map((entry) => (
        <div key={entry.name}>
          <dt><code>{entry.name}</code>{entry.needs ? <span className={styles.needs}>{entry.needs}</span> : null}</dt>
          <dd>{entry.text}</dd>
        </div>
      ))}
    </dl>
  )
}
