// Loads the plugin domain straight from frontend/src, so every rule the CLI
// applies is the one the app applies: Node strips the TypeScript types, and
// two in-thread hooks bridge what Vite resolves for the frontend.
import { readFileSync } from 'node:fs'
import { registerHooks } from 'node:module'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

if (typeof registerHooks !== 'function' || !process.features.typescript) {
  process.stderr.write(`pnpm plugin needs Node 22.18 or newer (type stripping and module.registerHooks); this is ${process.version}.\n`)
  process.exit(2)
}

// Type stripping and the hooks API announce themselves as experimental; the
// CLI's output is meant to be read (or parsed), so those two notes are dropped.
const printWarnings = process.listeners('warning')
process.removeAllListeners('warning')
process.on('warning', (warning) => {
  if (warning.name === 'ExperimentalWarning') return
  for (const listener of printWarnings) listener(warning)
})

export const FRONTEND_SRC = new URL('../../frontend/src/', import.meta.url)
const KNOWN_EXTENSION = /\.(?:[cm]?[jt]sx?|json|css)$/u

registerHooks({
  // The frontend imports `./manifest`, not `./manifest.ts`.
  resolve(specifier, context, nextResolve) {
    if (/^\.\.?\//u.test(specifier) && !KNOWN_EXTENSION.test(specifier) && context.parentURL?.startsWith(FRONTEND_SRC.href)) {
      for (const suffix of ['.ts', '/index.ts']) {
        try { return nextResolve(specifier + suffix, context) } catch { /* try the next form */ }
      }
    }
    return nextResolve(specifier, context)
  },
  // Vite gives JSON modules named exports (`import { version } from
  // '../../package.json'`); Node gives only a default one without an
  // import attribute, so such an import becomes a small module.
  load(url, context, nextLoad) {
    if (url.startsWith('file:') && url.endsWith('.json') && context.importAttributes?.type !== 'json') {
      const data = JSON.parse(readFileSync(fileURLToPath(url), 'utf8'))
      const names = data && typeof data === 'object' && !Array.isArray(data) ? Object.keys(data) : []
      const source = [`const data = ${JSON.stringify(data)};`, 'export default data;',
        ...names.map((name, index) => `const v${index} = data[${JSON.stringify(name)}]; export { v${index} as ${JSON.stringify(name)} };`)].join('\n')
      return { format: 'module', source, shortCircuit: true }
    }
    return nextLoad(url, context)
  },
})

const load = (path) => import(new URL(path, FRONTEND_SRC).href)

export const [manifest, permissions, settings, tokens, protocol, workerSource, host, extensions, plugin] = await Promise.all([
  load('features/plugins/domain/manifest.ts'),
  load('features/plugins/domain/permissions.ts'),
  load('features/plugins/domain/settings.ts'),
  load('shared/contracts/tokens.ts'),
  load('features/plugins/domain/protocol.ts'),
  load('features/plugins/adapters/worker-source.ts'),
  load('features/plugins/application/plugin-host.ts'),
  load('shared/contracts/extensions.ts'),
  load('features/plugins/domain/plugin.ts'),
])

/** Modules only `run` needs, loaded on demand. The fixture control plane and
 * the shell's route list are development conveniences: when either cannot be
 * loaded, `run` falls back and says so instead of failing. */
export async function optional(path) {
  try { return { module: await load(path), error: '' } } catch (error) { return { module: null, error: error instanceof Error ? error.message : String(error) } }
}
