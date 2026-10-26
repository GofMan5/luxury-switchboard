package application

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

type controlledPoolProber struct {
	entered  chan domain.Key
	release  chan int
	active   atomic.Int32
	peak     atomic.Int32
	returned atomic.Int32
}

func (p *controlledPoolProber) ProbeKey(ctx context.Context, _ string, key domain.Key) int {
	active := p.active.Add(1)
	defer p.active.Add(-1)
	for {
		previous := p.peak.Load()
		if active <= previous || p.peak.CompareAndSwap(previous, active) {
			break
		}
	}
	p.entered <- key
	var status int
	select {
	case status = <-p.release:
	case <-ctx.Done():
	}
	p.returned.Add(1)
	return status
}
func poolFixture(t *testing.T, count int) (*Manager, *Scheduler) {
	t.Helper()
	scheduler := NewScheduler(10)
	manager, err := NewManager(scheduler, &memoryRepository{}, map[string]Rate{"fixture": {}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := range count {
		_, err := manager.Add(context.Background(), domain.Params{ProviderID: "fixture", Label: fmt.Sprintf("key%d", index), Secret: fmt.Sprintf("synthetic-%d", index)})
		if err != nil {
			t.Fatal(err)
		}
	}
	return manager, scheduler
}
func awaitEntered(t *testing.T, ch <-chan domain.Key) domain.Key {
	t.Helper()
	select {
	case key := <-ch:
		return key
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
		return domain.Key{}
	}
}

func TestCheckPoolWorkerBoundAndCancelSkipsUndispatchedKeys(t *testing.T) {
	manager, _ := poolFixture(t, 20)
	prober := &controlledPoolProber{entered: make(chan domain.Key, 20), release: make(chan int)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	var report PoolReport
	go func() { var err error; report, err = manager.CheckPool(ctx, "fixture", prober); done <- err }()
	for range 3 {
		awaitEntered(t, prober.entered)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("check did not cancel")
	}
	if prober.peak.Load() != 3 || report.Total != 20 || report.Checked != 3 || report.Skipped != 17 || report.Inconclusive != 3 || report.Answered != 0 {
		t.Fatalf("bounded/canceled counts wrong: peak=%d report=%+v", prober.peak.Load(), report)
	}
}

func TestPoolProbeCannotFileOldVerdictAfterProxyReplacement(t *testing.T) {
	manager, _ := poolFixture(t, 1)
	prober := &controlledPoolProber{entered: make(chan domain.Key, 1), release: make(chan int)}
	done := make(chan error, 1)
	go func() { _, err := manager.CheckPool(context.Background(), "fixture", prober); done <- err }()
	key := awaitEntered(t, prober.entered)
	proxy := "http://127.0.0.1:1"
	if _, err := manager.Update(context.Background(), "fixture", key.ID, Update{Label: key.Label, ProxyURL: &proxy}); err != nil {
		t.Fatal(err)
	}
	prober.release <- 401
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("probe stuck")
	}
	state := manager.List("fixture")[0]
	if state.AuthStreak != 0 || state.CooldownMS != 0 {
		t.Fatalf("old check penalized new egress: %+v", state)
	}
}

type blockingPoolProber struct {
	entered  chan struct{}
	release  chan struct{}
	returned chan struct{}
}

func (p *blockingPoolProber) ProbeKey(context.Context, string, domain.Key) int {
	p.entered <- struct{}{}
	<-p.release
	close(p.returned)
	return 200
}

func TestPoolVerdictReturnedAfterCancellationIsInconclusive(t *testing.T) {
	for _, status := range []int{200, 401} {
		t.Run(outcomeStatusName(status), func(t *testing.T) {
			manager, _ := poolFixture(t, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			prober := fakeProber{
				statuses: map[string]int{"key0": status},
				log:      func(string, int) { cancel() },
			}
			report, err := manager.CheckPool(ctx, "fixture", prober)
			state := manager.List("fixture")[0]
			if !errors.Is(err, context.Canceled) || report.Checked != 1 || report.Inconclusive != 1 || report.Answered != 0 || state.LastOutcome != "" || state.AuthStreak != 0 {
				t.Fatalf("canceled result changed health: err=%v report=%+v state=%+v", err, report, state)
			}
		})
	}
}

func TestCheckPoolCancellationDoesNotWaitForContextIgnoringProber(t *testing.T) {
	manager, _ := poolFixture(t, 1)
	prober := &blockingPoolProber{
		entered:  make(chan struct{}, 1),
		release:  make(chan struct{}),
		returned: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		report PoolReport
		err    error
	}
	done := make(chan result, 1)
	go func() {
		report, err := manager.CheckPool(ctx, "fixture", prober)
		done <- result{report: report, err: err}
	}()
	select {
	case <-prober.entered:
	case <-time.After(time.Second):
		t.Fatal("blocking probe did not start")
	}
	cancel()
	select {
	case outcome := <-done:
		if !errors.Is(outcome.err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", outcome.err)
		}
		if outcome.report.Checked != 1 || outcome.report.Inconclusive != 1 || outcome.report.Skipped != 0 {
			t.Fatalf("canceled probe was not reported honestly: %+v", outcome.report)
		}
	case <-time.After(time.Second):
		close(prober.release)
		<-prober.returned
		t.Fatal("check waited for a context-ignoring prober")
	}
	close(prober.release)
	select {
	case <-prober.returned:
	case <-time.After(time.Second):
		t.Fatal("blocking prober did not finish after release")
	}
	state := manager.List("fixture")[0]
	if state.LastOutcome != "" || state.AuthStreak != 0 {
		t.Fatalf("late prober result filed after cancellation: %+v", state)
	}
}

// slowKeysPoolProber stalls the named keys until their probe times out and
// answers every other one at once, honouring cancellation like the real one.
type slowKeysPoolProber struct{ slow map[string]bool }

func (p slowKeysPoolProber) ProbeKey(ctx context.Context, _ string, key domain.Key) int {
	if p.slow[key.Label] {
		<-ctx.Done()
		return 0
	}
	return 200
}

// A probe that times out on its own deadline says nothing about the next key:
// three blackholed keys must not retire all three workers and leave the rest
// of the pool undispatched until the whole check's deadline.
func TestTimedOutProbesDoNotRetireTheWorkers(t *testing.T) {
	manager, _ := poolFixture(t, 6)
	manager.probeTimeout = 20 * time.Millisecond
	prober := slowKeysPoolProber{slow: map[string]bool{"key0": true, "key1": true, "key2": true}}
	done := make(chan PoolReport, 1)
	go func() {
		report, _ := manager.CheckPool(context.Background(), "fixture", prober)
		done <- report
	}()
	select {
	case report := <-done:
		if report.Checked != 6 || report.Accepted != 3 || report.Inconclusive != 3 || report.Skipped != 0 {
			t.Fatalf("timed-out probes stopped the check: %+v", report)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the check stalled after three probes timed out")
	}
}
