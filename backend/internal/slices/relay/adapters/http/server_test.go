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

// countingCredentialSource is a pool that reports its size, the way the keypool
// adapter does, so the relay can bound how often one request rotates keys.
type countingCredentialSource struct {
	credentialSource
	keys int
}

func (source *countingCredentialSource) Count(string) int { return source.keys }

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

func TestInternalPassthroughDispatchUsesTheEnteredKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if authorization := request.Header.Get("Authorization"); authorization != "Bearer entered-key" {
			t.Fatalf("entered key was not used: %q", authorization)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"id":"model"}]}`))
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "local", BaseURL: base, AuthMode: "passthrough"}},
		Credentials: &credentialSource{values: []string{"entered-key"}},
	})
	response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		Method: http.MethodGet, Path: "/v1/models", ProviderID: "local", UpstreamModel: "__model_catalog__",
		Headers: http.Header{"Accept": []string{"application/json"}}, AttemptLimit: 1, UseStoredCredential: true,
	})
	if err != nil || response.Status != http.StatusOK {
		t.Fatalf("internal passthrough dispatch failed: status=%d err=%v", response.Status, err)
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

// Newer OpenAI models refuse max_tokens and any non-default temperature. The
// relay retries once with the parameter the provider named instead of handing
// the client a 400.
func TestRejectedParametersAreRepairedOnce(t *testing.T) {
	var seen []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		body, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(body, &payload)
		seen = append(seen, payload)
		if _, legacy := payload["max_tokens"]; legacy {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead."}}`))
			return
		}
		if _, tuned := payload["temperature"]; tuned {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"message":"Unsupported value: 'temperature' does not support 0.2 with this model."}}`))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"chatcmpl-1","choices":[]}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: fixedRoute{route: relayapp.Route{ProviderID: "openai", BaseURL: parsed, AuthMode: "passthrough"}},
		Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 2},
	})
	response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		Method: http.MethodPost, Path: "/v1/chat/completions", ProviderID: "openai", UpstreamModel: "gpt-5",
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    []byte(`{"model":"gpt-5","max_tokens":64,"temperature":0.2,"messages":[]}`),
	})
	if err != nil || response.Status != http.StatusOK {
		t.Fatalf("rejected parameters were not repaired: status=%d err=%v", response.Status, err)
	}
	if len(seen) != 3 {
		t.Fatalf("expected one repair per rejected parameter, saw %d attempts", len(seen))
	}
	final := seen[2]
	if final["max_completion_tokens"] != float64(64) {
		t.Fatalf("max_tokens was not carried over: %v", final["max_completion_tokens"])
	}
	if _, legacy := final["max_tokens"]; legacy {
		t.Fatal("max_tokens survived the repair")
	}
	if _, tuned := final["temperature"]; tuned {
		t.Fatal("the rejected temperature survived the repair")
	}
}

// A 400 the relay cannot explain must reach the client immediately.
func TestUnrelatedBadRequestIsNotRetried(t *testing.T) {
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"message":"Unsupported parameter: 'seed'."}}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: fixedRoute{route: relayapp.Route{ProviderID: "openai", BaseURL: parsed, AuthMode: "passthrough"}},
		Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 1},
	})
	response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		Method: http.MethodPost, Path: "/v1/chat/completions", ProviderID: "openai", UpstreamModel: "gpt-5",
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    []byte(`{"model":"gpt-5","max_tokens":64,"messages":[]}`),
	})
	if err != nil || response.Status != http.StatusBadRequest || attempts != 1 {
		t.Fatalf("an unrelated 400 was retried: status=%d attempts=%d err=%v", response.Status, attempts, err)
	}
}

func TestAProviderThatOnlyRateLimitsStopsInsteadOfRetryingForever(t *testing.T) {
	// The local relay path sets no attempt limit, so before the ceiling existed a
	// provider stuck on 429 kept the loop and its stream keep-alives running for
	// hours: history holds a request that retried two thousand times over half a day.
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "openai", BaseURL: parsed, AuthMode: "passthrough"}},
		Credentials: &credentialSource{values: []string{""}},
		Config:      Config{RetryBase: time.Microsecond, RetryMax: time.Microsecond},
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-5","messages":[]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if attempts != maxRelayAttempts {
		t.Fatalf("the attempt ceiling was not applied: attempts=%d want=%d", attempts, maxRelayAttempts)
	}
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("the caller was not told why the request ended: status=%d", response.Code)
	}
}

func TestPlanSpecificModelUnavailableErrorIsClassified(t *testing.T) {
	body := []byte(`{"error":{"message":"Model 'gpt-5.6-sol' is not available on your plan."}}`)
	if !modelUnavailable(body, "gpt-5.6-sol") {
		t.Fatal("plan-specific unavailable model did not fall through to the next key")
	}
	if modelMissing(body, "gpt-5.6-sol") {
		t.Fatal("a plan restriction was mistaken for a missing model")
	}
}

// A model the provider does not host cannot appear on another key. The relay
// used to rotate the pool anyway, and because every rejection cools the key for
// the model, it kept waiting for those cooldowns until the request timed out.
func TestMissingModelAnswersWithoutRotatingTheKeyPool(t *testing.T) {
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"error":{"message":"The model 'claude-haiku-4-5' does not exist"}}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "privatka", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &countingCredentialSource{credentialSource: credentialSource{values: []string{"a", "b", "c"}}, keys: 3},
		Config:      Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 3},
	})
	response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		Method: http.MethodPost, Path: "/v1/responses", ProviderID: "privatka", UpstreamModel: "claude-haiku-4-5",
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    []byte(`{"model":"claude-haiku-4-5"}`),
	})
	if err != nil || response.Status != http.StatusNotFound {
		t.Fatalf("missing model did not surface as 404: status=%d err=%v", response.Status, err)
	}
	if attempts != 1 {
		t.Fatalf("missing model was retried %d times", attempts)
	}
}

// A credential-level rejection is worth another key, but only until the pool is
// exhausted; without the bound the request rotated keys until it timed out.
func TestCredentialRejectionStopsAfterEveryKeyAnswered(t *testing.T) {
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":"invalid credential"}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &countingCredentialSource{credentialSource: credentialSource{values: []string{"a", "b", "c"}}, keys: 3},
		Config:      Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 2},
	})
	response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		Method: http.MethodPost, Path: "/v1/responses", ProviderID: "echo", UpstreamModel: "gpt-test",
		Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"model":"gpt-test"}`),
	})
	if err != nil || response.Status != http.StatusUnauthorized {
		t.Fatalf("credential rejection did not surface: status=%d err=%v", response.Status, err)
	}
	if attempts != 3 {
		t.Fatalf("expected one attempt per key, saw %d", attempts)
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

func TestResponsesLifecycleEventsKeepStreamOpenUntilCompleted(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher := writer.(http.Flusher)
		_, _ = writer.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"status\":\"queued\"}}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.in_progress\",\"response\":{\"status\":\"in_progress\"}}\n\n"))
		flusher.Flush()
		<-release
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"error\":null,\"incomplete_details\":null}}\n\n"))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &credentialSource{values: []string{"key"}},
		Config:      Config{StreamIdleTimeout: time.Second, MaxRequestBytes: 1024 * 1024},
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{"model":"gpt-test","stream":true}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		done <- response
	}()
	select {
	case response := <-done:
		t.Fatalf("relay stopped on non-terminal lifecycle event: %s", response.Body.String())
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	response := <-done
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "response.in_progress") || !strings.Contains(response.Body.String(), "response.completed") {
		t.Fatalf("lifecycle stream did not complete: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestResponsesEmptyFailureAfterInProgressRetriesUntilCompleted(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"status\":\"queued\"}}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.in_progress\",\"response\":{\"status\":\"in_progress\"}}\n\n"))
		if attempts == 1 {
			_, _ = writer.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":null,\"output\":[]}}\n\n"))
			return
		}
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"error\":null,\"incomplete_details\":null}}\n\n"))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	credentials := &credentialSource{values: []string{"key"}}
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: credentialsRoute(parsed), Credentials: credentials,
		Config: Config{RetryBase: time.Millisecond, RetryMax: 2 * time.Millisecond, StreamIdleTimeout: time.Second, MaxRequestBytes: 1024 * 1024, PermanentAttempts: 2},
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{"model":"gpt-test","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if attempts != 2 || credentials.calls != 2 || response.Code != http.StatusOK || strings.Contains(response.Body.String(), "response.failed") || strings.Count(response.Body.String(), "response.created") != 1 || !strings.Contains(response.Body.String(), "response.in_progress") || !strings.Contains(response.Body.String(), "response.completed") {
		t.Fatalf("empty lifecycle failure was not retried cleanly: attempts=%d credentials=%d status=%d body=%s", attempts, credentials.calls, response.Code, response.Body.String())
	}
}

