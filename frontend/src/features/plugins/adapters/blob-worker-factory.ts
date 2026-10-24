import type { BootContext, PluginWorker, WorkerFactory } from '../application/ports'
import type { HostMessage } from '../domain/protocol'
import { workerSource } from './worker-source'

/**
 * Runs each plugin in its own dedicated Web Worker, built from a Blob URL:
 * a separate global with no DOM, no window, no access to this document, its
 * cookies or the Tauri bridge, and stopped with one `terminate()`.
 */
export class BlobWorkerFactory implements WorkerFactory {
  create(script: string, context: BootContext): PluginWorker {
    const source = workerSource(script, context)
    const url = URL.createObjectURL(new Blob([source.text], { type: 'text/javascript' }))
    let revoked = false
    const revoke = () => {
      if (revoked) return
      revoked = true
      URL.revokeObjectURL(url)
    }
    let worker: Worker
    try {
      worker = new Worker(url, { name: `plugin:${context.plugin.id}` })
    } catch (error) {
      revoke()
      throw error
    }
    let onMessage: (data: unknown) => void = () => undefined
    let onError: (message: string) => void = () => undefined
    worker.addEventListener('message', (event) => {
      revoke()
      onMessage(event.data)
    })
    worker.addEventListener('messageerror', () => onError('The script sent a message that could not be received.'))
    worker.addEventListener('error', (event) => {
      event.preventDefault()
      revoke()
      const line = event.lineno > source.offset ? ` (line ${event.lineno - source.offset} of the script)` : ''
      onError(`${event.message || 'The script failed to load'}${line}`)
    })
    return {
      post: (message: HostMessage) => worker.postMessage(message),
      onMessage: (listener) => { onMessage = listener },
      onError: (listener) => { onError = listener },
      terminate: () => {
        worker.terminate()
        revoke()
      },
    }
  }
}
