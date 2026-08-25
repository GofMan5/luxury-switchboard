import { describe, expect, it } from 'vitest'
import { parsePastedKeys, proxyURLIsValid } from './key-form'

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

describe('parsePastedKeys', () => {
  it('takes a label and a secret from every pasted line, whatever separates them', () => {
    expect(parsePastedKeys('github_1 sk-one\r\n\ngithub_2,sk-two\n  My spaced key\tsk-three  ')).toEqual([
      { label: 'github_1', secret: 'sk-one' },
      { label: 'github_2', secret: 'sk-two' },
      { label: 'My spaced key', secret: 'sk-three' },
    ])
  })

  it('keeps a line a key would otherwise refuse', () => {
    expect(parsePastedKeys('\nsk-alone')).toEqual([{ label: 'Key 2', secret: 'sk-alone' }])
    expect(parsePastedKeys('github_3: sk-colon')).toEqual([{ label: 'github_3', secret: 'sk-colon' }])
    expect(parsePastedKeys(`${'name'.repeat(30)} sk-long`)[0].label).toHaveLength(80)
    expect(parsePastedKeys('   \n\t\n')).toEqual([])
  })
})
