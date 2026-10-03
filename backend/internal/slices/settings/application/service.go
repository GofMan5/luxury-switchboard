package application

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/settings/domain"
)

// ErrStoreUnavailable names a write refused because the settings file could
// not be read: saving defaults over an unreadable file would replace it, and
// the user's whole configuration is gone after the next start. Routes,
// providers and the key pool made the same call first; this is the same rule.
var ErrStoreUnavailable = errors.New("settings storage could not be read; saving now would replace it")

type Repository interface {
	Load(context.Context) (domain.Settings, bool, error)
	Save(context.Context, domain.Settings) error
}

type Service struct {
	opMu       sync.Mutex
	mu         sync.RWMutex
	repository Repository
	settings   domain.Settings
	loadMu     sync.RWMutex
	loadErr    error

	listenersMu sync.RWMutex
	listeners   []func(domain.Settings)
}

type UpdateResult struct {
	Settings        domain.Settings `json:"settings"`
	RestartRequired bool            `json:"restartRequired"`
}

func NewService(repository Repository) (*Service, error) {
	if repository == nil {
		return nil, errors.New("settings repository is required")
	}
	defaults := domain.Defaults()
	return &Service{repository: repository, settings: defaults}, nil
}

func (service *Service) Load(ctx context.Context) error {
	settings, found, err := service.repository.Load(ctx)
	if err == nil && found {
		// Settings written before a field existed carry a zero value for it. Filling
		// those in before validating keeps an older file loadable instead of discarding
		// every setting the user had configured.
		settings = settings.Normalized()
		if invalid := settings.Validate(); invalid != nil {
			err = fmt.Errorf("stored settings are invalid: %w", invalid)
		}
	}
	service.loadMu.Lock()
	service.loadErr = err
	service.loadMu.Unlock()
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	service.mu.Lock()
	service.settings = settings
	service.mu.Unlock()
	return nil
}

// Availability reports why the settings file could not be read, or nil when it
// could. A save refuses while it is set.
func (service *Service) Availability() error {
	service.loadMu.RLock()
	defer service.loadMu.RUnlock()
	return service.loadErr
}

func (service *Service) Snapshot() domain.Settings {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.settings
}

func (service *Service) Update(ctx context.Context, settings domain.Settings) (UpdateResult, error) {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	if err := service.Availability(); err != nil {
		return UpdateResult{}, ErrStoreUnavailable
	}
	settings = settings.Normalized()
	if err := settings.Validate(); err != nil {
		return UpdateResult{}, err
	}
	if err := service.repository.Save(ctx, settings); err != nil {
		return UpdateResult{}, fmt.Errorf("settings could not be saved: %w", err)
	}
	service.mu.Lock()
	service.settings = settings
	service.mu.Unlock()
	// Every setting applies live — the listeners reconfigure the relay, the
	// queues, the stores and the probe in place. Nothing asks for a restart.
	service.notifyApplied(settings)
	return UpdateResult{Settings: settings, RestartRequired: false}, nil
}

// OnApplied registers a listener for settings that take effect without a restart.
func (service *Service) OnApplied(listener func(domain.Settings)) {
	if listener == nil {
		return
	}
	service.listenersMu.Lock()
	service.listeners = append(service.listeners, listener)
	service.listenersMu.Unlock()
}

func (service *Service) notifyApplied(settings domain.Settings) {
	service.listenersMu.RLock()
	listeners := service.listeners
	service.listenersMu.RUnlock()
	for _, listener := range listeners {
		listener(settings)
	}
}
