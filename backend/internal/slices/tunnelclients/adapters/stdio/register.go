package clientstdio

import (
	"context"
	"encoding/json"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/domain"
)

func Register(server *platform.Server, service *application.Service) {
	server.Handle("clients.list", func(_ context.Context, _ json.RawMessage) (any, error) {
		clients := service.List()
		return map[string]any{
			"clients":   withinOneFrame(clients),
			"available": len(clients),
		}, nil
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
	server.Handle("clients.profile", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			IP     string `json:"ip"`
			Banned bool   `json:"banned"`
			Note   string `json:"note"`
		}
		if platform.DecodePayload(payload, &command) != nil {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Invalid client profile"}
		}
		if err := service.SetProfile(ctx, domain.Profile{IP: command.IP, Banned: command.Banned, Note: command.Note}); err != nil {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Client profile could not be saved"}
		}
		return map[string]any{"clients": service.List()}, nil
	})
	service.OnChanged(func() { _ = server.Emit("clients.changed", map[string]bool{"changed": true}) })
}

// withinOneFrame drops the idle tail of the client list until the answer fits
// one frame. This is the third slice to need it: a leaked tunnel token can put
// a couple of thousand distinct addresses inside the one-hour idle window, and
// the registry's own ten-thousand-entry cap never notices — the workspace went
// dark with `response_too_large` at exactly the moment the ban list was needed
// most. Live clients stay ahead of idle profiles, because List already sorts
// the recent rows first and an address still acting is the one the owner acts
// on; `available` carries the pre-truncation count so a short list reads as a
// short list. No single client row can outgrow the budget, so the newest one
// always fits.
func withinOneFrame(clients []domain.Client) []domain.Client {
	return platform.TrimToPayloadBudget(clients)
}
