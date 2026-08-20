package guardrailstdio

import (
	"context"
	"encoding/json"

	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/application"
)

// Register exposes the guardrails to the desktop shell. The rules themselves stay
// inside the binary: the operator sees how many are loaded and what they caught,
// never the patterns, so a report cannot be turned into an evasion guide.
//
// These commands belong to both editions. A public user has the same right to
// know what a provider sent them as the owner does.
func Register(server *platform.Server, inspector *application.Inspector) {
	server.Handle("guardrails.status", func(_ context.Context, _ json.RawMessage) (any, error) {
		return status(inspector), nil
	})
	server.Handle("guardrails.findings", func(_ context.Context, payload json.RawMessage) (any, error) {
		var query struct {
			Limit int `json:"limit"`
		}
		if platform.DecodePayload(payload, &query) != nil {
			return nil, platform.MethodError{Code: "invalid_payload", Message: "Invalid guardrail query"}
		}
		if query.Limit == 0 {
			query.Limit = 100
		}
		return map[string]any{"findings": inspector.Records(query.Limit)}, nil
	})
	server.Handle("guardrails.clear", func(_ context.Context, _ json.RawMessage) (any, error) {
		inspector.Clear()
		_ = server.Emit("guardrails.changed", status(inspector))
		return map[string]bool{"cleared": true}, nil
	})
	inspector.OnRecord(func(record application.Record) {
		_ = server.Emit("guardrails.finding", record)
	})
}

func status(inspector *application.Inspector) map[string]any {
	return map[string]any{
		"mode":           string(inspector.Mode()),
		"ruleCount":      inspector.RuleCount(),
		"indicatorCount": inspector.IndicatorCount(),
		"ruleSetVersion": inspector.RuleSetVersion(),
		"findingCount":   len(inspector.Records(0)),
	}
}
