import { describe, expect, it } from 'vitest'
import { EXAMPLE_PLUGINS } from './examples'
import { CSS_BYTES, PACKAGE_SCHEMA, SCRIPT_BYTES, byteLength, checkPackage, compareVersions, packageBytes, packageSizeProblem, parsePackage } from './manifest'
import { PERMISSION_SPECS, defaultGrants, effectiveGrants, initialGrants, upgradeAsksMore, upgradeChanges } from './permissions'

const base = { schema: PACKAGE_SCHEMA, id: 'acme.tools', name: 'Acme tools', version: '1.0.0', permissions: [] as string[] }

function problems(value: unknown): readonly string[] {
  const checked = checkPackage(value)
  return checked.ok ? [] : checked.problems
}

describe('checkPackage', () => {
  it('accepts a minimal package and copies only known fields', () => {
    const checked = checkPackage({ ...base, author: 'Acme', description: 'Line one\nline two' })
    expect(checked).toEqual({ ok: true, package: { ...base, author: 'Acme', description: 'Line one\nline two' } })
  })

  it('accepts every built-in example', () => {
    for (const example of EXAMPLE_PLUGINS) expect(checkPackage(example.package)).toEqual({ ok: true, package: example.package })
  })

  it('lists every problem at once, including unknown fields', () => {
    const found = problems({ schema: 2, id: 'A', name: '', version: 'one two', permissions: ['ui:pages', 'ui:pages', 'root'], permisions: [] })
    expect(found).toEqual(expect.arrayContaining([
      'Unknown field "permisions".',
      '"schema" must be 1.',
      expect.stringContaining('"id" must be 3–64 characters'),
      '"name" is required.',
      expect.stringContaining('"version" must be'),
      'Permission "ui:pages" is listed twice.',
      'Unknown permission "root".',
    ]))
  })

  it.each([
    ['ab', false], ['abc', true], ['acme.tools-2', true], ['-acme', false], ['acme.', false], ['Acme', false], ['a'.repeat(64), true], ['a'.repeat(65), false],
  ])('id %s → %s', (id, ok) => {
    expect(checkPackage({ ...base, id }).ok).toBe(ok)
  })

  it('refuses control characters in names and lets descriptions keep line breaks', () => {
    expect(problems({ ...base, name: 'Acme\u0007' })).toContain('"name" contains control characters.')
    expect(problems({ ...base, description: 'tab\there' })).toContain('"description" contains control characters other than line breaks.')
    expect(problems({ ...base, author: 'x'.repeat(65) })).toContain('"author" is longer than 64 characters.')
  })

  it('needs a script for every permission that runs code', () => {
    expect(problems({ ...base, permissions: ['ui:pages', 'storage'] })).toContain('"ui:pages", "storage" need a "script".')
    expect(problems({ ...base, permissions: ['control:read'], script: '   ' })).toContain('"control:read" needs a "script".')
    expect(checkPackage({ ...base, permissions: ['ui:pages'], script: 'await 1' }).ok).toBe(true)
  })

  it('ties a theme and a stylesheet to their permissions', () => {
    const theme = { name: 'Ink', scheme: 'dark', tokens: { '--accent': '#88aaff' } }
    expect(problems({ ...base, theme })).toContain('A theme needs the "ui:theme" permission.')
    expect(problems({ ...base, css: 'a{}' })).toContain('A stylesheet needs the "ui:css" permission.')
    expect(problems({ ...base, css: '' })).toContain('A stylesheet needs the "ui:css" permission.') // the backend refuses it too
    expect(checkPackage({ ...base, permissions: ['ui:theme', 'ui:css'], theme, css: 'a{}' }).ok).toBe(true)
  })

  it('validates theme tokens with the shared token rules', () => {
    const permissions = ['ui:theme']
    expect(problems({ ...base, permissions, theme: { name: 'Ink', scheme: 'dim', tokens: {} } })).toContain('"theme.scheme" must be "dark" or "light".')
    expect(problems({ ...base, permissions, theme: { name: 'Ink', scheme: 'dark', tokens: { '--bg': 'url(https://example.test/x.png)' } } })).toEqual([expect.stringContaining('--bg contains characters or functions')])
    expect(problems({ ...base, permissions, theme: { name: 'Ink', scheme: 'dark', tokens: { color: 'red' } } })).toEqual([expect.stringContaining('not a valid custom property name')])
    expect(problems({ ...base, permissions, theme: { name: 'Ink', scheme: 'dark', tokens: { '--a': 'red; }' } } })).toHaveLength(1)
    expect(problems({ ...base, permissions, theme: { name: 'Ink', scheme: 'dark', tokens: {}, extra: 1 } })).toContain('Unknown field "theme.extra".')
  })

  it('refuses stylesheets that import or close the style element', () => {
    const permissions = ['ui:css']
    expect(problems({ ...base, permissions, css: '@import "https://example.test/a.css";' })).toContain('"css" may not contain @import or </style>.')
    expect(problems({ ...base, permissions, css: 'a{}</STYLE><script>' })).toContain('"css" may not contain @import or </style>.')
    expect(problems({ ...base, permissions, css: 'a'.repeat(CSS_BYTES + 1) })).toContain('"css" is larger than 64 KiB.')
  })

  it('caps the script in bytes, not characters', () => {
    const script = 'é'.repeat(SCRIPT_BYTES / 2 + 1)
    expect(byteLength(script)).toBeGreaterThan(SCRIPT_BYTES)
    expect(problems({ ...base, permissions: ['ui:pages'], script })).toContain('"script" is larger than 128 KiB.')
  })
})

