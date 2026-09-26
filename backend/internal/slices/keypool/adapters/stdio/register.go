package keypoolstdio

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/secretstore"
	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
)

func Register(server *platform.Server, manager *application.Manager) {
	server.Handle("keys.list", func(_ context.Context, payload json.RawMessage) (any, error) {
		var query struct {
			ProviderID string `json:"providerId"`
		}
		if platform.DecodePayload(payload, &query) != nil || query.ProviderID == "" {
			return nil, invalidPayload()
		}
		keys := manager.List(query.ProviderID)
		return map[string]any{
			"keys":      withinOneFrame(keys),
			"available": len(keys),
		}, nil
	})
	server.Handle("keys.add", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			ProviderID string `json:"providerId"`
			Label      string `json:"label"`
			Secret     string `json:"secret"`
			RPM        int    `json:"rpm"`
			ProxyURL   string `json:"proxyUrl"`
		}
		if platform.DecodePayload(payload, &command) != nil {
			return nil, invalidPayload()
		}
		key, err := manager.Add(ctx, domain.Params{
			ProviderID: command.ProviderID, Label: command.Label, Secret: command.Secret,
			RPM: command.RPM, ProxyURL: command.ProxyURL,
		})
		if err != nil {
			return nil, managementError(err)
		}
		_ = server.Emit("keys.changed", map[string]string{"providerId": command.ProviderID})
		return key, nil
	})
	server.Handle("keys.addMany", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			ProviderID string `json:"providerId"`
			RPM        int    `json:"rpm"`
			ProxyURL   string `json:"proxyUrl"`
			Entries    []struct {
				Label  string `json:"label"`
				Secret string `json:"secret"`
			} `json:"entries"`
		}
		if platform.DecodePayload(payload, &command) != nil || command.ProviderID == "" || len(command.Entries) == 0 {
			return nil, invalidPayload()
		}
		entries := make([]application.ImportEntry, 0, len(command.Entries))
		for _, entry := range command.Entries {
			entries = append(entries, application.ImportEntry{Label: entry.Label, Secret: entry.Secret})
		}
		report, err := manager.AddMany(ctx, application.Import{
			ProviderID: command.ProviderID, RPM: command.RPM,
			ProxyURL: command.ProxyURL, Entries: entries,
		})
		if err != nil {
			return nil, managementError(err)
		}
		// An import that added nothing changed nothing, so listeners are left alone.
		if report.Added > 0 {
			_ = server.Emit("keys.changed", map[string]string{"providerId": command.ProviderID})
		}
		return report, nil
	})
	server.Handle("keys.update", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			ProviderID string  `json:"providerId"`
			KeyID      string  `json:"keyId"`
			Label      string  `json:"label"`
			RPM        int     `json:"rpm"`
			ProxyURL   *string `json:"proxyUrl,omitempty"`
			Secret     *string `json:"secret,omitempty"`
		}
		if platform.DecodePayload(payload, &command) != nil || command.KeyID == "" {
			return nil, invalidPayload()
		}
		key, err := manager.Update(ctx, command.ProviderID, command.KeyID, application.Update{
			Label: command.Label, RPM: command.RPM,
			ProxyURL: command.ProxyURL, Secret: command.Secret,
		})
		if err != nil {
			return nil, managementError(err)
		}
		_ = server.Emit("keys.changed", map[string]string{"providerId": command.ProviderID})
		return key, nil
	})
	server.Handle("keys.remove", func(ctx context.Context, payload json.RawMessage) (any, error) {
		providerID, keyID, err := keyTarget(payload)
		if err != nil {
			return nil, err
		}
		if err := manager.Remove(ctx, providerID, keyID); err != nil {
			return nil, managementError(err)
		}
		_ = server.Emit("keys.changed", map[string]string{"providerId": providerID})
		return map[string]bool{"removed": true}, nil
	})
	server.Handle("keys.move", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			ProviderID string `json:"providerId"`
			KeyID      string `json:"keyId"`
			Direction  int    `json:"direction"`
		}
		if platform.DecodePayload(payload, &command) != nil || command.KeyID == "" {
			return nil, invalidPayload()
		}
		if err := manager.Move(ctx, command.ProviderID, command.KeyID, command.Direction); err != nil {
			return nil, managementError(err)
		}
		_ = server.Emit("keys.changed", map[string]string{"providerId": command.ProviderID})
		return map[string]bool{"moved": true}, nil
	})
	server.Handle("keys.reset", func(_ context.Context, payload json.RawMessage) (any, error) {
		providerID, keyID, err := keyTarget(payload)
		if err != nil {
			return nil, err
		}
		if err := manager.Reset(providerID, keyID); err != nil {
			return nil, managementError(err)
		}
		_ = server.Emit("keys.changed", map[string]string{"providerId": providerID})
		return map[string]bool{"reset": true}, nil
	})
}

func keyTarget(payload json.RawMessage) (string, string, error) {
	var command struct {
		ProviderID string `json:"providerId"`
		KeyID      string `json:"keyId"`
	}
	if platform.DecodePayload(payload, &command) != nil || command.ProviderID == "" || command.KeyID == "" {
		return "", "", invalidPayload()
	}
	return command.ProviderID, command.KeyID, nil
}

func invalidPayload() platform.MethodError {
	return platform.MethodError{Code: "invalid_payload", Message: "Invalid key settings"}
}

// withinOneFrame drops the lowest-priority keys until the answer fits one
// protocol frame. Rows are bounded — labels, counters, milliseconds — so this
// only bites a deliberately enormous pool, and `available` says so honestly
// instead of a short list passing for the whole pool. List already orders by
// priority, so what stays is what the scheduler would use first.
func withinOneFrame(keys []domain.PublicKey) []domain.PublicKey {
	for len(keys) > 0 {
		encoded, err := json.Marshal(keys)
		if err != nil {
			return nil
		}
		if len(encoded) <= platform.MaxPayloadBytes {
			return keys
		}
		next := len(keys) * platform.MaxPayloadBytes / len(encoded)
		keys = keys[:min(next, len(keys)-1)]
	}
	return keys
}

func managementError(err error) platform.MethodError {
	switch {
	case errors.Is(err, application.ErrStoreUnavailable), errors.Is(err, secretstore.ErrUnavailable):
		return platform.MethodError{Code: "secure_storage_unavailable", Message: "Secure storage is unavailable. Start or unlock Linux Secret Service, run Switchboard without sudo, then restart it."}
	case errors.Is(err, application.ErrKeyNotFound):
		return platform.MethodError{Code: "key_not_found", Message: "Key was not found"}
	case errors.Is(err, application.ErrPinnedKey):
		return platform.MethodError{Code: "key_pinned", Message: "Pinned key cannot be changed this way"}
	case errors.Is(err, application.ErrDuplicateKey):
		return platform.MethodError{Code: "key_duplicate", Message: "Key is already configured"}
	case errors.Is(err, application.ErrUnknownProvider):
		return platform.MethodError{Code: "provider_not_found", Message: "Provider was not found"}
	case errors.Is(err, application.ErrImportTooLarge):
		return platform.MethodError{Code: "import_too_large", Message: "Import at most " + strconv.Itoa(application.MaxImportBatch) + " keys at once"}
	default:
		return platform.MethodError{Code: "key_update_failed", Message: "Key settings could not be saved"}
	}
}
