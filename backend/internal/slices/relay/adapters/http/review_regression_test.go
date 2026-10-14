package relayhttp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	guardraildomain "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

func failoverFixture(t *testing.T, primary, sibling *httptest.Server, firstFormat, nextFormat string) (*Server, *switchingFailovers) {
	t.Helper()
	firstURL, _ := url.Parse(primary.URL)
	nextURL, _ := url.Parse(sibling.URL)
	routes := map[string]relayapp.Route{
		"echo":    {ProviderID: "echo", ProviderName: "First", BaseURL: firstURL, AuthMode: "bearer", Format: firstFormat, UpstreamModel: "first-model"},
		"sibling": {ProviderID: "sibling", ProviderName: "Sibling", BaseURL: nextURL, AuthMode: "bearer", Format: nextFormat, UpstreamModel: "next-model"},
	}
	chain := &recordingChain{routes: []relayapp.ModelRoute{{ProviderID: "echo"}, {ProviderID: "sibling"}}}
	failovers := &switchingFailovers{chain: chain, routes: routes}
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes: multiRoute{routes: routes, first: "echo"}, Failovers: failovers,
		Credentials: &credentialSource{values: []string{"fixture-key"}},
		Config:      Config{PermanentAttempts: 1, RetryBase: time.Millisecond, RetryMax: time.Millisecond},
	})
	return server, failovers
}

func refusedPrimary() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(w, `{"error":{"message":"Budget pool quota has been exhausted"}}`)
	}))
}

type handoffRecorder struct {
	*httptest.ResponseRecorder
	terminalSent *atomic.Bool
	handedOff    bool
}

func (writer *handoffRecorder) Write(body []byte) (int, error) {
	if strings.Contains(string(body), "response.output_item.added") && !writer.terminalSent.Load() {
		writer.handedOff = true
	}
	return writer.ResponseRecorder.Write(body)
}

func TestF01LiveFailoverUsesServingProviderAndActivity(t *testing.T) {
	for _, mode := range []guardraildomain.Mode{guardraildomain.ModeMonitor, guardraildomain.ModeBlock} {
		t.Run(string(mode), func(t *testing.T) {
			primary := refusedPrimary()
			defer primary.Close()
			var terminalSent atomic.Bool
			sibling := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				time.Sleep(5 * time.Millisecond)
				fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"status\":\"in_progress\"}}\n\n")
				w.(http.Flusher).Flush()
				time.Sleep(40 * time.Millisecond)
				terminalSent.Store(true)
				fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", maliciousCompletion)
			}))
			defer sibling.Close()
			guarded, inspector := guardedServer(t, guardraildomain.ModeMonitor, primary.URL)
			if mode == guardraildomain.ModeBlock {
				inspector.SetProviderModes(map[string]string{"sibling": "block"})
			} else {
				inspector.SetMode(guardraildomain.ModeBlock)
				inspector.SetProviderModes(map[string]string{"sibling": "monitor"})
			}
			server, _ := failoverFixture(t, primary, sibling, "responses", "responses")
			server.guardrail = guarded.guardrail
			server.config.LiveStreamProbation = time.Millisecond
			activity := &recordingActivity{}
			server.activity = activity
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(strings.TrimSuffix(declaredTools, "}")+`,"stream":true}`))
			request.Header.Set("Content-Type", "application/json")
			response := &handoffRecorder{ResponseRecorder: httptest.NewRecorder(), terminalSent: &terminalSent}
			server.ServeHTTP(response, request)
			if response.handedOff != (mode == guardraildomain.ModeMonitor) {
				t.Fatalf("live handoff policy wrong: %+v", response.handedOff)
			}
			if strings.Contains(response.Body.String(), "curl") != (mode == guardraildomain.ModeMonitor) {
				t.Fatal("serving-provider guardrail policy wrong")
			}
			if activity.finish.ProviderID != "sibling" || activity.finish.ProviderName != "Sibling" {
				t.Fatalf("live activity attribution wrong: %+v", activity.finish)
			}
			records := inspector.Records(0)
			if len(records) != 1 || records[0].ProviderID != "sibling" {
				t.Fatalf("live finding attribution wrong: %+v", records)
			}
		})
	}
}

func TestF01FailoverUsesServingProviderGuardrailPolicy(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, mode := range []guardraildomain.Mode{guardraildomain.ModeMonitor, guardraildomain.ModeBlock} {
			t.Run(fmt.Sprintf("stream=%v/sibling=%s", stream, mode), func(t *testing.T) {
				primary := refusedPrimary()
				defer primary.Close()
				sibling := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", maliciousCompletion)
					} else {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, maliciousCompletion)
					}
				}))
				defer sibling.Close()
				guarded, inspector := guardedServer(t, guardraildomain.ModeMonitor, primary.URL)
				if mode == guardraildomain.ModeBlock {
					inspector.SetProviderModes(map[string]string{"sibling": "block"})
				} else {
					inspector.SetMode(guardraildomain.ModeBlock)
					inspector.SetProviderModes(map[string]string{"sibling": "monitor"})
				}
				server, _ := failoverFixture(t, primary, sibling, "responses", "responses")
				server.guardrail = guarded.guardrail
				activity := &recordingActivity{}
				server.activity = activity
				body := strings.TrimSuffix(declaredTools, "}") + fmt.Sprintf(",\"stream\":%v}", stream)
				response := postThrough(server, body)
				leaked := strings.Contains(response.Body.String(), "curl")
				if leaked != (mode == guardraildomain.ModeMonitor) {
					t.Fatalf("serving-provider policy ignored: mode=%s status=%d body=%s", mode, response.Code, response.Body.String())
				}
				if activity.finish.ProviderID != "sibling" || activity.finish.ProviderName != "Sibling" {
					t.Fatalf("activity attributed to primary instead of serving route: %+v", activity.finish)
				}
				records := inspector.Records(0)
				if len(records) != 1 || records[0].ProviderID != "sibling" || records[0].ProviderName != "Sibling" {
					t.Fatalf("answer attributed to the wrong provider: %+v", records)
				}
			})
		}
	}
}