func TestResponsesFailureAfterOutputIsNotRetried(t *testing.T) {
	inspector := &sseInspector{path: "/v1/responses"}
	terminal := inspector.Feed([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":null}}\n\n"))
	if terminal != "response.failed" || !inspector.output || inspector.retryableFailure() {
		t.Fatalf("output-aware failure classification is wrong: terminal=%q output=%v retryable=%v", terminal, inspector.output, inspector.retryableFailure())
	}
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":null}}\n\n"))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: credentialsRoute(parsed), Credentials: &credentialSource{values: []string{"key"}},
		Config: Config{RetryBase: time.Millisecond, RetryMax: 2 * time.Millisecond, StreamIdleTimeout: time.Second, MaxRequestBytes: 1024 * 1024, PermanentAttempts: 3},
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{"model":"gpt-test","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if attempts != 1 || !strings.Contains(response.Body.String(), "partial") || !strings.Contains(response.Body.String(), "response.failed") {
		t.Fatalf("committed output was replayed or lost: attempts=%d body=%s", attempts, response.Body.String())
	}
}

func TestResponsesEmptyFailureRetriesAreBounded(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.in_progress\",\"response\":{\"status\":\"in_progress\"}}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":null,\"output\":[]}}\n\n"))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: credentialsRoute(parsed), Credentials: &credentialSource{values: []string{"key-a", "key-b", "key-c", "key-d"}},
		Config: Config{RetryBase: time.Millisecond, RetryMax: 2 * time.Millisecond, StreamIdleTimeout: time.Second, MaxRequestBytes: 1024 * 1024, PermanentAttempts: 100},
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{"model":"gpt-test","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if attempts != 3 || strings.Count(response.Body.String(), "response.failed") != 1 {
		t.Fatalf("empty failure retries were not bounded: attempts=%d body=%s", attempts, response.Body.String())
	}
}

