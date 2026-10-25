package application

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/rules/domain"
)

type memory struct {
	mu      sync.Mutex
	saved   *domain.Set
	saves   int
	loadErr error
	saveErr error
}

func (store *memory) Load(context.Context) (domain.Set, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.loadErr != nil || store.saved == nil {
		return domain.Set{}, false, store.loadErr
	}
	return store.saved.Clone(), true, nil
}

func (store *memory) Save(_ context.Context, set domain.Set) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.saveErr != nil {
		return store.saveErr
	}
	copied := set.Clone()
	store.saved = &copied
	store.saves++
	return nil
}

// clock is a settable test clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (value *clock) Now() time.Time {
	value.mu.Lock()
	defer value.mu.Unlock()
	return value.now
}

func (value *clock) Advance(duration time.Duration) {
	value.mu.Lock()
	value.now = value.now.Add(duration)
	value.mu.Unlock()
}

func newService(t *testing.T, store *memory) (*Service, *clock) {
	t.Helper()
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = service.Load(context.Background())
	at := &clock{now: time.Date(2026, 3, 10, 14, 20, 0, 0, time.Local)}
	service.now = at.Now
	return service, at
}

func requestRule(id string, effects domain.Effects) domain.RequestRule {
	return domain.RequestRule{ID: id, Name: "Rule " + id, Enabled: true, Effects: effects}
}

func retryRule(id string, match domain.RetryMatch, action string) domain.RetryRule {
	return domain.RetryRule{ID: id, Name: "Retry " + id, Enabled: true, Match: match, Action: action, MaxAttempts: 3}
}

func mustSaveRequest(t *testing.T, service *Service, rule domain.RequestRule, actor string) domain.RequestRule {
	t.Helper()
	saved, err := service.SaveRequestRule(context.Background(), rule, actor)
	if err != nil {
		t.Fatalf("save %s: %v", rule.ID, err)
	}
	return saved
}

func mustSaveRetry(t *testing.T, service *Service, rule domain.RetryRule, actor string) domain.RetryRule {
	t.Helper()
	saved, err := service.SaveRetryRule(context.Background(), rule, actor)
	if err != nil {
		t.Fatalf("save %s: %v", rule.ID, err)
	}
	return saved
}

func TestNewServiceNeedsARepository(t *testing.T) {
	if _, err := NewService(nil, nil); err == nil {
		t.Fatal("a nil repository was accepted")
	}
}

func TestSavePersistsNormalizesAndAnnounces(t *testing.T) {
	store := &memory{}
	service, _ := newService(t, store)
	changes := 0
	service.OnChanged(func() { changes++ })
	saved := mustSaveRequest(t, service, domain.RequestRule{ID: " route ", Name: " Route ", Enabled: true}, "")
	if saved.ID != "route" || saved.Name != "Route" || saved.Match.Models == nil || store.saves != 1 || changes != 1 {
		t.Fatalf("unexpected save: %+v saves=%d changes=%d", saved, store.saves, changes)
	}
	reloaded, _ := newService(t, store)
	if list := reloaded.List(); len(list.RequestRules) != 1 || list.RequestRules[0].ID != "route" {
		t.Fatalf("the rule did not persist: %+v", list)
	}
	saved.Name = "Changed"
	if service.List().RequestRules[0].Name != "Route" {
		t.Fatal("the returned rule shares memory with the stored one")
	}
}

