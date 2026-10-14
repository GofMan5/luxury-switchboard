const proxySchemes = new Set(['http:', 'https:', 'socks5:', 'socks5h:'])
const MAX_LABEL_RUNES = 80

/**
 * How many keys one import may carry. The control plane enforces the same bound,
 * but a paste far beyond it outgrows the protocol frame and would come back as a
 * transport failure instead of a sentence the caller can act on.
 */
export const MAX_IMPORT_KEYS = 500

export function proxyURLIsValid(value: string): boolean {
  const raw = value.trim()
  if (!raw) return true
  if (raw.length > 8_192) return false
  try {
    const proxy = new URL(raw)
    return proxySchemes.has(proxy.protocol) && Boolean(proxy.hostname) && !proxy.hash
  } catch {
    return false
  }
}

export interface PastedKey {
  readonly label: string
  readonly secret: string
}

/**
 * Reads a pasted list of keys, one key per line, the label first and the secret
 * last: `team_alpha sk-...`. Tabs, commas and semicolons separate as well, and
 * a label may contain spaces because only the last field is taken as the secret.
 *
 * A line with nothing but a secret still becomes a key, named after its line, so a
 * bare list of secrets imports instead of vanishing. Blank lines are ignored, and
 * an over-long label is cut to the length a key accepts rather than costing the
 * caller the line.
 */
export function parsePastedKeys(text: string): PastedKey[] {
  const keys: PastedKey[] = []
  const lines = text.split('\n')
  for (const [index, line] of lines.entries()) {
    const fields = line.split(/[\s,;]+/).filter(Boolean)
    const secret = fields.pop()
    if (!secret) continue
    const label = fields.join(' ').replace(/[:=]+$/, '').trim()
    keys.push({
      label: [...(label || `Key ${index + 1}`)].slice(0, MAX_LABEL_RUNES).join(''),
      secret,
    })
  }
  return keys
}
