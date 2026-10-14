package relayhttp

import (
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

type fixtureRejectOnce struct{ calls int }

func (g *fixtureRejectOnce) Review([]byte, bool, relayapp.GuardrailSubject) relayapp.GuardrailVerdict {
	g.calls++
	if g.calls == 1 {
		return relayapp.GuardrailVerdict{Blocked: true, Code: "guardrail_blocked"}
	}
	return relayapp.GuardrailVerdict{}
}
func (*fixtureRejectOnce) ClientDeclaredTools([]byte) bool { return false }
func (*fixtureRejectOnce) CanBlockFor(string) bool         { return true }

func TestNestedRetryLaddersCountEveryRetryAndBilledGeneration(t *testing.T) {
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		if attempts == 1 {
			fmt.Fprint(w, `{"status":"failed","error":{"message":"fixture failure"},"usage":{"input_tokens":100,"output_tokens":100,"total_tokens":200}}`)
			return
		}
		fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"fixture"}]}],"usage":{"input_tokens":100,"output_tokens":100,"total_tokens":200}}`)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	service := activityapp.NewService(10)
	server := NewServer("127.0.0.1:0", Dependencies{Routes: fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}}, Credentials: &credentialSource{values: []string{"fixture-secret"}}, Activity: relayactivity.NewRecorder(service), Guardrail: &fixtureRejectOnce{}, Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 2}})
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"fixture-model","input":"fixture"}`))
	r.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(httptest.NewRecorder(), r)
	row := service.List(1)[0]
	if attempts != 3 || row.Retries != 2 {
		t.Errorf("nested ladders undercount retries: sends=%d row=%+v", attempts, row)
	}
	if row.InputTokens != 300 || row.OutputTokens != 300 || row.TotalTokens != 600 || row.ContextTokens != 100 {
		t.Errorf("billed totals/peak wrong: %+v", row)
	}
	if row.GenerationMS < 60 {
		t.Errorf("aggregate tokens divided by final-only generation: %+v", row)
	}
}
