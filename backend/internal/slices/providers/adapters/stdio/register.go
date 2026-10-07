package providerstdio

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/secretstore"
	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/providers/domain"
)

type ListResult struct {
	ActiveID  string                  `json:"activeId"`
	Providers []domain.PublicProvider `json:"providers"`
}

type KeyCounter interface {
	Count(string) int
}

func Register(server *platform.Server, catalog *application.Catalog, manager *application.Manager, keys KeyCounter, health *application.HealthMonitor) {
	server.Handle("providers.list", func(_ context.Context, _ json.RawMessage) (any, error) {
		return snapshot(catalog, keys)
	})
	server.Handle("providers.health", func(_ context.Context, _ json.RawMessage) (any, error) {
		if health == nil {
			// No monitor means the probe is disabled: the answer is an honest
			// empty snapshot, not an error a surface has to treat as failure.
			return map[string]any{"states": []application.HealthState{}}, nil
		}
		return map[string]any{"states": health.Snapshot()}, nil
	})
	if health != nil {
		health.OnChanged(func(_ application.HealthState, snapshot []application.HealthState) {
			_ = server.Emit("providers.health", map[string]any{"states": snapshot})
		})
	}
	server.Handle("providers.activate", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			ID string `json:"id"`
		}
		if platform.DecodePayload(payload, &command) != nil || command.ID == "" {
			return nil, invalidPayload()
		}
		provider, err := manager.Activate(ctx, command.ID)
		if err != nil {
			return nil, managementError(err)
		}
		_ = server.Emit("providers.changed", map[string]any{"activeId": provider.ID})
		return publicProvider(provider, keys), nil
	})
	server.Handle("providers.add", func(ctx context.Context, payload json.RawMessage) (any, error) {
		params, err := providerParams(payload)
		if err != nil {
			return nil, err
		}
		provider, addErr := manager.Add(ctx, params)
		if addErr != nil {
			return nil, managementError(addErr)
		}
		_ = server.Emit("providers.changed", map[string]any{"providerId": provider.ID})
		return publicProvider(provider, keys), nil
	})
	server.Handle("providers.update", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			BaseURL     string `json:"baseUrl"`
			AuthMode    string `json:"authMode"`
			AuthHeader  string `json:"authHeader"`
			Dialect     string `json:"dialect"`
			ModelsPath  string `json:"modelsPath"`
			Format      string `json:"format"`
			ChatPath    string `json:"chatPath"`
			ImageCompat bool   `json:"imageCompat"`
			RPM         int    `json:"rpm"`
			RateUnit    string `json:"rateUnit"`
			Cache1H     bool   `json:"cache1h"`
			Enabled     bool   `json:"enabled"`
		}
		if platform.DecodePayload(payload, &command) != nil || command.ID == "" {
			return nil, invalidPayload()
		}
		provider, err := manager.Update(ctx, command.ID, domain.Params{
			Name: command.Name, BaseURL: command.BaseURL,
			AuthMode: domain.AuthMode(command.AuthMode), AuthHeader: command.AuthHeader,
			Dialect: domain.Dialect(command.Dialect), ModelsPath: command.ModelsPath, ImageCompat: command.ImageCompat,
			Format: domain.APIFormat(command.Format), ChatPath: command.ChatPath,
			RPM:      command.RPM,
			RateUnit: domain.RateUnit(command.RateUnit),
			CacheTTL: cacheTTL(command.Cache1H), Enabled: command.Enabled,
		})
		if err != nil {
			return nil, managementError(err)
		}
		_ = server.Emit("providers.changed", map[string]any{"providerId": provider.ID})
		return publicProvider(provider, keys), nil
	})
	server.Handle("providers.delete", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			ID string `json:"id"`
		}
		if platform.DecodePayload(payload, &command) != nil || command.ID == "" {
			return nil, invalidPayload()
		}
		// A preset entry belongs to the slice that provisioned it: the codex
		// account sign-in creates, relinks and removes it, so a generic delete
		// would strand the account link pointing at nothing. The refusal lives
		// at this control-plane boundary — not in the manager — because the
		// codex slice's own disconnect legitimately deletes preset-marked
		// leftovers through the same manager. Lookup answers existence, not
		// admission, and a miss falls through to Delete so its unavailable
		// error stays authoritative.
		if provider, exists := manager.Get(ctx, command.ID); exists && provider.Preset != "" {
			return nil, platform.MethodError{Code: "provider_managed", Message: "Managed provider cannot be deleted. Disconnect its account sign-in to remove it."}
		}
		if err := manager.Delete(ctx, command.ID); err != nil {
			return nil, managementError(err)
		}
		_ = server.Emit("providers.changed", map[string]any{"providerId": command.ID})
		return map[string]bool{"deleted": true}, nil
	})
}

