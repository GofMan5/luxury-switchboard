package application

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

type memoryRepository struct {
	keys     []domain.Key
	saveFail bool
}

func (repository *memoryRepository) Load(context.Context) ([]domain.Key, error) {
	return slices.Clone(repository.keys), nil
}

func (repository *memoryRepository) Save(_ context.Context, keys []domain.Key) error {
	if repository.saveFail {
		return errors.New("injected save failure")
	}
	repository.keys = slices.Clone(keys)
	return nil
}

func TestManagerPersistsAddedKeyAndPinnedRPM(t *testing.T) {
	repository := &memoryRepository{}
	scheduler := NewScheduler(10)
	builtin := testKey(t, "echo", "Environment key", "environment-secret", 0, 30)
	builtin.Pinned = true
	manager, err := NewManager(scheduler, repository, map[string]Rate{"echo": {Limit: 120}}, []domain.Key{builtin})
	if err != nil {
		t.Fatal(err)
	}
	added, err := manager.Add(context.Background(), domain.Params{
		ProviderID: "echo", Label: "Pro", Secret: "pro-secret", RPM: 90,
	})
	if err != nil {
		t.Fatal(err)
	}
	if added.Label != "Pro" || added.Pinned || len(repository.keys) != 2 {
		t.Fatalf("key was not persisted safely: %+v", added)
	}
	updated, err := manager.Update(context.Background(), "echo", builtin.ID, Update{
		Label: "Environment key", RPM: 45,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RPM != 45 || len(repository.keys) != 2 || !repository.keys[0].Pinned {
		t.Fatalf("pinned RPM was not persisted: %+v", updated)
	}
	if repository.keys[0].Credential.Reveal() == "environment-secret" {
		t.Fatal("environment credential was copied into persistent settings")
	}

	restarted, err := NewManager(NewScheduler(10), repository, map[string]Rate{"echo": {Limit: 120}}, []domain.Key{builtin})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	keys := restarted.List("echo")
	if len(keys) != 2 || keys[0].RPM != 45 || !keys[0].Pinned {
		t.Fatalf("restart did not restore keys: %+v", keys)
	}
}

func TestManagerDoesNotCommitFailedPersistence(t *testing.T) {
	repository := &memoryRepository{saveFail: true}
	manager, err := NewManager(NewScheduler(10), repository, map[string]Rate{"echo": {}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Add(context.Background(), domain.Params{
		ProviderID: "echo", Label: "Will fail", Secret: "secret", RPM: 10,
	})
	if err == nil {
		t.Fatal("injected persistence failure was ignored")
	}
	if keys := manager.List("echo"); len(keys) != 0 {
		t.Fatalf("failed save mutated runtime: %+v", keys)
	}
}

func TestManagerRejectsDuplicatePersistedKeysWithoutMutatingRuntime(t *testing.T) {
	key := testKey(t, "echo", "Duplicate", "same-secret", 1, 10)
	repository := &memoryRepository{keys: []domain.Key{key, key}}
	manager, err := NewManager(NewScheduler(10), repository, map[string]Rate{"echo": {}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Load(context.Background()); !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("duplicate persisted keys were accepted: %v", err)
	}
	if keys := manager.List("echo"); len(keys) != 0 {
		t.Fatalf("failed load mutated the scheduler: %+v", keys)
	}
}
