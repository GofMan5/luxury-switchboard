package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
	"sync"
)

var (
	ErrRunning  = errors.New("tunnel must be stopped")
	ErrNoRoutes = errors.New("tunnel has no public routes")
)

type Repository interface {
	Load(context.Context) (domain.Config, bool, error)
	Save(context.Context, domain.Config) error
}
type Runtime interface {
	Start(domain.Config) (string, error)
	Stop(context.Context) error
}
type RuntimeEvents interface {
	OnState(func(domain.State, string, string))
}
type Routes interface{ Count() int }
type Service struct {
	mu         sync.RWMutex
	repository Repository
	runtime    Runtime
	routes     Routes
	config     domain.Config
	snapshot   domain.Snapshot
	listeners  []func(domain.Snapshot)
}

func NewService(repository Repository, runtime Runtime, routes Routes) (*Service, error) {
	if repository == nil || runtime == nil || routes == nil {
		return nil, errors.New("tunnel dependencies invalid")
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	config := domain.Config{Port: 8797, Token: token, BrandResponse: "Luxury Private лучший приватный софт для абузов - @Luxuryprivate_bot"}
	service := &Service{repository: repository, runtime: runtime, routes: routes, config: config, snapshot: publicSnapshot(domain.StateStopped, "", config)}
	if events, ok := runtime.(RuntimeEvents); ok {
		events.OnState(service.runtimeState)
	}
	return service, nil
}
func (service *Service) runtimeState(state domain.State, address, message string) {
	service.mu.Lock()
	if service.snapshot.State == domain.StateStopped {
		service.mu.Unlock()
		return
	}
	service.snapshot = publicSnapshot(state, address, service.config)
	service.snapshot.Error = message
	service.mu.Unlock()
	service.publish()
}
func (service *Service) Load(ctx context.Context) error {
	config, found, err := service.repository.Load(ctx)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if err := config.Validate(); err != nil {
		return err
	}
	service.mu.Lock()
	service.config = config
	service.snapshot = publicSnapshot(domain.StateStopped, "", config)
	service.mu.Unlock()
	return nil
}
func (service *Service) Snapshot() domain.Snapshot {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.snapshot
}
func (service *Service) Configure(ctx context.Context, config domain.Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	service.mu.RLock()
	state := service.snapshot.State
	service.mu.RUnlock()
	if state != domain.StateStopped && state != domain.StateError {
		return ErrRunning
	}
	if err := service.repository.Save(ctx, config); err != nil {
		return errors.New("tunnel settings could not be saved")
	}
	service.mu.Lock()
	service.config = config
	service.snapshot = publicSnapshot(domain.StateStopped, "", config)
	service.mu.Unlock()
	service.publish()
	return nil
}
func (service *Service) Start() error {
	service.mu.Lock()
	if service.snapshot.State != domain.StateStopped && service.snapshot.State != domain.StateError {
		service.mu.Unlock()
		return ErrRunning
	}
	if service.routes.Count() == 0 {
		service.snapshot = publicSnapshot(domain.StateError, "", service.config)
		service.snapshot.Error = "No publishable tunnel models are configured"
		service.mu.Unlock()
		service.publish()
		return ErrNoRoutes
	}
	service.snapshot = publicSnapshot(domain.StateStarting, "", service.config)
	config := service.config
	service.mu.Unlock()
	service.publish()
	address, err := service.runtime.Start(config)
	service.mu.Lock()
	if err != nil {
		service.snapshot = publicSnapshot(domain.StateError, "", config)
		service.snapshot.Error = err.Error()
	} else {
		service.snapshot = publicSnapshot(domain.StateOnline, address, config)
	}
	service.mu.Unlock()
	service.publish()
	return err
}
func (service *Service) Stop(ctx context.Context) error {
	err := service.runtime.Stop(ctx)
	service.mu.Lock()
	service.snapshot = publicSnapshot(domain.StateStopped, "", service.config)
	service.mu.Unlock()
	service.publish()
	return err
}
func (service *Service) RotateToken(ctx context.Context) (string, error) {
	service.mu.RLock()
	if service.snapshot.State != domain.StateStopped && service.snapshot.State != domain.StateError {
		service.mu.RUnlock()
		return "", ErrRunning
	}
	config := service.config
	service.mu.RUnlock()
	token, err := newToken()
	if err != nil {
		return "", err
	}
	config.Token = token
	if err := service.Configure(ctx, config); err != nil {
		return "", err
	}
	return token, nil
}
func (service *Service) RevealToken() string {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.config.Token
}
func (service *Service) Config() domain.Config {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.config
}
func (service *Service) SetPublicationState(state string) {
	service.mu.Lock()
	current := service.snapshot.State
	if current != domain.StateOnline && current != domain.StatePaused {
		service.mu.Unlock()
		return
	}
	switch state {
	case "running":
		service.snapshot.State = domain.StateOnline
		service.snapshot.Error = ""
	case "paused":
		service.snapshot.State = domain.StatePaused
		service.snapshot.Error = "Publication paused from Shared Control"
	case "stopped":
		service.snapshot.State = domain.StatePaused
		service.snapshot.Error = "Publication stopped from Shared Control"
	default:
		service.mu.Unlock()
		return
	}
	service.mu.Unlock()
	service.publish()
}
func (service *Service) OnChanged(listener func(domain.Snapshot)) {
	if listener == nil {
		return
	}
	service.mu.Lock()
	service.listeners = append(service.listeners, listener)
	service.mu.Unlock()
}
func (service *Service) publish() {
	service.mu.RLock()
	snapshot := service.snapshot
	listeners := append([]func(domain.Snapshot){}, service.listeners...)
	service.mu.RUnlock()
	for _, listener := range listeners {
		listener(snapshot)
	}
}
func publicSnapshot(state domain.State, address string, config domain.Config) domain.Snapshot {
	return domain.Snapshot{State: state, Port: config.Port, Address: address, RPMPerIP: config.RPMPerIP, ContextLimitKiB: config.ContextLimitKiB, BrandResponse: config.BrandResponse, PublisherProfile: config.PublisherProfile, TokenConfigured: config.Token != ""}
}
func newToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", errors.New("secure random unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
