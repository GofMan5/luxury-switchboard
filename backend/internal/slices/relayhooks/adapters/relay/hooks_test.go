package relayhooksrelay

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/relayhooks/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/relayhooks/domain"
)

type allGranted struct{}

func (allGranted) Granted(string, string) bool { return true }

// host answers every invocation with decide and records what it saw.
type host struct {
	service      *application.Service
	decide       func(domain.Invocation) domain.Decision
	mu           sync.Mutex
	invocations  []domain.Invocation
	observations chan domain.Observation
}

func (host *host) Invoke(invocation domain.Invocation) (bool, error) {
	host.mu.Lock()
	host.invocations = append(host.invocations, invocation)
	host.mu.Unlock()
	go func() { _, _ = host.service.Respond(invocation.CallID, host.decide(invocation)) }()
	return false, nil
}

func (host *host) Observe(observation domain.Observation) error {
	host.observations <- observation
	return nil
}

func (host *host) Removed(application.Removal) error { return nil }

func setup(t *testing.T, decide func(domain.Invocation) domain.Decision) (Hooks, *host) {
	t.Helper()
	service := application.NewService()
	fake := &host{service: service, decide: decide, observations: make(chan domain.Observation, 4)}
	service.Connect(allGranted{}, fake)
	for hookID, phase := range map[string]string{"h": domain.PhaseRequest, "o": domain.PhaseResponse} {
		if err := service.Register(domain.Hook{PluginID: "acme.tools", HookID: hookID, Phase: phase, TimeoutMS: domain.DefaultTimeoutMS}); err != nil {
			t.Fatal(err)
		}
	}
	return New(service), fake
}

// The domain cannot import the relay slice, so it spells the shared names
// itself; this keeps the two spellings from drifting apart.
func TestSharedNamesAgree(t *testing.T) {
	if strings.Join(domain.Endpoints, ",") != strings.Join(relayapp.Endpoints, ",") {
		t.Fatalf("endpoints differ: %v vs %v", domain.Endpoints, relayapp.Endpoints)
	}
	pairs := [][2]string{
		{domain.PhaseRequest, relayapp.HookPhaseRequest}, {domain.PhaseResponse, relayapp.HookPhaseResponse},
		{domain.ActionContinue, relayapp.HookActionContinue}, {domain.ActionRewrite, relayapp.HookActionRewrite},
		{domain.ActionBlock, relayapp.HookActionBlock},
	}
	for _, pair := range pairs {
		if pair[0] != pair[1] {
			t.Fatalf("%q != %q", pair[0], pair[1])
		}
	}
}

var request = relayapp.HookRequest{
	ActivityID: "act-1", Model: "gpt-4o", Endpoint: relayapp.EndpointChat, Method: "POST",
	Path: "/v1/chat/completions", Body: []byte(`{"model":"gpt-4o"}`), Bytes: 18,
}

func TestBeforeTranslatesRewrite(t *testing.T) {
	hooks, fake := setup(t, func(domain.Invocation) domain.Decision {
		return domain.Decision{Action: domain.ActionRewrite, Model: "gpt-4o-mini", Body: `{"model":"gpt-4o-mini"}`, Tags: []string{"rewritten"}}
	})
	if !hooks.Active(relayapp.HookPhaseRequest, "gpt-4o", relayapp.EndpointChat) {
		t.Fatal("hook is not active")
	}
	decision := hooks.Before(context.Background(), request)
	if decision.Action != relayapp.HookActionRewrite || decision.Model != "gpt-4o-mini" || string(decision.Body) != `{"model":"gpt-4o-mini"}` {
		t.Fatalf("decision = %+v", decision)
	}
	if strings.Join(decision.Tags, ",") != "rewritten" {
		t.Fatalf("tags = %v", decision.Tags)
	}
	fake.mu.Lock()
	seen := fake.invocations[0].Request
	fake.mu.Unlock()
	if seen.Body != `{"model":"gpt-4o"}` || seen.Bytes != 18 || seen.ActivityID != "act-1" || seen.Path != request.Path {
		t.Fatalf("hook saw %+v", seen)
	}
}

func TestBeforeTranslatesBlockAndContinue(t *testing.T) {
	hooks, _ := setup(t, func(invocation domain.Invocation) domain.Decision {
		if invocation.Request.Model == "blocked" {
			return domain.Decision{Action: domain.ActionBlock, Block: &domain.Block{Status: 429, Message: "Later"}, Tags: []string{"b"}}
		}
		return domain.Decision{Action: domain.ActionContinue}
	})
	blocked := request
	blocked.Model = "blocked"
	decision := hooks.Before(context.Background(), blocked)
	if decision.Action != relayapp.HookActionBlock || decision.BlockStatus != 429 || decision.BlockMessage != "Later" || len(decision.Tags) != 1 {
		t.Fatalf("block = %+v", decision)
	}
	if decision := hooks.Before(context.Background(), request); decision.Action != relayapp.HookActionContinue || decision.Body != nil || decision.Model != "" {
		t.Fatalf("continue = %+v", decision)
	}
}

func TestBeforeOmitsBinaryBodies(t *testing.T) {
	hooks, fake := setup(t, func(domain.Invocation) domain.Decision {
		return domain.Decision{Action: domain.ActionRewrite, Body: `{}`}
	})
	binary := request
	binary.Body = []byte{0xff, 0xfe, '{'}
	if decision := hooks.Before(context.Background(), binary); decision.Action != relayapp.HookActionContinue || decision.Body != nil {
		t.Fatalf("an omitted body was rewritten: %+v", decision)
	}
	fake.mu.Lock()
	seen := fake.invocations[0].Request
	fake.mu.Unlock()
	if seen.Body != "" || !seen.BodyOmitted {
		t.Fatalf("binary body reached the hook: %+v", seen)
	}
}

func TestAfterTranslatesObservation(t *testing.T) {
	hooks, fake := setup(t, func(domain.Invocation) domain.Decision { return domain.Decision{Action: domain.ActionContinue} })
	hooks.After(relayapp.HookObservation{
		Request: request, Status: 502, ErrorCode: "upstream_status", LatencyMS: 120,
		Tokens: &relayapp.TokenUsage{InputTokens: 10, OutputTokens: 5, CachedTokens: 2, TotalTokens: 15},
		Body:   []byte(`{"error":"x"}`),
	})
	select {
	case observation := <-fake.observations:
		if observation.Request.Body != "" || observation.Request.Bytes != 18 {
			t.Fatalf("request = %+v", observation.Request)
		}
		response := observation.Response
		if response.Status != 502 || response.ErrorCode != "upstream_status" || response.LatencyMS != 120 || response.Body != `{"error":"x"}` {
			t.Fatalf("response = %+v", response)
		}
		if response.Tokens == nil || *response.Tokens != (domain.Tokens{Input: 10, Output: 5, Total: 15}) {
			t.Fatalf("tokens = %+v", response.Tokens)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no observation")
	}
}
