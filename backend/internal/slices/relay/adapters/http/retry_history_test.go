package relayhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode"

	activityapp "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	guardraildomain "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
	relayactivity "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/activity"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	rulesrelay "github.com/luxuryprivate/switchboard/backend/internal/slices/rules/adapters/relay"
	rulesapp "github.com/luxuryprivate/switchboard/backend/internal/slices/rules/application"
	rulesdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/rules/domain"
)

type retryMemory struct{ set rulesdomain.Set }

func (store *retryMemory) Load(context.Context) (rulesdomain.Set, bool, error) {
	return store.set.Clone(), true, nil
}
func (store *retryMemory) Save(_ context.Context, set rulesdomain.Set) error {
	store.set = set.Clone()
	return nil
}

func TestRetryRuleSavedFromHistory(t *testing.T) {
	for _, shape := range []string{"http", "json-failed", "json-incomplete", "sse-failed", "sse-incomplete", "sse-output-failed"} {
		for _, action := range []string{relayapp.RetryActionRetry, relayapp.RetryActionFail} {
			t.Run(shape+"/"+action, func(t *testing.T) {
				store := &retryMemory{}
				service, err := rulesapp.NewService(store, nil)
				if err != nil {
					t.Fatal(err)
				}
				rules := rulesrelay.New(service)
				upstream := newScriptedUpstream(func(attempt int, writer http.ResponseWriter, _ *http.Request) {
					if attempt == 3 {
						if strings.HasPrefix(shape, "sse") {
							writer.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprintf(writer, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", completedAnswer)
						} else {
							answerOK(writer)
						}
						return
					}
					retryFixtureFailure(writer, shape, "Widget   temporarily\n\tunavailable key")
				})
				defer upstream.Close()
				activity := &startsAndFinishes{}
				credentials := &countingCredentials{}
				server := rulesServer(fixedRoute{route: upstream.route("fixture")}, credentials, rules, nil, activity, nil)
				requestBody := `{"model":"gpt-test"}`
				if strings.HasPrefix(shape, "sse") {
					requestBody = `{"model":"gpt-test","stream":true}`
				}
				postRequest(server, requestBody, nil)
				if activity.finish.ErrorDetail == "" {
					t.Fatalf("history: %+v", activity.finish)
				}
				detail := strings.Join(strings.Fields(activity.finish.ErrorDetail), " ")
				statuses := []int{}
				if activity.finish.Status >= 400 {
					statuses = append(statuses, activity.finish.Status)
				}
				_, err = service.SaveRetryRule(context.Background(), rulesdomain.RetryRule{
					ID: "from-history", Name: "From history", Enabled: true,
					Match:  rulesdomain.RetryMatch{Statuses: statuses, ErrorCodes: []string{activity.finish.ErrorCode}, Contains: []string{detail}},
					Action: action, MaxAttempts: 1, DelayMS: 1,
				}, "")
				if err != nil {
					t.Fatal(err)
				}
				// Reload the saved rule through the real service and relay adapter.
				if err = service.Load(context.Background()); err != nil {
					t.Fatal(err)
				}
				response := postRequest(server, requestBody, nil)
				if service.Stats()["from-history"].Hits != 1 {
					t.Fatal("saved rule did not fire exactly once")
				}
				wantAttempts, wantStatus, wantTokens := 3, 200, int64(49)
				if shape == "http" {
					wantTokens = 42
				}
				if action == relayapp.RetryActionFail {
					wantAttempts, wantStatus, wantTokens = 2, 502, 7
					if shape == "http" {
						wantStatus, wantTokens = 400, 0
					}
					if strings.Contains(strings.ToLower(response.Body.String()), "widget") {
						t.Fatalf("non-neutral failure: %s", response.Body.String())
					}
				}
				wantWireStatus := wantStatus
				// Streaming requests already sent 200 headers; history retains the failure status.
				if strings.HasPrefix(shape, "sse") {
					wantWireStatus = 200
				}
				if response.Code != wantWireStatus || activity.finish.Status != wantStatus || len(upstream.requests()) != wantAttempts {
					t.Fatalf("wire=%d history=%d attempts=%d; want %d/%d/%d", response.Code, activity.finish.Status, len(upstream.requests()), wantWireStatus, wantStatus, wantAttempts)
				}
				if activity.finish.Usage.TotalTokens != wantTokens {
					t.Fatalf("usage=%+v want total=%d", activity.finish.Usage, wantTokens)
				}
				if acquired, finished := credentials.counts(); acquired != wantAttempts || finished != acquired {
					t.Fatalf("leases acquired=%d finished=%d", acquired, finished)
				}
			})
		}
	}
}

func retryFixtureFailure(writer http.ResponseWriter, shape, message string) {
	status := "failed"
	if strings.Contains(shape, "incomplete") {
		status = "incomplete"
	}
	field := "error"
	if status == "incomplete" {
		field = "incomplete_details"
	}
	encodedMessage, _ := json.Marshal(message)
	response := fmt.Sprintf(`{"object":"response","status":%q,%q:{"code":"widget_busy","message":%s},"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}`, status, field, encodedMessage)
	if strings.HasPrefix(shape, "sse") {
		writer.Header().Set("Content-Type", "text/event-stream")
		if shape == "sse-output-failed" {
			fmt.Fprint(writer, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
		}
		fmt.Fprintf(writer, "event: response.%s\ndata: {\"type\":\"response.%s\",\"response\":%s}\n\n", status, status, response)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	if shape == "http" {
		writer.WriteHeader(400)
	}
	fmt.Fprint(writer, response)
}

func TestBufferedRetryRuleBudgetAndRefusal(t *testing.T) {
	for _, action := range []string{relayapp.RetryActionRetry, relayapp.RetryActionFail} {
		for _, shape := range []string{"json-failed", "json-incomplete", "sse-failed", "sse-incomplete"} {
			for _, refused := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/refused=%v", shape, action, refused), func(t *testing.T) {
					message := "Widget unavailable"
					if refused {
						message = "rejected by our content policy"
					}
					upstream := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) { retryFixtureFailure(writer, shape, message) })
					defer upstream.Close()
					rules := &fakeRules{retry: retryRule(action, "", 1), outcome: relayapp.RuleOutcome{Budgeted: []string{"budget"}}}
					activity := &startsAndFinishes{}
					credentials := &countingCredentials{}
					requestBody := `{"model":"gpt-test"}`
					if strings.HasPrefix(shape, "sse") {
						requestBody = `{"model":"gpt-test","stream":true}`
					}
					response := postRequest(rulesServer(fixedRoute{route: upstream.route("fixture")}, credentials, rules, nil, activity, nil), requestBody, nil)
					wantAttempts, wantQueries := 2, 2
					if action == relayapp.RetryActionFail {
						wantAttempts, wantQueries = 1, 1
					}
					if refused {
						wantAttempts, wantQueries = 1, 0
					}
					if len(upstream.requests()) != wantAttempts || len(rules.queries) != wantQueries {
						t.Fatalf("attempts=%d queries=%d status=%d", len(upstream.requests()), len(rules.queries), response.Code)
					}
					if activity.finish.Usage.TotalTokens != int64(wantAttempts*7) {
						t.Fatalf("usage=%+v", activity.finish.Usage)
					}
					if len(rules.spent) != 1 || rules.spent[0] != int64(wantAttempts*7) {
						t.Fatalf("budget usage=%v", rules.spent)
					}
					if acquired, finished := credentials.counts(); acquired != wantAttempts || acquired != finished {
						t.Fatalf("leases=%d/%d", acquired, finished)
					}
				})
			}
		}
	}
}

