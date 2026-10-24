import type { CommandContribution, ExtensionPoints, StatusContribution } from '../../../shared/contracts/extensions'
import { controlAccess, topicAccess } from '../domain/control-access'
import type { PluginPermission } from '../domain/permissions'
import { IDLE_RUNTIME, LOG_LIMIT, pageKey, pluginOwner, type LogEntry, type PluginDetail, type PluginRuntime, type RuntimeState } from '../domain/plugin'
import {
  CallError,
  RUNTIME_LIMITS,
  STORAGE_VALUE_BYTES,
  commandSpec,
  localId,
  notifySpec,
  pageSpec,
  parseWorkerMessage,
  statusSpec,
  storageKey,
  PAGE_ID_PATTERN,
  type HostMessage,
  type Op,
  type PageEvent,
  type WorkerMessage,
} from '../domain/protocol'
import { checkTree, type UiNode } from '../domain/ui-tree'
import type { Announcement, BootContext, ControlPort, PluginWorker, PluginsPort, WorkerFactory } from './ports'

/** A page a running plugin registered, with the last tree it drew. */
export interface PluginPage {
  /** `<pluginId>/<pageId>`. */
  readonly key: string
  readonly pluginId: string
  readonly pluginName: string
  readonly pageId: string
  readonly title: string
  readonly icon?: string
  /** Null until the plugin draws the page for the first time. */
  readonly tree: UiNode | null
  /** Why the last tree was refused; the previous tree stays on screen. */
  readonly problem: string
}

export interface HostDeps {
  readonly workers: WorkerFactory
  readonly control: ControlPort
  readonly storage: Pick<PluginsPort, 'storageGet' | 'storageSet'>
  readonly extensions: ExtensionPoints
  readonly notify: (announcement: Announcement) => void
  readonly navigate: (route: string) => void
  /** Whether the shell has this route; plugins cannot navigate anywhere else. */
  readonly isRoute: (route: string) => boolean
  readonly app: BootContext['app']
  /** The runtime, pages or logs changed. */
  readonly onChange: () => void
}

/** Hardening budgets. A plugin that exceeds the message rate is stopped; the
 * others refuse the one call and leave the plugin running. */
export const HOST_LIMITS = {
  /** Messages a worker may post per window before it is stopped. */
  messages: 500,
  windowMs: 5_000,
  /** The script must finish its top-level code within this. */
  startMs: 10_000,
  pingMs: 5_000,
  /** No answer to pings for this long means it is stuck in a loop. */
  unresponsiveMs: 20_000,
  /** Arguments of one call, as JSON. Trees have their own 256 KiB cap. */
  argsBytes: 300 * 1024,
} as const

const encoder = new TextEncoder()

/**
 * Runs one plugin: applies its theme and stylesheet, starts its script in an
 * isolated worker, and answers the worker's calls — each one checked against
 * the protocol, the plugin's effective grants and the host's budgets before
 * anything happens. Everything the plugin added to the window lives under its
 * owner key and leaves with it.
 */
export class PluginHost {
  readonly id: string
  readonly name: string
  /** What the runtime was started from: a new revision or grant set restarts it. */
  readonly revision: string
  readonly #detail: PluginDetail
  readonly #grants: ReadonlySet<PluginPermission>
  readonly #deps: HostDeps
  readonly #owner: string
  #worker: PluginWorker | null = null
  #runtime: PluginRuntime = IDLE_RUNTIME
  #pages = new Map<string, PluginPage>()
  #commands = new Map<string, CommandContribution>()
  #status = new Map<string, StatusContribution>()
  #topics = new Map<string, () => void>()
  #inflight = new Set<AbortController>()
  #storage: Record<string, string> | null = null
  #window = { start: 0, count: 0 }
  #notified: number[] = []
  #startTimer: ReturnType<typeof setTimeout> | undefined
  #pingTimer: ReturnType<typeof setInterval> | undefined
  #pingSeq = 0
  #lastPong = 0
  #ready = false

