package analyticsstdio

import (
	"context"
	"encoding/json"
	"errors"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/analytics/domain"
)

// Register exposes the analytics report and the price catalog. The report is
// built from sanitized history rows (the activity slice already stripped
// prompts and bodies before persisting), and prices are market rates —
// nothing here needs redaction, in either edition.
func Register(server *platform.Server, service *application.Service) {
	server.Handle("analytics.report", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var query struct {
			Period application.Period `json:"period"`
		}
		if platform.DecodePayload(payload, &query) != nil {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Invalid analytics query"}
		}
		if !application.ValidPeriod(query.Period) {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Invalid analytics period"}
		}
		report, err := service.Report(ctx, query.Period)
		if err != nil {
			return nil, reportError(err)
		}
		return report, nil
	})
	server.Handle("analytics.prices.get", func(ctx context.Context, _ json.RawMessage) (any, error) {
		catalog, err := service.Prices(ctx)
		if err != nil {
			return nil, priceError(err)
		}
		return catalogResponse(catalog), nil
	})
	server.Handle("analytics.prices.set", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var query struct {
			Model       string  `json:"model"`
			Input       float64 `json:"input"`
			CachedInput float64 `json:"cachedInput"`
			Output      float64 `json:"output"`
			Reasoning   float64 `json:"reasoning"`
		}
		if platform.DecodePayload(payload, &query) != nil {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Invalid price entry"}
		}
		catalog, err := service.SetPrice(ctx, domain.Price{
			Model: query.Model, Input: query.Input, CachedInput: query.CachedInput,
			Output: query.Output, Reasoning: query.Reasoning,
		})
		if err != nil {
			return nil, priceError(err)
		}
		return catalogResponse(catalog), nil
	})
	server.Handle("analytics.prices.remove", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var query struct {
			Model string `json:"model"`
		}
		if platform.DecodePayload(payload, &query) != nil {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Invalid price entry"}
		}
		if query.Model == "" {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Model is required"}
		}
		catalog, err := service.RemovePrice(ctx, query.Model)
		if err != nil {
			return nil, priceError(err)
		}
		return catalogResponse(catalog), nil
	})
}

func catalogResponse(catalog domain.Catalog) map[string]any {
	entries := make([]domain.Price, 0, len(catalog.Prices))
	for _, model := range catalog.Models() {
		entries = append(entries, catalog.Prices[model])
	}
	return map[string]any{"prices": entries}
}

func reportError(err error) platform.MethodError {
	if errors.Is(err, application.ErrUnavailable) {
		return platform.MethodError{Code: "analytics_unavailable", Message: "Analytics is unavailable"}
	}
	return platform.MethodError{Code: "analytics_query_failed", Message: "Analytics query failed"}
}

func priceError(err error) platform.MethodError {
	if errors.Is(err, application.ErrUnavailable) {
		return platform.MethodError{Code: "analytics_unavailable", Message: "Analytics is unavailable"}
	}
	// Validation and persistence refuse differently: telling an operator
	// their rate is invalid when the catalog file failed to load sends them
	// editing numbers that were never the problem.
	if errors.Is(err, domain.ErrCatalogUnreadable) {
		return platform.MethodError{Code: "price_catalog_failed", Message: "The price catalog could not be read or written"}
	}
	return platform.MethodError{Code: "invalid_price", Message: "Invalid price entry"}
}
