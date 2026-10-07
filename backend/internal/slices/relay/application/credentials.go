package application

import (
	"context"
	"time"
)

type Credential struct {
	Value    string
	ProxyURL string
}

// MinRedactableMarkerBytes is the shortest value worth carrying as a sensitive
// marker. Anything shorter cannot be replaced in an answer without also rewriting
// ordinary prose, and the public sanitizer refuses any answer a marker survives in —
// so a marker below this width can only ever reject, and it rejects on words like
// "constructor" or "detail". Secrets are exempt: leaking a key is worse than
// refusing everything, so a short credential still refuses. Identifiers are not,
// which is why this width exists rather than the rule being "refuse on anything".
const MinRedactableMarkerBytes = 4

type AttemptKind uint8

const (
	AttemptSuccess AttemptKind = iota
	AttemptRateLimited
	AttemptServerError
	AttemptTransport
	AttemptRequestError
	AttemptModelUnavailable
	AttemptBalanceExhausted
	AttemptAuthentication
)

// AttemptOutcome is one finished upstream attempt's verdict. ErrorCode and
// ErrorMessage carry the provider's own error object when the refusal was
// an authentication one: relay fills them with the code and message the
// provider named, without interpreting either — which verdicts (if any) a
// specific credential source should act on is that source's decision, not
// the relay's. Both stay empty for every other kind; an authentication
// refusal whose body named no code carries none, which reads as "no
// verdict about the token family".
type AttemptOutcome struct {
	Kind       AttemptKind
	Model      string
	RetryAfter time.Duration
	// ErrorCode is the provider's error code, trimmed of nothing and
	// lowercased by nobody: verbatim as the JSON named it.
	ErrorCode string
	// ErrorMessage is the provider's error message, verbatim.
	ErrorMessage string
}

type CredentialLease interface {
	Credential() Credential
	Finish(AttemptOutcome)
}

type CredentialSource interface {
	Acquire(context.Context, string, string, func()) (CredentialLease, time.Duration, error)
	// TryAcquire takes a key only when one is dispatchable right now, without
	// queueing. The retry loop uses it once it is already rotating on a
	// rejection: the first attempt waited its fair turn, later ones must not
	// park on the cooldowns other requests left behind.
	TryAcquire(providerID, model string) (CredentialLease, bool)
}

// CredentialCounter is the optional half of a CredentialSource that knows how
// many keys a provider holds. The relay uses it to stop rotating once every key
// has rejected the request the same way.
type CredentialCounter interface {
	Count(providerID string) int
}
