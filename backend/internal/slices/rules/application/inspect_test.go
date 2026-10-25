package application

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/rules/domain"
)

type failures struct {
	records []FailureRecord
	err     error
}

func (history failures) RecentFailures(context.Context) ([]FailureRecord, error) {
	return history.records, history.err
}

func TestTestCountsNothingAndConsultsRetryOnlyForFailures(t *testing.T) {
	service, _ := newService(t, &memory{})
	routed := requestRule("routed", domain.Effects{Provider: "fixture-a", Tags: []string{"x"}})
	routed.Match.Endpoints = []string{domain.EndpointChat}
	limited := requestRule("limited", domain.Effects{RateLimit: &domain.RateLimit{Requests: 1, WindowSeconds: 60}, Stop: true})
	mustSaveRequest(t, service, routed, "")
	mustSaveRequest(t, service, limited, "")
	mustSaveRequest(t, service, requestRule("after-stop", domain.Effects{}), "")
	mustSaveRetry(t, service, retryRule("transport", domain.RetryMatch{ErrorCodes: []string{"transport"}}, domain.RetryRetry), "")
	mustSaveRetry(t, service, retryRule("any-5xx", domain.RetryMatch{Statuses: []int{500, 502, 503}, Providers: []string{"fixture-a"}}, domain.RetryRotateKey), "")

	for range 3 {
		result := service.Test(TestInput{Model: "m", Endpoint: domain.EndpointChat})
		if strings.Join(result.RequestRules, ",") != "routed,limited" || result.Effects.Provider != "fixture-a" || result.Effects.RateLimit == nil || result.RetryRule != "" {
			t.Fatalf("unexpected result: %+v", result)
		}
	}
	for id, stats := range service.Stats() {
		if stats.Hits != 0 || stats.WindowRequests != 0 {
			t.Fatalf("test counted %s: %+v", id, stats)
		}
	}
	if result := service.Test(TestInput{Model: "m", Endpoint: domain.EndpointEmbeddings, Status: 503, ProviderID: "fixture-a"}); result.RetryRule != "any-5xx" || strings.Join(result.RequestRules, ",") != "limited" {
		t.Fatalf("unexpected failure result: %+v", result)
	}
	if result := service.Test(TestInput{Model: "m", Endpoint: domain.EndpointOther, ErrorCode: "transport"}); result.RetryRule != "transport" {
		t.Fatalf("a transport failure did not match: %+v", result)
	}
	if result := service.Test(TestInput{Model: "m", Endpoint: domain.EndpointOther, Status: 503, ProviderID: "fixture-b"}); result.RetryRule != "" {
		t.Fatalf("a provider filter was ignored: %+v", result)
	}
	if service.Stats()["any-5xx"].Hits != 0 {
		t.Fatal("testing a retry rule counted a hit")
	}
	if empty := service.Test(TestInput{Model: "m", Endpoint: domain.EndpointChat}); empty.RequestRules == nil {
		t.Fatal("requestRules must encode as a list")
	}
}

