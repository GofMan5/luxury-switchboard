package application

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/backup/domain"
)

type memorySources struct {
	providers []domain.ProviderEntry
	keys      []domain.KeyEntry
	routes    []domain.RouteEntry
	prices    []domain.PriceEntry
}

func (sources *memorySources) Providers() ([]domain.ProviderEntry, error) {
	return sources.providers, nil
}
func (sources *memorySources) Keys() ([]domain.KeyEntry, error)     { return sources.keys, nil }
func (sources *memorySources) Routes() ([]domain.RouteEntry, error) { return sources.routes, nil }
func (sources *memorySources) Prices() ([]domain.PriceEntry, error) { return sources.prices, nil }

type memorySinks struct {
	providers map[string]bool
	keys      map[string]bool
	routes    map[string]bool
	prices    map[string]domain.PriceEntry
	refuse    map[string]bool
}

func newMemorySinks() *memorySinks {
	return &memorySinks{providers: map[string]bool{}, keys: map[string]bool{}, routes: map[string]bool{}, prices: map[string]domain.PriceEntry{}, refuse: map[string]bool{}}
}

func (sinks *memorySinks) AddProvider(entry domain.ProviderEntry) (bool, error) {
	if sinks.providers[entry.ID] {
		return false, nil
	}
	sinks.providers[entry.ID] = true
	return true, nil
}

func (sinks *memorySinks) AddKey(entry domain.KeyEntry) (bool, error) {
	// The stable derived id: provider + secret, same as the real domain.
	id := entry.ProviderID + "/" + entry.Secret
	if sinks.keys[id] {
		return false, nil
	}
	sinks.keys[id] = true
	return true, nil
}

func (sinks *memorySinks) UpsertRoute(entry domain.RouteEntry) (bool, error) {
	id := entry.Target + "/" + entry.PublicModel + "/" + entry.ProviderID
	if sinks.routes[id] {
		return false, nil
	}
	sinks.routes[id] = true
	return true, nil
}

func (sinks *memorySinks) ProviderExists(id string) bool { return sinks.providers[id] }

func (sinks *memorySinks) RestorePrice(entry domain.PriceEntry) error {
	if sinks.refuse[entry.Model] {
		return errors.New("rate is out of range")
	}
	sinks.prices[entry.Model] = entry
	return nil
}

