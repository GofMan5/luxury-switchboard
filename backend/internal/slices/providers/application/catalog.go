package application

import (
	"errors"
	"sync"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

var ErrProviderUnavailable = errors.New("provider is unavailable")

type Catalog struct {
	mu        sync.RWMutex
	providers map[string]domain.Provider
	order     []string
	activeID  string
	listeners []func(string)
}

func NewCatalog(providers []domain.Provider, activeID string) (*Catalog, error) {
	catalog := &Catalog{providers: make(map[string]domain.Provider)}
	for _, provider := range providers {
		if _, duplicate := catalog.providers[provider.ID]; duplicate {
			return nil, errors.New("duplicate provider id")
		}
		catalog.providers[provider.ID] = provider
		catalog.order = append(catalog.order, provider.ID)
	}
	active, exists := catalog.providers[activeID]
	if !exists || !active.Enabled {
		return nil, ErrProviderUnavailable
	}
	catalog.activeID = activeID
	return catalog, nil
}

func (catalog *Catalog) List() []domain.Provider {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	providers := make([]domain.Provider, 0, len(catalog.order))
	for _, id := range catalog.order {
		providers = append(providers, catalog.providers[id])
	}
	return providers
}

func (catalog *Catalog) Active() (domain.Provider, error) {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	provider, exists := catalog.providers[catalog.activeID]
	if !exists || !provider.Enabled {
		return domain.Provider{}, ErrProviderUnavailable
	}
	return provider, nil
}

func (catalog *Catalog) Get(id string) (domain.Provider, bool) {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	provider, exists := catalog.providers[id]
	return provider, exists && provider.Enabled
}

func (catalog *Catalog) Activate(id string) (domain.Provider, error) {
	catalog.mu.Lock()
	provider, exists := catalog.providers[id]
	if !exists || !provider.Enabled {
		catalog.mu.Unlock()
		return domain.Provider{}, ErrProviderUnavailable
	}
	changed := catalog.activeID != id
	catalog.activeID = id
	listeners := append([]func(string){}, catalog.listeners...)
	catalog.mu.Unlock()
	if changed {
		for _, listener := range listeners {
			listener(id)
		}
	}
	return provider, nil
}

func (catalog *Catalog) Replace(providers []domain.Provider, activeID string) error {
	next := make(map[string]domain.Provider, len(providers))
	order := make([]string, 0, len(providers))
	for _, provider := range providers {
		if _, duplicate := next[provider.ID]; duplicate {
			return errors.New("duplicate provider id")
		}
		next[provider.ID] = provider
		order = append(order, provider.ID)
	}
	active, exists := next[activeID]
	if !exists || !active.Enabled {
		return ErrProviderUnavailable
	}
	catalog.mu.Lock()
	previous, hadPrevious := catalog.providers[catalog.activeID]
	routeChanged := catalog.activeID != activeID || !hadPrevious || !sameRelayProvider(previous, active)
	catalog.providers = next
	catalog.order = order
	catalog.activeID = activeID
	listeners := append([]func(string){}, catalog.listeners...)
	catalog.mu.Unlock()
	if routeChanged {
		for _, listener := range listeners {
			listener(activeID)
		}
	}
	return nil
}

func sameRelayProvider(left, right domain.Provider) bool {
	return left.ID == right.ID && left.BaseURL.String() == right.BaseURL.String() &&
		left.AuthMode == right.AuthMode && left.AuthHeader == right.AuthHeader &&
		left.Dialect == right.Dialect && left.CacheTTL == right.CacheTTL &&
		left.ImageCompat == right.ImageCompat && left.Enabled == right.Enabled
}

func (catalog *Catalog) OnActivated(listener func(string)) {
	if listener == nil {
		return
	}
	catalog.mu.Lock()
	catalog.listeners = append(catalog.listeners, listener)
	catalog.mu.Unlock()
}
