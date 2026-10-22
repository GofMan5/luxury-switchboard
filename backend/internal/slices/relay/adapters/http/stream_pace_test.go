package relayhttp

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	activityapp "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	guardraildomain "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
	relayactivity "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/activity"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

func chatDelta(delta map[string]any) map[string]any {
	return map[string]any{"choices": []any{map[string]any{"index": 0.0, "delta": delta}}}
}

// Every token the provider bills is a token the meter must see: the rate
// divides the billed count by the window, so a skipped kind — whitespace,
// tool arguments, a refusal — shrinks the window under the same count.
func TestTokenEventCountsEveryGeneratedToken(t *testing.T) {
	answer, reasoning := answerSignal, reasoningSignal
	toolCall := []any{map[string]any{"index": 0.0, "id": "call_1", "type": "function", "function": map[string]any{"name": "look", "arguments": `{"q"`}}}
	for _, tc := range []struct {
		name, path, eventType string
		payload               map[string]any
		want                  tokenSignal
	}{
		{"chat content", "/v1/chat/completions", "", chatDelta(map[string]any{"content": "1 2"}), answer(3)},
		{"chat whitespace", "/v1/chat/completions", "", chatDelta(map[string]any{"content": " \n"}), answer(2)},
		{"chat refusal", "/v1/chat/completions", "", chatDelta(map[string]any{"refusal": "no"}), answer(2)},
		{"chat tool call", "/v1/chat/completions", "", chatDelta(map[string]any{"tool_calls": toolCall}), answer(8)},
		{"chat legacy function call", "/v1/chat/completions", "", chatDelta(map[string]any{"function_call": map[string]any{"arguments": "{}"}}), answer(2)},
		{"chat reasoning_content", "/v1/chat/completions", "", chatDelta(map[string]any{"reasoning_content": "hm"}), reasoning(2)},
		{"chat reasoning", "/v1/chat/completions", "", chatDelta(map[string]any{"reasoning": "hm"}), reasoning(2)},
		{"chat reasoning object", "/v1/chat/completions", "", chatDelta(map[string]any{"reasoning": map[string]any{"text": "hm"}}), reasoning(2)},
		{"chat reasoning named twice", "/v1/chat/completions", "", chatDelta(map[string]any{"reasoning": "hmm", "reasoning_details": []any{map[string]any{"type": "reasoning.text", "text": "hmm"}}}), reasoning(3)},
		{"chat encrypted reasoning", "/v1/chat/completions", "", chatDelta(map[string]any{"reasoning_details": []any{map[string]any{"type": "reasoning.encrypted", "data": "opaque"}}}), reasoning(0)},
		{"chat reasoning and answer", "/v1/chat/completions", "", chatDelta(map[string]any{"reasoning_content": "hm", "content": "okay"}), tokenSignal{reasoning: true, answer: true, reasoningBytes: 2, answerBytes: 4}},
		{"chat empty reasoning object", "/v1/chat/completions", "", chatDelta(map[string]any{"reasoning": map[string]any{}}), tokenSignal{}},
		{"chat role only", "/v1/chat/completions", "", chatDelta(map[string]any{"role": "assistant", "content": ""}), tokenSignal{}},
		{"chat usage frame", "/v1/chat/completions", "", map[string]any{"choices": []any{}, "usage": map[string]any{"completion_tokens": 9.0}}, tokenSignal{}},
		{"completions text", "/v1/completions", "", map[string]any{"choices": []any{map[string]any{"text": "1"}}}, answer(1)},
		{"responses text", "/v1/responses", "response.output_text.delta", map[string]any{"delta": "1"}, answer(1)},
		{"responses tool arguments", "/v1/responses", "response.function_call_arguments.delta", map[string]any{"delta": "{"}, answer(1)},
		{"responses custom tool input", "/v1/responses", "response.custom_tool_call_input.delta", map[string]any{"delta": "ls"}, answer(2)},
		{"responses refusal", "/v1/responses", "response.refusal.delta", map[string]any{"delta": "no"}, answer(2)},
		{"responses reasoning", "/v1/responses", "response.reasoning_summary_text.delta", map[string]any{"delta": "hm"}, reasoning(2)},
		{"responses reasoning item", "/v1/responses", "response.output_item.added", map[string]any{"item": map[string]any{"type": "reasoning"}}, tokenSignal{}},
		{"responses empty delta", "/v1/responses", "response.output_text.delta", map[string]any{"delta": ""}, tokenSignal{}},
		{"responses lifecycle", "/v1/responses", "response.created", map[string]any{}, tokenSignal{}},
		{"messages text", "/v1/messages", "content_block_delta", map[string]any{"delta": map[string]any{"type": "text_delta", "text": "1"}}, answer(1)},
		{"messages tool input", "/v1/messages", "content_block_delta", map[string]any{"delta": map[string]any{"type": "input_json_delta", "partial_json": `{"q"`}}, answer(4)},
		{"messages thinking", "/v1/messages", "content_block_delta", map[string]any{"delta": map[string]any{"type": "thinking_delta", "thinking": "hm"}}, reasoning(2)},
		{"messages thinking block", "/v1/messages", "content_block_start", map[string]any{"content_block": map[string]any{"type": "thinking"}}, tokenSignal{}},
		{"messages redacted block", "/v1/messages", "content_block_start", map[string]any{"content_block": map[string]any{"type": "redacted_thinking", "data": "opaque"}}, reasoning(0)},
		{"messages text block", "/v1/messages", "content_block_start", map[string]any{"content_block": map[string]any{"type": "text"}}, tokenSignal{}},
		{"messages signature", "/v1/messages", "content_block_delta", map[string]any{"delta": map[string]any{"type": "signature_delta", "signature": "abc"}}, tokenSignal{}},
		{"messages stop", "/v1/messages", "message_stop", map[string]any{}, tokenSignal{}},
	} {
		if got := tokenEvent(tc.path, tc.eventType, tc.payload); got != tc.want {
			t.Errorf("%s: tokenEvent = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestPaceSettlesOnlyAMeasuredAttempt(t *testing.T) {
	serving := &attemptMetadata{firstToken: time.Now(), tokensPerSecond: 99}
	newPaceMeter(time.Now()).settle(serving, relayapp.TokenUsage{OutputTokens: 100})
	if !serving.firstToken.IsZero() || serving.tokensPerSecond != 0 {
		t.Fatalf("an attempt without tokens kept the replaced attempt's pace: %+v", serving)
	}
	started := time.Now()
	meter := newPaceMeter(started.Add(-1100 * time.Millisecond))
	first := started.Add(-time.Second)
	for index := range 20 {
		meter.note(first.Add(time.Duration(index)*time.Second/19), answerSignal(24))
	}
	meter.settle(serving, relayapp.TokenUsage{OutputTokens: 101})
	if serving.firstToken != first || math.Abs(serving.tokensPerSecond-100) > 1e-9 {
		t.Fatalf("measured pace was not settled: %+v", serving)
	}
	if pace := serving.streamPace(started.Add(-1500 * time.Millisecond)); pace.FirstToken != 500*time.Millisecond {
		t.Fatalf("first token is not measured from the request's start: %s", pace.FirstToken)
	}
	if pace := serving.streamPace(started); pace.FirstToken != 0 {
		t.Fatalf("a first token before the request start must clamp to zero: %s", pace.FirstToken)
	}
}

type paceActivity struct {
	relayapp.NoopActivity
	mu       sync.Mutex
	finishes []relayapp.ActivityFinish
}

func (sink *paceActivity) Begin(relayapp.ActivityStart) string { return "req_pace" }

func (sink *paceActivity) Finish(_ string, finish relayapp.ActivityFinish) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.finishes = append(sink.finishes, finish)
}

// numberStream answers the counting task the model test asks for: count
// numbers, one per chunk with gap between them, then the provider's usage.
// Each chunk carries prose-sized text for the tokens it bills, as a real
// answer does.
func numberStream(chunks int, wait, gap time.Duration, completionTokens int) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		flush := func(chunk string) {
			_, _ = writer.Write([]byte(chunk))
			writer.(http.Flusher).Flush()
		}
		flush(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}` + "\n\n")
		time.Sleep(wait)
		for index := 1; index <= chunks; index++ {
			if index > 1 {
				time.Sleep(gap)
			}
			flush(fmt.Sprintf(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"%d %s"}}]}`, index, strings.Repeat("and then ", completionTokens/chunks)) + "\n\n")
		}
		flush(fmt.Sprintf(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":%d,"total_tokens":%d}}`, completionTokens, completionTokens+10) + "\n\n")
		flush("data: [DONE]\n\n")
	}
}