func TestExportWritesPlainJSONAndImportRestoresIt(t *testing.T) {
	sources := &memorySources{
		providers: []domain.ProviderEntry{{ID: "alpha-relay", Name: "Alpha Relay", BaseURL: "https://alpha-relay.example/v1", AuthMode: "bearer", Format: "responses", Enabled: true}},
		keys:      []domain.KeyEntry{{ProviderID: "alpha-relay", Label: "Primary", Secret: "sk-plain-secret", RPM: 60}},
		routes:    []domain.RouteEntry{{Target: "relay", PublicModel: "glm", UpstreamModel: "glm-5.3", ProviderID: "alpha-relay", Enabled: true, Priority: 0}},
	}
	service, err := NewService(sources, newMemorySinks())
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	path, err := service.Export(folder)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Plain JSON, secrets in the clear — that is the point of the format, and
	// the note says so to whoever opens the file.
	if !strings.Contains(string(raw), "sk-plain-secret") || !strings.Contains(string(raw), "Plain-text Switchboard backup") {
		t.Fatalf("the backup is not the plain document it promises to be")
	}

	sinks := newMemorySinks()
	restoring, err := NewService(sources, sinks)
	if err != nil {
		t.Fatal(err)
	}
	report, err := restoring.Import(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if report.ProvidersAdded != 1 || report.KeysAdded != 1 || report.RoutesAdded != 1 || report.Failed != 0 {
		t.Fatalf("the restore did not land: %+v", report)
	}
	// Restoring over the restored setup skips everything: the operation is
	// repeatable, not destructive.
	report, err = restoring.Import(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if report.ProvidersAdded != 0 || report.KeysAdded != 0 || report.RoutesAdded != 0 || report.ProvidersSkipped != 1 || report.KeysSkipped != 1 {
		t.Fatalf("a second restore was not a no-op: %+v", report)
	}
	// A key whose provider is not in the backup nor live is refused, not
	// half-imported.
	orphan, err := NewService(sources, sinks)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orphan.Import(`{"version":1,"exportedAt":"` + time.Now().Format(time.RFC3339) + `","keys":[{"providerId":"ghost","label":"x","secret":"y"}]}`); err != nil {
		t.Fatal(err)
	}
	if !sinks.providers["alpha-relay"] || sinks.providers["ghost"] {
		t.Fatal("the import touched providers it should not have")
	}
	// Garbage is refused with a sentence.
	if _, err := orphan.Import("not json"); err == nil {
		t.Fatal("garbage was accepted as a backup")
	}
	_ = filepath.Join
}

// The price catalog travels with the backup: an export carries it, a restore
// lands it, and an impossible rate is counted as failed instead of silently
// dropped — the same accounting every other entry gets.
func TestPricesTravelWithTheBackupAndRefuseLikeAnyEntry(t *testing.T) {
	sources := &memorySources{
		providers: []domain.ProviderEntry{{ID: "alpha-relay", Name: "Alpha Relay", BaseURL: "https://alpha-relay.example/v1", AuthMode: "bearer", Format: "responses", Enabled: true}},
		prices: []domain.PriceEntry{
			{Model: "glm-5.3-prime", Input: 1, CachedInput: 0.1, Output: 2, Reasoning: 2},
			{Model: "claude-opus-5", Input: 3, Output: 15},
		},
	}
	service, err := NewService(sources, newMemorySinks())
	if err != nil {
		t.Fatal(err)
	}
	path, err := service.Export(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "glm-5.3-prime") || !strings.Contains(string(raw), `"prices"`) {
		t.Fatalf("the price catalog did not travel: %s", string(raw))
	}

	sinks := newMemorySinks()
	restoring, err := NewService(sources, sinks)
	if err != nil {
		t.Fatal(err)
	}
	report, err := restoring.Import(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if report.PricesRestored != 2 || report.Failed != 0 || len(sinks.prices) != 2 {
		t.Fatalf("prices did not restore: %+v", report)
	}
	// A rate the analytics service would refuse is counted, not dropped.
	sinks.refuse["glm-5.3-prime"] = true
	report, err = restoring.Import(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if report.PricesRestored != 1 || report.Failed != 1 {
		t.Fatalf("a refused rate was not accounted: %+v", report)
	}
}

// A backup written before prices existed is still a valid backup: the field
// is optional, so an import restores everything it always carried and leaves
// the live catalog untouched.
func TestAPrePriceBackupImportsWithoutTouchingTheCatalog(t *testing.T) {
	sources := &memorySources{
		providers: []domain.ProviderEntry{{ID: "alpha-relay", Name: "Alpha Relay", BaseURL: "https://alpha-relay.example/v1", AuthMode: "bearer", Format: "responses", Enabled: true}},
	}
	sinks := newMemorySinks()
	sinks.prices["glm-5.3-prime"] = domain.PriceEntry{Model: "glm-5.3-prime", Input: 1, Output: 2}
	service, err := NewService(sources, sinks)
	if err != nil {
		t.Fatal(err)
	}
	document := `{"version":1,"exportedAt":"` + time.Now().Format(time.RFC3339) + `","providers":[{"id":"alpha-relay","name":"Alpha Relay","baseUrl":"https://alpha-relay.example/v1","authMode":"bearer","format":"responses","enabled":true}]}`
	report, err := service.Import(document)
	if err != nil {
		t.Fatal(err)
	}
	if report.ProvidersAdded != 1 || report.PricesRestored != 0 {
		t.Fatalf("the old-format import misbehaved: %+v", report)
	}
	if _, kept := sinks.prices["glm-5.3-prime"]; !kept {
		t.Fatal("an old backup overwrote the live price catalog")
	}
}
