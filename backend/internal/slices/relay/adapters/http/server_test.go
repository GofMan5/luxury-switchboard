package relayhttp

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
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

func TestDefaultTransportNeverUsesAmbientProxy(t *testing.T) {
	server := NewServer("127.0.0.1:0", Dependencies{})
	if server.transport.Proxy != nil {
		t.Fatal("direct credentials inherited an ambient process proxy")
	}
}

func TestProviderRedirectCannotMoveCredentialsToAnotherOrigin(t *testing.T) {
	reached := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached <- struct{}{} }))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &credentialSource{values: []string{"secret"}},
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	select {
	case <-reached:
		t.Fatal("provider redirect escaped its configured origin")
	default:
	}
	if response.Code != http.StatusBadGateway {
		t.Fatalf("provider redirect returned status %d", response.Code)
	}
}

func TestDispatchCarriesTheExactSelectedCredentialMarkers(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer proxy.Close()
	parsed, _ := url.Parse("http://provider.invalid")
	proxyURL := strings.Replace(proxy.URL, "http://", "http://proxy-user:proxy-pass@", 1)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &credentialSource{
			values: []string{"selected-secret"}, proxyURL: proxyURL,
		},
	})
	response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		Method: http.MethodPost, Path: "/v1/responses", ProviderID: "echo", UpstreamModel: "gpt-test",
		Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"model":"gpt-test"}`), AttemptLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(response.SensitiveMarkers, "\n")
	for _, marker := range []string{"selected-secret", "127.0.0.1", "proxy-user", "proxy-pass"} {
		if !strings.Contains(joined, marker) {
			t.Fatalf("selected credential marker %q was lost: %q", marker, joined)
		}
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
		writer.Header().Set("Content-Encoding", "gzip")
		compressed := gzip.NewWriter(writer)
		_, _ = compressed.Write([]byte(`{"ok":true}`))
		_ = compressed.Close()
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
	if receivedEncoding != "gzip" || response.Header().Get("Content-Encoding") != "" || response.Body.String() != `{"ok":true}` {
		t.Fatalf("upstream compression was not normalized safely: request=%q response=%q body=%s", receivedEncoding, response.Header().Get("Content-Encoding"), response.Body.String())
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

func TestAutoAuthRecognizesMessagesBelowProviderBasePath(t *testing.T) {
	seen := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen <- request.Header.Clone()
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL + "/gateway")
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "auto"}},
		Credentials: &credentialSource{values: []string{"configured-key"}},
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/messages", strings.NewReader(`{"model":"claude-test"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	headers := <-seen
	if headers.Get("x-api-key") != "configured-key" || headers.Get("Authorization") != "" {
		t.Fatalf("nested Anthropic route used the wrong auth: %v", headers)
	}
}

func TestExplicitProviderDialectOverridesAutoAuthPathGuess(t *testing.T) {
	base, _ := url.Parse("https://provider.example/v1")
	responses := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", nil)
	anthropic, err := buildUpstreamRequest(context.Background(), responses, nil, relayapp.Route{BaseURL: base, AuthMode: "auto", Dialect: "anthropic"}, "secret")
	if err != nil || anthropic.Header.Get("x-api-key") != "secret" || anthropic.Header.Get("Authorization") != "" {
		t.Fatalf("explicit Anthropic dialect used the wrong auth: %v err=%v", anthropic.Header, err)
	}
	messages := httptest.NewRequest(http.MethodPost, "http://relay/v1/messages", nil)
	openai, err := buildUpstreamRequest(context.Background(), messages, nil, relayapp.Route{BaseURL: base, AuthMode: "auto", Dialect: "openai"}, "secret")
	if err != nil || openai.Header.Get("Authorization") != "Bearer secret" || openai.Header.Get("x-api-key") != "" {
		t.Fatalf("explicit OpenAI dialect used the wrong auth: %v err=%v", openai.Header, err)
	}
}

