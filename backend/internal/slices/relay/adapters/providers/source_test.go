package providers

import (
	"context"
	"errors"
	"testing"

	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

type fixedResolver struct{ route relayapp.ModelRoute }

func (resolver fixedResolver) Relay(string) (relayapp.ModelRoute, bool) { return resolver.route, true }

func TestExplicitRouteToDisabledProviderFailsClosed(t *testing.T) {
	active, _ := providerdomain.New(providerdomain.Params{ID: "active", Name: "Active", BaseURL: "https://active.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: true})
	disabled, _ := providerdomain.New(providerdomain.Params{ID: "disabled", Name: "Disabled", BaseURL: "https://disabled.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: false})
	catalog, _ := providerapp.NewCatalog([]providerdomain.Provider{active, disabled}, active.ID)
	source := NewSource(catalog, fixedResolver{route: relayapp.ModelRoute{ProviderID: disabled.ID, UpstreamModel: "private-model"}})
	if _, err := source.Current(context.Background(), "public-model"); !errors.Is(err, providerapp.ErrProviderUnavailable) {
		t.Fatalf("disabled route silently fell back to the active provider: %v", err)
	}
}
