package relayhttp

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	activityapp "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	activitydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
	guardraildomain "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
	keyapp "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/application"
	keydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
	relayactivity "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/activity"
	relaykeypool "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/keypool"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

var _ relayapp.ActivitySink = (*relayactivity.Recorder)(nil)

func TestProbeClassifiesBodyVerdictsThroughRealKeyScheduler(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		body, outcome string
		blocked, auth int
		cooldown      bool
	}{
		{"model entitlement", 403, `{"error":{"message":"fixture-model is not available on your plan"}}`, "model-unavailable", 1, 0, false},
		{"client blocked", 403, `{"error":{"message":"unauthorized client detected for fixture-model"}}`, "request-error", 0, 0, false},
		{"policy refusal", 403, `{"error":{"code":"content_policy_violation","message":"content policy rejected fixture-model"}}`, "request-error", 0, 0, false},
		{"missing model", 403, `{"error":{"message":"fixture-model does not exist"}}`, "request-error", 0, 0, false},
		{"shared capacity", 402, `{"error":{"message":"Budget pool quota has been exhausted"}}`, "request-error", 0, 0, false},
		{"real authentication", 401, `{"error":{"code":"token_revoked","message":"Your token was revoked"}}`, "authentication", 0, 1, true},
		{"real billing", 402, `{"error":{"message":"insufficient balance"}}`, "balance-exhausted", 0, 0, true},
		{"body throttling", 503, `{"error":{"message":"rate limit exceeded"}}`, "rate-limited", 0, 0, true},
		{"status throttling", 429, `{"error":{"message":"busy"}}`, "rate-limited", 0, 0, true},
		{"server fault", 500, `{"error":{"message":"internal fault"}}`, "server-error", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheduler := keyapp.NewScheduler(10)
			key, err := keydomain.NewKey(keydomain.Params{ProviderID: "fixture", Label: "Synthetic", Secret: "fixture-key"})
			if err != nil {
				t.Fatal(err)
			}
			if err := scheduler.Configure("fixture", 0, 0, []keydomain.Key{key}); err != nil {
				t.Fatal(err)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer upstream.Close()
			base, _ := url.Parse(upstream.URL)
			activity := activityapp.NewService(10)
			server := NewServer("127.0.0.1:0", Dependencies{
				Routes:      fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer", Format: "chat"}},
				Credentials: relaykeypool.NewSource(scheduler), Activity: relayactivity.NewRecorder(activity),
				Config: Config{StreamIdleTimeout: time.Second},
			})
			report, err := server.Probe(context.Background(), "fixture", "fixture-model")
			if err != nil {
				t.Fatal(err)
			}
			if report.Status != tc.status || report.ErrorCode != "request_rejected" {
				t.Fatalf("refused attempt report=%+v", report)
			}
			state := scheduler.Snapshot("fixture")[0]
			if state.StartsInWindow != 1 || state.LastOutcome != tc.outcome || state.BlockedModels != tc.blocked || state.AuthStreak != tc.auth || (state.CooldownMS > 0) != tc.cooldown {
				t.Fatalf("probe verdict damaged unrelated key availability: %+v; want outcome=%s blocked=%d auth=%d cooldown=%t", state, tc.outcome, tc.blocked, tc.auth, tc.cooldown)
			}
			if !tc.cooldown {
				lease, ok := scheduler.TryAcquire("fixture", "other-model")
				if !ok {
					t.Fatal("request-local refusal cooled the key for an unrelated model")
				}
				lease.Finish(keydomain.Outcome{Kind: keydomain.OutcomeSuccess})
			}
		})
	}
}

