package application

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/slices/sharedcontrol/domain"
)

const (
	SelfName  = "Ваш коннект"
	errorText = "Shared tunnel control unavailable"
)

var errUnavailable = errors.New("shared tunnel control unavailable")
var validActions = map[string]string{"pause": "paused", "resume": "running", "stop": "stopped"}

type Client interface {
	Request(context.Context, ...string) (domain.Snapshot, error)
}

type Service struct {
	client     Client
	mu         sync.Mutex
	lastGood   domain.Snapshot
	lastGoodAt time.Time
	now        func() time.Time
	listeners  []func(domain.Snapshot)
}

func NewService(client Client) (*Service, error) {
	if client == nil {
		return nil, errors.New("shared control client is required")
	}
	return &Service{client: client, now: time.Now}, nil
}

func (service *Service) Snapshot(ctx context.Context) domain.Snapshot {
	for attempt := 0; attempt < 2; attempt++ {
		snapshot, err := service.client.Request(ctx, "list")
		if err == nil && self(snapshot) != nil {
			return service.remember(snapshot)
		}
		if attempt == 0 {
			select {
			case <-ctx.Done():
				break
			case <-time.After(150 * time.Millisecond):
			}
		}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.lastGood.Available && service.now().Sub(service.lastGoodAt) <= 21*time.Second {
		result := clone(service.lastGood)
		result.Stale = true
		return result
	}
	return unavailable()
}

func (service *Service) Control(ctx context.Context, position int, revision int64, action string) (domain.Snapshot, error) {
	desired, valid := validActions[action]
	if !valid || position < 0 || position > 999 || revision < 0 {
		return domain.Snapshot{}, errUnavailable
	}
	snapshot, err := service.client.Request(ctx, action, strconv.FormatInt(revision, 10), strconv.Itoa(position))
	if err == nil {
		return service.remember(snapshot), nil
	}
	current, listErr := service.client.Request(ctx, "list")
	if listErr == nil && position < len(current.Tunnels) && current.Tunnels[position].State == desired {
		return service.remember(current), nil
	}
	return domain.Snapshot{}, errUnavailable
}

func (service *Service) EnsureSelfRunning(ctx context.Context) (domain.Snapshot, error) {
	snapshot, err := service.client.Request(ctx, "list")
	if err != nil {
		return domain.Snapshot{}, errUnavailable
	}
	row := self(snapshot)
	if row == nil {
		return domain.Snapshot{}, errUnavailable
	}
	if row.State == "running" {
		return service.remember(snapshot), nil
	}
	return service.Control(ctx, row.Position, snapshot.Revision, "resume")
}

func (service *Service) OnChanged(listener func(domain.Snapshot)) {
	if listener == nil {
		return
	}
	service.mu.Lock()
	service.listeners = append(service.listeners, listener)
	service.mu.Unlock()
}

func (service *Service) remember(snapshot domain.Snapshot) domain.Snapshot {
	snapshot.Available, snapshot.Error, snapshot.Stale = true, "", false
	service.mu.Lock()
	service.lastGood = clone(snapshot)
	service.lastGoodAt = service.now()
	listeners := append([]func(domain.Snapshot){}, service.listeners...)
	service.mu.Unlock()
	for _, listener := range listeners {
		listener(clone(snapshot))
	}
	return clone(snapshot)
}

func self(snapshot domain.Snapshot) *domain.Tunnel {
	for index := range snapshot.Tunnels {
		if snapshot.Tunnels[index].Name == SelfName {
			return &snapshot.Tunnels[index]
		}
	}
	return nil
}

func clone(snapshot domain.Snapshot) domain.Snapshot {
	snapshot.Tunnels = slices.Clone(snapshot.Tunnels)
	return snapshot
}

func unavailable() domain.Snapshot {
	return domain.Snapshot{Available: false, Tunnels: []domain.Tunnel{}, Error: errorText}
}
