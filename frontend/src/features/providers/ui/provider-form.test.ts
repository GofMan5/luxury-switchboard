import { describe, expect, it } from 'vitest'
import type { ProviderInput } from '../domain/provider'
import { providerInputError } from './provider-form'

const valid: ProviderInput = { name: 'Custom', baseUrl: 'https://api.example.com/v1?api-version=2026-01-01', authMode: 'bearer', authHeader: '', dialect: 'auto', modelsPath: '/v1/models', imageCompat: false, rpm: 120, cache1h: false, enabled: true }

describe('providerInputError', () => {
  it('accepts compatible HTTPS providers and loopback HTTP', () => {
    expect(providerInputError(valid)).toBe('')
    expect(providerInputError({ ...valid, baseUrl: 'http://127.0.0.1:8799/v1' })).toBe('')
  })

  it('rejects remote HTTP and credentials in URLs', () => {
    expect(providerInputError({ ...valid, baseUrl: 'http://api.example.com/v1' })).toContain('HTTPS')
    expect(providerInputError({ ...valid, baseUrl: 'http://127.example.com/v1' })).toContain('HTTPS')
    expect(providerInputError({ ...valid, baseUrl: 'https://user:secret@api.example.com/v1' })).toContain('credentials')
    expect(providerInputError({ ...valid, baseUrl: 'https://api.example.com/v1?api_key=secret' })).toContain('API Keys')
  })

  it('rejects unsafe custom auth headers and model paths', () => {
    expect(providerInputError({ ...valid, authMode: 'custom', authHeader: 'Host' })).toContain('header')
    expect(providerInputError({ ...valid, authMode: 'custom', authHeader: 'Authorization' })).toBe('')
    expect(providerInputError({ ...valid, modelsPath: 'v1/models' })).toContain('Models path')
  })
})
