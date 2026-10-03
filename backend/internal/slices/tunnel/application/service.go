package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
)

var (
	ErrRunning            = errors.New("tunnel must be stopped")
	ErrNoRoutes           = errors.New("tunnel has no public routes")
	ErrPrivacyUnavailable = errors.New("tunnel privacy test is unavailable")
)

type Repository interface {
	Load(context.Context) (domain.Config, bool, error)
	Save(context.Context, domain.Config) error
}
type Runtime interface {
	Start(context.Context, domain.Config) (string, error)
	Stop(context.Context) error
}
type RuntimeEvents interface {
	OnState(func(domain.State, string, string))
}
type Routes interface{ Count() int }
type PrivacyAuditor interface {
	Audit(context.Context, string, string) (domain.PrivacyReport, error)
}
type Service struct {
	opMu           sync.Mutex
	mu             sync.RWMutex
	repository     Repository
	runtime        Runtime
	routes         Routes
	privacyAuditor PrivacyAuditor
	config         domain.Config
	snapshot       domain.Snapshot
	runtimeStatus  domain.State
	runtimeAddress string
	runtimeMessage string
	listeners      []func(domain.Snapshot)
}

func NewService(repository Repository, runtime Runtime, routes Routes, privacyAuditor PrivacyAuditor) (*Service, error) {
	if repository == nil || runtime == nil || routes == nil || privacyAuditor == nil {
		return nil, errors.New("tunnel dependencies invalid")
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	config := domain.Config{Port: 8797, Token: token}
	service := &Service{repository: repository, runtime: runtime, routes: routes, privacyAuditor: privacyAuditor, config: config, snapshot: publicSnapshot(domain.StateStopped, "", config), runtimeStatus: domain.StateStopped}
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
	service.runtimeStatus, service.runtimeAddress, service.runtimeMessage = state, address, message
	service.snapshot = service.visibleRuntimeLocked()
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
	service.runtimeStatus, service.runtimeAddress, service.runtimeMessage = domain.StateStopped, "", ""
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
	service.opMu.Lock()
	defer service.opMu.Unlock()
	if err := config.Validate(); err != nil {
		return err
	}
	service.mu.RLock()
	snapshot := service.snapshot
	service.mu.RUnlock()
	if !configurable(snapshot) {
		return ErrRunning
	}
	if err := service.repository.Save(ctx, config); err != nil {
		return errors.New("tunnel settings could not be saved")
	}
	service.mu.Lock()
	service.config = config
	service.runtimeStatus, service.runtimeAddress, service.runtimeMessage = domain.StateStopped, "", ""
	service.snapshot = publicSnapshot(domain.StateStopped, "", config)
	service.mu.Unlock()
	service.publish()
	return nil
}
func (service *Service) Start(ctx context.Context) error {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	service.mu.Lock()
	if !configurable(service.snapshot) {
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
	service.runtimeStatus, service.runtimeAddress, service.runtimeMessage = domain.StateStarting, "", ""
	service.snapshot = service.visibleRuntimeLocked()
	config := service.config
	service.mu.Unlock()
	service.publish()
	address, err := service.runtime.Start(ctx, config)
	service.mu.Lock()
	if err != nil {
		service.runtimeStatus, service.runtimeAddress, service.runtimeMessage = domain.StateError, "", err.Error()
	} else {
		service.runtimeStatus, service.runtimeAddress, service.runtimeMessage = domain.StateOnline, address, ""
	}
	service.snapshot = service.visibleRuntimeLocked()
	service.mu.Unlock()
	service.publish()
	return err
}
func (service *Service) Stop(ctx context.Context) error {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	err := service.runtime.Stop(ctx)
	service.mu.Lock()
	if err != nil {
		service.runtimeStatus, service.runtimeMessage = domain.StateError, "Tunnel could not be stopped cleanly"
	} else {
		service.runtimeStatus, service.runtimeAddress, service.runtimeMessage = domain.StateStopped, "", ""
	}
	service.snapshot = service.visibleRuntimeLocked()
	service.mu.Unlock()
	service.publish()
	return err
}
func (service *Service) RotateToken(ctx context.Context) (string, error) {
	service.mu.RLock()
	if !configurable(service.snapshot) {
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

func configurable(snapshot domain.Snapshot) bool {
	return snapshot.State == domain.StateStopped || (snapshot.State == domain.StateError && snapshot.Address == "")
}
func (service *Service) RevealToken() string {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.config.Token
}
func (service *Service) TestPrivacy(ctx context.Context) (domain.PrivacyReport, error) {
	service.mu.RLock()
	address := service.snapshot.Address
	token := service.config.Token
	service.mu.RUnlock()
	if address == "" || token == "" {
		return domain.PrivacyReport{}, ErrPrivacyUnavailable
	}
	return service.privacyAuditor.Audit(ctx, address, token)
}
func (service *Service) Config() domain.Config {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.config
}
func (service *Service) visibleRuntimeLocked() domain.Snapshot {
	snapshot := publicSnapshot(service.runtimeStatus, service.runtimeAddress, service.config)
	snapshot.Error = service.runtimeMessage
	return snapshot
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
	return domain.Snapshot{State: state, Port: config.Port, Address: address, RPMPerIP: config.RPMPerIP, ContextLimitKiB: config.ContextLimitKiB, BrandResponse: config.BrandResponse, TokenConfigured: config.Token != ""}
}
func newToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", errors.New("secure random unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
