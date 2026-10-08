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
	// ExtraHeaders ride along on every upstream request for this route. They
	// are the provider profile's identity, not credentials: a preset source
	// sets originator/User-Agent style headers the client form is not allowed
	// to express. Applied with Set semantics after the auth switch, so they
	// override whatever the client sent for the same names.
	ExtraHeaders map[string]string
	// ResponsesPath is the path a provider answers the Responses API on when
	// the client's own URL cannot be trusted to name it. Empty means the
	// route keeps using the client's path (joined onto the base URL).
	ResponsesPath string
	// ResponsesStateless marks an upstream that serves the Responses dialect
	// only stateless: every request carries the codex wire contract —
	// forced streaming, nothing stored server-side, a per-request session
	// id — so the relay buffers the terminal and folds the stream back into
	// whatever shape the client asked for.
	ResponsesStateless bool
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
