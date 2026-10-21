import { Brain } from 'lucide-react'
import { describeReasoningLevel, formatReasoningLevel } from '../format/reasoning'
import { Pill } from './chrome'
import styles from './chrome.module.css'

/** The reasoning level a request asked for, as a quiet badge beside the
 * model. Renders nothing for a request that left reasoning to the provider. */
export function ReasoningLevel({ effort, budget }: { readonly effort?: string; readonly budget?: number }) {
  const label = formatReasoningLevel(effort, budget)
  if (!label) return null
  return (
    <span className={styles.reasoningLevel} title={`Reasoning: ${describeReasoningLevel(effort, budget)}`}>
      <Pill>
        <Brain size={11} aria-hidden="true" />
        <span className="sr-only">Reasoning </span>
        {label}
      </Pill>
    </span>
  )
}
