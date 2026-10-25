import { describe, expect, it } from 'vitest'
import { isSafeHref, parseMarkdown, plainText } from './markdown'

describe('parseMarkdown', () => {
  it('parses the supported blocks', () => {
    const blocks = parseMarkdown('# Title\n\nSome **bold** and *em* with `code`.\n\n- one\n- two\n\n3. three\n4. four\n\n> quoted\n\n---\n\n```\nx < y\n```')
    expect(blocks.map((block) => block.kind)).toEqual(['heading', 'paragraph', 'list', 'list', 'quote', 'rule', 'code'])
    expect(blocks[0]).toEqual({ kind: 'heading', level: 1, children: [{ kind: 'text', text: 'Title' }] })
    expect(blocks[1]).toEqual({ kind: 'paragraph', children: [
      { kind: 'text', text: 'Some ' }, { kind: 'strong', children: [{ kind: 'text', text: 'bold' }] },
      { kind: 'text', text: ' and ' }, { kind: 'em', children: [{ kind: 'text', text: 'em' }] },
      { kind: 'text', text: ' with ' }, { kind: 'code', text: 'code' }, { kind: 'text', text: '.' },
    ] })
    expect(blocks[2]).toMatchObject({ kind: 'list', ordered: false, items: [[{ kind: 'paragraph' }], [{ kind: 'paragraph' }]] })
    expect(blocks[3]).toMatchObject({ kind: 'list', ordered: true, start: 3 })
    expect(blocks[6]).toEqual({ kind: 'code', text: 'x < y' })
  })

  it('never turns HTML into anything but text', () => {
    const [block] = parseMarkdown('<img src=x onerror=alert(1)> <b>hi</b>')
    expect(block?.kind).toBe('paragraph')
    expect(block && block.kind === 'paragraph' ? plainText(block.children) : '').toBe('<img src=x onerror=alert(1)> <b>hi</b>')
  })

  it('keeps safe links and turns unsafe ones into their label', () => {
    const [safe] = parseMarkdown('[docs](https://example.test/a)')
    expect(safe).toEqual({ kind: 'paragraph', children: [{ kind: 'link', href: 'https://example.test/a', children: [{ kind: 'text', text: 'docs' }] }] })
    for (const target of ['javascript:alert(1)', 'http://example.test', 'https://user:pass@example.test', 'data:text/html,x', 'file:///c:/x']) {
      const [block] = parseMarkdown(`[label](${target})`)
      expect(block && block.kind === 'paragraph' ? block.children.some((node) => node.kind === 'link') : true).toBe(false)
      expect(isSafeHref(target)).toBe(false)
    }
    expect(isSafeHref('https://example.test/a?b=c')).toBe(true)
  })

  it('stays fast on hostile input', () => {
    const started = performance.now()
    parseMarkdown('*['.repeat(20_000))
    parseMarkdown('> '.repeat(5_000) + 'deep')
    parseMarkdown('`'.repeat(20_000))
    expect(performance.now() - started).toBeLessThan(1_000)
  })
})
