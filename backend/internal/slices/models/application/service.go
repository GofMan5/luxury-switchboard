package application

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/models/domain"
)

var (
	ErrProviderUnavailable = errors.New("model provider is unavailable")
	ErrTestsRunning        = errors.New("model tests are already running")
	// ErrDiscoveryUnauthorized separates the one discovery failure an operator fixes
	// in API Keys from every other one, which is fixed in Providers. Collapsing them
	// left the interface guessing out loud at a key that was already configured.
	ErrDiscoveryUnauthorized = errors.New("model catalog refused the stored credential")
)

const maxTestModels = 500

type Providers interface {
	Get(string) (domain.Provider, bool)
}

type Gateway interface {
	Discover(context.Context, domain.Provider) ([]string, error)
	Test(context.Context, domain.Provider, string) domain.TestResult
}

type Service struct {
	providers Providers
	gateway   Gateway
	testing   atomic.Bool
	mu        sync.RWMutex
	listeners []func(domain.TestResult)
}

func NewService(providers Providers, gateway Gateway) (*Service, error) {
	if providers == nil || gateway == nil {
		return nil, errors.New("model service dependencies are invalid")
	}
	return &Service{providers: providers, gateway: gateway}, nil
}

func (service *Service) Discover(ctx context.Context, providerID string) ([]string, error) {
	provider, exists := service.providers.Get(strings.TrimSpace(providerID))
	if !exists {
		return nil, ErrProviderUnavailable
	}
	models, err := service.gateway.Discover(ctx, provider)
	if err != nil {
		return nil, err
	}
	sort.Strings(models)
	return models, nil
}

func (service *Service) Test(ctx context.Context, providerID, runID string, models []string) (int, error) {
	provider, exists := service.providers.Get(strings.TrimSpace(providerID))
	if !exists {
		return 0, ErrProviderUnavailable
	}
	if !validRunID(runID) {
		return 0, errors.New("invalid model test run")
	}
	if len(models) > maxTestModels {
		return 0, errors.New("too many models")
	}
	models = normalizeModels(models)
	if len(models) == 0 {
		return 0, errors.New("models are required")
	}
	if !service.testing.CompareAndSwap(false, true) {
		return 0, ErrTestsRunning
	}
	defer service.testing.Store(false)

	jobs := make(chan string)
	var workers sync.WaitGroup
	for range min(4, len(models)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for model := range jobs {
				if ctx.Err() != nil {
					return
				}
				result := service.gateway.Test(ctx, provider, model)
				if ctx.Err() != nil {
					return
				}
				result.RunID = runID
				service.publish(result)
			}
		}()
	}
	for _, model := range models {
		select {
		case jobs <- model:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return 0, ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	return len(models), ctx.Err()
}

func validRunID(value string) bool {
	if len(value) < 1 || len(value) > 80 {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func (service *Service) OnTested(listener func(domain.TestResult)) {
	if listener == nil {
		return
	}
	service.mu.Lock()
	service.listeners = append(service.listeners, listener)
	service.mu.Unlock()
}

func (service *Service) publish(result domain.TestResult) {
	service.mu.RLock()
	listeners := append([]func(domain.TestResult){}, service.listeners...)
	service.mu.RUnlock()
	for _, listener := range listeners {
		listener(result)
	}
}

func normalizeModels(values []string) []string {
	seen := make(map[string]struct{}, min(len(values), maxTestModels))
	result := make([]string, 0, min(len(values), maxTestModels))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 || len(result) == maxTestModels {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
