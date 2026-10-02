package application

// Guardrail reviews a finished provider answer before it reaches the client.
//
// The port speaks only in primitives so the relay stays independent of the
// guardrails slice: the relay needs to know whether to refuse an answer, not how
// that was decided. Rule identifiers, matches and excerpts stay on the far side
// of this boundary and never travel toward a client.
type Guardrail interface {
	// Review judges the final body in the dialect the client will actually read.
	// eventStream says whether the body is SSE.
	Review(body []byte, eventStream bool, subject GuardrailSubject) GuardrailVerdict
	// ClientDeclaredTools reports whether the request offered the model any tool.
	// A tool call in an answer to a request that declared none is an anomaly, and
	// recognising a declaration takes the same per-dialect knowledge as the review.
	ClientDeclaredTools(requestBody []byte) bool
	// CanBlock reports whether Review can ever refuse an answer. Monitor mode
	// cannot: its verdicts are recorded, never enforced, so a stream may be
	// delivered as it arrives and reviewed when it ends — the client sees the
	// same bytes either way, and time-to-first-token is the only thing
	// withholding them was buying. Block mode can refuse, and there the
	// answer is held back on purpose: the verdict decides whether the client
	// sees a byte at all.
	CanBlock() bool
}

// GuardrailSubject is the safe context of one answer: identifiers only — and
// the markers of the credential in flight, which travel so the journal can
// scrub them out of the evidence it keeps rather than echo them back in the
// findings UI.
type GuardrailSubject struct {
	ProviderID          string
	ProviderName        string
	Model               string
	ClientDeclaredTools bool
	Secrets             []string
}

// GuardrailVerdict is what the relay acts on.
type GuardrailVerdict struct {
	// Blocked means the answer must not reach the client.
	Blocked bool
	// Code is a short safe label for the activity record, set only when the answer
	// was refused. It never names a rule. Findings that were merely recorded stay
	// on the guardrails side, where they can be shown with their full safe detail.
	Code string
}

// NoopGuardrail is the behaviour when no guardrail is wired: forward everything.
type NoopGuardrail struct{}

func (NoopGuardrail) Review([]byte, bool, GuardrailSubject) GuardrailVerdict {
	return GuardrailVerdict{}
}

// Without a guardrail the answer is forwarded either way, so the cheaper answer
// is the honest one: nothing is being compared.
func (NoopGuardrail) ClientDeclaredTools([]byte) bool { return true }

// Nothing is being compared, so nothing can be refused.
func (NoopGuardrail) CanBlock() bool { return false }
