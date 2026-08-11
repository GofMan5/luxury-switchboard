package application

import (
	"context"
	"errors"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
	"slices"
	"testing"
)

type memoryRepository struct {
	values  []domain.Assignment
	loadErr error
	fail    bool
}

func (repository *memoryRepository) Load(context.Context) ([]domain.Assignment, error) {
	if repository.loadErr != nil {
		return nil, repository.loadErr
	}
	return slices.Clone(repository.values), nil
}

func TestFailedLoadBlocksFallbackAndOverwrite(t *testing.T) {
	loadErr := errors.New("encrypted route store unavailable")
	repository := &memoryRepository{loadErr: loadErr}
	service, _ := NewService(repository, providers{"echo": true})
	if err := service.Load(context.Background()); !errors.Is(err, loadErr) {
		t.Fatalf("load failure was hidden: %v", err)
	}
	if _, ok := service.Resolve(domain.TargetRelay, "public"); ok {
		t.Fatal("route resolved after its durable state failed to load")
	}
	err := service.Upsert(context.Background(), domain.Assignment{Target: domain.TargetRelay, PublicModel: "public", UpstreamModel: "private", ProviderID: "echo", Enabled: true})
	if !errors.Is(err, loadErr) {
		t.Fatalf("unreadable route store could be overwritten: %v", err)
	}
}
func (repository *memoryRepository) Save(_ context.Context, values []domain.Assignment) error {
	if repository.fail {
		return errors.New("injected")
	}
	repository.values = slices.Clone(values)
	return nil
}

type providers map[string]bool

func (value providers) Exists(id string) bool { return value[id] }
func TestRelayAndTunnelRoutesRemainIndependent(t *testing.T) {
	repository := &memoryRepository{}
	service, _ := NewService(repository, providers{"echo": true, "custom": true})
	relay := domain.Assignment{Target: domain.TargetRelay, PublicModel: "gpt-x", UpstreamModel: "gpt-x", ProviderID: "echo", Enabled: true}
	tunnel := domain.Assignment{Target: domain.TargetTunnel, PublicModel: "public-gpt", UpstreamModel: "gpt-x", ProviderID: "custom", ContextLimitKiB: 128 * 1024, Enabled: true}
	if err := service.Upsert(context.Background(), relay); err != nil {
		t.Fatal(err)
	}
	if err := service.Upsert(context.Background(), tunnel); err != nil {
		t.Fatal(err)
	}
	if got, ok := service.Resolve(domain.TargetRelay, "gpt-x"); !ok || got.ProviderID != "echo" {
		t.Fatalf("relay route missing: %+v", got)
	}
	if got, ok := service.Resolve(domain.TargetTunnel, "public-gpt"); !ok || got.UpstreamModel != "gpt-x" {
		t.Fatalf("tunnel route missing: %+v", got)
	}
	if err := service.Delete(context.Background(), domain.TargetRelay, "gpt-x"); err != nil {
		t.Fatal(err)
	}
	if _, ok := service.Resolve(domain.TargetTunnel, "public-gpt"); !ok {
		t.Fatal("relay deletion changed tunnel route")
	}
}
func TestFailedSaveDoesNotMutateRoutes(t *testing.T) {
	repository := &memoryRepository{fail: true}
	service, _ := NewService(repository, providers{"echo": true})
	err := service.Upsert(context.Background(), domain.Assignment{Target: domain.TargetRelay, PublicModel: "gpt", UpstreamModel: "gpt", ProviderID: "echo", Enabled: true})
	if err == nil {
		t.Fatal("save failure ignored")
	}
	if len(service.List(domain.TargetRelay)) != 0 {
		t.Fatal("failed save mutated routes")
	}
}

func TestBulkUpsertPersistsRoutesOnce(t *testing.T) {
	repository := &memoryRepository{}
	service, _ := NewService(repository, providers{"echo": true})
	routes := []domain.Assignment{
		{Target: domain.TargetTunnel, PublicModel: "public-a", UpstreamModel: "model-a", ProviderID: "echo", Enabled: true},
		{Target: domain.TargetTunnel, PublicModel: "public-b", UpstreamModel: "model-b", ProviderID: "echo", Enabled: true},
	}
	if err := service.UpsertMany(context.Background(), routes); err != nil {
		t.Fatal(err)
	}
	if len(service.List(domain.TargetTunnel)) != 2 || len(repository.values) != 2 {
		t.Fatal("bulk routes were not persisted")
	}
}

func TestLoadKeepsStaleProviderRouteVisibleAndFailClosed(t *testing.T) {
	stale := domain.Assignment{Target: domain.TargetRelay, PublicModel: "public", UpstreamModel: "private", ProviderID: "removed", Enabled: true}
	service, _ := NewService(&memoryRepository{values: []domain.Assignment{stale}}, providers{"active": true})
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if routes := service.List(domain.TargetRelay); len(routes) != 1 || routes[0] != stale {
		t.Fatalf("stale route was silently discarded: %+v", routes)
	}
	if err := service.Upsert(context.Background(), stale); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("stale provider became writable again: %v", err)
	}
}

func TestDuplicatePersistedAndBulkRoutesAreRejected(t *testing.T) {
	route := domain.Assignment{Target: domain.TargetTunnel, PublicModel: "public", UpstreamModel: "private", ProviderID: "echo", Enabled: true}
	service, _ := NewService(&memoryRepository{values: []domain.Assignment{route, route}}, providers{"echo": true})
	if err := service.Load(context.Background()); err == nil {
		t.Fatal("duplicate persisted routes were accepted")
	}
	service, _ = NewService(&memoryRepository{}, providers{"echo": true})
	if err := service.UpsertMany(context.Background(), []domain.Assignment{route, route}); err == nil {
		t.Fatal("duplicate route batch was accepted")
	}
	if err := service.Delete(context.Background(), domain.Target("invalid"), "public"); err == nil {
		t.Fatal("invalid route deletion was reported successful")
	}
}
