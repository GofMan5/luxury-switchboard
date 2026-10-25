package activity

import (
	"context"
	"errors"
	"testing"
	"time"

	activityapp "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	activitydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
)

type fakeHistory struct {
	requests []activitydomain.Request
	err      error
	period   activityapp.Period
	limit    int
}

func (history *fakeHistory) Record(activitydomain.Request) bool { return true }

func (history *fakeHistory) Recent(_ context.Context, period activityapp.Period, limit int) ([]activitydomain.Request, error) {
	history.period, history.limit = period, limit
	return history.requests, history.err
}

func (history *fakeHistory) Stats(context.Context, activityapp.Period) (activityapp.HistoryStats, error) {
	return activityapp.HistoryStats{}, nil
}

func (history *fakeHistory) Close(context.Context) error { return nil }

func TestRecentFailuresKeepsOnlyFailedRequests(t *testing.T) {
	at := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	history := &fakeHistory{requests: []activitydomain.Request{
		{UpdatedAt: at, State: activitydomain.StateCompleted, Status: 200, Model: "m"},
		{UpdatedAt: at.Add(time.Minute), State: activitydomain.StateFailed, Status: 503, ErrorCode: "upstream_status", ErrorDetail: "overloaded", ProviderID: "fixture-a", ProviderName: "Fixture A", Model: "gpt-4o"},
		{UpdatedAt: at.Add(2 * time.Minute), State: activitydomain.StateCancelled, ErrorCode: "client_cancelled"},
		{UpdatedAt: at.Add(3 * time.Minute), State: activitydomain.StateFailed, ErrorCode: "transport", ErrorDetail: "connection reset", ProviderName: "Fixture B", Model: "claude"},
	}}
	failures, err := New(history).RecentFailures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if history.period != activityapp.PeriodAll || history.limit != 500 {
		t.Fatalf("unexpected read: %s %d", history.period, history.limit)
	}
	if len(failures) != 2 {
		t.Fatalf("unexpected failures: %+v", failures)
	}
	first := failures[0]
	if !first.At.Equal(at.Add(time.Minute)) || first.Status != 503 || first.ErrorCode != "upstream_status" || first.Detail != "overloaded" || first.ProviderID != "fixture-a" || first.ProviderName != "Fixture A" || first.Model != "gpt-4o" {
		t.Fatalf("unexpected mapping: %+v", first)
	}
	if failures[1].Status != 0 || failures[1].ErrorCode != "transport" {
		t.Fatalf("unexpected transport mapping: %+v", failures[1])
	}
}

func TestUnavailableHistoryIsAnError(t *testing.T) {
	if _, err := New(nil).RecentFailures(context.Background()); err == nil {
		t.Fatal("a missing history answered")
	}
	if _, err := New(&fakeHistory{err: errors.New("locked")}).RecentFailures(context.Background()); err == nil {
		t.Fatal("a failed read answered")
	}
	failures, err := New(&fakeHistory{}).RecentFailures(context.Background())
	if err != nil || failures == nil {
		t.Fatalf("an empty history must answer an empty list: %v %v", failures, err)
	}
}
