package application

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

func TestTimestampPruningDoesNotAllocate(t *testing.T) {
	now := time.Unix(1000, 0)
	starts := []time.Time{now.Add(-time.Minute), now.Add(-time.Second), now}
	if allocations := testing.AllocsPerRun(100, func() {
		kept := pruneStarts(starts, now, time.Minute)
		if len(kept) != 2 || !kept[0].Equal(now.Add(-time.Second)) || !kept[1].Equal(now) {
			t.Fatal("window boundary or FIFO changed")
		}
	}); allocations != 0 {
		t.Fatalf("pruning allocates %.0f times per scan", allocations)
	}
}

func TestUnlimitedHistoryStillCountsWhenRatesChangeLive(t *testing.T) {
	for _, boundary := range []string{"provider", "key"} {
		t.Run(boundary, func(t *testing.T) {
			scheduler := NewScheduler(10)
			now := time.Unix(1000, 0)
			scheduler.now = func() time.Time { return now }
			key := testKey(t, "provider", "key", "fixture", 0, 0)
			if err := scheduler.Configure("provider", 0, time.Minute, []domain.Key{key}); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				lease, ok := scheduler.TryAcquire("provider", "model")
				if !ok {
					t.Fatal("unlimited request refused")
				}
				lease.Finish(domain.Outcome{Kind: domain.OutcomeSuccess})
			}
			if scheduler.Snapshot("provider")[0].StartsInWindow != 2 {
				t.Fatal("unlimited telemetry lost")
			}
			limit := 0
			if boundary == "provider" {
				limit = 2
			} else {
				key.RPM = 2
			}
			if err := scheduler.Configure("provider", limit, time.Minute, []domain.Key{key}); err != nil {
				t.Fatal(err)
			}
			if _, ok := scheduler.TryAcquire("provider", "model"); ok {
				t.Fatal("live rate change forgot earlier starts")
			}
			now = now.Add(time.Minute)
			lease, ok := scheduler.TryAcquire("provider", "model")
			if !ok {
				t.Fatal("exact expired window did not recover")
			}
			lease.Finish(domain.Outcome{Kind: domain.OutcomeSuccess})
			if scheduler.Snapshot("provider")[0].StartsInWindow != 1 {
				t.Fatal("expired history retained")
			}
		})
	}
}

func TestSchedulerFIFOAndCancellationSurvivePruningAndLiveRateChange(t *testing.T) {
	scheduler := NewScheduler(10)
	key := testKey(t, "provider", "key", "fixture", 0, 0)
	if err := scheduler.Configure("provider", 1, time.Minute, []domain.Key{key}); err != nil {
		t.Fatal(err)
	}
	lease, _, err := scheduler.Acquire(context.Background(), "provider", "model")
	if err != nil {
		t.Fatal(err)
	}
	lease.Finish(domain.Outcome{Kind: domain.OutcomeSuccess})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		index int
		err   error
	}
	results := make(chan result, 3)
	middle, cancelMiddle := context.WithCancel(ctx)
	defer cancelMiddle()
	for index := range 3 {
		queued := make(chan struct{})
		requestCtx := ctx
		if index == 1 {
			requestCtx = middle
		}
		go func() {
			lease, _, err := scheduler.AcquireWithQueue(requestCtx, "provider", "model", func() { close(queued) })
			if err == nil {
				lease.Finish(domain.Outcome{Kind: domain.OutcomeSuccess})
			}
			results <- result{index, err}
		}()
		select {
		case <-queued:
		case <-ctx.Done():
			t.Fatal("waiter did not queue")
		}
	}
	cancelMiddle()
	cancelled := <-results
	if cancelled.index != 1 || cancelled.err != context.Canceled {
		t.Fatalf("cancelled wrong waiter: %+v", cancelled)
	}
	for _, index := range []int{0, 2} {
		if err := scheduler.Configure("provider", 2+index/2, time.Minute, []domain.Key{key}); err != nil {
			t.Fatal(err)
		}
		next := <-results
		if next.index != index || next.err != nil {
			t.Fatalf("FIFO violated: %+v", next)
		}
	}
}

func BenchmarkSchedulerUnlimitedAcquireFinish(b *testing.B) {
	for _, count := range []int{1000, 4000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				scheduler := NewScheduler(10)
				now := time.Unix(1000, 0)
				scheduler.now = func() time.Time { return now }
				key, err := domain.NewKey(domain.Params{ProviderID: "provider", Label: "key", Secret: "fixture"})
				if err != nil {
					b.Fatal(err)
				}
				if err := scheduler.Configure("provider", 0, 0, []domain.Key{key}); err != nil {
					b.Fatal(err)
				}
				for range count {
					lease, _, err := scheduler.Acquire(context.Background(), "provider", "model")
					if err != nil {
						b.Fatal(err)
					}
					lease.Finish(domain.Outcome{Kind: domain.OutcomeSuccess})
				}
			}
		})
	}
}