func TestBufferedRetryRuleDispatchAttemptLimit(t *testing.T) {
	for _, shape := range []string{"json-failed", "sse-failed"} {
		for _, action := range []string{relayapp.RetryActionRetry, relayapp.RetryActionFail} {
			for _, limit := range []int{1, 3} {
				t.Run(fmt.Sprintf("%s/%s/limit=%d", shape, action, limit), func(t *testing.T) {
					upstream := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) {
						retryFixtureFailure(writer, shape, "Widget unavailable")
					})
					defer upstream.Close()
					credentials := &countingCredentials{}
					rules := &fakeRules{retry: retryRule(action, "", 1)}
					activity := &startsAndFinishes{}
					server := rulesServer(fixedRoute{route: upstream.route("fixture")}, credentials, rules, nil, activity, nil)
					body := `{"model":"gpt-test"}`
					if strings.HasPrefix(shape, "sse") {
						body = `{"model":"gpt-test","stream":true}`
					}
					response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{Method: http.MethodPost, Path: "/v1/responses", Headers: http.Header{"Content-Type": {"application/json"}}, Body: []byte(body), ProviderID: "fixture", AttemptLimit: limit, UseStoredCredential: true})
					if err != nil {
						t.Fatal(err)
					}
					wantAttempts, wantStatus := min(limit, 2), 200
					if action == relayapp.RetryActionFail {
						wantAttempts, wantStatus = 1, 502
					}
					if response.Status != wantStatus || len(upstream.requests()) != wantAttempts {
						t.Fatalf("status=%d attempts=%d", response.Status, len(upstream.requests()))
					}
					if activity.finish.Usage.TotalTokens != int64(wantAttempts*7) {
						t.Fatalf("usage=%+v", activity.finish.Usage)
					}
					if action == relayapp.RetryActionFail && strings.Contains(strings.ToLower(string(response.Body)), "widget") {
						t.Fatalf("non-neutral fail: %s", response.Body)
					}
					if acquired, finished := credentials.counts(); acquired != wantAttempts || finished != acquired {
						t.Fatalf("leases=%d/%d", acquired, finished)
					}
				})
			}
		}
	}
}

