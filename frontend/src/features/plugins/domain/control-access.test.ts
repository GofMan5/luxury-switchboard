import { describe, expect, it } from 'vitest'
import pluginsGuide from '../../../../../docs/PLUGINS.md?raw'
import type { Provider } from '../../providers/domain/provider'
import { CONTROL_METHODS, CONTROL_TOPICS, controlAccess, methodsFor, redactUpstream, topicAccess, UPSTREAM_FIELDS } from './control-access'
import { PLUGIN_PERMISSIONS } from './permissions'

const ALL = new Set<string>(PLUGIN_PERMISSIONS)

describe('controlAccess', () => {
  it('needs the matching grant for each tier', () => {
    expect(controlAccess('providers.list', new Set(['control:read']))).toEqual({ allowed: true, permission: 'control:read' })
    expect(controlAccess('providers.activate', new Set(['control:read']))).toMatchObject({ allowed: false, code: 'permission_denied', permission: 'control:write' })
    expect(controlAccess('providers.activate', new Set(['control:write']))).toEqual({ allowed: true, permission: 'control:write' })
    expect(controlAccess('keys.add', new Set(['control:read', 'control:write']))).toMatchObject({ allowed: false, code: 'permission_denied', permission: 'control:sensitive' })
    expect(controlAccess('tunnel.reveal', new Set(['control:sensitive']))).toEqual({ allowed: true, permission: 'control:sensitive' })
  })

  it.each(['plugins.update', 'plugins.install', 'plugins.storage.get', 'system.handshake', 'system.shutdown', 'backup.export', 'backup.import', 'appearance.update', 'updates.install'])(
    'never reaches %s, whatever is granted',
    (method) => {
      expect(controlAccess(method, ALL)).toMatchObject({ allowed: false, code: 'not_allowed', message: expect.stringContaining('never available') })
    },
  )

  it('refuses commands the table does not name, including prototype keys', () => {
    for (const method of ['providers.export', 'toString', 'constructor', '__proto__', 'hasOwnProperty', '']) {
      expect(controlAccess(method, ALL)).toMatchObject({ allowed: false, code: 'not_allowed' })
    }
  })

  it('keeps every allowed command on exactly one control tier and away from the denied families', () => {
    for (const [method, permission] of Object.entries(CONTROL_METHODS)) {
      expect(['control:read', 'control:write', 'control:sensitive', 'relay:rules']).toContain(permission)
      expect(method).not.toMatch(/^(plugins|system|backup)\./u)
      expect(['appearance.update', 'updates.install']).not.toContain(method)
    }
    const listed = [...methodsFor('control:read'), ...methodsFor('control:write'), ...methodsFor('control:sensitive'), ...methodsFor('relay:rules')]
    expect(new Set(listed).size).toBe(listed.length)
    expect(listed.length).toBe(Object.keys(CONTROL_METHODS).length)
    expect(Object.isFrozen(CONTROL_METHODS)).toBe(true)
  })

  it('documents every command and topic in docs/PLUGINS.md', () => {
    for (const method of Object.keys(CONTROL_METHODS)) expect(pluginsGuide, method).toContain(`\`${method}\``)
    for (const topic of CONTROL_TOPICS) expect(pluginsGuide, topic).toContain(`\`${topic}\``)
  })
})

describe('topicAccess', () => {
  it('follows listed topics with control:read only', () => {
    expect(topicAccess('activity.changed', new Set(['control:read']))).toEqual({ allowed: true, permission: 'control:read' })
    expect(topicAccess('activity.changed', new Set(['control:write', 'control:sensitive']))).toMatchObject({ allowed: false, code: 'permission_denied' })
  })

  it('refuses unlisted topics, including the plugin store and the handshake', () => {
    for (const topic of ['plugins.changed', 'system.ready', 'backup.progress', 'nonsense']) {
      expect(topicAccess(topic, ALL)).toMatchObject({ allowed: false, code: 'not_allowed' })
    }
  })
})

describe('redactUpstream', () => {
  // Every provider field is classified here: a new one fails typecheck until
  // someone decides whether it says where the provider points.
  const upstream: Record<keyof Provider, boolean> = {
    id: false, name: false, baseUrl: true, authMode: false, authHeader: true, dialect: false, modelsPath: true,
    format: false, chatPath: true, imageCompat: false, rpm: false, rateUnit: false, cacheTtl: false,
    enabled: false, keyConfigured: false, keyCount: false, builtin: false, preset: false,
  }

  it('hides exactly the provider fields that say where traffic goes', () => {
    expect([...UPSTREAM_FIELDS].sort()).toEqual(Object.entries(upstream).filter(([, hidden]) => hidden).map(([key]) => key).sort())
  })

  it('removes them at any depth and keeps everything else', () => {
    const answer = { providers: [{ id: 'p1', baseUrl: 'https://upstream.example.test', nested: { chatPath: '/c', rpm: 5 } }], activeId: 'p1' }
    expect(redactUpstream(answer)).toEqual({ providers: [{ id: 'p1', nested: { rpm: 5 } }], activeId: 'p1' })
  })
})
