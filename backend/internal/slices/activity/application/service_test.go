package application

import (
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
)

func TestActivityKeepsOnlySanitizedMetadataAndSummarizes(t *testing.T) {
	service := NewService(10)
	now := time.Date(2026, 7, 31, 1, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	id := service.Start(domain.Start{
		Model: "gpt-test", ProviderID: "echo", ProviderName: "Echo",
		Method: "POST", Path: "/v1/responses?secret=must-not-survive", BytesIn: 100,
	})
	now = now.Add(250 * time.Millisecond)
	service.Finish(id, domain.Finish{Status: 200, BytesOut: 200})

	requests := service.List(10)
	if len(requests) != 1 || requests[0].Path != "/v1/responses" {
		t.Fatalf("unexpected safe request: %+v", requests)
	}
	summary := service.Summary(15 * time.Minute)
	if summary.Requests != 1 || summary.SuccessRate != 100 || summary.P95MS != 250 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestUnknownErrorTextIsNotPersisted(t *testing.T) {
	service := NewService(10)
	id := service.Start(domain.Start{Path: "/v1/responses"})
	service.Finish(id, domain.Finish{Status: 502, ErrorCode: "private upstream detail"})
	request := service.List(1)[0]
	if request.State != domain.StateFailed || request.ErrorCode != "" {
		t.Fatalf("unsafe error was retained: %+v", request)
	}
}

func TestFinishDoesNotAddCachedOrReasoningTwice(t *testing.T) {
	service := NewService(10)
	id := service.Start(domain.Start{Path: "/v1/responses"})
	service.Finish(id, domain.Finish{
		Status: 200, InputTokens: 100, OutputTokens: 40,
		CachedTokens: 30, ReasoningTokens: 10, TotalTokens: 140,
		ContextTokens: 100, Generation: time.Second,
	})
	request := service.List(1)[0]
	if request.TotalTokens != 140 || request.ContextTokens != 100 || request.TokensPerSecond != 40 {
		t.Fatalf("token usage was inflated: %+v", request)
	}
}

func TestRequestIDsDoNotCollideAcrossServiceRestarts(t *testing.T) {
	first := NewService(10).Start(domain.Start{})
	second := NewService(10).Start(domain.Start{})
	if first == second {
		t.Fatalf("request id was reused across service instances: %s", first)
	}
}