  constructor(detail: PluginDetail, grants: readonly PluginPermission[], deps: HostDeps) {
    this.id = detail.plugin.id
    this.name = detail.plugin.name
    this.revision = revisionOf(detail.plugin.updatedAt, grants)
    this.#detail = detail
    this.#grants = new Set(grants)
    this.#deps = deps
    this.#owner = pluginOwner(this.id)
  }

  runtime(): PluginRuntime {
    return this.#runtime
  }

  pages(): Iterable<PluginPage> {
    return this.#pages.values()
  }

  start(): void {
    const { theme, css, script } = this.#detail
    this.#deps.extensions.contribute(this.#owner, {
      themes: theme && this.#grants.has('ui:theme') ? [{ id: this.#owner, name: theme.name, scheme: theme.scheme, tokens: theme.tokens }] : [],
      styles: css && this.#grants.has('ui:css') ? [{ owner: this.#owner, css }] : [],
      commands: [],
      status: [],
      pages: [],
    })
    if (!script.trim()) {
      this.#update({ state: 'active', reason: '' })
      return
    }
    this.#update({ state: 'starting', reason: '' })
    this.#note('info', `Starting ${this.name} ${this.#detail.plugin.version}.`)
    let worker: PluginWorker
    try {
      worker = this.#deps.workers.create(script, {
        plugin: { id: this.id, name: this.name, version: this.#detail.plugin.version, granted: [...this.#grants] },
        app: this.#deps.app,
      })
    } catch (error) {
      this.#crash(`The script could not be started: ${messageOf(error)}`)
      return
    }
    this.#worker = worker
    worker.onMessage((data) => this.#receive(worker, data))
    worker.onError((message) => {
      if (this.#worker === worker) this.#crash(message)
    })
    this.#startTimer = setTimeout(() => {
      if (this.#worker === worker && !this.#ready) this.#crash(`The script did not finish starting within ${HOST_LIMITS.startMs / 1000} seconds.`)
    }, HOST_LIMITS.startMs)
    this.#lastPong = Date.now()
    this.#pingTimer = setInterval(() => this.#ping(worker), HOST_LIMITS.pingMs)
  }

  /** Stops the script and takes back everything the plugin added. */
  stop(state: RuntimeState = 'stopped', reason = ''): void {
    this.#halt()
    this.#deps.extensions.clear(this.#owner)
    this.#pages.clear()
    this.#update({ state, reason })
  }

  /** Sends what the operator did on one of the plugin's pages. */
  dispatch(pageId: string, event: PageEvent): void {
    if (!this.#ready || !this.#pages.has(pageKey(this.id, pageId))) return
    this.#post({ type: 'ui', pageId, event })
  }

  #receive(worker: PluginWorker, data: unknown): void {
    if (this.#worker !== worker) return
    const now = Date.now()
    if (now - this.#window.start > HOST_LIMITS.windowMs) this.#window = { start: now, count: 0 }
    this.#window.count += 1
    if (this.#window.count > HOST_LIMITS.messages) {
      this.#crash(`Stopped for flooding the window: more than ${HOST_LIMITS.messages} messages in ${HOST_LIMITS.windowMs / 1000} seconds.`)
      return
    }
    const message = parseWorkerMessage(data)
    if (!message) {
      this.#crash('Stopped for sending a message outside the plugin protocol.')
      return
    }
    this.#handle(message)
  }

  #handle(message: WorkerMessage): void {
    switch (message.type) {
      case 'ready':
        if (this.#ready) return
        this.#ready = true
        clearTimeout(this.#startTimer)
        this.#update({ state: 'running', reason: '' })
        this.#note('info', 'Running.')
        this.#post({ type: 'activate' })
        return
      case 'fail':
        this.#crash(`The script failed while starting: ${message.message}`)
        return
      case 'fault':
        this.#runtime = { ...this.#runtime, faults: this.#runtime.faults + 1 }
        this.#note('error', message.message)
        return
      case 'log':
        this.#note(message.level, message.text)
        return
      case 'pong':
        this.#lastPong = Date.now()
        return
      case 'call':
        void this.#call(message.id, message.op, message.args)
        return
    }
  }

  async #call(id: number, op: Op, args: Record<string, unknown>): Promise<void> {
    try {
      if (op !== 'ui.render') {
        let size = 0
        try { size = encoder.encode(JSON.stringify(args)).length } catch { throw new CallError('invalid_argument', 'Arguments must be plain JSON data.') }
        if (size > HOST_LIMITS.argsBytes) throw new CallError('too_large', `The arguments are ${Math.ceil(size / 1024)} KiB; a call carries at most ${HOST_LIMITS.argsBytes / 1024} KiB.`)
      }
      const value = await this.#perform(op, args)
      this.#post({ type: 'result', id, ok: true, value: value ?? null })
    } catch (error) {
      const code = error instanceof Error && 'code' in error && typeof error.code === 'string' ? error.code : 'failed'
      const message = error instanceof DOMException && error.name === 'AbortError' ? 'The plugin was stopped.' : messageOf(error)
      if (code === 'permission_denied' || code === 'not_allowed') this.#note('warn', message)
      this.#post({ type: 'result', id, ok: false, error: { code, message } })
    }
  }

  async #perform(op: Op, args: Record<string, unknown>): Promise<unknown> {
    switch (op) {
      case 'ui.registerPage': {
        this.#need('ui:pages')
        const spec = pageSpec(args)
        const key = pageKey(this.id, spec.id)
        const existing = this.#pages.get(key)
        if (!existing && this.#pages.size >= RUNTIME_LIMITS.pages) throw new CallError('limit', `A plugin can register at most ${RUNTIME_LIMITS.pages} pages.`)
        this.#pages.set(key, { key, pluginId: this.id, pluginName: this.name, pageId: spec.id, title: spec.title, ...(spec.icon ? { icon: spec.icon } : {}), tree: existing?.tree ?? null, problem: '' })
        this.#publishPages()
        return { route: `plugin/${key}` }
      }
      case 'ui.unregisterPage': {
        this.#need('ui:pages')
        const key = pageKey(this.id, localId(args, 'id', PAGE_ID_PATTERN))
        if (this.#pages.delete(key)) this.#publishPages()
        return null
      }
      case 'ui.render': {
        this.#need('ui:pages')
        const key = pageKey(this.id, localId(args, 'pageId', PAGE_ID_PATTERN))
        const page = this.#pages.get(key)
        if (!page) throw new CallError('not_found', `Register page "${String(args.pageId)}" before rendering it.`)
        const checked = checkTree(args.tree)
        if (!checked.ok) {
          this.#pages.set(key, { ...page, problem: checked.problem })
          this.#deps.onChange()
          throw new CallError('invalid_tree', checked.problem)
        }
        this.#pages.set(key, { ...page, tree: checked.tree, problem: '' })
        this.#deps.onChange()
        return null
      }
      case 'ui.setStatus': {
        this.#need('ui:status')
        const { id, status } = statusSpec(args)
        const fullId = `${this.#owner}:${id}`
        if (!status) this.#status.delete(fullId)
        else {
          if (!this.#status.has(fullId) && this.#status.size >= RUNTIME_LIMITS.status) throw new CallError('limit', `A plugin can show at most ${RUNTIME_LIMITS.status} status readouts.`)
          // The tooltip always names the plugin, so a readout cannot pass
          // itself off as the relay's own.
          const tooltip = status.tooltip ? `${status.tooltip}\n— ${this.name}` : this.name
          this.#status.set(fullId, { id: fullId, text: status.text, ...(status.tone ? { tone: status.tone } : {}), tooltip })
        }
        this.#deps.extensions.contribute(this.#owner, { status: [...this.#status.values()] })
        this.#refreshCounts()
        return null
      }
      case 'commands.register': {
        this.#need('ui:commands')
        const spec = commandSpec(args)
        const fullId = `${this.#owner}:${spec.id}`
        if (!this.#commands.has(fullId) && this.#commands.size >= RUNTIME_LIMITS.commands) throw new CallError('limit', `A plugin can register at most ${RUNTIME_LIMITS.commands} commands.`)
        this.#commands.set(fullId, {
          id: fullId,
          title: spec.title,
          // Grouped under the plugin's name, so the palette always says
          // whose command it is.
          group: spec.group ? `${this.name} · ${spec.group}` : this.name,
          keywords: [...(spec.keywords ?? []), 'plugin', this.name],
          ...(spec.shortcut ? { shortcut: spec.shortcut } : {}),
          run: () => { this.#post({ type: 'command', id: spec.id }) },
        })
        this.#deps.extensions.contribute(this.#owner, { commands: [...this.#commands.values()] })
        this.#refreshCounts()
        return { id: fullId }
      }
      case 'commands.unregister': {
        this.#need('ui:commands')
        if (this.#commands.delete(`${this.#owner}:${localId(args)}`)) {
          this.#deps.extensions.contribute(this.#owner, { commands: [...this.#commands.values()] })
          this.#refreshCounts()
        }
        return null
      }
      case 'notify': {
        this.#need('ui:notify')
        const spec = notifySpec(args)
        const now = Date.now()
        this.#notified = this.#notified.filter((at) => now - at < 60_000)
        if (this.#notified.length >= RUNTIME_LIMITS.notifyPerMinute) throw new CallError('rate_limited', `A plugin can raise at most ${RUNTIME_LIMITS.notifyPerMinute} notifications a minute.`)
        this.#notified.push(now)
        this.#deps.notify({
          kind: 'plugin',
          severity: spec.tone === 'neutral' ? 'info' : spec.tone,
          title: spec.title,
          body: spec.body ? `${spec.body}\n— ${this.name}` : `— ${this.name}`,
        })
        return null
      }
      case 'control.call': {
        const method = typeof args.method === 'string' ? args.method : ''
        const access = controlAccess(method, this.#grants)
        if (!access.allowed) throw new CallError(access.code, access.message)
        if (this.#inflight.size >= RUNTIME_LIMITS.inflight) throw new CallError('busy', `A plugin can have at most ${RUNTIME_LIMITS.inflight} control calls in flight.`)
        const controller = new AbortController()
        this.#inflight.add(controller)
        try {
          return await this.#deps.control.call(method, args.payload, controller.signal)
        } finally {
          this.#inflight.delete(controller)
        }
      }
      case 'events.subscribe': {
        const topic = typeof args.topic === 'string' ? args.topic : ''
        const access = topicAccess(topic, this.#grants)
        if (!access.allowed) throw new CallError(access.code, access.message)
        if (this.#topics.has(topic)) return null
        if (this.#topics.size >= RUNTIME_LIMITS.topics) throw new CallError('limit', `A plugin can follow at most ${RUNTIME_LIMITS.topics} topics.`)
        this.#topics.set(topic, this.#deps.control.subscribe(topic, (payload) => this.#post({ type: 'topic', topic, payload: payload ?? null })))
        return null
      }
      case 'events.unsubscribe': {
        const topic = typeof args.topic === 'string' ? args.topic : ''
        this.#topics.get(topic)?.()
        this.#topics.delete(topic)
        return null
      }
      case 'storage.get': {
        this.#need('storage')
        const key = storageKey(args)
        const values = await this.#values()
        return Object.hasOwn(values, key) ? values[key] : null
      }
      case 'storage.all': {
        this.#need('storage')
        return { ...(await this.#values()) }
      }
      case 'storage.set':
      case 'storage.remove': {
        this.#need('storage')
        const key = storageKey(args)
        let value: string | null = null
        if (op === 'storage.set') {
          if (typeof args.value !== 'string') throw new CallError('invalid_argument', 'Stored values are strings; use JSON.stringify for anything else.')
          if (encoder.encode(args.value).length > STORAGE_VALUE_BYTES) throw new CallError('too_large', `A stored value is at most ${STORAGE_VALUE_BYTES / 1024} KiB.`)
          value = args.value
        }
        this.#storage = await this.#deps.storage.storageSet(this.id, key, value)
        return null
      }
      case 'navigate': {
        const route = typeof args.route === 'string' ? args.route.replace(/^#\/?/u, '') : ''
        if (!route || route.length > 160 || !this.#deps.isRoute(route)) throw new CallError('not_found', `"${route.slice(0, 160)}" is not a page in this window.`)
        this.#deps.navigate(route)
        return null
      }
    }
  }

  async #values(): Promise<Record<string, string>> {
    this.#storage ??= await this.#deps.storage.storageGet(this.id)
    return this.#storage
  }

  #need(permission: PluginPermission): void {
    if (!this.#grants.has(permission)) throw new CallError('permission_denied', `This needs the "${permission}" permission, which the plugin was not granted.`)
  }

  #publishPages(): void {
    this.#deps.extensions.contribute(this.#owner, {
      pages: [...this.#pages.values()].map((page) => ({ id: page.key, title: page.title, ...(page.icon ? { icon: page.icon } : {}) })),
    })
    this.#refreshCounts()
  }

  #refreshCounts(): void {
    this.#runtime = {
      ...this.#runtime,
      pages: [...this.#pages.values()].map((page) => ({ key: page.key, title: page.title })),
      commands: this.#commands.size,
      status: this.#status.size,
    }
    this.#deps.onChange()
  }

  #ping(worker: PluginWorker): void {
    if (this.#worker !== worker) return
    if (Date.now() - this.#lastPong > HOST_LIMITS.unresponsiveMs) {
      this.#crash(`Stopped because the script stopped responding for ${HOST_LIMITS.unresponsiveMs / 1000} seconds (an endless loop?).`)
      return
    }
    this.#pingSeq += 1
    this.#post({ type: 'ping', seq: this.#pingSeq })
  }

  #post(message: HostMessage): void {
    try {
      this.#worker?.post(message)
    } catch (error) {
      // A payload the worker cannot receive is the control plane's shape,
      // not the plugin's fault; report it to the plugin's log.
      this.#note('error', `A message could not be delivered: ${messageOf(error)}`)
    }
  }

  #crash(reason: string): void {
    this.#note('error', reason)
    this.#halt()
    // Its theme and stylesheet are plain data and stay; what the script
    // added goes with the script.
    this.#pages.clear()
    this.#deps.extensions.contribute(this.#owner, { commands: [], status: [], pages: [] })
    this.#update({ state: 'crashed', reason })
  }

  #halt(): void {
    clearTimeout(this.#startTimer)
    clearInterval(this.#pingTimer)
    this.#worker?.terminate()
    this.#worker = null
    this.#ready = false
    for (const unsubscribe of this.#topics.values()) unsubscribe()
    this.#topics.clear()
    for (const controller of this.#inflight) controller.abort()
    this.#inflight.clear()
    this.#commands.clear()
    this.#status.clear()
  }

  #note(level: LogEntry['level'], text: string): void {
    const logs = [...this.#runtime.logs, { at: Date.now(), level, text }]
    this.#runtime = { ...this.#runtime, logs: logs.length > LOG_LIMIT ? logs.slice(-LOG_LIMIT) : logs }
    this.#deps.onChange()
  }

  #update(change: { state: RuntimeState; reason: string }): void {
    this.#runtime = {
      ...this.#runtime,
      ...change,
      pages: [...this.#pages.values()].map((page) => ({ key: page.key, title: page.title })),
      commands: this.#commands.size,
      status: this.#status.size,
    }
    this.#deps.onChange()
  }
}

export function revisionOf(updatedAt: string, grants: readonly string[]): string {
  return `${updatedAt}|${[...grants].sort().join(',')}`
}

function messageOf(error: unknown): string {
  if (error instanceof Error && error.message) return error.message
  return typeof error === 'string' && error ? error : 'Unknown error'
}
