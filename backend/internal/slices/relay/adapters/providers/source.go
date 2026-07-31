package providers

import (
	"context"

	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

type Source struct {
	catalog *providerapp.Catalog
	routes  relayapp.ModelRouteResolver
}

func NewSource(catalog *providerapp.Catalog, routes relayapp.ModelRouteResolver) *Source {
	return &Source{catalog: catalog, routes: routes}
}

func (source *Source) Current(_ context.Context, model string) (relayapp.Route, error) {
	provider, err := source.catalog.Active()
	upstreamModel := model
	if source.routes != nil {
		if assignment, ok := source.routes.Relay(model); ok {
			routed, exists := source.catalog.Get(assignment.ProviderID)
			if !exists {
				return relayapp.Route{}, providerapp.ErrProviderUnavailable
			}
			provider = routed
			err = nil
			upstreamModel = assignment.UpstreamModel
		}
	}
	if err != nil {
		return relayapp.Route{}, err
	}
	return providerRoute(provider, upstreamModel), nil
}

func (source *Source) Pinned(_ context.Context, providerID, upstreamModel string) (relayapp.Route, error) {
	provider, exists := source.catalog.Get(providerID)
	if !exists {
		return relayapp.Route{}, providerapp.ErrProviderUnavailable
	}
	return providerRoute(provider, upstreamModel), nil
}

func providerRoute(provider providerdomain.Provider, upstreamModel string) relayapp.Route {
	return relayapp.Route{
		ProviderID: provider.ID, ProviderName: provider.Name, BaseURL: provider.BaseURL,
		AuthMode: string(provider.AuthMode), AuthHeader: provider.AuthHeader,
		UpstreamModel: upstreamModel, CacheTTL: provider.CacheTTL,
	}
}
