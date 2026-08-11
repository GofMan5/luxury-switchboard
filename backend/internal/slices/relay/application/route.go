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
