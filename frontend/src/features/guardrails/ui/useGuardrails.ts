import { useSyncExternalStore } from 'react'
import { useAppServices } from '../../../app/services'

export function useGuardrails() {
  const { guardrails } = useAppServices()
  return { model: guardrails, state: useSyncExternalStore(guardrails.subscribe, guardrails.snapshot) }
}
