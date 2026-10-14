package application

import (
	"context"
	"errors"
	"slices"
	"testing"

	keyapp "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/application"
	keydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

type admissionKeyRepository struct{ keys []keydomain.Key }

func (r *admissionKeyRepository) Load(context.Context) ([]keydomain.Key, error) {
	return slices.Clone(r.keys), nil
}
func (r *admissionKeyRepository) Save(_ context.Context, keys []keydomain.Key) error {
	r.keys = slices.Clone(keys)
	return nil
}

func TestFailedProviderAddCannotAdmitPersistedOrphanKey(t *testing.T) {
	catalog, _ := NewCatalog([]domain.Provider{providerFixture(t, "local", true)}, "local")
	keyRepository := &admissionKeyRepository{}
	keys, err := keyapp.NewManager(keyapp.NewScheduler(10), keyRepository, map[string]keyapp.Rate{"local": {}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var concurrentErr error
	repository := &memoryProviderRepository{saveFail: true, beforeSave: func() {
		_, concurrentErr = keys.Add(context.Background(), keydomain.Params{ProviderID: "fixture-restore", Label: "fixture", Secret: "synthetic"})
	}}
	manager, _ := NewManager(catalog, repository, keys)
	_, err = manager.Add(context.Background(), domain.Params{ID: "fixture-restore", Name: "fixture", BaseURL: "http://127.0.0.1:12345", AuthMode: domain.AuthBearer, Enabled: true})
	if err == nil {
		t.Fatal("failed provider save accepted")
	}
	if !errors.Is(concurrentErr, keyapp.ErrUnknownProvider) || len(keyRepository.keys) != 0 {
		t.Fatalf("uncommitted provider admitted orphan: concurrent=%v persisted=%d", concurrentErr, len(keyRepository.keys))
	}
	restarted, _ := keyapp.NewManager(keyapp.NewScheduler(10), keyRepository, map[string]keyapp.Rate{"local": {}}, nil)
	if err := restarted.Load(context.Background()); err != nil {
		t.Fatalf("failed provider save poisoned restart: %v", err)
	}
}
