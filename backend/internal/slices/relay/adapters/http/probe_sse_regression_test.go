package relayhttp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	activityapp "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	activitydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
	relayactivity "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/activity"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

func TestProbeRejectsInvalidResponsesCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, terminal, code string
	}{
		{"missing response", `{"type":"response.completed"}`, "upstream_status"},
		{"null response", `{"type":"response.completed","response":null}`, "upstream_status"},
		{"missing status", `{"type":"response.completed","response":{}}`, "upstream_status"},
		{"failed status", `{"type":"response.completed","response":{"status":"failed"}}`, "upstream_status"},
		{"incomplete details", `{"type":"response.completed","response":{"status":"completed","incomplete_details":{"reason":"max_output_tokens"}}}`, "upstream_status"},
		{"nested error", `{"type":"response.completed","response":{"status":"completed","error":{"message":"fixture failure"}}}`, "upstream_status"},
		{"foreign DONE", `[DONE]`, "stream_incomplete"},
		{"valid completed", `{"type":"response.completed","response":{"status":"completed","error":null,"incomplete_details":null}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: "+tc.terminal+"\n\n")
			}))
			defer upstream.Close()
			base, _ := url.Parse(upstream.URL)
			activity := activityapp.NewService(10)
			credentials := &credentialSource{values: []string{"fixture-key"}}
			server := NewServer("127.0.0.1:0", Dependencies{
				Routes:      fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}},
				Credentials: credentials, Activity: relayactivity.NewRecorder(activity),
				Config: Config{StreamIdleTimeout: time.Second},
			})
			report, err := server.Probe(context.Background(), "fixture", "fixture-model")
			if err != nil {
				t.Fatal(err)
			}
			row := activity.List(1)[0]
			wantState, wantOutcome := activitydomain.StateFailed, relayapp.AttemptRequestError
			if tc.code == "" {
				wantState, wantOutcome = activitydomain.StateCompleted, relayapp.AttemptSuccess
			}
			if report.ErrorCode != tc.code || row.ErrorCode != tc.code || row.State != wantState || len(credentials.outcomes) != 1 || credentials.outcomes[0].Kind != wantOutcome {
				t.Fatalf("invalid completion reported as available: report=%+v row=%+v outcomes=%+v", report, row, credentials.outcomes)
			}
		})
	}
}

func TestProbeMeasuresContentAndUsageInTheSameChatEvent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		time.Sleep(20 * time.Millisecond)
		fmt.Fprint(w, `data: {"choices":[{"delta":{"reasoning_content":"thinking"}}],"usage":{"prompt_tokens":100,"completion_tokens":17,"total_tokens":117}}`+"\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(20 * time.Millisecond)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	activity := activityapp.NewService(10)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer", Format: "chat"}},
		Credentials: &credentialSource{values: []string{"fixture-key"}}, Activity: relayactivity.NewRecorder(activity),
		Config: Config{StreamIdleTimeout: time.Second},
	})
	report, err := server.Probe(context.Background(), "fixture", "fixture-model")
	if err != nil {
		t.Fatal(err)
	}
	row := activity.List(1)[0]
	if report.ErrorCode != "" || report.OutputTokens != 17 || report.TTFTMs <= 0 || report.TTFTMs >= report.TotalMs || row.InputTokens != 100 || row.OutputTokens != 17 || row.TotalTokens != 117 {
		t.Fatalf("usage hid the first reasoning token: report=%+v row=%+v", report, row)
	}
}

func TestProbeReadsOneEventAcrossMultipleDataLines(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": keep-alive\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(20 * time.Millisecond)
		fmt.Fprint(w, "data: {\"type\":\"response.reasoning_text.delta\",\r\ndata: \"delta\":\"thinking\"}\r\n\r\n")
		w.(http.Flusher).Flush()
		time.Sleep(20 * time.Millisecond)
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\ndata: \"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":100,\"output_tokens\":17,\"total_tokens\":117}}}\n\n")
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	activity := activityapp.NewService(10)
	credentials := &credentialSource{values: []string{"fixture-key"}}
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer"}},
		Credentials: credentials, Activity: relayactivity.NewRecorder(activity),
		Config: Config{StreamIdleTimeout: time.Second},
	})
	report, err := server.Probe(context.Background(), "fixture", "fixture-model")
	if err != nil {
		t.Fatal(err)
	}
	row := activity.List(1)[0]
	if report.ErrorCode != "" || row.State != activitydomain.StateCompleted || report.OutputTokens != 17 || row.InputTokens != 100 || row.OutputTokens != 17 || row.TotalTokens != 117 {
		t.Fatalf("multidata framing changed terminal/billing: report=%+v row=%+v", report, row)
	}
	if report.TTFTMs < 15 || report.TTFTMs >= report.TotalMs || row.GenerationMS <= 0 {
		t.Fatalf("multidata reasoning lost first-token timing: report=%+v row=%+v", report, row)
	}
	if len(credentials.outcomes) != 1 || credentials.outcomes[0].Kind != relayapp.AttemptSuccess {
		t.Fatalf("multidata event retained or failed its lease: %+v", credentials.outcomes)
	}
}
