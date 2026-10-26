import type { ReactNode } from 'react'
import type { ActionContribution, BadgeContribution, CommandContribution, ExtensionPoints, SlotContext, SlotContribution, SlotScope, StatusContribution } from '../../../shared/contracts/extensions'
import { validateDrawOps, type CanvasChannel, type DrawOp } from '../domain/canvas'
import { controlAccess, stampsActor, topicAccess } from '../domain/control-access'
import type { PluginPermission } from '../domain/permissions'
import { IDLE_RUNTIME, LOG_LIMIT, pageKey, pluginOwner, type LogEntry, type PluginDetail, type PluginRuntime, type PluginSummary, type RuntimeState } from '../domain/plugin'
import {
  CallError,
  RUNTIME_LIMITS,
  STORAGE_VALUE_BYTES,
  badgeSpec,
  clipboardText,
  commandSpec,
  isRecord,
  confirmSpec,
  localId,
  notifySpec,
  pageSpec,
  parseWorkerMessage,
  statusSpec,
  storageBatch,
  storageKey,
  PAGE_ID_PATTERN,
  STREAM_MESSAGES,
  type HostMessage,
  type Op,
  type PageEvent,
  type WorkerMessage,
} from '../domain/protocol'
import { isSettingStorageKey, settingValues, type SettingValue } from '../domain/settings'
import {
  DATA_LIMITS,
  SLOT_LIMITS,
  actionSpec,
  busPayload,
  channel,
  collectionName,
  dialogSpec,
  docId,
  docValue,
  fetchSpec,
  headerButtonSpec,
  hookDecision,
  hookSpec,
  inspectorTabSpec,
  layoutSpec,
  listSpec,
  openSpec,
  saveSpec,
  settingsSectionSpec,
  toneSpec,
  widgetSpec,
  HOOK_ID_PATTERN,
  type ActionSpec,
  type DialogActionSpec,
  type ToneSpec,
} from '../domain/specs'
import { checkTree, type UiNode } from '../domain/ui-tree'
import type { PluginBus } from './plugin-bus'
import type { Announcement, BootContext, ConfirmRequest, ControlPort, FilesPort, PluginWorker, PluginsPort, ThemePort, WorkerFactory } from './ports'

/** A widget, inspector tab or settings section a running plugin registered,
 * with the last tree it drew. */
export interface PluginSurface {
  /** `<pluginId>/<surface>`. */
  readonly key: string
  readonly pluginId: string
  /** `widget:<id>`, `inspector:<id>` or `settings:<id>`. */
  readonly surface: string
  readonly tree: UiNode | null
  readonly problem: string
}

/** The one dialog a running plugin has open. */
export interface PluginDialog {
  readonly pluginId: string
  readonly pluginName: string
  readonly id: string
  readonly title: string
  readonly actions: readonly DialogActionSpec[]
  readonly size: 'small' | 'medium' | 'large'
  readonly tree: UiNode
  readonly problem: string
}

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
  /** What the sidebar entry shows next to the title. */
  readonly badge?: BadgeContribution
}

export interface HostDeps {
  readonly workers: WorkerFactory
  readonly control: ControlPort
  readonly storage: Pick<PluginsPort, 'storageGet' | 'storageUpdate'>
  readonly extensions: ExtensionPoints
  readonly notify: (announcement: Announcement) => void
  /** Asks the operator; resolves false when they decline, close the dialog or
   * the signal aborts (the plugin stopped). */
  readonly confirm: (request: ConfirmRequest, signal: AbortSignal) => Promise<boolean>
  readonly clipboard: (text: string) => Promise<void>
  readonly navigate: (route: string) => void
  /** Whether the shell has this route; plugins cannot navigate anywhere else. */
  readonly isRoute: (route: string) => boolean
  readonly app: BootContext['app']
  /** The runtime, pages, surfaces, dialog or logs changed. */
  readonly onChange: () => void
  readonly bus: PluginBus
  readonly files: FilesPort
  /** Plays one checked tone; never throws. */
  readonly sound: (tone: ToneSpec) => void
  readonly theme: ThemePort
  /** Draws a slot surface of a plugin inside the shell's frame. */
  readonly renderSlot: (pluginId: string, surface: string, context: SlotContext) => ReactNode
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
  /** Draw, frame and input messages per second, together. */
  streamPerSecond: 240,
  /** Stream messages per second past which the worker is stopped: dropping
   * that many still costs the window their delivery. */
  streamFloodPerSecond: 2_400,
  /** A frame the worker has not finished is given up after this. */
  frameTimeoutMs: 250,
  /** Last draws kept per plugin, so a canvas that mounts after the draw still paints. */
  drawCache: 32,
  /** Relay hook calls awaiting the worker's answer. */
  pendingHooks: 64,
  /** A repeated warning (bad draw, bad tone) reaches the log at most this often. */
  warnEveryMs: 5_000,
} as const

const SURFACE_KINDS = {
  widget: { limit: SLOT_LIMITS.widgets, what: 'widgets' },
  inspector: { limit: SLOT_LIMITS.inspectorTabs, what: 'inspector tabs' },
  settings: { limit: SLOT_LIMITS.settingsSections, what: 'settings sections' },
} as const
type SurfaceKind = keyof typeof SURFACE_KINDS