func TestRetryRuleNeverReplaysLiveFailure(t *testing.T) {
	upstream := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"status\":\"in_progress\"}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
		writer.(http.Flusher).Flush()
		time.Sleep(120 * time.Millisecond)
		retryFixtureFailure(writer, "sse-failed", "Widget unavailable")
	})
	defer upstream.Close()
	rules := &fakeRules{retry: retryRule(relayapp.RetryActionRetry, "", 1)}
	credentials := &countingCredentials{}
	activity := &startsAndFinishes{}
	server, _ := guardedServerWatchedBy(t, guardraildomain.ModeMonitor, upstream.URL, activity)
	server.rules, server.credentials = rules, credentials
	server.config.LiveStreamProbation = 5 * time.Millisecond
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(`{"model":"gpt-test","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if len(upstream.requests()) != 1 || len(rules.queries) != 0 || !strings.Contains(response.Body.String(), "partial") {
		t.Fatalf("attempts=%d queries=%d body=%s", len(upstream.requests()), len(rules.queries), response.Body.String())
	}
	if acquired, finished := credentials.counts(); acquired != 1 || finished != 1 {
		t.Fatalf("leases=%d/%d", acquired, finished)
	}
	if activity.finish.Usage.TotalTokens != 7 {
		t.Fatalf("usage=%+v", activity.finish.Usage)
	}
}

func TestRetryErrorTextMatchesDraftWhitespace(t *testing.T) {
	raw := []byte(`{"message":"Busy\u0000\ufeffnow"}`)
	got := retryErrorText(raw, "busy:\tBusy\x00\uFEFFnow\u0085 retry")
	if !strings.Contains(got, normalizeErrorText(raw)) || !strings.Contains(got, "busy: busy now retry") {
		t.Fatalf("raw or single-line draft missing: %q", got)
	}
}

func TestRetryRuleKeepsRawJSONMatching(t *testing.T) {
	upstream := newScriptedUpstream(fixedThenOK(400, "widget hiccup"))
	defer upstream.Close()
	service, err := rulesapp.NewService(&retryMemory{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SaveRetryRule(context.Background(), rulesdomain.RetryRule{ID: "raw", Name: "Raw JSON", Enabled: true, Match: rulesdomain.RetryMatch{Contains: []string{`"message":"widget hiccup"`}}, Action: relayapp.RetryActionRetry, MaxAttempts: 1, DelayMS: 1}, "")
	if err != nil {
		t.Fatal(err)
	}
	rules := rulesrelay.New(service)
	response := postRequest(rulesServer(fixedRoute{route: upstream.route("fixture")}, &countingCredentials{}, rules, nil, nil, nil), `{"model":"gpt-test"}`, nil)
	if response.Code != 200 || len(upstream.requests()) != 2 {
		t.Fatalf("status=%d attempts=%d", response.Code, len(upstream.requests()))
	}
}

func TestRetryRuleFromSanitizedHistory(t *testing.T) {
	for _, shape := range []string{"http", "json-failed", "json-incomplete", "sse-failed", "sse-incomplete"} {
		t.Run(shape, func(t *testing.T) {
			upstream := newScriptedUpstream(func(attempt int, writer http.ResponseWriter, _ *http.Request) {
				if attempt == 3 {
					if strings.HasPrefix(shape, "sse") {
						writer.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(writer, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", completedAnswer)
					} else {
						answerOK(writer)
					}
					return
				}
				retryFixtureFailure(writer, shape, "Busy\x00\x7fnow\u0085\uFEFF retry")
			})
			defer upstream.Close()
			history := activityapp.NewService(10)
			service, err := rulesapp.NewService(&retryMemory{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			server := rulesServer(fixedRoute{route: upstream.route("fixture")}, &countingCredentials{}, rulesrelay.New(service), nil, relayactivity.NewRecorder(history), nil)
			body := `{"model":"gpt-test"}`
			if strings.HasPrefix(shape, "sse") {
				body = `{"model":"gpt-test","stream":true}`
			}
			postRequest(server, body, nil)
			entry := history.List(1)[0]
			// Apply the same control/whitespace cleanup as the frontend draft.
			detail := strings.Map(func(r rune) rune {
				if unicode.IsControl(r) || r == '\uFEFF' {
					return ' '
				}
				return r
			}, entry.ErrorDetail)
			detail = strings.Join(strings.Fields(detail), " ")
			if !strings.Contains(detail, "Busynow") {
				t.Fatalf("history did not sanitize control characters: %q", detail)
			}
			_, err = service.SaveRetryRule(context.Background(), rulesdomain.RetryRule{ID: "sanitized-history", Name: "History", Enabled: true, Match: rulesdomain.RetryMatch{Contains: []string{detail}, ErrorCodes: []string{entry.ErrorCode}}, Action: relayapp.RetryActionRetry, MaxAttempts: 1, DelayMS: 1}, "")
			if err != nil {
				t.Fatal(err)
			}
			postRequest(server, body, nil)
			if len(upstream.requests()) != 3 || service.Stats()["sanitized-history"].Hits != 1 {
				t.Fatalf("attempts=%d hits=%d", len(upstream.requests()), service.Stats()["sanitized-history"].Hits)
			}
		})
	}
}

func TestBufferedRetryRuleSkipsReasonOnlyRefusal(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(fmt.Sprintf("nested=%v", nested), func(t *testing.T) {
			upstream := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				body := `{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}`
				if nested {
					body = `{"type":"response.incomplete","response":` + body + `}`
				}
				fmt.Fprint(writer, body)
			})
			defer upstream.Close()
			rules := &fakeRules{retry: retryRule(relayapp.RetryActionRetry, "", 1)}
			credentials := &countingCredentials{}
			postRequest(rulesServer(fixedRoute{route: upstream.route("fixture")}, credentials, rules, nil, nil, nil), `{"model":"gpt-test"}`, nil)
			if len(upstream.requests()) != 1 || len(rules.queries) != 0 {
				t.Fatalf("attempts=%d queries=%d", len(upstream.requests()), len(rules.queries))
			}
			if acquired, finished := credentials.counts(); acquired != 1 || finished != 1 {
				t.Fatalf("leases=%d/%d", acquired, finished)
			}
		})
	}
}

func TestRetryRuleFromBodylessHTTPHistory(t *testing.T) {
	for _, status := range []int{400, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			upstream := newScriptedUpstream(func(attempt int, writer http.ResponseWriter, _ *http.Request) {
				if attempt == 3 {
					answerOK(writer)
					return
				}
				writer.WriteHeader(status)
			})
			defer upstream.Close()
			history := activityapp.NewService(10)
			service, err := rulesapp.NewService(&retryMemory{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			server := rulesServer(fixedRoute{route: upstream.route("fixture")}, &countingCredentials{}, rulesrelay.New(service), nil, relayactivity.NewRecorder(history), nil)
			request := relayapp.DispatchRequest{Method: http.MethodPost, Path: "/v1/responses", Body: []byte(`{"model":"gpt-test"}`), ProviderID: "fixture", AttemptLimit: 1, UseStoredCredential: true}
			if _, err := server.Dispatch(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			entry := history.List(1)[0]
			if entry.ErrorDetail != "The request could not be completed" {
				t.Fatalf("unexpected history: %+v", entry)
			}
			_, err = service.SaveRetryRule(context.Background(), rulesdomain.RetryRule{ID: "bodyless-history", Name: "History", Enabled: true, Match: rulesdomain.RetryMatch{Statuses: []int{entry.Status}, ErrorCodes: []string{entry.ErrorCode}, Contains: []string{entry.ErrorDetail}}, Action: relayapp.RetryActionRetry, MaxAttempts: 1, DelayMS: 1}, "")
			if err != nil {
				t.Fatal(err)
			}
			request.AttemptLimit = 2
			response, err := server.Dispatch(context.Background(), request)
			if err != nil || response.Status != 200 || len(upstream.requests()) != 3 || service.Stats()["bodyless-history"].Hits != 1 {
				t.Fatalf("error=%v status=%d attempts=%d hits=%d", err, response.Status, len(upstream.requests()), service.Stats()["bodyless-history"].Hits)
			}
		})
	}
}
