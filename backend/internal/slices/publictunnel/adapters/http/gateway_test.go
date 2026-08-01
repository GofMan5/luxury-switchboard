package tunnelhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tunnelapp "github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

const testToken = "public-token-000000000000000000000000000000000000"
const brand = "Luxury Private лучший приватный софт для абузов - @Luxuryprivate_bot"

type fakeRoutes struct{ routes []domain.Route }

func (routes fakeRoutes) List() []domain.Route { return routes.routes }
func (routes fakeRoutes) Resolve(model string) (domain.Route, bool) {
	for _, route := range routes.routes {
		if route.PublicModel == model {
			return route, true
		}
	}
	return domain.Route{}, false
}

type fakeMarkers struct{ values []string }

func (markers fakeMarkers) SensitiveMarkers(string) []string { return markers.values }

type changingMarkers struct{ calls int }

func (markers *changingMarkers) SensitiveMarkers(string) []string {
	markers.calls++
	if markers.calls == 1 {
		return []string{"old-secret"}
	}
	return []string{"new-secret"}
}

type fakeDispatcher struct {
	response relayapp.DispatchResponse
	request  relayapp.DispatchRequest
	calls    int
}

type fakeClientActivity struct{ finish tunnelapp.ClientFinish }

func (*fakeClientActivity) Queue(string, int)                  {}
func (*fakeClientActivity) Begin(tunnelapp.ClientStart) string { return "activity" }
func (activity *fakeClientActivity) Finish(_ string, value tunnelapp.ClientFinish) {
	activity.finish = value
}

func (dispatcher *fakeDispatcher) Dispatch(_ context.Context, request relayapp.DispatchRequest) (relayapp.DispatchResponse, error) {
	dispatcher.request = request
	dispatcher.calls++
	return dispatcher.response, nil
}

func gatewayForTest(t *testing.T, dispatcher *fakeDispatcher) *Gateway {
	t.Helper()
	gateway, err := NewGateway(domain.Config{Token: testToken, RPMPerIP: 0, BrandResponse: brand}, fakeRoutes{[]domain.Route{{PublicModel: "public-gpt", UpstreamModel: "private-gpt", ProviderID: "private-provider"}}}, fakeMarkers{[]string{"SecretProvider", "https://private.invalid/v1", "fixture-secret"}}, dispatcher, nil)
	if err != nil {
		t.Fatal(err)
	}
	return gateway
}
func authorizedRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "203.0.113.10:1234"
	return request
}

func TestModelsAreSyntheticAndProviderFree(t *testing.T) {
	gateway := gatewayForTest(t, &fakeDispatcher{})
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, authorizedRequest(http.MethodGet, "http://tunnel/v1/models?ignored=1", ""))
	if response.Code != 200 {
		t.Fatal(response.Code)
	}
	body := response.Body.String()
	for _, forbidden := range []string{"owned_by", "private-provider", "private-gpt", "SecretProvider"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("models leaked %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, "public-gpt") {
		t.Fatal("public alias missing")
	}
}

