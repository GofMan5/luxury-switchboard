import type { BootContext } from '../application/ports'

/**
 * The code that runs in a plugin's worker before the plugin's own script.
 *
 * It seals every way out of the worker except the host — network, storage,
 * nested workers, broadcast channels, notifications — so the only thing a
 * plugin can reach is the frozen `switchboard` object, and every call on it
 * becomes a message the host checks. The window's Content-Security-Policy is
 * the real network boundary (`connect-src` names only the IPC origin); this
 * is the second wall, and it turns a blocked request into a clear error
 * instead of a CSP report.
 *
 * Plain JavaScript in a string: it runs in another global, not in this
 * bundle. It must not contain backticks, backslashes or template holes.
 */
const PRELUDE = `'use strict';
const __switchboardStart = (function (scope, boot) {
  const post = scope.postMessage.bind(scope);
  const listen = scope.addEventListener.bind(scope);
  const freeze = Object.freeze;
  const hasOwn = Object.prototype.hasOwnProperty;

  const refuse = function (name) {
    return function () {
      throw new TypeError(name + ' is not available to plugins. Use switchboard.control.call() to reach the Switchboard.');
    };
  };
  const seal = function (target, name, value) {
    for (let object = Object.getPrototypeOf(target); object; object = Object.getPrototypeOf(object)) {
      if (!hasOwn.call(object, name)) continue;
      try { Object.defineProperty(object, name, { value: value, writable: false, configurable: false }); } catch (ignored) { /* already sealed */ }
    }
    try { Object.defineProperty(target, name, { value: value, writable: false, configurable: false, enumerable: false }); } catch (ignored) { /* already sealed */ }
  };
  const BLOCKED = ['fetch', 'XMLHttpRequest', 'WebSocket', 'EventSource', 'importScripts', 'indexedDB', 'caches',
    'Worker', 'SharedWorker', 'BroadcastChannel', 'WebTransport', 'RTCPeerConnection', 'Notification', 'postMessage'];
  for (const name of BLOCKED) seal(scope, name, refuse(name));
  if (scope.navigator) for (const name of ['storage', 'serviceWorker', 'sendBeacon']) seal(scope.navigator, name, undefined);

  const describe = function (error) {
    if (error && typeof error === 'object') {
      if (typeof error.stack === 'string' && error.stack) return error.stack.slice(0, 2000);
      if (typeof error.message === 'string') return error.message.slice(0, 2000);
    }
    return String(error).slice(0, 2000);
  };
  const format = function (value) {
    if (typeof value === 'string') return value;
    if (value instanceof Error) return describe(value);
    try { const text = JSON.stringify(value); return text === undefined ? String(value) : text; } catch (ignored) { return String(value); }
  };

  // Console output goes to the plugin's log in the manager, within a budget,
  // so a chatty loop drops lines instead of tripping the flood limit.
  const original = scope.console;
  let logStart = 0;
  let logCount = 0;
  let dropped = 0;
  const log = function (level, values) {
    if (original && typeof original[level] === 'function') { try { original[level].apply(original, values); } catch (ignored) { /* devtools only */ } }
    const now = Date.now();
    if (now - logStart > 5000) {
      if (dropped > 0) post({ type: 'log', level: 'warn', text: dropped + ' log lines were dropped (more than 100 in 5 seconds).' });
      logStart = now;
      logCount = 0;
      dropped = 0;
    }
    logCount += 1;
    if (logCount > 100) { dropped += 1; return; }
    post({ type: 'log', level: level, text: Array.prototype.map.call(values, format).join(' ').slice(0, 2000) });
  };
  const logger = freeze({
    log: function () { log('info', arguments); },
    info: function () { log('info', arguments); },
    debug: function () { log('debug', arguments); },
    warn: function () { log('warn', arguments); },
    error: function () { log('error', arguments); },
  });
  seal(scope, 'console', logger);

  const fault = function (error) { post({ type: 'fault', message: describe(error) }); };
  const guard = function (handler, value) {
    try {
      const result = handler(value);
      if (result && typeof result.then === 'function') result.then(undefined, fault);
    } catch (error) {
      fault(error);
    }
  };
  listen('error', function (event) { event.preventDefault(); fault(event.error || event.message); });
  listen('unhandledrejection', function (event) { event.preventDefault(); fault(event.reason); });

  let nextId = 1;
  const pending = new Map();
  const call = function (op, args) {
    return new Promise(function (resolve, reject) {
      let data = {};
      if (args !== undefined) {
        try { data = JSON.parse(JSON.stringify(args)); } catch (error) { reject(new TypeError('Arguments must be plain JSON data: ' + describe(error))); return; }
      }
      const id = nextId;
      nextId += 1;
      pending.set(id, { resolve: resolve, reject: reject });
      post({ type: 'call', id: id, op: op, args: data });
    });
  };
  const needFunction = function (value, what) {
    if (typeof value !== 'function') throw new TypeError(what + ' must be a function');
    return value;
  };

  const pageHandlers = new Set();
  const topics = new Map();
  const commands = new Map();
  const activateHandlers = [];
  let active = false;

  listen('message', function (event) {
    const message = event.data;
    if (!message || typeof message !== 'object') return;
    switch (message.type) {
      case 'ping':
        post({ type: 'pong', seq: message.seq });
        return;
      case 'activate':
        if (active) return;
        active = true;
        for (const handler of activateHandlers.splice(0)) guard(handler);
        return;
      case 'result': {
        const entry = pending.get(message.id);
        if (!entry) return;
        pending.delete(message.id);
        if (message.ok) { entry.resolve(message.value); return; }
        const error = new Error(message.error && message.error.message || 'The call failed');
        error.code = message.error && message.error.code || 'failed';
        entry.reject(error);
        return;
      }
      case 'ui': {
        const detail = freeze(Object.assign({ pageId: message.pageId }, message.event, { form: freeze(Object.assign({}, message.event && message.event.form)) }));
        for (const entry of Array.from(pageHandlers)) if (!entry.pageId || entry.pageId === message.pageId) guard(entry.handler, detail);
        return;
      }
      case 'topic': {
        const subscription = topics.get(message.topic);
        if (subscription) for (const handler of Array.from(subscription.handlers)) guard(handler, message.payload);
        return;
      }
      case 'command': {
        const handler = commands.get(message.id);
        if (handler) guard(handler);
        return;
      }
      default:
    }
  });

  const granted = freeze(boot.plugin.granted.slice());
  const api = freeze({
    plugin: freeze({ id: boot.plugin.id, name: boot.plugin.name, version: boot.plugin.version, granted: granted }),
    app: freeze({ version: boot.app.version, capabilities: freeze(boot.app.capabilities.slice()) }),
    has: function (permission) { return granted.indexOf(permission) !== -1; },
    onActivate: function (handler) {
      needFunction(handler, 'handler');
      if (active) Promise.resolve().then(function () { guard(handler); });
      else activateHandlers.push(handler);
    },
    ui: freeze({
      registerPage: function (spec) { return call('ui.registerPage', spec); },
      unregisterPage: function (id) { return call('ui.unregisterPage', { id: id }); },
      render: function (pageId, tree) { return call('ui.render', { pageId: pageId, tree: tree }); },
      onEvent: function (pageIdOrHandler, maybeHandler) {
        const entry = typeof pageIdOrHandler === 'function'
          ? { pageId: '', handler: pageIdOrHandler }
          : { pageId: String(pageIdOrHandler), handler: needFunction(maybeHandler, 'handler') };
        pageHandlers.add(entry);
        return function () { pageHandlers.delete(entry); };
      },
      setStatus: function (id, status) { return call('ui.setStatus', { id: id, status: status === undefined ? null : status }); },
    }),
    commands: freeze({
      register: function (spec, handler) {
        needFunction(handler, 'handler');
        const id = spec && spec.id;
        const previous = commands.get(id);
        commands.set(id, handler);
        return call('commands.register', spec).catch(function (error) {
          if (commands.get(id) === handler) { if (previous) commands.set(id, previous); else commands.delete(id); }
          throw error;
        });
      },
      unregister: function (id) {
        commands.delete(id);
        return call('commands.unregister', { id: id });
      },
    }),
    notify: function (spec) { return call('notify', spec); },
    control: freeze({
      call: function (method, payload) { return call('control.call', payload === undefined ? { method: method } : { method: method, payload: payload }); },
    }),
    events: freeze({
      on: function (topic, handler) {
        needFunction(handler, 'handler');
        let subscription = topics.get(topic);
        if (!subscription) {
          subscription = { handlers: new Set(), ready: null };
          const created = subscription;
          created.ready = call('events.subscribe', { topic: topic }).catch(function (error) {
            if (topics.get(topic) === created) topics.delete(topic);
            throw error;
          });
          topics.set(topic, created);
        }
        const current = subscription;
        return current.ready.then(function () {
          current.handlers.add(handler);
          let off = false;
          return function () {
            if (off) return;
            off = true;
            current.handlers.delete(handler);
            if (current.handlers.size === 0 && topics.get(topic) === current) {
              topics.delete(topic);
              call('events.unsubscribe', { topic: topic }).catch(function () { /* stopping anyway */ });
            }
          };
        });
      },
    }),
    storage: freeze({
      get: function (key) { return call('storage.get', { key: key }); },
      set: function (key, value) { return call('storage.set', { key: key, value: value }); },
      remove: function (key) { return call('storage.remove', { key: key }); },
      all: function () { return call('storage.all'); },
    }),
    navigate: function (route) { return call('navigate', { route: route }); },
    log: logger,
  });
  seal(scope, 'switchboard', api);

  let started = false;
  return function start(main) {
    if (started) return;
    started = true;
    Promise.resolve()
      .then(function () { return main.call(undefined, api); })
      .then(function () { post({ type: 'ready' }); }, function (error) { post({ type: 'fail', message: describe(error) }); });
  };
})(self, `

export interface WorkerSource {
  readonly text: string
  /** Lines before the plugin's first line, to map error positions back. */
  readonly offset: number
}

/** The worker's whole program: the prelude, the boot facts, then the plugin
 * script as the body of an async function, so top-level `await` works and
 * the worker reports ready only when the script's start-up has finished. */
export function workerSource(script: string, context: BootContext): WorkerSource {
  const head = `${PRELUDE}${JSON.stringify(context)});\n__switchboardStart(async function (switchboard) {\n'use strict';\n`
  return { text: `${head}${script}\n});\n`, offset: head.split('\n').length - 1 }
}