// A refusal is the provider answering, not the provider breaking. It used to be
// filed as an empty failure, so the relay sent the same refused payload again
// under the next key in the pool: three attempts, three keys, one verdict, and a
// client held through two backoffs for the answer the first attempt already had.
func TestResponsesPolicyRefusalIsAnsweredOnceWithoutRotatingKeys(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte(`data: {"type":"response.in_progress","response":{"status":"in_progress"}}` + "\n\n"))
		_, _ = writer.Write([]byte(`data: {"type":"response.failed","response":{"status":"failed","output":[],"error":{"code":"invalid_prompt","message":"This content was flagged for possible cybersecurity risk."}}}` + "\n\n"))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	credentials := &credentialSource{values: []string{"key-a", "key-b", "key-c"}}
	activity := &recordingActivity{}
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: credentialsRoute(parsed), Credentials: credentials, Activity: activity,
		Config: Config{RetryBase: time.Millisecond, RetryMax: 2 * time.Millisecond, StreamIdleTimeout: time.Second, MaxRequestBytes: 1024 * 1024, PermanentAttempts: 3},
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{"model":"gpt-test","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if attempts != 1 || credentials.calls != 1 || len(activity.retries) != 0 {
		t.Fatalf("refusal was sent again: attempts=%d credentials=%d retries=%d", attempts, credentials.calls, len(activity.retries))
	}
	if !strings.Contains(response.Body.String(), "flagged for possible cybersecurity risk") {
		t.Fatalf("the provider's own refusal did not reach the client: %s", response.Body.String())
	}
	if activity.finish.ErrorCode != "policy_refusal" {
		t.Fatalf("refusal was filed as %q, so the operator reads it as a broken provider", activity.finish.ErrorCode)
	}
	// The key did nothing wrong and the answer is not one either: reporting success
	// would credit the pool for a turn that produced nothing.
	if len(credentials.outcomes) != 1 || credentials.outcomes[0].Kind != relayapp.AttemptRequestError {
		t.Fatalf("refused attempt was reported to the pool as %+v", credentials.outcomes)
	}
}

