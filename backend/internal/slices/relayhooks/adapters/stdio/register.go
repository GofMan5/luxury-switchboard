// Package relayhooksstdio serves the relay hooks commands to the plugin host
// and delivers hook traffic to it as events.
package relayhooksstdio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/relayhooks/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/relayhooks/domain"
)

// Register serves relayhooks.register, unregister, reset and respond.
func Register(server *platform.Server, service *application.Service) {
	server.Handle("relayhooks.register", func(_ context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			PluginID  string   `json:"pluginId"`
			HookID    string   `json:"hookId"`
			Phase     string   `json:"phase"`
			Models    []string `json:"models"`
			Endpoints []string `json:"endpoints"`
			TimeoutMS *int     `json:"timeoutMs"`
		}
		if platform.DecodePayload(payload, &command) != nil {
			return nil, invalid("Relay hook registration is malformed")
		}
		timeout := domain.DefaultTimeoutMS
		if command.TimeoutMS != nil {
			timeout = *command.TimeoutMS
		}
		err := service.Register(domain.Hook{
			PluginID: command.PluginID, HookID: command.HookID, Phase: command.Phase,
			Models: command.Models, Endpoints: command.Endpoints, TimeoutMS: timeout,
		})
		if err != nil {
			return nil, failure(err)
		}
		return map[string]bool{"registered": true}, nil
	})
	server.Handle("relayhooks.unregister", func(_ context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			PluginID string `json:"pluginId"`
			HookID   string `json:"hookId"`
		}
		if platform.DecodePayload(payload, &command) != nil {
			return nil, invalid("Relay hook removal is malformed")
		}
		removed, err := service.Unregister(command.PluginID, command.HookID)
		if err != nil {
			return nil, failure(err)
		}
		return map[string]int{"removed": removed}, nil
	})
	server.Handle("relayhooks.reset", func(_ context.Context, payload json.RawMessage) (any, error) {
		if platform.DecodePayload(payload, &struct{}{}) != nil {
			return nil, invalid("Relay hook reset takes no arguments")
		}
		return map[string]int{"removed": service.Reset()}, nil
	})
	server.Handle("relayhooks.respond", func(_ context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			CallID string   `json:"callId"`
			Action string   `json:"action"`
			Body   string   `json:"body"`
			Model  string   `json:"model"`
			Tags   []string `json:"tags"`
			Block  *struct {
				Status  int    `json:"status"`
				Message string `json:"message"`
			} `json:"block"`
		}
		if platform.DecodePayload(payload, &command) != nil {
			return nil, invalid("Relay hook decision is malformed")
		}
		decision := domain.Decision{Action: command.Action, Body: command.Body, Model: command.Model, Tags: command.Tags}
		if command.Block != nil {
			decision.Block = &domain.Block{Status: command.Block.Status, Message: command.Block.Message}
		}
		accepted, err := service.Respond(command.CallID, decision)
		if err != nil {
			return nil, failure(err)
		}
		return map[string]bool{"accepted": accepted}, nil
	})
}

func invalid(message string) error {
	return platform.MethodError{Code: "invalid_argument", Message: message}
}

// failure maps service errors to the codes plugins see. Messages name the
// field and the rule, never the value.
func failure(err error) error {
	var validation domain.ValidationError
	switch {
	case errors.As(err, &validation):
		return invalid(fmt.Sprintf("Relay hook %s", validation.Error()))
	case errors.Is(err, application.ErrNotGranted):
		return platform.MethodError{Code: "permission_denied", Message: "The plugin must be enabled and granted relay:content"}
	case errors.Is(err, application.ErrLimit):
		return platform.MethodError{Code: "limit", Message: fmt.Sprintf("At most %d relay hooks can run, and %d per plugin per phase", domain.MaxHooks, domain.MaxHooksPerPhase)}
	case errors.Is(err, application.ErrUnavailable):
		return platform.MethodError{Code: "relay_hooks_unavailable", Message: "Relay hooks are unavailable"}
	}
	return platform.MethodError{Code: "relay_hooks_failed", Message: "Relay hook command failed"}
}
