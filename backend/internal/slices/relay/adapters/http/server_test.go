package relayhttp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

type fixedRoute struct{ route relayapp.Route }

func (source fixedRoute) Current(context.Context, string) (relayapp.Route, error) {
	return source.route, nil
}

func (source fixedRoute) Pinned(_ context.Context, _ string, upstreamModel string) (relayapp.Route, error) {
	route := source.route
	route.UpstreamModel = upstreamModel
	return route, nil
}

type credentialSource struct {
	mu       sync.Mutex
	values   []string
	proxyURL string
	calls    int
	outcomes []relayapp.AttemptOutcome
}

type delayedCredentialSource struct {
	delay time.Duration
	value string
}

type delayedCredentialLease string

func (lease delayedCredentialLease) Credential() relayapp.Credential {
	return relayapp.Credential{Value: string(lease)}
}
func (delayedCredentialLease) Finish(relayapp.AttemptOutcome) {}

func (source delayedCredentialSource) Acquire(ctx context.Context, _, _ string, waiting func()) (relayapp.CredentialLease, time.Duration, error) {
	waiting()
	started := time.Now()
	timer := time.NewTimer(source.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, time.Since(started), ctx.Err()
	case <-timer.C:
		return delayedCredentialLease(source.value), time.Since(started), nil
	}
}

func (source *credentialSource) Acquire(context.Context, string, string, func()) (relayapp.CredentialLease, time.Duration, error) {
	source.mu.Lock()
	value := source.values[min(source.calls, len(source.values)-1)]
	source.calls++
	source.mu.Unlock()
	return &credentialLease{source: source, value: value, proxyURL: source.proxyURL}, 0, nil
}

type credentialLease struct {
	source   *credentialSource
	value    string
	proxyURL string
}

func (lease *credentialLease) Credential() relayapp.Credential {
	return relayapp.Credential{Value: lease.value, ProxyURL: lease.proxyURL}
}

func TestRelayUsesProxyFromSelectedCredential(t *testing.T) {
	direct := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusTeapot)
	}))
	defer direct.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer proxied-key" {
			t.Fatal("credential was not applied through proxy")
		}
		writer.WriteHeader(http.StatusCreated)
	}))
	defer proxy.Close()
	parsed, _ := url.Parse(direct.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &credentialSource{values: []string{"proxied-key"}, proxyURL: proxy.URL},
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("per-key proxy was ignored: status=%d", response.Code)
	}
}

func TestProxyClientCacheIsBounded(t *testing.T) {
	server := NewServer("127.0.0.1:0", Dependencies{})
	for index := range maxProxyClients + 1 {
		if _, err := server.clientForProxy(fmt.Sprintf("http://127.0.0.1:%d", 10_000+index)); err != nil {
			t.Fatal(err)
		}
	}
	if len(server.proxyClients) > maxProxyClients {
		t.Fatalf("proxy client cache grew without a bound: %d", len(server.proxyClients))
	}
}

func (lease *credentialLease) Finish(outcome relayapp.AttemptOutcome) {
	lease.source.mu.Lock()
	lease.source.outcomes = append(lease.source.outcomes, outcome)
	lease.source.mu.Unlock()
}

