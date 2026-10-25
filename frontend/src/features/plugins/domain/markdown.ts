/**
 * The markdown a `markdown` node takes, parsed into plain records the window
 * draws as elements. Nothing here ever becomes HTML: raw `<b>` stays the text
 * `<b>`. The subset is what plugin pages need — ATX headings, paragraphs,
 * emphasis, code spans and fences, lists, quotes, rules and links — and links
 * follow the `link` node's rules (https only, no credentials); any other
 * target leaves its label as plain text.
 *
 * The parser is linear in practice: delimiter searches that fail are
 * remembered, so a page of unmatched `*` or `[` cannot stall the window, and
 * nesting stops at a fixed depth.
 */

export type Inline =
  | { readonly kind: 'text'; readonly text: string }
  | { readonly kind: 'code'; readonly text: string }
  | { readonly kind: 'strong' | 'em'; readonly children: readonly Inline[] }
  | { readonly kind: 'link'; readonly href: string; readonly children: readonly Inline[] }

export type Block =
  | { readonly kind: 'heading'; readonly level: 1 | 2 | 3; readonly children: readonly Inline[] }
  | { readonly kind: 'paragraph'; readonly children: readonly Inline[] }
  | { readonly kind: 'code'; readonly text: string }
  | { readonly kind: 'list'; readonly ordered: boolean; readonly start: number; readonly items: readonly (readonly Block[])[] }
  | { readonly kind: 'quote'; readonly children: readonly Block[] }
  | { readonly kind: 'rule' }

/** Lists and quotes inside each other, and emphasis inside emphasis or links. */
const MAX_BLOCK_DEPTH = 6
const MAX_INLINE_DEPTH = 6
/** A link target longer than this is not a link, which also bounds the work per `[`. */
const MAX_HREF = 2048

export function parseMarkdown(source: string): readonly Block[] {
  return blocks(source.replace(/\r\n?/gu, '\n').split('\n'), 0)
}

/** Whether a markdown link may be followed: the same rule as the `link` node. */
export function isSafeHref(href: string): boolean {
  try {
    const url = new URL(href)
    return url.protocol === 'https:' && !url.username && !url.password
  } catch {
    return false
  }
}

const FENCE = /^ {0,3}(`{3,}|~{3,})(.*)$/u
/** The opening of an ATX heading; the text is taken apart by hand so a long line cannot make a regex backtrack. */
const HEADING = /^ {0,3}(#{1,6})(?=[ \t]|$)/u
const RULE = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/u
const QUOTE = /^ {0,3}> ?(.*)$/u
const ITEM = /^( {0,3})([-*+]|\d{1,9}[.)])(?:[ \t]+(.*))?$/u

function blocks(lines: readonly string[], depth: number): Block[] {
  const out: Block[] = []
  let paragraph: string[] = []
  const flush = () => {
    if (paragraph.length) out.push({ kind: 'paragraph', children: inline(paragraph.map((line) => line.trim()).join('\n'), 0) })
    paragraph = []
  }
  let i = 0
  while (i < lines.length) {
    const line = lines[i]
    if (line.trim() === '') { flush(); i += 1; continue }
    const fence = FENCE.exec(line)
    if (fence && !(fence[1][0] === '`' && fence[2].includes('`'))) {
      flush()
      const marker = fence[1]
      const indent = line.length - line.trimStart().length
      const body: string[] = []
      i += 1
      while (i < lines.length) {
        const closing = /^ {0,3}(`{3,}|~{3,})[ \t]*$/u.exec(lines[i])
        if (closing && closing[1][0] === marker[0] && closing[1].length >= marker.length) { i += 1; break }
        body.push(dedent(lines[i], indent))
        i += 1
      }
      out.push({ kind: 'code', text: body.join('\n') })
      continue
    }
    const heading = HEADING.exec(line)
    if (heading) {
      flush()
      out.push({ kind: 'heading', level: Math.min(3, heading[1].length) as 1 | 2 | 3, children: inline(headingText(line.slice(heading[0].length)), 0) })
      i += 1
      continue
    }
    if (RULE.test(line)) { flush(); out.push({ kind: 'rule' }); i += 1; continue }
    if (depth < MAX_BLOCK_DEPTH && QUOTE.test(line)) {
      flush()
      const inner: string[] = []
      while (i < lines.length && lines[i].trim() !== '') {
        const quoted = QUOTE.exec(lines[i])
        // A line without `>` right after quoted text continues that paragraph (a lazy continuation).
        inner.push(quoted ? quoted[1] : lines[i])
        i += 1
      }
      out.push({ kind: 'quote', children: blocks(inner, depth + 1) })
      continue
    }
    const item = ITEM.exec(line)
    // An item interrupts a paragraph only when it has text, so "2026 -" style lines stay prose.
    if (depth < MAX_BLOCK_DEPTH && item && (paragraph.length === 0 || item[3])) {
      flush()
      i = list(lines, i, depth, out)
      continue
    }
    paragraph.push(line)
    i += 1
  }
  flush()
  return out
}

