package routes

import (
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/routes/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
)

type Resolver struct{ service *application.Service }

func NewResolver(service *application.Service) *Resolver { return &Resolver{service: service} }
func (resolver *Resolver) Relay(model string) (relayapp.ModelRoute, bool, error) {
	if err := resolver.service.Availability(); err != nil {
		return relayapp.ModelRoute{}, false, err
	}
	assignment, ok := resolver.service.Resolve(domain.TargetRelay, model)
	if !ok {
		return relayapp.ModelRoute{}, false, nil
	}
	return relayapp.ModelRoute{ProviderID: assignment.ProviderID, UpstreamModel: assignment.UpstreamModel}, true, nil
}

// Chain hands the relay the failover order for a public model, healthiest
// provider first. Availability is not re-checked here: an unreadable route
// store already fails Relay, and a chain over a failed store must not look
// usable either.
func (resolver *Resolver) Chain(publicModel string) []relayapp.ModelRoute {
	if resolver.service.Availability() != nil {
		return nil
	}
	assignments := resolver.service.Chain(publicModel)
	chain := make([]relayapp.ModelRoute, 0, len(assignments))
	for _, assignment := range assignments {
		chain = append(chain, relayapp.ModelRoute{ProviderID: assignment.ProviderID, UpstreamModel: assignment.UpstreamModel})
	}
	return chain
}

func (resolver *Resolver) Degrade(providerID string) {
	resolver.service.Degrade(providerID)
}
