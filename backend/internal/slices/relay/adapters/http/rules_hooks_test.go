package relayhttp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	activityapp "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	relayactivity "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/activity"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	relayhooksrelay "github.com/luxuryprivate/switchboard/backend/internal/slices/relayhooks/adapters/relay"
	relayhooksapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relayhooks/application"
	relayhooksdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/relayhooks/domain"
)

// fakeRules answers Evaluate with a fixed outcome and Retry with retry, and
// records what the relay asked.
type fakeRules struct {
	mu        sync.Mutex
	outcome   relayapp.RuleOutcome
	retry     func(relayapp.RetryQuery) (relayapp.RetryDecision, bool)
	evaluated []relayapp.RuleRequest
	queries   []relayapp.RetryQuery
	spentIDs  []string
	spent     []int64
}

func (rules *fakeRules) Evaluate(request relayapp.RuleRequest) relayapp.RuleOutcome {
	rules.mu.Lock()
	defer rules.mu.Unlock()
	rules.evaluated = append(rules.evaluated, request)
	return rules.outcome
}

func (rules *fakeRules) SpendTokens(ruleIDs []string, tokens int64) {
	rules.mu.Lock()
	defer rules.mu.Unlock()
	rules.spentIDs = append(rules.spentIDs, ruleIDs...)
	rules.spent = append(rules.spent, tokens)
}

func (rules *fakeRules) Retry(query relayapp.RetryQuery) (relayapp.RetryDecision, bool) {
	rules.mu.Lock()
	rules.queries = append(rules.queries, query)
	retry := rules.retry
	rules.mu.Unlock()
	if retry == nil {
		return relayapp.RetryDecision{}, false
	}
	return retry(query)
}

// retryRule matches every failure whose text contains needle.
func retryRule(action, needle string, maxAttempts int) func(relayapp.RetryQuery) (relayapp.RetryDecision, bool) {
	return func(query relayapp.RetryQuery) (relayapp.RetryDecision, bool) {
		if !strings.Contains(query.ErrorText, needle) {
			return relayapp.RetryDecision{}, false
		}
		return relayapp.RetryDecision{RuleID: "rule-1", Action: action, MaxAttempts: maxAttempts, Delay: time.Millisecond}, true
	}
}

// startsAndFinishes records what history was told at both ends.
type startsAndFinishes struct {
	relayapp.NoopActivity
	mu     sync.Mutex
	starts []relayapp.ActivityStart
	finish relayapp.ActivityFinish
}

func (sink *startsAndFinishes) Begin(start relayapp.ActivityStart) string {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.starts = append(sink.starts, start)
	return "act-1"
}

func (sink *startsAndFinishes) Finish(_ string, finish relayapp.ActivityFinish) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.finish = finish
}

// seenRequest is what an upstream received on one attempt.
type seenRequest struct {
	auth   string
	team   string
	client string
	body   string
}

// scriptedUpstream answers attempt n with answer(n) and records each request.
type scriptedUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	seen []seenRequest
}

func newScriptedUpstream(answer func(attempt int, writer http.ResponseWriter, request *http.Request)) *scriptedUpstream {
	upstream := &scriptedUpstream{}
	upstream.Server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		upstream.mu.Lock()
		upstream.seen = append(upstream.seen, seenRequest{
			auth: request.Header.Get("Authorization"), team: request.Header.Get("X-Team"),
			client: request.Header.Get("X-Client"), body: string(body),
		})
		attempt := len(upstream.seen)
		upstream.mu.Unlock()
		answer(attempt, writer, request)
	}))
	return upstream
}

func (upstream *scriptedUpstream) requests() []seenRequest {
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	return append([]seenRequest(nil), upstream.seen...)
}

func (upstream *scriptedUpstream) route(providerID string) relayapp.Route {
	parsed, _ := url.Parse(upstream.URL)
	return relayapp.Route{ProviderID: providerID, ProviderName: providerID, BaseURL: parsed, AuthMode: "bearer"}
}

const completedAnswer = `{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":30,"output_tokens":12,"total_tokens":42}}`

func answerOK(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(completedAnswer))
}

func answerError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write([]byte(`{"error":{"message":"` + message + `"}}`))
}

