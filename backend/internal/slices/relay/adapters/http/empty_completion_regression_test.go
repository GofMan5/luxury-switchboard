package relayhttp

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

// Measured in the field: a provider answered a request it could not serve
// with a well-framed stream that carried no answer — a role chunk, a
// finish_reason "stop" chunk over zero tokens, [DONE] — twelve seconds in.
// The relay stamped it completed, the client read an empty answer it could
// only re-request on its own retry loop, and the ladder never got a chance
// to ask a different attempt. An answer-less completion is a retryable
// failure: nothing usable was delivered, so the retry is invisible.
func TestAnEmptyChatCompletionIsRetriedNotServedAsSuccess(t *testing.T) {
	const empty = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	var attempts int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			_, _ = io.WriteString(w, empty)
			return
		}
		_, _ = io.WriteString(w, "data: {\"id\":\"c2\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"pong\"},\"finish_reason\":null}]}\n\n"+
			"data: {\"id\":\"c2\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	for _, probation := range []time.Duration{0, time.Nanosecond} {
		t.Run(fmt.Sprintf("probation=%v", probation), func(t *testing.T) {
			atomic.StoreInt32(&attempts, 0)
			source := &countingCredentialSource{credentialSource: credentialSource{values: []string{"fixture-secret"}}, keys: 1}
			server := NewServer("127.0.0.1:0", Dependencies{
				Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: base, AuthMode: "bearer"}},
				Credentials: source,
				Config:      Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 3, LiveStreamProbation: probation, StreamIdleTimeout: time.Second},
			})
			relay := httptest.NewServer(server)
			defer relay.Close()
			request, _ := http.NewRequest("POST", relay.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
			request.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("relay request: %v", err)
			}
			wire, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if got := atomic.LoadInt32(&attempts); got != 2 {
				t.Fatalf("the empty completion was not retried once: attempts=%d\n%s", got, wire)
			}
			if !strings.Contains(string(wire), "pong") {
				t.Fatalf("the retry's answer did not reach the client:\n%s", wire)
			}
			if len(source.outcomes) < 2 || source.outcomes[0].Kind != relayapp.AttemptRequestError || source.outcomes[1].Kind != relayapp.AttemptSuccess {
				t.Fatalf("the empty attempt was not filed as a request error: %+v", source.outcomes)
			}
		})
	}
}

// When every attempt comes back empty the client must learn the stream
// failed, in the dialect's own grammar, instead of receiving a tidy empty
// completion — and the attempt budget is the ladder's, not a private one.
func TestAnAlwaysEmptyChatCompletionEndsInFailureNotAnEmptyAnswer(t *testing.T) {
	const empty = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	var attempts int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		_, _ = io.WriteString(w, empty)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: base, AuthMode: "bearer"}},
		Credentials: &credentialSource{values: []string{"fixture-secret"}},
		Config:      Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 2, LiveStreamProbation: time.Nanosecond},
	})
	relay := httptest.NewServer(server)
	defer relay.Close()
	request, _ := http.NewRequest("POST", relay.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("relay request: %v", err)
	}
	wire, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if got := atomic.LoadInt32(&attempts); got < 2 {
		t.Fatalf("the empty completion was served without a single retry: attempts=%d", got)
	}
	if strings.Contains(string(wire), `"finish_reason":"stop"`) && !strings.Contains(string(wire), `"finish_reason":"error"`) {
		t.Fatalf("the client received an empty answer stamped as a clean completion:\n%s", wire)
	}
}

// The answer a client can use still stands: text, a tool call, a refusal and
// reasoning each mark the stream as delivered, and none of them is retried.
func TestADeliveredChatAnswerIsNeverRetried(t *testing.T) {
	for name, body := range map[string]string{
		"text":                  "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
		"tool call":             "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"f\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n",
		"refusal":               "data: {\"choices\":[{\"index\":0,\"delta\":{\"refusal\":\"no\"},\"finish_reason\":\"content_filter\"}]}\n\ndata: [DONE]\n\n",
		"reasoning":             "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"thinking\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n",
		"buffered":              "data: {\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
		"role only":             "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n",
		"usage only":            "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0,\"total_tokens\":0}}\n\ndata: [DONE]\n\n",
		"zero completion usage": "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":0,\"total_tokens\":9}}\n\ndata: [DONE]\n\n",
		"finish only":           "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			inspector := &sseInspector{path: "/v1/chat/completions"}
			inspector.Feed([]byte(body))
			inspector.Finish()
			// A role delta or a zero-completion usage report shows the provider
			// was answering and gave nothing: retryable. A lone finish_reason, or a
			// usage report with no prompt behind it, is framing only.
			wantRetry := name == "role only" || name == "zero completion usage"
			if got := inspector.retryableFailure(); got != wantRetry {
				t.Fatalf("retryable=%v want %v (output=%v finished=%v terminal=%q)", got, wantRetry, inspector.output, inspector.finished, inspector.terminal)
			}
		})
	}
}

// The Anthropic dialect does not report its text to the output flag, so it
// keeps its own rule: only an explicit failed response retries, and a normal
// message_stop stream never does.
func TestTheAnthropicDialectKeepsItsOwnRule(t *testing.T) {
	inspector := &sseInspector{path: "/v1/messages"}
	inspector.Feed([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	inspector.Finish()
	if inspector.retryableFailure() {
		t.Fatal("a healthy Anthropic stream became retryable")
	}
	inspector = &sseInspector{path: "/v1/messages"}
	inspector.Feed([]byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"x\"}}\n\n"))
	inspector.Finish()
	if !inspector.retryableFailure() {
		t.Fatal("a failed Anthropic response stopped being retryable")
	}
}