func TestSaveReplacesInPlaceAndKeepsOrder(t *testing.T) {
	service, _ := newService(t, &memory{})
	for _, id := range []string{"a", "b", "c"} {
		mustSaveRequest(t, service, requestRule(id, domain.Effects{}), "")
	}
	replaced := requestRule("b", domain.Effects{Model: "rewritten"})
	mustSaveRequest(t, service, replaced, "")
	list := service.List().RequestRules
	if len(list) != 3 || list[1].ID != "b" || list[1].Effects.Model != "rewritten" {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestOwnershipRules(t *testing.T) {
	ctx := context.Background()
	service, _ := newService(t, &memory{})
	plugin, other := "plugin:fixture-one", "plugin:fixture-two"
	owned := mustSaveRequest(t, service, requestRule("owned", domain.Effects{}), plugin)
	if owned.Owner != plugin {
		t.Fatalf("an actor's rule is not owned by it: %q", owned.Owner)
	}
	mustSaveRequest(t, service, requestRule("operator", domain.Effects{}), "")

	if _, err := service.SaveRequestRule(ctx, requestRule("owned", domain.Effects{}), other); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("another plugin replaced the rule: %v", err)
	}
	if _, err := service.SaveRequestRule(ctx, requestRule("operator", domain.Effects{}), plugin); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("a plugin replaced the operator's rule: %v", err)
	}
	if err := service.DeleteRequestRule(ctx, "owned", other); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("another plugin deleted the rule: %v", err)
	}
	disabled := requestRule("owned", domain.Effects{})
	disabled.Enabled = false
	disabled.Owner = ""
	if saved := mustSaveRequest(t, service, disabled, ""); saved.Owner != plugin {
		t.Fatalf("the operator's edit took the rule over: %q", saved.Owner)
	}
	smuggled := requestRule("smuggled", domain.Effects{})
	smuggled.Owner = plugin
	if saved := mustSaveRequest(t, service, smuggled, ""); saved.Owner != "" {
		t.Fatalf("the owner field is not server-assigned: %q", saved.Owner)
	}
	if _, err := service.ReorderRequestRules(ctx, []string{"operator", "owned", "smuggled"}, plugin); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("a plugin reordered: %v", err)
	}
	if _, err := service.ReorderRetryRules(ctx, nil, plugin); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("a plugin reordered retry rules: %v", err)
	}
	if err := service.DeleteRequestRule(ctx, "owned", plugin); err != nil {
		t.Fatalf("a plugin could not delete its own rule: %v", err)
	}
	var validation domain.ValidationError
	if _, err := service.SaveRequestRule(ctx, requestRule("x", domain.Effects{}), "admin"); !errors.As(err, &validation) {
		t.Fatalf("a malformed actor was accepted: %v", err)
	}
}

func TestIDsAreUniqueAcrossKinds(t *testing.T) {
	service, _ := newService(t, &memory{})
	mustSaveRequest(t, service, requestRule("shared", domain.Effects{}), "")
	_, err := service.SaveRetryRule(context.Background(), retryRule("shared", domain.RetryMatch{Statuses: []int{503}}, domain.RetryRetry), "")
	if err == nil || err.Error() != "Rule id is used by a request rule" {
		t.Fatalf("unexpected result: %v", err)
	}
	mustSaveRetry(t, service, retryRule("retry", domain.RetryMatch{Statuses: []int{503}}, domain.RetryRetry), "")
	if _, err := service.SaveRequestRule(context.Background(), requestRule("retry", domain.Effects{}), ""); err == nil || err.Error() != "Rule id is used by a retry rule" {
		t.Fatalf("unexpected result: %v", err)
	}
}

func TestCapsAndSizeAreEnforced(t *testing.T) {
	service, _ := newService(t, &memory{})
	for index := range domain.MaxRequestRules {
		mustSaveRequest(t, service, requestRule("r"+strconv.Itoa(index), domain.Effects{}), "")
	}
	_, err := service.SaveRequestRule(context.Background(), requestRule("overflow", domain.Effects{}), "")
	if err == nil || err.Error() != "Request rules are limited to 64" {
		t.Fatalf("the 65th rule: %v", err)
	}
	// The replacement of an existing rule is not blocked by the cap.
	mustSaveRequest(t, service, requestRule("r0", domain.Effects{Model: "m"}), "")

	large, _ := newService(t, &memory{})
	var refused error
	for index := range domain.MaxRequestRules {
		rule := requestRule("big"+strconv.Itoa(index), domain.Effects{})
		for glob := range 16 {
			rule.Match.Models = append(rule.Match.Models, strings.Repeat(string(rune('a'+glob)), 200))
		}
		if _, refused = large.SaveRequestRule(context.Background(), rule, ""); refused != nil {
			break
		}
	}
	if refused == nil || !strings.HasPrefix(refused.Error(), "Rules are too large to store") {
		t.Fatalf("an oversized set was stored: %v", refused)
	}
}

