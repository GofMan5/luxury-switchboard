package live

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	analyticsdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/domain"
	backupapp "github.com/luxuryprivate/switchboard/backend/internal/slices/backup/application"
	backupdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/backup/domain"
	keypoolapp "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/application"
	keypooldomain "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	routeproviders "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/adapters/providers"
	routeapp "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/application"
)

type backupSetup struct {
	service    *backupapp.Service
	catalog    *providerapp.Catalog
	providers  *memoryProviderRepo
	keys       *memoryKeyRepo
	routes     *memoryRouteRepo
	keyManager *keypoolapp.Manager
}

func newBackupSetup(t *testing.T) backupSetup {
	t.Helper()
	local := plainProvider(t)
	local.ID = "local"
	catalog, err := providerapp.NewCatalog([]providerdomain.Provider{local}, local.ID)
	if err != nil {
		t.Fatal(err)
	}
	providers, keys, routes := &memoryProviderRepo{}, &memoryKeyRepo{}, &memoryRouteRepo{}
	keyManager, err := keypoolapp.NewManager(keypoolapp.NewScheduler(64), keys, map[string]keypoolapp.Rate{"local": {Limit: 60}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	providerManager, err := providerapp.NewManager(catalog, providers, keyManager)
	if err != nil {
		t.Fatal(err)
	}
	routeService, err := routeapp.NewService(routes, routeproviders.NewCatalog(catalog))
	if err != nil {
		t.Fatal(err)
	}
	service, err := backupapp.NewService(NewSources(providers, keys, routes, emptyPrices{}), NewSinks(providerManager, keyManager, routeService, catalog, &recordingPrices{}))
	if err != nil {
		t.Fatal(err)
	}
	return backupSetup{service, catalog, providers, keys, routes, keyManager}
}

type emptyPrices struct{}

func (emptyPrices) Load(context.Context) (analyticsdomain.Catalog, error) {
	return analyticsdomain.NewCatalog(), nil
}

func importDocument(t *testing.T, service *backupapp.Service, document backupdomain.Document) backupdomain.ImportReport {
	t.Helper()
	content, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.Import(string(content))
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestProviderRateUnitRoundtripsAndLegacyBackupsKeepMinuteBudget(t *testing.T) {
	for _, unit := range []providerdomain.RateUnit{providerdomain.RatePerSecond, providerdomain.RatePerMinute} {
		t.Run(string(unit), func(t *testing.T) {
			original := newBackupSetup(t)
			provider := plainProvider(t)
			provider.ID, provider.RPM, provider.RateUnit = "rate", 10, unit
			original.providers.state = providerapp.SavedState{Providers: []providerdomain.Provider{provider}, ActiveID: provider.ID}
			path, err := original.service.Export(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			restored := newBackupSetup(t)
			report, err := restored.service.Import(string(raw))
			if err != nil || report.ProvidersAdded != 1 || report.Failed != 0 {
				t.Fatalf("restore failed: %+v, %v", report, err)
			}
			got, exists := restored.catalog.Lookup(provider.ID)
			if !exists || got.RPM != 10 || got.RateUnit != unit || got.RateWindow() != provider.RateWindow() {
				t.Fatalf("rate budget changed: got %d/%s, want 10/%s", got.RPM, got.RateUnit, unit)
			}
			if !strings.Contains(string(raw), `"rateUnit"`) {
				t.Fatal("new backup omitted rate unit")
			}
		})
	}
	legacy := newBackupSetup(t)
	report := importDocument(t, legacy.service, backupdomain.Document{Version: 1, Providers: []backupdomain.ProviderEntry{{ID: "legacy", Name: "Legacy", BaseURL: "https://legacy.invalid/v1", AuthMode: "bearer", RPM: 10, Enabled: true}}})
	got, exists := legacy.catalog.Lookup("legacy")
	if report.Failed != 0 || !exists || got.RateUnit != providerdomain.RatePerMinute {
		t.Fatalf("legacy rate meaning changed: %+v, %s", report, got.RateUnit)
	}
	invalid := newBackupSetup(t)
	report, err := invalid.service.Import(`{"version":1,"providers":[{"id":"invalid","name":"Invalid","baseUrl":"https://invalid.invalid/v1","authMode":"bearer","rpm":10,"rateUnit":"hour","enabled":true}]}`)
	if err != nil || report.Failed != 1 {
		t.Fatalf("invalid unit did not fail validation: %+v, %v", report, err)
	}
}

func TestKeyRestoreUsesRecordedPriorityWithinEachProviderNotLabels(t *testing.T) {
	original := newBackupSetup(t)
	for _, id := range []string{"alpha", "beta"} {
		provider := plainProvider(t)
		provider.ID = id
		original.providers.state.Providers = append(original.providers.state.Providers, provider)
	}
	for _, fixture := range []struct {
		provider, label string
		priority        int
		pinned          bool
	}{
		{"alpha", "Alpha fallback", 9, false},
		{"beta", "Alpha fallback", 5, false},
		{"alpha", "Pinned environment", 0, true},
		{"beta", "Zulu preferred", 2, false},
		{"alpha", "Zulu preferred", 3, false},
	} {
		key, err := keypooldomain.NewKey(keypooldomain.Params{
			ProviderID: fixture.provider, Label: fixture.label, Priority: fixture.priority,
			Secret: "synthetic-" + fixture.label, Pinned: fixture.pinned,
		})
		if err != nil {
			t.Fatal(err)
		}
		original.keys.keys = append(original.keys.keys, key)
	}
	path, err := original.service.Export(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Pinned environment") {
		t.Fatal("pinned environment credential was exported")
	}
	restored := newBackupSetup(t)
	report, err := restored.service.Import(string(raw))
	if err != nil || report.KeysAdded != 4 || report.Failed != 0 {
		t.Fatalf("restore failed: %+v, %v", report, err)
	}
	for _, id := range []string{"alpha", "beta"} {
		got := restored.keyManager.List(id)
		if len(got) != 2 || got[0].Label != "Zulu preferred" || got[1].Label != "Alpha fallback" || got[0].Priority >= got[1].Priority {
			t.Fatalf("recorded order changed for %s: %+v", id, got)
		}
	}
	restarted, err := keypoolapp.NewManager(keypoolapp.NewScheduler(64), restored.keys,
		map[string]keypoolapp.Rate{"alpha": {}, "beta": {}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alpha", "beta"} {
		got := restarted.List(id)
		if len(got) != 2 || got[0].Label != "Zulu preferred" || got[1].Label != "Alpha fallback" {
			t.Fatalf("persisted order changed for %s: %+v", id, got)
		}
	}
	report, err = restored.service.Import(string(raw))
	if err != nil || report.KeysAdded != 0 || report.KeysSkipped != 4 || report.Failed != 0 {
		t.Fatalf("reimport changed existing keys: %+v, %v", report, err)
	}
}

func TestInvalidRecordedKeyPrioritiesAreRefusedRatherThanPromoted(t *testing.T) {
	setup := newBackupSetup(t)
	report := importDocument(t, setup.service, backupdomain.Document{
		Version: backupdomain.FormatVersion,
		Keys: []backupdomain.KeyEntry{
			{ProviderID: "local", Label: "negative", Secret: "synthetic-negative", Priority: -1},
			{ProviderID: "local", Label: "oversized", Secret: "synthetic-oversized", Priority: keypooldomain.MaxRPM + 1},
			{ProviderID: "local", Label: "valid", Secret: "synthetic-valid", Priority: 0},
		},
	})
	got := setup.keyManager.List("local")
	if report.Failed != 2 || report.KeysAdded != 1 || len(got) != 1 || got[0].Label != "valid" {
		t.Fatalf("invalid recorded priorities became usable keys: %+v, %+v", report, got)
	}
}

func TestDisabledProviderRestoresDependentsAndRepeatedImportDoesNotEnableIt(t *testing.T) {
	setup := newBackupSetup(t)
	document := backupdomain.Document{
		Version:   backupdomain.FormatVersion,
		Providers: []backupdomain.ProviderEntry{{ID: "disabled", Name: "Disabled", BaseURL: "https://provider.invalid/v1", AuthMode: "bearer", Enabled: false}},
		Keys:      []backupdomain.KeyEntry{{ProviderID: "disabled", Label: "main", Secret: "synthetic-disabled-key"}},
		Routes:    []backupdomain.RouteEntry{{ProviderID: "disabled", Target: "relay", PublicModel: "alias", UpstreamModel: "model", Enabled: false}},
	}
	report := importDocument(t, setup.service, document)
	if report.ProvidersAdded != 1 || report.KeysAdded != 1 || report.RoutesAdded != 1 || report.Failed != 0 {
		t.Fatalf("disabled provider dependents were not restored: %+v", report)
	}
	provider, exists := setup.catalog.Lookup("disabled")
	if !exists || provider.Enabled || len(setup.keys.keys) != 1 || len(setup.routes.assignments) != 1 || setup.routes.assignments[0].Enabled {
		t.Fatal("restore changed disabled configuration or lost its dependencies")
	}
	if _, admitted := setup.catalog.Get("disabled"); admitted {
		t.Fatal("disabled provider became servable")
	}
	report = importDocument(t, setup.service, document)
	if report.ProvidersAdded != 0 || report.KeysAdded != 0 || report.ProvidersSkipped != 1 || report.KeysSkipped != 1 || report.Failed != 0 {
		t.Fatalf("reimport did not recognize disabled provider: %+v", report)
	}
	if provider, _ := setup.catalog.Lookup("disabled"); provider.Enabled {
		t.Fatal("reimport enabled disabled provider")
	}
}
