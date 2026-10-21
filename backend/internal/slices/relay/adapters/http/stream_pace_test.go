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

func TestTokenEventCountsGeneratedTextOnly(t *testing.T) {
	for _, tc := range []struct {
		name, path, eventType string
		payload               map[string]any
		want                  bool
	}{
		{"chat content", "/v1/chat/completions", "", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "1 2"}}}}, true},
		{"chat reasoning_content", "/v1/chat/completions", "", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": "hm"}}}}, true},
		{"chat reasoning", "/v1/chat/completions", "", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning": "hm"}}}}, true},
		{"chat reasoning object", "/v1/chat/completions", "", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning": map[string]any{"text": "hm"}}}}}, true},
		{"chat empty reasoning object", "/v1/chat/completions", "", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning": map[string]any{}}}}}, false},
		{"chat whitespace", "/v1/chat/completions", "", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": " \n"}}}}, false},
		{"chat role only", "/v1/chat/completions", "", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"role": "assistant"}}}}, false},
		{"chat usage frame", "/v1/chat/completions", "", map[string]any{"choices": []any{}, "usage": map[string]any{"completion_tokens": 9.0}}, false},
		{"completions text", "/v1/completions", "", map[string]any{"choices": []any{map[string]any{"text": "1"}}}, true},
		{"responses text", "/v1/responses", "response.output_text.delta", map[string]any{"delta": "1"}, true},
		{"responses reasoning", "/v1/responses", "response.reasoning_summary_text.delta", map[string]any{"delta": "hm"}, true},
		{"responses tool arguments", "/v1/responses", "response.function_call_arguments.delta", map[string]any{"delta": "{"}, false},
		{"responses lifecycle", "/v1/responses", "response.created", map[string]any{}, false},
		{"messages text", "/v1/messages", "content_block_delta", map[string]any{"delta": map[string]any{"type": "text_delta", "text": "1"}}, true},
		{"messages thinking", "/v1/messages", "content_block_delta", map[string]any{"delta": map[string]any{"type": "thinking_delta", "thinking": "hm"}}, true},
		{"messages signature", "/v1/messages", "content_block_delta", map[string]any{"delta": map[string]any{"type": "signature_delta", "signature": "abc"}}, false},
		{"messages stop", "/v1/messages", "message_stop", map[string]any{}, false},
	} {
		if got := tokenEvent(tc.path, tc.eventType, tc.payload); got != tc.want {
			t.Errorf("%s: tokenEvent = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPaceSettlesOnlyAMeasuredAttempt(t *testing.T) {
	serving := &attemptMetadata{firstToken: time.Now(), tokensPerSecond: 99}
	(&paceMeter{}).settle(serving, 100)
	if !serving.firstToken.IsZero() || serving.tokensPerSecond != 0 {
		t.Fatalf("an attempt without tokens kept the replaced attempt's pace: %+v", serving)
	}
	started := time.Now()
	meter := &paceMeter{first: started.Add(-time.Second), chunks: 20}
	meter.settle(serving, 101)
	if serving.firstToken != meter.first || serving.tokensPerSecond <= 0 || serving.tokensPerSecond > 100 {
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
			flush(fmt.Sprintf(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"%d "}}]}`, index) + "\n\n")
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