func TestReorderNeedsAPermutation(t *testing.T) {
	ctx := context.Background()
	service, _ := newService(t, &memory{})
	for _, id := range []string{"a", "b", "c"} {
		mustSaveRequest(t, service, requestRule(id, domain.Effects{}), "")
	}
	for _, ids := range [][]string{{"a", "b"}, {"a", "b", "b"}, {"a", "b", "x"}, {"a", "b", "c", "a"}} {
		if _, err := service.ReorderRequestRules(ctx, ids, ""); err == nil || err.Error() != "Reorder must list every request rule exactly once" {
			t.Errorf("%v: %v", ids, err)
		}
	}
	ordered, err := service.ReorderRequestRules(ctx, []string{"c", "a", "b"}, "")
	if err != nil || ordered[0].ID != "c" || ordered[1].ID != "a" || ordered[2].ID != "b" {
		t.Fatalf("unexpected order %+v: %v", ordered, err)
	}
	mustSaveRetry(t, service, retryRule("x", domain.RetryMatch{Statuses: []int{500}}, domain.RetryRetry), "")
	mustSaveRetry(t, service, retryRule("y", domain.RetryMatch{Statuses: []int{502}}, domain.RetryRetry), "")
	retries, err := service.ReorderRetryRules(ctx, []string{"y", "x"}, "")
	if err != nil || retries[0].ID != "y" {
		t.Fatalf("unexpected retry order %+v: %v", retries, err)
	}
}

