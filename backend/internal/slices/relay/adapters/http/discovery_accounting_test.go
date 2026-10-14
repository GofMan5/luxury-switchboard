package relayhttp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

func TestEndpointDiscoveryRespectsAttemptBudgetAndCountsRetry(t *testing.T) {
	for _, limit := range []int{1, 3} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			sends := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends++
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path != "/chat-completion" {
					w.WriteHeader(404)
					fmt.Fprint(w, `{"error":{"message":"endpoint not found"}}`)
					return
				}
				fmt.Fprint(w, `{"choices":[{"index":0,"message":{"content":"fixture"},"finish_reason":"stop"}]}`)
			}))
			defer upstream.Close()
			base, _ := url.Parse(upstream.URL)
			sink := &recordingActivity{}
			server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer", Format: "auto"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: sink, Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond}})
			_, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{Method: "POST", Path: "/v1/responses", ProviderID: "fixture", UpstreamModel: "fixture-model", AttemptLimit: limit, Body: []byte(`{"model":"fixture-model","input":"fixture"}`), Headers: http.Header{"Content-Type": []string{"application/json"}}})
			if err != nil {
				t.Fatal(err)
			}
			if sends != limit || len(sink.retries) != limit-1 {
				t.Fatalf("discovery violates accounting/budget: limit=%d sends=%d retries=%+v", limit, sends, sink.retries)
			}
		})
	}
}

func TestNativePolicyRefusalRetainsDistinctActivityVerdict(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"content_policy_violation\",\"message\":\"content policy refusal\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":2,\"total_tokens\":12}}}\n\n")
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	sink := &recordingActivity{}
	server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: sink, Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond}})
	response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{Method: "POST", Path: "/v1/responses", ProviderID: "fixture", UpstreamModel: "fixture-model", AttemptLimit: 2, Body: []byte(`{"model":"fixture-model","input":"fixture","stream":true}`), Headers: http.Header{"Content-Type": []string{"application/json"}}})
	if err != nil {
		t.Fatal(err)
	}
	if sink.finish.ErrorCode != "policy_refusal" || response.Terminal != terminalRefused {
		t.Fatalf("native refusal misclassified: response=%+v finish=%+v", response, sink.finish)
	}
}
