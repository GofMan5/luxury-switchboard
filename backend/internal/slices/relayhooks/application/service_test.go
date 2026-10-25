package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/relayhooks/domain"
)

type fakeGrants struct {
	mu      sync.Mutex
	revoked map[string]bool
}

func (grants *fakeGrants) Granted(pluginID, permission string) bool {
	grants.mu.Lock()
	defer grants.mu.Unlock()
	return permission == domain.PermissionRelayContent && !grants.revoked[pluginID]
}

func (grants *fakeGrants) revoke(pluginID string) {
	grants.mu.Lock()
	defer grants.mu.Unlock()
	if grants.revoked == nil {
		grants.revoked = map[string]bool{}
	}
	grants.revoked[pluginID] = true
}

// fakeHost plays the plugin host: it answers each invocation with what the
// hook's answer function returns, from another goroutine; nil stays silent.
type fakeHost struct {
	service      *Service
	mu           sync.Mutex
	answers      map[string]func(domain.Invocation) *domain.Decision
	invocations  []domain.Invocation
	observations []domain.Observation
	removals     []Removal
	omitBody     bool
	failInvoke   bool
	observed     chan struct{}
	blockObserve chan struct{}
}

func (host *fakeHost) Invoke(invocation domain.Invocation) (bool, error) {
	host.mu.Lock()
	if host.failInvoke {
		host.mu.Unlock()
		return false, errors.New("emit failed")
	}
	host.invocations = append(host.invocations, invocation)
	answer := host.answers[invocation.PluginID+"/"+invocation.HookID]
	omit := host.omitBody
	host.mu.Unlock()
	if answer != nil {
		go func() {
			if decision := answer(invocation); decision != nil {
				_, _ = host.service.Respond(invocation.CallID, *decision)
			}
		}()
	}
	return omit, nil
}

func (host *fakeHost) Observe(observation domain.Observation) error {
	if host.blockObserve != nil {
		<-host.blockObserve
	}
	host.mu.Lock()
	host.observations = append(host.observations, observation)
	host.mu.Unlock()
	if host.observed != nil {
		host.observed <- struct{}{}
	}
	return nil
}

func (host *fakeHost) Removed(removal Removal) error {
	host.mu.Lock()
	host.removals = append(host.removals, removal)
	host.mu.Unlock()
	return nil
}

func (host *fakeHost) answer(pluginID, hookID string, answer func(domain.Invocation) *domain.Decision) {
	host.mu.Lock()
	defer host.mu.Unlock()
	host.answers[pluginID+"/"+hookID] = answer
}

func (host *fakeHost) calls() []domain.Invocation {
	host.mu.Lock()
	defer host.mu.Unlock()
	return append([]domain.Invocation(nil), host.invocations...)
}

func setup(t *testing.T) (*Service, *fakeGrants, *fakeHost) {
	t.Helper()
	service := NewService()
	grants := &fakeGrants{}
	host := &fakeHost{service: service, answers: map[string]func(domain.Invocation) *domain.Decision{}}
	service.Connect(grants, host)
	t.Cleanup(func() { waitIdle(t, service) })
	return service, grants, host
}

// waitIdle fails the test if a call stays pending or the observer goroutine
// does not exit.
func waitIdle(t *testing.T, service *Service) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		service.mu.Lock()
		idle := !service.draining && len(service.queue) == 0 && len(service.pending) == 0
		service.mu.Unlock()
		if idle {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("relay hooks did not go idle: a call or the observer goroutine leaked")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func register(t *testing.T, service *Service, hook domain.Hook) {
	t.Helper()
	if hook.Phase == "" {
		hook.Phase = domain.PhaseRequest
	}
	if hook.TimeoutMS == 0 {
		hook.TimeoutMS = domain.DefaultTimeoutMS
	}
	if err := service.Register(hook); err != nil {
		t.Fatalf("register %s/%s: %v", hook.PluginID, hook.HookID, err)
	}
}

func answering(decision domain.Decision) func(domain.Invocation) *domain.Decision {
	return func(domain.Invocation) *domain.Decision { return &decision }
}

var chatRequest = domain.Request{
	ActivityID: "act-1", Model: "gpt-4o", Endpoint: "chat", Method: "POST", Path: "/v1/chat/completions",
	Bytes: 16, Body: `{"model":"gpt-4o"}`,
}

func TestRegisterChecksTheGrantAndConnection(t *testing.T) {
	unconnected := NewService()
	hook := domain.Hook{PluginID: "acme.tools", HookID: "a", Phase: domain.PhaseRequest, TimeoutMS: 2000}
	if err := unconnected.Register(hook); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unconnected register: %v", err)
	}
	service, grants, _ := setup(t)
	grants.revoke("acme.tools")
	if err := service.Register(hook); !errors.Is(err, ErrNotGranted) {
		t.Fatalf("ungranted register: %v", err)
	}
	hook.HookID = "Bad"
	var validation domain.ValidationError
	if err := service.Register(hook); !errors.As(err, &validation) {
		t.Fatalf("invalid hook: %v", err)
	}
}

