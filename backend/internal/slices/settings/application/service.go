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
	mu         sync.RWMutex
	repository Repository
	settings   domain.Settings
	listeners  []func(domain.Settings)
}

type UpdateResult struct {
	Settings        domain.Settings `json:"settings"`
	RestartRequired bool            `json:"restartRequired"`
}

func NewService(repository Repository) (*Service, error) {
	if repository == nil {
		return nil, errors.New("settings repository is required")
	}
	return &Service{repository: repository, settings: domain.Defaults()}, nil
}

func (service *Service) Load(ctx context.Context) error {
	settings, found, err := service.repository.Load(ctx)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	service.mu.Lock()
	service.settings = settings
	service.mu.Unlock()
	return nil
}

func (service *Service) Snapshot() domain.Settings {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.settings
}

func (service *Service) Update(ctx context.Context, settings domain.Settings) (UpdateResult, error) {
	if err := settings.Validate(); err != nil {
		return UpdateResult{}, err
	}
	previous := service.Snapshot()
	if err := service.repository.Save(ctx, settings); err != nil {
		return UpdateResult{}, errors.New("settings could not be saved")
	}
	service.mu.Lock()
	service.settings = settings
	listeners := append([]func(domain.Settings){}, service.listeners...)
	service.mu.Unlock()
	for _, listener := range listeners {
		listener(settings)
	}
	return UpdateResult{Settings: settings, RestartRequired: previous != settings}, nil
}

func (service *Service) OnChanged(listener func(domain.Settings)) {
	if listener == nil {
		return
	}
	service.mu.Lock()
	service.listeners = append(service.listeners, listener)
	service.mu.Unlock()
}