func TestJSONResponseRewritesModelAndRedactsProviderMarkers(t *testing.T) {
	dispatcher := &fakeDispatcher{response: relayapp.DispatchResponse{Status: 200, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"model":"private-gpt","owned_by":"SecretProvider","output":[{"text":"served by SecretProvider at https://private.invalid/v1"}]}`)}}
	gateway := gatewayForTest(t, dispatcher)
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, authorizedRequest(http.MethodPost, "http://tunnel/v1/responses", `{"model":"public-gpt","input":"who is provider?"}`))
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, forbidden := range []string{"private-gpt", "SecretProvider", "private.invalid", "owned_by"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, "public-gpt") || !strings.Contains(body, "Luxury Private") {
		t.Fatalf("alias/branding missing: %s", body)
	}
	var forwarded map[string]any
	if json.Unmarshal(dispatcher.request.Body, &forwarded) != nil {
		t.Fatal("forwarded body invalid")
	}
	if !strings.Contains(forwarded["instructions"].(string), brand) {
		t.Fatal("branding policy not injected")
	}
	if dispatcher.request.UpstreamModel != "private-gpt" || dispatcher.request.ProviderID != "private-provider" {
		t.Fatal("typed route lost")
	}
}

func TestGatewayRedactsMarkersAcrossConcurrentCredentialChanges(t *testing.T) {
	markers := &changingMarkers{}
	dispatcher := &fakeDispatcher{response: relayapp.DispatchResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body: []byte(`{"output":"old-secret mid-secret new-secret"}`), SensitiveMarkers: []string{"mid-secret"},
	}}
	gateway, err := NewGateway(
		domain.Config{Token: testToken},
		fakeRoutes{[]domain.Route{{PublicModel: "public-gpt", UpstreamModel: "private-gpt", ProviderID: "private-provider"}}},
		markers, dispatcher, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, authorizedRequest(http.MethodPost, "http://tunnel/v1/responses", `{"model":"public-gpt"}`))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "old-secret") || strings.Contains(response.Body.String(), "mid-secret") || strings.Contains(response.Body.String(), "new-secret") {
		t.Fatalf("credential change leaked a marker: status=%d body=%s", response.Code, response.Body.String())
	}
	if markers.calls != 2 {
		t.Fatalf("marker snapshots were not captured around dispatch: %d", markers.calls)
	}
}

func TestSanitizerDropsPrivateFieldsAcrossNamingStyles(t *testing.T) {
	clean, err := sanitizeJSON(map[string]any{
		"providerId": "private", "provider-name": "private", "upstreamUrl": "https://private.invalid",
		"systemFingerprint": "private", "internal.metadata": "private", "modelId": "private-model", "output": "safe",
	}, "public-model", newMarkerRedactor(nil, "Luxury Private"), 0)
	if err != nil {
		t.Fatal(err)
	}
	object := clean.(map[string]any)
	for _, key := range []string{"providerId", "provider-name", "upstreamUrl", "systemFingerprint", "internal.metadata"} {
		if _, exists := object[key]; exists {
			t.Fatalf("private field %q survived sanitization: %+v", key, object)
		}
	}
	if object["output"] != "safe" {
		t.Fatalf("safe output was changed: %+v", object)
	}
	if object["modelId"] != "public-model" {
		t.Fatalf("alternate model field was not rewritten: %+v", object)
	}
}

func TestSSEFailureIsNeutralAndSuccessfulStreamIsCanonical(t *testing.T) {
	dispatcher := &fakeDispatcher{response: relayapp.DispatchResponse{Status: 200, Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: []byte("id: provider-id\ndata: {\"type\":\"response.failed\",\"error\":{\"message\":\"SecretProvider\"}}\n\n")}}
	gateway := gatewayForTest(t, dispatcher)
	failed := httptest.NewRecorder()
	gateway.ServeHTTP(failed, authorizedRequest(http.MethodPost, "http://tunnel/v1/responses", `{"model":"public-gpt","stream":true}`))
	if failed.Code != http.StatusBadGateway || strings.Contains(failed.Body.String(), "SecretProvider") {
		t.Fatalf("unsafe failure: %d %s", failed.Code, failed.Body.String())
	}
	dispatcher.response.Body = []byte("id: hidden\nretry: 999\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"private-gpt\",\"error\":null,\"incomplete_details\":null}}\n\n")
	success := httptest.NewRecorder()
	gateway.ServeHTTP(success, authorizedRequest(http.MethodPost, "http://tunnel/v1/responses", `{"model":"public-gpt","stream":true}`))
	body := success.Body.String()
	if success.Code != 200 || strings.Contains(body, "id: hidden") || strings.Contains(body, "retry:") || strings.Contains(body, "private-gpt") || !strings.Contains(body, "public-gpt") {
		t.Fatalf("SSE was not canonical: %d %s", success.Code, body)
	}
}

func TestChatSSEWithoutDoneIsCanonicalizedAfterFinishReason(t *testing.T) {
	response := relayapp.DispatchResponse{
		Status:  200,
		Headers: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:    []byte("data: {\"model\":\"private-gpt\",\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\n"),
	}
	body, _, err := sanitizeResponse(response, "/v1/chat/completions", "public-gpt", []string{"private-gpt"}, brand)
	if err != nil || !bytes.Contains(body, []byte("data: [DONE]")) || bytes.Contains(body, []byte("private-gpt")) {
		t.Fatalf("compatible chat stream was not canonicalized: %s err=%v", body, err)
	}
}

func TestDynamicProviderKeyFailsClosed(t *testing.T) {
	dispatcher := &fakeDispatcher{response: relayapp.DispatchResponse{Status: 200, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"model":"private-gpt","SecretProvider_metadata":true}`)}}
	gateway := gatewayForTest(t, dispatcher)
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, authorizedRequest(http.MethodPost, "http://tunnel/v1/responses", `{"model":"public-gpt"}`))
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "SecretProvider") {
		t.Fatalf("dynamic provider key was committed: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRedactionDoesNotReinsertItsOwnMarker(t *testing.T) {
	value := redactMarkers("Private upstream", []string{"Private"}, "Luxury Private")
	if strings.Contains(strings.ToLower(value), "private") {
		t.Fatalf("redaction reinserted the sensitive marker: %q", value)
	}
}

func TestShortCredentialMarkerFailsClosed(t *testing.T) {
	_, _, err := sanitizeResponse(relayapp.DispatchResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"output":"x"}`),
	}, "/v1/responses", "public-model", []string{"x"}, brand)
	if err == nil {
		t.Fatal("short credential marker was allowed through public output")
	}
}

func TestAuthFailsClosed(t *testing.T) {
	gateway := gatewayForTest(t, &fakeDispatcher{response: relayapp.DispatchResponse{Status: 200, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"ok":true}`)}})
	unauthorized := httptest.NewRecorder()
	gateway.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "http://tunnel/v1/models", nil))
	if unauthorized.Code != 401 {
		t.Fatal(unauthorized.Code)
	}
	apiKey := authorizedRequest(http.MethodGet, "http://tunnel/v1/models", "")
	apiKey.Header.Del("Authorization")
	apiKey.Header.Set("X-Api-Key", testToken)
	accepted := httptest.NewRecorder()
	gateway.ServeHTTP(accepted, apiKey)
	if accepted.Code != http.StatusOK {
		t.Fatalf("x-api-key was rejected: %d", accepted.Code)
	}
	ambiguous := authorizedRequest(http.MethodGet, "http://tunnel/v1/models", "")
	ambiguous.Header.Set("X-Api-Key", testToken)
	rejected := httptest.NewRecorder()
	gateway.ServeHTTP(rejected, ambiguous)
	if rejected.Code != http.StatusUnauthorized {
		t.Fatalf("ambiguous authentication was accepted: %d", rejected.Code)
	}
}