// The same verdict arriving as a status must not walk the pool either. The 400
// ladder repairs and resends, and stripping sealed reasoning is deliberately
// ungated on what the provider said - so a refusal that reached it was resent
// with a smaller body and then twice more under fresh keys.
func TestBadRequestPolicyRefusalIsNotRepairedOrRotated(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":{"code":"invalid_prompt","message":"This content was flagged for possible cybersecurity risk."}}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	credentials := &credentialSource{values: []string{"key-a", "key-b", "key-c"}}
	activity := &recordingActivity{}
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: credentialsRoute(parsed), Credentials: credentials, Activity: activity,
		Config: Config{RetryBase: time.Millisecond, RetryMax: 2 * time.Millisecond, StreamIdleTimeout: time.Second, MaxRequestBytes: 1024 * 1024, PermanentAttempts: 3},
	})
	body := `{"model":"gpt-test","input":[{"type":"reasoning","encrypted_content":"sealed"},{"type":"message","role":"user","content":"audit this"}]}`
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if attempts != 1 || credentials.calls != 1 || len(activity.retries) != 0 {
		t.Fatalf("refused request was repaired and resent: attempts=%d credentials=%d retries=%d", attempts, credentials.calls, len(activity.retries))
	}
	if activity.finish.ErrorCode != "policy_refusal" {
		t.Fatalf("status refusal was filed as %q", activity.finish.ErrorCode)
	}
}

// policyRefused decides whether a request is ever sent again, so it has to stay
// blind to prose that only sounds like a verdict: quota and context-length bodies
// are the ones another attempt can still serve.
func TestOrdinaryFaultsAreNotReadAsRefusals(t *testing.T) {
	// One phrasing per marker, so no marker sits in the list unguarded: a refusal
	// the list stops recognising is a refusal that goes back to walking the pool.
	for _, refusal := range []string{
		`{"error":{"message":"This content was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your request."}}`,
		`{"error":{"message":"To get authorized for security work, join the Trusted Access for Cyber program"}}`,
		`{"error":{"code":"invalid_prompt","message":"Your request was rejected"}}`,
		`{"error":{"code":"content_policy_violation"}}`,
		`{"error":{"message":"this violates our content policy"}}`,
		`{"error":{"code":"content_filter"}}`,
		`{"error":{"message":"stopped by the content filter"}}`,
		`{"error":{"message":"blocked by our moderation system"}}`,
		`{"error":{"message":"not permitted under our usage policies"}}`,
		`{"error":{"message":"rejected by the safety system"}}`,
	} {
		if !policyRefused(refusal) {
			t.Fatalf("a refusal was missed and will be resent under every key: %s", refusal)
		}
	}
	for _, fault := range []string{
		`{"error":{"code":"rate_limit_exceeded","message":"Rate limit reached for your organization"}}`,
		`{"error":{"code":"insufficient_quota","message":"You exceeded your current quota, please check your plan and billing details"}}`,
		`{"error":{"code":"server_error","message":"The server had an error while processing your request"}}`,
		`{"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 128000 tokens"}}`,
	} {
		if policyRefused(fault) {
			t.Fatalf("an ordinary fault was read as a refusal and will not be retried: %s", fault)
		}
	}
}

