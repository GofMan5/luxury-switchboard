package relayhttp

import (
	"strings"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	relaydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/domain"
)

// paceMeter times one attempt's generated tokens as they stream in: when the
// first token-bearing event arrived and how many arrived. One meter per
// attempt — a retried answer's tokens describe the attempt it replaced, not
// the one the client is served.
type paceMeter struct {
	first  time.Time
	chunks int
}

func (meter *paceMeter) note() {
	if meter.first.IsZero() {
		meter.first = time.Now()
	}
	meter.chunks++
}

// settle closes the measurement at the stream's end into the serving
// attempt's metadata: the absolute first-token time, and the decode rate
// over the window from that token to now. A meter that saw no token clears
// both — an answer without one has no first token to report.
func (meter *paceMeter) settle(serving *attemptMetadata, outputTokens int64) {
	if serving == nil {
		return
	}
	serving.firstToken = time.Time{}
	serving.tokensPerSecond = 0
	if meter == nil || meter.first.IsZero() {
		return
	}
	serving.firstToken = meter.first
	serving.tokensPerSecond = relaydomain.DecodeRate(outputTokens, meter.chunks, time.Since(meter.first))
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

// notePace counts the event when it carries a generated token.
func (inspector *sseInspector) notePace(eventType string, payload map[string]any) {
	if inspector.pace != nil && tokenEvent(inspector.path, eventType, payload) {
		inspector.pace.note()
	}
}

// tokenEvent reports whether one stream event carries generated text: answer
// or reasoning, the first of either being the token the caller waits for.
// Whitespace-only deltas, role announcements, usage frames and lifecycle
// events carry none. The shapes match the model test's own reading of the
// same dialects, so the journal and the test measure one thing.
func tokenEvent(path, eventType string, payload map[string]any) bool {
	switch {
	case responsesDialectPath(path):
		switch eventType {
		case "response.output_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			return textToken(payload["delta"])
		}
		return false
	case messagesDialectPath(path):
		if eventType != "content_block_delta" {
			return false
		}
		delta, _ := payload["delta"].(map[string]any)
		return textToken(delta["text"]) || textToken(delta["thinking"])
	case chatDialectPath(path), completionsDialectPath(path):
		choices, _ := payload["choices"].([]any)
		for _, choice := range choices {
			item, _ := choice.(map[string]any)
			if textToken(item["text"]) {
				return true
			}
			delta, _ := item["delta"].(map[string]any)
			if textToken(delta["content"]) || reasoningToken(delta["reasoning_content"]) || reasoningToken(delta["reasoning"]) {
				return true
			}
		}
	}
	return false
}

func textToken(value any) bool {
	text, _ := value.(string)
	return strings.TrimSpace(text) != ""
}

// reasoningToken accepts the reasoning field's string form and the object or
// list some providers send in its place; an empty value of any shape is none.
func reasoningToken(value any) bool {
	switch reasoning := value.(type) {
	case string:
		return strings.TrimSpace(reasoning) != ""
	case map[string]any:
		return len(reasoning) > 0
	case []any:
		return len(reasoning) > 0
	}
	return false
}