const HOOK_TOPICS = ['relayhooks.invoke', 'relayhooks.observe', 'relayhooks.changed'] as const

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
  #storage: Record<string, string> | null
  /** Storage calls run one after another, so a read never sees half of a
   * batch and writes land in the order the plugin made them. */
  #storageQueue: Promise<unknown> = Promise.resolve()
  #settings: Readonly<Record<string, SettingValue>>
  #window = { start: 0, count: 0 }
  #notified: number[] = []
  #writes: number[] = []
  /** When the operator last acted on the plugin: a page event or a command. */
  #gestureAt = Number.NEGATIVE_INFINITY
  #confirming: AbortController | null = null
  /** Commands the operator ran and the plugin has not failed yet, by local
   * id, so a thrown handler becomes one toast per run, never a stream. */
  #runs = new Map<string, number>()
  #startTimer: ReturnType<typeof setTimeout> | undefined
  #pingTimer: ReturnType<typeof setInterval> | undefined
  #pingSeq = 0
  #lastPong = 0
  #ready = false
  #secrets: readonly string[]
  /** Slot surfaces by `widget:<id>`, `inspector:<id>` or `settings:<id>`. */
  #surfaces = new Map<string, { readonly kind: SurfaceKind; readonly slot: SlotContribution; readonly view: PluginSurface }>()
  /** Actions and header buttons by local id. */
  #actions = new Map<string, ActionContribution>()
  #dialog: PluginDialog | null = null
  #dialogSeq = 0
  #busSubs = new Map<string, () => void>()
  #published: number[] = []
  #fetchTimes: number[] = []
  #fetching = 0
  #dbWrites: number[] = []
  #tones: number[] = []
  /** Draw, frame and input messages in the current second. */
  #stream = { start: 0, count: 0 }
  #warned = new Map<string, number>()
  /** The last checked draw per canvas key, oldest first. */
  #draws = new Map<string, readonly DrawOp[]>()
  #drawListeners = new Map<string, Set<(ops: readonly DrawOp[]) => void>>()
  /** Visible canvases that asked for frames. */
  #frames = new Set<string>()
  #frameCancel: (() => void) | null = null
  /** When the outstanding frame was sent; 0 once the worker finished it. */
  #frameSentAt = 0
  #frameLast = 0
  #frameClock = 0
  #themeUnsub: (() => void) | null = null
  /** Relay hooks this plugin registered, by hook id. */
  #hooks = new Set<string>()
  #hookTopics: (() => void)[] = []
  /** Request hook calls the worker has not answered yet, with when they came. */
  #pendingHooks = new Map<string, number>()

  /** `storage` is what the plugin had stored when it was loaded, when the
   * caller read it; the settings come from it. */
  constructor(detail: PluginDetail, grants: readonly PluginPermission[], deps: HostDeps, storage: Record<string, string> | null = null) {
    this.id = detail.plugin.id
    this.name = detail.plugin.name
    this.revision = revisionOf(detail.plugin, grants)
    this.#detail = detail
    this.#grants = new Set(grants)
    this.#deps = deps
    this.#owner = pluginOwner(this.id)
    this.#storage = storage
    this.#settings = settingValues(detail.settings, storage ?? {})
    this.#secrets = detail.plugin.secrets
  }

  /** The operator filled in or cleared a secret setting: the script learns
   * which secrets are set, never their values. */
  secretsChanged(keys: readonly string[]): void {
    if (keys.length === this.#secrets.length && keys.every((key) => this.#secrets.includes(key))) return
    this.#secrets = [...keys]
    if (this.#ready) this.#post({ type: 'settings', values: this.#settings, secrets: this.#secrets })
  }

  /** The operator saved settings (or storage changed elsewhere): the host
   * takes the new values and tells the script when a setting changed. */
  storageChanged(values: Record<string, string>): void {
    this.#storage = values
    const next = settingValues(this.#detail.settings, values)
    if (sameValues(next, this.#settings)) return
    this.#settings = next
    if (this.#ready) this.#post({ type: 'settings', values: next, secrets: this.#secrets })
  }

  runtime(): PluginRuntime {
    return this.#runtime
  }

  pages(): Iterable<PluginPage> {
    return this.#pages.values()
  }

  surfaces(): readonly PluginSurface[] {
    return [...this.#surfaces.values()].map((entry) => entry.view)
  }

  dialog(): PluginDialog | null {
    return this.#dialog
  }

  /** The operator pressed one of the dialog's buttons (`null`: dismissed it). */
  answerDialog(id: string, action: string | null, form: PageEvent['form']): void {
    const dialog = this.#dialog
    if (!dialog || dialog.id !== id || (action !== null && !dialog.actions.some((item) => item.id === action))) return
    this.#gestureAt = Date.now()
    this.#closeDialog(action, form)
  }

  /** How the plugin's canvases reach it. */
  readonly canvas: CanvasChannel = {
    subscribe: (surface, canvas, draw) => {
      const key = canvasKey(surface, canvas)
      let listeners = this.#drawListeners.get(key)
      if (!listeners) {
        listeners = new Set()
        this.#drawListeners.set(key, listeners)
      }
      listeners.add(draw)
      const last = this.#draws.get(key)
      if (last) draw(last)
      const own = listeners
      return () => {
        own.delete(draw)
        if (own.size === 0 && this.#drawListeners.get(key) === own) this.#drawListeners.delete(key)
      }
    },
    input: (surface, canvas, event) => {
      if (!this.#ready || !this.#knownSurface(surface)) return
      // Keys and clicks are the operator acting; moving the pointer is not.
      if (event.kind === 'keydown' || event.kind === 'keyup' || event.kind === 'pointerdown' || event.kind === 'pointerup') this.#gestureAt = Date.now()
      if (!this.#spend()) return
      this.#post({ type: 'input', event: { ...event, surface, canvas } })
    },
    visible: (surface, canvas, frames, visible) => {
      const key = canvasKey(surface, canvas)
      if (frames && visible) this.#frames.add(key)
      else this.#frames.delete(key)
      this.#scheduleFrame()
    },
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
        settings: this.#settings,
        secrets: this.#secrets,
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

  /** Sends what the operator did on one of the plugin's pages, slot surfaces
   * (`widget:<id>`, …) or its dialog (`dialog:<id>`). */
  dispatch(pageId: string, event: PageEvent): void {
    if (!this.#ready) return
    const known = pageId.startsWith('dialog:') ? this.#dialog?.id === pageId.slice('dialog:'.length) : this.#knownSurface(pageId)
    if (!known) return
    // Mounting and unmounting a surface is the window acting, not the operator.
    if (event.kind === 'action' || event.kind === 'change') this.#gestureAt = Date.now()
    this.#post({ type: 'ui', pageId, event })
  }

  /** A removed surface keeps no picture to replay and asks for no frames. */
  #forgetCanvases(surface: string): void {
    const prefix = canvasKey(surface, '')
    for (const key of [...this.#draws.keys()]) if (key.startsWith(prefix)) this.#draws.delete(key)
    for (const key of [...this.#frames]) if (key.startsWith(prefix)) this.#frames.delete(key)
  }

  /** A registered page id or slot surface. */
  #knownSurface(surface: string): boolean {
    return surface.includes(':') ? this.#surfaces.has(surface) : this.#pages.has(pageKey(this.id, surface))
  }

  /** A registered action or header button was clicked. */
  #runAction(id: string, scope: ActionContribution['scope'], target: string | undefined): void {
    if (!this.#ready || !this.#actions.has(id)) return
    this.#gestureAt = Date.now()
    const row = scope !== 'header' && typeof target === 'string' && target.length > 0 && target.length <= 256 ? { target } : {}
    this.#post({ type: 'ui', pageId: '', event: { kind: 'action', action: id, scope, ...row, pageId: '', form: {} } })
  }

  /** Runs one of the plugin's commands for the operator. */
  #runCommand(id: string): void {
    if (!this.#ready) return
    const now = Date.now()
    this.#gestureAt = now
    this.#runs.set(id, now)
    // Bounded: the oldest unfinished runs are forgotten first.
    if (this.#runs.size > RUNTIME_LIMITS.commands) this.#runs.delete(this.#runs.keys().next().value as string)
    this.#post({ type: 'command', id })
  }

  /** Clipboard writes and questions need the operator to have just acted, so
   * a plugin cannot raise dialogs or overwrite the clipboard on its own. */
  #needGesture(what: string): void {
    if (Date.now() - this.#gestureAt > RUNTIME_LIMITS.gestureMs) {
      throw new CallError('needs_gesture', `${what} only works within ${RUNTIME_LIMITS.gestureMs / 1000} seconds of the operator using the plugin's page or command.`)
    }
  }

  #receive(worker: PluginWorker, data: unknown): void {
    if (this.#worker !== worker) return
    const message = parseWorkerMessage(data)
    if (!message) {
      this.#crash('Stopped for sending a message outside the plugin protocol.')
      return
    }
    if (STREAM_MESSAGES.has(message.type)) {
      // Draws and tones have their own per-second budget; extras are dropped.
      // ponytail: dropping still costs a parse each, so far past the budget
      // the worker is stopped like any flood.
      this.#spend()
      if (this.#stream.count > HOST_LIMITS.streamFloodPerSecond) {
        this.#crash(`Stopped for flooding the window: more than ${HOST_LIMITS.streamFloodPerSecond} draw, sound and frame messages in a second.`)
        return
      }
    } else {
      const now = Date.now()
      if (now - this.#window.start > HOST_LIMITS.windowMs) this.#window = { start: now, count: 0 }
      this.#window.count += 1
      if (this.#window.count > HOST_LIMITS.messages) {
        this.#crash(`Stopped for flooding the window: more than ${HOST_LIMITS.messages} messages in ${HOST_LIMITS.windowMs / 1000} seconds.`)
        return
      }
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
        this.#themeUnsub = this.#deps.theme.subscribe(() => this.#post({ type: 'theme', theme: this.#deps.theme.read() }))
        this.#scheduleFrame()
        return
      case 'fail':
        this.#crash(`The script failed while starting: ${message.message}`)
        return
      case 'fault':
        this.#runtime = { ...this.#runtime, faults: this.#runtime.faults + 1 }
        this.#note('error', message.message)
        // The operator just ran this command and is waiting on it: say it
        // failed where they are looking, once per run.
        if (message.command && this.#runs.has(message.command) && Date.now() - (this.#runs.get(message.command) ?? 0) < 60_000) {
          this.#runs.delete(message.command)
          const title = this.#commands.get(`${this.#owner}:${message.command}`)?.title ?? message.command
          this.#deps.notify({ kind: 'plugin', severity: 'danger', title: `${title} failed`, body: `${message.message}\n— ${this.name}` })
        }
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
      case 'draw': {
        if (this.#stream.count > HOST_LIMITS.streamPerSecond || !this.#knownSurface(message.surface)) return
        const checked = validateDrawOps(message.ops)
        if (!checked.ok) {
          this.#warn('draw', `A draw on ${message.surface} was refused: ${checked.problem}`)
          return
        }
        const key = canvasKey(message.surface, message.canvas)
        this.#draws.delete(key)
        this.#draws.set(key, checked.ops)
        if (this.#draws.size > HOST_LIMITS.drawCache) this.#draws.delete(this.#draws.keys().next().value as string)
        for (const draw of this.#drawListeners.get(key) ?? []) draw(checked.ops)
        return
      }
      case 'tone': {
        if (!this.#grants.has('ui:sound')) return
        const now = Date.now()
        this.#tones = this.#tones.filter((at) => now - at < 1_000)
        if (this.#tones.length >= DATA_LIMITS.tonesPerSecond) return
        const tone = toneSpec(message.spec)
        if (typeof tone === 'string') {
          this.#warn('tone', `A tone was not played: ${tone}`)
          return
        }
        this.#tones.push(now)
        this.#deps.sound(tone)
        return
      }
      case 'frameDone':
        this.#frameSentAt = 0
        return
      case 'hookResult': {
        if (!this.#pendingHooks.delete(message.callId)) return
        const decision = hookDecision(message.decision)
        if (typeof decision === 'string') this.#warn('hook', `A request hook's answer was ignored and the request continued: ${decision}`)
        this.#respond(message.callId, typeof decision === 'string' ? { action: 'continue' } : decision)
        return
      }
    }
  }

  /** Counts one draw, frame, input or hook message; false past the budget. */
  #spend(): boolean {
    const now = Date.now()
    if (now - this.#stream.start >= 1_000) this.#stream = { start: now, count: 0 }
    this.#stream.count += 1
    return this.#stream.count <= HOST_LIMITS.streamPerSecond
  }

  /** A repeating problem reaches the log at most once per few seconds per kind. */
  #warn(kind: string, text: string): void {
    const now = Date.now()
    if (now - (this.#warned.get(kind) ?? Number.NEGATIVE_INFINITY) < HOST_LIMITS.warnEveryMs) return
    this.#warned.set(kind, now)
    this.#note('warn', text)
  }

  #scheduleFrame(): void {
    if (this.#frameCancel || !this.#ready || this.#frames.size === 0) return
    if (typeof requestAnimationFrame === 'function') {
      const handle = requestAnimationFrame(this.#tick)
      this.#frameCancel = () => cancelAnimationFrame(handle)
    } else {
      const handle = setTimeout(this.#tick, 16)
      this.#frameCancel = () => clearTimeout(handle)
    }
  }

  /** One display frame. At most one frame is outstanding, so a slow handler
   * lowers the frame rate instead of queueing frames. */
  #tick = (): void => {
    this.#frameCancel = null
    if (!this.#ready || this.#frames.size === 0) {
      this.#frameLast = 0
      return
    }
    const now = performance.now()
    if ((this.#frameSentAt === 0 || now - this.#frameSentAt >= HOST_LIMITS.frameTimeoutMs) && this.#spend()) {
      const dtMs = this.#frameLast === 0 ? 0 : now - this.#frameLast
      this.#frameLast = now
      this.#frameClock += dtMs
      this.#frameSentAt = now
      this.#post({ type: 'frame', dtMs, tMs: this.#frameClock })
    }
    this.#scheduleFrame()
  }

  #respond(callId: string, decision: Record<string, unknown>): void {
    this.#deps.control.call('relayhooks.respond', { callId, ...decision }).catch(() => undefined)
  }

  /** Follows the relay's hook topics for this plugin, once. Requests the
   * plugin cannot answer continue untouched (hooks fail open). */
  #followHooks(): void {
    if (this.#hookTopics.length > 0) return
    const mine = (payload: unknown): payload is Record<string, unknown> & { hookId: string } =>
      isRecord(payload) && payload.pluginId === this.id && typeof payload.hookId === 'string'
    const [invoke, observe, changed] = HOOK_TOPICS
    this.#hookTopics = [
      this.#deps.control.subscribe(invoke, (payload) => {
        if (!mine(payload) || typeof payload.callId !== 'string' || !payload.callId || payload.callId.length > 128) return
        const now = Date.now()
        for (const [callId, at] of this.#pendingHooks) if (now - at > DATA_LIMITS.hookTimeoutMs.max * 2) this.#pendingHooks.delete(callId)
        if (!this.#ready || !this.#hooks.has(payload.hookId) || this.#pendingHooks.size >= HOST_LIMITS.pendingHooks) {
          this.#respond(payload.callId, { action: 'continue' })
          return
        }
        this.#pendingHooks.set(payload.callId, now)
        this.#post({ type: 'hook', kind: 'invoke', hookId: payload.hookId, callId: payload.callId, request: payload.request ?? null })
      }),
      this.#deps.control.subscribe(observe, (payload) => {
        if (!mine(payload) || !this.#ready || !this.#hooks.has(payload.hookId)) return
        this.#post({ type: 'hook', kind: 'observe', hookId: payload.hookId, request: payload.request ?? null, response: payload.response ?? null })
      }),
      this.#deps.control.subscribe(changed, (payload) => {
        if (!mine(payload) || payload.removed !== true || !this.#hooks.delete(payload.hookId)) return
        const reason = typeof payload.reason === 'string' && payload.reason ? `: ${payload.reason.slice(0, 200)}` : '.'
        this.#note('warn', `The relay removed hook ${payload.hookId}${reason}`)
      }),
    ]
  }

  /** One control-plane command for the plugin, within the in-flight cap and
   * cancelled when the plugin stops. */
  async #invoke(method: string, payload: unknown): Promise<unknown> {
    if (this.#inflight.size >= RUNTIME_LIMITS.inflight) throw new CallError('busy', `A plugin can have at most ${RUNTIME_LIMITS.inflight} control calls in flight.`)
    const controller = new AbortController()
    this.#inflight.add(controller)
    try {
      return await this.#deps.control.call(method, payload, controller.signal)
    } finally {
      this.#inflight.delete(controller)
    }
  }

  /** Spends one of a per-minute budget or refuses the call; returns the
   * pruned history to store back. */
  #rate(times: readonly number[], limit: number, what: string): number[] {
    const now = Date.now()
    const recent = times.filter((at) => now - at < 60_000)
    if (recent.length >= limit) throw new CallError('rate_limited', `A plugin can ${what} at most ${limit} times a minute.`)
    recent.push(now)
    return recent
  }

  #registerSurface(kind: SurfaceKind, id: string, fields: Pick<SlotContribution, 'title' | 'order' | 'wide' | 'description'> & { readonly scope: SlotScope }): { surface: string } {
    this.#need('ui:slots')
    const surface = `${kind}:${id}`
    const existing = this.#surfaces.get(surface)
    const { limit, what } = SURFACE_KINDS[kind]
    if (!existing && [...this.#surfaces.values()].filter((entry) => entry.kind === kind).length >= limit) throw new CallError('limit', `A plugin can register at most ${limit} ${what}.`)
    const slot: SlotContribution = { id: `${this.#owner}:${surface}`, owner: this.name, ...fields, render: (context) => this.#deps.renderSlot(this.id, surface, context) }
    const view: PluginSurface = existing?.view ?? { key: `${this.id}/${surface}`, pluginId: this.id, surface, tree: null, problem: '' }
    this.#surfaces.set(surface, { kind, slot, view })
    this.#publishSlots()
    return { surface }
  }

  #registerAction(spec: ActionSpec): null {
    this.#need('ui:slots')
    const header = spec.scope === 'header'
    const limit = header ? SLOT_LIMITS.headerButtons : SLOT_LIMITS.actions
    const others = [...this.#actions.entries()].filter(([id, action]) => id !== spec.id && (action.scope === 'header') === header).length
    if (others >= limit) throw new CallError('limit', `A plugin can register at most ${limit} ${header ? 'header buttons' : 'actions'}.`)
    this.#actions.set(spec.id, {
      id: `${this.#owner}:${spec.id}`,
      owner: this.name,
      scope: spec.scope,
      title: spec.title,
      ...(spec.icon ? { icon: spec.icon } : {}),
      ...(spec.tone ? { tone: spec.tone } : {}),
      run: (target) => { this.#runAction(spec.id, spec.scope, target) },
    })
    this.#deps.extensions.contribute(this.#owner, { actions: [...this.#actions.values()] })
    return null
  }

  #publishSlots(): void {
    this.#deps.extensions.contribute(this.#owner, { slots: [...this.#surfaces.values()].map((entry) => entry.slot) })
    this.#deps.onChange()
  }

  #openDialog(args: Record<string, unknown>): PluginDialog {
    const dialog = this.#dialog
    if (!dialog || args.id !== dialog.id) throw new CallError('not_found', 'That dialog is no longer open.')
    return dialog
  }

  #closeDialog(action: string | null, form: PageEvent['form']): void {
    const dialog = this.#dialog
    if (!dialog) return
    this.#dialog = null
    this.#post({ type: 'dialog', id: dialog.id, result: { action, form } })
    this.#deps.onChange()
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
        this.#pages.set(key, {
          key, pluginId: this.id, pluginName: this.name, pageId: spec.id, title: spec.title, ...(spec.icon ? { icon: spec.icon } : {}),
          tree: existing?.tree ?? null, problem: '', ...(existing?.badge ? { badge: existing.badge } : {}),
        })
        this.#publishPages()
        return { route: `plugin/${key}` }
      }
      case 'ui.unregisterPage': {
        this.#need('ui:pages')
        const id = localId(args, 'id', PAGE_ID_PATTERN)
        if (this.#pages.delete(pageKey(this.id, id))) {
          this.#forgetCanvases(id)
          this.#publishPages()
        }
        return null
      }
      case 'ui.render': {
        if (typeof args.pageId === 'string' && args.pageId.includes(':')) {
          this.#need('ui:slots')
          const entry = this.#surfaces.get(args.pageId)
          if (!entry) throw new CallError('not_found', `Register ${args.pageId.slice(0, 80)} before rendering it.`)
          const checked = checkTree(args.tree)
          const view = checked.ok ? { ...entry.view, tree: checked.tree, problem: '' } : { ...entry.view, problem: checked.problem }
          this.#surfaces.set(args.pageId, { ...entry, view })
          this.#deps.onChange()
          if (!checked.ok) throw new CallError('invalid_tree', checked.problem)
          return null
        }
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
      case 'ui.setBadge': {
        this.#need('ui:pages')
        const { pageId, badge } = badgeSpec(args)
        const key = pageKey(this.id, pageId)
        const page = this.#pages.get(key)
        if (!page) throw new CallError('not_found', `Register page "${pageId}" before giving it a badge.`)
        if (sameBadge(page.badge, badge)) return null
        const { badge: _previous, ...rest } = page
        this.#pages.set(key, badge ? { ...rest, badge } : rest)
        this.#publishPages()
        return null
      }
      case 'ui.confirm': {
        this.#need('ui:notify')
        const spec = confirmSpec(args)
        this.#needGesture('Asking the operator')
        if (this.#confirming) throw new CallError('busy', 'The plugin is already asking the operator something.')
        const controller = new AbortController()
        this.#confirming = controller
        this.#inflight.add(controller)
        try {
          return await this.#deps.confirm({ pluginName: this.name, ...spec }, controller.signal)
        } finally {
          this.#inflight.delete(controller)
          if (this.#confirming === controller) this.#confirming = null
          // Answering is itself an operator action: the plugin may follow up
          // (copy a result, ask again) right away.
          if (!controller.signal.aborted) this.#gestureAt = Date.now()
        }
      }
      case 'clipboard.writeText': {
        this.#need('ui:clipboard')
        const text = clipboardText(args)
        this.#needGesture('Writing to the clipboard')
        try {
          await this.#deps.clipboard(text)
        } catch (error) {
          throw new CallError('failed', `The clipboard refused the text: ${messageOf(error)}`)
        }
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
        if (spec.shortcut) {
          const holder = this.#deps.extensions.snapshot().commands.find((command) => command.bind && command.shortcut === spec.shortcut && command.id !== fullId)
          if (holder) throw new CallError('conflict', `${spec.shortcut} is already bound to "${holder.title}" (${holder.group}).`)
        }
        this.#commands.set(fullId, {
          id: fullId,
          title: spec.title,
          // Grouped under the plugin's name, so the palette always says
          // whose command it is.
          group: spec.group ? `${this.name} · ${spec.group}` : this.name,
          keywords: [...(spec.keywords ?? []), 'plugin', this.name],
          // A plugin's shortcut is always a real global key; the shell binds
          // it, and no other plugin can hold the same combination.
          ...(spec.shortcut ? { shortcut: spec.shortcut, bind: true } : {}),
          run: () => { this.#runCommand(spec.id) },
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
        if (!stampsActor(method)) return this.#invoke(method, args.payload)
        // Rule changes record who made them; the plugin cannot pass itself
        // off as the operator, so a payload that is not an object is refused.
        if (!isRecord(args.payload)) throw new CallError('invalid_argument', `${method} takes an object payload.`)
        return this.#invoke(method, { ...args.payload, actor: `plugin:${this.id}` })
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
        return this.#serial(async () => {
          const values = await this.#values()
          return Object.hasOwn(values, key) ? values[key] : null
        })
      }
      case 'storage.all': {
        this.#need('storage')
        return this.#serial(async () => {
          const values: Record<string, string> = {}
          for (const [key, value] of Object.entries(await this.#values())) if (!isSettingStorageKey(key)) values[key] = value
          return values
        })
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
        return this.#write({ [key]: value })
      }
      case 'storage.setMany': {
        this.#need('storage')
        return this.#write(storageBatch(args))
      }
      case 'storage.clear': {
        this.#need('storage')
        // The operator's settings stay: only the plugin's own keys go.
        return this.#serial(async () => {
          const keys = Object.keys(await this.#values()).filter((key) => !isSettingStorageKey(key))
          if (keys.length === 0) return null
          return this.#write(Object.fromEntries(keys.map((key) => [key, null])), false)
        })
      }
      case 'ui.registerWidget': {
        const spec = widgetSpec(args)
        return this.#registerSurface('widget', spec.id, { scope: 'overview', title: spec.title, ...(spec.order !== undefined ? { order: spec.order } : {}), ...(spec.wide ? { wide: true } : {}) })
      }
      case 'ui.registerInspectorTab': {
        const spec = inspectorTabSpec(args)
        return this.#registerSurface('inspector', spec.id, { scope: spec.scope, title: spec.title })
      }
      case 'ui.registerSettingsSection': {
        const spec = settingsSectionSpec(args)
        return this.#registerSurface('settings', spec.id, { scope: 'settings', title: spec.title, ...(spec.description ? { description: spec.description } : {}) })
      }
      case 'ui.registerAction':
        return this.#registerAction(actionSpec(args))
      case 'ui.registerHeaderButton':
        return this.#registerAction(headerButtonSpec(args))
      case 'ui.unregister': {
        this.#need('ui:slots')
        const id = typeof args.id === 'string' ? args.id : ''
        if (id.includes(':')) {
          if (this.#surfaces.delete(id)) {
            this.#forgetCanvases(id)
            this.#publishSlots()
          }
        } else if (this.#actions.delete(id)) {
          this.#deps.extensions.contribute(this.#owner, { actions: [...this.#actions.values()] })
        }
        return null
      }
      case 'ui.setLayout': {
        this.#need('ui:layout')
        const spec = layoutSpec(args, this.#deps.isRoute)
        const snapshot = this.#deps.extensions.snapshot()
        if (snapshot.layoutOwner && snapshot.layoutOwner !== this.#owner) throw new CallError('conflict', `The layout is already customised by ${snapshot.layout?.owner ?? 'another plugin'}.`)
        this.#deps.extensions.contribute(this.#owner, {
          layout: { owner: this.name, ...spec, reset: () => this.#deps.extensions.contribute(this.#owner, { layout: undefined }) },
        })
        return null
      }
      case 'ui.resetLayout':
        this.#need('ui:layout')
        this.#deps.extensions.contribute(this.#owner, { layout: undefined })
        return null
      case 'ui.dialog': {
        this.#need('ui:dialogs')
        const spec = dialogSpec(args)
        this.#needGesture('Opening a dialog')
        if (this.#dialog) throw new CallError('busy', 'The plugin already has a dialog open.')
        const checked = checkTree(spec.tree)
        if (!checked.ok) throw new CallError('invalid_tree', checked.problem)
        this.#dialogSeq += 1
        const id = `d${this.#dialogSeq}`
        this.#dialog = { pluginId: this.id, pluginName: this.name, id, title: spec.title, actions: spec.actions, size: spec.size, tree: checked.tree, problem: '' }
        this.#deps.onChange()
        return { id }
      }
      case 'ui.dialogUpdate': {
        this.#need('ui:dialogs')
        const dialog = this.#openDialog(args)
        const checked = checkTree(args.tree)
        this.#dialog = checked.ok ? { ...dialog, tree: checked.tree, problem: '' } : { ...dialog, problem: checked.problem }
        this.#deps.onChange()
        if (!checked.ok) throw new CallError('invalid_tree', checked.problem)
        return null
      }
      case 'ui.dialogClose':
        this.#need('ui:dialogs')
        if (this.#dialog && args.id === this.#dialog.id) this.#closeDialog(null, {})
        return null
      case 'ui.theme':
        return this.#deps.theme.read()
      case 'net.fetch': {
        this.#need('net:fetch')
        const spec = fetchSpec(args)
        if (this.#fetching >= DATA_LIMITS.fetchInflight) throw new CallError('rate_limited', `A plugin can have at most ${DATA_LIMITS.fetchInflight} requests in flight.`)
        this.#fetchTimes = this.#rate(this.#fetchTimes, DATA_LIMITS.fetchPerMinute, 'fetch')
        this.#fetching += 1
        try {
          return await this.#invoke('plugins.fetch', { id: this.id, ...spec })
        } finally {
          this.#fetching -= 1
        }
      }
      case 'db.get': {
        this.#need('storage')
        const result = await this.#invoke('plugins.collection.get', { id: this.id, collection: collectionName(args), docId: docId(args) })
        return isRecord(result) && result.doc !== undefined ? result.doc : null
      }
      case 'db.put': {
        this.#need('storage')
        const payload = { id: this.id, collection: collectionName(args), docId: docId(args), doc: docValue(args.doc) }
        this.#dbWrites = this.#rate(this.#dbWrites, DATA_LIMITS.writesPerMinute, 'write its collections')
        await this.#invoke('plugins.collection.put', payload)
        return null
      }
      case 'db.delete': {
        this.#need('storage')
        const payload = { id: this.id, collection: collectionName(args), docId: docId(args) }
        this.#dbWrites = this.#rate(this.#dbWrites, DATA_LIMITS.writesPerMinute, 'write its collections')
        const result = await this.#invoke('plugins.collection.delete', payload)
        return isRecord(result) && result.deleted === true
      }
      case 'db.list':
        this.#need('storage')
        return this.#invoke('plugins.collection.list', { id: this.id, collection: collectionName(args), ...listSpec(args) })
      case 'db.clear': {
        this.#need('storage')
        const payload = { id: this.id, collection: collectionName(args) }
        this.#dbWrites = this.#rate(this.#dbWrites, DATA_LIMITS.writesPerMinute, 'write its collections')
        const result = await this.#invoke('plugins.collection.clear', payload)
        return isRecord(result) && typeof result.cleared === 'number' ? result.cleared : 0
      }
      case 'db.stats':
        this.#need('storage')
        return this.#invoke('plugins.collection.stats', { id: this.id })
      case 'files.open': {
        this.#need('files')
        const spec = openSpec(args)
        this.#needGesture('Opening a file')
        const controller = new AbortController()
        this.#inflight.add(controller)
        try {
          const file = await this.#deps.files.open(spec, controller.signal)
          if (!file) return null
          const name = file.name.slice(0, DATA_LIMITS.fileName)
          if (file.size > spec.maxBytes) throw new CallError('too_large', `${name} is ${Math.ceil(file.size / 1024)} KiB; the plugin reads at most ${Math.floor(spec.maxBytes / 1024)} KiB.`)
          return { name, text: await file.read(), size: file.size }
        } finally {
          this.#inflight.delete(controller)
          if (!controller.signal.aborted) this.#gestureAt = Date.now()
        }
      }
      case 'files.save': {
        this.#need('files')
        const spec = saveSpec(args)
        this.#needGesture('Saving a file')
        const saved = await this.#deps.files.save(spec)
        this.#gestureAt = Date.now()
        return saved
      }
      case 'bus.publish': {
        this.#need('bus')
        const name = channel(args)
        const payload = busPayload(args.payload)
        this.#published = this.#rate(this.#published, DATA_LIMITS.busPerMinute, 'publish')
        this.#deps.bus.publish(this.id, name, payload)
        return null
      }
      case 'bus.subscribe': {
        this.#need('bus')
        const name = channel(args)
        if (this.#busSubs.has(name)) return null
        if (this.#busSubs.size >= DATA_LIMITS.busSubscriptions) throw new CallError('limit', `A plugin can follow at most ${DATA_LIMITS.busSubscriptions} bus channels.`)
        this.#busSubs.set(name, this.#deps.bus.subscribe(this.id, name, (payload, from) => this.#post({ type: 'bus', channel: name, payload, from })))
        return null
      }
      case 'bus.unsubscribe': {
        const name = typeof args.channel === 'string' ? args.channel : ''
        this.#busSubs.get(name)?.()
        this.#busSubs.delete(name)
        return null
      }
      case 'relay.register': {
        this.#need('relay:content')
        const spec = hookSpec(args)
        this.#followHooks()
        const known = this.#hooks.has(spec.hookId)
        // Known before the relay can call it, so the first request is not
        // waved through.
        this.#hooks.add(spec.hookId)
        try {
          await this.#invoke('relayhooks.register', {
            pluginId: this.id, hookId: spec.hookId, phase: spec.phase, models: spec.models, endpoints: spec.endpoints,
            ...(spec.phase === 'request' ? { timeoutMs: spec.timeoutMs } : {}),
          })
        } catch (error) {
          if (!known) this.#hooks.delete(spec.hookId)
          throw error
        }
        return null
      }
      case 'relay.unregister': {
        this.#need('relay:content')
        const hookId = localId(args, 'hookId', HOOK_ID_PATTERN)
        if (!this.#hooks.has(hookId)) return null
        await this.#invoke('relayhooks.unregister', { pluginId: this.id, hookId })
        this.#hooks.delete(hookId)
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

  /** One storage write, budgeted and in order. Every key either changes or
   * none does. */
  #write(changes: Record<string, string | null>, queue = true): Promise<null> {
    const now = Date.now()
    this.#writes = this.#writes.filter((at) => now - at < 60_000)
    if (this.#writes.length >= RUNTIME_LIMITS.storageWritesPerMinute) {
      throw new CallError('rate_limited', `A plugin can write its storage at most ${RUNTIME_LIMITS.storageWritesPerMinute} times a minute; batch changes with storage.setMany.`)
    }
    this.#writes.push(now)
    const run = async (): Promise<null> => {
      this.#storage = await this.#deps.storage.storageUpdate(this.id, changes)
      return null
    }
    return queue ? this.#serial(run) : run()
  }

  #serial<T>(task: () => Promise<T>): Promise<T> {
    const run = this.#storageQueue.then(task)
    this.#storageQueue = run.catch(() => undefined)
    return run
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
      pages: [...this.#pages.values()].map((page) => ({
        id: page.key, title: page.title, ...(page.icon ? { icon: page.icon } : {}), ...(page.badge ? { badge: page.badge } : {}),
      })),
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
    this.#deps.extensions.contribute(this.#owner, { commands: [], status: [], pages: [], slots: [], actions: [], layout: undefined })
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
    this.#confirming = null
    this.#runs.clear()
    this.#commands.clear()
    this.#status.clear()
    this.#frameCancel?.()
    this.#frameCancel = null
    this.#frames.clear()
    this.#frameSentAt = 0
    this.#frameLast = 0
    this.#themeUnsub?.()
    this.#themeUnsub = null
    for (const unsubscribe of this.#busSubs.values()) unsubscribe()
    this.#busSubs.clear()
    for (const unsubscribe of this.#hookTopics) unsubscribe()
    this.#hookTopics = []
    // Hooks fail open: a request waiting on this plugin goes through as is.
    for (const callId of this.#pendingHooks.keys()) this.#respond(callId, { action: 'continue' })
    this.#pendingHooks.clear()
    if (this.#hooks.size > 0) this.#deps.control.call('relayhooks.unregister', { pluginId: this.id, hookId: '' }).catch(() => undefined)
    this.#hooks.clear()
    this.#dialog = null
    this.#surfaces.clear()
    this.#actions.clear()
    this.#draws.clear()
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

/** What a running host depends on: a different package (even one saved in
 * the same millisecond) or a different grant restarts it. */
export function revisionOf(plugin: Pick<PluginSummary, 'updatedAt' | 'version' | 'sizeBytes'>, grants: readonly string[]): string {
  return `${plugin.updatedAt}|${plugin.version}|${plugin.sizeBytes}|${[...grants].sort().join(',')}`
}

function canvasKey(surface: string, canvas: string): string {
  return `${surface}\n${canvas}`
}

function sameValues(a: Readonly<Record<string, SettingValue>>, b: Readonly<Record<string, SettingValue>>): boolean {
  const keys = Object.keys(a)
  return keys.length === Object.keys(b).length && keys.every((key) => Object.hasOwn(b, key) && b[key] === a[key])
}

function sameBadge(a: BadgeContribution | undefined, b: BadgeContribution | null): boolean {
  if (!a || !b) return !a && !b
  return a.text === b.text && a.tone === b.tone
}

function messageOf(error: unknown): string {
  if (error instanceof Error && error.message) return error.message
  return typeof error === 'string' && error ? error : 'Unknown error'
}
