import { appearanceProblem, normalizeAppearance, type Appearance } from './appearance'

/** The shareable appearance file: one record, tagged so a stray JSON file
 * (a plugin package, a backup) is refused instead of half-applied. */
export const APPEARANCE_FILE_FORMAT = 'luxury-switchboard.appearance'
export const APPEARANCE_FILE_VERSION = 1
const FILE_LIMIT_BYTES = 128 * 1024

export function serializeAppearance(appearance: Appearance): string {
  return `${JSON.stringify({ format: APPEARANCE_FILE_FORMAT, version: APPEARANCE_FILE_VERSION, appearance }, null, 2)}\n`
}

export type ParsedAppearanceFile = { readonly ok: true; readonly appearance: Appearance } | { readonly ok: false; readonly error: string }

export function parseAppearanceFile(text: string): ParsedAppearanceFile {
  if (new TextEncoder().encode(text).length > FILE_LIMIT_BYTES) return { ok: false, error: 'The file is larger than an appearance file can be' }
  let raw: unknown
  try { raw = JSON.parse(text) } catch { return { ok: false, error: 'The file is not valid JSON' } }
  const value = raw && typeof raw === 'object' ? raw as Record<string, unknown> : {}
  if (value.format !== APPEARANCE_FILE_FORMAT) return { ok: false, error: 'This is not a Luxury Switchboard appearance file' }
  if (value.version !== APPEARANCE_FILE_VERSION) return { ok: false, error: `Appearance file version ${String(value.version)} is not supported` }
  const record = value.appearance && typeof value.appearance === 'object' ? value.appearance as Record<string, unknown> : null
  if (!record) return { ok: false, error: 'The file carries no appearance record' }
  // Validate what the file says, not what normalization would quietly keep:
  // an import that drops half the tokens without a word is worse than a refusal.
  const tokens = record.tokens && typeof record.tokens === 'object' ? record.tokens as Record<string, string> : {}
  const appearance = normalizeAppearance(record)
  const problem = appearanceProblem({ ...appearance, tokens, customCss: typeof record.customCss === 'string' ? record.customCss : '' })
  return problem ? { ok: false, error: problem } : { ok: true, appearance }
}