// End to end through the relay: the journal files the client's wait for the
// first token and the decode rate over the window after it, on the buffered
// path and after the live handoff alike; a burst files the first token but
// no rate.
func TestRelayFilesFirstTokenAndDecodeRate(t *testing.T) {
	const wait, gap, chunks, tokens = 60 * time.Millisecond, 12 * time.Millisecond, 12, 45
	for _, live := range []bool{false, true} {
		for _, burst := range []bool{false, true} {
			name := map[bool]string{false: "buffered", true: "live"}[live] + map[bool]string{false: "", true: " burst"}[burst]
			t.Run(name, func(t *testing.T) {
				streamChunks := chunks
				if burst {
					streamChunks = 2
				}
				upstream := httptest.NewServer(numberStream(streamChunks, wait, gap, tokens))
				defer upstream.Close()
				sink := &paceActivity{}
				mode := guardraildomain.ModeBlock
				if live {
					mode = guardraildomain.ModeMonitor
				}
				server, _ := guardedServerWatchedBy(t, mode, upstream.URL, sink)
				server.config.LiveStreamProbation = 20 * time.Millisecond
				request := httptest.NewRequest(http.MethodPost, "http://relay/v1/chat/completions", strings.NewReader(
					`{"model":"glm-5.3","stream":true,"messages":[{"role":"user","content":"count"}]}`))
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				server.ServeHTTP(response, request)
				if !strings.Contains(response.Body.String(), fmt.Sprintf("%d ", streamChunks)) {
					t.Fatalf("the answer did not reach the client: %q", response.Body.String())
				}
				sink.mu.Lock()
				defer sink.mu.Unlock()
				if len(sink.finishes) != 1 {
					t.Fatalf("finishes = %d, want 1", len(sink.finishes))
				}
				pace := sink.finishes[0].Pace
				if pace.FirstToken < wait || pace.FirstToken > 5*time.Second {
					t.Fatalf("first token %s, want at least the provider's %s wait", pace.FirstToken, wait)
				}
				if burst {
					if pace.TokensPerSecond != 0 {
						t.Fatalf("a two-chunk burst filed a rate: %+v", pace)
					}
					return
				}
				// The window spans at least the gaps between the chunks, so
				// the rate can be no higher than tokens-after-the-first over
				// them; the first token's own wait never enters it.
				ceiling := float64(tokens-1) / (time.Duration(chunks-1) * gap).Seconds()
				if pace.TokensPerSecond <= 0 || pace.TokensPerSecond > ceiling {
					t.Fatalf("rate %.1f tok/s, want within (0, %.1f]", pace.TokensPerSecond, ceiling)
				}
			})
		}
	}
}

