package notifications

import (
	"context"
	"encoding/json"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/notifications/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/notifications/domain"
)

// Register exposes the notification feed to the desktop shell. The feed is
// already sanitized where it is written — sources are owner-side slices whose
// titles carry provider names on the owner's own screen, never upstream URLs or
// credentials — so this slice only stores, bounds and relays.
func Register(server *platform.Server, service *application.Service) {
	server.Handle("notifications.list", func(_ context.Context, _ json.RawMessage) (any, error) {
		return map[string]any{"notifications": service.List()}, nil
	})
	server.Handle("notifications.clear", func(_ context.Context, _ json.RawMessage) (any, error) {
		service.Clear()
		_ = server.Emit("notifications.changed", map[string]bool{"cleared": true})
		return map[string]bool{"cleared": true}, nil
	})
	service.OnChanged(func(notification domain.Notification) {
		_ = server.Emit("notifications.raised", notification)
		_ = server.Emit("notifications.changed", map[string]int{"count": len(service.List())})
	})
}