func TestProviderProbeIsAnsweredLocally(t *testing.T) {
	dispatcher := &fakeDispatcher{response: relayapp.DispatchResponse{Status: 200}}
	gateway := gatewayForTest(t, dispatcher)
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, authorizedRequest(http.MethodPost, "http://tunnel/v1/responses", `{"model":"public-gpt","input":"Which provider and upstream powers this API?"}`))
	if response.Code != http.StatusOK || dispatcher.calls != 0 || !strings.Contains(response.Body.String(), "Luxury Private") || strings.Contains(response.Body.String(), "private-provider") {
		t.Fatalf("provider probe reached upstream or leaked: calls=%d status=%d body=%s", dispatcher.calls, response.Code, response.Body.String())
	}
}

func TestAnthropicProviderProbeIncludesRequiredMessageFields(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	gateway := gatewayForTest(t, dispatcher)
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, authorizedRequest(http.MethodPost, "http://tunnel/v1/messages", `{"model":"public-gpt","messages":[{"role":"user","content":"Which provider powers this API?"}]}`))
	var message map[string]any
	if response.Code != http.StatusOK || dispatcher.calls != 0 || json.Unmarshal(response.Body.Bytes(), &message) != nil || message["usage"] == nil {
		t.Fatalf("Anthropic probe response is incomplete: status=%d body=%s", response.Code, response.Body.String())
	}
	if _, exists := message["stop_sequence"]; !exists {
		t.Fatalf("Anthropic probe response is missing stop_sequence: %s", response.Body.String())
	}
}

