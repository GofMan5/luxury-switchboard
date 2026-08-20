package application

import (
	"context"
	"errors"
	"sync"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/settings/domain"
)

type Repository interface {
	Load(context.Context) (domain.Settings, bool, error)
	Save(context.Context, domain.Settings) error
}

type Service struct {
	opMu       sync.Mutex
	mu         sync.RWMutex
	repository Repository
	settings   domain.Settings
	applied    domain.Settings

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
	return &Service{repository: repository, settings: defaults, applied: defaults}, nil
}

func (service *Service) Load(ctx context.Context) error {
	settings, found, err := service.repository.Load(ctx)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	// Settings written before a field existed carry a zero value for it. Filling
	// those in before validating keeps an older file loadable instead of discarding
	// every setting the user had configured.
	settings = settings.Normalized()
	if err := settings.Validate(); err != nil {
		return err
	}
	service.mu.Lock()
	service.settings = settings
	service.applied = settings
	service.mu.Unlock()
	return nil
}

func (service *Service) Snapshot() domain.Settings {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.settings
}

func (service *Service) Update(ctx context.Context, settings domain.Settings) (UpdateResult, error) {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	settings = settings.Normalized()
	if err := settings.Validate(); err != nil {
		return UpdateResult{}, err
	}
	if err := service.repository.Save(ctx, settings); err != nil {
		return UpdateResult{}, errors.New("settings could not be saved")
	}
	service.mu.Lock()
	service.settings = settings
	restartRequired := service.applied.RequiresRestart(settings)
	service.mu.Unlock()
	service.notifyApplied(settings)
	return UpdateResult{Settings: settings, RestartRequired: restartRequired}, nil
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
