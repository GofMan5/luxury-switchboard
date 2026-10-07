package live

import (
	"context"
	"encoding/json"
	"testing"

	analyticsdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/domain"
	backupapp "github.com/luxuryprivate/switchboard/backend/internal/slices/backup/application"
	backupdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/backup/domain"
	keypoolapp "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/application"
	keypooldomain "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	routeapp "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/application"
	routedomain "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
)

// fakeProviderStore stands in for the encrypted provider store on the export
// side: Load only has to hand back the saved state.
type fakeProviderStore struct {
	state providerapp.SavedState
}

func (store *fakeProviderStore) Load(context.Context) (providerapp.SavedState, error) {
	return store.state, nil
}

func plainProvider(t *testing.T) providerdomain.Provider {
	t.Helper()
	provider, err := providerdomain.New(providerdomain.Params{
		ID: "custom", Name: "Custom", BaseURL: "https://api.example.com/v1",
		AuthMode: providerdomain.AuthBearer, RPM: 30, Enabled: true,
	})
	if err != nil {
		t.Fatalf("plain provider fixture is invalid: %v", err)
	}
	return provider
}

func presetProvider(t *testing.T) providerdomain.Provider {
	t.Helper()
	provider, err := providerdomain.New(providerdomain.Params{
		ID: "codex", Name: "Codex", BaseURL: "https://api.openai.com/v1",
		AuthMode: providerdomain.AuthBearer, Format: providerdomain.FormatResponses,
		RPM: 60, Enabled: true, Preset: providerdomain.PresetCodex,
	})
	if err != nil {
		t.Fatalf("preset provider fixture is invalid: %v", err)
	}
	return provider
}

