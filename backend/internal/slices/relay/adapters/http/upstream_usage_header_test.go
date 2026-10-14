package relayhttp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

func TestRawAndLiveUpstreamHeadersNeverOwnRelayAccounting(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "raw", true: "live"}[live], func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Switchboard-Input-Tokens", "999")
				w.Header().Set("X-Switchboard-Total-Tokens", "999")
				w.Header().Set("X-Switchboard-Terminal", "response.incomplete")
				if !live {
					w.Header().Set("Content-Type", "text/plain")
					fmt.Fprint(w, "fixture")
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fixture\"}}]}\n\n")
				w.(http.Flusher).Flush()
				time.Sleep(5 * time.Millisecond)
				fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\ndata: [DONE]\n\n")
			}))
			defer upstream.Close()
			base, _ := url.Parse(upstream.URL)
			sink := &recordingActivity{}
			server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: sink, Config: Config{LiveStreamProbation: time.Millisecond}})
			path := "/fixture"
			body := `{"model":"fixture-model"}`
			want := int64(0)
			if live {
				path = "/v1/chat/completions"
				body = `{"model":"fixture-model","stream":true,"messages":[{"role":"user","content":"fixture"}]}`
				want = 10
			}
			r := httptest.NewRequest("POST", path, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			server.ServeHTTP(httptest.NewRecorder(), r)
			if sink.finish.Usage.InputTokens != want || sink.finish.ErrorCode != "" {
				t.Fatalf("upstream headers spoofed %v accounting: %+v", live, sink.finish)
			}
		})
	}
}

func TestLiveHandoffCarriesDiscardedUsageAndDurationExactlyOnce(t *testing.T) {
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		if attempts == 1 {
			// Hold headers too: the whole failed event is buffered before the
			// probation starts, instead of accidentally becoming a live failure.
			time.Sleep(10 * time.Millisecond)
			fmt.Fprint(w, "data: {\"error\":{\"message\":\"fixture retry\"},\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n")
			return
		}
		w.Header().Set("X-Switchboard-Input-Tokens", "999")
		w.Header().Set("X-Switchboard-Output-Tokens", "999")
		w.Header().Set("X-Switchboard-Generation-Nanoseconds", "999")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fixture\"}}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":3,\"total_tokens\":23}}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(20 * time.Millisecond)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	sink := &recordingActivity{}
	server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: sink, Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 2, LiveStreamProbation: 10 * time.Millisecond}})
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fixture-model","stream":true,"messages":[{"role":"user","content":"fixture"}]}`))
	r.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(httptest.NewRecorder(), r)
	if got := sink.finish.Usage; attempts != 2 || got.InputTokens != 30 || got.OutputTokens != 5 || got.TotalTokens != 35 || got.ContextTokens != 20 || sink.finish.Generation < 30*time.Millisecond {
		t.Fatalf("live carry duplicated or lost: sends=%d finish=%+v", attempts, sink.finish)
	}
}

func TestUpstreamHeadersCannotReplaceRelayOwnedAccounting(t *testing.T) {
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		if attempts == 1 {
			fmt.Fprint(w, `{"status":"failed","error":{"message":"fixture"},"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`)
			return
		}
		w.Header().Set("X-Switchboard-Input-Tokens", "999")
		w.Header().Set("X-Switchboard-Total-Tokens", "999")
		w.Header().Set("X-Switchboard-Terminal", "response.incomplete")
		w.WriteHeader(503)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	sink := &recordingActivity{}
	server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: sink, Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 2}})
	_, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{Method: "POST", Path: "/v1/responses", ProviderID: "fixture", UpstreamModel: "fixture-model", AttemptLimit: 2, Body: []byte(`{"model":"fixture-model"}`), Headers: http.Header{"Content-Type": []string{"application/json"}}})
	if err != nil {
		t.Fatal(err)
	}
	if sink.finish.Usage.InputTokens != 10 || sink.finish.Usage.TotalTokens != 12 || sink.finish.ErrorCode != "request_rejected" {
		t.Fatalf("upstream header overrode relay state: %+v", sink.finish)
	}
}