// fixedThenOK fails the first attempt with status and message, then answers.
func fixedThenOK(status int, message string) func(int, http.ResponseWriter, *http.Request) {
	return func(attempt int, writer http.ResponseWriter, _ *http.Request) {
		if attempt == 1 {
			answerError(writer, status, message)
			return
		}
		answerOK(writer)
	}
}

// pinRoutes is multiRoute with the rule pin the providers adapter offers:
// only providers listed as enabled can be pinned.
type pinRoutes struct {
	multiRoute
	enabled map[string]bool
}

func (source pinRoutes) PinnedEnabled(ctx context.Context, providerID, upstreamModel string) (relayapp.Route, error) {
	if !source.enabled[providerID] {
		return relayapp.Route{}, context.Canceled
	}
	return source.Pinned(ctx, providerID, upstreamModel)
}

// The relay's built-in ladder never retries a 400 with PermanentAttempts 1, so
// any second attempt in these tests is the rule's doing.
func rulesServer(routes relayapp.RouteSource, credentials relayapp.CredentialSource, rules relayapp.RequestRules, hooks relayapp.RequestHooks, activity relayapp.ActivitySink, failovers relayapp.FailoverSource) *Server {
	if activity == nil {
		activity = relayapp.NoopActivity{}
	}
	return NewServer("127.0.0.1:0", Dependencies{
		Routes: routes, Credentials: credentials, Rules: rules, Hooks: hooks, Activity: activity, Failovers: failovers,
		Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond, PermanentAttempts: 1},
	})
}