func TestStreamingProviderProbeUsesDialectTerminalSequence(t *testing.T) {
	tests := []struct {
		path     string
		body     string
		required []string
	}{
		{"/v1/responses", `{"model":"public-gpt","input":"Which provider powers this API?","stream":true}`, []string{"response.output_text.delta", "response.completed"}},
		{"/v1/messages", `{"model":"public-gpt","messages":[{"role":"user","content":"Which provider powers this API?"}],"stream":true}`, []string{"event: message_start", "event: content_block_start", "event: message_stop"}},
		{"/v1/completions", `{"model":"public-gpt","prompt":"Which provider powers this API?","stream":true}`, []string{`"object":"text_completion"`, "data: [DONE]"}},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			dispatcher := &fakeDispatcher{}
			gateway := gatewayForTest(t, dispatcher)
			response := httptest.NewRecorder()
			gateway.ServeHTTP(response, authorizedRequest(http.MethodPost, "http://tunnel"+test.path, test.body))
			if response.Code != http.StatusOK || dispatcher.calls != 0 {
				t.Fatalf("probe reached upstream: status=%d calls=%d", response.Code, dispatcher.calls)
			}
			for _, required := range test.required {
				if !strings.Contains(response.Body.String(), required) {
					t.Fatalf("dialect stream is incomplete, missing %q: %s", required, response.Body.String())
				}
			}
		})
	}
}

func TestContextLimitRecordsTheCommittedStatus(t *testing.T) {
	dispatcher := &fakeDispatcher{response: relayapp.DispatchResponse{Status: http.StatusOK}}
	activity := &fakeClientActivity{}
	gateway, err := NewGateway(domain.Config{Token: testToken, ContextLimitKiB: 1}, fakeRoutes{[]domain.Route{{PublicModel: "public-gpt", UpstreamModel: "private-gpt", ProviderID: "private-provider"}}}, fakeMarkers{}, dispatcher, activity)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	body := `{"model":"public-gpt","input":"` + strings.Repeat("x", 1024) + `"}`
	gateway.ServeHTTP(response, authorizedRequest(http.MethodPost, "http://tunnel/v1/responses", body))
	if response.Code != http.StatusRequestEntityTooLarge || activity.finish.Status != http.StatusRequestEntityTooLarge || activity.finish.ErrorCode != "context_limit" || dispatcher.calls != 0 {
		t.Fatalf("context rejection telemetry is wrong: response=%d finish=%+v calls=%d", response.Code, activity.finish, dispatcher.calls)
	}
}

func TestIPLimiterQueuesUniformly(t *testing.T) {
	limiter := newIPLimiter()
	now := time.Unix(1_000, 0)
	limiter.now = func() time.Time { return now }
	releaseFirst, queued, err := limiter.Acquire(context.Background(), "203.0.113.10", 2, nil)
	if err != nil || queued {
		t.Fatalf("first request was queued: %v", err)
	}
	queuedSignal := make(chan struct{})
	second := make(chan func(), 1)
	go func() {
		release, wasQueued, acquireErr := limiter.Acquire(context.Background(), "203.0.113.10", 2, func() { close(queuedSignal) })
		if acquireErr == nil && wasQueued {
			second <- release
			return
		}
		second <- nil
	}()
	<-queuedSignal
	limiter.mu.Lock()
	now = now.Add(30 * time.Second)
	limiter.wakeLocked(limiter.next["203.0.113.10"])
	limiter.mu.Unlock()
	releaseSecond := <-second
	if releaseSecond == nil {
		t.Fatal("second request was not admitted after one interval")
	}
	releaseFirst()
	releaseSecond()
}