// The refusal is read out of the failure the event reports, not out of the whole
// event. An answer that discusses content policy is not an answer refused by it,
// and reading the payload whole would strand exactly the turns this codebase
// produces - the ones that talk about moderation and key pools for a living.
func TestAnAnswerAboutPolicyIsNotAnAnswerRefusedByIt(t *testing.T) {
	inspector := &sseInspector{path: "/v1/responses"}
	terminal := inspector.Feed([]byte(`data: {"type":"response.failed","response":{"status":"failed","error":null,"output":[{"type":"message","content":[{"type":"output_text","text":"our content policy blocked the moderation test"}]}]}}` + "\n\n"))
	if terminal != "response.failed" || inspector.refused || !inspector.retryableFailure() || inspector.reason() != "response.failed" {
		t.Fatalf("an answer about policy was filed as one refused by it: refused=%v retryable=%v reason=%q", inspector.refused, inspector.retryableFailure(), inspector.reason())
	}
}

func TestResponsesInspectorReadsPastInProgressChunk(t *testing.T) {
	first := []byte("data: {\"type\":\"response.in_progress\",\"response\":{\"status\":\"in_progress\"}}\n\n")
	second := []byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"error\":null,\"incomplete_details\":null}}\n\n")
	response := &http.Response{Body: io.NopCloser(io.MultiReader(bytes.NewReader(first), &delayedReader{delay: 25 * time.Millisecond, body: second}))}
	terminal, body, _, err := bufferTerminalSSE(context.Background(), response, "/v1/responses", Config{StreamIdleTimeout: time.Second, MaxRequestBytes: 1024 * 1024})
	if err != nil || terminal != "response.completed" || !bytes.Contains(body, []byte("response.in_progress")) || !bytes.Contains(body, []byte("response.completed")) {
		t.Fatalf("inspector stopped on an in-progress chunk: terminal=%q body=%s err=%v", terminal, body, err)
	}
}

type delayedReader struct {
	delay  time.Duration
	body   []byte
	offset int
}

func (reader *delayedReader) Read(target []byte) (int, error) {
	if reader.offset == 0 {
		time.Sleep(reader.delay)
	}
	if reader.offset >= len(reader.body) {
		return 0, io.EOF
	}
	count := copy(target, reader.body[reader.offset:])
	reader.offset += count
	return count, nil
}

func TestResponsesInProgressWithoutUpstreamBytesKeepsClientAlive(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher := writer.(http.Flusher)
		_, _ = writer.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"status\":\"queued\"}}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.in_progress\",\"response\":{\"status\":\"in_progress\"}}\n\n"))
		flusher.Flush()
		time.Sleep(60 * time.Millisecond)
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"error\":null,\"incomplete_details\":null}}\n\n"))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: credentialsRoute(parsed), Credentials: &credentialSource{values: []string{"key"}},
		Config: Config{HeartbeatInterval: 10 * time.Millisecond, StreamIdleTimeout: time.Second, MaxRequestBytes: 1024 * 1024},
	})
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{"model":"gpt-test","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, ": switchboard keep-alive") || strings.Count(body, `"type":"response.in_progress"`) < 2 || !strings.Contains(body, "response.completed") {
		t.Fatalf("Responses lifecycle heartbeat was not emitted: status=%d body=%s", response.Code, body)
	}
}

