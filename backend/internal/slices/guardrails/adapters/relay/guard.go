// Package relayguardrail adapts the guardrails inspector to the small port the
// relay depends on. It is the only place the two slices meet, and it deliberately
// translates a rich decision into the one bit the relay is allowed to act on.
package relayguardrail

import (
	guardrailapp "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/application"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

// CodeBlocked is the activity label for a refused answer. It says that the
// guardrails refused, never which rule fired.
const CodeBlocked = "guardrail_blocked"

type Guard struct {
	inspector *guardrailapp.Inspector
}

func New(inspector *guardrailapp.Inspector) *Guard {
	return &Guard{inspector: inspector}
}

func (guard *Guard) Review(body []byte, eventStream bool, subject relayapp.GuardrailSubject) relayapp.GuardrailVerdict {
	decision := guard.inspector.Inspect(body, eventStream, guardrailapp.Subject{
		ProviderID:          subject.ProviderID,
		ProviderName:        subject.ProviderName,
		Model:               subject.Model,
		ClientDeclaredTools: subject.ClientDeclaredTools,
	})
	if !decision.Blocked() {
		return relayapp.GuardrailVerdict{}
	}
	return relayapp.GuardrailVerdict{Blocked: true, Code: CodeBlocked}
}

func (guard *Guard) ClientDeclaredTools(requestBody []byte) bool {
	return guardrailapp.RequestDeclaresTools(requestBody)
}
