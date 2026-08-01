import { describe, expect, it } from 'vitest'
import { proxyURLIsValid } from './key-form'

describe('proxyURLIsValid', () => {
  it('accepts direct, HTTP and SOCKS proxy settings', () => {
    expect(proxyURLIsValid('')).toBe(true)
    expect(proxyURLIsValid('https://user:pass@proxy.example:8443')).toBe(true)
    expect(proxyURLIsValid('socks5h://127.0.0.1:1080')).toBe(true)
  })

  it('rejects unsupported and malformed proxy URLs', () => {
    expect(proxyURLIsValid('ftp://proxy.example')).toBe(false)
    expect(proxyURLIsValid('https://:bad')).toBe(false)
    expect(proxyURLIsValid('https://proxy.example/#secret')).toBe(false)
  })
})