func postRequest(server *Server, body string, header http.Header) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "http://relay/v1/responses", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	for name, values := range header {
		request.Header[name] = values
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func ruleError(t *testing.T, response *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var payload struct {
		Error struct{ Message, Type string } `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("local answer is not JSON: %q", response.Body.String())
	}
	return payload.Error.Message, payload.Error.Type
}

func TestARuleBlockAnswersLocally(t *testing.T) {
	upstream := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) { answerOK(writer) })
	defer upstream.Close()
	rules := &fakeRules{outcome: relayapp.RuleOutcome{Block: &relayapp.RuleBlock{Status: 403, Message: "Not this model"}, Tags: []string{"blocked-by-rule"}}}
	activity := &startsAndFinishes{}
	server := rulesServer(fixedRoute{route: upstream.route("echo")}, &credentialSource{values: []string{"k1"}}, rules, nil, activity, nil)

	response := postRequest(server, `{"model":"gpt-test","input":"hi"}`, nil)
	message, kind := ruleError(t, response)
	if response.Code != 403 || message != "Not this model" || kind != "switchboard_rule" {
		t.Fatalf("block answer: %d %q %q", response.Code, message, kind)
	}
	if len(upstream.requests()) != 0 {
		t.Fatal("a blocked request reached the provider")
	}
	if activity.finish.ErrorCode != "rule_blocked" || activity.finish.Status != 403 {
		t.Fatalf("history: %+v", activity.finish)
	}
	if len(activity.starts) != 1 || strings.Join(activity.starts[0].Tags, ",") != "blocked-by-rule" {
		t.Fatalf("tags were not filed: %+v", activity.starts)
	}
	if len(rules.evaluated) != 1 || rules.evaluated[0] != (relayapp.RuleRequest{Model: "gpt-test", Endpoint: relayapp.EndpointResponses}) {
		t.Fatalf("evaluated %+v", rules.evaluated)
	}
}

func TestARuleLimitAnswers429WithRetryAfter(t *testing.T) {
	upstream := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) { answerOK(writer) })
	defer upstream.Close()
	rules := &fakeRules{outcome: relayapp.RuleOutcome{Limit: &relayapp.RuleLimit{RuleName: "hourly", RetryAfter: 1500 * time.Millisecond}}}
	activity := &startsAndFinishes{}
	server := rulesServer(fixedRoute{route: upstream.route("echo")}, &credentialSource{values: []string{"k1"}}, rules, nil, activity, nil)

	response := postRequest(server, `{"model":"gpt-test"}`, nil)
	message, kind := ruleError(t, response)
	if response.Code != 429 || response.Header().Get("Retry-After") != "2" || kind != "switchboard_rule" || !strings.Contains(message, `"hourly"`) {
		t.Fatalf("limit answer: %d retry-after=%q %q %q", response.Code, response.Header().Get("Retry-After"), message, kind)
	}
	if len(upstream.requests()) != 0 || activity.finish.ErrorCode != "rule_limited" {
		t.Fatalf("limited request: upstream=%d history=%+v", len(upstream.requests()), activity.finish)
	}
}

func TestARuleRewritesModelAddsHeadersTagsAndSpendsTokens(t *testing.T) {
	upstream := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) { answerOK(writer) })
	defer upstream.Close()
	rules := &fakeRules{outcome: relayapp.RuleOutcome{
		// X-Client is already set by the client: a rule never replaces a prepared header.
		Model: "gpt-mini", Headers: map[string]string{"X-Team": "red", "X-Client": "rule"}, Tags: []string{"team-red"}, Budgeted: []string{"budget-1"},
	}}
	activity := &startsAndFinishes{}
	server := rulesServer(fixedRoute{route: upstream.route("echo")}, &credentialSource{values: []string{"k1"}}, rules, nil, activity, nil)

	client := http.Header{"X-Client": []string{"cli"}}
	response := postRequest(server, `{"model":"gpt-test","input":"hi"}`, client)
	if response.Code != 200 {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	seen := upstream.requests()
	if len(seen) != 1 || !strings.Contains(seen[0].body, `"gpt-mini"`) || seen[0].team != "red" || seen[0].client != "cli" {
		t.Fatalf("upstream saw %+v", seen)
	}
	if client.Get("X-Team") != "" {
		t.Fatal("the rule header leaked into the client's own headers")
	}
	if strings.Join(activity.starts[0].Tags, ",") != "team-red" {
		t.Fatalf("tags: %v", activity.starts[0].Tags)
	}
	if strings.Join(rules.spentIDs, ",") != "budget-1" || len(rules.spent) != 1 || rules.spent[0] != 42 {
		t.Fatalf("spent %v on %v", rules.spent, rules.spentIDs)
	}
}

func TestARulePinNeedsAnEnabledProvider(t *testing.T) {
	alpha := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) { answerOK(writer) })
	defer alpha.Close()
	beta := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) { answerOK(writer) })
	defer beta.Close()
	routes := pinRoutes{
		multiRoute: multiRoute{routes: map[string]relayapp.Route{"alpha": alpha.route("alpha"), "beta": beta.route("beta"), "gamma": beta.route("gamma")}, first: "alpha"},
		enabled:    map[string]bool{"beta": true},
	}
	pinned := &fakeRules{outcome: relayapp.RuleOutcome{ProviderID: "beta"}}
	if response := postRequest(rulesServer(routes, &credentialSource{values: []string{"k1"}}, pinned, nil, nil, nil), `{"model":"gpt-test"}`, nil); response.Code != 200 {
		t.Fatalf("pinned status %d", response.Code)
	}
	if len(beta.requests()) != 1 || len(alpha.requests()) != 0 {
		t.Fatalf("the pin was not honoured: alpha=%d beta=%d", len(alpha.requests()), len(beta.requests()))
	}
	disabled := &fakeRules{outcome: relayapp.RuleOutcome{ProviderID: "gamma"}}
	if response := postRequest(rulesServer(routes, &credentialSource{values: []string{"k1"}}, disabled, nil, nil, nil), `{"model":"gpt-test"}`, nil); response.Code != 200 {
		t.Fatalf("unpinned status %d", response.Code)
	}
	if len(alpha.requests()) != 1 || len(beta.requests()) != 1 {
		t.Fatalf("a disabled pin changed routing: alpha=%d beta=%d", len(alpha.requests()), len(beta.requests()))
	}
}

func TestARetryRuleRetriesWhatTheLadderWouldNot(t *testing.T) {
	upstream := newScriptedUpstream(fixedThenOK(400, "widget hiccup"))
	defer upstream.Close()
	rules := &fakeRules{retry: retryRule(relayapp.RetryActionRetry, "widget hiccup", 1)}
	response := postRequest(rulesServer(fixedRoute{route: upstream.route("echo")}, &credentialSource{values: []string{"k1"}}, rules, nil, nil, nil), `{"model":"gpt-test"}`, nil)
	if response.Code != 200 || len(upstream.requests()) != 2 {
		t.Fatalf("status %d after %d attempts", response.Code, len(upstream.requests()))
	}
	query := rules.queries[0]
	if query.Status != 400 || query.ErrorCode != "upstream_status" || query.ProviderID != "echo" || query.Model != "gpt-test" {
		t.Fatalf("query %+v", query)
	}
}

func TestARetryRuleStopsAtItsOwnAttempts(t *testing.T) {
	upstream := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) { answerError(writer, 400, "widget hiccup") })
	defer upstream.Close()
	rules := &fakeRules{retry: retryRule(relayapp.RetryActionRetry, "widget hiccup", 2)}
	response := postRequest(rulesServer(fixedRoute{route: upstream.route("echo")}, &credentialSource{values: []string{"k1"}}, rules, nil, nil, nil), `{"model":"gpt-test"}`, nil)
	if response.Code != 400 || len(upstream.requests()) != 3 {
		t.Fatalf("status %d after %d attempts, want 400 after 3", response.Code, len(upstream.requests()))
	}
}

func TestARotateKeyRuleMovesToTheNextKey(t *testing.T) {
	upstream := newScriptedUpstream(func(_ int, writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") == "Bearer k1" {
			answerError(writer, 400, "account quirk")
			return
		}
		answerOK(writer)
	})
	defer upstream.Close()
	credentials := &countingCredentialSource{credentialSource: credentialSource{values: []string{"k1", "k2"}}, keys: 2}
	rules := &fakeRules{retry: retryRule(relayapp.RetryActionRotateKey, "account quirk", 1)}
	response := postRequest(rulesServer(fixedRoute{route: upstream.route("echo")}, credentials, rules, nil, nil, nil), `{"model":"gpt-test"}`, nil)
	seen := upstream.requests()
	if response.Code != 200 || len(seen) != 2 || seen[1].auth != "Bearer k2" {
		t.Fatalf("status %d, attempts %+v", response.Code, seen)
	}
	if credentials.tryCalls != 1 || credentials.outcomes[0].Kind != relayapp.AttemptRateLimited {
		t.Fatalf("rotation: tries=%d outcomes=%+v", credentials.tryCalls, credentials.outcomes)
	}
}

func TestASwitchProviderRuleWalksTheChain(t *testing.T) {
	alpha := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) { answerError(writer, 400, "region closed") })
	defer alpha.Close()
	beta := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) { answerOK(writer) })
	defer beta.Close()
	routes := map[string]relayapp.Route{"alpha": alpha.route("alpha"), "beta": beta.route("beta")}
	chain := &recordingChain{routes: []relayapp.ModelRoute{{ProviderID: "alpha"}, {ProviderID: "beta"}}}
	rules := &fakeRules{retry: retryRule(relayapp.RetryActionSwitchProvider, "region closed", 1)}
	server := rulesServer(multiRoute{routes: routes, first: "alpha"}, &credentialSource{values: []string{"k1"}}, rules, nil, nil, &switchingFailovers{chain: chain, routes: routes})
	response := postRequest(server, `{"model":"gpt-test"}`, nil)
	if response.Code != 200 || len(alpha.requests()) != 1 || len(beta.requests()) != 1 {
		t.Fatalf("status %d alpha=%d beta=%d", response.Code, len(alpha.requests()), len(beta.requests()))
	}
	if strings.Join(chain.degraded, ",") != "alpha" {
		t.Fatalf("degraded %v", chain.degraded)
	}
}

// Dispatch has no public model to chain by: switch_provider acts as retry,
// and request rules and hooks are never consulted.
func TestDispatchAppliesRetryRulesOnly(t *testing.T) {
	upstream := newScriptedUpstream(fixedThenOK(400, "region closed"))
	defer upstream.Close()
	routes := map[string]relayapp.Route{"alpha": upstream.route("alpha")}
	chain := &recordingChain{routes: []relayapp.ModelRoute{{ProviderID: "alpha"}, {ProviderID: "beta"}}}
	rules := &fakeRules{
		outcome: relayapp.RuleOutcome{Block: &relayapp.RuleBlock{Status: 403, Message: "no"}},
		retry:   retryRule(relayapp.RetryActionSwitchProvider, "region closed", 1),
	}
	hooks, host := hooksFor(t, 2000, func(relayhooksdomain.Invocation) relayhooksdomain.Decision {
		return relayhooksdomain.Decision{Action: relayhooksdomain.ActionBlock, Block: &relayhooksdomain.Block{Status: 403, Message: "no"}}
	})
	server := rulesServer(multiRoute{routes: routes, first: "alpha"}, &credentialSource{values: []string{"k1"}}, rules, hooks, nil, &switchingFailovers{chain: chain, routes: routes})
	response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		Method: http.MethodPost, Path: "/v1/responses", ProviderID: "alpha", UpstreamModel: "gpt-test",
		Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"model":"gpt-test"}`),
	})
	if err != nil || response.Status != 200 || len(upstream.requests()) != 2 {
		t.Fatalf("dispatch: status=%d err=%v attempts=%d", response.Status, err, len(upstream.requests()))
	}
	if len(rules.evaluated) != 0 || host.invoked() != 0 || len(chain.degraded) != 0 {
		t.Fatalf("dispatch consulted local-only policy: evaluated=%d hooks=%d degraded=%v", len(rules.evaluated), host.invoked(), chain.degraded)
	}
}

