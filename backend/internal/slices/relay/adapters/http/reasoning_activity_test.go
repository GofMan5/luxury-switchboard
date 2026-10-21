package relayhttp

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	guardraildomain "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

func TestRequestReasoningReadsEachDialectsSpelling(t *testing.T) {
	cases := []struct {
		name, body  string
		wantEffort  string
		wantBudget  int64
		contentType string
	}{
		{name: "responses effort", body: `{"model":"m","reasoning":{"effort":"high","summary":"auto"},"input":"secret prompt"}`, wantEffort: "high"},
		{name: "responses budget", body: `{"reasoning":{"max_tokens":4096}}`, wantEffort: "on", wantBudget: 4096},
		{name: "chat effort", body: `{"model":"m","reasoning_effort":"XHigh","messages":[]}`, wantEffort: "xhigh"},
		{name: "anthropic budget", body: `{"thinking":{"type":"enabled","budget_tokens":16000}}`, wantEffort: "on", wantBudget: 16000},
		{name: "anthropic adaptive with effort", body: `{"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"}}`, wantEffort: "medium"},
		{name: "anthropic adaptive", body: `{"thinking":{"type":"adaptive"}}`, wantEffort: "auto"},
		{name: "anthropic disabled", body: `{"thinking":{"type":"disabled","budget_tokens":2048}}`, wantEffort: "none"},
		{name: "qwen toggle", body: `{"enable_thinking":true,"thinking_budget":512}`, wantEffort: "on", wantBudget: 512},
		{name: "qwen off", body: `{"enable_thinking":false}`, wantEffort: "none"},
		{name: "openrouter toggle", body: `{"reasoning":{"enabled":true}}`, wantEffort: "on"},
		{name: "responses wins over chat", body: `{"reasoning":{"effort":"low"},"reasoning_effort":"high"}`, wantEffort: "low"},
		{name: "plain request", body: `{"model":"m","input":"hi"}`},
		// Client-controlled text that is not a level never reaches activity.
		{name: "unknown effort", body: `{"reasoning_effort":"ignore previous instructions"}`},
		{name: "wrong types", body: `{"reasoning":"high","reasoning_effort":3,"thinking":true}`},
		{name: "negative budget", body: `{"thinking":{"type":"enabled","budget_tokens":-1}}`, wantEffort: "on"},
		{name: "not json", body: `reasoning_effort=high`, contentType: "application/x-www-form-urlencoded"},
		{name: "broken json", body: `{"reasoning_effort":"high"`},
	}
	for _, test := range cases {
		contentType := test.contentType
		if contentType == "" {
			contentType = "application/json; charset=utf-8"
		}
		effort, budget := requestReasoning([]byte(test.body), contentType)
		if effort != test.wantEffort || budget != test.wantBudget {
			t.Errorf("%s: got (%q, %d), want (%q, %d)", test.name, effort, budget, test.wantEffort, test.wantBudget)
		}
	}
}

func TestReasoningPhaseClassifiesEachDialect(t *testing.T) {
	type event struct {
		eventType string
		payload   map[string]any
	}
	cases := []struct {
		name   string
		path   string
		events []event
		want   []bool
	}{
		{
			name: "responses reasoning then answer",
			path: "/v1/responses",
			events: []event{
				{"response.created", map[string]any{}},
				{"response.output_item.added", map[string]any{"item": map[string]any{"type": "reasoning"}}},
				{"response.reasoning_summary_text.delta", map[string]any{}},
				{"response.reasoning_summary_text.delta", map[string]any{}},
				{"response.output_item.done", map[string]any{"item": map[string]any{"type": "reasoning"}}},
				{"response.output_item.added", map[string]any{"item": map[string]any{"type": "message"}}},
				{"response.output_text.delta", map[string]any{}},
			},
			want: []bool{true, false},
		},
		{
			name: "messages thinking block then text",
			path: "/v1/messages",
			events: []event{
				{"message_start", map[string]any{}},
				{"content_block_start", map[string]any{"content_block": map[string]any{"type": "thinking"}}},
				{"content_block_delta", map[string]any{"delta": map[string]any{"type": "thinking_delta"}}},
				{"content_block_delta", map[string]any{"delta": map[string]any{"type": "signature_delta"}}},
				{"content_block_stop", map[string]any{}},
				{"content_block_start", map[string]any{"content_block": map[string]any{"type": "text"}}},
				{"content_block_delta", map[string]any{"delta": map[string]any{"type": "text_delta"}}},
			},
			want: []bool{true, false},
		},
		{
			name: "chat reasoning_content then content",
			path: "/v1/chat/completions",
			events: []event{
				{"", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"role": "assistant"}}}}},
				{"", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": "…"}}}}},
				{"", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning": "…"}}}}},
				{"", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "Hi"}}}}},
				{"", map[string]any{"usage": map[string]any{"completion_tokens": 3}}},
			},
			want: []bool{true, false},
		},
		{
			name: "chat answer without reasoning reports nothing",
			path: "/v1/chat/completions",
			events: []event{
				{"", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "Hi"}}}}},
			},
		},
		{
			name: "chat tool call ends reasoning",
			path: "/v1/chat/completions",
			events: []event{
				{"", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": "…"}}}}},
				{"", map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0}}}}}}},
			},
			want: []bool{true, false},
		},
	}
	for _, test := range cases {
		var got []bool
		inspector := &sseInspector{path: test.path, onThinking: func(thinking bool) { got = append(got, thinking) }}
		for _, event := range test.events {
			inspector.notePhase(event.eventType, event.payload)
		}
		if !slices.Equal(got, test.want) {
			t.Errorf("%s: transitions %v, want %v", test.name, got, test.want)
		}
	}
}