func TestRelayRewritesAuthAndExtendsExistingCacheTTL(t *testing.T) {
	var receivedAuth string
	var receivedEncoding string
	var receivedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		receivedAuth = request.Header.Get("Authorization")
		receivedEncoding = request.Header.Get("Accept-Encoding")
		body, _ := io.ReadAll(request.Body)
		receivedBody = string(body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL + "/v1")
	credentials := &credentialSource{values: []string{"configured-key"}}
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: fixedRoute{route: relayapp.Route{
			ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer", CacheTTL: time.Hour,
		}},
		Credentials: credentials,
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{"model":"gpt-test","input":[{"cache_control":{"type":"ephemeral"}}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer client-key")
	request.Header.Set("Accept-Encoding", "gzip, br")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	if receivedAuth != "Bearer configured-key" {
		t.Fatalf("upstream auth was not replaced")
	}
	if receivedEncoding != "identity" {
		t.Fatalf("compressed upstream response bypassed stream inspection: %q", receivedEncoding)
	}
	if !strings.Contains(receivedBody, `"ttl":"1h"`) {
		t.Fatalf("cache TTL was not extended: %s", receivedBody)
	}
}

func TestAutoAuthUsesAnthropicHeaderOnlyForMessages(t *testing.T) {
	seen := make(chan http.Header, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen <- request.Header.Clone()
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "auto"}}, Credentials: &credentialSource{values: []string{"configured-key"}}})
	for _, path := range []string{"/v1/messages", "/v1/responses"} {
		request := httptest.NewRequest(http.MethodPost, "http://relay"+path, strings.NewReader(`{"model":"gpt-test"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d", path, response.Code)
		}
	}
	messages, responses := <-seen, <-seen
	if messages.Get("x-api-key") != "configured-key" || messages.Get("Authorization") != "" || responses.Get("Authorization") != "Bearer configured-key" || responses.Get("x-api-key") != "" {
		t.Fatalf("auto auth mismatch: messages=%v responses=%v", messages, responses)
	}
}

func TestRateLimitRotatesCredentialWithoutLeaking429(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		if request.Header.Get("Authorization") == "Bearer first-key" {
			writer.Header().Set("Retry-After", "0.001")
			writer.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writer.WriteHeader(http.StatusCreated)
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	credentials := &credentialSource{values: []string{"first-key", "second-key"}}
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: credentials,
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || attempts != 2 {
		t.Fatalf("rate limit was exposed or not retried: status=%d attempts=%d", response.Code, attempts)
	}
	if len(credentials.outcomes) != 2 || credentials.outcomes[0].Kind != relayapp.AttemptRateLimited || credentials.outcomes[1].Kind != relayapp.AttemptSuccess {
		t.Fatalf("unexpected outcomes: %+v", credentials.outcomes)
	}
}

func TestDispatchAttemptLimitBoundsHealthProbe(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.WriteHeader(http.StatusTooManyRequests)
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &credentialSource{values: []string{"key"}},
	})
	response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		Method: http.MethodPost, Path: "/v1/responses", ProviderID: "echo", UpstreamModel: "gpt-test",
		Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"model":"gpt-test"}`), AttemptLimit: 1,
	})
	if err != nil || response.Status != http.StatusTooManyRequests || attempts != 1 {
		t.Fatalf("bounded dispatch failed: status=%d attempts=%d err=%v", response.Status, attempts, err)
	}
}

func TestRouteSwitchCancelsPinnedTunnelDispatch(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
	}))
	defer upstream.Close()
	defer close(release)
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &credentialSource{values: []string{"key"}},
	})
	done := make(chan error, 1)
	go func() {
		_, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{Method: http.MethodPost, Path: "/v1/responses", ProviderID: "echo", UpstreamModel: "gpt-test", Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"model":"gpt-test"}`)})
		done <- err
	}()
	<-started
	server.CancelActive()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("dispatch returned the wrong cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("route switch left tunnel dispatch running")
	}
}

func TestPermanentErrorIsBoundedAndSanitized(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = writer.Write([]byte(`{"error":"private provider detail"}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &credentialSource{values: []string{"key"}},
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || attempts != 2 {
		t.Fatalf("unexpected bounded result: status=%d attempts=%d", response.Code, attempts)
	}
	if strings.Contains(response.Body.String(), "private provider detail") {
		t.Fatal("provider detail leaked through permanent error")
	}
}

func TestStreamingPermanentErrorEndsWithDialectTerminal(t *testing.T) {
	tests := []struct {
		path     string
		body     string
		terminal string
	}{
		{"/v1/responses", `{"model":"gpt-test","stream":true}`, "response.failed"},
		{"/v1/chat/completions", `{"model":"gpt-test","stream":true}`, "[DONE]"},
		{"/v1/messages", `{"model":"gpt-test","stream":true}`, `"type":"error"`},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = writer.Write([]byte(`{"error":"private provider detail"}`))
			}))
			defer upstream.Close()
			parsed, _ := url.Parse(upstream.URL)
			server := NewServer("127.0.0.1:0", Dependencies{
				Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
				Credentials: &credentialSource{values: []string{"key"}},
				Config:      Config{PermanentAttempts: 1},
			})
			request := httptest.NewRequest(http.MethodPost, "http://relay"+test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.terminal) || strings.Contains(response.Body.String(), "private provider detail") {
				t.Fatalf("stream did not finish safely: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestBalanceErrorsIncludePaymentAndLocalizedMarkers(t *testing.T) {
	if !balanceUnavailable(http.StatusPaymentRequired, nil) || !balanceUnavailable(http.StatusForbidden, []byte(`{"error":"Недостаточный баланс"}`)) || balanceUnavailable(http.StatusBadRequest, []byte(`{"error":"bad prompt"}`)) {
		t.Fatal("balance error classification is incomplete")
	}
}

func TestBalanceErrorRotatesKeyBeforeAuthenticationCooldown(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") == "Bearer empty-key" {
			writer.WriteHeader(http.StatusForbidden)
			_, _ = writer.Write([]byte(`{"error":"insufficient_balance"}`))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	credentials := &credentialSource{values: []string{"empty-key", "paid-key"}}
	server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}}, Credentials: credentials})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(credentials.outcomes) < 2 || credentials.outcomes[0].Kind != relayapp.AttemptBalanceExhausted {
		t.Fatalf("balance key was misclassified: status=%d outcomes=%+v", response.Code, credentials.outcomes)
	}
}

