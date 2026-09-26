package activitystdio

import (
	"context"
	"encoding/json"
	"time"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/activity/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/activity/domain"
)

func Register(server *platform.Server, service *application.Service, history application.History) {
	server.Handle("activity.list", func(_ context.Context, payload json.RawMessage) (any, error) {
		var query struct {
			Limit int `json:"limit"`
		}
		if platform.DecodePayload(payload, &query) != nil {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Invalid activity query"}
		}
		if query.Limit == 0 {
			query.Limit = 100
		}
		requests := service.List(query.Limit)
		return map[string]any{
			"requests":  withinOneFrame(requests),
			"available": len(requests),
		}, nil
	})
	server.Handle("activity.summary", func(_ context.Context, _ json.RawMessage) (any, error) {
		return service.Summary(time.Minute), nil
	})
	server.Handle("history.recent", func(ctx context.Context, payload json.RawMessage) (any, error) {
		if history == nil {
			return nil, platform.MethodError{Code: "history_unavailable", Message: "History is unavailable"}
		}
		var query struct {
			Period application.Period `json:"period"`
			Limit  int                `json:"limit"`
		}
		if platform.DecodePayload(payload, &query) != nil {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Invalid history query"}
		}
		if !application.ValidPeriod(query.Period) {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Invalid history period"}
		}
		requests, err := history.Recent(ctx, query.Period, query.Limit)
		if err != nil {
			return nil, platform.MethodError{Code: "history_query_failed", Message: "History query failed"}
		}
		return map[string]any{"requests": requests}, nil
	})
	server.Handle("history.stats", func(ctx context.Context, payload json.RawMessage) (any, error) {
		if history == nil {
			return nil, platform.MethodError{Code: "history_unavailable", Message: "History is unavailable"}
		}
		var query struct {
			Period application.Period `json:"period"`
		}
		if platform.DecodePayload(payload, &query) != nil {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Invalid history query"}
		}
		if !application.ValidPeriod(query.Period) {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Invalid history period"}
		}
		stats, err := history.Stats(ctx, query.Period)
		if err != nil {
			return nil, platform.MethodError{Code: "history_query_failed", Message: "History query failed"}
		}
		return stats, nil
	})
	service.OnChanged(func(request domain.Request) {
		if history != nil {
			history.Record(request)
		}
		_ = server.Emit("activity.changed", request)
	})
}

// withinOneFrame drops the oldest requests until the answer fits one frame.
//
// Rows are bounded in every field but one: the error detail carries the
// provider's own answer, up to the store's 4096-rune cap. During a provider
// incident that is exactly what the rows are full of, and measured - not
// guessed - a hundred such rows came to 351 KB against the 258 KB payload
// budget, so the workspace answered `response_too_large` and went dark at the
// moment it was needed most.
//
// Newest first, because List already returns them that way and the recent
// requests are the ones being acted on. `available` in the answer carries the
// pre-truncation count, so a short list is visible as a short list rather than
// passing for the whole buffer. No single row can outgrow the budget (12.5 KB
// worst case), so this never answers empty because the newest one alone did
// not fit.
func withinOneFrame(requests []domain.Request) []domain.Request {
	for len(requests) > 0 {
		encoded, err := json.Marshal(requests)
		if err != nil {
			return nil
		}
		if len(encoded) <= platform.MaxPayloadBytes {
			return requests
		}
		// Proportional, then verified: rows are within an order of magnitude of
		// each other, so one estimate normally lands; taking at least one off
		// guarantees this ends.
		next := len(requests) * platform.MaxPayloadBytes / len(encoded)
		requests = requests[:min(next, len(requests)-1)]
	}
	return requests
}
