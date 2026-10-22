package domain

import "time"

// Pace guards, shared by the model test and the request journal: fewer
// tokens, fewer token-bearing chunks or a shorter window than these measure
// the network's batching, not the provider's decode speed. A gateway that
// buffers the answer and replays it as one or two chunks would otherwise
// report a "speed" of thousands of tokens per second.
const (
	MinPaceTokens = 16
	MinPaceChunks = 8
	MinPaceWindow = 100 * time.Millisecond
)

// MinStreamedBytesPerToken tells reasoning a provider streamed from reasoning
// it hid. Reasoning is prose, about four UTF-8 bytes per token in any script;
// reasoning kept hidden or replaced by a summary leaves a fraction of a byte
// per billed token.
const MinStreamedBytesPerToken = 3

// DecodeRate is the decode speed of one streamed answer: the output tokens
// after the first one, over the window from the first token to the last, so
// queueing and prefill — the time to first token — never drag it down. Zero
// when the guards above reject the evidence.
func DecodeRate(outputTokens int64, chunks int, window time.Duration) float64 {
	if outputTokens < MinPaceTokens || chunks < MinPaceChunks || window < MinPaceWindow {
		return 0
	}
	return float64(outputTokens-1) * float64(time.Second) / float64(window)
}

// PaceSample is what one streamed answer showed of its own generation, with
// the provider's usage for it. The provider bills every token it generated
// but may stream only some — reasoning can be hidden or replaced by a short
// summary — and dividing the billed tokens by the window the visible ones
// spanned reports several times the real speed. DecodeRate picks the window
// the billed tokens were actually generated in.
type PaceSample struct {
	// Sent is when the request left for the provider. Opened is the first
	// generated token of any kind, Answer the first past reasoning — text,
	// a tool call, a refusal — and Last the last of either.
	Sent, Opened, Answer, Last time.Time
	// Events counts token-bearing events; AnswerEvents and ReasoningEvents
	// those that carried each kind.
	Events, AnswerEvents, ReasoningEvents int
	// StreamedBytes is the generated text the stream carried,
	// ReasoningBytes the reasoning share of it.
	StreamedBytes, ReasoningBytes int64
	// OutputTokens and ReasoningTokens are the provider's usage report.
	OutputTokens, ReasoningTokens int64
}

// DecodeRate is the sample's decode speed, or zero when the stream cannot
// tell it. An answer that streamed every token it billed — no reasoning, or
// reasoning shown in full — is measured over its whole window. Otherwise the
// reasoning was hidden or summarized:
//
//   - its size reported, the answer phase alone is exact: the output minus
//     the reasoning, over the window from the first answer token;
//   - unreported, the hidden tokens were generated in the wait before the
//     window or during it; the window's rate stands when the window outlasts
//     that wait, which bounds the overstatement at double, and is withheld
//     otherwise.
//
// An answer that never reasoned has nothing to hide, however dense its text.
func (sample PaceSample) DecodeRate() float64 {
	if sample.Opened.IsZero() || sample.Last.IsZero() {
		return 0
	}
	window := sample.Last.Sub(sample.Opened)
	switch {
	case sample.ReasoningTokens > 0:
		if sample.ReasoningBytes < MinStreamedBytesPerToken*sample.ReasoningTokens {
			if sample.Answer.IsZero() {
				return 0
			}
			return DecodeRate(sample.OutputTokens-sample.ReasoningTokens, sample.AnswerEvents, sample.Last.Sub(sample.Answer))
		}
	case sample.ReasoningEvents > 0 && sample.StreamedBytes < MinStreamedBytesPerToken*sample.OutputTokens:
		if sample.Sent.IsZero() || window < sample.Opened.Sub(sample.Sent) {
			return 0
		}
	}
	return DecodeRate(sample.OutputTokens, sample.Events, window)
}
