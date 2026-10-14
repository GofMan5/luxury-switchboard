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

	activityapp "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	relayactivity "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/activity"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

func TestFinalChatCompatibilityVerdictReachesActivity(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, dispatch := range []bool{false, true} {
			for _, reason := range []string{"stop", "length", "content_filter"} {
				t.Run(fmt.Sprintf("stream=%v dispatch=%v reason=%s", stream, dispatch, reason), func(t *testing.T) {
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprintf(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"fixture answer"},"finish_reason":%q}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`, reason)
					}))
					defer upstream.Close()
					base, _ := url.Parse(upstream.URL)
					service := activityapp.NewService(10)
					server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer", Format: "chat"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: relayactivity.NewRecorder(service), Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 1}})
					body := []byte(fmt.Sprintf(`{"model":"fixture-model","input":"fixture","stream":%v}`, stream))
					if dispatch {
						_, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{Method: "POST", Path: "/v1/responses", ProviderID: "fixture", UpstreamModel: "fixture-model", AttemptLimit: 1, Body: body, Headers: http.Header{"Content-Type": []string{"application/json"}}})
						if err != nil {
							t.Fatal(err)
						}
					} else {
						r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(body)))
						r.Header.Set("Content-Type", "application/json")
						server.ServeHTTP(httptest.NewRecorder(), r)
					}
					row := service.List(1)[0]
					want := ""
					if reason != "stop" {
						want = "stream_incomplete"
						if row.State != "failed" {
							t.Fatalf("translated incomplete reported completed: %+v", row)
						}
					}
					if row.ErrorCode != want {
						t.Fatalf("client terminal %s stream=%v filed as %q instead of %q", reason, stream, row.ErrorCode, want)
					}
					if row.InputTokens != 11 || row.OutputTokens != 7 {
						t.Fatalf("translation lost billing: %+v", row)
					}
				})
			}
		}
	}
}
