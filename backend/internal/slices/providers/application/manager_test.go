package application

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

type memoryProviderRepository struct {
	state      SavedState
	saveFail   bool
	beforeSave func()
}

func (repository *memoryProviderRepository) Load(context.Context) (SavedState, error) {
	return SavedState{Providers: slices.Clone(repository.state.Providers), ActiveID: repository.state.ActiveID}, nil
}

func (repository *memoryProviderRepository) Save(_ context.Context, state SavedState) error {
	if repository.beforeSave != nil {
		repository.beforeSave()
	}
	if repository.saveFail {
		return errors.New("injected failure")
	}
	repository.state = SavedState{Providers: slices.Clone(state.Providers), ActiveID: state.ActiveID}
	return nil
}

type fakeKeyPool struct {
	rates map[string]int
	count map[string]int
}

type fakeRouteUsage map[string]bool

func (usage fakeRouteUsage) LockProvider(id string) (bool, func()) { return usage[id], func() {} }

func (keys *fakeKeyPool) Count(id string) int { return keys.count[id] }
func (keys *fakeKeyPool) EnsureProvider(id string, rpm int, _ time.Duration) error {
	if keys.rates == nil {
		keys.rates = make(map[string]int)
	}
	keys.rates[id] = rpm
	return nil
}
func (keys *fakeKeyPool) RemoveProvider(id string) error {
	if keys.count[id] > 0 {
		return ErrProviderHasKeys
	}
	delete(keys.rates, id)
	return nil
}