// scriptedFrame is one chat chunk sent after a pause.
type scriptedFrame struct {
	after time.Duration
	data  string
}

func scriptedStream(frames []scriptedFrame) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		for _, frame := range frames {
			time.Sleep(frame.after)
			_, _ = writer.Write([]byte("data: " + frame.data + "\n\n"))
			writer.(http.Flusher).Flush()
		}
	}
}

func relayChatPace(t *testing.T, handler http.HandlerFunc, body string) relayapp.StreamPace {
	t.Helper()
	upstream := httptest.NewServer(handler)
	defer upstream.Close()
	sink := &paceActivity{}
	server, _ := guardedServerWatchedBy(t, guardraildomain.ModeMonitor, upstream.URL, sink)
	server.config.LiveStreamProbation = 20 * time.Millisecond
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(httptest.NewRecorder(), request)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.finishes) != 1 {
		t.Fatalf("finishes = %d, want 1", len(sink.finishes))
	}
	return sink.finishes[0].Pace
}

// A provider that bills hidden reasoning but streams only a one-line summary
// of it: dividing every billed token by the window the visible ones spanned
// reported several times the real speed. The answer phase alone is the
// decode evidence.
func TestRelayMeasuresTheAnswerPhaseBehindHiddenReasoning(t *testing.T) {
	const wait, hidden, gap, chunks = 30 * time.Millisecond, 200 * time.Millisecond, 12 * time.Millisecond, 12
	const output, reasoning = 1_000, 900
	frames := []scriptedFrame{
		{0, `{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`},
		{wait, `{"choices":[{"index":0,"delta":{"reasoning_content":"Planning."}}]}`},
	}
	for index := 1; index <= chunks; index++ {
		pause := gap
		if index == 1 {
			pause = hidden
		}
		frames = append(frames, scriptedFrame{pause, fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":"%d %s"}}]}`, index, strings.Repeat("and then ", 8))})
	}
	frames = append(frames,
		scriptedFrame{0, fmt.Sprintf(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":%d,"completion_tokens_details":{"reasoning_tokens":%d}}}`, output, reasoning)},
		scriptedFrame{0, "[DONE]"},
	)
	pace := relayChatPace(t, scriptedStream(frames), `{"model":"glm-5.3","stream":true,"messages":[{"role":"user","content":"count"}]}`)
	if pace.FirstToken < wait {
		t.Fatalf("first token %s, want the reasoning summary's %s", pace.FirstToken, wait)
	}
	// The answer's 100 tokens over at least its 11 gaps. The whole window
	// would have said 999 tokens over ~330ms — about four times as fast.
	ceiling := float64(output-reasoning-1) / (time.Duration(chunks-1) * gap).Seconds()
	if pace.TokensPerSecond <= 0 || pace.TokensPerSecond > ceiling {
		t.Fatalf("rate %.1f tok/s, want within (0, %.1f]", pace.TokensPerSecond, ceiling)
	}
}

// A tool call is generated output like text: an answer that only called a
// tool used to file no first token and no speed.
func TestRelayMeasuresAToolCallOnlyAnswer(t *testing.T) {
	const wait, gap, chunks, output = 30 * time.Millisecond, 12 * time.Millisecond, 12, 45
	frames := []scriptedFrame{
		{0, `{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`},
		{wait, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":""}}]}}]}`},
	}
	for index := 2; index <= chunks; index++ {
		frames = append(frames, scriptedFrame{gap, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"and then and "}}]}}]}`})
	}
	frames = append(frames,
		scriptedFrame{0, fmt.Sprintf(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":%d}}`, output)},
		scriptedFrame{0, "[DONE]"},
	)
	pace := relayChatPace(t, scriptedStream(frames), `{"model":"glm-5.3","stream":true,"messages":[{"role":"user","content":"look it up"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`)
	if pace.FirstToken < wait || pace.FirstToken > 5*time.Second {
		t.Fatalf("first token %s, want the tool call's %s", pace.FirstToken, wait)
	}
	ceiling := float64(output-1) / (time.Duration(chunks-1) * gap).Seconds()
	if pace.TokensPerSecond <= 0 || pace.TokensPerSecond > ceiling {
		t.Fatalf("rate %.1f tok/s, want within (0, %.1f]", pace.TokensPerSecond, ceiling)
	}
}