func TestRegisterLimits(t *testing.T) {
	service, _, _ := setup(t)
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "one"})
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "two"})
	if err := service.Register(domain.Hook{PluginID: "acme.tools", HookID: "three", Phase: domain.PhaseRequest, TimeoutMS: 2000}); !errors.Is(err, ErrLimit) {
		t.Fatalf("third request hook: %v", err)
	}
	// Replacing a hook does not count against the limit; the other phase has its own.
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "two", Models: []string{"gpt-*"}})
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "seen", Phase: domain.PhaseResponse})
	for index := range 5 {
		register(t, service, domain.Hook{PluginID: fmt.Sprintf("plugin-%d", index), HookID: "h"})
	}
	if err := service.Register(domain.Hook{PluginID: "other.one", HookID: "h", Phase: domain.PhaseRequest, TimeoutMS: 2000}); !errors.Is(err, ErrLimit) {
		t.Fatalf("ninth hook: %v", err)
	}
	register(t, service, domain.Hook{PluginID: "plugin-0", HookID: "h", Models: []string{"x"}})
	if removed := service.Reset(); removed != domain.MaxHooks {
		t.Fatalf("reset removed %d, want %d", removed, domain.MaxHooks)
	}
}

func TestReplacingKeepsTheOrder(t *testing.T) {
	service, _, host := setup(t)
	register(t, service, domain.Hook{PluginID: "first.one", HookID: "h"})
	register(t, service, domain.Hook{PluginID: "second.one", HookID: "h"})
	register(t, service, domain.Hook{PluginID: "first.one", HookID: "h", Models: []string{"gpt-*"}})
	host.answer("first.one", "h", answering(domain.Decision{Action: domain.ActionContinue}))
	host.answer("second.one", "h", answering(domain.Decision{Action: domain.ActionContinue}))
	service.Before(context.Background(), chatRequest)
	calls := host.calls()
	if len(calls) != 2 || calls[0].PluginID != "first.one" || calls[1].PluginID != "second.one" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestBeforeWithoutHooksContinues(t *testing.T) {
	service, _, host := setup(t)
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "h", Models: []string{"claude-*"}})
	decision := service.Before(context.Background(), chatRequest)
	if decision.Action != domain.ActionContinue || len(host.calls()) != 0 {
		t.Fatalf("decision = %+v, calls = %d", decision, len(host.calls()))
	}
	if service.Active(domain.PhaseRequest, "gpt-4o", "chat") || !service.Active(domain.PhaseRequest, "claude-3", "chat") {
		t.Fatal("Active disagrees with the model filter")
	}
	if service.Active(domain.PhaseResponse, "claude-3", "chat") {
		t.Fatal("Active must keep phases apart")
	}
}

func TestBeforeContinueCarriesTags(t *testing.T) {
	service, _, host := setup(t)
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "h"})
	host.answer("acme.tools", "h", answering(domain.Decision{Action: domain.ActionContinue, Tags: []string{"seen"}}))
	decision := service.Before(context.Background(), chatRequest)
	if decision.Action != domain.ActionContinue || strings.Join(decision.Tags, ",") != "seen" {
		t.Fatalf("decision = %+v", decision)
	}
	calls := host.calls()
	if len(calls) != 1 || len(calls[0].CallID) != 32 || calls[0].Request.Body != chatRequest.Body {
		t.Fatalf("invocation = %+v", calls)
	}
}

