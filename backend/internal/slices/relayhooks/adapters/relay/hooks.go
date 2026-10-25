// Package relayhooksrelay plugs the relay hooks service into the relay as its
// RequestHooks, translating between the two slices' types.
package relayhooksrelay

import (
	"context"
	"unicode/utf8"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/relayhooks/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/relayhooks/domain"
)

// Hooks is the relay's view of the relay hooks service.
type Hooks struct {
	service *application.Service
}

var _ relayapp.RequestHooks = Hooks{}

// New returns the relay's view of service.
func New(service *application.Service) Hooks { return Hooks{service: service} }

// Active reports whether a hook of phase would see the request.
func (hooks Hooks) Active(phase, model, endpoint string) bool {
	return hooks.service.Active(phase, model, endpoint)
}

// Before asks the request hooks; the zero decision continues unchanged.
func (hooks Hooks) Before(ctx context.Context, request relayapp.HookRequest) relayapp.HookDecision {
	decision := hooks.service.Before(ctx, hookRequest(request, true))
	switch decision.Action {
	case domain.ActionBlock:
		answer := relayapp.HookDecision{Action: relayapp.HookActionBlock, Tags: decision.Tags}
		if decision.Block != nil {
			answer.BlockStatus, answer.BlockMessage = decision.Block.Status, decision.Block.Message
		}
		return answer
	case domain.ActionRewrite:
		answer := relayapp.HookDecision{Action: relayapp.HookActionRewrite, Model: decision.Model, Tags: decision.Tags}
		if decision.Body != "" {
			answer.Body = []byte(decision.Body)
		}
		return answer
	}
	return relayapp.HookDecision{Action: relayapp.HookActionContinue, Tags: decision.Tags}
}

// After hands a finished request to the response observers without waiting.
func (hooks Hooks) After(observation relayapp.HookObservation) {
	response := domain.Response{
		Status: observation.Status, ErrorCode: observation.ErrorCode, LatencyMS: observation.LatencyMS,
	}
	if usage := observation.Tokens; usage != nil {
		response.Tokens = &domain.Tokens{Input: usage.InputTokens, Output: usage.OutputTokens, Total: usage.TotalTokens}
	}
	response.Body, response.BodyOmitted = text(observation.Body, observation.BodyOmitted)
	hooks.service.After(hookRequest(observation.Request, false), response)
}

func hookRequest(request relayapp.HookRequest, withBody bool) domain.Request {
	converted := domain.Request{
		ActivityID: request.ActivityID, Model: request.Model, Endpoint: request.Endpoint,
		Method: request.Method, Path: request.Path, Bytes: request.Bytes,
	}
	if withBody {
		converted.Body, converted.BodyOmitted = text(request.Body, request.BodyOmitted)
	}
	return converted
}

// text passes a body on only when it is UTF-8 within the size hooks see; any
// other body is omitted.
func text(body []byte, omitted bool) (string, bool) {
	if omitted || len(body) > domain.MaxBodyBytes || !utf8.Valid(body) {
		return "", omitted || len(body) > 0
	}
	return string(body), false
}
