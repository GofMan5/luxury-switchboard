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
	drops map[string]int
}

func (keys *fakeKeyPool) Count(id string) int { return keys.count[id] }
func (keys *fakeKeyPool) EnsureProvider(id string, rpm int, _ time.Duration) error {
	if keys.rates == nil {
		keys.rates = make(map[string]int)
	}
	keys.rates[id] = rpm
	return nil
}
func (keys *fakeKeyPool) RemoveProvider(id string) error {
	delete(keys.rates, id)
	return nil
}
func (keys *fakeKeyPool) DropProvider(_ context.Context, id string) error {
	if keys.drops == nil {
		keys.drops = make(map[string]int)
	}
	keys.drops[id]++
	delete(keys.rates, id)
	delete(keys.count, id)
	return nil
}

// fakeRouteCascade records the route removals the manager asked for: the
// delete contract under test is who owns the cascade, not what the routes
// slice does with it (that slice tests its own removal).
type fakeRouteCascade map[string]int

func (cascade fakeRouteCascade) RemoveProvider(_ context.Context, id string) error {
	cascade[id]++
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
	if err := manager.Delete(context.Background(), added.ID); err != nil {
		t.Fatalf("active provider deletion was refused: %v", err)
	}
	if repository.state.ActiveID != "local" {
		t.Fatalf("active route did not fall back to the builtin: %q", repository.state.ActiveID)
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

// A failed entry save is not a failed delete: the cascade already
// happened, and rolling it back would mean resurrecting key material
// this manager never held. The provider stays listed, empty of keys and
// routes — a consistent state — and the retry finishes the job.
func TestFailedDeletePersistenceLeavesTheCascadeDoneAndTheEntryRetriable(t *testing.T) {
	local := providerFixture(t, "local", true)
	custom := providerFixture(t, "custom", false)
	catalog, _ := NewCatalog([]domain.Provider{local, custom}, local.ID)
	keys := &fakeKeyPool{rates: map[string]int{"local": 0, "custom": 77}, count: map[string]int{"custom": 2}}
	routes := fakeRouteCascade{}
	repository := &memoryProviderRepository{saveFail: true}
	repository.beforeSave = func() {
		if _, exists := keys.rates["custom"]; exists {
			t.Fatal("provider remained open for concurrent key insertion during delete")
		}
	}
	manager, _ := NewManager(catalog, repository, keys)
	manager.SetRouteCascade(routes)
	if err := manager.Delete(context.Background(), "custom"); err == nil {
		t.Fatal("injected persistence failure was ignored")
	}
	if routes["custom"] != 1 || keys.drops["custom"] != 1 || keys.rates["custom"] != 0 {
		t.Fatalf("failed save rolled the cascade back: routes=%v drops=%v rates=%v", routes, keys.drops, keys.rates)
	}
	if _, exists := catalog.Lookup("custom"); !exists {
		t.Fatal("failed save removed the entry from the runtime catalog anyway")
	}
	repository.saveFail = false
	if err := manager.Delete(context.Background(), "custom"); err != nil {
		t.Fatalf("retry after a failed save was refused: %v", err)
	}
	if _, exists := catalog.Lookup("custom"); exists {
		t.Fatal("the retried delete left the provider in the catalog")
	}
}

// Deleting a provider is one deliberate action, not a scavenger hunt: the
// routes and keys that name it leave with it, in the same delete, before
// the entry itself goes. The old guards made the user pre-clean routes and
// keys by hand precisely so a delete could refuse — the user already
// decided, and the manager now finishes the job.
func TestDeleteCascadesRoutesAndKeysBeforeRemovingTheEntry(t *testing.T) {
	local := providerFixture(t, "local", true)
	custom := providerFixture(t, "custom", false)
	catalog, _ := NewCatalog([]domain.Provider{local, custom}, local.ID)
	keys := &fakeKeyPool{rates: map[string]int{"local": 0, "custom": 77}, count: map[string]int{"custom": 2}}
	routes := fakeRouteCascade{}
	repository := &memoryProviderRepository{}
	manager, _ := NewManager(catalog, repository, keys)
	manager.SetRouteCascade(routes)
	if err := manager.Delete(context.Background(), "custom"); err != nil {
		t.Fatalf("delete with keys and routes was refused: %v", err)
	}
	if routes["custom"] != 1 {
		t.Fatalf("routes were not cascaded: %v", routes)
	}
	if keys.drops["custom"] != 1 {
		t.Fatalf("keys were not cascaded: %v", keys.drops)
	}
	if keys.rates["custom"] != 0 {
		t.Fatalf("key admission survived the cascade: %v", keys.rates)
	}
	if _, exists := catalog.Lookup("custom"); exists {
		t.Fatal("deleted provider stayed in the catalog")
	}
	if len(repository.state.Providers) != 1 || repository.state.Providers[0].ID != "local" {
		t.Fatalf("entry was not removed from the store: %+v", repository.state.Providers)
	}
	if repository.state.ActiveID != "local" {
		t.Fatalf("an untouched active provider moved: %q", repository.state.ActiveID)
	}
}

// Deleting the provider the active route points at is not refused: the
// delete is the user's decision, so the route moves with it — to a
// deterministic builtin, never to a dangling id. catalog.Active() must
// always resolve; the refusal belongs to the case with nowhere to move.
func TestDeletingTheActiveProviderFallsBackToABuiltin(t *testing.T) {
	local := providerFixture(t, "local", true)
	echo := providerFixture(t, "echo", true)
	custom := providerFixture(t, "custom", false)
	catalog, _ := NewCatalog([]domain.Provider{local, echo, custom}, custom.ID)
	keys := &fakeKeyPool{rates: map[string]int{"local": 0, "custom": 77}, count: make(map[string]int)}
	routes := fakeRouteCascade{}
	repository := &memoryProviderRepository{}
	manager, _ := NewManager(catalog, repository, keys)
	manager.SetRouteCascade(routes)
	if err := manager.Delete(context.Background(), "custom"); err != nil {
		t.Fatalf("deleting the active provider was refused: %v", err)
	}
	if _, exists := catalog.Lookup("custom"); exists {
		t.Fatal("the active provider was not removed from the catalog")
	}
	active, err := catalog.Active()
	if err != nil {
		t.Fatalf("the catalog has no active provider after the delete: %v", err)
	}
	if active.ID != "local" {
		t.Fatalf("active route fell back to %q, want the first enabled builtin", active.ID)
	}
	if repository.state.ActiveID != "local" {
		t.Fatalf("persisted active id is %q, want local", repository.state.ActiveID)
	}
}

// The one refusal left: the active provider is the last enabled one.
// Moving the route to nothing would leave every future request without
// a provider, so the delete is rejected before anything is cascaded.
func TestDeletingTheLastEnabledProviderIsRefusedBeforeTheCascade(t *testing.T) {
	local, err := domain.New(domain.Params{
		ID: "local", Name: "local", BaseURL: "http://127.0.0.1:8798",
		AuthMode: domain.AuthPassthrough, Builtin: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	echo, err := domain.New(domain.Params{
		ID: "echo", Name: "echo", BaseURL: "http://127.0.0.1:8798",
		AuthMode: domain.AuthPassthrough, Builtin: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	custom := providerFixture(t, "custom", false)
	catalog, _ := NewCatalog([]domain.Provider{local, echo, custom}, custom.ID)
	keys := &fakeKeyPool{rates: map[string]int{"custom": 77}, count: map[string]int{"custom": 2}}
	routes := fakeRouteCascade{}
	manager, _ := NewManager(catalog, &memoryProviderRepository{}, keys)
	manager.SetRouteCascade(routes)
	if err := manager.Delete(context.Background(), "custom"); err == nil {
		t.Fatal("deleting the last enabled provider was accepted")
	}
	if routes["custom"] != 0 || keys.drops["custom"] != 0 || keys.rates["custom"] != 77 {
		t.Fatalf("refused delete still cascaded: routes=%v drops=%v rates=%v", routes, keys.drops, keys.rates)
	}
	if _, exists := catalog.Lookup("custom"); !exists {
		t.Fatal("refused delete removed the entry anyway")
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

// List is the provisioner's read surface: it must see every entry — a
// retired, disabled preset included — or relinking a parked codex row
// would mint a duplicate instead of reclaiming it. The answer is the
// catalog's order as a copy: mutating it must not leak back in.
func TestListSeesEveryEntryIncludingDisabledOnes(t *testing.T) {
	local := providerFixture(t, "local", true)
	codex, err := domain.New(domain.Params{
		ID: "codex", Name: "Codex", BaseURL: "https://chatgpt.com/backend-api/codex",
		AuthMode: domain.AuthBearer, Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := NewCatalog([]domain.Provider{local, codex}, local.ID)
	manager, _ := NewManager(catalog, &memoryProviderRepository{}, &fakeKeyPool{})
	entries := manager.List(context.Background())
	if len(entries) != 2 || entries[0].ID != "local" || entries[1].ID != "codex" {
		t.Fatalf("list did not report every entry in catalog order: %+v", entries)
	}
	if entries[1].Enabled {
		t.Fatal("a disabled entry was not reported as itself")
	}
	entries[1].Name = "mutated"
	if fresh := manager.List(context.Background()); fresh[1].Name != "Codex" {
		t.Fatal("mutating the returned slice leaked into the catalog")
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
