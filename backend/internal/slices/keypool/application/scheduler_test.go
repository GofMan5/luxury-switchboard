package application

import (
	"context"
	"errors"
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
	if err := scheduler.Configure("echo", 0, 0, []domain.Key{primary, fallback}); err != nil {
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
	if err := scheduler.Configure("echo", 0, 0, []domain.Key{primary, fallback}); err != nil {
		t.Fatal(err)
	}
	first, _, _ := scheduler.Acquire(context.Background(), "echo", "gpt-test")
	second, _, _ := scheduler.Acquire(context.Background(), "echo", "gpt-test")
	if first.Key().ID != primary.ID || second.Key().ID != fallback.ID {
		t.Fatalf("unexpected priority: %s then %s", first.Key().Label, second.Key().Label)
	}
	primary.RPM = 2
	if err := scheduler.Configure("echo", 0, 0, []domain.Key{primary, fallback}); err != nil {
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
	if err := scheduler.Configure("echo", 0, 0, []domain.Key{primary, fallback}); err != nil {
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

func TestCancelledAcquireDoesNotConsumeRateCapacity(t *testing.T) {
	scheduler := NewScheduler(10)
	key, err := domain.NewKey(domain.Params{ProviderID: "echo", Label: "Key", Secret: "secret", RPM: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Configure("echo", 1, 0, []domain.Key{key}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := scheduler.Acquire(ctx, "echo", "model"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquire returned %v", err)
	}
	if snapshot := scheduler.Snapshot("echo"); len(snapshot) != 1 || snapshot[0].StartsInWindow != 0 {
		t.Fatalf("cancelled acquire consumed RPM: %+v", snapshot)
	}
}

func TestManualResetPreservesRPMWindowAndRetryTelemetry(t *testing.T) {
	scheduler := NewScheduler(10)
	key := testKey(t, "echo", "Primary", "reset-secret", 0, 600)
	if err := scheduler.Configure("echo", 0, 0, []domain.Key{key}); err != nil {
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
	_, _, err := scheduler.Acquire(context.Background(), "echo", "blocked-model")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := scheduler.Snapshot("echo")[0]
	if snapshot.StartsInWindow != 2 || snapshot.Retries429 != 1 || snapshot.CooldownMS != 0 {
		t.Fatalf("reset changed RPM/retry telemetry: %+v", snapshot)
	}
}

// A provider that caps requests per second must recover within that second,
// not sit out the whole minute the default window would impose.
func TestPerSecondWindowRecoversWithinASecond(t *testing.T) {
	scheduler := NewScheduler(10)
	clock := time.Now()
	scheduler.now = func() time.Time { return clock }
	key := testKey(t, "echo", "Burst", "burst-secret", 0, 2)
	if err := scheduler.Configure("echo", 2, time.Second, []domain.Key{key}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		lease, _, err := scheduler.Acquire(context.Background(), "echo", "model")
		if err != nil {
			t.Fatal(err)
		}
		lease.Finish(domain.Outcome{Kind: domain.OutcomeSuccess})
	}
	exhausted, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := scheduler.Acquire(exhausted, "echo", "model"); !errors.Is(err, context.Canceled) {
		t.Fatalf("a spent per-second budget must still be spent: %v", err)
	}
	clock = clock.Add(1100 * time.Millisecond)
	if _, _, err := scheduler.Acquire(context.Background(), "echo", "model"); err != nil {
		t.Fatalf("per-second window did not lapse after a second: %v", err)
	}
}

// The unit is per provider, so a per-minute provider must keep its minute-long
// window even while a per-second provider is configured beside it.
func TestPerMinuteWindowIsUnaffectedByASecondProvider(t *testing.T) {
	scheduler := NewScheduler(10)
	clock := time.Now()
	scheduler.now = func() time.Time { return clock }
	slow := testKey(t, "slow", "Slow", "slow-secret", 0, 1)
	fast := testKey(t, "fast", "Fast", "fast-secret", 0, 1)
	if err := scheduler.Configure("slow", 1, 0, []domain.Key{slow}); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Configure("fast", 1, time.Second, []domain.Key{fast}); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"slow", "fast"} {
		lease, _, err := scheduler.Acquire(context.Background(), provider, "model")
		if err != nil {
			t.Fatal(err)
		}
		lease.Finish(domain.Outcome{Kind: domain.OutcomeSuccess})
	}
	clock = clock.Add(1100 * time.Millisecond)
	if _, _, err := scheduler.Acquire(context.Background(), "fast", "model"); err != nil {
		t.Fatalf("per-second provider did not recover: %v", err)
	}
	spent, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := scheduler.Acquire(spent, "slow", "model"); !errors.Is(err, context.Canceled) {
		t.Fatal("per-minute provider recovered after one second")
	}
	clock = clock.Add(time.Minute)
	if _, _, err := scheduler.Acquire(context.Background(), "slow", "model"); err != nil {
		t.Fatalf("per-minute window did not lapse after a minute: %v", err)
	}
}

func TestQueueCallbackFiresOnceWhileWaitingForRPM(t *testing.T) {
	scheduler := NewScheduler(10)
	key := testKey(t, "echo", "Limited", "limited-secret", 0, 1)
	if err := scheduler.Configure("echo", 0, 0, []domain.Key{key}); err != nil {
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

// A rotation takes a free key without queueing, and reports a miss instead of
// waiting when the pool has nothing dispatchable.
func TestTryAcquireTakesWithoutQueueingAndMissesOnCooldown(t *testing.T) {
	scheduler := NewScheduler(10)
	clock := time.Now()
	scheduler.now = func() time.Time { return clock }
	primary := testKey(t, "echo", "Primary", "primary-secret", 0, 0)
	fallback := testKey(t, "echo", "Fallback", "fallback-secret", 1, 0)
	if err := scheduler.Configure("echo", 0, 0, []domain.Key{primary, fallback}); err != nil {
		t.Fatal(err)
	}
	first, ok := scheduler.TryAcquire("echo", "model")
	if !ok || first.Key().ID != primary.ID {
		t.Fatalf("free key was not taken: ok=%v", ok)
	}
	// A cooled key is not dispatchable: the rotation ends instead of waiting
	// for the cooldown to lapse.
	first.Finish(domain.Outcome{Kind: domain.OutcomeRateLimited, RetryAfter: time.Minute})
	second, ok := scheduler.TryAcquire("echo", "model")
	if !ok || second.Key().ID != fallback.ID {
		t.Fatalf("rotation did not fall through to the free key: ok=%v", ok)
	}
	second.Finish(domain.Outcome{Kind: domain.OutcomeRateLimited, RetryAfter: time.Minute})
	if _, ok := scheduler.TryAcquire("echo", "model"); ok {
		t.Fatal("a fully cooled pool must miss, not park")
	}
	clock = clock.Add(time.Minute)
	if _, ok := scheduler.TryAcquire("echo", "model"); !ok {
		t.Fatal("lapsed cooldown did not free the pool")
	}
}

// An auth verdict never resolves on a timer, so the key that produced one sits
// out thirty seconds, not five minutes: a dead single-key pool used to make
// every request queue the whole cooldown before failing again (measured:
// nineteen five-minute waits on one "Invalid token"). Thirty seconds keeps the
// retry pace gentle while the refusal stays fast.
func TestAnAuthenticationRefusalCoolsTheKeyForThirtySeconds(t *testing.T) {
	scheduler := NewScheduler(10)
	clock := time.Now()
	scheduler.now = func() time.Time { return clock }
	key := testKey(t, "echo", "Solo", "solo-secret", 0, 0)
	if err := scheduler.Configure("echo", 0, 0, []domain.Key{key}); err != nil {
		t.Fatal(err)
	}
	lease, _, err := scheduler.Acquire(context.Background(), "echo", "model")
	if err != nil {
		t.Fatal(err)
	}
	lease.Finish(domain.Outcome{Kind: domain.OutcomeAuthentication})
	if _, ok := scheduler.TryAcquire("echo", "model"); ok {
		t.Fatal("an auth-refused key was still dispatchable")
	}
	clock = clock.Add(29 * time.Second)
	if _, ok := scheduler.TryAcquire("echo", "model"); ok {
		t.Fatal("the auth cooldown lapsed early")
	}
	clock = clock.Add(2 * time.Second)
	if _, ok := scheduler.TryAcquire("echo", "model"); !ok {
		t.Fatal("the auth cooldown did not lapse after thirty seconds")
	}
}

// A dead key announces itself once: the listener fires at the threshold and
// not again until the streak breaks and rebuilds.
func TestADeadKeyAnnouncesItselfOncePerStreak(t *testing.T) {
	scheduler := NewScheduler(10)
	clock := time.Now()
	scheduler.now = func() time.Time { return clock }
	key := testKey(t, "echo", "Solo", "solo-secret", 0, 0)
	if err := scheduler.Configure("echo", 0, 0, []domain.Key{key}); err != nil {
		t.Fatal(err)
	}
	announced := make(chan string, 4)
	scheduler.OnDeadKey(func(_, label string) { announced <- label })
	// Each refusal cools the key for the auth cooldown, so the clock advances
	// between attempts: the test measures the streak, not the wait.
	refuse := func(outcome domain.OutcomeKind) {
		t.Helper()
		lease, _, err := scheduler.Acquire(context.Background(), "echo", "model")
		if err != nil {
			t.Fatal(err)
		}
		lease.Finish(domain.Outcome{Kind: outcome})
		clock = clock.Add(authCooldown + time.Second)
	}
	// Two refusals are noise; the third is the verdict, the fourth is the
	// same streak continuing.
	refuse(domain.OutcomeAuthentication)
	refuse(domain.OutcomeAuthentication)
	refuse(domain.OutcomeAuthentication)
	refuse(domain.OutcomeAuthentication)
	select {
	case label := <-announced:
		if label != "Solo" {
			t.Fatalf("the wrong key announced itself: %q", label)
		}
	case <-time.After(time.Second):
		t.Fatal("a dead key never announced itself")
	}
	select {
	case label := <-announced:
		t.Fatalf("the same streak announced itself twice: %q", label)
	case <-time.After(50 * time.Millisecond):
	}
	// A success breaks the streak; a rebuilt streak announces again.
	refuse(domain.OutcomeSuccess)
	refuse(domain.OutcomeAuthentication)
	refuse(domain.OutcomeAuthentication)
	refuse(domain.OutcomeAuthentication)
	select {
	case <-announced:
	case <-time.After(time.Second):
		t.Fatal("a rebuilt streak never announced itself")
	}
}

// A rotation never jumps the fair queue: with another request waiting on the
// provider, even a free key is a miss.
func TestTryAcquireMissesWhileAnotherRequestWaits(t *testing.T) {
	scheduler := NewScheduler(10)
	first := testKey(t, "echo", "First", "first-secret", 0, 0)
	second := testKey(t, "echo", "Second", "second-secret", 1, 0)
	if err := scheduler.Configure("echo", 0, 0, []domain.Key{first, second}); err != nil {
		t.Fatal(err)
	}
	// Block both keys for this model only, so they stay dispatchable for any
	// other model while a waiter parks on the blocked one.
	for range 2 {
		lease, _, err := scheduler.Acquire(context.Background(), "echo", "model-a")
		if err != nil {
			t.Fatal(err)
		}
		lease.Finish(domain.Outcome{Kind: domain.OutcomeModelUnavailable, Model: "model-a"})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		acquired, _, err := scheduler.Acquire(ctx, "echo", "model-a")
		if err != nil {
			return
		}
		acquired.Finish(domain.Outcome{Kind: domain.OutcomeSuccess})
	}()
	// The waiter registers before its first wait; without it the assertion
	// below could run before the queue exists.
	deadline := time.Now().Add(5 * time.Second)
	for {
		scheduler.mu.Lock()
		queued := len(scheduler.waiters)
		scheduler.mu.Unlock()
		if queued > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, ok := scheduler.TryAcquire("echo", "model-b"); ok {
		t.Fatal("rotation jumped a queued request")
	}
	cancel()
	<-done
}
