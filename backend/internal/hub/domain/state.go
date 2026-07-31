package domain

import (
	"errors"
	"regexp"
	"sort"
	"strconv"
)

const (
	StateRunning = "running"
	StatePaused  = "paused"
	StateStopped = "stopped"
	SelfName     = "Ваш коннект"
)

var (
	IDPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)
	OwnerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
)

type Tunnel struct {
	ID    string
	Owner *string
	State string
}

type State struct {
	Revision int64
	Tunnels  []Tunnel
}

type VisibleTunnel struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

func (state State) Validate() error {
	if state.Revision < 0 || len(state.Tunnels) > 256 {
		return errors.New("invalid hub state")
	}
	ids := make(map[string]struct{}, len(state.Tunnels))
	owners := make(map[string]struct{}, len(state.Tunnels))
	for _, tunnel := range state.Tunnels {
		if !IDPattern.MatchString(tunnel.ID) || (tunnel.State != StateRunning && tunnel.State != StatePaused && tunnel.State != StateStopped) {
			return errors.New("invalid hub state")
		}
		if _, exists := ids[tunnel.ID]; exists {
			return errors.New("invalid hub state")
		}
		ids[tunnel.ID] = struct{}{}
		if tunnel.Owner != nil {
			if !OwnerPattern.MatchString(*tunnel.Owner) {
				return errors.New("invalid hub state")
			}
			if _, exists := owners[*tunnel.Owner]; exists {
				return errors.New("invalid hub state")
			}
			owners[*tunnel.Owner] = struct{}{}
		}
	}
	return nil
}

func (state State) OwnerView(owner string) ([]int, []VisibleTunnel) {
	owned := make([]int, 0, 1)
	others := make([]int, 0, len(state.Tunnels))
	for index, tunnel := range state.Tunnels {
		if tunnel.Owner != nil && *tunnel.Owner == owner {
			owned = append(owned, index)
		} else {
			others = append(others, index)
		}
	}
	sort.Slice(others, func(left, right int) bool { return state.Tunnels[others[left]].ID < state.Tunnels[others[right]].ID })
	ordered := append(owned, others...)
	visible := make([]VisibleTunnel, 0, len(ordered))
	other := 0
	for _, index := range ordered {
		name := SelfName
		if state.Tunnels[index].Owner == nil || *state.Tunnels[index].Owner != owner {
			other++
			name = "Tunnel " + strconv.Itoa(other)
		}
		visible = append(visible, VisibleTunnel{Name: name, State: state.Tunnels[index].State})
	}
	return ordered, visible
}
