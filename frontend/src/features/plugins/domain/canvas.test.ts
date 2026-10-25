import { describe, expect, it } from 'vitest'
import { CANVAS_LIMITS, parseColor, validateDrawOps } from './canvas'

function problem(raw: unknown): string {
  const checked = validateDrawOps(raw)
  return checked.ok ? '' : checked.problem
}

describe('parseColor', () => {
  it('takes hex, rgb()/rgba() and theme tokens', () => {
    for (const css of ['#abc', '#abcd', '#a1b2c3', '#A1B2C3FF', 'rgb(1, 2, 3)', 'rgba(255,255,255,0.5)', 'rgb(10% 20% 30% / 50%)', 'rgb(.5 1 2)']) {
      expect(parseColor(css)).toEqual({ css })
    }
    expect(parseColor('token:accent')).toEqual({ token: 'accent' })
  })

  it('refuses everything that could reach outside the canvas or is not a colour', () => {
    for (const bad of ['red', 'url(https://x.test/a.png)', '#abcde', 'rgb(256, 0, 0)', 'rgba(0,0,0,2)', 'rgb(101% 0 0)', 'token:nope', 'var(--accent)', 1, null, `#${'a'.repeat(CANVAS_LIMITS.color)}`]) {
      expect(parseColor(bad)).toBeNull()
    }
  })
})

describe('validateDrawOps', () => {
  it('returns a clean copy of a valid draw', () => {
    const ops = [
      { op: 'clear', color: 'token:background' },
      { op: 'rect', x: 1, y: 2, w: 3, h: 4, fill: '#fff', radius: 2 },
      { op: 'line', x1: 0, y1: 0, x2: 5, y2: 5, stroke: 'token:border', lineWidth: 2 },
      { op: 'circle', x: 5, y: 5, r: 3, stroke: '#000' },
      { op: 'path', points: [0, 0, 1, 1, 2, 0], closed: true, fill: 'rgb(1,2,3)' },
      { op: 'text', x: 0, y: 0, text: 'Score', color: 'token:text', size: 14, align: 'center', baseline: 'top', weight: 'bold', font: 'mono' },
      { op: 'save' }, { op: 'translate', x: 1, y: 1 }, { op: 'scale', x: 2, y: 2 }, { op: 'rotate', angle: 0.5 }, { op: 'alpha', value: 0.5 }, { op: 'restore' },
    ]
    expect(validateDrawOps(ops)).toEqual({ ok: true, ops })
  })

  it('refuses the whole draw on an unknown op or field, naming where', () => {
    expect(problem([{ op: 'clear' }, { op: 'rect', x: 0, y: 0, w: 1, h: 1, fil: '#fff' }])).toBe('ops[1].fil is not a field of a "rect" op.')
    expect(problem([{ op: 'image', src: 'x' }])).toMatch(/^ops\[0\]\.op "image" is not a draw op/u)
    expect(problem([{}])).toBe('ops[0].op is missing.')
    expect(problem([{ op: 'clear', 'bad key': 1 }])).toBe('ops[0]["bad key"] is not a field of a "clear" op.')
    expect(problem('clear')).toBe('ops must be a list of draw ops.')
  })

  it('checks numbers, colours, ranges and paint', () => {
    expect(problem([{ op: 'rect', x: Number.NaN, y: 0, w: 1, h: 1, fill: '#fff' }])).toBe('ops[0].x must be a finite number.')
    expect(problem([{ op: 'rect', x: 0, y: 0, w: 1, h: 1 }])).toBe('ops[0] needs fill or stroke; a "rect" with neither draws nothing.')
    expect(problem([{ op: 'line', x1: 0, y1: 0, x2: 1, y2: 1 }])).toBe('ops[0].stroke is missing.')
    expect(problem([{ op: 'circle', x: 0, y: 0, r: -1, fill: '#fff' }])).toBe('ops[0].r must be 0 or more.')
    expect(problem([{ op: 'alpha', value: 2 }])).toBe('ops[0].value must be from 0 to 1.')
    expect(problem([{ op: 'text', x: 0, y: 0, text: 'a', color: '#fff', size: 200 }])).toBe('ops[0].size must be from 6 to 96.')
    expect(problem([{ op: 'text', x: 0, y: 0, text: 'a', color: '#fff', size: 12, align: 'justify' }])).toBe('ops[0].align must be one of left, center, right.')
    expect(problem([{ op: 'clear', color: 'url(x)' }])).toMatch(/^ops\[0\]\.color is not a colour/u)
    expect(problem([{ op: 'rect', x: 0, y: 0, w: 1, h: 1, fill: '#fff', lineWidth: 0 }])).toBe('ops[0].lineWidth must be above 0 and at most 2048.')
  })

  it('bounds the draw: op count, path numbers, text length and JSON bytes', () => {
    expect(problem(Array.from({ length: CANVAS_LIMITS.ops + 1 }, () => ({ op: 'save' })))).toBe(`ops has ${CANVAS_LIMITS.ops + 1} entries; a draw takes at most ${CANVAS_LIMITS.ops}.`)
    expect(problem([{ op: 'path', points: Array(CANVAS_LIMITS.pathNumbers + 2).fill(0), stroke: '#000' }])).toMatch(/points has 802 numbers/u)
    expect(problem([{ op: 'path', points: [0, 0, 1], stroke: '#000' }])).toMatch(/even count of at least 4/u)
    expect(problem([{ op: 'text', x: 0, y: 0, text: 'é'.repeat(CANVAS_LIMITS.text + 1), color: '#fff', size: 12 }])).toBe('ops[0].text is longer than 200 characters.')
    // Emoji are one character each, so 200 of them fit even though they are 400 UTF-16 units.
    expect(problem([{ op: 'text', x: 0, y: 0, text: '😀'.repeat(CANVAS_LIMITS.text), color: '#fff', size: 12 }])).toBe('')
    const big = Array.from({ length: 4000 }, () => ({ op: 'text', x: 0, y: 0, text: 'ж'.repeat(60), color: '#fff', size: 12 }))
    expect(problem(big)).toBe('The draw is over 512 KiB of JSON.')
  })

  it('refuses data JSON cannot hold', () => {
    const cycle: Record<string, unknown> = { op: 'save' }
    cycle.self = cycle
    expect(problem([cycle])).toMatch(/not plain data/u)
  })
})
