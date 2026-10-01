package application

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

type fakeProber struct {
	statuses map[string]int
	log      func(label string, status int)
}

func (prober fakeProber) ProbeKey(_ context.Context, _ string, key domain.Key) int {
	status := prober.statuses[key.Label]
	if prober.log != nil {
		prober.log(key.Label, status)
	}
	return status
}

// A pool check files every answered verdict: a rejected key earns its
// authentication streak — the dead-key badge and notification ride the same
// counter — and an accepted one resets it. A probe with no answer at all says
// the provider was unreachable, not that the keys are bad.
func TestCheckPoolFilesVerdictsPerKey(t *testing.T) {
	repository := &memoryRepository{}
	scheduler := NewScheduler(10)
	clock := time.Unix(0, 0)
	scheduler.now = func() time.Time { return clock }
	keys := []domain.Key{
		testKey(t, "echo", "Alive", "alive-secret", 0, 0),
		testKey(t, "echo", "Dead", "dead-secret", 1, 0),
		testKey(t, "echo", "Silent", "silent-secret", 2, 0),
	}
	manager, err := NewManager(scheduler, repository, map[string]Rate{"echo": {Limit: 120}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.persistAndApply(context.Background(), keys); err != nil {
		t.Fatal(err)
	}
	prober := fakeProber{
		statuses: map[string]int{"Alive": 200, "Dead": 401, "Silent": 0},
		log:      func(label string, status int) { t.Logf("probe %s -> %d", label, status) },
	}
	report, err := manager.CheckPool(context.Background(), "echo", prober)
	if err != nil {
		t.Fatal(err)
	}
	if report.Checked != 3 || report.Rejected != 1 || !report.Reachable {
		t.Fatalf("unexpected report: %+v", report)
	}
	streaks := map[string]int{}
	outcomes := map[string]string{}
	for _, key := range scheduler.Snapshot("echo") {
		streaks[key.Label] = key.AuthStreak
		outcomes[key.Label] = key.LastOutcome
	}
	if streaks["Dead"] != 1 || outcomes["Dead"] != "authentication" {
		t.Fatalf("the rejected key did not earn its streak: %+v", streaks)
	}
	if streaks["Alive"] != 0 || outcomes["Alive"] != "success" {
		t.Fatalf("the accepted key did not reset: %+v", streaks)
	}
	if streaks["Silent"] != 0 || outcomes["Silent"] != "" {
		t.Fatalf("an unanswered probe was filed as a verdict: streaks=%+v outcomes=%+v", streaks, outcomes)
	}
	// An unknown provider is an error, not an empty report.
	if _, err := manager.CheckPool(context.Background(), "missing", prober); err == nil {
		t.Fatal("checking a missing provider succeeded")
	}
}

// A pool check runs while other stdio workers edit providers. EnsureProvider and
// RemoveProvider write the provider table under mu, and the check used to read
// it with no lock at all: an unlocked read of a concurrently written map is a
// runtime fatal the protocol server's recover() cannot catch, so one keys.check
// racing a provider edit took the whole sidecar down. The -race detector makes
// the interleaving loud even on hardware where the unlocked read would not
// happen to crash; without the detector, the fixed lock discipline is what the
// eye can verify in the source.
func TestCheckPoolReadsTheProviderTableUnderTheSameLockAsItsWriters(t *testing.T) {
	repository := &memoryRepository{}
	manager, err := NewManager(NewScheduler(10), repository, map[string]Rate{"echo": {}, "spare": {}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.persistAndApply(context.Background(), []domain.Key{testKey(t, "echo", "One", "secret", 0, 0)}); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		group.Add(1)
		go func() {
			defer group.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = manager.CheckPool(context.Background(), "echo", fakeProber{statuses: map[string]int{"One": 200}})
			}
		}()
	}
	for index := range 300 {
		if index%2 == 0 {
			if err := manager.EnsureProvider("spare", 60, time.Minute); err != nil {
				t.Fatalf("ensure: %v", err)
			}
		} else {
			// The add/remove pair walks both write paths: map insert and delete.
			if err := manager.RemoveProvider("spare"); err != nil {
				t.Fatalf("remove: %v", err)
			}
		}
	}
	close(stop)
	group.Wait()
}