func TestBeforeRewritesInChain(t *testing.T) {
	service, _, host := setup(t)
	register(t, service, domain.Hook{PluginID: "first.one", HookID: "h"})
	register(t, service, domain.Hook{PluginID: "second.one", HookID: "h", Models: []string{"gpt-4o-mini"}})
	register(t, service, domain.Hook{PluginID: "third.one", HookID: "h", Models: []string{"gpt-4o"}})
	host.answer("first.one", "h", answering(domain.Decision{
		Action: domain.ActionRewrite, Model: "gpt-4o-mini", Body: `{"model":"gpt-4o-mini"}`, Tags: []string{"a"},
	}))
	var seen domain.Request
	host.answer("second.one", "h", func(invocation domain.Invocation) *domain.Decision {
		seen = invocation.Request
		return &domain.Decision{Action: domain.ActionRewrite, Body: "not json", Tags: []string{"b", "a"}}
	})
	decision := service.Before(context.Background(), chatRequest)
	if decision.Action != domain.ActionRewrite || decision.Model != "gpt-4o-mini" || decision.Body != `{"model":"gpt-4o-mini"}` {
		t.Fatalf("decision = %+v", decision)
	}
	if strings.Join(decision.Tags, ",") != "a,b" {
		t.Fatalf("tags = %v", decision.Tags)
	}
	if seen.Model != "gpt-4o-mini" || seen.Body != `{"model":"gpt-4o-mini"}` || seen.Bytes != int64(len(seen.Body)) {
		t.Fatalf("the second hook saw %+v", seen)
	}
	// The rewrite moved the request out of the third hook's filter.
	if calls := host.calls(); len(calls) != 2 {
		t.Fatalf("%d calls, want 2", len(calls))
	}
}

func TestOmittedBodyCannotBeRewritten(t *testing.T) {
	service, _, host := setup(t)
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "h"})
	host.answer("acme.tools", "h", answering(domain.Decision{Action: domain.ActionRewrite, Body: `{}`}))
	request := chatRequest
	request.Body, request.BodyOmitted = "", true
	if decision := service.Before(context.Background(), request); decision.Action != domain.ActionContinue || decision.Body != "" {
		t.Fatalf("relay-omitted body rewritten: %+v", decision)
	}
	host.mu.Lock()
	host.omitBody = true
	host.mu.Unlock()
	if decision := service.Before(context.Background(), chatRequest); decision.Action != domain.ActionContinue || decision.Body != "" {
		t.Fatalf("transport-omitted body rewritten: %+v", decision)
	}
}

func TestBeforeBlockStopsTheChain(t *testing.T) {
	service, _, host := setup(t)
	register(t, service, domain.Hook{PluginID: "first.one", HookID: "h"})
	register(t, service, domain.Hook{PluginID: "second.one", HookID: "h"})
	host.answer("first.one", "h", answering(domain.Decision{
		Action: domain.ActionBlock, Block: &domain.Block{Status: 403, Message: "Not here"}, Tags: []string{"blocked"},
	}))
	host.answer("second.one", "h", answering(domain.Decision{Action: domain.ActionContinue}))
	decision := service.Before(context.Background(), chatRequest)
	if decision.Action != domain.ActionBlock || decision.Block == nil || decision.Block.Status != 403 || decision.Block.Message != "Not here" {
		t.Fatalf("decision = %+v", decision)
	}
	if strings.Join(decision.Tags, ",") != "blocked" || len(host.calls()) != 1 {
		t.Fatalf("tags = %v, calls = %d", decision.Tags, len(host.calls()))
	}
}