func providerParams(payload json.RawMessage) (domain.Params, error) {
	var command struct {
		Name        string `json:"name"`
		BaseURL     string `json:"baseUrl"`
		AuthMode    string `json:"authMode"`
		AuthHeader  string `json:"authHeader"`
		Dialect     string `json:"dialect"`
		ModelsPath  string `json:"modelsPath"`
		Format      string `json:"format"`
		ChatPath    string `json:"chatPath"`
		ImageCompat bool   `json:"imageCompat"`
		RPM         int    `json:"rpm"`
		RateUnit    string `json:"rateUnit"`
		Cache1H     bool   `json:"cache1h"`
		Enabled     bool   `json:"enabled"`
	}
	if platform.DecodePayload(payload, &command) != nil {
		return domain.Params{}, invalidPayload()
	}
	return domain.Params{
		Name: command.Name, BaseURL: command.BaseURL,
		AuthMode: domain.AuthMode(command.AuthMode), AuthHeader: command.AuthHeader,
		Dialect: domain.Dialect(command.Dialect), ModelsPath: command.ModelsPath, ImageCompat: command.ImageCompat,
		Format: domain.APIFormat(command.Format), ChatPath: command.ChatPath,
		RPM:      command.RPM,
		RateUnit: domain.RateUnit(command.RateUnit),
		CacheTTL: cacheTTL(command.Cache1H), Enabled: command.Enabled,
	}, nil
}

func cacheTTL(enabled bool) time.Duration {
	if enabled {
		return time.Hour
	}
	return 0
}

func snapshot(catalog *application.Catalog, keys KeyCounter) (ListResult, error) {
	active, err := catalog.Active()
	if err != nil {
		return ListResult{}, err
	}
	providers := catalog.List()
	public := make([]domain.PublicProvider, 0, len(providers))
	for _, provider := range providers {
		public = append(public, publicProvider(provider, keys))
	}
	return ListResult{ActiveID: active.ID, Providers: public}, nil
}

func publicProvider(provider domain.Provider, keys KeyCounter) domain.PublicProvider {
	value := provider.Public()
	if keys != nil {
		value.KeyCount = keys.Count(provider.ID)
		value.KeyConfigured = value.KeyCount > 0
	}
	return value
}

func invalidPayload() platform.MethodError {
	return platform.MethodError{Code: "invalid_payload", Message: "Invalid provider settings"}
}

func managementError(err error) platform.MethodError {
	switch {
	case errors.Is(err, application.ErrStoreUnavailable):
		return platform.MethodError{Code: "secure_storage_unavailable", Message: "Secure storage is unavailable. Start or unlock Linux Secret Service, run Switchboard without sudo, then restart it."}
	case errors.Is(err, secretstore.ErrUnavailable):
		return platform.MethodError{Code: "secure_storage_unavailable", Message: "Secure storage is unavailable. Start or unlock Linux Secret Service, run Switchboard without sudo, then restart it."}
	case errors.Is(err, application.ErrProviderUnavailable):
		return platform.MethodError{Code: "provider_unavailable", Message: "Provider is unavailable"}
	case errors.Is(err, application.ErrBuiltinProvider):
		return platform.MethodError{Code: "provider_builtin", Message: "Built-in provider cannot be deleted"}
	case errors.Is(err, application.ErrActiveProvider):
		return platform.MethodError{Code: "provider_active", Message: "Active provider cannot be disabled or deleted"}
	case errors.Is(err, application.ErrProviderHasKeys):
		return platform.MethodError{Code: "provider_has_keys", Message: "Remove provider keys first"}
	case errors.Is(err, application.ErrProviderHasRoutes):
		return platform.MethodError{Code: "provider_has_routes", Message: "Reassign or delete provider model routes first"}
	default:
		return platform.MethodError{Code: "provider_update_failed", Message: "Provider settings could not be saved"}
	}
}
