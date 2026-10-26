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

// A token count answers how large the prompt is; nothing was generated or
// billed. The history keeps the counted size as the request's context, never
// as spent input.
func TestTokenCountIsRecordedAsContextNotSpend(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"input_tokens":12345}`)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	for _, path := range []string{"/v1/messages/count_tokens", "/V1/Messages/Count_Tokens/", "/messages/count_tokens"} {
		sink := &recordingActivity{}
		server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: sink, Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond}})
		_, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{Method: "POST", Path: path, ProviderID: "fixture", UpstreamModel: "fixture-model", AttemptLimit: 1, Body: []byte(`{"model":"fixture-model","messages":[{"role":"user","content":"fixture"}]}`), Headers: http.Header{"Content-Type": []string{"application/json"}}})
		if err != nil {
			t.Fatal(err)
		}
		if got := sink.finish.Usage; got != (relayapp.TokenUsage{ContextTokens: 12345}) {
			t.Fatalf("%s: counted tokens recorded as %+v", path, got)
		}
	}

	// An ordinary answer that happens to carry the same top-level field is
	// not a count.
	sink := &recordingActivity{}
	server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: sink, Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond}})
	_, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{Method: "POST", Path: "/v1/embeddings", ProviderID: "fixture", UpstreamModel: "fixture-model", AttemptLimit: 1, Body: []byte(`{"model":"fixture-model","input":"fixture"}`), Headers: http.Header{"Content-Type": []string{"application/json"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := sink.finish.Usage; got != (relayapp.TokenUsage{}) {
		t.Fatalf("non-count answer recorded as %+v", got)
	}
}
