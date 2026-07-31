package application

import (
	"context"
	"time"
)

type Credential struct {
	Value    string
	ProxyURL string
}

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
}
