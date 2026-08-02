package systemstdio

import (
	"context"
	"encoding/json"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
)

const AppVersion = "1.0.4"

func Register(server *platform.Server) {
	server.Handle("system.handshake", func(_ context.Context, _ json.RawMessage) (any, error) {
		return map[string]any{
			"protocol":   platform.ProtocolVersion,
			"appVersion": AppVersion,
			"capabilities": []string{
				"relay.control",
				"providers.read",
				"providers.activate",
				"providers.manage",
				"keys.manage",
				"activity.read",
				"history.read",
				"settings.manage",
				"routes.manage",
				"tunnel.manage",
				"events.v1",
			},
		}, nil
	})
}
