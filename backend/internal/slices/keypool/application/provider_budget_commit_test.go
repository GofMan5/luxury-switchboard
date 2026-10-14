package application

import (
	"context"
	"errors"
	"testing"
	"time"

	keydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

type failedBudgetRepository struct{ duringSave func() }

func (*failedBudgetRepository) Load(context.Context) (providerapp.SavedState, error) {
	return providerapp.SavedState{}, nil
}
func (r *failedBudgetRepository) Save(context.Context, providerapp.SavedState) error {
	r.duringSave()
	return errors.New("fixture save failure")
}

func TestFailedProviderWindowEditCannotPruneCommittedQuota(t *testing.T) {
	scheduler := NewScheduler(10)
	now := time.Unix(100, 0)
	scheduler.now = func() time.Time { return now }
	keys, err := NewManager(scheduler, &memoryRepository{}, map[string]Rate{"fixture": {Limit: 1, Window: time.Minute}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Add(context.Background(), keydomain.Params{ProviderID: "fixture", Label: "fixture", Secret: "synthetic", RPM: 1}); err != nil {
		t.Fatal(err)
	}
	params := domain.Params{ID: "fixture", Name: "Fixture", BaseURL: "http://127.0.0.1:1", AuthMode: domain.AuthBearer, RPM: 1, RateUnit: domain.RatePerMinute, Enabled: true}
	provider, err := domain.New(params)
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := providerapp.NewCatalog([]domain.Provider{provider}, "fixture")
	repository := &failedBudgetRepository{duringSave: func() { keys.List("fixture") }}
	manager, err := providerapp.NewManager(catalog, repository, keys)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := scheduler.TryAcquire("fixture", "model"); !ok {
		t.Fatal("initial key unavailable")
	}
	now = now.Add(2 * time.Second)
	params.RateUnit = domain.RatePerSecond
	if _, err := manager.Update(context.Background(), "fixture", params); err == nil {
		t.Fatal("fixture persistence unexpectedly accepted")
	}
	if _, ok := scheduler.TryAcquire("fixture", "model"); ok {
		t.Fatal("failed provisional per-second window erased committed minute quota")
	}
	if got := keys.List("fixture")[0].StartsInWindow; got != 1 {
		t.Fatalf("consumed history lost: %d", got)
	}
}
