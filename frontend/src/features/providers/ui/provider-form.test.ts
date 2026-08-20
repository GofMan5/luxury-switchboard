import { describe, expect, it } from 'vitest'
import type { ProviderInput } from '../domain/provider'
import { providerInputError } from './provider-form'

const valid: ProviderInput = { name: 'Custom', baseUrl: 'https://api.example.com/v1?api-version=2026-01-01', authMode: 'bearer', authHeader: '', dialect: 'auto', modelsPath: '/v1/models', format: 'auto', chatPath: '/v1/chat/completions', imageCompat: false, rpm: 120, rateUnit: 'minute', cache1h: false, enabled: true }

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

  it('validates the chat completions path for chat-only providers', () => {
    expect(providerInputError({ ...valid, format: 'chat' })).toBe('')
    expect(providerInputError({ ...valid, format: 'chat', chatPath: 'chat' })).toContain('Chat completions path')
    expect(providerInputError({ ...valid, format: 'chat', chatPath: '/chat?api=1' })).toContain('Chat completions path')
    expect(providerInputError({ ...valid, format: 'chat', chatPath: '/chat' })).toBe('')
    expect(providerInputError({ ...valid, format: 'custom' } as unknown as ProviderInput)).toContain('request format')
  })

  it('accepts both rate units and rejects anything else', () => {
    // The unit decides whether the limit is a minute budget or a burst cap, so an
    // unrecognised value must not reach the sidecar and be silently read as a minute.
    expect(providerInputError({ ...valid, rpm: 5, rateUnit: 'second' })).toBe('')
    expect(providerInputError({ ...valid, rateUnit: 'minute' })).toBe('')
    expect(providerInputError({ ...valid, rateUnit: 'hour' } as unknown as ProviderInput)).toContain('per minute or per second')
    expect(providerInputError({ ...valid, rateUnit: '' } as unknown as ProviderInput)).toContain('per minute or per second')
  })
})
