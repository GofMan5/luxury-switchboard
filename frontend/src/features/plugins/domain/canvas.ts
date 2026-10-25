/**
 * The canvas contract: what a plugin may paint on a `canvas` node, what the
 * window sends back as input, and the channel between the plugin host and the
 * renderer. A draw is a display list of plain records, checked like a page
 * tree: an unknown op or field refuses the whole draw, so a misspelt `fil`
 * never leaves a picture that is silently half painted.
 */

export const CANVAS_LIMITS = {
  /** Width and height of a canvas node, in canvas units. */
  size: 2048,
  ops: 10_000,
  /** UTF-8 bytes of the draw's JSON. */
  bytes: 512 * 1024,
  /** x, y pairs of a `path`, counted as numbers. */
  pathNumbers: 800,
  /** Characters (code points) of a `text` op. */
  text: 200,
  textSize: { min: 6, max: 96 },
  lineWidth: 2048,
  /** A colour string longer than this is not one of the accepted forms. */
  color: 64,
  /** How often `pointermove` reaches the plugin. */
  movesPerSecond: 60,
} as const

/** The theme colours a draw can name as `token:<name>`; `ui.theme()` reports the same set. */
export const THEME_TOKENS = ['background', 'surface', 'text', 'muted', 'border', 'accent', 'success', 'warning', 'danger', 'info'] as const
export type ThemeToken = (typeof THEME_TOKENS)[number]

/** The window's custom property behind each theme token, so a canvas and `ui.theme()` read the same colours. */
export const THEME_TOKEN_PROPERTIES: Readonly<Record<ThemeToken, `--${string}`>> = {
  background: '--bg-canvas',
  surface: '--bg-surface',
  text: '--text-primary',
  muted: '--text-muted',
  border: '--border-strong',
  accent: '--accent',
  success: '--status-success',
  warning: '--status-warning',
  danger: '--status-danger',
  info: '--status-info',
}

/** A checked colour string: `#rgb`, `#rgba`, `#rrggbb`, `#rrggbbaa`, `rgb()`/`rgba()` or `token:<name>`. */
export type Color = string

export type DrawOp =
  | { readonly op: 'clear'; readonly color?: Color }
  | { readonly op: 'rect'; readonly x: number; readonly y: number; readonly w: number; readonly h: number; readonly fill?: Color; readonly stroke?: Color; readonly lineWidth?: number; readonly radius?: number }
  | { readonly op: 'line'; readonly x1: number; readonly y1: number; readonly x2: number; readonly y2: number; readonly stroke: Color; readonly lineWidth?: number }
  | { readonly op: 'circle'; readonly x: number; readonly y: number; readonly r: number; readonly fill?: Color; readonly stroke?: Color; readonly lineWidth?: number }
  | { readonly op: 'path'; readonly points: readonly number[]; readonly closed?: boolean; readonly fill?: Color; readonly stroke?: Color; readonly lineWidth?: number }
  | {
    readonly op: 'text'; readonly x: number; readonly y: number; readonly text: string; readonly color: Color; readonly size: number
    readonly align?: 'left' | 'center' | 'right'; readonly baseline?: 'top' | 'middle' | 'alphabetic' | 'bottom'
    readonly weight?: 'normal' | 'bold'; readonly font?: 'sans' | 'mono'
  }
  | { readonly op: 'save' }
  | { readonly op: 'restore' }
  | { readonly op: 'translate'; readonly x: number; readonly y: number }
  | { readonly op: 'scale'; readonly x: number; readonly y: number }
  | { readonly op: 'rotate'; readonly angle: number }
  | { readonly op: 'alpha'; readonly value: number }

export type DrawOpName = DrawOp['op']

/** What a canvas sends to the plugin's `ui.onInput`; `x`/`y` are canvas units whatever the on-screen scale. */
export type CanvasInput =
  | {
    readonly surface: string; readonly canvas: string; readonly kind: 'keydown' | 'keyup'
    readonly key: string; readonly code: string; readonly repeat: boolean
    readonly shift: boolean; readonly alt: boolean; readonly ctrl: boolean; readonly meta: boolean
  }
  | {
    readonly surface: string; readonly canvas: string; readonly kind: 'pointerdown' | 'pointerup' | 'pointermove' | 'wheel'
    readonly x: number; readonly y: number; readonly button: number; readonly buttons: number; readonly deltaY?: number
  }
  | { readonly surface: string; readonly canvas: string; readonly kind: 'focus' | 'blur' }

