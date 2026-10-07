package live

import (
	"context"
	"time"

	analyticsdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/domain"
	backupdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/backup/domain"
	keypoolapp "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/application"
	keypooldomain "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
	providerapp "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	providerdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
	routeapp "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/application"
	routedomain "github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
)

// Sources reads the live configuration: the encrypted stores each slice
// already owns, decrypted here only long enough to write the plain backup.
type Sources struct {
	providers providerdpapi
	keys      keydpapi
	routes    routedpapi
	prices    priceCatalog
}

type providerdpapi interface {
	Load(context.Context) (providerapp.SavedState, error)
}
type keydpapi interface {
	Load(context.Context) ([]keypooldomain.Key, error)
}
type routedpapi interface {
	Load(context.Context) ([]routedomain.Assignment, error)
}

// priceCatalog is the analytics price store: plain JSON, not a secret, but it
// travels with the backup so a restore does not leave the cost estimate
// blind on a new machine.
type priceCatalog interface {
	Load(context.Context) (analyticsdomain.Catalog, error)
}

func NewSources(providers providerdpapi, keys keydpapi, routes routedpapi, prices priceCatalog) *Sources {
	return &Sources{providers: providers, keys: keys, routes: routes, prices: prices}
}

func (sources *Sources) Providers() ([]backupdomain.ProviderEntry, error) {
	state, err := sources.providers.Load(context.Background())
	if err != nil {
		return nil, err
	}
	entries := make([]backupdomain.ProviderEntry, 0, len(state.Providers))
	for _, provider := range state.Providers {
		if provider.Preset != "" {
			// A preset entry is provisioned by its own slice: the codex
			// provisioner owns the row end-to-end and rebuilds it on the
			// next sign-in, and the backup document has no preset field to
			// carry the marker anyway — exporting it would freeze a
			// hand-built stranger a restore would then refuse to relink.
			// Only operator-managed providers travel.
			continue
		}
		entries = append(entries, backupdomain.ProviderEntry{
			ID: provider.ID, Name: provider.Name, BaseURL: provider.BaseURL.String(),
			AuthMode: string(provider.AuthMode), AuthHeader: provider.AuthHeader,
			Dialect: string(provider.Dialect), Format: string(provider.Format),
			ChatPath: provider.ChatPath, ModelsPath: provider.ModelsPath,
			ImageCompat: provider.ImageCompat, RPM: provider.RPM,
			CacheTTL: provider.CacheTTL.String(), Enabled: provider.Enabled,
		})
	}
	return entries, nil
}

func (sources *Sources) Keys() ([]backupdomain.KeyEntry, error) {
	keys, err := sources.keys.Load(context.Background())
	if err != nil {
		return nil, err
	}
	entries := make([]backupdomain.KeyEntry, 0, len(keys))
	for _, key := range keys {
		if key.Pinned {
			// A pinned key is the environment credential of a builtin
			// provider: it is not the operator's to carry between machines.
			continue
		}
		entries = append(entries, backupdomain.KeyEntry{
			ProviderID: key.ProviderID, Label: key.Label, Secret: key.Credential.Reveal(),
			RPM: key.RPM, Priority: key.Priority, ProxyURL: key.ProxyURL,
		})
	}
	return entries, nil
}

func (sources *Sources) Routes() ([]backupdomain.RouteEntry, error) {
	assignments, err := sources.routes.Load(context.Background())
	if err != nil {
		return nil, err
	}
	entries := make([]backupdomain.RouteEntry, 0, len(assignments))
	for _, assignment := range assignments {
		entries = append(entries, backupdomain.RouteEntry{
			Target: string(assignment.Target), PublicModel: assignment.PublicModel,
			UpstreamModel: assignment.UpstreamModel, ProviderID: assignment.ProviderID,
			ContextLimitKiB: assignment.ContextLimitKiB, Aliases: assignment.Aliases,
			Enabled: assignment.Enabled, Priority: assignment.Priority,
		})
	}
	return entries, nil
}

