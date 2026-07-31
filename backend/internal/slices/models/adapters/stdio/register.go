package stdio

import (
	"context"
	"encoding/json"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/models/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/models/domain"
)

func Register(server *platform.Server, service *application.Service) {
	server.Handle("models.discover", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var query struct {
			ProviderID string `json:"providerId"`
		}
		if platform.DecodePayload(payload, &query) != nil {
			return nil, invalid()
		}
		models, err := service.Discover(ctx, query.ProviderID)
		if err != nil {
			return nil, platform.MethodError{Code: "model_discovery_failed", Message: "Provider models are unavailable"}
		}
		return map[string]any{"providerId": query.ProviderID, "models": models}, nil
	})
	server.Handle("models.test", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			ProviderID string   `json:"providerId"`
			Models     []string `json:"models"`
		}
		if platform.DecodePayload(payload, &command) != nil {
			return nil, invalid()
		}
		count, err := service.Test(ctx, command.ProviderID, command.Models)
		if err != nil {
			return nil, platform.MethodError{Code: "model_test_failed", Message: "Model tests could not complete"}
		}
		return map[string]int{"tested": count}, nil
	})
	service.OnTested(func(result domain.TestResult) { _ = server.Emit("models.tested", result) })
}

func invalid() platform.MethodError {
	return platform.MethodError{Code: "invalid_payload", Message: "Invalid model request"}
}
