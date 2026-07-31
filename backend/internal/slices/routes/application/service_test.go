package application

import (
	"context"
	"errors"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
	"slices"
	"testing"
)

type memoryRepository struct {
	values []domain.Assignment
	fail   bool
}

func (repository *memoryRepository) Load(context.Context) ([]domain.Assignment, error) {
	return slices.Clone(repository.values), nil
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
