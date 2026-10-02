package relayhttp

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	guardrailrelay "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/adapters/relay"
	guardrailruleset "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/adapters/ruleset"
	guardrailapp "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/application"
	guardraildomain "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

// The Codex-on-GLM pattern, measured in the field: every request paid one
// probe 400 before its 200 — Codex asks for reasoning_effort "medium", GLM's
// edge accepts low/high/max and says so in the complaint. The repair mapped
// the effort and the request succeeded, so nothing looked broken while every
// single request spent an extra round trip. The memo learns the provider's
// accepted levels from the first request's complaint and maps the effort
// before sending, from the second request on.
func TestTheSecondRequestSendsTheLearnedShape(t *testing.T) {
	const chatStream = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"glm\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":40,\"total_tokens\":140}}\n\n" +
		"data: [DONE]\n\n"
	var requests, effort400s atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		body := make([]byte, 8192)
		count, _ := request.Body.Read(body)
		text := string(body[:count])
		if strings.Contains(text, `"reasoning_effort":"medium"`) {
			effort400s.Add(1)
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"message":"'reasoning_effort' must be one of: 'low', 'high', 'max'"}}`))
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = writer.Write([]byte(chatStream))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	sink := &recordingActivity{}
	engine, err := guardraildomain.NewEngine(guardrailruleset.RulesJSON, guardrailruleset.BlocklistJSON)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	inspector, err := guardrailapp.NewInspector(engine, guardraildomain.ModeMonitor, 16)
	if err != nil {
		t.Fatalf("inspector: %v", err)
	}
	server := NewServer("127.0.0.1:0", Dependencies{
		// Format "chat": the request is translated to Chat Completions, the
		// way a chat-only provider is served — the translation is what turns
		// reasoning.effort into the reasoning_effort the probe rejects.
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "edge", ProviderName: "Edge", BaseURL: parsed, AuthMode: "bearer", Format: "chat"}},
		Credentials: &credentialSource{values: []string{"key"}},
		Guardrail:   guardrailrelay.New(inspector),
		Activity:    sink,
		Config:      Config{RetryBase: time.Millisecond, RetryMax: 5 * time.Millisecond, StreamIdleTimeout: time.Second},
	})

	call := func() {
		request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(
			`{"model":"glm-5.3","stream":true,"instructions":"be brief","input":"hi","reasoning":{"effort":"medium"}}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("the request was not delivered: status=%d body=%s", response.Code, response.Body.String())
		}
	}

	// The first request pays for the probe: the effort 400, then the repair.
	call()
	if effort400s.Load() != 1 {
		t.Fatalf("the fixture did not exercise the probe: effort=%d", effort400s.Load())
	}
	if requests.Load() != 2 {
		t.Fatalf("the first request took %d attempts, want 2 (probe, success)", requests.Load())
	}

	// The second request sends the shape the provider already accepted: no
	// probe, no extra round trip, one attempt.
	call()
	if effort400s.Load() != 1 {
		t.Fatalf("the second request re-paid the probe the memo exists to prepay: effort=%d", effort400s.Load())
	}
	if requests.Load() != 3 {
		t.Fatalf("the second request took %d attempts, want 1", requests.Load()-2)
	}

	// Usage still flows — the memo rewrites the request, never the accounting.
	usage := sink.finish.Usage
	if usage.InputTokens != 100 || usage.OutputTokens != 40 {
		t.Fatalf("usage was lost through the learned shape: %+v", usage)
	}
}

// A failed request teaches nothing: a provider that is simply down must not
// have its parameters "repaired" out of the next request — the learning
// happens on the request that finally succeeded, not the ones that did not.
func TestAFailingRequestTeachesNothing(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if calls.Load() == 1 {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"message":"unsupported parameter: 'temperature'"}}`))
			return
		}
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"error":{"message":"overloaded"}}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	sink := &recordingActivity{}
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "edge", ProviderName: "Edge", BaseURL: parsed, AuthMode: "bearer", Format: "chat"}},
		Credentials: &credentialSource{values: []string{"key"}},
		Activity:    sink,
		Config:      Config{RetryBase: time.Millisecond, RetryMax: 5 * time.Millisecond, StreamIdleTimeout: time.Second},
	})

	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(
		`{"model":"glm-5.3","stream":true,"input":"hi","temperature":0.5}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	// The stream was committed, so the client status is the 200 its headers
	// already carried; the failure rides the stream's own terminal event and
	// the activity's error code.
	if sink.finish.ErrorCode == "" {
		t.Fatal("the overloaded provider was reported as a success")
	}

	// Nothing was learned: the memo is empty, so a later body still carries
	// its temperature untouched.
	body := server.repairs.apply("edge", []byte(`{"model":"glm-5.3","temperature":0.5,"messages":[]}`))
	if !strings.Contains(string(body), `"temperature":0.5`) {
		t.Fatalf("a failed request taught the memo to strip temperature: %s", body)
	}
}

// The memo's rewrites are the repairs' own transformations, nothing more:
// the developer role becomes system, max_tokens is renamed, refused
// parameters disappear, and an effort the provider does not host maps to the
// nearest accepted level at or below the ask.
func TestTheMemoRewritesExactlyWhatTheRepairsDo(t *testing.T) {
	memo := newRepairMemo()
	memo.merge("edge", providerAdjustments{
		developerRole: true, maxTokens: true,
		dropFields: []string{"temperature"},
		efforts:    []string{"reasoning_effort", "low", "high", "max"},
	})
	body := memo.apply("edge", []byte(`{"model":"glm","max_tokens":512,"temperature":0.7,"reasoning_effort":"medium",`+
		`"messages":[{"role":"developer","content":"be brief"},{"role":"user","content":"hi"}]}`))
	text := string(body)
	for _, unwanted := range []string{`"max_tokens"`, `"temperature"`, `"developer"`, `"reasoning_effort":"medium"`} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("the memo left %q in the request: %s", unwanted, text)
		}
	}
	for _, wanted := range []string{`"max_completion_tokens":512`, `"role":"system"`, `"reasoning_effort":"low"`} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("the memo lost %q from the request: %s", wanted, text)
		}
	}

	// A body with none of the learned fields is byte-identical.
	untouched := `{"model":"glm","messages":[{"role":"user","content":"hi"}]}`
	if got := string(memo.apply("edge", []byte(untouched))); got != untouched {
		t.Fatalf("a body without learned fields was rewritten: %s", got)
	}
	// An unknown provider is untouched too.
	if got := string(memo.apply("other", []byte(`{"max_tokens":512}`))); got != `{"max_tokens":512}` {
		t.Fatalf("another provider's body was rewritten: %s", got)
	}
}
