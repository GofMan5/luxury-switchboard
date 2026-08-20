package domain

import "errors"

// Mode is how far the guardrails act on what they find.
//
// The default is Monitor rather than Block. A false positive in Block mode
// destroys a legitimate answer the caller is waiting on, and these rules match
// on shell and network idiom that a developer's own assistant produces for
// honest reasons all day. Monitor still tells the operator exactly what a
// provider sent; they turn on Block for providers they do not trust.
type Mode string

const (
	// ModeOff runs no inspection at all — not even extraction.
	ModeOff Mode = "off"
	// ModeMonitor records findings and forwards the answer unchanged.
	ModeMonitor Mode = "monitor"
	// ModeBlock additionally refuses answers whose worst finding is high.
	ModeBlock Mode = "block"
)

// Verdict is what the guardrails decided about one answer.
type Verdict string

const (
	// VerdictClean means nothing matched.
	VerdictClean Verdict = "clean"
	// VerdictAlert means something matched and the answer was still forwarded.
	VerdictAlert Verdict = "alert"
	// VerdictBlocked means the answer was refused and never reached the client.
	VerdictBlocked Verdict = "blocked"
)

// ParseMode validates a configured mode. An unrecognised value is an error
// rather than a silent fallback: a typo must not quietly disable the guardrails.
func ParseMode(value string) (Mode, error) {
	switch Mode(value) {
	case ModeOff, ModeMonitor, ModeBlock:
		return Mode(value), nil
	default:
		return "", errors.New("guardrail mode must be off, monitor or block")
	}
}

// Inspects reports whether the mode does any work.
func (mode Mode) Inspects() bool {
	return mode == ModeMonitor || mode == ModeBlock
}

// Decide turns findings into a verdict for this mode.
func (mode Mode) Decide(findings []Finding) Verdict {
	if len(findings) == 0 {
		return VerdictClean
	}
	if mode == ModeBlock && MaxSeverity(findings) == SeverityHigh {
		return VerdictBlocked
	}
	return VerdictAlert
}
