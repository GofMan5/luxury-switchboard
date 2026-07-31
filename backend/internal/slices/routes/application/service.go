package application

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
)

var ErrProviderUnavailable = errors.New("route provider is unavailable")

type Repository interface {
	Load(context.Context) ([]domain.Assignment, error)
	Save(context.Context, []domain.Assignment) error
}

type ProviderCatalog interface{ Exists(string) bool }

type Service struct {
	opMu        sync.Mutex
	mu          sync.RWMutex
	repository  Repository
	providers   ProviderCatalog
	assignments []domain.Assignment
	listeners   []func(domain.Target)
}

func NewService(repository Repository, providers ProviderCatalog) (*Service, error) {
	if repository == nil || providers == nil {
		return nil, errors.New("route dependencies are invalid")
	}
	return &Service{repository: repository, providers: providers}, nil
}

func (service *Service) Load(ctx context.Context) error {
	assignments, err := service.repository.Load(ctx)
	if err != nil {
		return err
	}
	for _, assignment := range assignments {
		if assignment.Validate() != nil {
			return errors.New("saved routes are invalid")
		}
	}
	service.mu.Lock()
	service.assignments = slices.Clone(assignments)
	service.mu.Unlock()
	return nil
}

func (service *Service) List(target domain.Target) []domain.Assignment {
	service.mu.RLock()
	defer service.mu.RUnlock()
	result := make([]domain.Assignment, 0)
	for _, assignment := range service.assignments {
		if assignment.Target == target {
			result = append(result, assignment)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PublicModel < result[j].PublicModel })
	return result
}

func (service *Service) Resolve(target domain.Target, model string) (domain.Assignment, bool) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	for _, assignment := range service.assignments {
		if assignment.Target == target && assignment.Enabled && assignment.PublicModel == model {
			return assignment, true
		}
	}
	return domain.Assignment{}, false
}

func (service *Service) Upsert(ctx context.Context, assignment domain.Assignment) error {
	if err := assignment.Validate(); err != nil {
		return err
	}
	if !service.providers.Exists(assignment.ProviderID) {
		return ErrProviderUnavailable
	}
	service.opMu.Lock()
	defer service.opMu.Unlock()
	service.mu.RLock()
	candidate := slices.Clone(service.assignments)
	service.mu.RUnlock()
	index := slices.IndexFunc(candidate, func(item domain.Assignment) bool {
		return item.Target == assignment.Target && item.PublicModel == assignment.PublicModel
	})
	if index < 0 {
		candidate = append(candidate, assignment)
	} else {
		candidate[index] = assignment
	}
	return service.persist(ctx, candidate, assignment.Target)
}

func (service *Service) UpsertMany(ctx context.Context, assignments []domain.Assignment) error {
	if len(assignments) == 0 || len(assignments) > 500 {
		return errors.New("invalid route batch")
	}
	target := assignments[0].Target
	for _, assignment := range assignments {
		if assignment.Target != target || assignment.Validate() != nil || !service.providers.Exists(assignment.ProviderID) {
			return errors.New("invalid route batch")
		}
	}
	service.opMu.Lock()
	defer service.opMu.Unlock()
	service.mu.RLock()
	candidate := slices.Clone(service.assignments)
	service.mu.RUnlock()
	for _, assignment := range assignments {
		index := slices.IndexFunc(candidate, func(item domain.Assignment) bool {
			return item.Target == assignment.Target && item.PublicModel == assignment.PublicModel
		})
		if index < 0 {
			candidate = append(candidate, assignment)
		} else {
			candidate[index] = assignment
		}
	}
	return service.persist(ctx, candidate, target)
}

func (service *Service) Delete(ctx context.Context, target domain.Target, publicModel string) error {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	service.mu.RLock()
	candidate := slices.Clone(service.assignments)
	service.mu.RUnlock()
	index := slices.IndexFunc(candidate, func(item domain.Assignment) bool { return item.Target == target && item.PublicModel == publicModel })
	if index < 0 {
		return nil
	}
	candidate = append(candidate[:index], candidate[index+1:]...)
	return service.persist(ctx, candidate, target)
}

func (service *Service) OnChanged(listener func(domain.Target)) {
	if listener == nil {
		return
	}
	service.mu.Lock()
	service.listeners = append(service.listeners, listener)
	service.mu.Unlock()
}

func (service *Service) persist(ctx context.Context, candidate []domain.Assignment, target domain.Target) error {
	if err := service.repository.Save(ctx, candidate); err != nil {
		return errors.New("routes could not be saved")
	}
	service.mu.Lock()
	service.assignments = slices.Clone(candidate)
	listeners := append([]func(domain.Target){}, service.listeners...)
	service.mu.Unlock()
	for _, listener := range listeners {
		listener(target)
	}
	return nil
}
