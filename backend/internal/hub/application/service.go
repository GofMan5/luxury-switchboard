package application

import (
	"context"
	"errors"

	"github.com/luxuryprivate/switchboard/backend/internal/hub/domain"
)

type Repository interface {
	Transaction(context.Context, func(*domain.State) (bool, error)) error
}

type Snapshot struct {
	Version  int                    `json:"v"`
	OK       bool                   `json:"ok"`
	Revision int64                  `json:"revision"`
	Tunnels  []domain.VisibleTunnel `json:"tunnels"`
}

type Service struct{ repository Repository }

func NewService(repository Repository) (*Service, error) {
	if repository == nil {
		return nil, errors.New("hub repository is required")
	}
	return &Service{repository: repository}, nil
}

func (service *Service) List(ctx context.Context, owner string) (Snapshot, error) {
	if !domain.OwnerPattern.MatchString(owner) {
		return Snapshot{}, errors.New("invalid owner")
	}
	var result Snapshot
	err := service.repository.Transaction(ctx, func(state *domain.State) (bool, error) {
		_, visible := state.OwnerView(owner)
		result = Snapshot{Version: 1, OK: true, Revision: state.Revision, Tunnels: visible}
		return false, nil
	})
	return result, err
}

func (service *Service) Control(ctx context.Context, owner, action string, revision int64, position int) (Snapshot, error) {
	desired, ok := map[string]string{"pause": domain.StatePaused, "resume": domain.StateRunning, "stop": domain.StateStopped}[action]
	if !ok || !domain.OwnerPattern.MatchString(owner) || revision < 0 || position < 0 || position > 999 {
		return Snapshot{}, errors.New("invalid control request")
	}
	var result Snapshot
	err := service.repository.Transaction(ctx, func(state *domain.State) (bool, error) {
		ordered, _ := state.OwnerView(owner)
		if state.Revision != revision || position >= len(ordered) {
			return false, errors.New("stale control request")
		}
		changed := state.Tunnels[ordered[position]].State != desired
		if changed {
			if state.Revision == int64(^uint64(0)>>1) {
				return false, errors.New("hub revision exhausted")
			}
			state.Tunnels[ordered[position]].State = desired
			state.Revision++
		}
		_, visible := state.OwnerView(owner)
		result = Snapshot{Version: 1, OK: true, Revision: state.Revision, Tunnels: visible}
		return changed, nil
	})
	return result, err
}

func (service *Service) Running(ctx context.Context, id string) bool {
	if !domain.IDPattern.MatchString(id) {
		return false
	}
	running := false
	if service.repository.Transaction(ctx, func(state *domain.State) (bool, error) {
		for _, tunnel := range state.Tunnels {
			if tunnel.ID == id && tunnel.State == domain.StateRunning {
				running = true
				break
			}
		}
		return false, nil
	}) != nil {
		return false
	}
	return running
}
