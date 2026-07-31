package application

import (
	"context"
	"errors"
	"slices"
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

func (keys *fakeKeyPool) Count(id string) int { return keys.count[id] }
func (keys *fakeKeyPool) EnsureProvider(id string, rpm int) error {
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
