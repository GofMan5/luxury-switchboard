package relayhttp

import (
	"strings"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	relaydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/domain"
)

// paceMeter times one attempt's generation as it streams in. One meter per
// attempt — a retried answer's tokens describe the attempt it replaced, not
// the one the client is served. It keeps counters only, never text.
type paceMeter struct {
	sample relaydomain.PaceSample
}

// newPaceMeter starts a meter for an attempt sent at sent: the wait from
// there to the first token bounds any reasoning the provider kept hidden.
func newPaceMeter(sent time.Time) *paceMeter {
	return &paceMeter{sample: relaydomain.PaceSample{Sent: sent}}
}

func (meter *paceMeter) note(at time.Time, signal tokenSignal) {
	if !signal.any() {
		return
	}
	sample := &meter.sample
	if sample.Opened.IsZero() {
		sample.Opened = at
	}
	sample.Last = at
	sample.Events++
	sample.StreamedBytes += int64(signal.reasoningBytes + signal.answerBytes)
	sample.ReasoningBytes += int64(signal.reasoningBytes)
	if signal.reasoning {
		sample.ReasoningEvents++
	}
	if signal.answer {
		if sample.Answer.IsZero() {
			sample.Answer = at
		}
		sample.AnswerEvents++
	}
}

// settle closes the measurement into the serving attempt's metadata: when
// generation became visible, and the decode rate the provider's usage and
// the stream's own shape support (PaceSample.DecodeRate). A meter that saw
// no generation clears both — an answer without a token has no first token
// to report.
func (meter *paceMeter) settle(serving *attemptMetadata, usage relayapp.TokenUsage) {
	if serving == nil {
		return
	}
	serving.firstToken = time.Time{}
	serving.tokensPerSecond = 0
	if meter == nil || meter.sample.Opened.IsZero() {
		return
	}
	serving.firstToken = meter.sample.Opened
	meter.sample.ReasoningRequested = serving.reasoningRequested
	serving.tokensPerSecond = meter.rate(usage)
}

func (meter *paceMeter) rate(usage relayapp.TokenUsage) float64 {
	sample := meter.sample
	sample.OutputTokens, sample.ReasoningTokens = usage.OutputTokens, usage.ReasoningTokens
	return sample.DecodeRate()
}

// streamPace reports the serving attempt's pace relative to the request's
// start: the first token as the client's wait felt it — queueing and retries
// included — and the decode rate measured on that attempt alone.
func (metadata *attemptMetadata) streamPace(requestStarted time.Time) relayapp.StreamPace {
	if metadata.firstToken.IsZero() {
		return relayapp.StreamPace{}
	}
	return relayapp.StreamPace{
		FirstToken:      max(metadata.firstToken.Sub(requestStarted), 0),
		TokensPerSecond: metadata.tokensPerSecond,
	}
}

// notePace feeds the meter the event's generated tokens.
func (inspector *sseInspector) notePace(eventType string, payload map[string]any) {
	if inspector.pace == nil {
		return
	}
	if signal := tokenEvent(inspector.path, eventType, payload); signal.any() {
		inspector.pace.note(time.Now(), signal)
	}
}

// tokenSignal is what one stream event carried of the generation: whether it
// held reasoning or answer tokens, and their text as UTF-8 bytes. Every
// generated token counts — reasoning, answer text, whitespace, tool-call
// arguments, refusals — because the provider's usage bills them all, and
// dividing all of them by the window of some is wrong by the share skipped.
// An encrypted reasoning entry is reasoning with no visible text. The zero
// value carries nothing.
type tokenSignal struct {
	reasoning, answer           bool
	reasoningBytes, answerBytes int
}

func (signal tokenSignal) any() bool { return signal.reasoning || signal.answer }

func reasoningSignal(size int) tokenSignal { return tokenSignal{reasoning: true, reasoningBytes: size} }
func answerSignal(size int) tokenSignal    { return tokenSignal{answer: true, answerBytes: size} }

// addReasoning and addAnswer fold one value's generatedSize into a chat
// chunk's signal; a negative size is a value the chunk did not carry.
func (signal *tokenSignal) addReasoning(size int) {
	if size >= 0 {
		signal.reasoning = true
		signal.reasoningBytes += size
	}
}

func (signal *tokenSignal) addAnswer(size int) {
	if size >= 0 {
		signal.answer = true
		signal.answerBytes += size
	}
}

// tokenEvent classifies one stream event the way reasoningPhase reads the
// same dialects, so the phase indicator and the meter agree on what thinking
// is. Role announcements, usage frames, signatures and lifecycle events carry
// no generated token.
func tokenEvent(path, eventType string, payload map[string]any) tokenSignal {
	switch {
	case responsesDialectPath(path):
		size := generatedSize(payload["delta"])
		if !strings.HasSuffix(eventType, ".delta") || size < 0 {
			return tokenSignal{}
		}
		if strings.HasPrefix(eventType, "response.reasoning") {
			return reasoningSignal(size)
		}
		return answerSignal(size)
	case messagesDialectPath(path):
		switch eventType {
		case "content_block_start":
			// Redacted thinking arrives whole, once generated.
			if nestedType(payload, "content_block") == "redacted_thinking" {
				return reasoningSignal(0)
			}
		case "content_block_delta":
			delta, _ := payload["delta"].(map[string]any)
			switch delta["type"] {
			case "thinking_delta":
				if size := generatedSize(delta["thinking"]); size > 0 {
					return reasoningSignal(size)
				}
			case "signature_delta":
				// The signature seals the thinking block: bytes the
				// provider computed, not tokens it generated.
			case "input_json_delta":
				if size := generatedSize(delta["partial_json"]); size > 0 {
					return answerSignal(size)
				}
			default:
				if size := generatedSize(delta["text"]); size > 0 {
					return answerSignal(size)
				}
			}
		}
		return tokenSignal{}
	case chatDialectPath(path), completionsDialectPath(path):
		var signal tokenSignal
		choices, _ := payload["choices"].([]any)
		for _, choice := range choices {
			item, _ := choice.(map[string]any)
			signal.addAnswer(generatedSize(item["text"]))
			delta, _ := item["delta"].(map[string]any)
			if delta == nil {
				continue
			}
			for _, field := range []string{"content", "refusal", "tool_calls", "function_call"} {
				signal.addAnswer(generatedSize(delta[field]))
			}
			// Providers send the same reasoning under more than one name
			// (a string and its structured details): the largest is the
			// text, the rest repeat it.
			reasoning := -1
			for _, field := range []string{"reasoning_content", "reasoning", "reasoning_details"} {
				reasoning = max(reasoning, generatedSize(delta[field]))
			}
			signal.addReasoning(reasoning)
		}
		return signal
	}
	return tokenSignal{}
}

// generatedSize is the generated text one value carries: a string's length,
// the text-bearing fields of the object or list some providers send instead
// (a tool call's name and arguments, an encrypted reasoning entry with no
// visible text at all), or -1 for an absent or empty value.
func generatedSize(value any) int {
	switch value := value.(type) {
	case string:
		if value == "" {
			return -1
		}
		return len(value)
	case map[string]any:
		if len(value) == 0 {
			return -1
		}
		size := 0
		for _, field := range []string{"text", "summary", "content", "name", "arguments", "function"} {
			size += max(generatedSize(value[field]), 0)
		}
		return size
	case []any:
		if len(value) == 0 {
			return -1
		}
		size := 0
		for _, item := range value {
			size += max(generatedSize(item), 0)
		}
		return size
	}
	return -1
}
