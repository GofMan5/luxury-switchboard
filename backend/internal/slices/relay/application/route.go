package application

import (
	"context"
	"net/url"
	"time"
)

type Route struct {
	ProviderID    string
	ProviderName  string
	BaseURL       *url.URL
	AuthMode      string
	AuthHeader    string
	Dialect       string
	ImageCompat   bool
	UpstreamModel string
	Format        string
	ChatPath      string
	CacheTTL      time.Duration
}

type RouteSource interface {
	Current(context.Context, string) (Route, error)
	Pinned(context.Context, string, string) (Route, error)
}

type ModelRoute struct {
	ProviderID    string
	UpstreamModel string
}

type ModelRouteResolver interface {
	Relay(string) (ModelRoute, bool, error)
}

// FailoverRoutes is the routing half of a failover chain: the ordered siblings
// behind a public model, and the parking of a provider that just answered
// terminally. The relay drives it; the routes slice owns the order and TTL.
type FailoverRoutes interface {
	Chain(publicModel string) []ModelRoute
	Degrade(providerID string)
}

// FailoverSource answers the next concrete route after the current provider
// answered terminally. It is the providers-side view of the chain: entries
// whose provider no longer exists or is disabled are skipped here, so the
// relay never has to know the catalog.
type FailoverSource interface {
	Next(ctx context.Context, currentProviderID, publicModel string) (Route, bool, error)
	Degrade(providerID string)
}
