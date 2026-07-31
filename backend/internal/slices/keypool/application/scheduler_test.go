package application

import (
	"context"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

func testKey(t *testing.T, provider, label, secret string, priority, rpm int) domain.Key {
	t.Helper()
	key, err := domain.NewKey(domain.Params{
		ProviderID: provider, Label: label, Secret: secret, Priority: priority, RPM: rpm,
	})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestRequestScopedFailuresKeepPrimaryKeyEligible(t *testing.T) {
	scheduler := NewScheduler(10)
	primary := testKey(t, "echo", "Primary", "primary-secret", 0, 0)
	fallback := testKey(t, "echo", "Fallback", "fallback-secret", 1, 0)
	if err := scheduler.Configure("echo", 0, []domain.Key{primary, fallback}); err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []domain.OutcomeKind{
		domain.OutcomeRequestError, domain.OutcomeServerError, domain.OutcomeTransport,
	} {
		lease, _, err := scheduler.Acquire(context.Background(), "echo", "gpt-test")
		if err != nil {
			t.Fatal(err)
		}
		if lease.Key().ID != primary.ID {
			t.Fatalf("request-scoped failure rotated to %s", lease.Key().Label)
		}
		lease.Finish(domain.Outcome{Kind: outcome})
	}
}

func TestRPMUsesFallbackAndLiveUpdateWakesPrimary(t *testing.T) {
	scheduler := NewScheduler(10)
	primary := testKey(t, "echo", "Lite", "lite-secret", 0, 1)
	fallback := testKey(t, "echo", "Pro", "pro-secret", 1, 0)
	if err := scheduler.Configure("echo", 0, []domain.Key{primary, fallback}); err != nil {
		t.Fatal(err)
	}
	first, _, _ := scheduler.Acquire(context.Background(), "echo", "gpt-test")
	second, _, _ := scheduler.Acquire(context.Background(), "echo", "gpt-test")
	if first.Key().ID != primary.ID || second.Key().ID != fallback.ID {
		t.Fatalf("unexpected priority: %s then %s", first.Key().Label, second.Key().Label)
	}
	primary.RPM = 2
	if err := scheduler.Configure("echo", 0, []domain.Key{primary, fallback}); err != nil {
		t.Fatal(err)
	}
	third, _, _ := scheduler.Acquire(context.Background(), "echo", "gpt-test")
	if third.Key().ID != primary.ID {
		t.Fatal("live RPM update did not make primary eligible")
	}
}

func TestModelBlockFallsThroughAndAllBlockedWaitsForRecovery(t *testing.T) {
	scheduler := NewScheduler(10)
	primary := testKey(t, "echo", "Lite", "lite-secret", 0, 0)
	fallback := testKey(t, "echo", "Pro", "pro-secret", 1, 0)
	if err := scheduler.Configure("echo", 0, []domain.Key{primary, fallback}); err != nil {
		t.Fatal(err)
	}
	lease, _, _ := scheduler.Acquire(context.Background(), "echo", "gpt-5.6-sol")
	lease.Finish(domain.Outcome{Kind: domain.OutcomeModelUnavailable, Model: "gpt-5.6-sol"})
	lease, _, _ = scheduler.Acquire(context.Background(), "echo", "gpt-5.6-sol")
	if lease.Key().ID != fallback.ID {
		t.Fatal("model block did not fall through to fallback")
	}
	lease.Finish(domain.Outcome{Kind: domain.OutcomeModelUnavailable, Model: "gpt-5.6-sol"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err := scheduler.Acquire(ctx, "echo", "gpt-5.6-sol")
	if err == nil || time.Since(started) < 20*time.Millisecond {
		t.Fatalf("all-key model block escaped the recovery queue: %v", err)
	}
}

func TestManualResetPreservesRPMWindowAndRetryTelemetry(t *testing.T) {
	scheduler := NewScheduler(10)
	key := testKey(t, "echo", "Primary", "reset-secret", 0, 600)
	if err := scheduler.Configure("echo", 0, []domain.Key{key}); err != nil {
		t.Fatal(err)
	}
	lease, _, _ := scheduler.Acquire(context.Background(), "echo", "blocked-model")
	lease.Finish(domain.Outcome{Kind: domain.OutcomeRateLimited, RetryAfter: time.Minute})
	before := scheduler.Snapshot("echo")[0]
	if before.CooldownMS <= 0 || before.StartsInWindow != 1 || before.Retries429 != 1 {
		t.Fatalf("rate-limit state was not recorded: %+v", before)
	}
	if !scheduler.Reset("echo", key.ID) {
		t.Fatal("reset did not find key")
	}
	lease, _, err := scheduler.Acquire(context.Background(), "echo", "blocked-model")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := scheduler.Snapshot("echo")[0]
	if snapshot.StartsInWindow != 2 || snapshot.Retries429 != 1 || snapshot.CooldownMS != 0 {
		t.Fatalf("reset changed RPM/retry telemetry: %+v", snapshot)
	}
}

func TestQueueCallbackFiresOnceWhileWaitingForRPM(t *testing.T) {
	scheduler := NewScheduler(10)
	key := testKey(t, "echo", "Limited", "limited-secret", 0, 1)
	if err := scheduler.Configure("echo", 0, []domain.Key{key}); err != nil {
		t.Fatal(err)
	}
	lease, _, _ := scheduler.Acquire(context.Background(), "echo", "model")
	lease.Finish(domain.Outcome{Kind: domain.OutcomeSuccess})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	queued := 0
	_, _, _ = scheduler.AcquireWithQueue(ctx, "echo", "model", func() { queued++ })
	if queued != 1 {
		t.Fatalf("queue callback count=%d", queued)
	}
}
