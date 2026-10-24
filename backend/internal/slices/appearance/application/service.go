package application

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/appearance/domain"
)

// ErrStoreUnavailable names a write refused because the appearance file could
// not be read: saving over an unreadable file would replace the owner's
// choices with whatever this one update carries. Settings, routes and the key
// pool follow the same rule.
var ErrStoreUnavailable = errors.New("appearance storage could not be read; saving now would replace it")

type Repository interface {
	Load(context.Context) (domain.Appearance, bool, error)
	Save(context.Context, domain.Appearance) error
}

type Service struct {
	opMu       sync.Mutex
	mu         sync.RWMutex
	repository Repository
	appearance domain.Appearance
	loadErr    error
	// changed hears each saved appearance under opMu, so notifications
	// arrive in update order and the last one is always the current state.
	changed func(domain.Appearance)
}

func NewService(repository Repository) (*Service, error) {
	if repository == nil {
		return nil, errors.New("appearance repository is required")
	}
	return &Service{repository: repository, appearance: domain.Defaults()}, nil
}

// OnChanged sets the listener told each saved appearance. Set before serving.
func (service *Service) OnChanged(listener func(domain.Appearance)) {
	service.opMu.Lock()
	service.changed = listener
	service.opMu.Unlock()
}

// Load restores the saved appearance. A failure is remembered: the service
// keeps answering with defaults, and refuses to save until a restart reads the
// file successfully.
func (service *Service) Load(ctx context.Context) error {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	appearance, found, err := service.repository.Load(ctx)
	if err == nil && found {
		appearance = appearance.Normalized()
		if invalid := appearance.Validate(); invalid != nil {
			err = fmt.Errorf("stored appearance is invalid: %w", invalid)
		}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.loadErr = err
	if err == nil && found {
		service.appearance = appearance
	}
	return err
}

// Snapshot returns a copy the caller may keep: the token map is never shared.
func (service *Service) Snapshot() domain.Appearance {
	service.mu.RLock()
	defer service.mu.RUnlock()
	snapshot := service.appearance
	snapshot.Tokens = maps.Clone(snapshot.Tokens)
	return snapshot
}

// Update normalizes, validates and persists appearance, and only then makes it
// current. Validation failures are domain.ValidationError.
func (service *Service) Update(ctx context.Context, appearance domain.Appearance) (domain.Appearance, error) {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	appearance = appearance.Normalized()
	if err := appearance.Validate(); err != nil {
		return domain.Appearance{}, err
	}
	service.mu.RLock()
	loadErr := service.loadErr
	service.mu.RUnlock()
	if loadErr != nil {
		return domain.Appearance{}, ErrStoreUnavailable
	}
	if err := service.repository.Save(ctx, appearance); err != nil {
		return domain.Appearance{}, fmt.Errorf("appearance could not be saved: %w", err)
	}
	service.mu.Lock()
	service.appearance = appearance
	service.mu.Unlock()
	if service.changed != nil {
		notified := appearance
		notified.Tokens = maps.Clone(appearance.Tokens)
		service.changed(notified)
	}
	result := appearance
	result.Tokens = maps.Clone(appearance.Tokens)
	return result, nil
}
