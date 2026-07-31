package keypool

import (
	"context"
	"time"

	keyapp "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/application"
	keydomain "github.com/luxuryprivate/switchboard/backend/internal/slices/keypool/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

type Source struct {
	scheduler *keyapp.Scheduler
}

func NewSource(scheduler *keyapp.Scheduler) *Source {
	return &Source{scheduler: scheduler}
}

func (source *Source) Acquire(ctx context.Context, providerID, model string, waiting func()) (relayapp.CredentialLease, time.Duration, error) {
	lease, waited, err := source.scheduler.AcquireWithQueue(ctx, providerID, model, waiting)
	if err != nil {
		return nil, waited, err
	}
	return &leaseAdapter{lease: lease}, waited, nil
}

type leaseAdapter struct {
	lease *keyapp.Lease
}

func (adapter *leaseAdapter) Credential() relayapp.Credential {
	key := adapter.lease.Key()
	return relayapp.Credential{Value: key.Credential.Reveal(), ProxyURL: key.ProxyURL}
}

func (adapter *leaseAdapter) Finish(outcome relayapp.AttemptOutcome) {
	adapter.lease.Finish(keydomain.Outcome{
		Kind:       outcomeKind(outcome.Kind),
		Model:      outcome.Model,
		RetryAfter: outcome.RetryAfter,
	})
}

func outcomeKind(kind relayapp.AttemptKind) keydomain.OutcomeKind {
	switch kind {
	case relayapp.AttemptRateLimited:
		return keydomain.OutcomeRateLimited
	case relayapp.AttemptServerError:
		return keydomain.OutcomeServerError
	case relayapp.AttemptTransport:
		return keydomain.OutcomeTransport
	case relayapp.AttemptRequestError:
		return keydomain.OutcomeRequestError
	case relayapp.AttemptModelUnavailable:
		return keydomain.OutcomeModelUnavailable
	case relayapp.AttemptBalanceExhausted:
		return keydomain.OutcomeBalanceExhausted
	case relayapp.AttemptAuthentication:
		return keydomain.OutcomeAuthentication
	default:
		return keydomain.OutcomeSuccess
	}
}