func TestTimeoutsFailOpenThenUnregister(t *testing.T) {
	service, _, host := setup(t)
	register(t, service, domain.Hook{PluginID: "slow.one", HookID: "h", TimeoutMS: domain.MinTimeoutMS})
	register(t, service, domain.Hook{PluginID: "fast.one", HookID: "h", TimeoutMS: domain.MinTimeoutMS})
	host.answer("fast.one", "h", answering(domain.Decision{Action: domain.ActionContinue, Tags: []string{"fast"}}))
	for round := range domain.MaxConsecutiveTimeouts {
		started := time.Now()
		decision := service.Before(context.Background(), chatRequest)
		if decision.Action != domain.ActionContinue || strings.Join(decision.Tags, ",") != "fast" {
			t.Fatalf("round %d: decision = %+v", round, decision)
		}
		if elapsed := time.Since(started); elapsed < 90*time.Millisecond || elapsed > time.Second {
			t.Fatalf("round %d took %v", round, elapsed)
		}
	}
	host.mu.Lock()
	removals := append([]Removal(nil), host.removals...)
	host.mu.Unlock()
	if len(removals) != 1 || removals[0] != (Removal{PluginID: "slow.one", HookID: "h", Phase: domain.PhaseRequest, Reason: RemovalTimeouts}) {
		t.Fatalf("removals = %+v", removals)
	}
	if !service.Active(domain.PhaseRequest, "gpt-4o", "chat") {
		t.Fatal("the answering hook must stay")
	}
	if removed, _ := service.Unregister("slow.one", ""); removed != 0 {
		t.Fatal("the slow hook was not removed")
	}
	// A late answer to an expired call is not accepted.
	late := host.calls()[0].CallID
	if accepted, err := service.Respond(late, domain.Decision{Action: domain.ActionContinue}); accepted || err != nil {
		t.Fatalf("late answer: accepted=%v err=%v", accepted, err)
	}
}

func TestAnswerResetsTheTimeoutCount(t *testing.T) {
	service, _, host := setup(t)
	register(t, service, domain.Hook{PluginID: "flaky.one", HookID: "h", TimeoutMS: domain.MinTimeoutMS})
	for round := range 2 * domain.MaxConsecutiveTimeouts {
		if round%domain.MaxConsecutiveTimeouts == domain.MaxConsecutiveTimeouts-1 {
			host.answer("flaky.one", "h", answering(domain.Decision{Action: domain.ActionContinue}))
		} else {
			host.answer("flaky.one", "h", nil)
		}
		service.Before(context.Background(), chatRequest)
	}
	if !service.Active(domain.PhaseRequest, "gpt-4o", "chat") {
		t.Fatal("a hook that answered every third call was removed")
	}
}

func TestGrantIsCheckedAtInvokeTime(t *testing.T) {
	service, grants, host := setup(t)
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "h"})
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "o", Phase: domain.PhaseResponse})
	host.answer("acme.tools", "h", answering(domain.Decision{Action: domain.ActionBlock, Block: &domain.Block{Status: 403, Message: "x"}}))
	grants.revoke("acme.tools")
	if service.Active(domain.PhaseRequest, "gpt-4o", "chat") || service.Active(domain.PhaseResponse, "gpt-4o", "chat") {
		t.Fatal("an ungranted plugin's hooks are active")
	}
	if decision := service.Before(context.Background(), chatRequest); decision.Action != domain.ActionContinue {
		t.Fatalf("an ungranted plugin decided: %+v", decision)
	}
	service.After(chatRequest, domain.Response{Status: 200})
	waitIdle(t, service)
	host.mu.Lock()
	observed := len(host.observations)
	host.mu.Unlock()
	if len(host.calls()) != 0 || observed != 0 {
		t.Fatal("an ungranted plugin was told about a request")
	}
}

func TestRespondValidates(t *testing.T) {
	service, _, host := setup(t)
	if accepted, err := service.Respond(strings.Repeat("ab", 16), domain.Decision{Action: domain.ActionContinue}); accepted || err != nil {
		t.Fatalf("unknown call: accepted=%v err=%v", accepted, err)
	}
	if accepted, _ := service.Respond("nope", domain.Decision{Action: domain.ActionContinue}); accepted {
		t.Fatal("malformed call id accepted")
	}
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "h"})
	results := make(chan error, 1)
	host.answer("acme.tools", "h", func(invocation domain.Invocation) *domain.Decision {
		_, err := service.Respond(invocation.CallID, domain.Decision{Action: domain.ActionBlock, Block: &domain.Block{Status: 500, Message: "x"}})
		results <- err
		return nil
	})
	started := time.Now()
	decision := service.Before(context.Background(), chatRequest)
	var validation domain.ValidationError
	if err := <-results; !errors.As(err, &validation) || validation.Field != "block.status" {
		t.Fatalf("invalid decision: %v", err)
	}
	if decision.Action != domain.ActionContinue || time.Since(started) > time.Second {
		t.Fatalf("an invalid decision must continue at once: %+v", decision)
	}
}

