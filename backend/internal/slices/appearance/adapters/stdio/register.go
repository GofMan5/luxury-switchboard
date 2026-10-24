package appearancestdio

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/secretstore"
	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/appearance/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/appearance/domain"
)

func Register(server *platform.Server, service *application.Service) {
	service.OnChanged(func(appearance domain.Appearance) {
		_ = server.Emit("appearance.changed", appearance)
	})
	server.Handle("appearance.get", func(_ context.Context, payload json.RawMessage) (any, error) {
		if platform.DecodePayload(payload, &struct{}{}) != nil {
			return nil, platform.MethodError{Code: "invalid_appearance", Message: "Appearance request is malformed"}
		}
		return service.Snapshot(), nil
	})
	server.Handle("appearance.update", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var appearance domain.Appearance
		if platform.DecodePayload(payload, &appearance) != nil {
			return nil, platform.MethodError{Code: "invalid_appearance", Message: "Appearance is malformed"}
		}
		result, err := service.Update(ctx, appearance)
		if err != nil {
			return nil, updateError(err)
		}
		return result, nil
	})
}

func updateError(err error) error {
	var validation domain.ValidationError
	switch {
	case errors.As(err, &validation):
		// The domain message names the field and the rule, never the value.
		return platform.MethodError{Code: "invalid_appearance", Message: "Appearance " + validation.Error()}
	case errors.Is(err, application.ErrStoreUnavailable) || errors.Is(err, secretstore.ErrUnavailable):
		return platform.MethodError{Code: "secure_storage_unavailable", Message: "Secure storage is unavailable. Start or unlock Linux Secret Service, run Switchboard without sudo, then restart it."}
	default:
		return platform.MethodError{Code: "appearance_update_failed", Message: "Appearance could not be saved"}
	}
}
