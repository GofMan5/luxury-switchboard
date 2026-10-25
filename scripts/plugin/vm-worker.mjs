// A WorkerFactory for Node: runs the exact text BlobWorkerFactory hands to a
// Web Worker (prelude + plugin script) in a node:vm context, so `pnpm plugin
// run` drives the real PluginHost against the real worker prelude.
//
// node:vm is NOT a security boundary. The script runs inside this process and
// can escape the context through any host object it is handed; only run code
// you trust.
import { randomUUID, getRandomValues } from 'node:crypto'
import process from 'node:process'
import vm from 'node:vm'

const FILENAME = 'plugin.js'
const live = new Set()

// Promises made inside a context report unhandled rejections to this
// process. Each goes to the worker whose realm made it, the way a browser
// fires `unhandledrejection` on the worker's own global.
let routing = false
function routeRejections() {
  if (routing) return
  routing = true
  process.on('unhandledRejection', (reason, promise) => {
    for (const worker of live) if (worker.owns(promise)) return worker.rejected(reason)
    process.stderr.write(`Unhandled rejection outside any plugin: ${reason instanceof Error ? reason.stack : String(reason)}\n`)
    process.exitCode = 1
  })
}

function lineOf(error, offset) {
  const match = typeof error?.stack === 'string' ? new RegExp(`${FILENAME}:(\\d+)`, 'u').exec(error.stack) : null
  return match ? Number(match[1]) - offset : 0
}

const describe = (error) => (error && typeof error === 'object' && 'message' in error ? `${error.name ?? 'Error'}: ${error.message}` : String(error))

class VmWorker {
  #context
  #entry
  #parse
  #promise
  #offset
  #timeoutMs
  #listeners = { message: [], error: [], unhandledrejection: [] }
  #onMessage = []
  #onError = []
  #timers = new Map()
  #nextTimer = 1
  #closed = false

  constructor(text, offset, timeoutMs, globals = {}) {
    this.#offset = offset
    this.#timeoutMs = timeoutMs
    const timer = (repeat) => (callback, delay, ...args) => {
      if (typeof callback !== 'function' || this.#closed) return 0
      const id = this.#nextTimer++
      this.#timers.set(id, (repeat ? setInterval : setTimeout)(() => {
        if (!repeat) this.#timers.delete(id)
        this.#enter(() => callback(...args))
      }, Number(delay) || 0))
      return id
    }
    const clear = (id) => {
      const handle = this.#timers.get(id)
      if (handle === undefined) return
      clearTimeout(handle)
      clearInterval(handle)
      this.#timers.delete(id)
    }
    this.#context = vm.createContext({
      postMessage: (data) => {
        const copy = structuredClone(data) // throws DataCloneError, as a worker's postMessage does
        setImmediate(() => { if (!this.#closed) for (const listener of this.#onMessage) listener(copy) })
      },
      addEventListener: (type, listener) => { if (typeof listener === 'function') this.#listeners[type]?.push(listener) },
      setTimeout: timer(false), setInterval: timer(true), clearTimeout: clear, clearInterval: clear,
      // The CLI prints the plugin's log as the host records it, so the
      // prelude's devtools mirror stays quiet instead of printing twice.
      console: Object.freeze({ log() {}, info() {}, debug() {}, warn() {}, error() {} }),
      TextEncoder, TextDecoder, URL, URLSearchParams, atob, btoa,
      crypto: Object.freeze({ randomUUID: () => randomUUID(), getRandomValues: (array) => getRandomValues(array) }),
      performance: Object.freeze({ now: () => performance.now(), timeOrigin: performance.timeOrigin }),
      ...globals,
    }, { name: 'switchboard-plugin', microtaskMode: 'afterEvaluate' })
    // `afterEvaluate` gives the context its own microtask queue, drained at
    // the end of every entry under the same time limit, so a runaway promise
    // chain is caught like a runaway loop.
    vm.runInContext('globalThis.self = globalThis; globalThis.queueMicrotask = function (task) { Promise.resolve().then(task); };', this.#context)
    this.#promise = vm.runInContext('Promise', this.#context)
    this.#parse = vm.runInContext('JSON.parse', this.#context)
    this.#entry = new vm.Script('globalThis.__switchboardEntry()', { filename: 'host-entry.js' })

    let script
    try {
      script = new vm.Script(text, { filename: FILENAME })
    } catch (error) {
      this.#fail(`${describe(error)} (line ${lineOf(error, offset)} of the script)`)
      return
    }
    live.add(this)
    routeRejections()
    setImmediate(() => this.#enter(() => script.runInContext(this.#context, { displayErrors: false })))
  }

  /** Every way into the context is one evaluation with a time limit. */
  #enter(task) {
    if (this.#closed) return
    this.#context.__switchboardEntry = task
    try {
      this.#entry.runInContext(this.#context, { timeout: this.#timeoutMs, displayErrors: false })
    } catch (error) {
      if (error?.code === 'ERR_SCRIPT_EXECUTION_TIMEOUT') this.#fail(`The script ran for ${this.#timeoutMs / 1000} seconds without yielding.`)
      else this.#uncaught(error)
    } finally {
      this.#context.__switchboardEntry = undefined
    }
  }

  /** An exception no plugin code caught: the worker's `error` event. */
  #uncaught(error) {
    let prevented = false
    const event = { error, message: error && typeof error === 'object' ? String(error.message) : String(error), preventDefault() { prevented = true } }
    for (const listener of this.#listeners.error) {
      try { listener(event) } catch { /* a broken handler counts as none */ }
    }
    if (prevented) return
    const line = lineOf(error, this.#offset)
    this.#fail(`${describe(error)}${line > 0 ? ` (line ${line} of the script)` : ''}`)
  }

  owns(promise) {
    return promise instanceof this.#promise
  }

  rejected(reason) {
    if (this.#closed) return
    let prevented = false
    const event = { reason, preventDefault() { prevented = true } }
    this.#enter(() => { for (const listener of this.#listeners.unhandledrejection) listener(event) })
    if (!prevented) this.#uncaught(reason)
  }

  #fail(message) {
    setImmediate(() => { if (!this.#closed) for (const listener of this.#onError) listener(message) })
  }

  post(message) {
    if (this.#closed) return
    const data = this.#parse(JSON.stringify(message)) // the context's own objects, as a structured clone would make
    setImmediate(() => this.#enter(() => { for (const listener of this.#listeners.message) listener({ data }) }))
  }

  /** Calls `globalThis.__cliHooks[name](payload)` inside the context, under the same time limit. */
  invoke(name, payload) {
    if (this.#closed) return
    const data = this.#parse(JSON.stringify(payload ?? null))
    this.#enter(() => this.#context.__cliHooks?.[name]?.(data))
  }

  onMessage(listener) { this.#onMessage.push(listener) }
  onError(listener) { this.#onError.push(listener) }

  terminate() {
    if (this.#closed) return
    this.#closed = true
    live.delete(this)
    for (const handle of this.#timers.values()) { clearTimeout(handle); clearInterval(handle) }
    this.#timers.clear()
  }
}

/** A WorkerFactory over the frontend's own workerSource(script, context). */
export function vmWorkerFactory(workerSource, { timeoutMs = 5_000, globals } = {}) {
  return {
    create(script, context) {
      const { text, offset } = workerSource(script, context)
      return new VmWorker(text, offset, timeoutMs, globals)
    },
  }
}