func TestCallerContextEndsTheWait(t *testing.T) {
	service, _, host := setup(t)
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "h", TimeoutMS: domain.MaxTimeoutMS})
	ctx, cancel := context.WithCancel(context.Background())
	host.answer("acme.tools", "h", func(domain.Invocation) *domain.Decision {
		cancel()
		return nil
	})
	started := time.Now()
	service.Before(ctx, chatRequest)
	if time.Since(started) > time.Second {
		t.Fatal("a cancelled request kept waiting")
	}
	service.mu.Lock()
	timeouts := service.hooks[0].timeouts
	service.mu.Unlock()
	if timeouts != 0 {
		t.Fatal("a client that left must not count against the hook")
	}
}

func TestTotalBudget(t *testing.T) {
	service, _, _ := setup(t)
	service.budget = 150 * time.Millisecond
	register(t, service, domain.Hook{PluginID: "first.one", HookID: "h", TimeoutMS: domain.MaxTimeoutMS})
	register(t, service, domain.Hook{PluginID: "second.one", HookID: "h", TimeoutMS: domain.MaxTimeoutMS})
	started := time.Now()
	if decision := service.Before(context.Background(), chatRequest); decision.Action != domain.ActionContinue {
		t.Fatalf("decision = %+v", decision)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("the budget did not bound the hooks: %v", elapsed)
	}
}

func TestUnregisterReleasesWaitingRequests(t *testing.T) {
	service, _, host := setup(t)
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "h", TimeoutMS: domain.MaxTimeoutMS})
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "o", Phase: domain.PhaseResponse})
	host.answer("acme.tools", "h", func(domain.Invocation) *domain.Decision {
		service.UnregisterPlugin("acme.tools")
		return nil
	})
	started := time.Now()
	service.Before(context.Background(), chatRequest)
	if time.Since(started) > time.Second {
		t.Fatal("removing a hook did not release its waiting request")
	}
	if service.Active(domain.PhaseRequest, "x", "chat") || service.Active(domain.PhaseResponse, "x", "chat") {
		t.Fatal("hooks remain after the plugin was removed")
	}
	if removed, err := service.Unregister("acme.tools", ""); removed != 0 || err != nil {
		t.Fatalf("unregister after removal: %d, %v", removed, err)
	}
}

func TestUnregister(t *testing.T) {
	service, _, _ := setup(t)
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "a"})
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "b", Phase: domain.PhaseResponse})
	register(t, service, domain.Hook{PluginID: "other.one", HookID: "a"})
	if removed, err := service.Unregister("acme.tools", "a"); removed != 1 || err != nil {
		t.Fatalf("one hook: %d, %v", removed, err)
	}
	if removed, err := service.Unregister("acme.tools", ""); removed != 1 || err != nil {
		t.Fatalf("whole plugin: %d, %v", removed, err)
	}
	if _, err := service.Unregister("acme.tools", "Bad"); err == nil {
		t.Fatal("invalid hook id accepted")
	}
	if _, err := service.Unregister("", ""); err == nil {
		t.Fatal("missing plugin id accepted")
	}
	if removed := service.Reset(); removed != 1 {
		t.Fatalf("reset removed %d", removed)
	}
}

func TestEmitFailureContinues(t *testing.T) {
	service, _, host := setup(t)
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "h", TimeoutMS: domain.MaxTimeoutMS})
	host.mu.Lock()
	host.failInvoke = true
	host.mu.Unlock()
	started := time.Now()
	if decision := service.Before(context.Background(), chatRequest); decision.Action != domain.ActionContinue || time.Since(started) > time.Second {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestPendingCallsAreBounded(t *testing.T) {
	service, _, host := setup(t)
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "h"})
	service.mu.Lock()
	for index := range MaxPendingCalls {
		service.pending[fmt.Sprintf("%032x", index)] = &pendingCall{answer: make(chan callResult, 1)}
	}
	service.mu.Unlock()
	if decision := service.Before(context.Background(), chatRequest); decision.Action != domain.ActionContinue || len(host.calls()) != 0 {
		t.Fatal("a call over the bound was made")
	}
	service.mu.Lock()
	clear(service.pending)
	service.mu.Unlock()
}

