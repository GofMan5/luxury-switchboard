package application

import (
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
)

func TestSummaryGaugesIncludeOldAndHiddenInflightRequests(t *testing.T) {
	service := NewService(4)
	now := time.Date(2026, 7, 31, 1, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	oldActive := service.Start(domain.Start{Model: "old-active"})
	oldQueued := service.Start(domain.Start{Model: "old-queued"})
	service.Waiting(oldQueued)
	oldCompleted := service.Start(domain.Start{Model: "old-completed"})
	service.Finish(oldCompleted, domain.Finish{Status: 200})

	now = now.Add(2 * time.Minute)
	service.Start(domain.Start{Model: "new-active"})
	queued := service.Start(domain.Start{Model: "new-queued"})
	service.Waiting(queued)
	completed := service.Start(domain.Start{Model: "new-completed"})
	failed := service.Start(domain.Start{Model: "new-failed"})
	now = now.Add(250 * time.Millisecond)
	service.Finish(completed, domain.Finish{Status: 200})
	service.Finish(failed, domain.Finish{Status: 502})

	assertSummary := func(active, queued int) {
		t.Helper()
		got := service.Summary(time.Minute)
		if got.Active != active || got.Queued != queued || got.Requests != 4 || got.RPM != 4 || got.SuccessRate != 50 || got.P95MS != 250 {
			t.Fatalf("gauges must include every inflight request while window statistics use startedAt: %+v; want active=%d queued=%d requests=4 rpm=4 success=50 p95=250", got, active, queued)
		}
	}
	assertSummary(2, 2)

	terminalEvents := 0
	service.OnChanged(func(request domain.Request) {
		if request.ID == oldActive || request.ID == oldQueued {
			if domain.IsTerminal(request.State) {
				terminalEvents++
			}
		}
	})
	service.Resume(oldQueued, time.Second)
	assertSummary(3, 1)
	service.Finish(oldActive, domain.Finish{Status: 200})
	assertSummary(2, 1)
	service.Finish(oldQueued, domain.Finish{Status: 200})
	assertSummary(1, 1)
	service.Resume(oldActive, 0)
	service.Waiting(oldQueued)
	service.Finish(oldActive, domain.Finish{Status: 200})
	assertSummary(1, 1)
	if terminalEvents != 2 {
		t.Fatalf("hidden requests published %d terminal events, want exactly two", terminalEvents)
	}
	for _, request := range service.List(10) {
		if request.ID == oldActive || request.ID == oldQueued || request.ID == oldCompleted {
			t.Fatalf("trimmed request resurrected in visible history: %+v", request)
		}
	}
}

func TestSummaryCountsHiddenStartedWindowRequestsOnce(t *testing.T) {
	service := NewService(1)
	now := time.Date(2026, 7, 31, 1, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	first := service.Start(domain.Start{})
	second := service.Start(domain.Start{})
	service.Waiting(first)
	got := service.Summary(time.Minute)
	if got.Requests != 2 || got.RPM != 2 || got.Active != 1 || got.Queued != 1 {
		t.Fatalf("hidden and visible requests must contribute exactly once: %+v", got)
	}
	service.Finish(first, domain.Finish{Status: 200})
	service.Finish(second, domain.Finish{Status: 200})
	got = service.Summary(time.Minute)
	if got.Requests != 1 || got.RPM != 1 || got.Active != 0 || got.Queued != 0 || got.SuccessRate != 100 {
		t.Fatalf("hidden terminal request remained in current work or visible history statistics: %+v", got)
	}
}

func TestFinishPreservesPeakContextSeparatelyFromAggregateBilling(t *testing.T) {
	for _, value := range []struct {
		name                                   string
		context, total, wantContext, wantTotal int64
	}{
		{"retry aggregate exceeds peak", 100, 240, 100, 240},
		{"total covers aggregate input", 100, 140, 100, 240},
		{"explicit context exceeds billed input", 300, 240, 300, 240},
		{"omitted context defaults to input", 0, 0, 200, 240},
		{"negative context defaults to input", -1, 0, 200, 240},
	} {
		t.Run(value.name, func(t *testing.T) {
			service := NewService(10)
			id := service.Start(domain.Start{Path: "/v1/responses"})
			service.Finish(id, domain.Finish{
				Status: 200, InputTokens: 200, OutputTokens: 40,
				CachedTokens: 30, ReasoningTokens: 10,
				ContextTokens: value.context, TotalTokens: value.total,
			})
			request := service.List(1)[0]
			if request.ContextTokens != value.wantContext || request.TotalTokens != value.wantTotal {
				t.Fatalf("context peak and billed aggregate conflated: context=%d total=%d; want context=%d total=%d", request.ContextTokens, request.TotalTokens, value.wantContext, value.wantTotal)
			}
		})
	}
}
