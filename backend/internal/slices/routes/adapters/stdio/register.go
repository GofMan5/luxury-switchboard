package routestdio

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/secretstore"
	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/routes/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/routes/domain"
)

func Register(server *platform.Server, service *application.Service) {
	server.Handle("routes.list", func(_ context.Context, payload json.RawMessage) (any, error) {
		var query struct {
			Target domain.Target `json:"target"`
		}
		if platform.DecodePayload(payload, &query) != nil {
			return nil, invalid()
		}
		if err := service.Availability(); err != nil {
			return nil, operationError(err, "route_list_failed", "Model routes could not be loaded")
		}
		return map[string]any{"routes": service.List(query.Target)}, nil
	})
	server.Handle("routes.upsert", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var assignment domain.Assignment
		if platform.DecodePayload(payload, &assignment) != nil {
			return nil, invalid()
		}
		if err := service.Upsert(ctx, assignment); err != nil {
			return nil, operationError(err, "route_update_failed", "Route could not be saved")
		}
		return assignment, nil
	})
	server.Handle("routes.upsertMany", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			Routes []domain.Assignment `json:"routes"`
		}
		if platform.DecodePayload(payload, &command) != nil {
			return nil, invalid()
		}
		if err := service.UpsertMany(ctx, command.Routes); err != nil {
			return nil, operationError(err, "route_update_failed", "Routes could not be saved")
		}
		return map[string]int{"saved": len(command.Routes)}, nil
	})
	server.Handle("routes.delete", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			Target      domain.Target `json:"target"`
			PublicModel string        `json:"publicModel"`
		}
		if platform.DecodePayload(payload, &command) != nil {
			return nil, invalid()
		}
		if err := service.Delete(ctx, command.Target, command.PublicModel); err != nil {
			return nil, operationError(err, "route_delete_failed", "Route could not be deleted")
		}
		return map[string]bool{"deleted": true}, nil
	})
	service.OnChanged(func(target domain.Target) {
		_ = server.Emit("routes.changed", map[string]domain.Target{"target": target})
	})
}
func invalid() platform.MethodError {
	return platform.MethodError{Code: "invalid_payload", Message: "Invalid route settings"}
}

func operationError(err error, code, message string) platform.MethodError {
	if errors.Is(err, secretstore.ErrUnavailable) {
		return platform.MethodError{
			Code:    "secure_storage_unavailable",
			Message: "Secure storage is unavailable. Start or unlock Linux Secret Service, run Switchboard without sudo, then restart it.",
		}
	}
	return platform.MethodError{Code: code, Message: message}
}
