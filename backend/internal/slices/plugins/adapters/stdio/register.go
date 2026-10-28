package pluginsstdio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/secretstore"
	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/plugins/domain"
)

type idPayload struct {
	ID string `json:"id"`
}

type installPayload struct {
	Package domain.Package `json:"package"`
	Granted []string       `json:"granted"`
	// Source is the https link the package was downloaded from; absent for
	// a file install.
	Source string `json:"source"`
}

type updatePayload struct {
	ID      string    `json:"id"`
	Enabled *bool     `json:"enabled"`
	Granted *[]string `json:"granted"`
}

type storageUpdatePayload struct {
	ID string `json:"id"`
	// Values maps each key to its new value, or null to remove it.
	Values map[string]*string `json:"values"`
	// Clear removes every key before Values apply.
	Clear bool `json:"clear"`
}

type storageSetPayload struct {
	ID  string `json:"id"`
	Key string `json:"key"`
	// Value is raw so an absent value is told apart from an explicit null,
	// which is the one way to delete a key.
	Value json.RawMessage `json:"value"`
}

func Register(server *platform.Server, service *application.Service) {
	service.OnChanged(func(plugins []application.Summary) {
		_ = server.Emit("plugins.changed", map[string]any{"plugins": plugins})
	})
	server.Handle("plugins.list", func(_ context.Context, payload json.RawMessage) (any, error) {
		if platform.DecodePayload(payload, &struct{}{}) != nil {
			return nil, invalid("Plugin list request is malformed")
		}
		plugins, err := service.List()
		if err != nil {
			return nil, failure(err)
		}
		return map[string]any{"plugins": plugins}, nil
	})
	server.Handle("plugins.get", func(_ context.Context, payload json.RawMessage) (any, error) {
		id, err := decodeID(payload)
		if err != nil {
			return nil, err
		}
		detail, err := service.Get(id)
		if err != nil {
			return nil, failure(err)
		}
		return detail, nil
	})
	server.Handle("plugins.install", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var request installPayload
		if platform.DecodePayload(payload, &request) != nil {
			return nil, invalid("Plugin package is malformed")
		}
		plugin, err := service.Install(ctx, request.Package, request.Granted, request.Source)
		if err != nil {
			return nil, failure(err)
		}
		return map[string]any{"plugin": plugin}, nil
	})
	server.Handle("plugins.export", func(_ context.Context, payload json.RawMessage) (any, error) {
		id, err := decodeID(payload)
		if err != nil {
			return nil, err
		}
		pkg, err := service.Export(id)
		if err != nil {
			return nil, failure(err)
		}
		return map[string]any{"package": pkg}, nil
	})
	server.Handle("plugins.update", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var request updatePayload
		if platform.DecodePayload(payload, &request) != nil || !domain.ValidID(request.ID) {
			return nil, invalid("Plugin update is malformed")
		}
		plugin, err := service.Update(ctx, request.ID, request.Enabled, request.Granted)
		if err != nil {
			return nil, failure(err)
		}
		return map[string]any{"plugin": plugin}, nil
	})
	server.Handle("plugins.remove", func(ctx context.Context, payload json.RawMessage) (any, error) {
		id, err := decodeID(payload)
		if err != nil {
			return nil, err
		}
		if err := service.Remove(ctx, id); err != nil {
			return nil, failure(err)
		}
		return map[string]bool{"removed": true}, nil
	})
	server.Handle("plugins.storage.get", func(_ context.Context, payload json.RawMessage) (any, error) {
		id, err := decodeID(payload)
		if err != nil {
			return nil, err
		}
		values, err := service.StorageGet(id)
		if err != nil {
			return nil, failure(err)
		}
		return values, nil
	})
	server.Handle("plugins.storage.set", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var request storageSetPayload
		if platform.DecodePayload(payload, &request) != nil || !domain.ValidID(request.ID) || len(request.Value) == 0 {
			return nil, invalid("Plugin storage write is malformed")
		}
		var value *string
		if platform.DecodePayload(request.Value, &value) != nil {
			return nil, invalid("Plugin storage value must be a string or null")
		}
		values, err := service.StorageSet(ctx, request.ID, request.Key, value)
		if err != nil {
			return nil, failure(err)
		}
		return values, nil
	})
	registerSecrets(server, service)
	server.Handle("plugins.storage.update", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var request storageUpdatePayload
		// An absent or null values object is malformed; {} is an empty batch.
		if platform.DecodePayload(payload, &request) != nil || !domain.ValidID(request.ID) || request.Values == nil {
			return nil, invalid("Plugin storage update is malformed")
		}
		values, err := service.StorageUpdate(ctx, request.ID, request.Values, request.Clear)
		if err != nil {
			return nil, failure(err)
		}
		return values, nil
	})
}

func decodeID(payload json.RawMessage) (string, error) {
	var request idPayload
	if platform.DecodePayload(payload, &request) != nil || !domain.ValidID(request.ID) {
		return "", invalid("Plugin id is malformed")
	}
	return request.ID, nil
}

func invalid(message string) error {
	return platform.MethodError{Code: "invalid_plugin", Message: message}
}

func failure(err error) error {
	var validation domain.ValidationError
	switch {
	case errors.As(err, &validation):
		// The domain message names the field and the rule, never the value.
		return invalid("Plugin " + validation.Error())
	case errors.Is(err, application.ErrBroken):
		return invalid("Plugin cannot be used by this version; reinstall it or remove it")
	case errors.Is(err, application.ErrNotFound):
		return platform.MethodError{Code: "plugin_not_found", Message: "Plugin is not installed"}
	case errors.Is(err, application.ErrLimit):
		return platform.MethodError{Code: "plugin_limit", Message: fmt.Sprintf("At most %d plugins can be installed", domain.MaxPlugins)}
	case errors.Is(err, application.ErrTooLarge):
		return platform.MethodError{Code: "plugin_too_large", Message: "Plugin is too large to install"}
	case errors.Is(err, application.ErrRateLimited):
		return platform.MethodError{Code: "rate_limited", Message: fmt.Sprintf("Plugin wrote its storage more than %d times in a minute; wait and retry", application.StorageWritesPerMinute)}
	case errors.Is(err, application.ErrStorageFull):
		return platform.MethodError{Code: "plugin_storage_full", Message: "Plugin storage is full"}
	case errors.Is(err, application.ErrStoreUnavailable) || errors.Is(err, secretstore.ErrUnavailable):
		return platform.MethodError{Code: "secure_storage_unavailable", Message: "Secure storage is unavailable. Start or unlock Linux Secret Service, run Switchboard without sudo, then restart it."}
	default:
		return platform.MethodError{Code: "plugin_save_failed", Message: "Plugins could not be saved"}
	}
}
