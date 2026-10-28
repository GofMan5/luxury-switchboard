import { knownTime, strings } from './plugin'

export const JOURNAL_KINDS = ['installed', 'updated', 'enabled', 'disabled', 'permissions', 'linked', 'unlinked', 'removed'] as const
export type JournalKind = typeof JOURNAL_KINDS[number]

export const JOURNAL_ORIGINS = ['file', 'link', 'folder'] as const
export type JournalOrigin = typeof JOURNAL_ORIGINS[number]

/** One recorded change to a plugin. It names permissions, never data or links. */
export interface JournalEntry {
  readonly seq: number
  readonly at: string
  readonly kind: JournalKind
  readonly pluginId: string
  readonly name: string
  readonly version: string
  readonly fromVersion: string
  readonly granted: readonly string[]
  readonly revoked: readonly string[]
  readonly origin: JournalOrigin | ''
}

/** Newest first. `next` is the cursor for older entries, 0 when there are none. */
export interface JournalPage {
  readonly entries: readonly JournalEntry[]
  readonly next: number
  readonly incomplete: boolean
}

export interface JournalQuery {
  readonly pluginId?: string
  readonly before?: number
  readonly limit?: number
}

/** Keeps only entries this version understands; an unknown kind is dropped, not guessed. */
export function normalizeJournalPage(raw: unknown): JournalPage {
  const page = object(raw)
  const entries = Array.isArray(page.entries) ? page.entries.flatMap((value) => {
    const entry = normalizeEntry(value)
    return entry ? [entry] : []
  }) : []
  return { entries, next: positive(page.next), incomplete: page.incomplete === true }
}

function normalizeEntry(raw: unknown): JournalEntry | null {
  const entry = object(raw)
  const kind = JOURNAL_KINDS.find((value) => value === entry.kind)
  const seq = positive(entry.seq)
  const at = text(entry.at)
  const pluginId = text(entry.pluginId)
  if (!kind || seq === 0 || knownTime(at) === null || pluginId === '') return null
  return {
    seq,
    at,
    kind,
    pluginId,
    name: text(entry.name) || pluginId,
    version: text(entry.version),
    fromVersion: text(entry.fromVersion),
    granted: strings(entry.granted),
    revoked: strings(entry.revoked),
    origin: JOURNAL_ORIGINS.find((value) => value === entry.origin) ?? '',
  }
}

function positive(value: unknown): number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0 ? value : 0
}

function object(value: unknown): Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function text(value: unknown): string {
  return typeof value === 'string' ? value : ''
}
