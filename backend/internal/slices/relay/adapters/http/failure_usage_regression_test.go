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

func TestIncompleteBufferedStreamRetainsReportedUsage(t *testing.T) {
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		if attempts == 1 {
			fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fixture\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":15,\"completion_tokens\":3,\"total_tokens\":18}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	sink := &recordingActivity{}
	server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: sink, Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond}})
	_, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{Method: "POST", Path: "/v1/chat/completions", ProviderID: "fixture", UpstreamModel: "fixture-model", AttemptLimit: 2, Body: []byte(`{"model":"fixture-model","stream":true,"messages":[{"role":"user","content":"fixture"}]}`), Headers: http.Header{"Content-Type": []string{"application/json"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := sink.finish.Usage; got.InputTokens != 25 || got.OutputTokens != 5 || got.TotalTokens != 30 || got.ContextTokens != 15 {
		t.Fatalf("incomplete stream usage vanished: %+v", got)
	}
}

func TestJSONReturnedInsteadOfSSEStillCarriesBilledUsage(t *testing.T) {
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fixture\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":15,\"completion_tokens\":3,\"total_tokens\":18}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	sink := &recordingActivity{}
	server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: sink, Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond}})
	_, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{Method: "POST", Path: "/v1/chat/completions", ProviderID: "fixture", UpstreamModel: "fixture-model", AttemptLimit: 2, Body: []byte(`{"model":"fixture-model","stream":true,"messages":[{"role":"user","content":"fixture"}]}`), Headers: http.Header{"Content-Type": []string{"application/json"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := sink.finish.Usage; attempts != 2 || got.InputTokens != 25 || got.OutputTokens != 5 || got.TotalTokens != 30 || got.ContextTokens != 15 {
		t.Fatalf("wrong-framed JSON generation was billed but erased: sends=%d usage=%+v", attempts, got)
	}
}

func TestDiscardedBilledUsageSurvivesTerminalRetryFailure(t *testing.T) {
	for _, kind := range []string{"status", "transport"} {
		t.Run(kind, func(t *testing.T) {
			attempts := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if attempts == 1 {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"status":"failed","error":{"message":"fixture failed"},"usage":{"input_tokens":100,"output_tokens":17,"total_tokens":117}}`)
					return
				}
				if kind == "transport" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
					return
				}
				w.WriteHeader(503)
			}))
			defer upstream.Close()
			base, _ := url.Parse(upstream.URL)
			sink := &recordingActivity{}
			server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: sink, Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 2}})
			_, _ = server.Dispatch(context.Background(), relayapp.DispatchRequest{Method: "POST", Path: "/v1/responses", ProviderID: "fixture", UpstreamModel: "fixture-model", AttemptLimit: 2, Body: []byte(`{"model":"fixture-model"}`), Headers: http.Header{"Content-Type": []string{"application/json"}}})
			if got := sink.finish.Usage; got.InputTokens != 100 || got.OutputTokens != 17 || got.TotalTokens != 117 || got.ContextTokens != 100 {
				t.Fatalf("billed failed generation vanished on %s: %+v", kind, got)
			}
		})
	}
}