func TestManagerPersistsAddUpdateAndActivation(t *testing.T) {
	local := providerFixture(t, "local", true)
	catalog, err := NewCatalog([]domain.Provider{local}, "local")
	if err != nil {
		t.Fatal(err)
	}
	repository := &memoryProviderRepository{}
	keys := &fakeKeyPool{count: make(map[string]int)}
	manager, err := NewManager(catalog, repository, keys)
	if err != nil {
		t.Fatal(err)
	}
	added, err := manager.Add(context.Background(), domain.Params{
		Name: "Custom", BaseURL: "https://provider.example/v1",
		AuthMode: domain.AuthBearer, RPM: 90, CacheTTL: time.Hour, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.state.Providers) != 2 || keys.rates[added.ID] != 90 {
		t.Fatalf("provider was not persisted: %+v", repository.state)
	}
	updated, err := manager.Update(context.Background(), added.ID, domain.Params{
		Name: "Custom Pro", BaseURL: "https://provider.example/v2",
		AuthMode: domain.AuthBearer, RPM: 120, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RPM != 120 || keys.rates[added.ID] != 120 {
		t.Fatal("provider RPM did not update")
	}
	if _, err := manager.Activate(context.Background(), added.ID); err != nil {
		t.Fatal(err)
	}
	if repository.state.ActiveID != added.ID {
		t.Fatal("active provider was not persisted")
	}
	if err := manager.Delete(context.Background(), added.ID); !errors.Is(err, ErrActiveProvider) {
		t.Fatalf("active provider deletion was not blocked: %v", err)
	}
}

func TestAddWithoutAnIDMintsOne(t *testing.T) {
	local := providerFixture(t, "local", true)
	catalog, _ := NewCatalog([]domain.Provider{local}, "local")
	repository := &memoryProviderRepository{}
	keys := &fakeKeyPool{count: make(map[string]int)}
	manager, _ := NewManager(catalog, repository, keys)
	added, err := manager.Add(context.Background(), domain.Params{
		Name: "Custom", BaseURL: "https://provider.example/v1",
		AuthMode: domain.AuthBearer, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(added.ID, "provider_") || added.ID == "provider_" {
		t.Fatalf("no usable id was minted: %q", added.ID)
	}
	if _, ok := manager.Get(context.Background(), added.ID); !ok {
		t.Fatalf("minted id is not readable back: %q", added.ID)
	}
}

func TestAddHonorsARequestedID(t *testing.T) {
	local := providerFixture(t, "local", true)
	catalog, _ := NewCatalog([]domain.Provider{local}, "local")
	repository := &memoryProviderRepository{}
	keys := &fakeKeyPool{count: make(map[string]int)}
	manager, _ := NewManager(catalog, repository, keys)
	added, err := manager.Add(context.Background(), domain.Params{
		ID: "  codex  ", Name: "Codex", BaseURL: "https://chatgpt.com/backend-api/codex",
		AuthMode: domain.AuthBearer, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// whitespace around the request is trimmed, the id itself is honored
	// verbatim — the entry lands exactly where the caller asked
	if added.ID != "codex" {
		t.Fatalf("requested id was not honored verbatim: %q", added.ID)
	}
	found, ok := manager.Get(context.Background(), "codex")
	if !ok || found.ID != "codex" {
		t.Fatalf("added provider is not readable by its requested id: %+v", found)
	}
}

func TestAddRefusesARequestedIDThatIsTaken(t *testing.T) {
	local := providerFixture(t, "local", true)
	catalog, _ := NewCatalog([]domain.Provider{local}, "local")
	repository := &memoryProviderRepository{}
	keys := &fakeKeyPool{count: make(map[string]int)}
	manager, _ := NewManager(catalog, repository, keys)
	_, err := manager.Add(context.Background(), domain.Params{
		ID: "local", Name: "Impostor", BaseURL: "https://provider.example/v1",
		AuthMode: domain.AuthBearer, Enabled: true,
	})
	if !errors.Is(err, ErrProviderIDExists) {
		t.Fatalf("id collision was not refused with the sentinel: %v", err)
	}
	// the existing entry is never overwritten or merged, and nothing about
	// the refusal leaks into the catalog, the store, or the key pool
	if entries := catalog.List(); len(entries) != 1 || entries[0].Name != "local" {
		t.Fatalf("refused add mutated the catalog: %+v", entries)
	}
	if len(repository.state.Providers) != 0 {
		t.Fatalf("refused add touched persistence: %+v", repository.state)
	}
	if len(keys.rates) != 0 {
		t.Fatalf("refused add registered a rate limit: %+v", keys.rates)
	}
}

func TestRepeatedAddOfTheSameRequestedIDKeepsOneEntry(t *testing.T) {
	local := providerFixture(t, "local", true)
	catalog, _ := NewCatalog([]domain.Provider{local}, "local")
	repository := &memoryProviderRepository{}
	keys := &fakeKeyPool{count: make(map[string]int)}
	manager, _ := NewManager(catalog, repository, keys)
	params := domain.Params{
		ID: "codex", Name: "Codex", BaseURL: "https://chatgpt.com/backend-api/codex",
		AuthMode: domain.AuthBearer, Enabled: true,
	}
	if _, err := manager.Add(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	_, err := manager.Add(context.Background(), params)
	if !errors.Is(err, ErrProviderIDExists) {
		t.Fatalf("re-adding the same requested id was not refused: %v", err)
	}
	if entries := catalog.List(); len(entries) != 2 {
		t.Fatalf("a second entry appeared for one id: %+v", entries)
	}
	if len(repository.state.Providers) != 2 {
		t.Fatalf("persistence diverged from the catalog: %+v", repository.state)
	}
}

func TestGetReportsExistenceRegardlessOfEnabled(t *testing.T) {
	local := providerFixture(t, "local", true)
	off, err := domain.New(domain.Params{
		ID: "off", Name: "Off", BaseURL: "http://127.0.0.1:8799",
		AuthMode: domain.AuthPassthrough, Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := NewCatalog([]domain.Provider{local, off}, "local")
	manager, _ := NewManager(catalog, &memoryProviderRepository{}, &fakeKeyPool{count: make(map[string]int)})
	// a disabled entry is still an entry: write paths must distinguish
	// "missing" from "off", so Get answers existence, not admission
	found, ok := manager.Get(context.Background(), "off")
	if !ok || found.ID != "off" {
		t.Fatalf("a disabled entry is not visible to Get: %+v", found)
	}
	if _, admitted := catalog.Get("off"); admitted {
		t.Fatal("Get stopped gating the read paths on Enabled")
	}
	if _, ok := manager.Get(context.Background(), "missing"); ok {
		t.Fatal("an unknown id was reported as present")
	}
}

func TestManagerRollsBackRateWhenPersistenceFails(t *testing.T) {
	local := providerFixture(t, "local", true)
	catalog, _ := NewCatalog([]domain.Provider{local}, "local")
	repository := &memoryProviderRepository{saveFail: true}
	keys := &fakeKeyPool{rates: map[string]int{"local": 0}, count: make(map[string]int)}
	manager, _ := NewManager(catalog, repository, keys)
	_, err := manager.Update(context.Background(), "local", domain.Params{
		Name: "Local", BaseURL: "http://127.0.0.1:8799",
		AuthMode: domain.AuthPassthrough, RPM: 77, Enabled: true,
	})
	if err == nil || keys.rates["local"] != 0 || catalog.List()[0].RPM != 0 {
		t.Fatalf("failed persistence mutated runtime: rate=%d provider=%+v err=%v", keys.rates["local"], catalog.List()[0], err)
	}
}

func TestDeleteReservesKeyPoolBeforePersistenceAndRollsBack(t *testing.T) {
	local := providerFixture(t, "local", true)
	custom := providerFixture(t, "custom", false)
	catalog, _ := NewCatalog([]domain.Provider{local, custom}, local.ID)
	keys := &fakeKeyPool{rates: map[string]int{"local": 0, "custom": 77}, count: make(map[string]int)}
	repository := &memoryProviderRepository{saveFail: true}
	repository.beforeSave = func() {
		if _, exists := keys.rates["custom"]; exists {
			t.Fatal("provider remained open for concurrent key insertion during delete")
		}
	}
	manager, _ := NewManager(catalog, repository, keys)
	if err := manager.Delete(context.Background(), "custom"); err == nil {
		t.Fatal("injected persistence failure was ignored")
	}
	if keys.rates["custom"] != 0 {
		t.Fatalf("failed delete did not restore provider admission: %+v", keys.rates)
	}
}

func TestDeleteRejectsProviderReferencedByModelRoutes(t *testing.T) {
	local := providerFixture(t, "local", true)
	custom := providerFixture(t, "custom", false)
	catalog, _ := NewCatalog([]domain.Provider{local, custom}, local.ID)
	keys := &fakeKeyPool{rates: map[string]int{"local": 0, "custom": 77}, count: make(map[string]int)}
	manager, _ := NewManager(catalog, &memoryProviderRepository{}, keys)
	manager.SetRouteUsage(fakeRouteUsage{"custom": true})
	if err := manager.Delete(context.Background(), "custom"); !errors.Is(err, ErrProviderHasRoutes) {
		t.Fatalf("routed provider deletion was accepted: %v", err)
	}
	if keys.rates["custom"] != 77 {
		t.Fatal("blocked deletion changed key admission")
	}
}

func TestLoadValidatesCatalogBeforeMutatingKeyPool(t *testing.T) {
	local := providerFixture(t, "local", true)
	catalog, _ := NewCatalog([]domain.Provider{local}, local.ID)
	repository := &memoryProviderRepository{state: SavedState{Providers: []domain.Provider{local, local}, ActiveID: local.ID}}
	keys := &fakeKeyPool{rates: map[string]int{"local": 77}, count: make(map[string]int)}
	manager, _ := NewManager(catalog, repository, keys)
	if err := manager.Load(context.Background()); err == nil {
		t.Fatal("duplicate persisted providers were accepted")
	}
	if keys.rates["local"] != 77 {
		t.Fatalf("invalid persisted catalog mutated key admission: %+v", keys.rates)
	}
}

func providerFixture(t *testing.T, id string, builtin bool) domain.Provider {
	t.Helper()
	provider, err := domain.New(domain.Params{
		ID: id, Name: id, BaseURL: "http://127.0.0.1:8799",
		AuthMode: domain.AuthPassthrough, Enabled: true, Builtin: builtin,
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

// An unreadable provider store must block the first write rather than let it
// atomically replace the file: the runtime catalog after a failed load holds
// builtins plus nothing, and saving it would erase every provider definition
// the user ever entered the moment the keyring unlocks mid-session.
func TestFailedLoadBlocksTheFirstWrite(t *testing.T) {
	local := providerFixture(t, "local", true)
	catalog, _ := NewCatalog([]domain.Provider{local}, local.ID)
	repository := &failingProviderRepository{}
	keys := &fakeKeyPool{rates: map[string]int{"local": 0}, count: make(map[string]int)}
	manager, _ := NewManager(catalog, repository, keys)
	if err := manager.Load(context.Background()); err == nil {
		t.Fatal("the injected load failure was not reported")
	}
	if _, err := manager.Add(context.Background(), domain.Params{
		Name: "Custom", BaseURL: "https://provider.example/v1",
		AuthMode: domain.AuthBearer, RPM: 90, Enabled: true,
	}); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("a write over an unreadable store was accepted: %v", err)
	}
	if _, err := manager.Update(context.Background(), local.ID, domain.Params{
		Name: "Local", BaseURL: "http://127.0.0.1:8799",
		AuthMode: domain.AuthPassthrough, RPM: 12, Enabled: true,
	}); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("an update over an unreadable store was accepted: %v", err)
	}
	if err := manager.Delete(context.Background(), local.ID); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("a delete over an unreadable store was accepted: %v", err)
	}
	if repository.saved {
		t.Fatal("an unreadable store was rewritten")
	}
}

type failingProviderRepository struct {
	saved bool
}

func (repository *failingProviderRepository) Load(context.Context) (SavedState, error) {
	return SavedState{}, errors.New("secure storage is locked")
}

func (repository *failingProviderRepository) Save(context.Context, SavedState) error {
	repository.saved = true
	return nil
}