/**
 * How a rendered canvas talks to the plugin that owns it; the plugin host
 * implements it. `surface` is the page or surface id the tree was rendered
 * for and `canvas` the node's `id`.
 */
export interface CanvasChannel {
  /** Delivers each checked draw for this canvas until the returned function is called. */
  subscribe(surface: string, canvas: string, draw: (ops: readonly DrawOp[]) => void): () => void
  input(surface: string, canvas: string, event: CanvasInput): void
  /** Whether the canvas is on screen; frames run only while a `frames` canvas is visible. */
  visible(surface: string, canvas: string, frames: boolean, visible: boolean): void
}

export type DrawCheck = { readonly ok: true; readonly ops: readonly DrawOp[] } | { readonly ok: false; readonly problem: string }

class DrawError extends Error {}

type OpShapes = { readonly [K in DrawOpName]: Omit<Extract<DrawOp, { op: K }>, 'op'> }

/** The fields each op takes besides `op`. Anything else refuses the draw. */
const OP_FIELD_LISTS = {
  clear: ['color'],
  rect: ['x', 'y', 'w', 'h', 'fill', 'stroke', 'lineWidth', 'radius'],
  line: ['x1', 'y1', 'x2', 'y2', 'stroke', 'lineWidth'],
  circle: ['x', 'y', 'r', 'fill', 'stroke', 'lineWidth'],
  path: ['points', 'closed', 'fill', 'stroke', 'lineWidth'],
  text: ['x', 'y', 'text', 'color', 'size', 'align', 'baseline', 'weight', 'font'],
  save: [],
  restore: [],
  translate: ['x', 'y'],
  scale: ['x', 'y'],
  rotate: ['angle'],
  alpha: ['value'],
} as const satisfies { readonly [K in DrawOpName]: readonly (keyof OpShapes[K])[] }

type Unlisted = { [K in DrawOpName]: Exclude<keyof OpShapes[K], (typeof OP_FIELD_LISTS)[K][number]> }[DrawOpName]
/** Checked both ways, as in the page tree: a field added to an op without listing it here does not compile. */
const OP_FIELDS: typeof OP_FIELD_LISTS & ([Unlisted] extends [never] ? unknown : { readonly unlisted: Unlisted }) = OP_FIELD_LISTS

export const DRAW_OPS = Object.keys(OP_FIELDS) as readonly DrawOpName[]

const HEX = /^#(?:[0-9a-f]{3,4}|[0-9a-f]{6}|[0-9a-f]{8})$/iu
const NUMBER = String.raw`(\d{1,3}(?:\.\d+)?|\.\d+)(%?)`
const LEGACY_RGB = new RegExp(String.raw`^rgba?\(\s*${NUMBER}\s*,\s*${NUMBER}\s*,\s*${NUMBER}\s*(?:,\s*${NUMBER}\s*)?\)$`, 'iu')
const MODERN_RGB = new RegExp(String.raw`^rgba?\(\s*${NUMBER}\s+${NUMBER}\s+${NUMBER}\s*(?:\/\s*${NUMBER}\s*)?\)$`, 'iu')
const COLOR_FORMS = '#rgb, #rgba, #rrggbb, #rrggbbaa, rgb(…), rgba(…) or token:<name>'

/** A colour a draw names: a CSS colour the canvas takes as is, or a theme token the window resolves. */
export type ParsedColor = { readonly css: string } | { readonly token: ThemeToken }

