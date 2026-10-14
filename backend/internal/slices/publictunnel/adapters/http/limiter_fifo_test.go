package tunnelhttp

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIPWaiterIdentityRemovesOnlyItsOwnTicket(t *testing.T) {
	first, middle, last := &ipWaiter{}, &ipWaiter{}, &ipWaiter{}
	state := &ipLimit{waiters: []*ipWaiter{first, middle, last}}
	if first == middle || middle == last || first == last {
		t.Fatal("distinct pending requests have equal identities")
	}
	if !removeWaiter(state, middle) || len(state.waiters) != 2 || state.waiters[0] != first || state.waiters[1] != last {
		t.Fatal("cancellation removed another request")
	}
	if !removeWaiter(state, first) || len(state.waiters) != 1 || state.waiters[0] != last {
		t.Fatal("head cancellation changed the successor")
	}
}

func TestIPLimiterCannotOvertakeAParkedHead(t *testing.T) {
	limiter := newIPLimiter()
	now := time.Unix(1000, 0)
	limiter.now = func() time.Time { return now }
	release, _, err := limiter.Acquire(context.Background(), "203.0.113.1", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	parked, resume := make(chan struct{}), make(chan struct{})
	head := make(chan error, 1)
	go func() {
		release, _, err := limiter.Acquire(ctx, "203.0.113.1", 1, func() { close(parked); <-resume })
		if err == nil {
			release()
		}
		head <- err
	}()
	select {
	case <-parked:
	case <-ctx.Done():
		t.Fatal("head did not queue")
	}
	limiter.mu.Lock()
	now = now.Add(time.Minute)
	limiter.mu.Unlock()
	laterCtx, cancelLater := context.WithCancel(ctx)
	laterQueued, later := make(chan struct{}), make(chan error, 1)
	go func() {
		release, _, err := limiter.Acquire(laterCtx, "203.0.113.1", 1, func() { close(laterQueued) })
		if err == nil {
			release()
		}
		later <- err
	}()
	select {
	case <-laterQueued:
	case err := <-later:
		close(resume)
		cancel()
		<-head
		t.Fatalf("later request overtook parked head: %v", err)
	case <-ctx.Done():
		close(resume)
		t.Fatal("later request neither queued nor completed")
	}
	cancelLater()
	if err := <-later; !errors.Is(err, context.Canceled) {
		t.Fatalf("later cancellation: %v", err)
	}
	close(resume)
	if err := <-head; err != nil {
		t.Fatalf("head did not receive available interval: %v", err)
	}
	limiter.mu.Lock()
	state := limiter.next["203.0.113.1"]
	if limiter.active != 1 || limiter.pending != 1 || len(state.waiters) != 0 {
		t.Fatalf("queued capacity leaked: active=%d pending=%d waiters=%d", limiter.active, limiter.pending, len(state.waiters))
	}
	limiter.mu.Unlock()
}