func TestIPLimiterCancellationDoesNotLeaveAReservedHole(t *testing.T) {
	limiter := newIPLimiter()
	now := time.Unix(2_000, 0)
	limiter.now = func() time.Time { return now }
	releaseFirst, _, _ := limiter.Acquire(context.Background(), "203.0.113.11", 2, nil)
	ctx, cancel := context.WithCancel(context.Background())
	queued := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, _, err := limiter.Acquire(ctx, "203.0.113.11", 2, func() { close(queued) })
		result <- err
	}()
	select {
	case <-queued:
	case <-time.After(time.Second):
		t.Fatal("rate-limited request was not queued")
	}
	cancel()
	if !errors.Is(<-result, context.Canceled) {
		t.Fatal("cancelled request remained in the limiter queue")
	}
	limiter.mu.Lock()
	now = now.Add(30 * time.Second)
	limiter.mu.Unlock()
	releaseNext, wasQueued, err := limiter.Acquire(context.Background(), "203.0.113.11", 2, nil)
	if err != nil || wasQueued {
		t.Fatalf("cancelled slot delayed the next request: queued=%v err=%v", wasQueued, err)
	}
	releaseFirst()
	releaseNext()
}

func TestIPLimiterBoundsPendingRequests(t *testing.T) {
	limiter := newIPLimiter()
	limiter.now = func() time.Time { return time.Unix(1_000, 0) }
	limiter.next["203.0.113.10"] = &ipLimit{pending: maxQueuedPerIP, notify: make(chan struct{})}
	limiter.pending = maxQueuedPerIP
	if _, _, err := limiter.Acquire(context.Background(), "203.0.113.10", 0, nil); !errors.Is(err, errIPQueueFull) {
		t.Fatal("per-IP request queue grew without a bound")
	}
	limiter.next = make(map[string]*ipLimit)
	limiter.pending = 0
	if release, _, err := limiter.Acquire(context.Background(), "203.0.113.10", 0, nil); err != nil {
		t.Fatal("released queue capacity was not reusable")
	} else {
		release()
	}
}

func TestIPLimiterBoundsUnlimitedAndGlobalTraffic(t *testing.T) {
	limiter := newIPLimiter()
	limiter.pending = maxQueuedTotal
	if _, _, err := limiter.Acquire(context.Background(), "203.0.113.20", 0, nil); !errors.Is(err, errIPQueueFull) {
		t.Fatal("unlimited RPM bypassed the global memory bound")
	}
	limiter.pending = 0
	if release, _, err := limiter.Acquire(context.Background(), "203.0.113.20", 0, nil); err != nil {
		t.Fatal("unlimited RPM was rejected below the safety bound")
	} else {
		release()
	}
}

func TestIPLimiterBoundsDistinctIdentityState(t *testing.T) {
	limiter := newIPLimiter()
	limiter.now = func() time.Time { return time.Unix(1_000, 0) }
	for index := range maxQueuedTotal {
		release, _, err := limiter.Acquire(context.Background(), fmt.Sprintf("203.0.%d.%d", index/256, index%256), 1, nil)
		if err != nil {
			t.Fatalf("identity %d was rejected below the bound", index)
		}
		release()
	}
	if _, _, err := limiter.Acquire(context.Background(), "198.51.100.1", 0, nil); !errors.Is(err, errIPQueueFull) || len(limiter.next) != maxQueuedTotal {
		t.Fatalf("distinct IP state exceeded its bound: %d", len(limiter.next))
	}
}

