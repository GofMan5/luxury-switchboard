import type { CommandContribution } from '../shared/contracts/extensions'

/** Subsequence match with a bonus for word starts and contiguous runs, so
 * "sr" finds "Start relay" ahead of "Settings → Retention". 0 = no match. */
export function scoreCommand(query: string, command: CommandContribution): number {
  const needle = query.trim().toLowerCase()
  if (!needle) return 1
  const haystacks = [command.title, command.group, ...(command.keywords ?? [])]
  let best = 0
  for (const [index, raw] of haystacks.entries()) {
    const text = raw.toLowerCase()
    let score = 0
    let position = -1
    let run = 0
    for (const char of needle) {
      if (char === ' ') continue
      const found = text.indexOf(char, position + 1)
      if (found < 0) { score = 0; break }
      const wordStart = found === 0 || /[\s/:.-]/u.test(text[found - 1] ?? '')
      run = found === position + 1 ? run + 1 : 0
      score += 1 + (wordStart ? 3 : 0) + run * 2
      position = found
    }
    if (text.startsWith(needle)) score += 8
    // The title outranks the group, the group outranks keywords.
    if (score > 0) best = Math.max(best, score * (index === 0 ? 1.3 : index === 1 ? 1 : 0.9))
  }
  return best
}