/** Reads one list from `start`, pushes it and returns the index after it. */
function list(lines: readonly string[], start: number, depth: number, out: Block[]): number {
  const first = ITEM.exec(lines[start]) as RegExpExecArray
  const ordered = /\d/u.test(first[2])
  const delimiter = first[2].at(-1)
  const items: Block[][] = []
  let i = start
  while (i < lines.length) {
    const item = ITEM.exec(lines[i])
    if (!item || /\d/u.test(item[2]) !== ordered || item[2].at(-1) !== delimiter) break
    // Content lines belong to the item while they are indented past its marker.
    const contentIndent = item[1].length + item[2].length + 1
    const body = [item[3] ?? '']
    i += 1
    while (i < lines.length) {
      const next = lines[i]
      if (next.trim() === '') {
        // A blank line ends the list unless the item (or another item) goes on after it.
        let j = i
        while (j < lines.length && lines[j].trim() === '') j += 1
        if (j < lines.length && indentOf(lines[j]) >= contentIndent) { body.push(...lines.slice(i, j).map(() => '')); i = j; continue }
        break
      }
      if (indentOf(next) >= contentIndent) { body.push(dedent(next, contentIndent)); i += 1; continue }
      // Text right under an item continues it; a new item or block does not.
      if (ITEM.test(next) || FENCE.test(next) || HEADING.test(next) || RULE.test(next) || QUOTE.test(next)) break
      body.push(next.trim())
      i += 1
    }
    items.push(blocks(body, depth + 1))
    // A blank line between items keeps the list going.
    let j = i
    while (j < lines.length && lines[j].trim() === '') j += 1
    if (j > i && j < lines.length && ITEM.test(lines[j])) i = j
  }
  out.push({ kind: 'list', ordered, start: ordered ? Number.parseInt(first[2], 10) : 1, items })
  return i
}

/** A heading's text: trimmed, without an optional closing run of `#` (`## Title ##`). */
function headingText(rest: string): string {
  const text = rest.trim()
  let end = text.length
  while (end > 0 && text[end - 1] === '#') end -= 1
  if (end === text.length) return text
  if (end === 0) return ''
  return text[end - 1] === ' ' || text[end - 1] === '\t' ? text.slice(0, end).trimEnd() : text
}

function indentOf(line: string): number {
  let count = 0
  for (const ch of line) {
    if (ch === ' ') count += 1
    else if (ch === '\t') count += 4 - (count % 4)
    else break
  }
  return count
}

/** Removes up to `amount` columns of leading indentation. */
function dedent(line: string, amount: number): string {
  let column = 0
  let index = 0
  while (index < line.length && column < amount) {
    const ch = line[index]
    if (ch === ' ') column += 1
    else if (ch === '\t') column += 4 - (column % 4)
    else break
    index += 1
  }
  return line.slice(index)
}