func TestResponsesStreamRetriesTruncatedAttemptBeforeForwarding(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.Header().Set("Content-Type", "text/event-stream")
		if attempts == 1 {
			_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"discard\"}\n\n"))
			return
		}
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"error\":null,\"incomplete_details\":null}}\n\n"))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &credentialSource{values: []string{"key"}},
		Config:      Config{RetryBase: time.Millisecond, RetryMax: 2 * time.Millisecond, StreamIdleTimeout: time.Second, MaxRequestBytes: 1024 * 1024, PermanentAttempts: 2},
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{"model":"gpt-test","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if attempts != 2 || response.Code != http.StatusOK {
		t.Fatalf("unexpected stream result: attempts=%d status=%d", attempts, response.Code)
	}
	if strings.Contains(response.Body.String(), "discard") || !strings.Contains(response.Body.String(), "response.completed") {
		t.Fatalf("partial attempt leaked or terminal missing: %s", response.Body.String())
	}
}

func TestStreamHeartbeatCoversCredentialQueue(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"error\":null,\"incomplete_details\":null}}\n\n"))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: delayedCredentialSource{delay: 25 * time.Millisecond, value: "key"},
		Config:      Config{HeartbeatInterval: 5 * time.Millisecond, StreamIdleTimeout: time.Second},
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{"model":"gpt-test","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if !strings.Contains(response.Body.String(), ": switchboard keep-alive") || !strings.Contains(response.Body.String(), "response.completed") {
		t.Fatalf("stream was idle while queued or lost its terminal event: %s", response.Body.String())
	}
}

func TestChatStreamRetriesUntilDoneMarker(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.Header().Set("Content-Type", "text/event-stream")
		if attempts == 1 {
			_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"discard\"}}]}\n\n"))
			return
		}
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &credentialSource{values: []string{"key"}},
		Config:      Config{RetryBase: time.Millisecond, RetryMax: 2 * time.Millisecond, StreamIdleTimeout: time.Second, MaxRequestBytes: 1024 * 1024},
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if attempts != 2 || strings.Contains(response.Body.String(), "discard") || !strings.Contains(response.Body.String(), "[DONE]") {
		t.Fatalf("chat stream was not retried safely: attempts=%d body=%s", attempts, response.Body.String())
	}
}

func TestJSONBodyDisconnectRetriesBeforeClientCommit(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		if attempts == 1 {
			connection, _, err := writer.(http.Hijacker).Hijack()
			if err != nil {
				t.Fatal(err)
			}
			_, _ = fmt.Fprint(connection, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"partial\":")
			_ = connection.Close()
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &credentialSource{values: []string{"key"}},
		Config:      Config{RetryBase: time.Millisecond, RetryMax: 2 * time.Millisecond, StreamIdleTimeout: time.Second, MaxRequestBytes: 1024 * 1024},
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/chat/completions", strings.NewReader(`{"model":"gpt-test"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if attempts != 2 || response.Code != http.StatusOK || response.Body.String() != `{"ok":true}` {
		t.Fatalf("truncated JSON was committed: attempts=%d status=%d body=%s", attempts, response.Code, response.Body.String())
	}
}

func TestResponsesIncompleteTerminalIsNotRetried(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n"))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &credentialSource{values: []string{"key"}},
		Config:      Config{StreamIdleTimeout: time.Second, MaxRequestBytes: 1024 * 1024},
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{"model":"gpt-test","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if attempts != 1 || !strings.Contains(response.Body.String(), "response.incomplete") {
		t.Fatalf("valid incomplete terminal was retried or lost: attempts=%d body=%s", attempts, response.Body.String())
	}
}

func TestSSEInspectorHandlesFragmentedMultilineEvent(t *testing.T) {
	inspector := &sseInspector{}
	parts := []string{
		"data: {\"type\":\"response.completed\",\n",
		"data: \"response\":{\"status\":\"completed\",\"error\":null,\"incomplete_details\":null}}\n",
		"\n",
	}
	terminal := ""
	for _, part := range parts {
		terminal = inspector.Feed([]byte(part))
	}
	if terminal != "response.completed" {
		t.Fatalf("unexpected terminal %q", terminal)
	}
}

