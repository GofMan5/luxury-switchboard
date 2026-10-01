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
		return map[string]any{"findings": withinOneFrame(inspector.Records(query.Limit))}, nil
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

// withinOneFrame drops the oldest records until the answer fits one frame.
//
// The caller's limit counts records and the shell counts bytes, and one hostile
// answer carries as many findings as it triggered rules, so the two disagree by an
// order of magnitude: 200 records of ten findings each - measured, not guessed -
// came to over 256 KiB, and the workspace answered `response_too_large` every time
// it was opened. That made the findings page unreachable exactly when a provider
// was sending the most, and the only way back was to clear the evidence.
//
// Newest first, because Records already returns them that way and the recent
// answers are the ones being acted on. The count the operator compares against is
// `findingCount` in the status, which is never truncated, so a short list is
// visible as a short list rather than passing for the whole journal.
//
// Every field of a Record is bounded — 32 findings of a 160-byte match and a
// 200-byte excerpt, a 128-byte model, an 80-byte provider name — so the widest one
// that can exist is 26 KB and the budget holds 9 of those. That is the floor: a
// real flood measured 60. No single record can outgrow the budget, so this never
// answers empty because the newest one alone did not fit.
func withinOneFrame(records []application.Record) []application.Record {
	return platform.TrimToPayloadBudget(records)
}