const PUNCTUATION = /[!-/:-@[-`{-~]/u
const WORD = /[\p{L}\p{N}]/u
const SPACE = /\s/u

function inline(source: string, depth: number): Inline[] {
  if (depth > MAX_INLINE_DEPTH) return source ? [{ kind: 'text', text: source }] : []
  const out: Inline[] = []
  let text = ''
  const push = (node: Inline) => {
    if (text) out.push({ kind: 'text', text })
    text = ''
    out.push(node)
  }
  /** Delimiters known to have no closer after the current position. */
  const misses = new Set<string>()
  let bracket = -1
  let i = 0
  while (i < source.length) {
    const ch = source[i]
    if (ch === '\\' && i + 1 < source.length && PUNCTUATION.test(source[i + 1])) {
      text += source[i + 1]
      i += 2
      continue
    }
    if (ch === '`') {
      const n = runLength(source, i, '`')
      const close = misses.has(`\`${n}`) ? -1 : findRun(source, '`', n, i + n, false)
      if (close < 0) {
        misses.add(`\`${n}`)
        text += source.slice(i, i + n)
        i += n
        continue
      }
      let code = source.slice(i + n, close).replace(/\n/gu, ' ')
      if (code.length > 2 && code.startsWith(' ') && code.endsWith(' ') && code.trim()) code = code.slice(1, -1)
      push({ kind: 'code', text: code })
      i = close + n
      continue
    }
    if (ch === '*' || ch === '_') {
      const n = runLength(source, i, ch)
      const after = source[i + n] ?? ''
      const before = source[i - 1] ?? ''
      const opens = n <= 3 && after !== '' && !SPACE.test(after) && !(ch === '_' && WORD.test(before))
      const key = `${ch}${n}`
      const close = opens && !misses.has(key) ? findRun(source, ch, n, i + n + 1, true) : -1
      if (close < 0) {
        if (opens) misses.add(key)
        text += source.slice(i, i + n)
        i += n
        continue
      }
      const inner = inline(source.slice(i + n, close), depth + 1)
      push(n === 1 ? { kind: 'em', children: inner } : n === 2 ? { kind: 'strong', children: inner } : { kind: 'strong', children: [{ kind: 'em', children: inner }] })
      i = close + n
      continue
    }
    if (ch === '[') {
      // The first `]` after an earlier `[` is still the first after this one until we pass it; none left stays none.
      if (bracket !== Infinity && bracket <= i) bracket = closingBracket(source, i + 1)
      const close = bracket === Infinity ? -1 : bracket
      const target = close < 0 || source[close + 1] !== '(' ? null : linkTarget(source, close + 2)
      if (!target) {
        text += ch
        i += 1
        continue
      }
      const label = inline(source.slice(i + 1, close), depth + 1)
      if (isSafeHref(target.href)) push({ kind: 'link', href: target.href, children: label })
      else for (const node of label) push(node)
      i = target.end
      continue
    }
    text += ch
    i += 1
  }
  if (text) out.push({ kind: 'text', text })
  return out
}

function runLength(source: string, at: number, ch: string): number {
  let end = at
  while (source[end] === ch) end += 1
  return end - at
}

/**
 * The start of the first run of exactly `n` × `ch` at or after `from`. An
 * emphasis closer must follow text (not a space), and an `_` closer must not
 * be followed by a letter, so `snake_case_name` stays one word.
 */
function findRun(source: string, ch: string, n: number, from: number, emphasis: boolean): number {
  const needle = ch.repeat(n)
  let at = source.indexOf(needle, from)
  while (at >= 0) {
    const length = runLength(source, at, ch)
    const before = source[at - 1] ?? ''
    const after = source[at + length] ?? ''
    const fits = length === n && (!emphasis || (before !== '' && !SPACE.test(before) && !(ch === '_' && WORD.test(after))))
    // A backslash before an emphasis run escapes it.
    if (fits && !(emphasis && before === '\\')) return at
    at = source.indexOf(needle, at + length)
  }
  return -1
}

/** The first `]` not escaped by a backslash, at or after `from`, or Infinity when there is none. */
function closingBracket(source: string, from: number): number {
  let at = source.indexOf(']', from)
  while (at > 0 && source[at - 1] === '\\') at = source.indexOf(']', at + 1)
  return at < 0 ? Infinity : at
}

/** `(href)` starting just after the `(`: no spaces, at most MAX_HREF characters, an optional `<…>`. */
function linkTarget(source: string, from: number): { readonly href: string; readonly end: number } | null {
  const close = source.slice(from, from + MAX_HREF + 1).indexOf(')')
  if (close < 0) return null
  let href = source.slice(from, from + close)
  if (href.startsWith('<') && href.endsWith('>')) href = href.slice(1, -1)
  if (href === '' || SPACE.test(href) || href.includes('<') || href.includes('>')) return null
  return { href, end: from + close + 1 }
}

/** The plain text of inline content, for labels and tests. */
export function plainText(nodes: readonly Inline[]): string {
  return nodes.map((node) => (node.kind === 'text' || node.kind === 'code' ? node.text : plainText(node.children))).join('')
}
