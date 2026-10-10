package relay

import (
	"context"
	"errors"
	"time"

	codexapp "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
	"github.com/luxuryprivate/switchboard/backend/internal/slices/codex/domain"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

// errNotCodex reports a request for a provider the token source does not
// serve. The codex bearer token must never travel to another upstream.
var errNotCodex = errors.New("provider is not the codex provider")

// defaultTryAcquireBudget bounds the acquire TryAcquire performs. The fast
// path — a usable token served from memory — returns in microseconds, so the
// bound never touches it; what it caps is the slow path: a token that must be
// refreshed costs a network round trip budgeted at 30s on its own, and the
// refresh cycle's queue can hold a caller longer still. A rotation that
// cannot get its token within the bound ends on the rejection it already
// carries. Three seconds sits far above the memory read and far below any
// honest refresh wait.
const defaultTryAcquireBudget = 3 * time.Second

// credentialService is the codex session surface the token source needs.
type credentialService interface {
	Status() codexapp.Status
	AcquireAccessToken(ctx context.Context, accountID string) (string, error)
	// InvalidateAccessToken names the access token the attempt used, so
	// only the account that served it rotates; the other accounts' tokens
	// stay untouched.
	InvalidateAccessToken(accessToken string)
	// RejectAccessToken names the access token the upstream itself
	// revoked: that account moves to reauth-needed, because a refresh
	// that mints another token from the same chain only serves the
	// next doomed request.
	RejectAccessToken(rejectedAccessToken string)
}

// TokenSource serves the codex session's OAuth token as the relay credential
// for the "codex" provider.
type TokenSource struct {
	service credentialService

	// tryAcquireBudget caps how long TryAcquire's acquire may run. It is
	// fixed at construction from defaultTryAcquireBudget; tests shrink it
	// so timing assertions stay deterministic.
	tryAcquireBudget time.Duration
}

// NewTokenSource wires the token source to the codex service.
func NewTokenSource(service credentialService) *TokenSource {
	return &TokenSource{service: service, tryAcquireBudget: defaultTryAcquireBudget}
}

var _ relayapp.CredentialSource = (*TokenSource)(nil)
var _ relayapp.CredentialCounter = (*TokenSource)(nil)

// Acquire returns the account's access token as the credential for a codex
// request. The providerID is the routing itself — every signed-in account
// provisions its own provider entry — so it is resolved to the account that
// serves it, and a provider nobody serves is refused: the codex bearer
// token must never travel to another upstream, and the fallback holds no
// credential for a codex entry. The token is never queued for —
// AcquireAccessToken does its own waiting — so waiting is never invoked and
// the reported wait is zero. Session errors (not signed in, refresh failed)
// propagate verbatim so the relay can classify them.
func (source *TokenSource) Acquire(ctx context.Context, providerID, model string, waiting func()) (relayapp.CredentialLease, time.Duration, error) {
	account, ok := source.accountFor(providerID)
	if !ok {
		return nil, 0, errNotCodex
	}
	token, err := source.service.AcquireAccessToken(ctx, account.AccountID)
	if err != nil {
		return nil, 0, err
	}
	return &tokenLease{service: source.service, token: token}, 0, nil
}

// TryAcquire takes the token only when the account can serve one right now.
// The acquire runs under a short deadline: a usable token is served from
// memory without any waiting, so a request already rotating on a rejection
// gets its answer in memory-read time; the deadline caps only the slow path —
// a token that must be refreshed, or a queue behind a refresh already in
// flight. A deadline hit is the token being not available now, exactly like
// any session error: the rotation ends on the rejection it already carries
// instead of queueing behind the refresh. A refresh that completes inside
// the budget is still served.
func (source *TokenSource) TryAcquire(providerID, model string) (relayapp.CredentialLease, bool) {
	account, ok := source.accountFor(providerID)
	if !ok {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), source.tryAcquireBudget)
	defer cancel()
	token, err := source.service.AcquireAccessToken(ctx, account.AccountID)
	if err != nil {
		return nil, false
	}
	return &tokenLease{service: source.service, token: token}, true
}

