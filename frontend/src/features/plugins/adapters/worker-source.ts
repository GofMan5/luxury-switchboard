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
    'Worker', 'SharedWorker', 'BroadcastChannel', 'WebTransport', 'WebSocketStream', 'RTCPeerConnection', 'Notification',
    'fetchLater', 'postMessage'];
  for (const name of BLOCKED) seal(scope, name, refuse(name));
  if (scope.navigator) for (const name of ['storage', 'serviceWorker', 'sendBeacon', 'locks']) seal(scope.navigator, name, undefined);

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

  // A fault names the command it came from, so the operator who ran it
  // hears that it failed.
  const reporter = function (command) {
    return function (error) { post(command ? { type: 'fault', message: describe(error), command: command } : { type: 'fault', message: describe(error) }); };
  };
  const fault = reporter('');
  const guard = function (handler, value, extra, command) {
    const report = command ? reporter(command) : fault;
    try {
      const result = handler(value, extra);
      if (result && typeof result.then === 'function') result.then(undefined, report);
    } catch (error) {
      report(error);
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
  const settingHandlers = new Set();
  let settings = freeze(Object.assign({}, boot.settings));
  let secrets = freeze((boot.secrets || []).slice());
  let active = false;
  const frameHandlers = new Set();
  const inputHandlers = new Set();
  const themeHandlers = new Set();
  const channels = new Map();
  const hooks = new Map();
  const dialogs = new Map();
  let nextHook = 1;

  // A hook's answer goes back once; anything that goes wrong lets the
  // request continue (fail-open), as a timeout would.
  const answerHook = function (callId, decision) {
    let data = { action: 'continue' };
    try { if (decision && typeof decision === 'object') data = JSON.parse(JSON.stringify(decision)); } catch (error) { fault(error); }
    post({ type: 'hookResult', callId: callId, decision: data });
  };
  const runHook = function (message) {
    const hook = hooks.get(message.hookId);
    const request = freeze(Object.assign({}, message.request));
    if (message.kind === 'observe') {
      if (hook && hook.phase === 'response') guard(hook.handler, request, freeze(Object.assign({}, message.response)));
      return;
    }
    if (typeof message.callId !== 'string') return;
    if (!hook || hook.phase !== 'request') { answerHook(message.callId); return; }
    let result;
    try { result = hook.handler(request); } catch (error) { fault(error); answerHook(message.callId); return; }
    Promise.resolve(result).then(function (decision) { answerHook(message.callId, decision); }, function (error) { fault(error); answerHook(message.callId); });
  };

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
        if (handler) guard(handler, undefined, undefined, String(message.id));
        return;
      }
      case 'settings': {
        const next = freeze(Object.assign({}, message.values));
        const changed = freeze(Object.keys(next).filter(function (key) { return next[key] !== settings[key]; }));
        settings = next;
        if (Array.isArray(message.secrets)) secrets = freeze(message.secrets.slice());
        if (changed.length === 0) return;
        for (const handler of Array.from(settingHandlers)) guard(handler, next, changed);
        return;
      }
      case 'frame':
        // The host sends the next frame only after this one is done.
        for (const handler of Array.from(frameHandlers)) {
          try { handler(message.dtMs, message.tMs); } catch (error) { fault(error); }
        }
        post({ type: 'frameDone' });
        return;
      case 'input': {
        const detail = freeze(Object.assign({}, message.event));
        for (const handler of Array.from(inputHandlers)) guard(handler, detail);
        return;
      }
      case 'theme': {
        const theme = freeze(Object.assign({}, message.theme));
        for (const handler of Array.from(themeHandlers)) guard(handler, theme);
        return;
      }
      case 'bus': {
        const subscription = channels.get(message.channel);
        if (subscription) for (const handler of Array.from(subscription.handlers)) guard(handler, message.payload, String(message.from));
        return;
      }
      case 'hook':
        runHook(message);
        return;
      case 'dialog': {
        const settle = dialogs.get(message.id);
        if (!settle) return;
        dialogs.delete(message.id);
        const result = message.result || {};
        settle(freeze({ action: typeof result.action === 'string' ? result.action : null, form: freeze(Object.assign({}, result.form)) }));
        return;
      }
      default:
    }
  });

  // The handler is known before the relay can call it, so the first request
  // is not waved through.
  const hook = function (phase, filter, handler) {
    needFunction(handler, 'handler');
    const hookId = 'h' + nextHook;
    nextHook += 1;
    const entry = { phase: phase, handler: handler };
    hooks.set(hookId, entry);
    const spec = Object.assign({}, filter, { hookId: hookId, phase: phase });
    if (phase === 'response') delete spec.timeoutMs;
    return call('relay.register', spec).then(function () {
      let removing = null;
      return function () {
        if (!removing) removing = call('relay.unregister', { hookId: hookId }).then(function () { hooks.delete(hookId); }, function (error) { removing = null; throw error; });
        return removing;
      };
    }, function (error) {
      hooks.delete(hookId);
      throw error;
    });
  };

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
      setBadge: function (pageId, badge) { return call('ui.setBadge', { pageId: pageId, badge: badge === undefined ? null : badge }); },
      confirm: function (spec) { return call('ui.confirm', spec); },
      registerWidget: function (spec) { return call('ui.registerWidget', spec); },
      registerInspectorTab: function (spec) { return call('ui.registerInspectorTab', spec); },
      registerSettingsSection: function (spec) { return call('ui.registerSettingsSection', spec); },
      registerAction: function (spec) { return call('ui.registerAction', spec); },
      registerHeaderButton: function (spec) { return call('ui.registerHeaderButton', spec); },
      unregister: function (id) { return call('ui.unregister', { id: id }); },
      setLayout: function (layout) { return call('ui.setLayout', layout); },
      resetLayout: function () { return call('ui.resetLayout'); },
      dialog: function (spec) {
        return call('ui.dialog', spec).then(function (opened) {
          const id = opened.id;
          const result = new Promise(function (resolve) { dialogs.set(id, resolve); });
          return freeze({
            id: id,
            update: function (tree) { return call('ui.dialogUpdate', { id: id, tree: tree }); },
            close: function () { return call('ui.dialogClose', { id: id }); },
            result: result,
          });
        });
      },
      sound: freeze({
        tone: function (spec) {
          if (!spec || typeof spec !== 'object') throw new TypeError('tone needs a spec object');
          post({ type: 'tone', spec: JSON.parse(JSON.stringify(spec)) });
        },
      }),
      theme: function () { return call('ui.theme'); },
      onTheme: function (handler) {
        const entry = function (theme) { return handler(theme); };
        needFunction(handler, 'handler');
        themeHandlers.add(entry);
        return function () { themeHandlers.delete(entry); };
      },
      draw: function (surfaceId, canvasId, ops) {
        if (typeof surfaceId !== 'string' || typeof canvasId !== 'string' || surfaceId.length > 64 || canvasId.length > 64) throw new TypeError('draw needs a surface id and a canvas id');
        post({ type: 'draw', surface: surfaceId, canvas: canvasId, ops: ops });
      },
      onFrame: function (handler) {
        const entry = function (dtMs, tMs) { return handler(dtMs, tMs); };
        needFunction(handler, 'handler');
        frameHandlers.add(entry);
        return function () { frameHandlers.delete(entry); };
      },
      onInput: function (handler) {
        const entry = function (event) { return handler(event); };
        needFunction(handler, 'handler');
        inputHandlers.add(entry);
        return function () { inputHandlers.delete(entry); };
      },
    }),
    settings: freeze({
      get: function (key) { return hasOwn.call(settings, key) ? settings[key] : undefined; },
      all: function () { return settings; },
      get secrets() { return secrets; },
      onChange: function (handler) {
        needFunction(handler, 'handler');
        const entry = function (values, changed) { return handler(values, changed); };
        settingHandlers.add(entry);
        return function () { settingHandlers.delete(entry); };
      },
    }),
    clipboard: freeze({
      writeText: function (text) { return call('clipboard.writeText', { text: text }); },
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
      setMany: function (values) { return call('storage.setMany', { values: values }); },
      clear: function () { return call('storage.clear'); },
    }),
    navigate: function (route) { return call('navigate', { route: route }); },
    log: logger,
    db: freeze({
      collection: function (name) {
        return freeze({
          get: function (id) { return call('db.get', { collection: name, docId: id }); },
          put: function (id, doc) { return call('db.put', { collection: name, docId: id, doc: doc }); },
          delete: function (id) { return call('db.delete', { collection: name, docId: id }); },
          list: function (options) { return call('db.list', Object.assign({}, options, { collection: name })); },
          clear: function () { return call('db.clear', { collection: name }); },
          stats: function () { return call('db.stats'); },
        });
      },
    }),
    files: freeze({
      open: function (options) { return call('files.open', options || {}); },
      save: function (file) { return call('files.save', file); },
    }),
    bus: freeze({
      publish: function (channel, payload) { return call('bus.publish', { channel: channel, payload: payload }); },
      subscribe: function (channel, handler) {
        needFunction(handler, 'handler');
        const name = String(channel);
        let subscription = channels.get(name);
        if (!subscription) {
          const created = { handlers: new Set() };
          subscription = created;
          channels.set(name, created);
          call('bus.subscribe', { channel: name }).catch(function (error) {
            if (channels.get(name) === created) channels.delete(name);
            fault(error);
          });
        }
        const current = subscription;
        const entry = function (payload, from) { return handler(payload, from); };
        current.handlers.add(entry);
        let off = false;
        return function () {
          if (off) return;
          off = true;
          current.handlers.delete(entry);
          if (current.handlers.size === 0 && channels.get(name) === current) {
            channels.delete(name);
            call('bus.unsubscribe', { channel: name }).catch(function () { /* stopping anyway */ });
          }
        };
      },
    }),
    net: freeze({
      fetch: function (url, init) {
        return call('net.fetch', Object.assign({}, init, { url: url })).then(function (answer) {
          const text = typeof answer.body === 'string' ? answer.body : '';
          return freeze({
            status: answer.status,
            ok: answer.status >= 200 && answer.status <= 299,
            headers: freeze(Object.assign({}, answer.headers)),
            text: text,
            json: function () { return JSON.parse(text); },
          });
        });
      },
    }),
    rules: freeze({
      list: function () { return api.control.call('rules.list', {}); },
      saveRequestRule: function (rule) { return api.control.call('rules.saveRequestRule', { rule: rule }); },
      deleteRequestRule: function (id) { return api.control.call('rules.deleteRequestRule', { id: id }); },
      saveRetryRule: function (rule) { return api.control.call('rules.saveRetryRule', { rule: rule }); },
      deleteRetryRule: function (id) { return api.control.call('rules.deleteRetryRule', { id: id }); },
      test: function (input) { return api.control.call('rules.test', input); },
      stats: function () { return api.control.call('rules.stats', {}); },
      recentErrors: function (limit) { return api.control.call('rules.recentErrors', limit === undefined ? {} : { limit: limit }); },
    }),
    relay: freeze({
      onRequest: function (filter, handler) { return hook('request', filter, handler); },
      onResponse: function (filter, handler) { return hook('response', filter, handler); },
    }),
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
