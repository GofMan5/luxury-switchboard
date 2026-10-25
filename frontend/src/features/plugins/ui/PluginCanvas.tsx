import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type PointerEvent, type WheelEvent } from 'react'
import { isSafeModeChord } from '../../../shared/runtime/safe-mode'
import { CANVAS_LIMITS, THEME_TOKEN_PROPERTIES, THEME_TOKENS, parseColor, type CanvasChannel, type CanvasInput, type DrawOp, type ThemeToken } from '../domain/canvas'
import type { CanvasNode } from '../domain/ui-tree'
import styles from './PluginTree.module.css'

const MOVE_INTERVAL = 1000 / CANVAS_LIMITS.movesPerSecond
/** A contained canvas never grows taller than this share of the window. */
const MAX_HEIGHT_SHARE = 0.7

type Props = {
  readonly node: CanvasNode
  readonly channel?: CanvasChannel | undefined
  readonly surface?: string | undefined
  /** While the plugin is not running the canvas keeps its picture but sends nothing. */
  readonly disabled: boolean
}

/**
 * A plugin's drawing surface. The plugin sends checked display lists through
 * the channel; this draws them at the screen's pixel density and sends back
 * input in canvas units. Keys reach the plugin only while the canvas has focus,
 * and never Tab, a Ctrl/Alt/Meta chord or the safe-mode chord, so the shell's
 * own keys keep working whatever a plugin asks for.
 */
export function PluginCanvas({ channel, surface, ...props }: Props) {
  const { node } = props
  if (!channel || surface === undefined) {
    return (
      <div className={styles.canvasMissing} role="img" aria-label={node.label} style={{ maxWidth: node.width, aspectRatio: `${node.width} / ${node.height}` }}>
        Canvas unavailable
      </div>
    )
  }
  return <LiveCanvas {...props} channel={channel} surface={surface} />
}

