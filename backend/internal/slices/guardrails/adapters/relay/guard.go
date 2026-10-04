// Package relayguardrail adapts the guardrails inspector to the small port the
// relay depends on. It is the only place the two slices meet, and it deliberately
// translates a rich decision into the one bit the relay is allowed to act on.
package relayguardrail

import (
	guardrailapp "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/application"
	guardraildomain "github.com/luxuryprivate/switchboard/backend/internal/slices/guardrails/domain"
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
		Secrets:             subject.Secrets,
	})
	if !decision.Blocked() {
		return relayapp.GuardrailVerdict{}
	}
	return relayapp.GuardrailVerdict{Blocked: true, Code: CodeBlocked}
}

func (guard *Guard) ClientDeclaredTools(requestBody []byte) bool {
	return guardrailapp.RequestDeclaresTools(requestBody)
}

// CanBlockFor reports whether Review can refuse an answer from this
// provider. Monitor mode records verdicts without enforcing them, so the
// relay may deliver a stream as it arrives and review it when it ends; a
// block override on the provider (or a global block) means the verdict
// decides, and the bytes are held back.
func (guard *Guard) CanBlockFor(providerID string) bool {
	return guard.inspector.ModeFor(providerID) == guardraildomain.ModeBlock
}
