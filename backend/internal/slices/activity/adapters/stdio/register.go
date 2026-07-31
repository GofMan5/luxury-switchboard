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
		if len(payload) > 0 && json.Unmarshal(payload, &query) != nil {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Invalid activity query"}
		}
		if query.Limit == 0 {
			query.Limit = 100
		}
		return map[string]any{"requests": service.List(query.Limit)}, nil
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