func TestProvidersExportSkipsPresetEntries(t *testing.T) {
	store := &fakeProviderStore{state: providerapp.SavedState{
		Providers: []providerdomain.Provider{plainProvider(t), presetProvider(t)},
		ActiveID:  "custom",
	}}
	sources := NewSources(store, nil, nil, nil)

	entries, err := sources.Providers()
	if err != nil {
		t.Fatalf("providers export failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("exported %d providers, want the one operator-managed entry", len(entries))
	}
	entry := entries[0]
	if entry.ID != "custom" || entry.Name != "Custom" {
		t.Fatalf("exported provider %q (%q), want custom (Custom)", entry.ID, entry.Name)
	}
	if entry.BaseURL != "https://api.example.com/v1" || entry.RPM != 30 || !entry.Enabled {
		t.Fatalf("exported provider lost its settings: %+v", entry)
	}
}

func TestAStoreOfOnlyPresetEntriesExportsNoProviders(t *testing.T) {
	store := &fakeProviderStore{state: providerapp.SavedState{
		Providers: []providerdomain.Provider{presetProvider(t)},
		ActiveID:  "codex",
	}}
	sources := NewSources(store, nil, nil, nil)

	entries, err := sources.Providers()
	if err != nil {
		t.Fatalf("providers export failed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("exported %d providers, want none — preset rows are rebuilt by their own slice", len(entries))
	}
}

// The sinks side of the wiring runs through each slice's real managers, so a
// restored backup lands where a hand-typed entry would — and importing the
// same file twice is a repeat, not a pile of duplicates.
func TestImportRestoresOperatorProvidersAndIsRepeatable(t *testing.T) {
	local, err := providerdomain.New(providerdomain.Params{
		ID: "local", Name: "Local", BaseURL: "http://127.0.0.1:8787/v1",
		AuthMode: providerdomain.AuthPassthrough, RPM: 60, Enabled: true, Builtin: true,
	})
	if err != nil {
		t.Fatalf("builtin provider fixture is invalid: %v", err)
	}
	catalog, err := providerapp.NewCatalog([]providerdomain.Provider{local}, "local")
	if err != nil {
		t.Fatalf("catalog fixture is invalid: %v", err)
	}

	providerRepo := &memoryProviderRepo{}
	keys, err := keypoolapp.NewManager(keypoolapp.NewScheduler(64), &memoryKeyRepo{},
		map[string]keypoolapp.Rate{"local": {Limit: 60}}, nil)
	if err != nil {
		t.Fatalf("key manager fixture is invalid: %v", err)
	}
	providerManager, err := providerapp.NewManager(catalog, providerRepo, keys)
	if err != nil {
		t.Fatalf("provider manager fixture is invalid: %v", err)
	}

	routeRepo := &memoryRouteRepo{}
	routes, err := routeapp.NewService(routeRepo, catalogLookup{catalog})
	if err != nil {
		t.Fatalf("route service fixture is invalid: %v", err)
	}
	prices := &recordingPrices{}
	sinks := NewSinks(providerManager, keys, routes, catalog, prices)

	service, err := backupapp.NewService(NewSources(&fakeProviderStore{}, nil, nil, nil), sinks)
	if err != nil {
		t.Fatalf("backup service fixture is invalid: %v", err)
	}

	document := backupdomain.Document{
		Version: backupdomain.FormatVersion,
		Providers: []backupdomain.ProviderEntry{{
			ID: "custom", Name: "Custom", BaseURL: "https://api.example.com/v1",
			AuthMode: string(providerdomain.AuthBearer), Dialect: string(providerdomain.DialectOpenAI),
			Format: string(providerdomain.FormatChat), RPM: 30, Enabled: true,
		}},
		Keys: []backupdomain.KeyEntry{{
			ProviderID: "custom", Label: "main", Secret: "sk-test-123", RPM: 30,
		}},
		Routes: []backupdomain.RouteEntry{{
			Target: string(routedomain.TargetRelay), PublicModel: "fast",
			UpstreamModel: "gpt-4o-mini", ProviderID: "custom",
			ContextLimitKiB: 128, Enabled: true, Priority: 1,
		}},
		Prices: []backupdomain.PriceEntry{{Model: "fast", Input: 1, Output: 2}},
	}
	content, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("backup document fixture is invalid: %v", err)
	}

	report, err := service.Import(string(content))
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if report.Failed != 0 {
		t.Fatalf("first import reported %d failures, want none: %+v", report.Failed, report)
	}
	if report.ProvidersAdded != 1 || report.KeysAdded != 1 || report.RoutesAdded != 1 || report.PricesRestored != 1 {
		t.Fatalf("first import report is wrong: %+v", report)
	}

	restored, exists := providerManager.Get(context.Background(), "custom")
	if !exists {
		t.Fatalf("imported provider is missing from the catalog")
	}
	if restored.Name != "Custom" || restored.BaseURL.String() != "https://api.example.com/v1" || !restored.Enabled {
		t.Fatalf("imported provider lost its settings: %+v", restored)
	}
	if restored.Preset != "" {
		t.Fatalf("imported provider carries a preset marker: %q", restored.Preset)
	}
	if got := keys.Count("custom"); got != 1 {
		t.Fatalf("imported %d keys for custom, want 1", got)
	}
	if len(routeRepo.assignments) != 1 {
		t.Fatalf("persisted %d routes, want 1", len(routeRepo.assignments))
	}
	assignment := routeRepo.assignments[0]
	if assignment.Target != routedomain.TargetRelay || assignment.PublicModel != "fast" ||
		assignment.UpstreamModel != "gpt-4o-mini" || assignment.ProviderID != "custom" ||
		assignment.ContextLimitKiB != 128 || !assignment.Enabled || assignment.Priority != 1 {
		t.Fatalf("imported route lost its settings: %+v", assignment)
	}
	if len(prices.set) != 1 || prices.set[0].Model != "fast" || prices.set[0].Input != 1 || prices.set[0].Output != 2 {
		t.Fatalf("imported price lost its settings: %+v", prices.set)
	}

	report, err = service.Import(string(content))
	if err != nil {
		t.Fatalf("second import failed: %v", err)
	}
	if report.Failed != 0 {
		t.Fatalf("second import reported %d failures, want none: %+v", report.Failed, report)
	}
	if report.ProvidersAdded != 0 || report.KeysAdded != 0 {
		t.Fatalf("second import added entries again: %+v", report)
	}
	if report.ProvidersSkipped != 1 || report.KeysSkipped != 1 {
		t.Fatalf("second import did not skip known entries: %+v", report)
	}
	if report.RoutesAdded != 1 || report.PricesRestored != 1 {
		// Routes and prices are settings, not identities: restoring them twice
		// rewrites the same row, which is the documented repeat semantics.
		t.Fatalf("second import report is wrong: %+v", report)
	}

	if got := len(catalog.List()); got != 2 {
		t.Fatalf("catalog holds %d providers after two imports, want local and custom", got)
	}
	if got := keys.Count("custom"); got != 1 {
		t.Fatalf("second import duplicated keys: %d", got)
	}
	if got := len(routeRepo.assignments); got != 1 {
		t.Fatalf("second import duplicated routes: %d", got)
	}
	if got := len(prices.set); got != 2 {
		t.Fatalf("price restore ran %d times, want 2", got)
	}
}

type memoryProviderRepo struct {
	state providerapp.SavedState
}

func (repo *memoryProviderRepo) Load(context.Context) (providerapp.SavedState, error) {
	return repo.state, nil
}

func (repo *memoryProviderRepo) Save(_ context.Context, state providerapp.SavedState) error {
	repo.state = state
	return nil
}

type memoryKeyRepo struct {
	keys []keypooldomain.Key
}

func (repo *memoryKeyRepo) Load(context.Context) ([]keypooldomain.Key, error) {
	return repo.keys, nil
}

func (repo *memoryKeyRepo) Save(_ context.Context, keys []keypooldomain.Key) error {
	repo.keys = keys
	return nil
}

type memoryRouteRepo struct {
	assignments []routedomain.Assignment
}

func (repo *memoryRouteRepo) Load(context.Context) ([]routedomain.Assignment, error) {
	return repo.assignments, nil
}

func (repo *memoryRouteRepo) Save(_ context.Context, assignments []routedomain.Assignment) error {
	repo.assignments = assignments
	return nil
}

// catalogLookup answers the routes slice's provider question with the same
// catalog the sinks restore into.
type catalogLookup struct {
	catalog *providerapp.Catalog
}

func (lookup catalogLookup) Exists(id string) bool {
	_, exists := lookup.catalog.Get(id)
	return exists
}

type recordingPrices struct {
	set []analyticsdomain.Price
}

func (prices *recordingPrices) SetPrice(_ context.Context, price analyticsdomain.Price) (analyticsdomain.Catalog, error) {
	prices.set = append(prices.set, price)
	return analyticsdomain.Catalog{
		Prices:   map[string]analyticsdomain.Price{price.Model: price},
		Currency: analyticsdomain.DefaultCurrency,
	}, nil
}
