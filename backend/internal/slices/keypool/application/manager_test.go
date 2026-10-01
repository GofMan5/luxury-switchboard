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
	saves    int
	saveFail bool
}

func (repository *memoryRepository) Load(context.Context) ([]domain.Key, error) {
	return slices.Clone(repository.keys), nil
}

func (repository *memoryRepository) Save(_ context.Context, keys []domain.Key) error {
	if repository.saveFail {
		return errors.New("injected save failure")
	}
	repository.saves++
	repository.keys = slices.Clone(keys)
	return nil
}

// An unreadable key store must block the first write rather than let it
// atomically replace the file: the runtime pool after a failed load holds the
// builtins and nothing else, and saving it would erase every API key the user
// entered the moment the keyring unlocks mid-session.
func TestFailedLoadBlocksTheFirstWrite(t *testing.T) {
	repository := &lockedKeyRepository{}
	scheduler := NewScheduler(10)
	builtin := testKey(t, "echo", "Environment key", "environment-secret", 0, 30)
	builtin.Pinned = true
	manager, err := NewManager(scheduler, repository, map[string]Rate{"echo": {Limit: 120}}, []domain.Key{builtin})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Load(context.Background()); err == nil {
		t.Fatal("the injected load failure was not reported")
	}
	if _, err := manager.Add(context.Background(), domain.Params{
		ProviderID: "echo", Label: "Pro", Secret: "pro-secret", RPM: 90,
	}); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("an add over an unreadable store was accepted: %v", err)
	}
	if _, err := manager.AddMany(context.Background(), Import{
		ProviderID: "echo", Entries: []ImportEntry{{Label: "A", Secret: "a-secret"}, {Label: "B", Secret: "b-secret"}},
	}); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("an import over an unreadable store was accepted: %v", err)
	}
	if _, err := manager.Update(context.Background(), "echo", builtin.ID, Update{Label: "x", RPM: 1}); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("an update over an unreadable store was accepted: %v", err)
	}
	if err := manager.Remove(context.Background(), "echo", builtin.ID); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("a remove over an unreadable store was accepted: %v", err)
	}
	if err := manager.Move(context.Background(), "echo", builtin.ID, 1); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("a move over an unreadable store was accepted: %v", err)
	}
	if repository.saved {
		t.Fatal("an unreadable store was rewritten")
	}
}

type lockedKeyRepository struct {
	saved bool
}

func (repository *lockedKeyRepository) Load(context.Context) ([]domain.Key, error) {
	return nil, errors.New("secure storage is locked")
}

func (repository *lockedKeyRepository) Save(context.Context, []domain.Key) error {
	repository.saved = true
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

func TestManagerDeduplicatesPersistedRowsAndKeepsOneCopy(t *testing.T) {
	key := testKey(t, "echo", "Duplicate", "same-secret", 1, 10)
	repository := &memoryRepository{keys: []domain.Key{key, key}}
	manager, err := NewManager(NewScheduler(10), repository, map[string]Rate{"echo": {}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A duplicate stored row is one secret filed twice: refusing the whole
	// load used to lock the pool forever behind a "secure storage unavailable"
	// misdiagnosis with no recovery from inside the app. The pool takes one
	// copy and works; the extra row is simply not a second key.
	if err := manager.Load(context.Background()); err != nil {
		t.Fatalf("a duplicated stored row locked the pool: %v", err)
	}
	if keys := manager.List("echo"); len(keys) != 1 {
		t.Fatalf("the duplicate row became a second key (or vanished): %+v", keys)
	}
}

func TestAddManyImportsSkippingDuplicatesAndRejectsInOneSave(t *testing.T) {
	repository := &memoryRepository{}
	manager, err := NewManager(NewScheduler(10), repository, map[string]Rate{"echo": {Limit: 120}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Add(context.Background(), domain.Params{
		ProviderID: "echo", Label: "Existing", Secret: "same-secret", RPM: 10,
	}); err != nil {
		t.Fatal(err)
	}
	before := repository.saves

	report, err := manager.AddMany(context.Background(), Import{
		ProviderID: "echo", RPM: 30,
		Entries: []ImportEntry{
			{Label: "One", Secret: "one-secret"},
			{Label: "Existing again", Secret: "same-secret"}, // already configured
			{Label: "One again", Secret: "one-secret"},       // repeated inside the batch
			{Label: "Broken", Secret: "   "},                 // blank once trimmed, refused
			{Label: "Two", Secret: "two-secret"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Added != 2 || len(report.Duplicate) != 2 || len(report.Rejected) != 1 {
		t.Fatalf("wrong report: %+v", report)
	}
	if report.Duplicate[0] != 1 || report.Duplicate[1] != 2 || report.Rejected[0] != 3 {
		t.Fatalf("wrong positions: %+v", report)
	}
	if repository.saves != before+1 {
		t.Fatalf("import persisted %d times, one save per batch expected", repository.saves-before)
	}
	listed := manager.List("echo")
	if len(listed) != 3 {
		t.Fatalf("imported keys missing: %+v", listed)
	}
	first, second := listed[0], listed[len(listed)-1]
	if first.RPM != 10 || second.RPM != 30 {
		t.Fatalf("each key must keep its own limit: %+v", listed)
	}
	for index := 1; index < len(listed); index++ {
		if listed[index].Priority <= listed[index-1].Priority {
			t.Fatalf("imported keys must queue behind the pool, one priority each: %+v", listed)
		}
	}
}

func TestAddManyAllDuplicatesPersistsNothingAndStillReports(t *testing.T) {
	repository := &memoryRepository{}
	manager, err := NewManager(NewScheduler(10), repository, map[string]Rate{"echo": {}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Add(context.Background(), domain.Params{
		ProviderID: "echo", Label: "Existing", Secret: "same-secret", RPM: 10,
	}); err != nil {
		t.Fatal(err)
	}
	before := repository.saves
	report, err := manager.AddMany(context.Background(), Import{
		ProviderID: "echo", Entries: []ImportEntry{{Label: "Again", Secret: "same-secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Added != 0 || len(report.Duplicate) != 1 {
		t.Fatalf("wrong report: %+v", report)
	}
	if repository.saves != before {
		t.Fatal("an import that added nothing rewrote the store")
	}
}

func TestAddManyRejectsAnOversizedBatch(t *testing.T) {
	repository := &memoryRepository{}
	manager, err := NewManager(NewScheduler(10), repository, map[string]Rate{"echo": {}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]ImportEntry, MaxImportBatch+1)
	for position := range entries {
		entries[position] = ImportEntry{Label: "K", Secret: "s"}
	}
	if _, err := manager.AddMany(context.Background(), Import{ProviderID: "echo", Entries: entries}); !errors.Is(err, ErrImportTooLarge) {
		t.Fatalf("oversized batch was accepted: %v", err)
	}
}