// Count reports how many credentials the source can serve for providerID.
// The signed-in account is one credential, but a rejected token can be
// refreshed once, so it counts as two: the relay's rotation budget allows
// one bad token followed by one refreshed token, and then gives up. An
// account that needs re-auth serves zero — its chain is dead — and a
// provider nobody serves is not this source's problem at all.
func (source *TokenSource) Count(providerID string) int {
	account, ok := source.accountFor(providerID)
	if !ok || account.State != codexapp.StateSignedIn {
		return 0
	}
	return 2
}

// tokenLease holds the acquired token and the session it came from.
type tokenLease struct {
	service credentialService
	token   string
}

// Credential exposes the leased token. The token is a secret: it travels
// only on the upstream Authorization header, never into errors or logs.
func (lease *tokenLease) Credential() relayapp.Credential {
	return relayapp.Credential{Value: lease.token}
}

// Finish reports the attempt outcome. An authentication failure
// invalidates the token, so the next acquire refreshes it — unless the
// provider's own error named the access token itself as revoked: that
// verdict survives a refresh, so the session moves to reauth-needed
// instead of minting the next request another doomed token. The code and
// message arrive verbatim from the relay; reading them is this slice's
// call, the relay stays interpretation-free. Any other outcome (rate
// limit, server error, plain success) leaves the session alone.
func (lease *tokenLease) Finish(outcome relayapp.AttemptOutcome) {
	if outcome.Kind != relayapp.AttemptAuthentication {
		return
	}
	if domain.IsAccessTokenRevocation(outcome.ErrorCode, outcome.ErrorMessage) {
		lease.service.RejectAccessToken(lease.token)
		return
	}
	lease.service.InvalidateAccessToken(lease.token)
}

// DelegatingSource is the surface the composite needs from the source it
// falls back to: acquiring and releasing credentials, plus the rotation
// count that bounds how often a single request rotates.
type DelegatingSource interface {
	relayapp.CredentialSource
	relayapp.CredentialCounter
}

// CompositeSource routes codex requests to the OAuth token and every other
// provider to the fallback source. The codex route never falls back: the
// key pool holds no credential for the codex provider, so a failed codex
// acquire fails the request instead of borrowing a key.
type CompositeSource struct {
	codex    *TokenSource
	fallback DelegatingSource
}

// NewCompositeSource wires the composite credential pool.
func NewCompositeSource(codex *TokenSource, fallback DelegatingSource) *CompositeSource {
	return &CompositeSource{codex: codex, fallback: fallback}
}

var _ relayapp.CredentialSource = (*CompositeSource)(nil)
var _ relayapp.CredentialCounter = (*CompositeSource)(nil)

// Acquire routes providerID to the token source or the fallback source.
func (source *CompositeSource) Acquire(ctx context.Context, providerID, model string, waiting func()) (relayapp.CredentialLease, time.Duration, error) {
	if source.codex.routesToCodex(providerID) {
		return source.codex.Acquire(ctx, providerID, model, waiting)
	}
	return source.fallback.Acquire(ctx, providerID, model, waiting)
}

// TryAcquire routes providerID to the token source or the fallback source.
func (source *CompositeSource) TryAcquire(providerID, model string) (relayapp.CredentialLease, bool) {
	if source.codex.routesToCodex(providerID) {
		return source.codex.TryAcquire(providerID, model)
	}
	return source.fallback.TryAcquire(providerID, model)
}

// Count routes providerID to the token source or the fallback source.
func (source *CompositeSource) Count(providerID string) int {
	if source.codex.routesToCodex(providerID) {
		return source.codex.Count(providerID)
	}
	return source.fallback.Count(providerID)
}

// routesToCodex reports whether providerID is a codex account's provider
// entry: the provisioner registers one per signed-in account, and the
// relay dispatches them by name, so the entry set in Status() is the
// routing table. A codex catalog entry nobody currently serves is not
// here — it routes to the fallback, which holds no credential for it, and
// the request fails rather than borrowing a key.
func (source *TokenSource) routesToCodex(providerID string) bool {
	_, ok := source.accountFor(providerID)
	return ok
}

// accountFor resolves the provider entry the relay dispatches by name to
// the account that serves it: one entry, one account, one token chain.
// The scan is linear over the account rows — a human-sized list — and the
// resolution is one in-memory read on the request path.
func (source *TokenSource) accountFor(providerID string) (codexapp.AccountStatus, bool) {
	for _, account := range source.service.Status().Accounts {
		if account.ProviderID == providerID {
			return account, true
		}
	}
	return codexapp.AccountStatus{}, false
}