/** Reads a draw colour, or null when it is none of the accepted forms. */
export function parseColor(value: unknown): ParsedColor | null {
  if (typeof value !== 'string' || value.length > CANVAS_LIMITS.color) return null
  if (value.startsWith('token:')) {
    const name = value.slice('token:'.length)
    return (THEME_TOKENS as readonly string[]).includes(name) ? { token: name as ThemeToken } : null
  }
  if (HEX.test(value)) return { css: value }
  const match = LEGACY_RGB.exec(value) ?? MODERN_RGB.exec(value)
  if (!match) return null
  // Three channels as 0–255 or percentages, then an optional alpha as 0–1 or a percentage.
  for (let channel = 0; channel < 4; channel += 1) {
    const amount = match[1 + channel * 2]
    if (amount === undefined) continue
    const percent = match[2 + channel * 2] === '%'
    const limit = percent ? 100 : channel === 3 ? 1 : 255
    if (Number(amount) > limit) return null
  }
  return { css: value }
}

/**
 * Checks a draw a plugin sent and returns a clean copy (known fields only), or
 * the first problem with the path to it (`ops[12].fill`) so the author can
 * find it.
 */
export function validateDrawOps(raw: unknown): DrawCheck {
  if (!Array.isArray(raw)) return { ok: false, problem: 'ops must be a list of draw ops.' }
  if (raw.length > CANVAS_LIMITS.ops) return { ok: false, problem: `ops has ${raw.length} entries; a draw takes at most ${CANVAS_LIMITS.ops}.` }
  let json: string
  try {
    json = JSON.stringify(raw) ?? ''
  } catch {
    return { ok: false, problem: 'ops is not plain data (it has a cycle or a value JSON cannot hold).' }
  }
  // A UTF-16 unit is 1–3 UTF-8 bytes, so only a length between the two bounds needs encoding (draws arrive up to 240 a second).
  const over = json.length > CANVAS_LIMITS.bytes || (json.length * 3 > CANVAS_LIMITS.bytes && new TextEncoder().encode(json).length > CANVAS_LIMITS.bytes)
  if (over) return { ok: false, problem: `The draw is over ${CANVAS_LIMITS.bytes / 1024} KiB of JSON.` }
  try {
    return { ok: true, ops: raw.map((op, index) => drawOp(op, `ops[${index}]`)) }
  } catch (error) {
    if (error instanceof DrawError) return { ok: false, problem: error.message }
    throw error
  }
}

