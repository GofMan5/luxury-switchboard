package application

import (
	"context"
	"errors"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/settings/domain"
	"testing"
)

type memoryRepository struct {
	settings domain.Settings
	found    bool
	fail     bool
}

func (repository *memoryRepository) Load(context.Context) (domain.Settings, bool, error) {
	return repository.settings, repository.found, nil
}
func (repository *memoryRepository) Save(_ context.Context, settings domain.Settings) error {
	if repository.fail {
		return errors.New("injected")
	}
	repository.settings = settings
	repository.found = true
	return nil
}

// An unreadable settings file must block the first save rather than let it
// atomically replace the file: the service runs on defaults after a failed
// load, and saving those would erase the user's whole configuration the
// moment the keyring unlocks mid-session.
func TestFailedLoadBlocksTheFirstSave(t *testing.T) {
	service, _ := NewService(&lockedSettingsRepository{err: errors.New("secure storage is locked")})
	if err := service.Load(context.Background()); err == nil {
		t.Fatal("the injected load failure was not reported")
	}
	if _, err := service.Update(context.Background(), domain.Defaults()); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("a save over an unreadable store was accepted: %v", err)
	}
}

type lockedSettingsRepository struct {
	err error
}

func (repository *lockedSettingsRepository) Load(context.Context) (domain.Settings, bool, error) {
	return domain.Settings{}, false, repository.err
}

func (repository *lockedSettingsRepository) Save(context.Context, domain.Settings) error {
	return repository.err
}

func TestServicePersistsBeforePublishing(t *testing.T) {
	repository := &memoryRepository{}
	service, _ := NewService(repository)
	next := domain.Defaults()
	next.ListenerPort = 9000
	result, err := service.Update(context.Background(), next)
	if err != nil || !result.RestartRequired || service.Snapshot().ListenerPort != 9000 {
		t.Fatalf("unexpected update: %+v %v", result, err)
	}
	result, err = service.Update(context.Background(), next)
	if err != nil || !result.RestartRequired {
		t.Fatalf("restart requirement was cleared before restart: %+v %v", result, err)
	}
	repository.fail = true
	failed := next
	failed.ListenerPort = 9001
	if _, err := service.Update(context.Background(), failed); err == nil {
		t.Fatal("save failure was ignored")
	}
	if service.Snapshot().ListenerPort != 9000 {
		t.Fatal("failed save mutated settings")
	}
}

// The regression this guards: a settings file from a build that predates a field
// must still load. Rejecting it would silently reset the operator's whole
// configuration to defaults on upgrade.
func TestOlderSettingsFileLoadsInsteadOfBeingDiscarded(t *testing.T) {
	stored := domain.Defaults()
	stored.ListenerPort = 9123
	stored.MaxQueued = 555
	stored.GuardrailMode = ""
	stored.GuardrailFindings = 0

	service, _ := NewService(&memoryRepository{settings: stored, found: true})
	if err := service.Load(context.Background()); err != nil {
		t.Fatalf("an older settings file was rejected: %v", err)
	}
	loaded := service.Snapshot()
	if loaded.ListenerPort != 9123 || loaded.MaxQueued != 555 {
		t.Fatalf("loading lost configured values: %+v", loaded)
	}
	if loaded.GuardrailMode != domain.DefaultGuardrailMode {
		t.Fatalf("the missing mode did not become the default: %+v", loaded)
	}
}

func TestCorruptSettingsAreStillRejected(t *testing.T) {
	stored := domain.Defaults()
	stored.ListenerPort = 0
	service, _ := NewService(&memoryRepository{settings: stored, found: true})
	if err := service.Load(context.Background()); err == nil {
		t.Fatal("an out-of-range setting was accepted")
	}
	if service.Snapshot() != domain.Defaults() {
		t.Fatal("a rejected file replaced the running defaults")
	}
}

// The mode takes effect on the next request, not the next launch, so the listener
// has to fire and the result must not ask for a restart.
func TestGuardrailModeIsPublishedWithoutRequiringARestart(t *testing.T) {
	service, _ := NewService(&memoryRepository{})
	var applied []string
	service.OnApplied(func(settings domain.Settings) { applied = append(applied, settings.GuardrailMode) })
	service.OnApplied(nil) // must be ignored rather than panic on the next update

	next := domain.Defaults()
	next.GuardrailMode = "block"
	result, err := service.Update(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	if result.RestartRequired {
		t.Fatal("a mode change asked for a restart")
	}
	if len(applied) != 1 || applied[0] != "block" {
		t.Fatalf("the mode change was not published: %v", applied)
	}

	// A listener also has to see changes that do need a restart, so a live setting
	// updated alongside them is not lost until the process comes back.
	next.ListenerPort = 9000
	result, err = service.Update(context.Background(), next)
	if err != nil || !result.RestartRequired {
		t.Fatalf("a port change did not require a restart: %+v %v", result, err)
	}
	if len(applied) != 2 {
		t.Fatalf("the second update was not published: %v", applied)
	}
}

func TestInvalidGuardrailModeIsRejectedWithoutChangingAnything(t *testing.T) {
	service, _ := NewService(&memoryRepository{})
	var calls int
	service.OnApplied(func(domain.Settings) { calls++ })
	next := domain.Defaults()
	next.GuardrailMode = "blocking"
	if _, err := service.Update(context.Background(), next); err == nil {
		t.Fatal("a typo in the mode was accepted")
	}
	if calls != 0 {
		t.Fatal("a rejected update was published to listeners")
	}
	if service.Snapshot().GuardrailMode != domain.DefaultGuardrailMode {
		t.Fatalf("a rejected update changed the active mode: %+v", service.Snapshot())
	}
}
