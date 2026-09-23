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

type AttemptOutcome struct {
	Kind       AttemptKind
	Model      string
	RetryAfter time.Duration
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