func TestDeleteMissingRuleIsNotFound(t *testing.T) {
	service, _ := newService(t, &memory{})
	if err := service.DeleteRequestRule(context.Background(), "missing", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := service.DeleteRetryRule(context.Background(), "missing", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestAnUnreadableStoreRefusesWrites(t *testing.T) {
	store := &memory{loadErr: errors.New("locked")}
	service, err := NewService(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if service.Load(context.Background()) == nil {
		t.Fatal("the load failure was not reported")
	}
	if _, err := service.SaveRequestRule(context.Background(), requestRule("a", domain.Effects{}), ""); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("a write over an unreadable store: %v", err)
	}
	if store.saves != 0 {
		t.Fatal("the unreadable store was overwritten")
	}
}

func TestInvalidStoredRulesAreAnErrorNotAnEmptySet(t *testing.T) {
	bad := domain.Set{RequestRules: []domain.RequestRule{{ID: "BAD", Name: "x"}}}
	store := &memory{saved: &bad}
	service, _ := NewService(store, nil)
	if service.Load(context.Background()) == nil {
		t.Fatal("an invalid stored set loaded")
	}
	if _, err := service.SaveRequestRule(context.Background(), requestRule("a", domain.Effects{}), ""); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("an invalid stored set was overwritten: %v", err)
	}
}

func TestAFailedSaveChangesNothing(t *testing.T) {
	store := &memory{}
	service, _ := newService(t, store)
	changes := 0
	service.OnChanged(func() { changes++ })
	store.saveErr = errors.New("disk is full")
	if _, err := service.SaveRequestRule(context.Background(), requestRule("a", domain.Effects{}), ""); err == nil {
		t.Fatal("a failed save reported success")
	}
	if len(service.List().RequestRules) != 0 || changes != 0 {
		t.Fatal("a failed save was published")
	}
}

func TestDropOwnerRemovesOnlyThatOwnersRules(t *testing.T) {
	store := &memory{}
	service, _ := newService(t, store)
	mustSaveRequest(t, service, requestRule("mine", domain.Effects{}), "plugin:fixture-one")
	mustSaveRetry(t, service, retryRule("mine-retry", domain.RetryMatch{Statuses: []int{503}}, domain.RetryRetry), "plugin:fixture-one")
	mustSaveRequest(t, service, requestRule("theirs", domain.Effects{}), "plugin:fixture-two")
	mustSaveRequest(t, service, requestRule("operator", domain.Effects{}), "")
	saves := store.saves
	if err := service.DropOwner(context.Background(), "plugin:fixture-three"); err != nil || store.saves != saves {
		t.Fatalf("dropping an owner with no rules saved: %v", err)
	}
	if err := service.DropOwner(context.Background(), "plugin:fixture-one"); err != nil {
		t.Fatal(err)
	}
	list := service.List()
	if len(list.RequestRules) != 2 || len(list.RetryRules) != 0 || list.RequestRules[0].ID != "theirs" {
		t.Fatalf("unexpected rules left: %+v", list)
	}
}

func TestEvaluateMergesInOrderUntilStop(t *testing.T) {
	service, _ := newService(t, &memory{})
	first := requestRule("first", domain.Effects{Provider: "fixture-a", Headers: map[string]string{"X-One": "1"}, Tags: []string{"a", "b"}})
	first.Match.Models = []string{"gpt-*"}
	second := requestRule("second", domain.Effects{Provider: "fixture-b", Model: "rewritten", Headers: map[string]string{"X-ONE": "ignored", "X-Two": "2"}, Tags: []string{"b", "c"}, Stop: true})
	disabled := requestRule("disabled", domain.Effects{Model: "never"})
	disabled.Enabled = false
	elsewhere := requestRule("elsewhere", domain.Effects{Model: "never"})
	elsewhere.Match.Endpoints = []string{domain.EndpointEmbeddings}
	after := requestRule("after", domain.Effects{Tags: []string{"never"}})
	for _, rule := range []domain.RequestRule{disabled, elsewhere, first, second, after} {
		mustSaveRequest(t, service, rule, "")
	}
	outcome := service.Evaluate(Request{Model: "GPT-4o", Endpoint: domain.EndpointChat})
	if outcome.Provider != "fixture-a" || outcome.Model != "rewritten" || outcome.Block != nil || outcome.Limit != nil {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	if len(outcome.Headers) != 2 || outcome.Headers["X-One"] != "1" || outcome.Headers["X-Two"] != "2" {
		t.Fatalf("headers are not first-wins per name: %+v", outcome.Headers)
	}
	if strings.Join(outcome.Tags, ",") != "a,b,c" {
		t.Fatalf("tags are not a union in order: %v", outcome.Tags)
	}
	stats := service.Stats()
	if stats["first"].Hits != 1 || stats["second"].Hits != 1 || stats["after"].Hits != 0 || stats["disabled"].Hits != 0 || stats["elsewhere"].Hits != 0 {
		t.Fatalf("unexpected hits: %+v", stats)
	}
	if stats["first"].LastHitAt == nil || stats["after"].LastHitAt != nil {
		t.Fatal("lastHitAt must be set only after a hit")
	}
	if none := service.Evaluate(Request{Model: "claude", Endpoint: domain.EndpointEmbeddings}); none.Model != "never" {
		t.Fatalf("the embeddings rule did not match: %+v", none)
	}
}

func TestTagsAreCappedAtSixteen(t *testing.T) {
	service, _ := newService(t, &memory{})
	for index := range 3 {
		tags := make([]string, 0, 8)
		for tag := range 8 {
			tags = append(tags, "t"+strconv.Itoa(index)+"-"+strconv.Itoa(tag))
		}
		mustSaveRequest(t, service, requestRule("r"+strconv.Itoa(index), domain.Effects{Tags: tags}), "")
	}
	if tags := service.Evaluate(Request{Model: "m", Endpoint: domain.EndpointChat}).Tags; len(tags) != domain.MaxTags {
		t.Fatalf("tags were not capped: %d", len(tags))
	}
}

func TestBlockWinsBeforeLimitsAndCountsNoWindow(t *testing.T) {
	service, _ := newService(t, &memory{})
	mustSaveRequest(t, service, requestRule("limited", domain.Effects{RateLimit: &domain.RateLimit{Requests: 1, WindowSeconds: 60}}), "")
	mustSaveRequest(t, service, requestRule("blocked", domain.Effects{Block: &domain.Block{Status: 403, Message: "No."}}), "")
	for range 3 {
		outcome := service.Evaluate(Request{Model: "m", Endpoint: domain.EndpointChat})
		if outcome.Block == nil || outcome.Block.Status != 403 || outcome.Block.Message != "No." || outcome.Limit != nil {
			t.Fatalf("unexpected outcome: %+v", outcome)
		}
	}
	stats := service.Stats()
	if stats["limited"].WindowRequests != 0 || stats["limited"].Hits != 3 || stats["blocked"].Hits != 3 {
		t.Fatalf("a blocked request was counted against a window: %+v", stats)
	}
}

func TestRateLimitIsAFixedWindowFromTheFirstAdmission(t *testing.T) {
	service, at := newService(t, &memory{})
	mustSaveRequest(t, service, requestRule("limited", domain.Effects{RateLimit: &domain.RateLimit{Requests: 2, WindowSeconds: 60}}), "")
	request := Request{Model: "m", Endpoint: domain.EndpointChat}
	start := at.Now()
	for range 2 {
		if outcome := service.Evaluate(request); outcome.Limit != nil {
			t.Fatalf("admission within the limit refused: %+v", outcome)
		}
		at.Advance(10 * time.Second)
	}
	refused := service.Evaluate(request)
	if refused.Limit == nil || refused.Limit.RuleName != "Rule limited" || refused.Limit.RetryAfter != 40*time.Second {
		t.Fatalf("unexpected refusal: %+v", refused.Limit)
	}
	stats := service.Stats()["limited"]
	if stats.WindowRequests != 2 || stats.WindowResetsAt == nil || !stats.WindowResetsAt.Equal(start.Add(time.Minute)) || stats.Hits != 3 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	at.Advance(40 * time.Second)
	if outcome := service.Evaluate(request); outcome.Limit != nil {
		t.Fatalf("the next window refused: %+v", outcome.Limit)
	}
	if stats := service.Stats()["limited"]; stats.WindowRequests != 1 || !stats.WindowResetsAt.Equal(at.Now().Add(time.Minute)) {
		t.Fatalf("the new window did not open at its first admission: %+v", stats)
	}
}

func TestLimitsAreAllOrNothingAndNameTheLongestWait(t *testing.T) {
	service, at := newService(t, &memory{})
	mustSaveRequest(t, service, requestRule("short", domain.Effects{RateLimit: &domain.RateLimit{Requests: 1, WindowSeconds: 10}}), "")
	mustSaveRequest(t, service, requestRule("long", domain.Effects{RateLimit: &domain.RateLimit{Requests: 1, WindowSeconds: 100}}), "")
	mustSaveRequest(t, service, requestRule("roomy", domain.Effects{RateLimit: &domain.RateLimit{Requests: 50, WindowSeconds: 100}}), "")
	request := Request{Model: "m", Endpoint: domain.EndpointChat}
	service.Evaluate(request)
	at.Advance(time.Second)
	refused := service.Evaluate(request)
	if refused.Limit == nil || refused.Limit.RuleName != "Rule long" || refused.Limit.RetryAfter != 99*time.Second {
		t.Fatalf("unexpected refusal: %+v", refused.Limit)
	}
	if stats := service.Stats()["roomy"]; stats.WindowRequests != 1 {
		t.Fatalf("a refused request counted against an open window: %+v", stats)
	}
	at.Advance(10 * time.Second)
	if refused := service.Evaluate(request); refused.Limit == nil || refused.Limit.RuleName != "Rule long" {
		t.Fatalf("one exhausted window must still refuse: %+v", refused.Limit)
	}
	if stats := service.Stats()["short"]; stats.WindowRequests != 0 {
		t.Fatalf("a refused request opened a window: %+v", stats)
	}
}

func TestBudgetsResetOnTheLocalHourAndChargeTokens(t *testing.T) {
	service, at := newService(t, &memory{})
	mustSaveRequest(t, service, requestRule("hourly", domain.Effects{Budget: &domain.Budget{Requests: 3, Tokens: 1000, Period: domain.BudgetPeriodHour}}), "")
	mustSaveRequest(t, service, requestRule("requests-only", domain.Effects{Budget: &domain.Budget{Requests: 100, Period: domain.BudgetPeriodDay}}), "")
	request := Request{Model: "m", Endpoint: domain.EndpointChat}
	admitted := service.Evaluate(request)
	if strings.Join(admitted.Budgeted, ",") != "hourly" {
		t.Fatalf("only token budgets are charged: %v", admitted.Budgeted)
	}
	service.SpendTokens(admitted.Budgeted, 600)
	service.Evaluate(request)
	service.SpendTokens([]string{"hourly", "missing", "requests-only"}, 500)
	refused := service.Evaluate(request)
	if refused.Limit == nil || refused.Limit.RuleName != "Rule hourly" || refused.Limit.RetryAfter != 40*time.Minute {
		t.Fatalf("a spent token budget did not refuse until the hour: %+v", refused.Limit)
	}
	stats := service.Stats()
	hourEnd := time.Date(2026, 3, 10, 15, 0, 0, 0, time.Local)
	if hourly := stats["hourly"]; hourly.WindowRequests != 2 || hourly.WindowTokens != 1100 || !hourly.WindowResetsAt.Equal(hourEnd) {
		t.Fatalf("unexpected hourly stats: %+v", hourly)
	}
	dayEnd := time.Date(2026, 3, 11, 0, 0, 0, 0, time.Local)
	if daily := stats["requests-only"]; daily.WindowRequests != 2 || daily.WindowTokens != 0 || !daily.WindowResetsAt.Equal(dayEnd) {
		t.Fatalf("unexpected daily stats: %+v", daily)
	}
	at.Advance(40 * time.Minute)
	if outcome := service.Evaluate(request); outcome.Limit != nil {
		t.Fatalf("the budget did not reset on the hour: %+v", outcome.Limit)
	}
	if hourly := service.Stats()["hourly"]; hourly.WindowRequests != 1 || hourly.WindowTokens != 0 {
		t.Fatalf("unexpected stats after the reset: %+v", hourly)
	}
}

func TestBudgetRequestCountRefuses(t *testing.T) {
	service, _ := newService(t, &memory{})
	mustSaveRequest(t, service, requestRule("daily", domain.Effects{Budget: &domain.Budget{Requests: 1, Period: domain.BudgetPeriodDay}}), "")
	request := Request{Model: "m", Endpoint: domain.EndpointChat}
	service.Evaluate(request)
	refused := service.Evaluate(request)
	if refused.Limit == nil || refused.Limit.RetryAfter != 9*time.Hour+40*time.Minute {
		t.Fatalf("unexpected refusal: %+v", refused.Limit)
	}
}

func TestStatsCoverEveryRuleAndDropDeletedOnes(t *testing.T) {
	service, _ := newService(t, &memory{})
	mustSaveRequest(t, service, requestRule("plain", domain.Effects{}), "")
	mustSaveRetry(t, service, retryRule("retry", domain.RetryMatch{Statuses: []int{503}}, domain.RetryRetry), "")
	stats := service.Stats()
	if len(stats) != 2 || stats["plain"].WindowResetsAt != nil || stats["retry"].Hits != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	service.Evaluate(Request{Model: "m", Endpoint: domain.EndpointChat})
	if err := service.DeleteRequestRule(context.Background(), "plain", ""); err != nil {
		t.Fatal(err)
	}
	mustSaveRequest(t, service, requestRule("plain", domain.Effects{}), "")
	if stats := service.Stats(); stats["plain"].Hits != 0 {
		t.Fatalf("a deleted rule's counters survived: %+v", stats["plain"])
	}
}

func TestRetryFirstEnabledMatchWinsAndCountsAHit(t *testing.T) {
	service, _ := newService(t, &memory{})
	disabled := retryRule("disabled", domain.RetryMatch{Statuses: []int{503}}, domain.RetryFail)
	disabled.Enabled = false
	mustSaveRetry(t, service, disabled, "")
	mustSaveRetry(t, service, retryRule("overloaded", domain.RetryMatch{Contains: []string{"overloaded"}}, domain.RetrySwitch), "")
	slow := retryRule("unavailable", domain.RetryMatch{Statuses: []int{503}}, domain.RetryRetry)
	slow.DelayMS = 1500
	mustSaveRetry(t, service, slow, "")
	decision, ok := service.Retry(domain.Failure{Status: 503, ErrorText: "Service Unavailable"})
	if !ok || decision.RuleID != "unavailable" || decision.Action != domain.RetryRetry || decision.MaxAttempts != 3 || decision.Delay != 1500*time.Millisecond {
		t.Fatalf("unexpected decision: %+v %v", decision, ok)
	}
	decision, ok = service.Retry(domain.Failure{Status: 503, ErrorText: "Model OVERLOADED"})
	if !ok || decision.RuleID != "overloaded" {
		t.Fatalf("the first matching rule did not win: %+v", decision)
	}
	if _, ok := service.Retry(domain.Failure{Status: 400, ErrorText: "bad request"}); ok {
		t.Fatal("an unmatched failure found a rule")
	}
	stats := service.Stats()
	if stats["unavailable"].Hits != 1 || stats["overloaded"].Hits != 1 || stats["disabled"].Hits != 0 {
		t.Fatalf("unexpected hits: %+v", stats)
	}
}

func TestEvaluateIsSafeConcurrently(t *testing.T) {
	service, _ := newService(t, &memory{})
	mustSaveRequest(t, service, requestRule("limited", domain.Effects{RateLimit: &domain.RateLimit{Requests: 50, WindowSeconds: 60}, Budget: &domain.Budget{Tokens: 1_000_000, Period: domain.BudgetPeriodDay}}), "")
	var group sync.WaitGroup
	admitted := make(chan bool, 200)
	for range 200 {
		group.Add(1)
		go func() {
			defer group.Done()
			outcome := service.Evaluate(Request{Model: "m", Endpoint: domain.EndpointChat})
			service.SpendTokens(outcome.Budgeted, 10)
			admitted <- outcome.Limit == nil
		}()
	}
	group.Add(1)
	go func() {
		defer group.Done()
		_, _ = service.SaveRequestRule(context.Background(), requestRule("other", domain.Effects{}), "")
		_ = service.Stats()
	}()
	group.Wait()
	close(admitted)
	count := 0
	for ok := range admitted {
		if ok {
			count++
		}
	}
	if count != 50 {
		t.Fatalf("admitted %d requests under a limit of 50", count)
	}
	if stats := service.Stats()["limited"]; stats.WindowTokens != 500 {
		t.Fatalf("unexpected tokens: %+v", stats)
	}
}