func TestSSEUsageKeepsCacheAndReasoningAsSubsets(t *testing.T) {
	inspector := &sseInspector{}
	event := `data: {"type":"response.completed","response":{"status":"completed","error":null,"incomplete_details":null,"usage":{"input_tokens":100,"output_tokens":40,"total_tokens":140,"input_tokens_details":{"cached_tokens":30},"output_tokens_details":{"reasoning_tokens":10}}}}` + "\n\n"
	if terminal := inspector.Feed([]byte(event)); terminal != "response.completed" {
		t.Fatalf("unexpected terminal %q", terminal)
	}
	usage := inspector.usage
	if usage.InputTokens != 100 || usage.OutputTokens != 40 || usage.CachedTokens != 30 || usage.ReasoningTokens != 10 || usage.TotalTokens != 140 {
		t.Fatalf("usage subsets were inflated: %+v", usage)
	}
}

func TestSSEUsageNormalizesAnthropicCacheFields(t *testing.T) {
	inspector := &sseInspector{}
	event := `data: {"type":"message_delta","usage":{"input_tokens":10,"cache_read_input_tokens":50,"cache_creation_input_tokens":20,"output_tokens":5}}` + "\n\n"
	inspector.Feed([]byte(event))
	usage := inspector.usage
	if usage.InputTokens != 80 || usage.CachedTokens != 50 || usage.OutputTokens != 5 || usage.TotalTokens != 85 {
		t.Fatalf("anthropic cache accounting is wrong: %+v", usage)
	}
}

func TestJSONUsageDoesNotDoubleCountCachedOrReasoningSubsets(t *testing.T) {
	usage := usageFromJSON([]byte(`{"usage":{"input_tokens":100,"output_tokens":40,"total_tokens":140,"input_tokens_details":{"cached_tokens":30},"output_tokens_details":{"reasoning_tokens":10}}}`))
	if usage.InputTokens != 100 || usage.OutputTokens != 40 || usage.CachedTokens != 30 || usage.ReasoningTokens != 10 || usage.TotalTokens != 140 {
		t.Fatalf("JSON usage subsets were inflated: %+v", usage)
	}
}

func TestMultipartImageRequestModelIsReadAndRewritten(t *testing.T) {
	buffer := bytes.Buffer{}
	writer := multipart.NewWriter(&buffer)
	if err := writer.WriteField("model", "public-image"); err != nil {
		t.Fatal(err)
	}
	file, err := writer.CreateFormFile("image", "fixture.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("fixture-image-bytes"))
	contentType := writer.FormDataContentType()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if got := requestModel(buffer.Bytes(), contentType); got != "public-image" {
		t.Fatalf("multipart model was not read: %q", got)
	}
	rewritten := rewriteRequestModel(buffer.Bytes(), contentType, "private-image")
	if got := requestModel(rewritten, contentType); got != "private-image" || !bytes.Contains(rewritten, []byte("fixture-image-bytes")) {
		t.Fatalf("multipart model/file rewrite failed: model=%q", got)
	}
}

func TestCodexImagesEndpointBridgesResponsesImageTool(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if request.URL.Path != "/v1/responses" || !strings.Contains(string(body), `"model":"gpt-5.6-sol"`) || !strings.Contains(string(body), `"image_generation"`) {
			t.Fatalf("image request was not bridged: path=%s body=%s", request.URL.Path, body)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1780000002,\"status\":\"completed\",\"error\":null,\"output\":[{\"type\":\"image_generation_call\",\"result\":\"YWJjZA==\"}]}}\n\n"))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer", UpstreamModel: "gpt-5.6-sol"}},
		Credentials: &credentialSource{values: []string{"key"}},
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/images/generations", strings.NewReader(`{"model":"gpt-image-2","prompt":"blue robot","quality":"high"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" || response.Body.String() != `{"created":1780000002,"data":[{"b64_json":"YWJjZA=="}]}` {
		t.Fatalf("unexpected images response: status=%d type=%s body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}

func TestJoinPathDoesNotDuplicateVersionPrefix(t *testing.T) {
	if value := joinPath("/v1", "/v1/models"); value != "/v1/models" {
		t.Fatalf("unexpected path %q", value)
	}
	if value := joinPath("/gateway", "/v1/models"); value != "/gateway/v1/models" {
		t.Fatalf("unexpected path %q", value)
	}
}
