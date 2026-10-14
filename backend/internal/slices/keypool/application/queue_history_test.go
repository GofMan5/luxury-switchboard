package application

import (
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

func TestExpiredSiblingHistoryCannotHideOlderEligibleWaiter(t *testing.T) {
	scheduler := NewScheduler(10)
	now := time.Unix(100, 0)
	for _, id := range []string{"older", "current"} {
		if err := scheduler.Configure(id, 1, time.Second, []domain.Key{testKey(t, id, "key", "synthetic", 0, 1)}); err != nil {
			t.Fatal(err)
		}
	}
	older := &waiter{providerID: "older"}
	current := &waiter{providerID: "current"}
	scheduler.waiters = []*waiter{older, current}
	scheduler.providers["older"].starts = []time.Time{now.Add(-2 * time.Second)}
	scheduler.providers["older"].keys[0].starts = []time.Time{now.Add(-2 * time.Second)}
	selected, key := scheduler.firstDispatchableLocked(now)
	if selected != older || key == nil {
		t.Fatalf("expired sibling hides earlier eligible waiter: selected=%+v", selected)
	}
}

func TestExpiredIdleProviderCannotForceBusyWakeups(t *testing.T) {
	scheduler := NewScheduler(10)
	now := time.Unix(100, 0)
	if err := scheduler.Configure("idle", 1, time.Second, []domain.Key{testKey(t, "idle", "key", "synthetic", 0, 1)}); err != nil {
		t.Fatal(err)
	}
	scheduler.providers["idle"].starts = []time.Time{now.Add(-2 * time.Second)}
	scheduler.providers["idle"].keys[0].starts = []time.Time{now.Add(-2 * time.Second)}
	if got := scheduler.nextWakeLocked(now); got != time.Minute {
		t.Fatalf("expired idle history forced busy wake interval: %v", got)
	}
}