describe('parsePackage', () => {
  it('reports JSON that does not parse and non-object roots', () => {
    expect(parsePackage('{')).toEqual({ ok: false, problems: ['The file is not valid JSON.'] })
    expect(parsePackage('[]')).toEqual({ ok: false, problems: ['A plugin package is a JSON object.'] })
    expect(parsePackage(JSON.stringify(base)).ok).toBe(true)
  })
})

describe('package size', () => {
  it('measures the stored compact JSON', () => {
    expect(packageBytes(base as never)).toBe(byteLength(JSON.stringify(base)))
  })

  // Backslashes are one byte in the file and two in JSON, so a package within
  // its field caps can still overflow a frame.
  const heavy = (escaped: number) => ({
    ...base, permissions: ['ui:pages', 'ui:css'], css: 'y'.repeat(CSS_BYTES),
    script: '\\'.repeat(escaped) + 'x'.repeat(SCRIPT_BYTES - escaped),
  }) as never

  it('passes an ordinary package', () => {
    expect(packageSizeProblem(EXAMPLE_PLUGINS[1].package, ['ui:pages'])).toBe('')
    expect(packageSizeProblem(heavy(0), ['ui:pages', 'ui:css'])).toBe('')
  })

  it('refuses a package the shell cannot send in one command', () => {
    expect(checkPackage(heavy(SCRIPT_BYTES)).ok).toBe(true)
    expect(packageSizeProblem(heavy(SCRIPT_BYTES), [])).toMatch(/on the wire; one command carries at most 256 KiB/u)
  })

  it('refuses a package the control plane could store but not answer with', () => {
    expect(packageSizeProblem(heavy(62_000), [])).toMatch(/too large to load back/u)
  })
})

describe('grants', () => {
  it('ticks every permission by default except the ones that reach traffic, the network or secrets', () => {
    const all = PERMISSION_SPECS.map((spec) => spec.id)
    const optIn = ['control:sensitive', 'net:fetch', 'relay:content']
    expect(defaultGrants(all)).toEqual(all.filter((permission) => !optIn.includes(permission)))
  })

  it('keeps only declared, known, unique grants in declared order', () => {
    expect(effectiveGrants(['storage', 'ui:pages', 'bogus'], ['ui:pages', 'storage', 'storage', 'control:write', 'bogus'])).toEqual(['storage', 'ui:pages'])
  })

  it('starts an upgrade from the previous decisions and defaults only new permissions', () => {
    const previous = { permissions: ['ui:pages', 'control:write'], granted: ['ui:pages'], hosts: [] }
    expect(initialGrants(['ui:pages', 'control:write', 'storage', 'control:sensitive'], previous)).toEqual(['ui:pages', 'storage'])
    expect(initialGrants(['ui:pages', 'control:sensitive'])).toEqual(['ui:pages'])
  })

  it('asks again for net:fetch when an upgrade adds a host, not when the hosts stay', () => {
    const previous = { permissions: ['net:fetch', 'storage'], granted: ['net:fetch', 'storage'], hosts: ['api.example.com'] }
    expect(initialGrants(['net:fetch', 'storage'], previous, ['api.example.com'])).toEqual(['net:fetch', 'storage'])
    expect(initialGrants(['net:fetch', 'storage'], previous, ['api.example.com', 'collect.example.net'])).toEqual(['storage'])
  })
})

