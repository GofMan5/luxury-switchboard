package providers

import (
	"context"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	relayproviders "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/providers"
	routeapp "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/application"
	routedomain "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
	"testing"
)

type routeStore struct{ rows []routedomain.Assignment }

func (store *routeStore) Load(context.Context) ([]routedomain.Assignment, error) {
	return store.rows, nil
}
func (store *routeStore) Save(_ context.Context, rows []routedomain.Assignment) error {
	store.rows = rows
	return nil
}
func TestDisabledProviderCanRetainConfigurationButCannotServe(t *testing.T) {
	active, _ := providerdomain.New(providerdomain.Params{ID: "active", Name: "Active", BaseURL: "https://active.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: true})
	off, _ := providerdomain.New(providerdomain.Params{ID: "off", Name: "Off", BaseURL: "https://off.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: false})
	catalog, _ := providerapp.NewCatalog([]providerdomain.Provider{active, off}, active.ID)
	store := &routeStore{}
	service, _ := routeapp.NewService(store, NewCatalog(catalog))
	row := routedomain.Assignment{Target: routedomain.TargetRelay, PublicModel: "public", UpstreamModel: "private", ProviderID: off.ID, Enabled: false}
	if err := service.Upsert(context.Background(), row); err != nil {
		t.Fatalf("disabled provider config rejected: %v", err)
	}
	row.PublicModel = "second"
	if err := service.UpsertMany(context.Background(), []routedomain.Assignment{row}); err != nil {
		t.Fatal(err)
	}
	if len(store.rows) != 2 {
		t.Fatal("configuration lost")
	}
	source := relayproviders.NewSource(catalog, nil, nil)
	if _, err := source.Pinned(context.Background(), off.ID, "private"); err == nil {
		t.Fatal("disabled provider admitted at serving boundary")
	}
}
