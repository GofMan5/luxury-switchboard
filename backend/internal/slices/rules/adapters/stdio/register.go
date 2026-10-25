package rulesstdio

import (
	"context"
	"encoding/json"
	"errors"
	pathpkg "path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/secretstore"
	platform "github.com/luxuryprivate/switchboard/backend/internal/platform/stdio"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/rules/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/rules/domain"
)

const (
	maxTestPath      = 2048
	maxTestModel     = 200
	maxTestErrorText = 2048
	maxTestProvider  = 64
	defaultRecent    = 50
	maxRecent        = 100
	storageMessage   = "Secure storage is unavailable. Start or unlock Linux Secret Service, run Switchboard without sudo, then restart it."
)

var testErrorCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type idRequest struct {
	ID    string `json:"id"`
	Actor string `json:"actor,omitempty"`
}

type reorderRequest struct {
	IDs   []string `json:"ids"`
	Actor string   `json:"actor,omitempty"`
}

type requestRuleRequest struct {
	Rule  *domain.RequestRule `json:"rule"`
	Actor string              `json:"actor,omitempty"`
}

type retryRuleRequest struct {
	Rule  *domain.RetryRule `json:"rule"`
	Actor string            `json:"actor,omitempty"`
}

type testRequest struct {
	Model      string `json:"model"`
	Path       string `json:"path"`
	Status     *int   `json:"status,omitempty"`
	ErrorText  string `json:"errorText,omitempty"`
	ErrorCode  string `json:"errorCode,omitempty"`
	ProviderID string `json:"providerId,omitempty"`
}

type recentRequest struct {
	Limit *int `json:"limit,omitempty"`
}

type deleted struct {
	Deleted bool `json:"deleted"`
}

// Register serves every rules.* method and announces saved changes as
// rules.changed. The plugin host stamps actor; the operator sends none.
func Register(server *platform.Server, service *application.Service) {
	service.OnChanged(func() { _ = server.Emit("rules.changed", struct{}{}) })
	server.Handle("rules.list", func(_ context.Context, payload json.RawMessage) (any, error) {
		if platform.DecodePayload(payload, &struct{}{}) != nil {
			return nil, malformed("Rules request is malformed")
		}
		return service.List(), nil
	})
	server.Handle("rules.saveRequestRule", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var request requestRuleRequest
		if platform.DecodePayload(payload, &request) != nil || request.Rule == nil {
			return nil, malformed("Request rule is malformed")
		}
		rule, err := service.SaveRequestRule(ctx, *request.Rule, request.Actor)
		if err != nil {
			return nil, methodError(err)
		}
		return rule, nil
	})
	server.Handle("rules.deleteRequestRule", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var request idRequest
		if platform.DecodePayload(payload, &request) != nil {
			return nil, malformed("Delete request is malformed")
		}
		if err := service.DeleteRequestRule(ctx, request.ID, request.Actor); err != nil {
			return nil, methodError(err)
		}
		return deleted{Deleted: true}, nil
	})
	server.Handle("rules.reorderRequestRules", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var request reorderRequest
		if platform.DecodePayload(payload, &request) != nil {
			return nil, malformed("Reorder request is malformed")
		}
		rules, err := service.ReorderRequestRules(ctx, request.IDs, request.Actor)
		if err != nil {
			return nil, methodError(err)
		}
		return struct {
			RequestRules []domain.RequestRule `json:"requestRules"`
		}{rules}, nil
	})
	server.Handle("rules.saveRetryRule", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var request retryRuleRequest
		if platform.DecodePayload(payload, &request) != nil || request.Rule == nil {
			return nil, malformed("Retry rule is malformed")
		}
		rule, err := service.SaveRetryRule(ctx, *request.Rule, request.Actor)
		if err != nil {
			return nil, methodError(err)
		}
		return rule, nil
	})
	server.Handle("rules.deleteRetryRule", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var request idRequest
		if platform.DecodePayload(payload, &request) != nil {
			return nil, malformed("Delete request is malformed")
		}
		if err := service.DeleteRetryRule(ctx, request.ID, request.Actor); err != nil {
			return nil, methodError(err)
		}
		return deleted{Deleted: true}, nil
	})
	server.Handle("rules.reorderRetryRules", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var request reorderRequest
		if platform.DecodePayload(payload, &request) != nil {
			return nil, malformed("Reorder request is malformed")
		}
		rules, err := service.ReorderRetryRules(ctx, request.IDs, request.Actor)
		if err != nil {
			return nil, methodError(err)
		}
		return struct {
			RetryRules []domain.RetryRule `json:"retryRules"`
		}{rules}, nil
	})
	server.Handle("rules.stats", func(_ context.Context, payload json.RawMessage) (any, error) {
		if platform.DecodePayload(payload, &struct{}{}) != nil {
			return nil, malformed("Statistics request is malformed")
		}
		return struct {
			Rules map[string]application.RuleStats `json:"rules"`
		}{service.Stats()}, nil
	})
	server.Handle("rules.test", func(_ context.Context, payload json.RawMessage) (any, error) {
		var request testRequest
		if platform.DecodePayload(payload, &request) != nil {
			return nil, malformed("Test request is malformed")
		}
		input, err := testInput(request)
		if err != nil {
			return nil, err
		}
		return service.Test(input), nil
	})
	server.Handle("rules.recentErrors", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var request recentRequest
		if platform.DecodePayload(payload, &request) != nil {
			return nil, malformed("Recent errors request is malformed")
		}
		limit := defaultRecent
		if request.Limit != nil {
			if *request.Limit < 1 || *request.Limit > maxRecent {
				return nil, malformed("Limit must be 1-100")
			}
			limit = *request.Limit
		}
		recent, err := service.RecentErrors(ctx, limit)
		if err != nil {
			return nil, platform.MethodError{Code: "history_unavailable", Message: "Request history is unavailable"}
		}
		return struct {
			Errors []application.RecentError `json:"errors"`
		}{platform.TrimToPayloadBudget(recent)}, nil
	})
}

