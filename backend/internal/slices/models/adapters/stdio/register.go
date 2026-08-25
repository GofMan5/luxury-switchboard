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
		// The shell refuses a frame over MaxFrameBytes by killing the sidecar, so the
		// protocol answers `response_too_large` instead - a code written for a bug,
		// surfaced here as "Command response is too large" over a catalog that is
		// simply big. The count cap alone does not bound this: 5000 names of 43 bytes
		// fills 87% of the frame and 5000 of 50 bytes does not fit at all, which is an
		// ordinary aggregator catalog, not a hostile one. Refusing rather than
		// truncating, because the catalog is the list the operator publishes FROM and
		// a model silently missing from it is a model they cannot publish.
		if oversizedCatalog(query.ProviderID, models) {
			return nil, platform.MethodError{Code: "model_catalog_too_large", Message: "Provider model catalog is too large to load"}
		}
		return map[string]any{"providerId": query.ProviderID, "models": models}, nil
	})
	server.Handle("models.test", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var command struct {
			ProviderID string   `json:"providerId"`
			RunID      string   `json:"runId"`
			Models     []string `json:"models"`
		}
		if platform.DecodePayload(payload, &command) != nil {
			return nil, invalid()
		}
		count, err := service.Test(ctx, command.ProviderID, command.RunID, command.Models)
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

// oversizedCatalog measures against the platform payload budget rather than the frame
// limit: what is encoded here is the payload, and the frame that carries it adds its
// envelope on top.
func oversizedCatalog(providerID string, models []string) bool {
	encoded, err := json.Marshal(map[string]any{"providerId": providerID, "models": models})
	return err != nil || len(encoded) > platform.MaxPayloadBytes
}
