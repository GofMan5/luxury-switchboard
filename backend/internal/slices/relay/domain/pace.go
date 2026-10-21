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

// DecodeRate is the decode speed of one streamed answer: the reported output
// tokens after the first one, over the window from the first token to the
// stream's end, so queueing and prefill — the time to first token — never
// drag it down. Zero when the guards above reject the evidence.
func DecodeRate(outputTokens int64, chunks int, window time.Duration) float64 {
	if outputTokens < MinPaceTokens || chunks < MinPaceChunks || window < MinPaceWindow {
		return 0
	}
	return float64(outputTokens-1) * float64(time.Second) / float64(window)
}