func credentialsRoute(base *url.URL) fixedRoute {
	return fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: base, AuthMode: "bearer"}}
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
	if !strings.Contains(response.Body.String(), "response.in_progress") || !strings.Contains(response.Body.String(), "response.completed") {
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

// The one-hour cache policy rewrites the client's request, so it also has to
// declare the capability that rewrite depends on. A client asking for the hour
// itself sends `anthropic-beta: extended-cache-ttl-2025-04-11` with it; the relay
// raising a five-minute breakpoint on the client's behalf used to send the body
// without the header, which is a request no client produces.
//
// What decides it is the body being sent, not who wrote the hour into it: an
// upstream that gates the feature reads the header, so an hour the client asked
// for and forgot to declare has to be declared too.
//
// The body shape is the whole gate: `cache_control` is Anthropic's, so a Responses
// or Chat Completions request has no breakpoint to carry an hour and must not be
// given an Anthropic capability it never asked for.
func TestTheHourIsDeclaredWheneverTheBodyCarriesIt(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		sent     string
		expected string
	}{
		{
			name:     "an ephemeral breakpoint declares the hour",
			body:     `{"model":"m","system":[{"type":"text","text":"t","cache_control":{"type":"ephemeral"}}]}`,
			expected: extendedCacheTTLBeta,
		},
		{
			name:     "the client's own features survive the addition",
			body:     `{"model":"m","system":[{"type":"text","text":"t","cache_control":{"type":"ephemeral"}}]}`,
			sent:     "context-1m-2025-08-07,files-api-2025-04-14",
			expected: "context-1m-2025-08-07,files-api-2025-04-14," + extendedCacheTTLBeta,
		},
		{
			name:     "a client that already declared it is left alone",
			body:     `{"model":"m","system":[{"type":"text","text":"t","cache_control":{"type":"ephemeral"}}]}`,
			sent:     extendedCacheTTLBeta,
			expected: extendedCacheTTLBeta,
		},
		{
			name:     "a body with nothing to raise declares nothing",
			body:     `{"model":"m","input":"hello"}`,
			expected: "",
		},
		{
			name:     "an hour the client asked for and forgot to declare",
			body:     `{"model":"m","system":[{"type":"text","text":"t","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`,
			expected: extendedCacheTTLBeta,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var declared, forwarded string
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				declared = request.Header.Get("Anthropic-Beta")
				raw, _ := io.ReadAll(request.Body)
				forwarded = string(raw)
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"ok":true}`))
			}))
			defer upstream.Close()
			base, _ := url.Parse(upstream.URL)
			server := NewServer("127.0.0.1:0", Dependencies{
				Routes: fixedRoute{route: relayapp.Route{
					ProviderID: "anthropic", Dialect: "anthropic", AuthMode: "auto",
					BaseURL: base, CacheTTL: time.Hour,
				}},
				Credentials: &credentialSource{values: []string{"sk-upstream"}},
			})
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(testCase.body))
			request.Header.Set("Content-Type", "application/json")
			if testCase.sent != "" {
				request.Header.Set("Anthropic-Beta", testCase.sent)
			}
			server.ServeHTTP(httptest.NewRecorder(), request)
			if declared != testCase.expected {
				t.Fatalf("declared %q, expected %q", declared, testCase.expected)
			}
			// A body carrying the hour must be declared, and one that declares nothing
			// must not be carrying it.
			if strings.Contains(forwarded, `"ttl":"1h"`) != strings.Contains(declared, extendedCacheTTLBeta) {
				t.Fatalf("body and header disagree about the hour: body=%s header=%q", forwarded, declared)
			}
			// The caller's own header must never be edited: the relay answers from it
			// after the upstream call.
			if request.Header.Get("Anthropic-Beta") != testCase.sent {
				t.Fatalf("the caller's header was mutated: %q", request.Header.Get("Anthropic-Beta"))
			}
		})
	}
}

func TestJSONRewritesPreserveLargeProviderNumbers(t *testing.T) {
	body := []byte(`{"model":"public","seed":9007199254740993,"input":[{"cache_control":{"type":"ephemeral"}}]}`)
	rewritten := rewriteRequestModel(body, "application/json", "private")
	extended, carriesHour := extendCacheTTL(rewritten, "application/json")
	if !carriesHour {
		t.Fatal("the extended body does not carry an hour-long breakpoint")
	}
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
	extended, carriesHour := extendCacheTTL(body, "application/json")
	// Nothing is claimed about a body that was handed back untouched.
	if carriesHour || !bytes.Equal(extended, body) {
		t.Fatal("cache traversal crossed its nesting limit")
	}
}

// A gateway that answers 200 with nothing is the failure a client reports as an
// empty or malformed response, and it ends the run. The relay retries it instead
// of forwarding the silence.
func TestAnEmptyTwoHundredIsRetriedAndTheRealAnswerWins(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.Header().Set("Content-Type", "application/json")
		if attempts == 1 {
			return
		}
		_, _ = writer.Write([]byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`))
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &credentialSource{values: []string{"key"}},
		Config:      Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, StreamIdleTimeout: time.Second, PermanentAttempts: 3},
	})
	response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		Method: http.MethodPost, Path: "/v1/responses", ProviderID: "echo", UpstreamModel: "gpt-test",
		Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"model":"gpt-test"}`),
	})
	if err != nil || response.Status != http.StatusOK {
		t.Fatalf("an empty answer was not retried: status=%d err=%v", response.Status, err)
	}
	if attempts != 2 {
		t.Fatalf("expected one retry after the empty answer, got %d attempts", attempts)
	}
	if !strings.Contains(string(response.Body), `"done"`) {
		t.Fatalf("the retried answer never reached the client: %s", response.Body)
	}
}

// The retry is bounded like every other provider failure, and what travels at the
// end is the provider's own answer: an error of ours would hide whose fault it is.
func TestAProviderThatOnlyAnswersEmptyIsBoundedAndStillForwarded(t *testing.T) {
	var attempts int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		writer.Header().Set("Content-Type", "application/json")
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: parsed, AuthMode: "bearer"}},
		Credentials: &credentialSource{values: []string{"key"}},
		Config:      Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, StreamIdleTimeout: time.Second, PermanentAttempts: 2},
	})
	response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		Method: http.MethodPost, Path: "/v1/chat/completions", ProviderID: "echo", UpstreamModel: "gpt-test",
		Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"model":"gpt-test","messages":[]}`),
	})
	if err != nil {
		t.Fatalf("the bounded retry turned into an error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("the empty answer was retried %d times, not the permanent-attempt budget", attempts)
	}
	if response.Status != http.StatusOK || len(response.Body) != 0 {
		t.Fatalf("the provider's own answer was replaced: status=%d body=%q", response.Status, response.Body)
	}
	// The relay strips X-Switchboard-Terminal from the headers before they leave, so
	// asserting on the header would pass whatever the retry ladder decided. The field is
	// where that decision survives, and it has to stay empty: an empty answer is the
	// provider's own, not a terminal failure the relay is claiming happened.
	if response.Terminal != "" {
		t.Fatalf("an empty answer was reported as a terminal failure: %q", response.Terminal)
	}
	if response.Headers.Get("X-Switchboard-Terminal") != "" {
		t.Fatalf("an internal header reached the client: %q", response.Headers.Get("X-Switchboard-Terminal"))
	}
}

