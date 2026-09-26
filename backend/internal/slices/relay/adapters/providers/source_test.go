package providers

import (
	"context"
	"errors"
	"testing"

	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

type fixedResolver struct {
	route relayapp.ModelRoute
	err   error
}

func (resolver fixedResolver) Relay(string) (relayapp.ModelRoute, bool, error) {
	return resolver.route, true, resolver.err
}

// Next walks a failover chain past the provider that refused, skipping
// siblings that no longer exist or are disabled, and reports a miss when the
// chain has nothing left worth trying.
func TestNextWalksTheChainPastMissingAndDisabledSiblings(t *testing.T) {
	active, _ := providerdomain.New(providerdomain.Params{ID: "active", Name: "Active", BaseURL: "https://active.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: true})
	healthy, _ := providerdomain.New(providerdomain.Params{ID: "healthy", Name: "Healthy", BaseURL: "https://healthy.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: true})
	disabled, _ := providerdomain.New(providerdomain.Params{ID: "disabled", Name: "Disabled", BaseURL: "https://disabled.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: false})
	catalog, _ := providerapp.NewCatalog([]providerdomain.Provider{active, healthy, disabled}, active.ID)
	chain := &fixedChain{
		routes: []relayapp.ModelRoute{
			{ProviderID: "active", UpstreamModel: "up-active"},
			{ProviderID: "gone", UpstreamModel: "up-gone"},
			{ProviderID: "disabled", UpstreamModel: "up-disabled"},
			{ProviderID: "healthy", UpstreamModel: "up-healthy"},
		},
	}
	source := NewSource(catalog, fixedResolver{}, chain)
	next, ok, err := source.Next(context.Background(), "active", "public")
	if err != nil || !ok || next.ProviderID != "healthy" || next.UpstreamModel != "up-healthy" {
		t.Fatalf("the chain did not land on the healthy sibling: ok=%v next=%+v err=%v", ok, next, err)
	}
	// Degrading is delegated to the routes half, so the relay never learns the
	// TTL or the ordering rules.
	source.Degrade("active")
	if len(chain.degraded) != 1 || chain.degraded[0] != "active" {
		t.Fatalf("degradation was not delegated: %v", chain.degraded)
	}
	// The chain exhausted of usable siblings is a miss, not an error.
	empty := NewSource(catalog, fixedResolver{}, &fixedChain{})
	if _, ok, err := empty.Next(context.Background(), "active", "public"); err != nil || ok {
		t.Fatalf("an empty chain was not a clean miss: ok=%v err=%v", ok, err)
	}
}

type fixedChain struct {
	routes   []relayapp.ModelRoute
	degraded []string
}

func (chain *fixedChain) Chain(string) []relayapp.ModelRoute { return chain.routes }
func (chain *fixedChain) Degrade(providerID string) {
	chain.degraded = append(chain.degraded, providerID)
}

func TestExplicitRouteToDisabledProviderFailsClosed(t *testing.T) {
	active, _ := providerdomain.New(providerdomain.Params{ID: "active", Name: "Active", BaseURL: "https://active.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: true})
	disabled, _ := providerdomain.New(providerdomain.Params{ID: "disabled", Name: "Disabled", BaseURL: "https://disabled.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: false})
	catalog, _ := providerapp.NewCatalog([]providerdomain.Provider{active, disabled}, active.ID)
	source := NewSource(catalog, fixedResolver{route: relayapp.ModelRoute{ProviderID: disabled.ID, UpstreamModel: "private-model"}}, nil)
	if _, err := source.Current(context.Background(), "public-model"); !errors.Is(err, providerapp.ErrProviderUnavailable) {
		t.Fatalf("disabled route silently fell back to the active provider: %v", err)
	}
}

func TestUnavailableRouteStoreDoesNotFallBackToActiveProvider(t *testing.T) {
	active, _ := providerdomain.New(providerdomain.Params{ID: "active", Name: "Active", BaseURL: "https://active.invalid/v1", AuthMode: providerdomain.AuthBearer, Enabled: true})
	catalog, _ := providerapp.NewCatalog([]providerdomain.Provider{active}, active.ID)
	routeErr := errors.New("route store unavailable")
	source := NewSource(catalog, fixedResolver{err: routeErr}, nil)
	if _, err := source.Current(context.Background(), "public-model"); !errors.Is(err, routeErr) {
		t.Fatalf("unavailable route store silently fell back to the active provider: %v", err)
	}
}
