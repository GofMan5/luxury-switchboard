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

func TestCompleteReportedJSONUsageSurvivesHTTPTruncation(t *testing.T) {
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		time.Sleep(10 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		if attempts == 1 {
			w.Header().Set("Content-Length", "1000")
			fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"fixture"}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`)
			return
		}
		fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"fixture"}]}],"usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23}}`)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	sink := &recordingActivity{}
	server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: sink, Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond}})
	_, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{Method: "POST", Path: "/v1/responses", ProviderID: "fixture", UpstreamModel: "fixture-model", AttemptLimit: 2, Body: []byte(`{"model":"fixture-model","input":"fixture"}`), Headers: http.Header{"Content-Type": []string{"application/json"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := sink.finish.Usage; got.InputTokens != 30 || got.OutputTokens != 5 || got.TotalTokens != 35 || sink.finish.Generation < 20*time.Millisecond {
		t.Fatalf("read failure erased generated cost: %+v", sink.finish)
	}
}

func TestInvalidFallbackJSONIsBilledWithoutForwarding(t *testing.T) {
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 3 {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if attempts == 4 {
			fmt.Fprint(w, `{"status":"in_progress","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`)
			return
		}
		fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"fixture"}]}],"usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23}}`)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	sink := &recordingActivity{}
	server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: sink, Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond}})
	_, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{Method: "POST", Path: "/v1/responses", ProviderID: "fixture", UpstreamModel: "fixture-model", AttemptLimit: 5, Body: []byte(`{"model":"fixture-model","input":"fixture","stream":true}`), Headers: http.Header{"Content-Type": []string{"application/json"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := sink.finish.Usage; attempts != 5 || got.InputTokens != 30 || got.OutputTokens != 5 || got.TotalTokens != 35 {
		t.Fatalf("fallback refusal erased generated cost: sends=%d finish=%+v", attempts, sink.finish)
	}
}
