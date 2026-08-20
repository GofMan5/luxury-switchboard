import { describe, expect, it } from 'vitest'
import {
  GUARDRAIL_MODES,
  isGuardrailMode,
  modeDescription,
  modeLabel,
  type GuardrailMode,
} from './guardrail'

describe('guardrail domain', () => {
  it('accepts exactly the three modes the control plane validates', () => {
    // A mode the backend rejects must not reach it, and a mode it accepts must not
    // be refused here: either mismatch shows up as a save that silently does nothing.
    expect([...GUARDRAIL_MODES]).toEqual(['off', 'monitor', 'block'])
    for (const mode of GUARDRAIL_MODES) expect(isGuardrailMode(mode)).toBe(true)
    for (const value of ['', 'Block', 'blocking', 'on', 'true', ' monitor', 'OFF']) {
      expect(isGuardrailMode(value)).toBe(false)
    }
  })

  it('names every mode and says what it does to an answer', () => {
    for (const mode of GUARDRAIL_MODES) {
      expect(modeLabel(mode)).not.toBe('')
      expect(modeDescription(mode).length).toBeGreaterThan(20)
    }
    // Only block refuses. If the copy stopped saying so the operator could not tell
    // watching from refusing.
    expect(modeDescription('block')).toMatch(/refused/u)
    expect(modeDescription('monitor')).toMatch(/delivered/u)
    expect(modeDescription('off')).toMatch(/without inspection/u)
  })

  it('treats an unknown mode as unrecognised rather than as a label', () => {
    const unknown = 'hour' as GuardrailMode
    expect(isGuardrailMode(unknown)).toBe(false)
    // The label helper must still not throw: it is called on whatever the control
    // plane reported, and a crash would take the workspace down with it.
    expect(modeLabel(unknown)).toBe('Monitor')
  })
})
