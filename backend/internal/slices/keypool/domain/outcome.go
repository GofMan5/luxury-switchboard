package domain

import "time"

type OutcomeKind uint8

const (
	OutcomeSuccess OutcomeKind = iota
	OutcomeRateLimited
	OutcomeServerError
	OutcomeTransport
	OutcomeRequestError
	OutcomeModelUnavailable
	OutcomeBalanceExhausted
	OutcomeAuthentication
)

type Outcome struct {
	Kind       OutcomeKind
	Model      string
	RetryAfter time.Duration
}