function LiveCanvas({ node, channel, surface, disabled }: Props & { readonly channel: CanvasChannel; readonly surface: string }) {
  const { id, width, height } = node
  const frame = useRef<HTMLDivElement>(null)
  const canvas = useRef<HTMLCanvasElement>(null)
  const ops = useRef<readonly DrawOp[]>([])
  const [scale, setScale] = useState(1)
  const scaleRef = useRef(scale)
  const [focused, setFocused] = useState(false)
  const moves = useRef<{ last: number; timer: ReturnType<typeof setTimeout> | undefined; pending: CanvasInput | undefined }>({ last: -Infinity, timer: undefined, pending: undefined })
  const interactive = node.keys || node.pointer

  // contain: fill the width the page gives, keeping the aspect ratio; none: canvas units are CSS pixels.
  useLayoutEffect(() => {
    const element = frame.current
    if (!element || node.fit === 'none') {
      setScale(1)
      return
    }
    const measure = () => {
      const available = element.clientWidth
      if (available > 0) setScale(Math.min(available / width, (window.innerHeight * MAX_HEIGHT_SHARE) / height))
    }
    measure()
    const observer = typeof ResizeObserver === 'undefined' ? undefined : new ResizeObserver(measure)
    observer?.observe(element)
    window.addEventListener('resize', measure)
    return () => {
      observer?.disconnect()
      window.removeEventListener('resize', measure)
    }
  }, [node.fit, width, height])

  // Redraws the last picture whenever its size changes, or the theme it names colours from does.
  useLayoutEffect(() => {
    scaleRef.current = scale
    const element = canvas.current
    if (!element) return
    const redraw = () => render(element, ops.current, width, height, scale)
    redraw()
    const theme = new MutationObserver(redraw)
    theme.observe(document.documentElement, { attributes: true })
    return () => theme.disconnect()
  }, [scale, width, height])

  useEffect(() => {
    ops.current = []
    return channel.subscribe(surface, id, (next) => {
      ops.current = next
      if (canvas.current) render(canvas.current, next, width, height, scaleRef.current)
    })
  }, [channel, surface, id, width, height])

  // Frames run only while the canvas is on screen; leaving the page counts as hidden.
  useEffect(() => {
    const element = canvas.current
    if (!element) return
    let shown = false
    const show = (next: boolean) => {
      if (next === shown) return
      shown = next
      channel.visible(surface, id, node.frames, next)
    }
    if (typeof IntersectionObserver === 'undefined') {
      show(true)
      return () => show(false)
    }
    const observer = new IntersectionObserver((entries) => show(entries.some((entry) => entry.isIntersecting)))
    observer.observe(element)
    return () => {
      observer.disconnect()
      show(false)
    }
  }, [channel, surface, id, node.frames])

  useEffect(() => () => clearTimeout(moves.current.timer), [])

  const send = (event: CanvasInput) => channel.input(surface, id, event)
  const flushMove = () => {
    const pending = moves.current
    clearTimeout(pending.timer)
    pending.timer = undefined
    if (!pending.pending) return
    pending.last = performance.now()
    send(pending.pending)
    pending.pending = undefined
  }

  const pointer = (kind: 'pointerdown' | 'pointerup' | 'pointermove' | 'wheel', event: PointerEvent<HTMLCanvasElement> | WheelEvent<HTMLCanvasElement>): CanvasInput => {
    const box = event.currentTarget.getBoundingClientRect()
    const x = box.width > 0 ? ((event.clientX - box.left) / box.width) * width : 0
    const y = box.height > 0 ? ((event.clientY - box.top) / box.height) * height : 0
    return {
      surface, canvas: id, kind, x: round(x), y: round(y), button: event.button, buttons: event.buttons,
      ...(kind === 'wheel' ? { deltaY: (event as WheelEvent).deltaY } : {}),
    }
  }
  const pointing = node.pointer && !disabled

  const onKey = (event: KeyboardEvent<HTMLCanvasElement>) => {
    if (!node.keys || disabled) return
    if (event.key === 'Tab' || event.ctrlKey || event.altKey || event.metaKey || isSafeModeChord(event.nativeEvent)) return
    // The page and the shell must not act on a key the plugin took.
    event.preventDefault()
    event.stopPropagation()
    send({
      surface, canvas: id, kind: event.type === 'keyup' ? 'keyup' : 'keydown',
      key: event.key, code: event.code, repeat: event.repeat, shift: event.shiftKey, alt: false, ctrl: false, meta: false,
    })
  }

  return (
    <div ref={frame} className={styles.canvasFrame}>
      <span className={styles.canvasBox}>
        <canvas
          ref={canvas}
          className={styles.canvas}
          style={{ width: width * scale, height: height * scale }}
          data-pointer={node.pointer || undefined}
          role={node.keys ? 'application' : 'img'}
          aria-label={node.label}
          aria-roledescription="canvas"
          tabIndex={interactive ? 0 : undefined}
          onKeyDown={onKey}
          onKeyUp={onKey}
          onFocus={() => {
            setFocused(true)
            if (interactive && !disabled) send({ surface, canvas: id, kind: 'focus' })
          }}
          onBlur={() => {
            setFocused(false)
            if (interactive && !disabled) send({ surface, canvas: id, kind: 'blur' })
          }}
          onPointerDown={pointing ? (event) => {
            // Captured, so a drag that leaves the canvas still ends with pointerup.
            event.currentTarget.setPointerCapture?.(event.pointerId)
            flushMove()
            send(pointer('pointerdown', event))
          } : undefined}
          onPointerUp={pointing ? (event) => {
            flushMove()
            send(pointer('pointerup', event))
          } : undefined}
          onPointerMove={pointing ? (event) => {
            const pending = moves.current
            pending.pending = pointer('pointermove', event)
            const wait = pending.last + MOVE_INTERVAL - performance.now()
            if (wait <= 0) flushMove()
            else pending.timer ??= setTimeout(flushMove, wait)
          } : undefined}
          onWheel={pointing ? (event) => send(pointer('wheel', event)) : undefined}
        >
          {node.label}
        </canvas>
        {node.keys && !focused && !disabled ? <span className={styles.canvasHint} aria-hidden="true">Click to focus</span> : null}
      </span>
    </div>
  )
}