// An empty body means nothing on the endpoints that legitimately return an empty
// collection, and a well-formed envelope with no items is the provider's own
// account of the turn rather than a lost answer.
func TestOnlyInferencePathsCountAnEmptyBodyAsALostAnswer(t *testing.T) {
	for _, testCase := range []struct {
		path   string
		body   string
		status int
		want   bool
	}{
		{"/v1/responses", "", http.StatusOK, true},
		{"/v1/chat/completions", "   \n\t", http.StatusOK, true},
		{"/v1/messages", "", http.StatusOK, true},
		{"/v1/completions/", "", http.StatusOK, true},
		{"/v1/images/generations", "", http.StatusOK, false},
		{"/v1/models", "", http.StatusOK, false},
		{"/v1/responses", `{"status":"completed","output":[]}`, http.StatusOK, false},
		{"/v1/chat/completions", `{"choices":[]}`, http.StatusOK, false},
		// An empty 201/202 is an API saying it accepted the work, not one losing it.
		{"/v1/responses", "", http.StatusAccepted, false},
		{"/v1/responses", "", http.StatusCreated, false},
	} {
		if got := emptyUpstreamAnswer([]byte(testCase.body), testCase.path, testCase.status); got != testCase.want {
			t.Fatalf("%s %d with body %q read as lost=%v", testCase.path, testCase.status, testCase.body, got)
		}
	}
}
