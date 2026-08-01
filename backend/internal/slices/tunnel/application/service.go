package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnel/domain"
	"sync"
	"time"
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
	Start(context.Context, domain.Config) (string, error)
	Stop(context.Context) error
}
type RuntimeEvents interface {
	OnState(func(domain.State, string, string))
}
type Routes interface{ Count() int }
type Service struct {
	opMu              sync.Mutex
	controlMu         sync.Mutex
	controlCancel     context.CancelFunc
	controlAction     string
	controlGeneration uint64
	mu                sync.RWMutex
	repository        Repository
	runtime           Runtime
	routes            Routes
	config            domain.Config
	snapshot          domain.Snapshot
	runtimeStatus     domain.State
	runtimeAddress    string
	runtimeMessage    string
	publicationState  string
	listeners         []func(domain.Snapshot)
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
	service := &Service{repository: repository, runtime: runtime, routes: routes, config: config, snapshot: publicSnapshot(domain.StateStopped, "", config), runtimeStatus: domain.StateStopped}
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
	service.cancelOppositePublication("start")
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
	service.cancelOppositePublication("stop")
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
func (service *Service) Config() domain.Config {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.config
}
func (service *Service) SetPublicationState(state string) {
	service.mu.Lock()
	service.publicationState = state
	current := service.snapshot.State
	switch state {
	case "running":
		stoppedRemotely := current == domain.StatePaused && service.snapshot.Error == "Publication stopped from Shared Control"
		if current == domain.StatePaused && !stoppedRemotely {
			service.snapshot = service.visibleRuntimeLocked()
			service.mu.Unlock()
			service.publish()
			return
		}
		shouldStart := (current == domain.StateStopped || current == domain.StateError || stoppedRemotely) && service.config.PublisherProfile != ""
		service.mu.Unlock()
		if shouldStart {
			service.reconcilePublication("start")
		}
		return
	case "paused":
		if current != domain.StateOnline && current != domain.StateStarting && current != domain.StatePaused {
			service.mu.Unlock()
			return
		}
		service.snapshot = service.visibleRuntimeLocked()
	case "stopped":
		if current == domain.StateStopped {
			service.mu.Unlock()
			return
		}
		service.snapshot = service.visibleRuntimeLocked()
		service.mu.Unlock()
		service.publish()
		service.reconcilePublication("stop")
		return
	default:
		service.mu.Unlock()
		return
	}
	service.mu.Unlock()
	service.publish()
}

func (service *Service) visibleRuntimeLocked() domain.Snapshot {
	snapshot := publicSnapshot(service.runtimeStatus, service.runtimeAddress, service.config)
	snapshot.Error = service.runtimeMessage
	if service.config.PublisherProfile == "" || service.runtimeStatus == domain.StateStopped || service.runtimeStatus == domain.StateError {
		return snapshot
	}
	switch service.publicationState {
	case "paused":
		snapshot.State = domain.StatePaused
		snapshot.Error = "Publication paused from Shared Control"
	case "stopped":
		snapshot.State = domain.StatePaused
		snapshot.Error = "Publication stopped from Shared Control"
	}
	return snapshot
}

func (service *Service) reconcilePublication(action string) {
	service.controlMu.Lock()
	if service.controlAction == action {
		service.controlMu.Unlock()
		return
	}
	if service.controlCancel != nil {
		service.controlCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	service.controlGeneration++
	generation := service.controlGeneration
	service.controlAction, service.controlCancel = action, cancel
	service.controlMu.Unlock()
	go func() {
		timeout := 5 * time.Second
		if action == "start" {
			timeout = 50 * time.Second
		}
		opCtx, cancelOperation := context.WithTimeout(ctx, timeout)
		if action == "start" {
			_ = service.Start(opCtx)
		} else {
			_ = service.Stop(opCtx)
		}
		cancelOperation()
		cancel()
		service.controlMu.Lock()
		if service.controlGeneration == generation {
			service.controlAction, service.controlCancel = "", nil
		}
		service.controlMu.Unlock()
	}()
}

func (service *Service) cancelOppositePublication(action string) {
	service.controlMu.Lock()
	if service.controlAction != "" && service.controlAction != action && service.controlCancel != nil {
		service.controlCancel()
	}
	service.controlMu.Unlock()
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