func (sources *Sources) Prices() ([]backupdomain.PriceEntry, error) {
	catalog, err := sources.prices.Load(context.Background())
	if err != nil {
		return nil, err
	}
	entries := make([]backupdomain.PriceEntry, 0, len(catalog.Prices))
	// Models() is the stable order, so two exports of one setup differ only
	// by the timestamp.
	for _, model := range catalog.Models() {
		price := catalog.Prices[model]
		entries = append(entries, backupdomain.PriceEntry{
			Model: price.Model, Input: price.Input, CachedInput: price.CachedInput,
			Output: price.Output, Reasoning: price.Reasoning,
		})
	}
	return entries, nil
}

// Sinks restores through each slice's own manager, so validation and
// persistence are the same code paths a hand-typed entry takes.
type Sinks struct {
	providers *providerapp.Manager
	keys      *keypoolapp.Manager
	routes    *routeapp.Service
	catalog   *providerapp.Catalog
	prices    priceSetter
}

type priceSetter interface {
	SetPrice(context.Context, analyticsdomain.Price) (analyticsdomain.Catalog, error)
}

func NewSinks(providers *providerapp.Manager, keys *keypoolapp.Manager, routes *routeapp.Service, catalog *providerapp.Catalog, prices priceSetter) *Sinks {
	return &Sinks{providers: providers, keys: keys, routes: routes, catalog: catalog, prices: prices}
}

func (sinks *Sinks) ProviderExists(id string) bool {
	_, exists := sinks.catalog.Get(id)
	return exists
}

func (sinks *Sinks) AddProvider(entry backupdomain.ProviderEntry) (bool, error) {
	if sinks.ProviderExists(entry.ID) {
		return false, nil
	}
	cacheTTL, err := time.ParseDuration(entry.CacheTTL)
	if err != nil {
		cacheTTL = 0
	}
	if _, err := sinks.providers.Add(context.Background(), providerdomain.Params{
		ID: entry.ID, Name: entry.Name, BaseURL: entry.BaseURL,
		AuthMode: providerdomain.AuthMode(entry.AuthMode), AuthHeader: entry.AuthHeader,
		Dialect: providerdomain.Dialect(entry.Dialect), Format: providerdomain.APIFormat(entry.Format),
		ChatPath: entry.ChatPath, ModelsPath: entry.ModelsPath,
		ImageCompat: entry.ImageCompat, RPM: entry.RPM, CacheTTL: cacheTTL,
		Enabled: entry.Enabled,
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (sinks *Sinks) AddKey(entry backupdomain.KeyEntry) (bool, error) {
	_, err := sinks.keys.Add(context.Background(), keypooldomain.Params{
		ProviderID: entry.ProviderID, Label: entry.Label, Secret: entry.Secret,
		RPM: entry.RPM, ProxyURL: entry.ProxyURL,
	})
	if err != nil {
		if err == keypoolapp.ErrDuplicateKey {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (sinks *Sinks) UpsertRoute(entry backupdomain.RouteEntry) (bool, error) {
	if err := sinks.routes.Upsert(context.Background(), routedomain.Assignment{
		Target: routedomain.Target(entry.Target), PublicModel: entry.PublicModel,
		UpstreamModel: entry.UpstreamModel, ProviderID: entry.ProviderID,
		ContextLimitKiB: entry.ContextLimitKiB, Aliases: entry.Aliases,
		Enabled: entry.Enabled, Priority: entry.Priority,
	}); err != nil {
		return false, err
	}
	return true, nil
}

// RestorePrice lands the entry through the analytics service, so the same
// validation a hand-typed rate takes refuses an impossible backup here too.
func (sinks *Sinks) RestorePrice(entry backupdomain.PriceEntry) error {
	_, err := sinks.prices.SetPrice(context.Background(), analyticsdomain.Price{
		Model: entry.Model, Input: entry.Input, CachedInput: entry.CachedInput,
		Output: entry.Output, Reasoning: entry.Reasoning,
	})
	return err
}
