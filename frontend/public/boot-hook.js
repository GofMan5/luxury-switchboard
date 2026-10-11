/* The boot sentinel: a plain classic script that runs before the app bundle
   and keeps the window lit when the bundle never arrives, never parses, or
   never commits its first frame. The bundle reports readiness through
   window.__dshBootReady (see src/app/boot.ts); until then any script error
   or a 15s silence swaps the pending message for a failure notice, so the
   failure mode is a sentence, not a black window. No framework, no build
   step, no inline handler — the CSP allows this file and nothing else that
   could run at this point. */
(function () {
  'use strict'

  var BOOT_TIMEOUT_MS = 15000
  var state = 'pending'
  var timer = 0

  function clearTimer() {
    if (timer !== 0) {
      window.clearTimeout(timer)
      timer = 0
    }
  }

  function ensureSentinel() {
    var host = document.getElementById('root') || document.body
    if (host === null) return null
    var sentinel = document.getElementById('boot-sentinel')
    if (sentinel === null) {
      // The first commit already wiped the pending message: rebuild it, so
      // the failure notice lands somewhere a user can see it.
      sentinel = document.createElement('div')
      sentinel.id = 'boot-sentinel'
      sentinel.setAttribute('role', 'status')
      sentinel.setAttribute('aria-live', 'polite')
      host.appendChild(sentinel)
    }
    return sentinel
  }

  function fail(detail) {
    if (state !== 'pending') return
    state = 'failed'
    clearTimer()
    var sentinel = ensureSentinel()
    if (sentinel === null) return
    sentinel.setAttribute('data-state', 'failed')
    var text = document.createElement('p')
    text.textContent = 'The app failed to start. Close it and open it again; if it keeps failing, reinstall it.'
    sentinel.replaceChildren(text)
    try {
      // The owner gets the reason; the screen states the fact, nothing else.
      console.error('[boot] Luxury Switchboard never reached its first frame', detail)
    } catch { /* a broken console must not break the notice */ }
  }

  window.__dshBootReady = function () {
    if (state === 'pending') state = 'ready'
    clearTimer()
    // The commit may have kept the pending message under it; either way the
    // app now owns the window and the message must not cover it.
    var sentinel = document.getElementById('boot-sentinel')
    if (sentinel !== null) sentinel.remove()
  }

  window.addEventListener('error', function (event) {
    if (state === 'pending') fail(event)
  })
  window.addEventListener('unhandledrejection', function (event) {
    if (state === 'pending') fail(event.reason)
  })

  timer = window.setTimeout(function () {
    fail(new Error('no first frame within ' + BOOT_TIMEOUT_MS + 'ms'))
  }, BOOT_TIMEOUT_MS)
})()
