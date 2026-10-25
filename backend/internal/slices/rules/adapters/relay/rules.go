// Package relay adapts the rules service to the relay's RequestRules port.
package relay

import (
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/rules/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/rules/domain"
)

type Rules struct{ service *application.Service }

// New returns the relay's view of service.
func New(service *application.Service) *Rules { return &Rules{service: service} }

var _ relayapp.RequestRules = (*Rules)(nil)

func (rules *Rules) Evaluate(request relayapp.RuleRequest) relayapp.RuleOutcome {
	outcome := rules.service.Evaluate(application.Request{Model: request.Model, Endpoint: request.Endpoint})
	result := relayapp.RuleOutcome{
		ProviderID: outcome.Provider,
		Model:      outcome.Model,
		Headers:    outcome.Headers,
		Tags:       outcome.Tags,
		Budgeted:   outcome.Budgeted,
	}
	if outcome.Block != nil {
		result.Block = &relayapp.RuleBlock{Status: outcome.Block.Status, Message: outcome.Block.Message}
	}
	if outcome.Limit != nil {
		result.Limit = &relayapp.RuleLimit{RuleName: outcome.Limit.RuleName, RetryAfter: outcome.Limit.RetryAfter}
	}
	return result
}

func (rules *Rules) SpendTokens(ruleIDs []string, tokens int64) {
	rules.service.SpendTokens(ruleIDs, tokens)
}

func (rules *Rules) Retry(query relayapp.RetryQuery) (relayapp.RetryDecision, bool) {
	decision, ok := rules.service.Retry(domain.Failure{
		ProviderID: query.ProviderID,
		Model:      query.Model,
		Status:     query.Status,
		ErrorCode:  query.ErrorCode,
		ErrorText:  query.ErrorText,
	})
	if !ok {
		return relayapp.RetryDecision{}, false
	}
	return relayapp.RetryDecision{
		RuleID:      decision.RuleID,
		Action:      decision.Action,
		MaxAttempts: decision.MaxAttempts,
		Delay:       decision.Delay,
	}, true
}
