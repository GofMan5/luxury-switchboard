package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

var (
	ErrBuiltinProvider   = errors.New("builtin provider cannot be deleted")
	ErrActiveProvider    = errors.New("active provider cannot be deleted")
	ErrProviderHasKeys   = errors.New("provider still has keys")
	ErrProviderHasRoutes = errors.New("provider still has model routes")
)

type KeyPool interface {
	// EnsureProvider registers the provider's request budget: the limit and the
	// window it is counted over.
	EnsureProvider(providerID string, rpm int, window time.Duration) error
	RemoveProvider(string) error
}

type RouteUsage interface {
	LockProvider(string) (bool, func())
}

type Manager struct {
	opMu       sync.Mutex
	catalog    *Catalog
	repository Repository
	keys       KeyPool
	routes     RouteUsage
	loadMu     sync.RWMutex
	loadErr    error
}

func (manager *Manager) SetRouteUsage(routes RouteUsage) {
	manager.opMu.Lock()
	manager.routes = routes
	manager.opMu.Unlock()
}

func NewManager(catalog *Catalog, repository Repository, keys KeyPool) (*Manager, error) {
	if catalog == nil || repository == nil || keys == nil {
		return nil, errors.New("provider manager dependencies are invalid")
	}
	return &Manager{catalog: catalog, repository: repository, keys: keys}, nil
}

func (manager *Manager) Load(ctx context.Context) error {
	err := manager.load(ctx)
	manager.loadMu.Lock()
	manager.loadErr = err
	manager.loadMu.Unlock()
	return err
}

func (manager *Manager) load(ctx context.Context) error {
	state, err := manager.repository.Load(ctx)
	if err != nil {
		return err
	}
	if len(state.Providers) == 0 {
		return nil
	}
	if _, err := NewCatalog(state.Providers, state.ActiveID); err != nil {
		return err
	}
	for _, provider := range state.Providers {
		if err := manager.keys.EnsureProvider(provider.ID, provider.RPM, provider.RateWindow()); err != nil {
			return err
		}
	}
	return manager.catalog.Replace(state.Providers, state.ActiveID)
}

// Availability reports why the persisted provider state could not be read, or
// nil when it could. A write must refuse while this is set: saving runtime
// defaults over a file that merely failed to load atomically replaces it, and
// every provider definition the user ever entered is gone after the next
// start. Routes made the same call first; this is the same rule.
func (manager *Manager) Availability() error {
	manager.loadMu.RLock()
	defer manager.loadMu.RUnlock()
	return manager.loadErr
}

// ErrStoreUnavailable names a write refused because the store behind it could
// not be read: the caller's own message should say what to unlock rather than
// blaming the edit.
var ErrStoreUnavailable = errors.New("provider storage could not be read; saving now would replace it")

func (manager *Manager) Add(ctx context.Context, params domain.Params) (domain.Provider, error) {
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	if err := manager.Availability(); err != nil {
		return domain.Provider{}, ErrStoreUnavailable
	}
	id, err := randomProviderID()
	if err != nil {
		return domain.Provider{}, err
	}
	params.ID = id
	params.Builtin = false
	provider, err := domain.New(params)
	if err != nil {
		return domain.Provider{}, err
	}
	active, err := manager.catalog.Active()
	if err != nil {
		return domain.Provider{}, err
	}
	providers := append(manager.catalog.List(), provider)
	if err := manager.keys.EnsureProvider(provider.ID, provider.RPM, provider.RateWindow()); err != nil {
		return domain.Provider{}, err
	}
	if err := manager.repository.Save(ctx, SavedState{Providers: providers, ActiveID: active.ID}); err != nil {
		_ = manager.keys.RemoveProvider(provider.ID)
		return domain.Provider{}, fmt.Errorf("provider settings could not be saved: %w", err)
	}
	if err := manager.catalog.Replace(providers, active.ID); err != nil {
		return domain.Provider{}, err
	}
	return provider, nil
}

func (manager *Manager) Update(ctx context.Context, id string, params domain.Params) (domain.Provider, error) {
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	if err := manager.Availability(); err != nil {
		return domain.Provider{}, ErrStoreUnavailable
	}
	providers := manager.catalog.List()
	index := slices.IndexFunc(providers, func(provider domain.Provider) bool { return provider.ID == id })
	if index < 0 {
		return domain.Provider{}, ErrProviderUnavailable
	}
	current := providers[index]
	params.ID = id
	params.Builtin = current.Builtin
	updated, err := domain.New(params)
	if err != nil {
		return domain.Provider{}, err
	}
	active, err := manager.catalog.Active()
	if err != nil {
		return domain.Provider{}, err
	}
	if active.ID == id && !updated.Enabled {
		return domain.Provider{}, ErrActiveProvider
	}
	providers[index] = updated
	if err := manager.keys.EnsureProvider(id, updated.RPM, updated.RateWindow()); err != nil {
		return domain.Provider{}, err
	}
	if err := manager.repository.Save(ctx, SavedState{Providers: providers, ActiveID: active.ID}); err != nil {
		_ = manager.keys.EnsureProvider(id, current.RPM, current.RateWindow())
		return domain.Provider{}, fmt.Errorf("provider settings could not be saved: %w", err)
	}
	if err := manager.catalog.Replace(providers, active.ID); err != nil {
		return domain.Provider{}, err
	}
	return updated, nil
}

func (manager *Manager) Delete(ctx context.Context, id string) error {
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	if err := manager.Availability(); err != nil {
		return ErrStoreUnavailable
	}
	providers := manager.catalog.List()
	index := slices.IndexFunc(providers, func(provider domain.Provider) bool { return provider.ID == id })
	if index < 0 {
		return ErrProviderUnavailable
	}
	if providers[index].Builtin {
		return ErrBuiltinProvider
	}
	active, err := manager.catalog.Active()
	if err != nil {
		return err
	}
	if active.ID == id {
		return ErrActiveProvider
	}
	if manager.routes != nil {
		referenced, unlock := manager.routes.LockProvider(id)
		defer unlock()
		if referenced {
			return ErrProviderHasRoutes
		}
	}
	removed := providers[index]
	if err := manager.keys.RemoveProvider(id); err != nil {
		return ErrProviderHasKeys
	}
	providers = append(providers[:index], providers[index+1:]...)
	if err := manager.repository.Save(ctx, SavedState{Providers: providers, ActiveID: active.ID}); err != nil {
		_ = manager.keys.EnsureProvider(id, removed.RPM, removed.RateWindow())
		return fmt.Errorf("provider settings could not be saved: %w", err)
	}
	return manager.catalog.Replace(providers, active.ID)
}

func (manager *Manager) Activate(ctx context.Context, id string) (domain.Provider, error) {
	manager.opMu.Lock()
	defer manager.opMu.Unlock()
	if err := manager.Availability(); err != nil {
		return domain.Provider{}, ErrStoreUnavailable
	}
	providers := manager.catalog.List()
	provider, exists := providerByID(providers, id)
	if !exists || !provider.Enabled {
		return domain.Provider{}, ErrProviderUnavailable
	}
	if err := manager.repository.Save(ctx, SavedState{Providers: providers, ActiveID: id}); err != nil {
		return domain.Provider{}, fmt.Errorf("provider settings could not be saved: %w", err)
	}
	return manager.catalog.Activate(id)
}

func providerByID(providers []domain.Provider, id string) (domain.Provider, bool) {
	for _, provider := range providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return domain.Provider{}, false
}

func randomProviderID() (string, error) {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		return "", errors.New("secure random source unavailable")
	}
	return "provider_" + hex.EncodeToString(value), nil
}
