package relay

import (
	"context"
	"slices"
	"testing"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/rules/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/rules/domain"
)

type memory struct{ saved *domain.Set }

func (store *memory) Load(context.Context) (domain.Set, bool, error) { return domain.Set{}, false, nil }

func (store *memory) Save(_ context.Context, set domain.Set) error {
	store.saved = &set
	return nil
}

func TestEndpointNamesAgreeWithTheRelay(t *testing.T) {
	if !slices.Equal(domain.Endpoints, relayapp.Endpoints) {
		t.Fatalf("the rules endpoints %v differ from the relay's %v", domain.Endpoints, relayapp.Endpoints)
	}
	for _, action := range []string{relayapp.RetryActionRetry, relayapp.RetryActionRotateKey, relayapp.RetryActionSwitchProvider, relayapp.RetryActionFail} {
		if !slices.Contains([]string{domain.RetryRetry, domain.RetryRotateKey, domain.RetrySwitch, domain.RetryFail}, action) {
			t.Fatalf("the relay action %q is not a rules action", action)
		}
	}
}

func TestAdapterTranslatesOutcomesAndDecisions(t *testing.T) {
	ctx := context.Background()
	service, err := application.NewService(&memory{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveRequestRule(ctx, domain.RequestRule{ID: "route", Name: "Route", Enabled: true, Effects: domain.Effects{
		Provider: "fixture-a", Model: "rewritten", Headers: map[string]string{"X-Team": "a"}, Tags: []string{"team"},
		Budget: &domain.Budget{Tokens: 100, Period: domain.BudgetPeriodHour},
	}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveRetryRule(ctx, domain.RetryRule{ID: "slow", Name: "Slow", Enabled: true,
		Match: domain.RetryMatch{Statuses: []int{503}, Contains: []string{"try again"}}, Action: domain.RetrySwitch, MaxAttempts: 2, DelayMS: 250}, ""); err != nil {
		t.Fatal(err)
	}
	rules := New(service)
	outcome := rules.Evaluate(relayapp.RuleRequest{Model: "m", Endpoint: relayapp.EndpointChat})
	if outcome.ProviderID != "fixture-a" || outcome.Model != "rewritten" || outcome.Headers["X-Team"] != "a" || !slices.Equal(outcome.Tags, []string{"team"}) || !slices.Equal(outcome.Budgeted, []string{"route"}) || outcome.Block != nil || outcome.Limit != nil {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	rules.SpendTokens(outcome.Budgeted, 100)
	refused := rules.Evaluate(relayapp.RuleRequest{Model: "m", Endpoint: relayapp.EndpointChat})
	if refused.Limit == nil || refused.Limit.RuleName != "Route" || refused.Limit.RetryAfter <= 0 {
		t.Fatalf("unexpected refusal: %+v", refused)
	}
	decision, ok := rules.Retry(relayapp.RetryQuery{ProviderID: "fixture-a", Model: "m", Status: 503, ErrorCode: "upstream_status", ErrorText: "Please TRY_AGAIN later"})
	if !ok || decision.RuleID != "slow" || decision.Action != relayapp.RetryActionSwitchProvider || decision.MaxAttempts != 2 || decision.Delay != 250*time.Millisecond {
		t.Fatalf("unexpected decision: %+v %v", decision, ok)
	}
	if _, ok := rules.Retry(relayapp.RetryQuery{Status: 500, ErrorText: "try again"}); ok {
		t.Fatal("an unmatched failure found a rule")
	}

	if _, err := service.SaveRequestRule(ctx, domain.RequestRule{ID: "block", Name: "Block", Enabled: true, Effects: domain.Effects{Block: &domain.Block{Status: 503, Message: "Closed."}}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReorderRequestRules(ctx, []string{"block", "route"}, ""); err != nil {
		t.Fatal(err)
	}
	blocked := rules.Evaluate(relayapp.RuleRequest{Model: "m", Endpoint: relayapp.EndpointOther})
	if blocked.Block == nil || blocked.Block.Status != 503 || blocked.Block.Message != "Closed." {
		t.Fatalf("unexpected block: %+v", blocked)
	}
}
