package application_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/domain"
)

// Delaying the return after a durable write models a store completion arriving
// after a newer decision. Access stays synchronized so this tests ordering,
// not a Go data race in the synthetic store.
type delayedProfileStore struct {
	mu       sync.Mutex
	profiles map[string]domain.Profile
	writes   int
	written  chan struct{}
	release  chan struct{}
}

func (store *delayedProfileStore) Record(domain.Event) bool { return true }
func (store *delayedProfileStore) Recent(context.Context, string, int) ([]domain.Event, error) {
	return nil, nil
}
func (store *delayedProfileStore) Close(context.Context) error { return nil }
func (store *delayedProfileStore) Profiles(context.Context) ([]domain.Profile, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]domain.Profile, 0, len(store.profiles))
	for _, profile := range store.profiles {
		result = append(result, profile)
	}
	return result, nil
}
func (store *delayedProfileStore) SaveProfile(_ context.Context, profile domain.Profile) error {
	store.write(profile)
	return nil
}
func (store *delayedProfileStore) DeleteProfile(_ context.Context, ip string) error {
	store.write(domain.Profile{IP: ip})
	return nil
}
func (store *delayedProfileStore) write(profile domain.Profile) {
	store.mu.Lock()
	if profile.Empty() {
		delete(store.profiles, profile.IP)
	} else {
		store.profiles[profile.IP] = profile
	}
	store.writes++
	first := store.writes == 1
	store.mu.Unlock()
	if first {
		close(store.written)
		<-store.release
	}
}

func waitResult(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("profile operation did not finish after storage was released")
	}
}

func TestConcurrentProfileCompletionsKeepLiveAndRestartedDecisionsInTheSameOrder(t *testing.T) {
	const ip = "203.0.113.7"
	for _, test := range []struct {
		name           string
		earlier, later domain.Profile
	}{
		{"save then save", domain.Profile{IP: ip, Note: "observed"}, domain.Profile{IP: ip, Banned: true, Note: "latest"}},
		{"delete then save", domain.Profile{IP: ip}, domain.Profile{IP: ip, Banned: true, Note: "latest"}},
		{"save then delete", domain.Profile{IP: ip, Banned: true, Note: "earlier"}, domain.Profile{IP: ip}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := &delayedProfileStore{
				profiles: map[string]domain.Profile{ip: {IP: ip, Banned: true, Note: "initial"}},
				written:  make(chan struct{}), release: make(chan struct{}),
			}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(store.release) }) }
			defer release()
			service := application.NewService(store)
			if err := service.LoadProfiles(ctx); err != nil {
				t.Fatal(err)
			}
			earlier := make(chan error, 1)
			go func() { earlier <- service.SetProfile(ctx, test.earlier) }()
			select {
			case <-store.written:
			case <-time.After(time.Second):
				t.Fatal("earlier decision never reached storage")
			}
			// Disk I/O must not hold the traffic/read mutex. Reads see the last
			// committed live decision while the next one is still being saved.
			read := make(chan bool, 1)
			go func() {
				clients := service.List()
				read <- service.Banned(ip) && len(clients) == 1 && clients[0].Note == "initial"
			}()
			select {
			case responsive := <-read:
				if !responsive {
					t.Fatal("a decision was applied before its save completed")
				}
			case <-time.After(time.Second):
				t.Fatal("profile persistence blocked live client reads")
			}
			later := make(chan error, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				later <- service.SetProfile(ctx, test.later)
			}()
			<-started
			// Broken code completes the later operation first. Serialized code
			// waits for the earlier operation; the bounded pause exercises both.
			laterFinished := false
			select {
			case err := <-later:
				if err != nil {
					t.Fatal(err)
				}
				laterFinished = true
			case <-time.After(100 * time.Millisecond):
			}
			release()
			waitResult(t, earlier)
			if !laterFinished {
				waitResult(t, later)
			}
			if service.Banned(ip) != test.later.Banned {
				t.Fatal("earlier completion replaced the later live ban decision")
			}
			restarted := application.NewService(store)
			if err := restarted.LoadProfiles(ctx); err != nil {
				t.Fatal(err)
			}
			live, durable := service.List(), restarted.List()
			if len(live) != len(durable) || len(live) != boolCount(!test.later.Empty()) {
				t.Fatalf("live and durable profile presence disagree: %v vs %v", live, durable)
			}
			if len(live) > 0 && (live[0].Banned != durable[0].Banned || live[0].Note != durable[0].Note || live[0].Note != test.later.Note) {
				t.Fatalf("live and durable profile decisions disagree: %v vs %v", live, durable)
			}
		})
	}
}

type delayedProfileRead struct {
	*delayedProfileStore
	once     sync.Once
	snapshot chan struct{}
	resume   chan struct{}
}

func (store *delayedProfileRead) Profiles(ctx context.Context) ([]domain.Profile, error) {
	profiles, err := store.delayedProfileStore.Profiles(ctx)
	store.once.Do(func() {
		close(store.snapshot)
		<-store.resume
	})
	return profiles, err
}

func TestAnEarlierProfileRestoreCannotUndoALaterDeletion(t *testing.T) {
	const ip = "203.0.113.7"
	ctx := context.Background()
	store := &delayedProfileRead{
		delayedProfileStore: &delayedProfileStore{
			profiles: map[string]domain.Profile{ip: {IP: ip, Banned: true}},
			writes:   1, // Only the read is gated in this scenario.
		},
		snapshot: make(chan struct{}), resume: make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(store.resume) }) }
	defer release()
	service := application.NewService(store)
	loaded := make(chan error, 1)
	go func() { loaded <- service.LoadProfiles(ctx) }()
	select {
	case <-store.snapshot:
	case <-time.After(time.Second):
		t.Fatal("restore did not read its snapshot")
	}
	deleted := make(chan error, 1)
	go func() { deleted <- service.SetProfile(ctx, domain.Profile{IP: ip}) }()
	deleteFinished := false
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatal(err)
		}
		deleteFinished = true
	case <-time.After(100 * time.Millisecond):
	}
	release()
	waitResult(t, loaded)
	if !deleteFinished {
		waitResult(t, deleted)
	}
	restarted := application.NewService(store)
	if err := restarted.LoadProfiles(ctx); err != nil {
		t.Fatal(err)
	}
	if service.Banned(ip) || len(service.List()) != 0 || len(restarted.List()) != 0 {
		t.Fatal("earlier restore resurrected a deleted owner decision")
	}
}

func TestProfileChangedListenersCanReenterProfileReads(t *testing.T) {
	ctx := context.Background()
	store := &delayedProfileStore{profiles: make(map[string]domain.Profile), writes: 1}
	service := application.NewService(store)
	observed := make(chan error, 1)
	service.OnChanged(func() { observed <- service.LoadProfiles(ctx) })
	done := make(chan error, 1)
	go func() { done <- service.SetProfile(ctx, domain.Profile{IP: "203.0.113.7", Banned: true}) }()
	waitResult(t, done)
	waitResult(t, observed)
}

func boolCount(present bool) int {
	if present {
		return 1
	}
	return 0
}
