package domain

import (
	"errors"
	"strings"
)

type Target string

const (
	TargetRelay  Target = "relay"
	TargetTunnel Target = "tunnel"
)

type Assignment struct {
	Target          Target   `json:"target"`
	PublicModel     string   `json:"publicModel"`
	UpstreamModel   string   `json:"upstreamModel"`
	ProviderID      string   `json:"providerId"`
	ContextLimitKiB int      `json:"contextLimitKiB"`
	Aliases         []string `json:"aliases,omitempty"`
	Enabled         bool     `json:"enabled"`
	// Priority orders the failover chain among assignments that share a public
	// model on the relay target: a request tries them in this order and moves
	// to the next when the current one answers terminally. Lower comes first.
	// The tunnel target keeps one route per model, so the field reads zero
	// there and orders nothing.
	Priority int `json:"priority"`
}

func (assignment Assignment) Validate() error {
	if err := ValidateIdentity(assignment.Target, assignment.PublicModel); err != nil {
		return err
	}
	if !validModel(assignment.UpstreamModel) {
		return errors.New("invalid route model")
	}
	if assignment.ProviderID == "" || assignment.ProviderID != strings.TrimSpace(assignment.ProviderID) {
		return errors.New("route provider is required")
	}
	if assignment.ContextLimitKiB < 0 || assignment.ContextLimitKiB > 2*1024*1024 {
		return errors.New("route context limit is invalid")
	}
	if assignment.Priority < 0 || assignment.Priority > 1000 {
		return errors.New("route priority is invalid")
	}
	if len(assignment.Aliases) > 16 {
		return errors.New("route alias list is too long")
	}
	for _, alias := range assignment.Aliases {
		if !validModel(alias) || alias == assignment.PublicModel {
			return errors.New("route alias is invalid")
		}
	}
	return nil
}

func ValidateIdentity(target Target, publicModel string) error {
	if target != TargetRelay && target != TargetTunnel {
		return errors.New("invalid route target")
	}
	if !validModel(publicModel) {
		return errors.New("invalid route model")
	}
	return nil
}

func validModel(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character < 32 || character == 127 {
			return false
		}
	}
	return true
}
