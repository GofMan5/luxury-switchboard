package relayhttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

func TestRotationNeverFilesPreviousCredentialsErrorForFinalRateLimit(t *testing.T) {
	const first = "fixture-long-first-secret"
	const second = "fixture-long-second-secret"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+first {
			w.WriteHeader(401)
			fmt.Fprintf(w, `{"error":{"message":"invalid credential %s"}}`, first)
			return
		}
		w.WriteHeader(429)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	sink := &recordingActivity{}
	server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}}, Credentials: &countingCredentialSource{credentialSource: credentialSource{values: []string{first, second}}, keys: 2}, Activity: sink, Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond}})
	response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{Method: "POST", Path: "/v1/responses", ProviderID: "fixture", UpstreamModel: "fixture-model", AttemptLimit: 2, Body: []byte(`{"model":"fixture-model"}`), Headers: http.Header{"Content-Type": []string{"application/json"}}})
	if err != nil || response.Status != 429 {
		t.Fatalf("status=%d err=%v", response.Status, err)
	}
	if strings.Contains(sink.finish.ErrorDetail, first) || strings.Contains(sink.finish.ErrorDetail, "invalid credential") {
		t.Fatalf("previous auth error leaked/misattributed: %+v", sink.finish)
	}
}

type failingFixtureTransport struct{}

func (failingFixtureTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("fixture dial failed")
}

func TestTransportFailureDoesNotPersistURLQueryOrCredential(t *testing.T) {
	base, _ := url.Parse("http://127.0.0.1:1?trace=fixture-private-query")
	sink := &recordingActivity{}
	server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: sink, Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond}})
	server.client = &http.Client{Transport: failingFixtureTransport{}}
	_, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{Method: "POST", Path: "/v1/responses?trace=fixture-private-query", ProviderID: "fixture", AttemptLimit: 1, UpstreamModel: "fixture-model", Body: []byte(`{"model":"fixture-model"}`), Headers: http.Header{"Content-Type": []string{"application/json"}}})
	if err == nil {
		t.Fatal("fixture did not fail")
	}
	if strings.Contains(sink.finish.ErrorDetail, "fixture-private-query") || strings.Contains(sink.finish.ErrorDetail, "http://") || strings.Contains(sink.finish.ErrorDetail, "fixture-secret") {
		t.Fatalf("transport detail persisted URL/query: %+v", sink.finish)
	}
}

func TestOwnerDetailsNeverReflectEvenShortCredential(t *testing.T) {
	const secret = "abc123"
	if got := upstreamErrorDetail(`{"error":{"message":"invalid abc123"}}`, []string{secret}, nil); strings.Contains(got, secret) {
		t.Fatalf("short secret persisted: %q", got)
	}
	if !containsSecret("header abc123", []string{secret}) {
		t.Fatal("short credential passed header guard")
	}
}

func TestUnrecognizedStructuredErrorIsNotRawBodyHistory(t *testing.T) {
	if got := upstreamErrorDetail(`{"debug":{"request":"fixture-private-body"}}`, nil, nil); got != "" {
		t.Fatalf("raw structured body filed: %q", got)
	}
	if got := upstreamErrorDetail("catalog temporarily unavailable", nil, nil); got != "catalog temporarily unavailable" {
		t.Fatalf("plain safe reason lost: %q", got)
	}
}

func TestRetryAfterNumericCannotOverflowDuration(t *testing.T) {
	for _, value := range []string{"9223372037", "1e100", "30", "0.05"} {
		response := &http.Response{Header: http.Header{"Retry-After": []string{value}}}
		want := 30 * time.Second
		if value == "0.05" {
			want = 50 * time.Millisecond
		}
		if got := retryDelay(0, response, Config{RetryMax: 30 * time.Second, RetryBase: time.Millisecond}); got != want {
			t.Fatalf("header=%q delay=%v want=%v", value, got, want)
		}
	}
}

func TestDispatchTerminalFailuresAreNeverCompletedActivity(t *testing.T) {
	for _, terminal := range []string{"response.failed", "response.incomplete"} {
		t.Run(terminal, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "event: %s\ndata: {\"type\":\"%s\",\"response\":{\"status\":\"%s\",\"error\":{\"message\":\"fixture terminal failure\"}}}\n\n", terminal, terminal, strings.TrimPrefix(terminal, "response."))
			}))
			defer upstream.Close()
			base, _ := url.Parse(upstream.URL)
			sink := &recordingActivity{}
			server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: sink, Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 1}})
			_, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{Method: "POST", Path: "/v1/responses", ProviderID: "fixture", UpstreamModel: "fixture-model", AttemptLimit: 1, Body: []byte(`{"model":"fixture-model","stream":true}`), Headers: http.Header{"Content-Type": []string{"application/json"}}})
			if err != nil {
				t.Fatal(err)
			}
			if sink.finish.ErrorCode == "" {
				t.Fatalf("terminal failure filed as success: %+v", sink.finish)
			}
		})
	}
}