func TestRateLimitRotatesCredentialWithoutLeaking429(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		if request.Header.Get("Authorization") == "Bearer first-key" {
			writer.Header().Set("Retry-After", "30")
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
	started := time.Now()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || attempts != 2 {
		t.Fatalf("rate limit was exposed or not retried: status=%d attempts=%d", response.Code, attempts)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("eligible fallback key was delayed by Retry-After: %s", elapsed)
	}
	if len(credentials.outcomes) != 2 || credentials.outcomes[0].Kind != relayapp.AttemptRateLimited || credentials.outcomes[1].Kind != relayapp.AttemptSuccess {
		t.Fatalf("unexpected outcomes: %+v", credentials.outcomes)
	}
}

func TestPassthroughCredentialFailuresBackOffWithoutBusyLoop(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			attempts := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				attempts++
				if attempts == 1 {
					writer.Header().Set("Retry-After", "0.04")
					writer.WriteHeader(status)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"ok":true}`))
			}))
			defer upstream.Close()
			parsed, _ := url.Parse(upstream.URL)
			server := NewServer("127.0.0.1:0", Dependencies{
				Routes: fixedRoute{route: relayapp.Route{ProviderID: "local", BaseURL: parsed, AuthMode: "passthrough"}},
				Config: Config{RetryBase: 40 * time.Millisecond, RetryMax: time.Second},
			})
			started := time.Now()
			response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
				Method: http.MethodPost, Path: "/v1/responses", ProviderID: "local", UpstreamModel: "gpt-test",
				Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"model":"gpt-test"}`), AttemptLimit: 2,
			})
			if err != nil || response.Status != http.StatusOK || attempts != 2 {
				t.Fatalf("passthrough retry failed: status=%d attempts=%d err=%v", response.Status, attempts, err)
			}
			if elapsed := time.Since(started); elapsed < 30*time.Millisecond {
				t.Fatalf("passthrough retry did not back off: %s", elapsed)
			}
		})
	}
}

