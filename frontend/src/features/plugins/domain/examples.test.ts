import { describe, expect, it } from 'vitest'
import { EXAMPLE_FOLDERS, EXAMPLE_PLUGINS } from './examples'
import { checkPackage, packageSizeProblem } from './manifest'

// The reference copies plugin authors read. They must stay byte-identical to
// what the Developer area installs, so the docs never drift from the product.
const files = import.meta.glob('../../../../../docs/plugins/examples/*.json', { query: '?raw', import: 'default', eager: true }) as Record<string, string>
const byName = new Map(Object.entries(files).map(([path, text]) => [path.slice(path.lastIndexOf('/') + 1), text]))
const manifests = Object.keys(import.meta.glob('../../../../../docs/plugins/examples/*/plugin.json'))
const folders = manifests.map((path) => path.split('/').at(-2))
const written = EXAMPLE_PLUGINS.filter((example) => example.file.endsWith('.json'))

describe('example plugins', () => {
  it('has one docs file per written example and no stray files', () => {
    expect([...byName.keys()].sort()).toEqual(written.map((example) => example.file).sort())
  })

  it('offers every docs source folder, each once', () => {
    expect(EXAMPLE_FOLDERS.map(([folder]) => folder).sort()).toEqual(folders.sort())
    expect(EXAMPLE_PLUGINS.filter((example) => example.file.endsWith('/')).map((example) => example.file)).toEqual(EXAMPLE_FOLDERS.map(([folder]) => `${folder}/`))
  })

  it('assembles a folder like pnpm plugin pack: the files verbatim, LF line endings', () => {
    const report = EXAMPLE_PLUGINS.find((example) => example.file === 'usage-report/')?.package
    expect(report?.script).toMatch(/^\/\/ Usage report/u)
    expect(report?.script).not.toContain('\r')
    expect(report?.script?.endsWith('\n')).toBe(true)
    expect(report).not.toHaveProperty('css')
  })

  it.each(written.map((example) => [example.file, example] as const))('%s matches docs/plugins/examples', (file, example) => {
    expect(byName.get(file)?.replace(/\r\n/gu, '\n')).toBe(`${JSON.stringify(example.package, null, 2)}\n`)
  })

  it.each(EXAMPLE_PLUGINS.map((example) => [example.package.id, example] as const))('%s is a valid package that fits every budget', (_id, example) => {
    expect(checkPackage(example.package)).toEqual({ ok: true, package: example.package })
    expect(packageSizeProblem(example.package, example.package.permissions)).toBe('')
  })

  it('uses unique ids and file names', () => {
    expect(new Set(EXAMPLE_PLUGINS.map((example) => example.package.id)).size).toBe(EXAMPLE_PLUGINS.length)
    expect(new Set(EXAMPLE_PLUGINS.map((example) => example.file)).size).toBe(EXAMPLE_PLUGINS.length)
  })
})