func TestProbeAccountingUsesExistingUsageParsersAcrossDialects(t *testing.T) {
	for _, tc := range []struct {
		name, format, dialect, contentType, body string
		input, output, cached, reasoning, total  int64
		code                                     string
	}{
		{"responses usage", "responses", "", "text/event-stream", `data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":100,"output_tokens":17,"total_tokens":117,"input_tokens_details":{"cached_tokens":30},"output_tokens_details":{"reasoning_tokens":7}}}}` + "\n\n", 100, 17, 30, 7, 117, ""},
		{"anthropic cache components", "", "anthropic", "text/event-stream", `data: {"type":"message_start","message":{"usage":{"input_tokens":60,"cache_read_input_tokens":30,"cache_creation_input_tokens":10}}}` + "\n\n" + `data: {"type":"content_block_delta","delta":{"text":"OK"}}` + "\n\n" + `data: {"type":"message_delta","usage":{"output_tokens":17}}` + "\n\n" + `data: {"type":"message_stop"}` + "\n\n", 100, 17, 30, 0, 117, ""},
		{"plain JSON failure", "responses", "", "application/json", `{"status":"failed","error":{"message":"unavailable"},"usage":{"input_tokens":100,"output_tokens":17,"total_tokens":117}}`, 100, 17, 0, 0, 117, "upstream_status"},
		{"frame estimate is not billed usage", "responses", "", "text/event-stream", `data: {"type":"response.output_text.delta","delta":"OK"}` + "\n\n" + `data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n", 0, 0, 0, 0, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				// Immediate loopback can finish inside one Windows clock tick.
				// Make the positive generation-time assertion measure real waiting.
				time.Sleep(20 * time.Millisecond)
				fmt.Fprint(w, tc.body)
			}))
			defer upstream.Close()
			base, _ := url.Parse(upstream.URL)
			activity := activityapp.NewService(10)
			server := NewServer("127.0.0.1:0", Dependencies{
				Routes:      fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer", Format: tc.format, Dialect: tc.dialect}},
				Credentials: &credentialSource{values: []string{"fixture-key"}}, Activity: relayactivity.NewRecorder(activity),
				Config: Config{StreamIdleTimeout: time.Second},
			})
			report, err := server.Probe(context.Background(), "fixture", "fixture-model")
			if err != nil {
				t.Fatal(err)
			}
			row := activity.List(1)[0]
			if report.ErrorCode != tc.code || row.ErrorCode != tc.code || row.InputTokens != tc.input || row.OutputTokens != tc.output || row.CachedTokens != tc.cached || row.ReasoningTokens != tc.reasoning || row.TotalTokens != tc.total || row.GenerationMS <= 0 {
				t.Fatalf("dialect usage mismatch: report=%+v row=%+v", report, row)
			}
		})
	}
}

func TestProbeBillsGuardrailBlockedAnswer(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(time.Millisecond)
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":100,"output_tokens":17,"total_tokens":117},"output":[{"type":"function_call","name":"unexpected_fixture_tool","arguments":"{}"}]}}`+"\n\n")
	}))
	defer upstream.Close()
	activity := activityapp.NewService(10)
	server, inspector := guardedServerWatchedBy(t, guardraildomain.ModeBlock, upstream.URL, relayactivity.NewRecorder(activity))
	report, err := server.Probe(context.Background(), "echo", "fixture-model")
	if err != nil {
		t.Fatal(err)
	}
	row := activity.List(1)[0]
	if report.ErrorCode != "guardrail_blocked" || row.State != activitydomain.StateFailed || row.ErrorCode != "guardrail_blocked" || len(inspector.Records(0)) != 1 {
		t.Fatalf("probe did not reach real guardrail block: report=%+v row=%+v", report, row)
	}
	if row.InputTokens != 100 || row.OutputTokens != 17 || row.TotalTokens != 117 || row.GenerationMS <= 0 || row.BytesOut == 0 {
		t.Fatalf("refused answer was billed upstream but vanished from activity accounting: %+v", row)
	}
}

func TestProbeCancellationRetainsPartialProviderUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"partial"}}],"usage":{"prompt_tokens":100,"completion_tokens":17,"total_tokens":117}}`+"\n\n")
		w.(http.Flusher).Flush()
		// Leave ample time for the real socket's prefix to be read, then cancel
		// while the provider is deliberately waiting rather than completing.
		timer := time.AfterFunc(100*time.Millisecond, cancel)
		defer timer.Stop()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	activity := activityapp.NewService(10)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer", Format: "chat"}},
		Credentials: &credentialSource{values: []string{"fixture-key"}},
		Activity:    relayactivity.NewRecorder(activity),
		Config:      Config{StreamIdleTimeout: time.Second},
	})
	report, err := server.Probe(ctx, "fixture", "fixture-model")
	if err != nil {
		t.Fatal(err)
	}
	row := activity.List(1)[0]
	if report.ErrorCode != "cancelled" || row.State != activitydomain.StateCancelled || row.ErrorCode != "cancelled" || row.InputTokens != 100 || row.OutputTokens != 17 || row.TotalTokens != 117 || row.GenerationMS <= 0 {
		t.Fatalf("cancelled probe lost partial billing or terminal state: report=%+v row=%+v", report, row)
	}
}

func TestProbeRecordsMeasuredUsageThroughRealActivityRecorder(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(10 * time.Millisecond)
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":17,\"total_tokens\":117,\"prompt_tokens_details\":{\"cached_tokens\":30},\"completion_tokens_details\":{\"reasoning_tokens\":7}}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	activity := activityapp.NewService(10)
	server := NewServer("127.0.0.1:0", Dependencies{
		Routes:      fixedRoute{route: relayapp.Route{ProviderID: "fixture", BaseURL: base, AuthMode: "bearer", Format: "chat"}},
		Credentials: &credentialSource{values: []string{"fixture-key"}},
		Activity:    relayactivity.NewRecorder(activity),
		Config:      Config{StreamIdleTimeout: time.Second},
	})
	report, err := server.Probe(context.Background(), "fixture", "fixture-model")
	if err != nil {
		t.Fatal(err)
	}
	rows := activity.List(10)
	if len(rows) != 1 {
		t.Fatalf("recorded %d probe attempts, want one", len(rows))
	}
	row := rows[0]
	if row.State != activitydomain.StateCompleted || report.OutputTokens != 17 || row.InputTokens != 100 || row.OutputTokens != 17 || row.TotalTokens != 117 || row.ContextTokens != 100 || row.CachedTokens != 30 || row.ReasoningTokens != 7 {
		t.Fatalf("real probe accounting lost provider usage: report=%+v row=%+v", report, row)
	}
	if row.GenerationMS <= 0 || math.Abs(row.GenerationMS-report.TotalMs) > .001 || row.TokensPerSecond <= 0 || row.BytesOut == 0 {
		t.Fatalf("probe generation was not recorded from measurement: report=%+v row=%+v", report, row)
	}
}