// testInput validates a dry run and classifies its path the way the relay
// does: canonicalized first, so a spelling cannot test differently.
func testInput(request testRequest) (application.TestInput, error) {
	if !plainText(request.Path, maxTestPath, false) || request.Path == "" {
		return application.TestInput{}, malformed("Path must be 1-2048 characters")
	}
	if request.Model != "" && !plainText(request.Model, maxTestModel, false) {
		return application.TestInput{}, malformed("Model must be at most 200 characters")
	}
	input := application.TestInput{
		Model:    strings.TrimSpace(request.Model),
		Endpoint: relayapp.ClassifyEndpoint(strings.ToLower(pathpkg.Clean("/" + request.Path))),
	}
	if request.Status != nil {
		if *request.Status < 400 || *request.Status > 599 {
			return application.TestInput{}, malformed("Status must be 400-599")
		}
		input.Status = *request.Status
	}
	if request.ErrorText != "" {
		if !plainText(request.ErrorText, maxTestErrorText, true) {
			return application.TestInput{}, malformed("Error text must be at most 2048 characters")
		}
		input.ErrorText = request.ErrorText
	}
	if request.ErrorCode != "" {
		if !testErrorCodePattern.MatchString(request.ErrorCode) {
			return application.TestInput{}, malformed("Error code must be a lowercase error code")
		}
		input.ErrorCode = request.ErrorCode
	}
	if request.ProviderID != "" {
		if !plainText(request.ProviderID, maxTestProvider, false) {
			return application.TestInput{}, malformed("Provider id must be 1-64 characters")
		}
		input.ProviderID = request.ProviderID
	}
	return input, nil
}

// plainText reports whether value is valid UTF-8 of at most limit runes
// without control characters; multiline also admits tab and newline.
func plainText(value string, limit int, multiline bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > limit {
		return false
	}
	for _, character := range value {
		if multiline && (character == '\t' || character == '\n' || character == '\r') {
			continue
		}
		if character < 32 || character == 127 {
			return false
		}
	}
	return true
}

func malformed(message string) error {
	return platform.MethodError{Code: "invalid_payload", Message: message}
}

func methodError(err error) error {
	var validation domain.ValidationError
	switch {
	case errors.As(err, &validation):
		// The domain message names the field and the rule, never the value.
		return platform.MethodError{Code: "invalid_rule", Message: validation.Error()}
	case errors.Is(err, application.ErrNotFound):
		return platform.MethodError{Code: "rule_not_found", Message: "Rule was not found"}
	case errors.Is(err, application.ErrPermissionDenied):
		return platform.MethodError{Code: "permission_denied", Message: "This rule belongs to another owner"}
	case errors.Is(err, application.ErrStoreUnavailable) || errors.Is(err, secretstore.ErrUnavailable):
		return platform.MethodError{Code: "secure_storage_unavailable", Message: storageMessage}
	default:
		return platform.MethodError{Code: "rules_update_failed", Message: "Rules could not be saved"}
	}
}