func TestAFailRuleEndsTheRequestWithoutAnotherAttempt(t *testing.T) {
	upstream := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) {
		answerError(writer, 503, "maintenance window")
	})
	defer upstream.Close()
	rules := &fakeRules{retry: retryRule(relayapp.RetryActionFail, "maintenance window", 1)}
	response := postRequest(rulesServer(fixedRoute{route: upstream.route("echo")}, &credentialSource{values: []string{"k1"}}, rules, nil, nil, nil), `{"model":"gpt-test"}`, nil)
	if response.Code != 503 || len(upstream.requests()) != 1 {
		t.Fatalf("status %d after %d attempts", response.Code, len(upstream.requests()))
	}
	if strings.Contains(response.Body.String(), "maintenance") {
		t.Fatalf("provider prose reached the client: %s", response.Body.String())
	}
}

func TestARefusalNeverReachesARetryRule(t *testing.T) {
	upstream := newScriptedUpstream(fixedThenOK(400, "rejected by our content policy"))
	defer upstream.Close()
	rules := &fakeRules{retry: retryRule(relayapp.RetryActionRetry, "", 5)}
	response := postRequest(rulesServer(fixedRoute{route: upstream.route("echo")}, &credentialSource{values: []string{"k1"}}, rules, nil, nil, nil), `{"model":"gpt-test"}`, nil)
	if response.Code == 200 || len(upstream.requests()) != 1 || len(rules.queries) != 0 {
		t.Fatalf("a refusal was retried: status=%d attempts=%d queries=%d", response.Code, len(upstream.requests()), len(rules.queries))
	}
}