func TestAfterObservesWithoutTheRequestBody(t *testing.T) {
	service, _, host := setup(t)
	host.observed = make(chan struct{}, 4)
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "o", Phase: domain.PhaseResponse, Endpoints: []string{"chat"}})
	register(t, service, domain.Hook{PluginID: "other.one", HookID: "o", Phase: domain.PhaseResponse, Models: []string{"claude-*"}})
	response := domain.Response{Status: 200, LatencyMS: 42, Tokens: &domain.Tokens{Input: 1, Output: 2, Total: 3}, Body: `{"ok":true}`}
	service.After(chatRequest, response)
	<-host.observed
	waitIdle(t, service)
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.observations) != 1 {
		t.Fatalf("%d observations, want 1", len(host.observations))
	}
	got := host.observations[0]
	if got.PluginID != "acme.tools" || got.HookID != "o" || got.Request.Body != "" || got.Request.Bytes != chatRequest.Bytes {
		t.Fatalf("observation = %+v", got)
	}
	if got.Response.Body != `{"ok":true}` || got.Response.Tokens.Total != 3 || got.Response.LatencyMS != 42 {
		t.Fatalf("response = %+v", got.Response)
	}
}

func TestAfterDropsWhenTheQueueIsFull(t *testing.T) {
	service, _, host := setup(t)
	host.blockObserve = make(chan struct{})
	register(t, service, domain.Hook{PluginID: "acme.tools", HookID: "o", Phase: domain.PhaseResponse})
	started := time.Now()
	for range 4 * MaxQueuedObservations {
		service.After(chatRequest, domain.Response{Status: 200})
	}
	if time.Since(started) > time.Second {
		t.Fatal("After blocked")
	}
	service.mu.Lock()
	queued := len(service.queue)
	service.mu.Unlock()
	if queued > MaxQueuedObservations {
		t.Fatalf("%d queued, bound %d", queued, MaxQueuedObservations)
	}
	close(host.blockObserve)
	waitIdle(t, service)
	host.mu.Lock()
	delivered := len(host.observations)
	host.mu.Unlock()
	if delivered > MaxQueuedObservations+1 || delivered == 0 {
		t.Fatalf("%d delivered", delivered)
	}
}

func TestConcurrentTraffic(t *testing.T) {
	service, grants, host := setup(t)
	for _, plugin := range []string{"first.one", "second.one", "third.one"} {
		register(t, service, domain.Hook{PluginID: plugin, HookID: "h", TimeoutMS: domain.MinTimeoutMS})
		register(t, service, domain.Hook{PluginID: plugin, HookID: "o", Phase: domain.PhaseResponse})
		host.answer(plugin, "h", answering(domain.Decision{Action: domain.ActionRewrite, Model: "gpt-4o", Tags: []string{"t"}}))
	}
	var group sync.WaitGroup
	for worker := range 16 {
		group.Go(func() {
			for range 20 {
				request := chatRequest
				request.ActivityID = fmt.Sprintf("act-%d", worker)
				service.Before(context.Background(), request)
				service.Active(domain.PhaseRequest, request.Model, request.Endpoint)
				service.After(request, domain.Response{Status: 200})
			}
		})
	}
	group.Go(func() {
		for round := range 20 {
			_, _ = service.Unregister("third.one", "h")
			_ = service.Register(domain.Hook{PluginID: "third.one", HookID: "h", Phase: domain.PhaseRequest, TimeoutMS: domain.MinTimeoutMS})
			if round == 10 {
				grants.revoke("second.one")
			}
			_, _ = service.Respond(strings.Repeat("0", 32), domain.Decision{Action: domain.ActionContinue})
		}
	})
	group.Wait()
	service.UnregisterPlugin("first.one")
	waitIdle(t, service)
}
