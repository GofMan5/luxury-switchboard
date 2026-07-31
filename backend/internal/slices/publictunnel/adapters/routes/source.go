package routes

import (
	"github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/domain"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/routes/application"
	routedomain "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
)

type ProviderPolicy interface{ Publishable(string) bool }
type Source struct {
	service *application.Service
	policy  ProviderPolicy
}

func NewSource(service *application.Service, policies ...ProviderPolicy) *Source {
	var policy ProviderPolicy
	if len(policies) > 0 {
		policy = policies[0]
	}
	return &Source{service: service, policy: policy}
}
func (source *Source) List() []domain.Route {
	assignments := source.service.List(routedomain.TargetTunnel)
	result := make([]domain.Route, 0, len(assignments))
	for _, item := range assignments {
		if item.Enabled && source.publishable(item.ProviderID) {
			result = append(result, toRoute(item))
		}
	}
	return result
}
func (source *Source) Resolve(model string) (domain.Route, bool) {
	assignment, ok := source.service.Resolve(routedomain.TargetTunnel, model)
	if !ok || !source.publishable(assignment.ProviderID) {
		return domain.Route{}, false
	}
	return toRoute(assignment), true
}
func (source *Source) publishable(providerID string) bool {
	return source.policy == nil || source.policy.Publishable(providerID)
}
func toRoute(item routedomain.Assignment) domain.Route {
	return domain.Route{PublicModel: item.PublicModel, UpstreamModel: item.UpstreamModel, ProviderID: item.ProviderID, ContextLimitKiB: item.ContextLimitKiB}
}