func TestATransportRuleSeesTransportFailures(t *testing.T) {
	upstream := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) {
		connection, _, err := writer.(http.Hijacker).Hijack()
		if err == nil {
			_ = connection.Close()
		}
	})
	defer upstream.Close()
	rules := &fakeRules{retry: func(query relayapp.RetryQuery) (relayapp.RetryDecision, bool) {
		return relayapp.RetryDecision{RuleID: "net", Action: relayapp.RetryActionFail, MaxAttempts: 1}, query.ErrorCode == "transport"
	}}
	response := postRequest(rulesServer(fixedRoute{route: upstream.route("echo")}, &credentialSource{values: []string{"k1"}}, rules, nil, nil, nil), `{"model":"gpt-test"}`, nil)
	if response.Code != 502 || len(upstream.requests()) != 1 {
		t.Fatalf("status %d after %d attempts", response.Code, len(upstream.requests()))
	}
	if rules.queries[0].Status != 0 || rules.queries[0].ErrorCode != "transport" {
		t.Fatalf("query %+v", rules.queries[0])
	}
}

// With rules configured the error body is read early for matching; when no
// rule matches, the built-in path must still see the provider's words.
func TestAnUnmatchedRuleLeavesTheBuiltInVerdictIntact(t *testing.T) {
	upstream := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) {
		answerError(writer, 402, "Budget pool quota has been exhausted")
	})
	defer upstream.Close()
	activity := &startsAndFinishes{}
	rules := &fakeRules{}
	server := rulesServer(fixedRoute{route: upstream.route("echo")}, &credentialSource{values: []string{"k1"}}, rules, nil, activity, nil)
	response, err := server.Dispatch(context.Background(), relayapp.DispatchRequest{
		Method: http.MethodPost, Path: "/v1/responses", ProviderID: "echo", UpstreamModel: "gpt-test",
		Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"model":"gpt-test"}`),
	})
	if err != nil || response.Status != 402 || len(rules.queries) == 0 {
		t.Fatalf("status=%d err=%v queries=%d", response.Status, err, len(rules.queries))
	}
	if !strings.Contains(activity.finish.ErrorDetail, "Budget pool quota has been exhausted") {
		t.Fatalf("the replayed body lost the provider's words: %+v", activity.finish)
	}
}

// hookHost answers request hooks with decide (or never, when decide is nil)
// and collects observations.
type hookHost struct {
	service      *relayhooksapp.Service
	decide       func(relayhooksdomain.Invocation) relayhooksdomain.Decision
	mu           sync.Mutex
	invocations  []relayhooksdomain.Invocation
	observations chan relayhooksdomain.Observation
}

func (host *hookHost) Invoke(invocation relayhooksdomain.Invocation) (bool, error) {
	host.mu.Lock()
	host.invocations = append(host.invocations, invocation)
	host.mu.Unlock()
	if host.decide != nil {
		go func() { _, _ = host.service.Respond(invocation.CallID, host.decide(invocation)) }()
	}
	return false, nil
}

func (host *hookHost) Observe(observation relayhooksdomain.Observation) error {
	host.observations <- observation
	return nil
}

func (host *hookHost) Removed(relayhooksapp.Removal) error { return nil }

func (host *hookHost) invoked() int {
	host.mu.Lock()
	defer host.mu.Unlock()
	return len(host.invocations)
}

type everyGrant struct{}

func (everyGrant) Granted(string, string) bool { return true }

// hooksFor wires the real hooks service, with one request hook and one
// response observer, through the relay adapter.
func hooksFor(t *testing.T, timeoutMS int, decide func(relayhooksdomain.Invocation) relayhooksdomain.Decision) (relayapp.RequestHooks, *hookHost) {
	t.Helper()
	service := relayhooksapp.NewService()
	host := &hookHost{service: service, decide: decide, observations: make(chan relayhooksdomain.Observation, 4)}
	service.Connect(everyGrant{}, host)
	for hookID, phase := range map[string]string{"req": relayhooksdomain.PhaseRequest, "obs": relayhooksdomain.PhaseResponse} {
		if err := service.Register(relayhooksdomain.Hook{PluginID: "acme.tools", HookID: hookID, Phase: phase, TimeoutMS: timeoutMS}); err != nil {
			t.Fatal(err)
		}
	}
	return relayhooksrelay.New(service), host
}

func (host *hookHost) observation(t *testing.T) relayhooksdomain.Observation {
	t.Helper()
	select {
	case observation := <-host.observations:
		return observation
	case <-time.After(2 * time.Second):
		t.Fatal("no response observation")
		return relayhooksdomain.Observation{}
	}
}

func TestAContinuingHookTagsAndIsObserved(t *testing.T) {
	upstream := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) { answerOK(writer) })
	defer upstream.Close()
	hooks, host := hooksFor(t, 2000, func(relayhooksdomain.Invocation) relayhooksdomain.Decision {
		return relayhooksdomain.Decision{Action: relayhooksdomain.ActionContinue, Tags: []string{"seen"}}
	})
	activity := &startsAndFinishes{}
	const body = `{"model":"gpt-test","input":"hi"}`
	response := postRequest(rulesServer(fixedRoute{route: upstream.route("echo")}, &credentialSource{values: []string{"k1"}}, nil, hooks, activity, nil), body, nil)
	if response.Code != 200 || upstream.requests()[0].body != body {
		t.Fatalf("status %d, upstream %+v", response.Code, upstream.requests())
	}
	if strings.Join(activity.starts[0].Tags, ",") != "seen" || host.invocations[0].Request.Body != body {
		t.Fatalf("tags %v, hook saw %+v", activity.starts[0].Tags, host.invocations[0].Request)
	}
	observation := host.observation(t)
	if observation.Response.Status != 200 || observation.Request.Body != "" || observation.Response.Body != "" || observation.Request.ActivityID != "act-1" {
		t.Fatalf("observation %+v", observation)
	}
	if observation.Response.Tokens == nil || observation.Response.Tokens.Total != 42 {
		t.Fatalf("tokens %+v", observation.Response.Tokens)
	}
}

func TestARewritingHookReplacesTheUpstreamBody(t *testing.T) {
	upstream := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) { answerOK(writer) })
	defer upstream.Close()
	hooks, _ := hooksFor(t, 2000, func(relayhooksdomain.Invocation) relayhooksdomain.Decision {
		return relayhooksdomain.Decision{Action: relayhooksdomain.ActionRewrite, Body: `{"model":"gpt-other","input":"rewritten"}`}
	})
	response := postRequest(rulesServer(fixedRoute{route: upstream.route("echo")}, &credentialSource{values: []string{"k1"}}, nil, hooks, nil, nil), `{"model":"gpt-test","input":"hi"}`, nil)
	seen := upstream.requests()
	if response.Code != 200 || !strings.Contains(seen[0].body, "rewritten") || !strings.Contains(seen[0].body, "gpt-other") {
		t.Fatalf("status %d, upstream %+v", response.Code, seen)
	}
}

func TestABlockingHookAnswersLocally(t *testing.T) {
	upstream := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) { answerOK(writer) })
	defer upstream.Close()
	hooks, host := hooksFor(t, 2000, func(relayhooksdomain.Invocation) relayhooksdomain.Decision {
		return relayhooksdomain.Decision{Action: relayhooksdomain.ActionBlock, Block: &relayhooksdomain.Block{Status: 429, Message: "Slow down"}}
	})
	activity := &startsAndFinishes{}
	response := postRequest(rulesServer(fixedRoute{route: upstream.route("echo")}, &credentialSource{values: []string{"k1"}}, nil, hooks, activity, nil), `{"model":"gpt-test"}`, nil)
	message, kind := ruleError(t, response)
	if response.Code != 429 || message != "Slow down" || kind != "switchboard_rule" || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("block: %d %q %q retry-after=%q", response.Code, message, kind, response.Header().Get("Retry-After"))
	}
	if len(upstream.requests()) != 0 || activity.finish.ErrorCode != "hook_blocked" {
		t.Fatalf("upstream=%d history=%+v", len(upstream.requests()), activity.finish)
	}
	if observation := host.observation(t); observation.Response.Status != 429 || observation.Response.ErrorCode != "hook_blocked" {
		t.Fatalf("observation %+v", observation)
	}
}

func TestASilentHookTimesOutAndTheRequestContinues(t *testing.T) {
	upstream := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) { answerOK(writer) })
	defer upstream.Close()
	hooks, host := hooksFor(t, relayhooksdomain.MinTimeoutMS, nil)
	const body = `{"model":"gpt-test","input":"hi"}`
	started := time.Now()
	response := postRequest(rulesServer(fixedRoute{route: upstream.route("echo")}, &credentialSource{values: []string{"k1"}}, nil, hooks, nil, nil), body, nil)
	if response.Code != 200 || host.invoked() != 1 || upstream.requests()[0].body != body {
		t.Fatalf("status %d, invoked %d, upstream %+v", response.Code, host.invoked(), upstream.requests())
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the hook timeout was not honoured: %v", elapsed)
	}
}

// The request hook, the response observer and the history row all name one
// request by the same ID, whether it is served or a hook blocks it.
func TestHooksAndHistoryShareTheActivityID(t *testing.T) {
	upstream := newScriptedUpstream(func(_ int, writer http.ResponseWriter, _ *http.Request) { answerOK(writer) })
	defer upstream.Close()
	for name, action := range map[string]string{"served": relayhooksdomain.ActionContinue, "blocked": relayhooksdomain.ActionBlock} {
		t.Run(name, func(t *testing.T) {
			hooks, host := hooksFor(t, 2000, func(relayhooksdomain.Invocation) relayhooksdomain.Decision {
				decision := relayhooksdomain.Decision{Action: action}
				if action == relayhooksdomain.ActionBlock {
					decision.Block = &relayhooksdomain.Block{Status: 403, Message: "No"}
				}
				return decision
			})
			history := activityapp.NewService(10)
			server := rulesServer(fixedRoute{route: upstream.route("echo")}, &credentialSource{values: []string{"k1"}}, nil, hooks, relayactivity.NewRecorder(history), nil)
			postRequest(server, `{"model":"gpt-test","input":"hi"}`, nil)
			observation := host.observation(t)
			rows := history.List(10)
			host.mu.Lock()
			requestID := host.invocations[0].Request.ActivityID
			host.mu.Unlock()
			if len(rows) != 1 || requestID == "" || requestID != observation.Request.ActivityID || requestID != rows[0].ID {
				t.Fatalf("request hook %q, observer %q, history %+v", requestID, observation.Request.ActivityID, rows)
			}
		})
	}
}
