package application

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

func TestPoolCheckOnlyAcceptedCatalogAuthenticates(t *testing.T) {
	for _, status := range []int{0, 200, 204, 401, 403, 404, 429, 503} {
		t.Run(outcomeStatusName(status), func(t *testing.T) {
			scheduler := NewScheduler(10)
			manager, err := NewManager(scheduler, &memoryRepository{}, map[string]Rate{"fixture": {}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			key, err := manager.Add(context.Background(), domain.Params{ProviderID: "fixture", Label: "key", Secret: "synthetic"})
			if err != nil {
				t.Fatal(err)
			}
			for range 3 {
				lease := scheduler.probeLease("fixture", key.ID)
				lease.Finish(domain.Outcome{Kind: domain.OutcomeAuthentication})
			}
			report, err := manager.CheckPool(context.Background(), "fixture", fakeProber{statuses: map[string]int{"key": status}})
			if err != nil {
				t.Fatal(err)
			}
			state := manager.List("fixture")[0]
			wantStreak := 3
			wantOutcome := "authentication"
			if status >= 200 && status < 300 {
				wantStreak = 0
				wantOutcome = "success"
			} else if status == 401 || status == 403 {
				wantStreak++
			}
			if state.AuthStreak != wantStreak || state.LastOutcome != wantOutcome {
				t.Fatalf("status=%d falsely changed credential verdict: %+v report=%+v", status, state, report)
			}
		})
	}
}

func outcomeStatusName(status int) string {
	switch status {
	case 0:
		return "unreachable"
	case 200:
		return "OK"
	case 204:
		return "no-content"
	case 401:
		return "unauthorized"
	case 403:
		return "forbidden"
	case 404:
		return "wrong-catalog"
	case 429:
		return "rate-limited"
	default:
		return "service-error"
	}
}

type observedPoolContext struct {
	context.Context
	checked chan struct{}
}

func (ctx observedPoolContext) Err() error {
	err := ctx.Context.Err()
	select {
	case ctx.checked <- struct{}{}:
	default:
	}
	return err
}

func TestPoolCancellationDuringSetupIsNotSuccessful(t *testing.T) {
	for name, count := range map[string]int{"empty_pool": 0, "nil_prober": 1} {
		t.Run(name, func(t *testing.T) {
			manager, _ := poolFixture(t, count)
			baseCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := observedPoolContext{Context: baseCtx, checked: make(chan struct{}, 1)}
			manager.opMu.Lock()
			done := make(chan error, 1)
			go func() {
				_, err := manager.CheckPool(ctx, "fixture", nil)
				done <- err
			}()
			select {
			case <-ctx.checked:
			case <-time.After(time.Second):
				manager.opMu.Unlock()
				t.Fatal("check did not enter setup")
			}
			cancel()
			manager.opMu.Unlock()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation became success: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("check did not finish setup")
			}
		})
	}
}

type canceledPoolProber struct{ calls atomic.Int32 }

func (p *canceledPoolProber) ProbeKey(context.Context, string, domain.Key) int {
	p.calls.Add(1)
	return 401
}

func TestAlreadyCanceledPoolCheckDoesNotDispatch(t *testing.T) {
	scheduler := NewScheduler(10)
	manager, err := NewManager(scheduler, &memoryRepository{}, map[string]Rate{"fixture": {}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Add(context.Background(), domain.Params{ProviderID: "fixture", Label: "key", Secret: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	prober := &canceledPoolProber{}
	_, err = manager.CheckPool(ctx, "fixture", prober)
	if !errors.Is(err, context.Canceled) || prober.calls.Load() != 0 || manager.List("fixture")[0].AuthStreak != 0 {
		t.Fatalf("canceled check ran: err=%v calls=%d state=%+v", err, prober.calls.Load(), manager.List("fixture"))
	}
}
