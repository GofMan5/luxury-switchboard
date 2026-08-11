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
