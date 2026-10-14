package relayhttp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

func TestCommittedTerminalRejectionKeepsEarlierUsage(t *testing.T) {
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		time.Sleep(20 * time.Millisecond)
		if attempts == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"fixture\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":2,\"total_tokens\":12}}}\n\n")
			return
		}
		w.WriteHeader(503)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	sink := &recordingActivity{}
	server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: sink, Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 2, HeartbeatInterval: time.Millisecond, LiveStreamProbation: time.Second}})
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"fixture-model","input":"fixture","stream":true}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "response.failed") {
		t.Fatalf("fixture did not commit failing stream: %d %q", w.Code, w.Body.String())
	}
	if got := sink.finish.Usage; got.InputTokens != 10 || got.OutputTokens != 2 || got.TotalTokens != 12 {
		t.Fatalf("committed rejection erased billed usage: %+v", sink.finish)
	}
}
