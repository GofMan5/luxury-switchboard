package application

import (
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
)

func TestReasoningEffortIsAllowlisted(t *testing.T) {
	cases := map[string]string{
		"high": "high", " XHigh ": "xhigh", "minimal": "minimal", "none": "none",
		"max": "max", "auto": "auto", "on": "on",
		// Client-controlled text outside the dialects' names never persists.
		"ultra": "", "high; secret": "", "": "",
	}
	for input, want := range cases {
		service := NewService(10)
		id := service.Start(domain.Start{Path: "/v1/responses", ReasoningEffort: input, ReasoningBudget: -5})
		request := service.List(1)[0]
		if request.ID != id || request.ReasoningEffort != want || request.ReasoningBudget != 0 {
			t.Errorf("effort %q: got %+v, want %q", input, request, want)
		}
	}
	service := NewService(10)
	service.Start(domain.Start{ReasoningEffort: "on", ReasoningBudget: 1 << 40})
	if got := service.List(1)[0].ReasoningBudget; got != maxReasoningBudget {
		t.Fatalf("an implausible budget was not bounded: %d", got)
	}
}

func TestThinkingPhasePublishesTransitionsAndAccumulates(t *testing.T) {
	service := NewService(10)
	now := time.Date(2026, 7, 31, 1, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	var events []domain.Request
	service.OnChanged(func(request domain.Request) { events = append(events, request) })
	id := service.Start(domain.Start{Path: "/v1/responses", ReasoningEffort: "high"})
	started := len(events)

	service.Thinking(id, true)
	// Repeated reports of the same phase are one SSE event each upstream;
	// they must not become one activity frame each.
	service.Thinking(id, true)
	service.Thinking(id, true)
	if got := len(events) - started; got != 1 {
		t.Fatalf("repeated phase reports published %d events, want 1", got)
	}
	if !events[len(events)-1].Thinking {
		t.Fatalf("thinking phase was not published: %+v", events[len(events)-1])
	}
	now = now.Add(300 * time.Millisecond)
	service.Thinking(id, false)
	service.Thinking(id, false)
	if got := len(events) - started; got != 2 {
		t.Fatalf("phase exit published %d events in total, want 2", got)
	}
	answering := events[len(events)-1]
	if answering.Thinking || answering.ThinkingMS != 300 {
		t.Fatalf("closed span was not accumulated: %+v", answering)
	}
	// A second span — reasoning resumed after a tool call — adds to the total,
	// and Finish closes a span the stream left open.
	service.Thinking(id, true)
	now = now.Add(200 * time.Millisecond)
	service.Finish(id, domain.Finish{Status: 200})
	finished := service.List(1)[0]
	if finished.Thinking || finished.ThinkingMS != 500 || finished.State != domain.StateCompleted {
		t.Fatalf("finish did not close the open span: %+v", finished)
	}
	// A late report from a stream still draining must not reopen history.
	before := len(events)
	service.Thinking(id, true)
	if len(events) != before || service.List(1)[0].Thinking {
		t.Fatal("a finished request was reopened by a late phase report")
	}
}

func TestRetryClosesTheReasoningSpanOfTheReplacedAttempt(t *testing.T) {
	service := NewService(10)
	now := time.Date(2026, 7, 31, 1, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	id := service.Start(domain.Start{Path: "/v1/chat/completions"})
	service.Thinking(id, true)
	now = now.Add(100 * time.Millisecond)
	service.Retry(id, domain.Retry{Attempt: 2, Status: 503, Delay: time.Second})
	request := service.List(1)[0]
	if request.Thinking || request.ThinkingMS != 100 {
		t.Fatalf("retry left the replaced attempt thinking: %+v", request)
	}
	service.Thinking("unknown", true)
}