func TestPassthroughAuthenticationFailureStopsAfterPermanentAttempts(t *testing.T) {
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":"invalid credential"}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: fixedRoute{route: relayapp.Route{ProviderID: "local", BaseURL: parsed, AuthMode: "passthrough"}},
		Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 2},
	})
	response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		Method: http.MethodPost, Path: "/v1/responses", ProviderID: "local", UpstreamModel: "gpt-test",
		Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"model":"gpt-test"}`),
	})
	if err != nil || response.Status != http.StatusUnauthorized || attempts != 2 {
		t.Fatalf("passthrough authentication did not terminate: status=%d attempts=%d err=%v", response.Status, attempts, err)
	}
}

func TestRetryAfterHTTPDateIsClamped(t *testing.T) {
	response := &http.Response{Header: http.Header{"Retry-After": []string{time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)}}}
	if delay := retryDelay(0, response, Config{RetryBase: time.Millisecond, RetryMax: 2 * time.Second}); delay != 2*time.Second {
		t.Fatalf("HTTP-date retry window was ignored: %s", delay)
	}
}

func TestPlanSpecificModelUnavailableErrorIsClassified(t *testing.T) {
	body := []byte(`{"error":{"message":"Model 'gpt-5.6-sol' is not available on your plan."}}`)
	if !modelUnavailable(body, "gpt-5.6-sol") {
		t.Fatal("plan-specific unavailable model did not fall through to the next key")
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

func TestChatStreamAcceptsFinishReasonWhenProviderOmitsDoneSentinel(t *testing.T) {
	inspector := sseInspector{path: "/v1/chat/completions"}
	inspector.Feed([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\n"))
	if terminal := inspector.Finish(); terminal != "done" {
		t.Fatalf("compatible chat terminal was rejected: %q", terminal)
	}
}

func TestBufferedResponsesHaveAnIndependentMemoryCeiling(t *testing.T) {
	limit := responseBufferLimit(Config{MaxRequestBytes: absoluteMaxRequestBytes})
	if limit != maxBufferedResponseBytes {
		t.Fatalf("buffered response limit followed the request limit: %d", limit)
	}
	if small := responseBufferLimit(Config{MaxRequestBytes: 1024}); small != 8*1024*1024 {
		t.Fatalf("small responses lost the compatibility floor: %d", small)
	}
}

func TestOversizedSuccessfulResponseIsPermanent(t *testing.T) {
	body := bytes.Repeat([]byte{'x'}, 8*1024*1024+1)
	response := &http.Response{Body: io.NopCloser(bytes.NewReader(body))}
	_, err := bufferJSONResponse(context.Background(), response, Config{MaxRequestBytes: 1024, StreamIdleTimeout: time.Second})
	if !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("oversized response was treated as retryable: %v", err)
	}
}

func TestTerminalSSEAcceptsIncorrectProviderContentTypeOnlyAfterValidation(t *testing.T) {
	response := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/plain"}},
		Body:   io.NopCloser(strings.NewReader("data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n")),
	}
	terminal, _, _, err := bufferTerminalSSE(context.Background(), response, "/v1/chat/completions", Config{StreamIdleTimeout: time.Second, MaxRequestBytes: 1024})
	if err != nil || terminal != "done" {
		t.Fatalf("valid SSE with a wrong content type was rejected: terminal=%q err=%v", terminal, err)
	}
}

func TestKnownJSONEndpointAcceptsMissingProviderContentType(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header()["Content-Type"] = nil
		_, _ = writer.Write([]byte(`{"status":"completed","output":[]}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &credentialSource{values: []string{"key"}},
		Config:      Config{StreamIdleTimeout: time.Second},
	})
	response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		Method: http.MethodPost, Path: "/v1/responses", ProviderID: "echo", UpstreamModel: "gpt-test",
		Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"model":"gpt-test"}`), AttemptLimit: 1,
	})
	if err != nil || response.Status != http.StatusOK || !json.Valid(response.Body) {
		t.Fatalf("valid JSON without a content type was rejected: status=%d body=%s err=%v", response.Status, response.Body, err)
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

func TestSSEInspectorRejectsCompletedEventWithoutResponse(t *testing.T) {
	for _, event := range []string{
		"data: {\"type\":\"response.completed\"}\n\n",
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"failed\",\"error\":{}}}\n\n",
	} {
		inspector := &sseInspector{}
		if terminal := inspector.Feed([]byte(event)); terminal != "response.invalid" {
			t.Fatalf("malformed completion was accepted: %q", terminal)
		}
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

func TestNonStreamingApplicationFailureIsTerminalAndNotCountedAsSuccess(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"failed","error":{"code":"provider_error"}}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	credentials := &credentialSource{values: []string{"key"}}
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}}, Credentials: credentials,
	})
	response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		Method: http.MethodPost, Path: "/v1/responses", ProviderID: "echo", UpstreamModel: "gpt-test",
		Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"model":"gpt-test"}`), AttemptLimit: 1,
	})
	if err != nil || response.Terminal != "response.failed" || len(credentials.outcomes) != 1 || credentials.outcomes[0].Kind != relayapp.AttemptRequestError {
		t.Fatalf("application failure was reported as success: terminal=%q outcomes=%+v err=%v", response.Terminal, credentials.outcomes, err)
	}
}

