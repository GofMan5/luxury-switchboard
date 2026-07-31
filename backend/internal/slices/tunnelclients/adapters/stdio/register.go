package clientstdio

import (
	"context"
	"encoding/json"
	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/application"
)

func Register(server *platform.Server, service *application.Service) {
	server.Handle("clients.list", func(_ context.Context, _ json.RawMessage) (any, error) {
		return map[string]any{"clients": service.List()}, nil
	})
	server.Handle("clients.events", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var query struct {
			IP string `json:"ip"`
		}
		if platform.DecodePayload(payload, &query) != nil || query.IP == "" {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Invalid client query"}
		}
		return map[string]any{"events": service.Events(ctx, query.IP)}, nil
	})
	service.OnChanged(func() { _ = server.Emit("clients.changed", map[string]bool{"changed": true}) })
}
