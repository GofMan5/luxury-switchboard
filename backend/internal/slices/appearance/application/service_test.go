package application

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/appearance/domain"
)

type memory struct {
	mu      sync.Mutex
	saved   domain.Appearance
	found   bool
	loadErr error
	fail    bool
	saves   int
}

func (store *memory) Load(context.Context) (domain.Appearance, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	saved := store.saved
	saved.Tokens = maps.Clone(saved.Tokens)
	return saved, store.found, store.loadErr
}

func (store *memory) Save(_ context.Context, appearance domain.Appearance) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.fail {
		return errors.New("disk is full")
	}
	store.saves++
	store.saved, store.found = appearance, true
	store.saved.Tokens = maps.Clone(appearance.Tokens)
	return nil
}

func TestAMissingFileServesDefaults(t *testing.T) {
	service, _ := NewService(&memory{})
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshot := service.Snapshot(); snapshot.Theme != domain.DefaultTheme || snapshot.Tokens == nil {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

func TestUpdatePersistsTheNormalizedAppearance(t *testing.T) {
	store := &memory{}
	service, _ := NewService(store)
	_ = service.Load(context.Background())
	updated, err := service.Update(context.Background(), domain.Appearance{Theme: "paper", Accent: "#AABBCC", Tokens: map[string]string{"--bg": "#fff"}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Accent != "#aabbcc" || updated.Density != domain.DefaultDensity || store.saved.Theme != "paper" {
		t.Fatalf("unexpected result %+v, saved %+v", updated, store.saved)
	}
	updated.Tokens["--bg"] = "#000"
	if service.Snapshot().Tokens["--bg"] != "#fff" {
		t.Fatal("the returned appearance shares state with the service")
	}
	snapshot := service.Snapshot()
	snapshot.Tokens["--bg"] = "#000"
	if service.Snapshot().Tokens["--bg"] != "#fff" {
		t.Fatal("a snapshot shares state with the service")
	}

	reloaded, _ := NewService(store)
	if err := reloaded.Load(context.Background()); err != nil || reloaded.Snapshot().Theme != "paper" {
		t.Fatalf("the update did not survive a restart: %+v %v", reloaded.Snapshot(), err)
	}
}

func TestInvalidUpdateIsRefusedBeforeSaving(t *testing.T) {
	store := &memory{}
	service, _ := NewService(store)
	_ = service.Load(context.Background())
	_, err := service.Update(context.Background(), domain.Appearance{Density: "cozy"})
	if !domain.IsValidation(err) || store.saves != 0 {
		t.Fatalf("invalid update: %v, saves %d", err, store.saves)
	}
}

func TestAFailedSaveLeavesTheCurrentAppearance(t *testing.T) {
	store := &memory{}
	service, _ := NewService(store)
	_ = service.Load(context.Background())
	if _, err := service.Update(context.Background(), domain.Appearance{Theme: "paper"}); err != nil {
		t.Fatal(err)
	}
	store.fail = true
	if _, err := service.Update(context.Background(), domain.Appearance{Theme: "ink"}); err == nil || domain.IsValidation(err) || errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("unexpected save failure: %v", err)
	}
	if service.Snapshot().Theme != "paper" {
		t.Fatalf("memory moved ahead of the file: %+v", service.Snapshot())
	}
}

func TestAnUnreadableFileServesDefaultsAndRefusesWrites(t *testing.T) {
	for name, store := range map[string]*memory{
		"unreadable": {loadErr: errors.New("secure storage unavailable")},
		"invalid":    {found: true, saved: domain.Appearance{Density: "cozy"}},
	} {
		service, _ := NewService(store)
		if err := service.Load(context.Background()); err == nil {
			t.Fatalf("%s: load succeeded", name)
		}
		if service.Snapshot().Theme != domain.DefaultTheme {
			t.Fatalf("%s: unexpected snapshot", name)
		}
		if _, err := service.Update(context.Background(), domain.Defaults()); !errors.Is(err, ErrStoreUnavailable) || store.saves != 0 {
			t.Fatalf("%s: a write replaced an unreadable file: %v", name, err)
		}
	}
}

func TestConcurrentUpdatesAndReadsStayConsistent(t *testing.T) {
	store := &memory{}
	service, _ := NewService(store)
	_ = service.Load(context.Background())
	var group sync.WaitGroup
	for index := range 16 {
		group.Add(2)
		go func() {
			defer group.Done()
			theme := fmt.Sprintf("theme-%d", index)
			if _, err := service.Update(context.Background(), domain.Appearance{Theme: theme, Tokens: map[string]string{"--n": theme}}); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer group.Done()
			snapshot := service.Snapshot()
			if snapshot.Theme != domain.DefaultTheme && snapshot.Tokens["--n"] != snapshot.Theme {
				t.Errorf("torn snapshot: %+v", snapshot)
			}
		}()
	}
	group.Wait()
	if final := service.Snapshot(); final.Theme != store.saved.Theme || store.saves != 16 {
		t.Fatalf("memory %q and file %q disagree after %d saves", final.Theme, store.saved.Theme, store.saves)
	}
}