func TestApplicationFailureRetriesBeforeReturningSuccess(t *testing.T) {
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.Header().Set("Content-Type", "application/json")
		if attempts == 1 {
			_, _ = writer.Write([]byte(`{"status":"failed","error":{"code":"temporary"}}`))
			return
		}
		_, _ = writer.Write([]byte(`{"status":"completed","output":[]}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &credentialSource{values: []string{"key"}},
		Config:      Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 2},
	})
	response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		Method: http.MethodPost, Path: "/v1/responses", ProviderID: "echo", UpstreamModel: "gpt-test",
		Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"model":"gpt-test"}`),
	})
	if err != nil || response.Status != http.StatusOK || response.Terminal != "" || attempts != 2 {
		t.Fatalf("application failure was not recovered: status=%d terminal=%q attempts=%d err=%v", response.Status, response.Terminal, attempts, err)
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

func TestRequestModelRejectsOversizedAndControlIdentities(t *testing.T) {
	oversized, _ := json.Marshal(map[string]string{"model": strings.Repeat("m", maxRequestModelBytes+1)})
	if model := requestModel(oversized, "application/json"); model != "" {
		t.Fatalf("oversized model reached the scheduler: %d bytes", len(model))
	}
	if model := requestModel([]byte(`{"model":"bad\u0001model"}`), "application/json"); model != "" {
		t.Fatalf("control-bearing model reached the scheduler: %q", model)
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
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer", UpstreamModel: "gpt-5.6-sol", ImageCompat: true}},
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
	if value := joinPath("/gateway/v1", "/v1/models"); value != "/gateway/v1/models" {
		t.Fatalf("nested API version was duplicated: %q", value)
	}
	if value := joinPath("/gateway/v1", "/v1"); value != "/gateway/v1" {
		t.Fatalf("nested API root was duplicated: %q", value)
	}
}

func TestBuildUpstreamRequestPreservesProviderAndClientQuery(t *testing.T) {
	base, _ := url.Parse("https://provider.example/gateway?api-version=2026-08-01")
	incoming := httptest.NewRequest(http.MethodGet, "http://relay/v1/models?preview=true", nil)
	request, err := buildUpstreamRequest(context.Background(), incoming, nil, relayapp.Route{BaseURL: base, AuthMode: "passthrough"}, "")
	if err != nil {
		t.Fatal(err)
	}
	query := request.URL.Query()
	if query.Get("api-version") != "2026-08-01" || query.Get("preview") != "true" {
		t.Fatalf("provider or request query was lost: %s", request.URL.RawQuery)
	}
}

func TestBuildUpstreamRequestSupportsExactCustomAuthorizationScheme(t *testing.T) {
	base, _ := url.Parse("https://provider.example/v1")
	incoming := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{}`))
	request, err := buildUpstreamRequest(context.Background(), incoming, []byte(`{}`), relayapp.Route{BaseURL: base, AuthMode: "custom", AuthHeader: "Authorization"}, "Token custom-secret")
	if err != nil || request.Header.Get("Authorization") != "Token custom-secret" {
		t.Fatalf("custom Authorization value was not preserved exactly: %q err=%v", request.Header.Get("Authorization"), err)
	}
}

func TestJSONRewritesPreserveLargeProviderNumbers(t *testing.T) {
	body := []byte(`{"model":"public","seed":9007199254740993,"input":[{"cache_control":{"type":"ephemeral"}}]}`)
	rewritten := rewriteRequestModel(body, "application/json", "private")
	extended := extendCacheTTL(rewritten, "application/json")
	if !bytes.Contains(extended, []byte(`"seed":9007199254740993`)) || !bytes.Contains(extended, []byte(`"ttl":"1h"`)) {
		t.Fatalf("JSON mutation changed provider values: %s", extended)
	}
}

func TestCacheExtensionStopsAtTheNestingLimit(t *testing.T) {
	var nested any = "leaf"
	for range maxCacheTraversalDepth + 1 {
		nested = map[string]any{"child": nested}
	}
	payload := map[string]any{
		"cache_control": map[string]any{"type": "ephemeral"},
		"nested":        nested,
	}
	body, _ := json.Marshal(payload)
	if extended := extendCacheTTL(body, "application/json"); !bytes.Equal(extended, body) {
		t.Fatal("cache traversal crossed its nesting limit")
	}
}
