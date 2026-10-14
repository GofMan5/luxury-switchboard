package relayhttp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	guardraildomain "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

func TestBrokenLiveLeaseNeverReportsSuccess(t *testing.T) {
	for _, completed := range []bool{false, true} {
		name := "broken"
		if completed {
			name = "completed"
		}
		t.Run(name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"fixture\"}}]}\n\n")
				w.(http.Flusher).Flush()
				time.Sleep(20 * time.Millisecond)
				if completed {
					io.WriteString(w, "data: [DONE]\n\n")
				}
			}))
			defer upstream.Close()
			sink := &recordingActivity{}
			server, _ := guardedServerWatchedBy(t, guardraildomain.ModeMonitor, upstream.URL, sink)
			base, _ := url.Parse(upstream.URL)
			server.routes = fixedRoute{route: relayapp.Route{ProviderID: "echo", BaseURL: base, AuthMode: "bearer"}}
			source := &countingCredentialSource{credentialSource: credentialSource{values: []string{"fixture-secret"}}, keys: 1}
			server.credentials = source
			server.config.LiveStreamProbation = time.Millisecond
			request := httptest.NewRequest("POST", "http://relay/v1/chat/completions", strings.NewReader(`{"model":"fixture-model","stream":true,"messages":[{"role":"user","content":"fixture"}]}`))
			request.Header.Set("Content-Type", "application/json")
			server.ServeHTTP(httptest.NewRecorder(), request)
			if len(source.outcomes) != 1 {
				t.Fatalf("lease wasn't finished exactly once: %+v", source.outcomes)
			}
			want := relayapp.AttemptTransport
			if completed {
				want = relayapp.AttemptSuccess
			}
			if source.outcomes[0].Kind != want {
				t.Fatalf("live completion=%v outcome=%+v want=%v", completed, source.outcomes, want)
			}
		})
	}
}
