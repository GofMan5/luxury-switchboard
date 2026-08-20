import type { GuardrailMode, GuardrailRecord, GuardrailStatus } from '../domain/guardrail'

export interface GuardrailsPort {
  status(signal?: AbortSignal): Promise<GuardrailStatus>
  findings(limit: number, signal?: AbortSignal): Promise<readonly GuardrailRecord[]>
  clear(signal?: AbortSignal): Promise<void>
  /** The mode lives in Settings, so changing it goes through the settings command. */
  setMode(mode: GuardrailMode, signal?: AbortSignal): Promise<GuardrailMode>
  /** Called for each new finding, with the record the control plane emitted. */
  subscribe(listener: (record: GuardrailRecord) => void): () => void
  /** Called when the finding list is cleared or the rule set state changes. */
  subscribeStatus(listener: () => void): () => void
}