function round(value: number): number {
  return Math.round(value * 100) / 100
}

/** Replays a whole display list: every draw replaces the picture, so a resize can redraw the last one. */
function render(canvas: HTMLCanvasElement, ops: readonly DrawOp[], width: number, height: number, scale: number): void {
  const pixels = scale * (window.devicePixelRatio || 1)
  const backingWidth = Math.max(1, Math.round(width * pixels))
  const backingHeight = Math.max(1, Math.round(height * pixels))
  // Assigning a size clears the canvas and resets its state, so only a real change does it.
  if (canvas.width !== backingWidth) canvas.width = backingWidth
  if (canvas.height !== backingHeight) canvas.height = backingHeight
  const context = canvas.getContext('2d')
  if (!context) return
  const style = getComputedStyle(canvas)
  const tokens = new Map<ThemeToken, string>(THEME_TOKENS.map((token) => [token, style.getPropertyValue(THEME_TOKEN_PROPERTIES[token]).trim() || 'transparent']))
  const color = (value: string) => {
    const parsed = parseColor(value)
    return !parsed ? 'transparent' : 'css' in parsed ? parsed.css : tokens.get(parsed.token) ?? 'transparent'
  }
  const sans = style.fontFamily || 'sans-serif'
  const mono = style.getPropertyValue('--font-mono').trim() || 'monospace'
  const finish = (op: { readonly fill?: string; readonly stroke?: string; readonly lineWidth?: number }) => {
    if (op.fill !== undefined) {
      context.fillStyle = color(op.fill)
      context.fill()
    }
    if (op.stroke !== undefined) {
      context.strokeStyle = color(op.stroke)
      context.lineWidth = op.lineWidth ?? 1
      context.stroke()
    }
  }

  context.setTransform(pixels, 0, 0, pixels, 0, 0)
  context.globalAlpha = 1
  context.clearRect(0, 0, width, height)
  context.save()
  // A plugin's unmatched restore must not pop the base transform.
  let depth = 0
  for (const op of ops) {
    switch (op.op) {
      case 'clear':
        context.save()
        context.setTransform(pixels, 0, 0, pixels, 0, 0)
        context.globalAlpha = 1
        context.clearRect(0, 0, width, height)
        if (op.color !== undefined) {
          context.fillStyle = color(op.color)
          context.fillRect(0, 0, width, height)
        }
        context.restore()
        break
      case 'rect':
        context.beginPath()
        if (op.radius) context.roundRect(op.x, op.y, op.w, op.h, op.radius)
        else context.rect(op.x, op.y, op.w, op.h)
        finish(op)
        break
      case 'line':
        context.beginPath()
        context.moveTo(op.x1, op.y1)
        context.lineTo(op.x2, op.y2)
        finish(op)
        break
      case 'circle':
        context.beginPath()
        context.arc(op.x, op.y, op.r, 0, Math.PI * 2)
        finish(op)
        break
      case 'path':
        context.beginPath()
        context.moveTo(op.points[0]!, op.points[1]!)
        for (let index = 2; index < op.points.length; index += 2) context.lineTo(op.points[index]!, op.points[index + 1]!)
        if (op.closed) context.closePath()
        finish(op)
        break
      case 'text':
        context.font = `${op.weight ?? 'normal'} ${op.size}px ${op.font === 'mono' ? mono : sans}`
        context.textAlign = op.align ?? 'left'
        context.textBaseline = op.baseline ?? 'alphabetic'
        context.fillStyle = color(op.color)
        context.fillText(op.text, op.x, op.y)
        break
      case 'save':
        depth += 1
        context.save()
        break
      case 'restore':
        if (depth === 0) break
        depth -= 1
        context.restore()
        break
      case 'translate':
        context.translate(op.x, op.y)
        break
      case 'scale':
        context.scale(op.x, op.y)
        break
      case 'rotate':
        context.rotate(op.angle)
        break
      case 'alpha':
        context.globalAlpha = op.value
        break
    }
  }
  for (; depth > 0; depth -= 1) context.restore()
  context.restore()
}
