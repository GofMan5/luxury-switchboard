package stdio

import (
	"context"
	"encoding/json"
	"time"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/sharedcontrol/application"
)

func Register(server *platform.Server, service *application.Service) {
	server.Handle("shared.list", func(ctx context.Context, _ json.RawMessage) (any, error) {
		requestCtx, cancel := context.WithTimeout(ctx, 18*time.Second)
		defer cancel()
		return service.Snapshot(requestCtx), nil
	})
	server.Handle("shared.control", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			Position int    `json:"position"`
			Revision int64  `json:"revision"`
			Action   string `json:"action"`
		}
		if platform.DecodePayload(payload, &command) != nil {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Invalid shared control request"}
		}
		requestCtx, cancel := context.WithTimeout(ctx, 18*time.Second)
		defer cancel()
		snapshot, err := service.Control(requestCtx, command.Position, command.Revision, command.Action)
		if err != nil {
			return nil, platform.MethodError{Code: "shared_control_failed", Message: "Shared tunnel control unavailable"}
		}
		return snapshot, nil
	})
}
