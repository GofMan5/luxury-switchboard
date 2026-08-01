package application

import (
	"context"
	"errors"
	"sync"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/relay/domain"
)

type Runtime interface {
	Start() (domain.Snapshot, error)
	Stop(context.Context) error
	CancelActive()
}

type Service struct {
	opMu      sync.Mutex
	mu        sync.Mutex
	runtime   Runtime
	snapshot  domain.Snapshot
	listeners []func(domain.Snapshot)
}

func NewService(runtime Runtime) *Service {
	return &Service{
		runtime:  runtime,
		snapshot: domain.Snapshot{State: domain.StateStopped},
	}
}

func (service *Service) Start() (domain.Snapshot, error) {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	service.mu.Lock()
	if service.snapshot.State == domain.StateLive {
		snapshot := service.snapshot
		service.mu.Unlock()
		return snapshot, nil
	}
	service.snapshot = domain.Snapshot{State: domain.StateStarting}
	service.mu.Unlock()
	service.publish()

	snapshot, err := service.runtime.Start()
	service.mu.Lock()
	if err != nil {
		service.snapshot = domain.Snapshot{State: domain.StateError, Error: "Relay could not start"}
	} else {
		service.snapshot = snapshot
	}
	result := service.snapshot
	service.mu.Unlock()
	service.publish()
	return result, err
}

func (service *Service) Stop(ctx context.Context) error {
	service.opMu.Lock()
	defer service.opMu.Unlock()
	service.mu.Lock()
	if service.snapshot.State == domain.StateStopped {
		service.mu.Unlock()
		return nil
	}
	previous := service.snapshot
	service.mu.Unlock()
	service.runtime.CancelActive()
	err := service.runtime.Stop(ctx)
	service.mu.Lock()
	if err != nil {
		previous.State = domain.StateError
		previous.Error = "Relay could not stop cleanly"
		service.snapshot = previous
	} else {
		service.snapshot = domain.Snapshot{State: domain.StateStopped}
	}
	service.mu.Unlock()
	service.publish()
	return err
}

func (service *Service) Snapshot() domain.Snapshot {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.snapshot
}

func (service *Service) OnChanged(listener func(domain.Snapshot)) error {
	if listener == nil {
		return errors.New("relay listener is required")
	}
	service.mu.Lock()
	service.listeners = append(service.listeners, listener)
	service.mu.Unlock()
	return nil
}

func (service *Service) CancelActive() {
	service.runtime.CancelActive()
}

func (service *Service) publish() {
	service.mu.Lock()
	snapshot := service.snapshot
	listeners := append([]func(domain.Snapshot){}, service.listeners...)
	service.mu.Unlock()
	for _, listener := range listeners {
		listener(snapshot)
	}
}
