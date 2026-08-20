/**
 * Mode decides what the guardrails do with a finding. Monitor is the default:
 * these rules match shell and network idiom an honest coding assistant produces
 * all day, so blocking every match would destroy legitimate answers.
 */
export type GuardrailMode = 'off' | 'monitor' | 'block'

export const GUARDRAIL_MODES: readonly GuardrailMode[] = ['off', 'monitor', 'block']

export type GuardrailSeverity = 'low' | 'medium' | 'high'

export type GuardrailVerdict = 'clean' | 'alert' | 'blocked'

/**
 * A finding carries the evidence and nothing else. There is no rule pattern here
 * on purpose: a report that showed the patterns would double as an evasion guide.
 */
export interface GuardrailFinding {
  readonly ruleId: string
  readonly category: string
  readonly severity: GuardrailSeverity
  readonly match: string
  readonly excerpt: string
  readonly source: string
  readonly description: string
}

export interface GuardrailRecord {
  readonly id: string
  readonly at: string
  readonly verdict: GuardrailVerdict
  readonly severity: GuardrailSeverity
  readonly providerId: string
  readonly providerName: string
  readonly model: string
  readonly findings: readonly GuardrailFinding[]
}

export interface GuardrailStatus {
  readonly mode: GuardrailMode
  readonly ruleCount: number
  readonly indicatorCount: number
  readonly ruleSetVersion: number
  readonly findingCount: number
}

export function isGuardrailMode(value: string): value is GuardrailMode {
  return (GUARDRAIL_MODES as readonly string[]).includes(value)
}

export function modeLabel(mode: GuardrailMode): string {
  if (mode === 'off') return 'Off'
  return mode === 'block' ? 'Block' : 'Monitor'
}

export function modeDescription(mode: GuardrailMode): string {
  if (mode === 'off') return 'Answers are forwarded without inspection.'
  if (mode === 'block') return 'Answers with a high-severity finding are refused before you see them.'
  return 'Everything is inspected and recorded, and every answer is still delivered.'
}
