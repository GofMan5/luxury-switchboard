package application

import (
	"strconv"
	"strings"
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

func TestRelayErrorCodesSurviveFinish(t *testing.T) {
	for _, code := range []string{"policy_refusal", "chat_compatibility", "responses_compatibility", "guardrail_blocked", "pool_busy", "rule_blocked", "rule_limited", "hook_blocked"} {
		service := NewService(10)
		id := service.Start(domain.Start{Path: "/v1/responses"})
		service.Finish(id, domain.Finish{Status: 502, ErrorCode: code, ErrorDetail: "relay detail"})
		request := service.List(1)[0]
		if request.State != domain.StateFailed || request.ErrorCode != code || request.ErrorDetail != "relay detail" {
			t.Errorf("relay code %q was dropped: %+v", code, request)
		}
	}
}

func TestUnknownErrorCodeClearsDetail(t *testing.T) {
	service := NewService(10)
	id := service.Start(domain.Start{Path: "/v1/responses"})
	service.Finish(id, domain.Finish{Status: 502, ErrorCode: "private upstream detail", ErrorDetail: "must not persist alone"})
	request := service.List(1)[0]
	if request.State != domain.StateFailed || request.ErrorCode != "" || request.ErrorDetail != "" {
		t.Fatalf("unknown error left residue: %+v", request)
	}
}

func TestP95IndexBoundaries(t *testing.T) {
	cases := map[int]int{-5: 0, 0: 0, 1: 0, 2: 1, 19: 18, 20: 18, 21: 19, 100: 94}
	for count, want := range cases {
		if got := P95Index(count); got != want {
			t.Fatalf("P95Index(%d) = %d, want %d", count, got, want)
		}
	}
}

func TestErrorDetailIsRetainedWithControlAndSizeBounds(t *testing.T) {
	service := NewService(10)
	id := service.Start(domain.Start{Path: "/v1/responses"})
	detail := "provider detail\nrequest id: abc"
	service.Finish(id, domain.Finish{Status: 502, ErrorCode: "transport", ErrorDetail: detail})
	request := service.List(1)[0]
	if request.ErrorCode != "transport" || request.ErrorDetail != detail {
		t.Fatalf("error detail was not retained: %+v", request)
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
	if request.TotalTokens != 140 || request.ContextTokens != 100 {
		t.Fatalf("token usage was inflated: %+v", request)
	}
}

// The decode rate is the relay's measurement, not output over generation:
// that ratio counted prefill and queueing as decode time and halved fast
// providers. The service files the measured rate and the first-token time,
// and only for a completed answer.
func TestFinishFilesMeasuredRateAndFirstToken(t *testing.T) {
	service := NewService(10)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	id := service.Start(domain.Start{Path: "/v1/chat/completions"})
	now = now.Add(3 * time.Second)
	service.Finish(id, domain.Finish{
		Status: 200, OutputTokens: 200, Generation: 3 * time.Second,
		FirstToken: 250 * time.Millisecond, TokensPerSecond: 85.5,
	})
	request := service.List(1)[0]
	if request.TokensPerSecond != 85.5 || request.TTFTMS != 250 || request.LatencyMS != 3_000 {
		t.Fatalf("measured rate or first token was not filed: %+v", request)
	}

	failed := service.Start(domain.Start{Path: "/v1/chat/completions"})
	service.Finish(failed, domain.Finish{
		Status: 200, ErrorCode: "stream_incomplete", OutputTokens: 200,
		FirstToken: time.Millisecond, TokensPerSecond: 85.5,
	})
	if request := service.List(1)[0]; request.TokensPerSecond != 0 {
		t.Fatalf("a broken stream must not file a decode rate: %+v", request)
	}

	unmeasured := service.Start(domain.Start{Path: "/v1/responses"})
	service.Finish(unmeasured, domain.Finish{Status: 200, OutputTokens: 40, Generation: time.Second})
	if request := service.List(1)[0]; request.TokensPerSecond != 0 || request.TTFTMS != 0 {
		t.Fatalf("an unmeasured answer must not invent a rate: %+v", request)
	}
}

func TestRequestIDsDoNotCollideAcrossServiceRestarts(t *testing.T) {
	first := NewService(10).Start(domain.Start{})
	second := NewService(10).Start(domain.Start{})
	if first == second {
		t.Fatalf("request id was reused across service instances: %s", first)
	}
}

func TestTrimmedInflightRequestStillPublishesItsTerminalEvent(t *testing.T) {
	service := NewService(1)
	var terminalEvent domain.Request
	service.OnChanged(func(request domain.Request) {
		if request.State == domain.StateCompleted {
			terminalEvent = request
		}
	})
	first := service.Start(domain.Start{Model: "first"})
	service.Start(domain.Start{Model: "second"})
	service.Finish(first, domain.Finish{Status: 200})
	if terminalEvent.ID != first || terminalEvent.State != domain.StateCompleted {
		t.Fatalf("trimmed inflight request lost its terminal event: %+v", terminalEvent)
	}
	if _, retained := service.requests[first]; retained {
		t.Fatal("hidden terminal request was retained after publication")
	}
}

func TestTagsAreValidatedMergedAndBounded(t *testing.T) {
	service := NewService(10)
	id := service.Start(domain.Start{Path: "/v1/responses", Tags: []string{"team.a", "Bad Tag", "team.a", "-lead", "ok_1"}})
	if tags := service.List(1)[0].Tags; strings.Join(tags, ",") != "team.a,ok_1" {
		t.Fatalf("start tags were not cleaned: %q", tags)
	}
	extra := []string{"ok_1", strings.Repeat("x", 33), "hook,comma"}
	for index := range 20 {
		extra = append(extra, "h"+strconv.Itoa(index))
	}
	service.Finish(id, domain.Finish{Status: 200, Tags: extra})
	tags := service.List(1)[0].Tags
	if len(tags) != domain.MaxTags || tags[0] != "team.a" || tags[1] != "ok_1" || tags[2] != "h0" {
		t.Fatalf("finish tags were not merged in order and capped: %q", tags)
	}
	untagged := service.Start(domain.Start{Path: "/v1/responses"})
	service.Finish(untagged, domain.Finish{Status: 200})
	if tags := service.List(1)[0].Tags; tags != nil {
		t.Fatalf("an untagged request carries tags: %q", tags)
	}
}
