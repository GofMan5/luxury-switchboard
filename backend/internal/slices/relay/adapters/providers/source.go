package providers

import (
	"context"
	"runtime"
	"sync/atomic"

	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

type Source struct {
	catalog   *providerapp.Catalog
	routes    relayapp.ModelRouteResolver
	failovers relayapp.FailoverRoutes
	// appVersion stamps the preset identity headers (User-Agent carries the
	// product version). Stored atomically because bootstrap sets it while the
	// relay is already wired; requests read it without a lock.
	appVersion atomic.Value // string
	// failoverEnabled gates the chain: off means terminal verdicts end the
	// request where they happened. Applied live from settings.
	failoverEnabled atomic.Bool
}

func NewSource(catalog *providerapp.Catalog, routes relayapp.ModelRouteResolver, failovers relayapp.FailoverRoutes) *Source {
	source := &Source{catalog: catalog, routes: routes, failovers: failovers}
	source.failoverEnabled.Store(true)
	return source
}

// SetAppVersion carries the build version into preset identity headers. It is
// set once by bootstrap from the same constant the system handshake reports.
func (source *Source) SetAppVersion(version string) { source.appVersion.Store(version) }

func (source *Source) appVersionString() string {
	if version, ok := source.appVersion.Load().(string); ok && version != "" {
		return version
	}
	return "dev"
}

// SetFailoverEnabled gates the chain live: requests in flight keep the route
// they resolved with.
func (source *Source) SetFailoverEnabled(enabled bool) { source.failoverEnabled.Store(enabled) }

func (source *Source) Current(_ context.Context, model string) (relayapp.Route, error) {
	provider, err := source.catalog.Active()
	upstreamModel := model
	if source.routes != nil {
		assignment, ok, routeErr := source.routes.Relay(model)
		if routeErr != nil {
			return relayapp.Route{}, routeErr
		}
		if ok {
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
	return source.providerRoute(provider, upstreamModel), nil
}

func (source *Source) Pinned(_ context.Context, providerID, upstreamModel string) (relayapp.Route, error) {
	provider, exists := source.catalog.Get(providerID)
	if !exists {
		return relayapp.Route{}, providerapp.ErrProviderUnavailable
	}
	return source.providerRoute(provider, upstreamModel), nil
}

// Next walks the failover chain past the provider that just refused. Entries
// whose provider is gone or disabled are skipped here rather than at the
// routes slice: the catalog is this adapter's own domain. With the chain
// gated off it reports a clean miss, which the relay reads as "the verdict is
// final" — the strict routing an operator asked for.
func (source *Source) Next(_ context.Context, currentProviderID, publicModel string) (relayapp.Route, bool, error) {
	if source.failovers == nil || publicModel == "" || !source.failoverEnabled.Load() {
		return relayapp.Route{}, false, nil
	}
	for _, candidate := range source.failovers.Chain(publicModel) {
		if candidate.ProviderID == currentProviderID {
			continue
		}
		provider, exists := source.catalog.Get(candidate.ProviderID)
		if !exists || !provider.Enabled {
			continue
		}
		return source.providerRoute(provider, candidate.UpstreamModel), true, nil
	}
	return relayapp.Route{}, false, nil
}

func (source *Source) Degrade(providerID string) {
	if source.failovers != nil {
		source.failovers.Degrade(providerID)
	}
}

func (source *Source) providerRoute(provider providerdomain.Provider, upstreamModel string) relayapp.Route {
	route := relayapp.Route{
		ProviderID: provider.ID, ProviderName: provider.Name, BaseURL: provider.BaseURL,
		AuthMode: string(provider.AuthMode), AuthHeader: provider.AuthHeader, Dialect: string(provider.Dialect),
		ImageCompat: provider.ImageCompat, UpstreamModel: upstreamModel, CacheTTL: provider.CacheTTL,
		Format: string(provider.Format), ChatPath: provider.ChatPath,
	}
	switch provider.Preset {
	case providerdomain.PresetCodex:
		// The codex preset owns its identity on the wire: a User-Agent that
		// names the product build and an originator the upstream recognizes.
		// These are profile headers, not credentials — the form cannot express
		// them (the editor forbids a custom User-Agent), so the route carries
		// them and the HTTP adapter applies them after the auth switch.
		route.ExtraHeaders = map[string]string{
			"User-Agent": "Codex Desktop/" + source.appVersionString() + " (" + runtime.GOOS + "; " + runtime.GOARCH + ")",
			"originator": "Codex Desktop",
		}
		if provider.AccountID != "" {
			route.ExtraHeaders["Chatgpt-Account-Id"] = string(provider.AccountID)
		}
		// The Responses API of this upstream lives at a fixed path regardless
		// of how the client spells the dialect (/v1/responses or /responses),
		// and answers only stateless: forced streaming, no server-side state.
		route.ResponsesPath = "/responses"
		route.ResponsesStateless = true
	}
	return route
}
