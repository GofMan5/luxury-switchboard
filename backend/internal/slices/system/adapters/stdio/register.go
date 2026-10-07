package systemstdio

import (
	"context"
	"encoding/json"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
)

const AppVersion = "1.0.47"

// The handshake states which edition answers, so a client never offers a workspace
// this binary has no handler for.
func Register(server *platform.Server) {
	server.Handle("system.handshake", func(_ context.Context, _ json.RawMessage) (any, error) {
		return map[string]any{
			"protocol":     platform.ProtocolVersion,
			"appVersion":   AppVersion,
			"edition":      Edition,
			"capabilities": append([]string(nil), append(baseCapabilities, editionCapabilities...)...),
		}, nil
	})
}

var baseCapabilities = []string{
	"relay.control",
	"providers.read",
	"providers.activate",
	"providers.manage",
	"keys.manage",
	"activity.read",
	"history.read",
	"analytics.read",
	"analytics.manage",
	"settings.manage",
	"routes.manage",
	"guardrails.manage",
	"events.v1",
	"codex.login",
}