func TestRecentErrorsGroupsByStatusCodeAndNormalizedDetail(t *testing.T) {
	base := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	history := failures{records: []FailureRecord{
		{At: base.Add(5 * time.Minute), Status: 503, ErrorCode: "upstream_status", Detail: "Overloaded  (request 12345)", ProviderID: "fixture-a", ProviderName: "Fixture A", Model: "gpt-4o"},
		{At: base.Add(1 * time.Minute), Status: 503, ErrorCode: "upstream_status", Detail: "overloaded (request 99)", ProviderID: "fixture-b", ProviderName: "Fixture B", Model: "gpt-4o-mini"},
		{At: base.Add(3 * time.Minute), Status: 503, ErrorCode: "upstream_status", Detail: "OVERLOADED (request 7)", ProviderID: "fixture-a", ProviderName: "Fixture A", Model: "gpt-4o"},
		{At: base.Add(4 * time.Minute), Status: 0, ErrorCode: "transport", Detail: "connection reset", ProviderID: "fixture-a", ProviderName: "Fixture A", Model: "gpt-4o"},
		{At: base.Add(2 * time.Minute), Status: 429, ErrorCode: "upstream_status", Detail: "overloaded (request 1)", ProviderID: "fixture-a", ProviderName: "Fixture A", Model: "gpt-4o"},
		{At: base, Status: 400, ErrorCode: "upstream_status", Detail: strings.Repeat("é", 300), ProviderName: "Fixture A", Model: "gpt-4o"},
	}}
	service, err := NewService(&memory{}, history)
	if err != nil {
		t.Fatal(err)
	}
	_ = service.Load(context.Background())
	mustSaveRetry(t, service, retryRule("overloaded", domain.RetryMatch{Statuses: []int{503}, Contains: []string{"overloaded"}}, domain.RetryRetry), "")
	mustSaveRetry(t, service, retryRule("transport", domain.RetryMatch{ErrorCodes: []string{"transport"}}, domain.RetryRetry), "")

	recent, err := service.RecentErrors(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 4 {
		t.Fatalf("unexpected groups: %+v", recent)
	}
	top := recent[0]
	if top.Status != 503 || top.Count != 3 || !top.LastAt.Equal(base.Add(5*time.Minute)) || top.Detail != "Overloaded  (request 12345)" || top.ProviderName != "Fixture A" || top.CoveredBy != "overloaded" {
		t.Fatalf("unexpected top group: %+v", top)
	}
	if len(top.Signature) != 16 || strings.Trim(top.Signature, "0123456789abcdef") != "" {
		t.Fatalf("unexpected signature: %q", top.Signature)
	}
	byCode := map[string]RecentError{}
	for _, group := range recent[1:] {
		byCode[group.ErrorCode+strconv.Itoa(group.Status)] = group
	}
	if transport := byCode["transport0"]; transport.CoveredBy != "transport" || transport.Count != 1 {
		t.Fatalf("unexpected transport group: %+v", transport)
	}
	if limited := byCode["upstream_status429"]; limited.CoveredBy != "" {
		t.Fatalf("a 429 was covered by a 503 rule: %+v", limited)
	}
	if long := byCode["upstream_status400"]; len([]rune(long.Detail)) != 200 {
		t.Fatalf("the detail was not bounded: %d", len([]rune(long.Detail)))
	}
	if service.Stats()["overloaded"].Hits != 0 {
		t.Fatal("recentErrors counted a retry hit")
	}
	again, _ := service.RecentErrors(context.Background(), 1)
	if len(again) != 1 || again[0].Signature != top.Signature {
		t.Fatalf("the limit or the signature is unstable: %+v", again)
	}
}

func TestRecentErrorsWithoutHistoryIsAnError(t *testing.T) {
	service, _ := newService(t, &memory{})
	if _, err := service.RecentErrors(context.Background(), 10); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatalf("missing history answered: %v", err)
	}
	broken, _ := NewService(&memory{}, failures{err: errors.New("database is locked")})
	if _, err := broken.RecentErrors(context.Background(), 10); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatalf("a failed read answered: %v", err)
	}
	empty, _ := NewService(&memory{}, failures{})
	if recent, err := empty.RecentErrors(context.Background(), 10); err != nil || recent == nil || len(recent) != 0 {
		t.Fatalf("an empty history must answer an empty list: %v %v", recent, err)
	}
}

func TestNormalizeDetail(t *testing.T) {
	for input, want := range map[string]string{
		"Rate limit: 12 of 10 used\n\tretry in 3.5s": "rate limit: # of # used retry in #.#s",
		"  leading and trailing  ":                   "leading and trailing",
		"":                                           "",
	} {
		if got := normalizeDetail(input); got != want {
			t.Errorf("normalizeDetail(%q) = %q, want %q", input, got, want)
		}
	}
}