function drawOp(raw: unknown, path: string): DrawOp {
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) fail(path, 'must be an object')
  const value = raw as Record<string, unknown>
  const op = value.op
  if (typeof op !== 'string' || !Object.hasOwn(OP_FIELDS, op)) {
    fail(`${path}.op`, typeof op === 'string' ? `"${[...op].slice(0, 32).join('')}" is not a draw op (use one of ${DRAW_OPS.join(', ')})` : 'is missing')
  }
  const name = op as DrawOpName
  const allowed: readonly string[] = OP_FIELDS[name]
  for (const field of Object.keys(value)) {
    if (field !== 'op' && !allowed.includes(field)) fail(`${path}${member(field)}`, `is not a field of a "${name}" op`)
  }
  const at = (field: string) => `${path}.${field}`
  const num = (field: string) => finite(value[field], at(field))
  const paint = () => {
    const fill = optColor(value.fill, at('fill'))
    const stroke = optColor(value.stroke, at('stroke'))
    if (fill === undefined && stroke === undefined) fail(path, `needs fill or stroke; a "${name}" with neither draws nothing`)
    return { ...(fill === undefined ? {} : { fill }), ...(stroke === undefined ? {} : { stroke }), ...lineWidth(value.lineWidth, at('lineWidth')) }
  }
  switch (name) {
    case 'clear': {
      const color = optColor(value.color, at('color'))
      return color === undefined ? { op: name } : { op: name, color }
    }
    case 'rect': {
      const radius = value.radius === undefined ? undefined : range(value.radius, 0, CANVAS_LIMITS.size, at('radius'))
      return { op: name, x: num('x'), y: num('y'), w: num('w'), h: num('h'), ...paint(), ...(radius === undefined ? {} : { radius }) }
    }
    case 'line':
      return { op: name, x1: num('x1'), y1: num('y1'), x2: num('x2'), y2: num('y2'), stroke: color(value.stroke, at('stroke')), ...lineWidth(value.lineWidth, at('lineWidth')) }
    case 'circle':
      return { op: name, x: num('x'), y: num('y'), r: range(value.r, 0, Infinity, at('r')), ...paint() }
    case 'path': {
      if (!Array.isArray(value.points)) fail(at('points'), 'must be a list of numbers: x, y, x, y, …')
      const points = value.points as unknown[]
      if (points.length > CANVAS_LIMITS.pathNumbers) fail(at('points'), `has ${points.length} numbers; at most ${CANVAS_LIMITS.pathNumbers}`)
      if (points.length < 4 || points.length % 2 !== 0) fail(at('points'), 'needs an even count of at least 4 numbers (two x, y points)')
      const checked = points.map((point, index) => finite(point, `${at('points')}[${index}]`))
      const closed = value.closed === undefined ? undefined : bool(value.closed, at('closed'))
      return { op: name, points: checked, ...(closed === undefined ? {} : { closed }), ...paint() }
    }
    case 'text': {
      if (typeof value.text !== 'string') fail(at('text'), 'must be a string')
      const text = value.text as string
      if (text.length > CANVAS_LIMITS.text && [...text].length > CANVAS_LIMITS.text) fail(at('text'), `is longer than ${CANVAS_LIMITS.text} characters`)
      const size = range(value.size, CANVAS_LIMITS.textSize.min, CANVAS_LIMITS.textSize.max, at('size'))
      return {
        op: name, x: num('x'), y: num('y'), text, color: color(value.color, at('color')), size,
        ...choice(value.align, ['left', 'center', 'right'] as const, 'align', path),
        ...choice(value.baseline, ['top', 'middle', 'alphabetic', 'bottom'] as const, 'baseline', path),
        ...choice(value.weight, ['normal', 'bold'] as const, 'weight', path),
        ...choice(value.font, ['sans', 'mono'] as const, 'font', path),
      }
    }
    case 'save':
    case 'restore':
      return { op: name }
    case 'translate':
    case 'scale':
      return { op: name, x: num('x'), y: num('y') }
    case 'rotate':
      return { op: name, angle: num('angle') }
    case 'alpha':
      return { op: name, value: range(value.value, 0, 1, at('value')) }
  }
}

function fail(path: string, message: string): never {
  throw new DrawError(`${path} ${message}.`)
}

/** A field as a path segment: `.fill`, or quoted when it is not a plain name (`["line width"]`). */
function member(field: string): string {
  return /^[A-Za-z_$][\w$]{0,63}$/u.test(field) ? `.${field}` : `[${JSON.stringify([...field].slice(0, 64).join(''))}]`
}

function finite(value: unknown, path: string): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) fail(path, 'must be a finite number')
  return value as number
}

function range(value: unknown, min: number, max: number, path: string): number {
  const amount = finite(value, path)
  if (amount < min || amount > max) fail(path, max === Infinity ? `must be ${min} or more` : `must be from ${min} to ${max}`)
  return amount
}

function bool(value: unknown, path: string): boolean {
  if (typeof value !== 'boolean') fail(path, 'must be true or false')
  return value as boolean
}

function color(value: unknown, path: string): Color {
  if (value === undefined) fail(path, 'is missing')
  if (!parseColor(value)) fail(path, `is not a colour (use ${COLOR_FORMS}; tokens are ${THEME_TOKENS.join(', ')})`)
  return value as string
}

function optColor(value: unknown, path: string): Color | undefined {
  return value === undefined ? undefined : color(value, path)
}

function lineWidth(value: unknown, path: string): { readonly lineWidth?: number } {
  if (value === undefined) return {}
  const amount = finite(value, path)
  if (amount <= 0 || amount > CANVAS_LIMITS.lineWidth) fail(path, `must be above 0 and at most ${CANVAS_LIMITS.lineWidth}`)
  return { lineWidth: amount }
}

function choice<const T extends string, const F extends string>(value: unknown, allowed: readonly T[], field: F, path: string): { readonly [K in F]?: T } {
  if (value === undefined) return {}
  if (typeof value !== 'string' || !allowed.includes(value as T)) fail(`${path}.${field}`, `must be one of ${allowed.join(', ')}`)
  return { [field]: value } as { readonly [K in F]?: T }
}