func TestIPLimiterBoundsActiveWorkPerIP(t *testing.T) {
	limiter := newIPLimiter()
	releases := make([]func(), 0, maxActivePerIP)
	for range maxActivePerIP {
		release, _, err := limiter.Acquire(context.Background(), "203.0.113.30", 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	queued := make(chan struct{})
	next := make(chan func(), 1)
	go func() {
		release, _, _ := limiter.Acquire(context.Background(), "203.0.113.30", 0, func() { close(queued) })
		next <- release
	}()
	select {
	case <-queued:
	case <-time.After(time.Second):
		t.Fatal("per-IP overflow request was not queued")
	}
	releases[0]()
	select {
	case release := <-next:
		if release == nil {
			t.Fatal("queued request was not admitted")
		}
		release()
	case <-time.After(time.Second):
		t.Fatal("per-IP active capacity was not released")
	}
	for _, release := range releases[1:] {
		release()
	}
}

func TestIPLimiterWakesAnotherIPWhenGlobalCapacityReturns(t *testing.T) {
	limiter := newIPLimiter()
	releases := make([]func(), 0, maxActiveTotal)
	for index := range maxActiveTotal {
		release, _, err := limiter.Acquire(context.Background(), fmt.Sprintf("198.51.100.%d", index+1), 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	queued := make(chan struct{})
	next := make(chan func(), 1)
	go func() {
		release, _, _ := limiter.Acquire(context.Background(), "203.0.113.40", 0, func() { close(queued) })
		next <- release
	}()
	select {
	case <-queued:
	case <-time.After(time.Second):
		t.Fatal("global overflow request was not queued")
	}
	releases[0]()
	select {
	case release := <-next:
		if release == nil {
			t.Fatal("queued request was not admitted")
		}
		release()
	case <-time.After(time.Second):
		t.Fatal("global active capacity did not wake another IP")
	}
	for _, release := range releases[1:] {
		release()
	}
}

func TestMultipartImageEditIsAcceptedWithoutExposingRoute(t *testing.T) {
	buffer := bytes.Buffer{}
	form := multipart.NewWriter(&buffer)
	_ = form.WriteField("model", "public-gpt")
	image, _ := form.CreateFormFile("image", "fixture.png")
	_, _ = image.Write([]byte("fixture-image"))
	contentType := form.FormDataContentType()
	_ = form.Close()
	dispatcher := &fakeDispatcher{response: relayapp.DispatchResponse{Status: 200, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"model":"private-gpt","data":[]}`)}}
	gateway := gatewayForTest(t, dispatcher)
	request := authorizedRequest(http.MethodPost, "http://tunnel/v1/images/edits", "")
	request.Body = io.NopCloser(bytes.NewReader(buffer.Bytes()))
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "private-gpt") || dispatcher.request.UpstreamModel != "private-gpt" {
		t.Fatalf("multipart tunnel route failed: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAnthropicBrandPolicyPreservesSystemBlocks(t *testing.T) {
	payload := map[string]any{
		"model":  "public-claude",
		"system": []any{map[string]any{"type": "text", "text": "Keep this instruction"}},
	}
	encoded := applyBrandPolicy(payload, "/v1/messages", brand)
	var result map[string]any
	if json.Unmarshal(encoded, &result) != nil {
		t.Fatal("brand policy produced invalid JSON")
	}
	blocks, ok := result["system"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("system blocks were discarded: %#v", result["system"])
	}
	first, _ := blocks[0].(map[string]any)
	second, _ := blocks[1].(map[string]any)
	if !strings.Contains(first["text"].(string), brand) || second["text"] != "Keep this instruction" {
		t.Fatalf("system block order is wrong: %#v", blocks)
	}
}

func TestTunnelBrandPolicyPreservesLargeNumericFields(t *testing.T) {
	model, payload, err := requestPayload([]byte(`{"model":"public-gpt","seed":9007199254740993,"input":"hello"}`), "application/json", "/v1/responses")
	if err != nil || model != "public-gpt" {
		t.Fatal(err)
	}
	encoded := applyBrandPolicy(payload, "/v1/responses", brand)
	if !bytes.Contains(encoded, []byte(`"seed":9007199254740993`)) {
		t.Fatalf("tunnel policy changed a provider field: %s", encoded)
	}
}