// The model test reads the same stream the same way and reports the rate it
// files: one measurement, one number on both screens.
func TestProbeMeasuresTheDecodeRate(t *testing.T) {
	const wait, gap, chunks, tokens = 40 * time.Millisecond, 12 * time.Millisecond, 12, 61
	upstream := httptest.NewServer(numberStream(chunks, wait, gap, tokens))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	activity := activityapp.NewService(10)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer", Format: "chat"}},
		Credentials: &credentialSource{values: []string{"fixture-key"}},
		Activity:    relayactivity.NewRecorder(activity),
		Config:      Config{StreamIdleTimeout: time.Second},
	})
	report, err := server.Probe(context.Background(), "fixture", "fixture-model")
	if err != nil {
		t.Fatal(err)
	}
	ceiling := float64(tokens-1) / (time.Duration(chunks-1) * gap).Seconds()
	if report.ErrorCode != "" || report.OutputTokens != tokens || report.TTFTMs < float64(wait.Milliseconds()) || report.OutputTPS <= 0 || report.OutputTPS > ceiling {
		t.Fatalf("probe measurement = %+v, want rate within (0, %.1f]", report, ceiling)
	}
	row := activity.List(1)[0]
	if math.Abs(row.TTFTMS-report.TTFTMs) > .001 || math.Abs(row.TokensPerSecond-report.OutputTPS) > .001 {
		t.Fatalf("journal and model test disagree: report=%+v row=%+v", report, row)
	}
}
