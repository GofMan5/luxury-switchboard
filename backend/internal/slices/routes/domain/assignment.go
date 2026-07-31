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
	Target          Target `json:"target"`
	PublicModel     string `json:"publicModel"`
	UpstreamModel   string `json:"upstreamModel"`
	ProviderID      string `json:"providerId"`
	ContextLimitKiB int    `json:"contextLimitKiB"`
	Enabled         bool   `json:"enabled"`
}

func (assignment Assignment) Validate() error {
	if assignment.Target != TargetRelay && assignment.Target != TargetTunnel {
		return errors.New("invalid route target")
	}
	if !validModel(assignment.PublicModel) || !validModel(assignment.UpstreamModel) {
		return errors.New("invalid route model")
	}
	if strings.TrimSpace(assignment.ProviderID) == "" {
		return errors.New("route provider is required")
	}
	if assignment.ContextLimitKiB < 0 || assignment.ContextLimitKiB > 2*1024*1024 {
		return errors.New("route context limit is invalid")
	}
	return nil
}

func validModel(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character < 32 || character == 127 {
			return false
		}
	}
	return true
}