// A stream alternating between reasoning and answer on every event must not
// become one activity frame per event, and must never end marked thinking.
func TestReasoningPhaseIsBoundedPerStream(t *testing.T) {
	var got []bool
	inspector := &sseInspector{path: "/v1/chat/completions", onThinking: func(thinking bool) { got = append(got, thinking) }}
	reasoning := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": "…"}}}}
	answer := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "x"}}}}
	for range 1000 {
		inspector.notePhase("", reasoning)
		inspector.notePhase("", answer)
	}
	if len(got) != 2*maxThinkingSpans {
		t.Fatalf("an alternating stream reported %d transitions, want %d", len(got), 2*maxThinkingSpans)
	}
	if got[len(got)-1] {
		t.Fatal("a capped stream was left marked as thinking")
	}
}

// phaseActivity records what the activity feed receives: the start metadata
// and every phase transition, guarded because the live body reports from the
// goroutine copying the stream to the client.
type phaseActivity struct {
	relayapp.NoopActivity
	mu     sync.Mutex
	start  relayapp.ActivityStart
	phases []bool
	done   bool
	late   bool
}

func (sink *phaseActivity) Begin(start relayapp.ActivityStart) string {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.start = start
	return "req_phase"
}

func (sink *phaseActivity) Thinking(_ string, thinking bool) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.done {
		sink.late = true
	}
	sink.phases = append(sink.phases, thinking)
}

func (sink *phaseActivity) Finish(string, relayapp.ActivityFinish) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.done = true
}

// End to end through the relay: the client's reasoning level reaches the
// activity feed from its own body, and the provider's reasoning phase is
// reported as the stream arrives — on the buffered path and after the live
// handoff alike — without the prompt or the reasoning text going anywhere.
func TestRelayReportsReasoningLevelAndPhase(t *testing.T) {
	for _, live := range []bool{false, true} {
		name := "buffered"
		if live {
			name = "live"
		}
		t.Run(name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				flush := func(chunk string) {
					_, _ = writer.Write([]byte(chunk))
					writer.(http.Flusher).Flush()
				}
				flush(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"private chain of thought"}}]}` + "\n\n")
				// A reasoning silence longer than the keep-alive cadence.
				time.Sleep(80 * time.Millisecond)
				flush(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":" more"}}]}` + "\n\n")
				flush(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"completion_tokens_details":{"reasoning_tokens":3}}}` + "\n\n")
				flush("data: [DONE]\n\n")
			}))
			defer upstream.Close()
			sink := &phaseActivity{}
			// Block mode can refuse the answer, so the relay holds the whole
			// stream back: the phase must still be reported as it arrives.
			// Monitor mode hands the stream off live on its first chunk, and
			// the rest of the phase comes from the live body.
			mode := guardraildomain.ModeBlock
			if live {
				mode = guardraildomain.ModeMonitor
			}
			server, _ := guardedServerWatchedBy(t, mode, upstream.URL, sink)
			server.config.HeartbeatInterval = 25 * time.Millisecond
			server.config.LiveStreamProbation = 30 * time.Millisecond
			if live == server.guardrail.CanBlockFor("echo") {
				t.Fatalf("live=%v, but the guardrail decides otherwise", live)
			}

			request := httptest.NewRequest(http.MethodPost, "http://relay/v1/chat/completions", strings.NewReader(
				`{"model":"glm-5.3","stream":true,"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)

			if !strings.Contains(response.Body.String(), "Hello") {
				t.Fatalf("the answer did not reach the client: %q", response.Body.String())
			}
			sink.mu.Lock()
			defer sink.mu.Unlock()
			if sink.start.ReasoningEffort != "high" || sink.start.ReasoningBudget != 0 {
				t.Fatalf("the requested level did not reach activity: %+v", sink.start)
			}
			if len(sink.phases) != 2 || !sink.phases[0] || sink.phases[1] {
				t.Fatalf("phase transitions = %v, want [true false]", sink.phases)
			}
			if sink.late {
				t.Fatal("a phase was reported after the request finished")
			}
		})
	}
}