describe('compareVersions', () => {
  it('orders numeric parts, padding missing ones', () => {
    expect(compareVersions('1.2.0', '1.10.0')).toBe(-1)
    expect(compareVersions('2.0', '1.9.9')).toBe(1)
    expect(compareVersions('1.0', '1.0.0')).toBe(0)
  })

  it('puts a prerelease before its release and orders prereleases', () => {
    expect(compareVersions('1.0.0-beta', '1.0.0')).toBe(-1)
    expect(compareVersions('1.0.0', '1.0.0-rc.1')).toBe(1)
    expect(compareVersions('1.0.0-alpha', '1.0.0-alpha.1')).toBe(-1)
    expect(compareVersions('1.0.0-beta.2', '1.0.0-beta.11')).toBe(-1)
    expect(compareVersions('1.0.0-1', '1.0.0-alpha')).toBe(-1)
    expect(compareVersions('1.0.0-rc', '1.0.0-beta')).toBe(1)
  })

  it('ignores build metadata', () => {
    expect(compareVersions('1.0.0+abc', '1.0.0+def')).toBe(0)
    expect(compareVersions('1.0.1+1', '1.0.0+9')).toBe(1)
  })

  it('claims no order for unnumbered versions', () => {
    expect(compareVersions('nightly', '1.0.0')).toBeNull()
    expect(compareVersions('1.0.0', 'v1.0.0')).toBeNull()
    expect(compareVersions('1..0', '1.0.0')).toBeNull()
    expect(compareVersions('1.0.0-', '1.0.0')).toBeNull()
  })
})

describe('upgradeChanges', () => {
  it('lists dropped permissions and host changes', () => {
    expect(upgradeChanges(
      { permissions: ['ui:pages', 'net:fetch', 'unknown:thing'], hosts: ['a.example', 'b.example'] },
      { permissions: ['ui:pages', 'net:fetch'], hosts: ['b.example', 'c.example'] },
    )).toEqual({ droppedPermissions: [], addedHosts: ['c.example'], droppedHosts: ['a.example'] })
    expect(upgradeChanges({ permissions: ['ui:pages', 'net:fetch'], hosts: ['a.example'] }, { permissions: ['ui:pages'], hosts: [] }))
      .toEqual({ droppedPermissions: ['net:fetch'], addedHosts: [], droppedHosts: ['a.example'] })
  })
})

describe('upgradeAsksMore', () => {
  const previous = { permissions: ['ui:pages', 'net:fetch'], hosts: ['a.example'] }
  it('is quiet for the same or fewer permissions and hosts', () => {
    expect(upgradeAsksMore(previous, previous)).toBe(false)
    expect(upgradeAsksMore(previous, { permissions: ['ui:pages'], hosts: [] })).toBe(false)
    expect(upgradeAsksMore(previous, { permissions: ['ui:pages', 'net:fetch', 'unknown:thing'], hosts: ['a.example'] })).toBe(false)
  })
  it('asks for a new permission or a new host', () => {
    expect(upgradeAsksMore(previous, { permissions: ['ui:pages', 'net:fetch', 'ui:css'], hosts: ['a.example'] })).toBe(true)
    expect(upgradeAsksMore(previous, { permissions: ['ui:pages', 'net:fetch'], hosts: ['a.example', 'b.example'] })).toBe(true)
  })
})
