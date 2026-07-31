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

func NewSource(catalog *providerapp.Catalog, routes ...relayapp.ModelRouteResolver) *Source {
	var resolver relayapp.ModelRouteResolver
	if len(routes) > 0 {
		resolver = routes[0]
	}
	return &Source{catalog: catalog, routes: resolver}
}

func (source *Source) Current(_ context.Context, model string) (relayapp.Route, error) {
	provider, err := source.catalog.Active()
	upstreamModel := model
	if source.routes != nil {
		if assignment, ok := source.routes.Relay(model); ok {
			if routed, exists := source.catalog.Get(assignment.ProviderID); exists {
				provider = routed
				err = nil
				upstreamModel = assignment.UpstreamModel
			}
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
		Dialect: string(provider.Dialect), ModelsPath: provider.ModelsPath,
		UpstreamModel: upstreamModel, RPM: provider.RPM, CacheTTL: provider.CacheTTL,
	}
}
