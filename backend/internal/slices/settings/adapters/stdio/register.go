package settingsstdio

import (
	"context"
	"encoding/json"
	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/settings/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/settings/domain"
)

func Register(server *platform.Server, service *application.Service) {
	server.Handle("settings.get", func(_ context.Context, _ json.RawMessage) (any, error) {
		return service.Snapshot(), nil
	})
	server.Handle("settings.update", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var settings domain.Settings
		if platform.DecodePayload(payload, &settings) != nil {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Invalid settings"}
		}
		result, err := service.Update(ctx, settings)
		if err != nil {
			return nil, platform.MethodError{Code: "settings_update_failed", Message: "Settings could not be saved"}
		}
		_ = server.Emit("settings.changed", result)
		return result, nil
	})
}
