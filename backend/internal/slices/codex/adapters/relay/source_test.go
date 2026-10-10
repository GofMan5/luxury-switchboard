package relay

import (
	"context"
	"errors"
	"testing"
	"time"

	codexapp "github.com/luxuryprivate/switchboard/backend/internal/slices/codex/application"
	relaykeypool "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/adapters/keypool"
	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

// The narrow session surface is a real subset of the codex service.
var _ credentialService = (*codexapp.Service)(nil)

// The fallback the composition root passes is the key pool source.
var _ DelegatingSource = (*relaykeypool.Source)(nil)

type ctxKey struct{}

// fakeService serves a canned token and records what the source did to it.
type fakeService struct {
	status       codexapp.Status
	token        string
	acquireErr   error
	acquireCalls []context.Context
	acquiredIDs  []string
	invalidated  []string
	rejected     []string
}

func (fake *fakeService) Status() codexapp.Status {
	return fake.status
}

func (fake *fakeService) AcquireAccessToken(ctx context.Context, accountID string) (string, error) {
	fake.acquireCalls = append(fake.acquireCalls, ctx)
	fake.acquiredIDs = append(fake.acquiredIDs, accountID)
	if fake.acquireErr != nil {
		return "", fake.acquireErr
	}
	return fake.token, nil
}

func (fake *fakeService) InvalidateAccessToken(accessToken string) {
	fake.invalidated = append(fake.invalidated, accessToken)
}

func (fake *fakeService) RejectAccessToken(rejectedAccessToken string) {
	fake.rejected = append(fake.rejected, rejectedAccessToken)
}

// fakeFallback stands in for the key pool and records every routing decision.
type fakeFallback struct {
	acquiredFor []string
	triedFor    []string
	countedFor  []string
}

func (fake *fakeFallback) Acquire(ctx context.Context, providerID, model string, waiting func()) (relayapp.CredentialLease, time.Duration, error) {
	fake.acquiredFor = append(fake.acquiredFor, providerID)
	if waiting != nil {
		waiting()
	}
	return fallbackLease{}, 3 * time.Second, nil
}

func (fake *fakeFallback) TryAcquire(providerID, model string) (relayapp.CredentialLease, bool) {
	fake.triedFor = append(fake.triedFor, providerID)
	return fallbackLease{}, true
}

func (fake *fakeFallback) Count(providerID string) int {
	fake.countedFor = append(fake.countedFor, providerID)
	return 7
}

type fallbackLease struct{}

func (fallbackLease) Credential() relayapp.Credential {
	return relayapp.Credential{Value: "key-1"}
}

func (fallbackLease) Finish(outcome relayapp.AttemptOutcome) {}

// accountRow is one signed-in account's routing row: the provider entry
// its provisioner registered, and the account the entry serves.
func accountRow(providerID, accountID string, state codexapp.ConnState) codexapp.AccountStatus {
	return codexapp.AccountStatus{AccountID: accountID, ProviderID: providerID, State: state}
}

func signedInService(token string) *fakeService {
	return &fakeService{
		status: codexapp.Status{
			State:    codexapp.StateSignedIn,
			Accounts: []codexapp.AccountStatus{accountRow(codexapp.CodexProviderID, "account-1", codexapp.StateSignedIn)},
		},
		token: token,
	}
}

func TestAcquirePassesTheRequestContextThrough(t *testing.T) {
	service := signedInService("tok-1")
	source := NewTokenSource(service)
	ctx := context.WithValue(context.Background(), ctxKey{}, "request")
	waiting := func() { t.Error("waiting was invoked for the codex token") }

	lease, wait, err := source.Acquire(ctx, codexapp.CodexProviderID, "gpt-5", waiting)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if len(service.acquireCalls) != 1 {
		t.Fatalf("acquire calls = %d, want 1", len(service.acquireCalls))
	}
	if got, ok := service.acquireCalls[0].Value(ctxKey{}).(string); !ok || got != "request" {
		t.Errorf("the session saw context %v, want the request context", service.acquireCalls[0])
	}
	if wait != 0 {
		t.Errorf("wait = %v, want 0 (the token is never queued for)", wait)
	}
	if lease == nil {
		t.Fatal("lease = nil, want a token lease")
	}
	if credential := lease.Credential(); credential != (relayapp.Credential{Value: "tok-1"}) {
		t.Errorf("credential = %+v, want the session token", credential)
	}
}

func TestAcquireReturnsSessionErrorsVerbatim(t *testing.T) {
	sessionErr := errors.New("codex session is not signed in")
	service := signedInService("tok-1")
	service.acquireErr = sessionErr
	source := NewTokenSource(service)

	lease, wait, err := source.Acquire(context.Background(), codexapp.CodexProviderID, "gpt-5", nil)
	if err != sessionErr {
		t.Errorf("err = %v, want the identical session error", err)
	}
	if lease != nil {
		t.Errorf("lease = %v, want nil", lease)
	}
	if wait != 0 {
		t.Errorf("wait = %v, want 0", wait)
	}
}

func TestTryAcquireTakesTheTokenWithoutWaiting(t *testing.T) {
	service := signedInService("tok-1")
	source := NewTokenSource(service)

	started := time.Now()
	lease, ok := source.TryAcquire(codexapp.CodexProviderID, "gpt-5")
	elapsed := time.Since(started)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if credential := lease.Credential(); credential != (relayapp.Credential{Value: "tok-1"}) {
		t.Errorf("credential = %+v, want the session token", credential)
	}
	// A fresh token is a memory read: it must beat the budget by miles,
	// not merely fit inside it.
	if elapsed >= source.tryAcquireBudget {
		t.Errorf("TryAcquire took %v, want the memory-read fast path, not the budget", elapsed)
	}
	if len(service.acquireCalls) != 1 {
		t.Fatalf("acquire calls = %d, want 1", len(service.acquireCalls))
	}
	// The acquire runs on a fresh background context — but a budgeted one,
	// so a slow session cannot hold the rotation forever.
	deadline, hasDeadline := service.acquireCalls[0].Deadline()
	if !hasDeadline {
		t.Fatal("the acquire ran without a deadline, want the budgeted context")
	}
	if remain := time.Until(deadline); remain <= 0 || remain > source.tryAcquireBudget {
		t.Errorf("the acquire's deadline leaves %v of a %v budget, want the whole budget", remain, source.tryAcquireBudget)
	}
}

// blockingService is the session mid-refresh: the acquire cannot return
// until the caller's context gives up, which is how a network refresh — and
// the queue behind one — behaves on the slow path.
type blockingService struct {
	acquireCalls []context.Context
}

func (fake *blockingService) Status() codexapp.Status {
	return codexapp.Status{
		State:    codexapp.StateSignedIn,
		Accounts: []codexapp.AccountStatus{accountRow(codexapp.CodexProviderID, "account-1", codexapp.StateSignedIn)},
	}
}

func (fake *blockingService) AcquireAccessToken(ctx context.Context, accountID string) (string, error) {
	fake.acquireCalls = append(fake.acquireCalls, ctx)
	<-ctx.Done()
	return "", ctx.Err()
}

func (fake *blockingService) InvalidateAccessToken(accessToken string) {}

func (fake *blockingService) RejectAccessToken(rejectedAccessToken string) {}

func TestTryAcquireDoesNotWaitBehindARefresh(t *testing.T) {
	service := &blockingService{}
	source := NewTokenSource(service)
	// The production budget is seconds; the test shrinks it so the bound
	// proves itself without the suite waiting it out.
	source.tryAcquireBudget = 20 * time.Millisecond

	started := time.Now()
	lease, ok := source.TryAcquire(codexapp.CodexProviderID, "gpt-5")
	elapsed := time.Since(started)

	if lease != nil || ok {
		t.Errorf("TryAcquire = (%v, %v), want (nil, false): a token behind a refresh is not available now", lease, ok)
	}
	if elapsed >= time.Second {
		t.Errorf("TryAcquire took %v, want the 20ms budget, not the refresh's wait", elapsed)
	}
	if len(service.acquireCalls) != 1 {
		t.Fatalf("acquire calls = %d, want 1", len(service.acquireCalls))
	}
	if _, hasDeadline := service.acquireCalls[0].Deadline(); !hasDeadline {
		t.Error("the acquire ran without a deadline, want the budgeted context")
	}
}

func TestTryAcquireRefusesWhenTheSessionCannotServe(t *testing.T) {
	service := signedInService("tok-1")
	service.acquireErr = errors.New("refresh failed")
	source := NewTokenSource(service)

	lease, ok := source.TryAcquire(codexapp.CodexProviderID, "gpt-5")
	if lease != nil || ok {
		t.Errorf("TryAcquire = (%v, %v), want (nil, false)", lease, ok)
	}
}

func TestOtherProvidersAreRefusedByTheTokenSource(t *testing.T) {
	service := signedInService("tok-1")
	source := NewTokenSource(service)

	if _, _, err := source.Acquire(context.Background(), "acme", "claude-3", nil); !errors.Is(err, errNotCodex) {
		t.Errorf("Acquire err = %v, want errNotCodex", err)
	}
	if lease, ok := source.TryAcquire("acme", "claude-3"); lease != nil || ok {
		t.Errorf("TryAcquire = (%v, %v), want (nil, false)", lease, ok)
	}
	if count := source.Count("acme"); count != 0 {
		t.Errorf("Count = %d, want 0", count)
	}
	if len(service.acquireCalls) != 0 {
		t.Errorf("the session saw %d acquire calls, want 0", len(service.acquireCalls))
	}
}

func TestCountReflectsThatAccountState(t *testing.T) {
	cases := []struct {
		providerID string
		state      codexapp.ConnState
		want       int
	}{
		{codexapp.CodexProviderID, codexapp.StateSignedIn, 2},
		{codexapp.CodexProviderID, codexapp.StateSignedOut, 0},
		{codexapp.CodexProviderID, codexapp.StateReauthNeeded, 0},
	}
	for _, testCase := range cases {
		service := &fakeService{
			status: codexapp.Status{State: testCase.state, Accounts: []codexapp.AccountStatus{accountRow(testCase.providerID, "account-1", testCase.state)}},
			token:  "tok-1",
		}
		source := NewTokenSource(service)
		if count := source.Count(testCase.providerID); count != testCase.want {
			t.Errorf("Count(%q, %q) = %d, want %d", testCase.providerID, testCase.state, count, testCase.want)
		}
	}
}

// Every account is its own route: acquiring through the second account's
// provider entry asks the session for the second account's token, and the
// count follows that entry alone, so one broken account cannot starve or
// inflate another's rotation budget.
func TestEachAccountServesItsOwnProviderEntry(t *testing.T) {
	service := &fakeService{
		status: codexapp.Status{
			State: codexapp.StateSignedIn,
			Accounts: []codexapp.AccountStatus{
				accountRow(codexapp.CodexProviderID, "account-1", codexapp.StateSignedIn),
				accountRow(codexapp.CodexProviderID+"2", "account-2", codexapp.StateSignedIn),
			},
		},
		token: "tok-1",
	}
	source := NewTokenSource(service)

	for _, providerID := range []string{codexapp.CodexProviderID + "2", codexapp.CodexProviderID} {
		if _, _, err := source.Acquire(context.Background(), providerID, "gpt-5", nil); err != nil {
			t.Fatalf("Acquire(%q): %v", providerID, err)
		}
	}
	if ids := service.acquiredIDs; len(ids) != 2 || ids[0] != "account-2" || ids[1] != "account-1" {
		t.Errorf("acquired account ids = %v, want the entry's own account each time", ids)
	}

	if count := source.Count(codexapp.CodexProviderID + "2"); count != 2 {
		t.Errorf("Count(codex2) = %d, want 2", count)
	}
	if count := source.Count(codexapp.CodexProviderID); count != 2 {
		t.Errorf("Count(codex) = %d, want 2", count)
	}
}

// The routing is the account rows, not a remembered provider name: a
// codex catalog entry nobody serves is refused outright, without the
// session ever being asked for a token.
func TestAProviderEntryNobodyServesIsRefused(t *testing.T) {
	service := &fakeService{status: codexapp.Status{State: codexapp.StateSignedOut}, token: "tok-1"}
	source := NewTokenSource(service)

	if _, _, err := source.Acquire(context.Background(), codexapp.CodexProviderID, "gpt-5", nil); !errors.Is(err, errNotCodex) {
		t.Errorf("Acquire err = %v, want errNotCodex", err)
	}
	if lease, ok := source.TryAcquire(codexapp.CodexProviderID, "gpt-5"); lease != nil || ok {
		t.Errorf("TryAcquire = (%v, %v), want (nil, false)", lease, ok)
	}
	if count := source.Count(codexapp.CodexProviderID); count != 0 {
		t.Errorf("Count = %d, want 0", count)
	}
	if len(service.acquireCalls) != 0 {
		t.Errorf("the session saw %d acquire calls, want 0", len(service.acquireCalls))
	}
}

func TestOnlyAuthenticationFailuresInvalidateTheToken(t *testing.T) {
	cases := []relayapp.AttemptKind{
		relayapp.AttemptSuccess,
		relayapp.AttemptRateLimited,
		relayapp.AttemptServerError,
		relayapp.AttemptTransport,
		relayapp.AttemptRequestError,
		relayapp.AttemptModelUnavailable,
		relayapp.AttemptBalanceExhausted,
		relayapp.AttemptAuthentication,
	}
	for _, kind := range cases {
		service := signedInService("tok-1")
		source := NewTokenSource(service)
		lease, _, err := source.Acquire(context.Background(), codexapp.CodexProviderID, "gpt-5", nil)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		lease.Finish(relayapp.AttemptOutcome{Kind: kind})
		want := 0
		if kind == relayapp.AttemptAuthentication {
			want = 1
		}
		if len(service.invalidated) != want {
			t.Errorf("outcome %q invalidated %d times, want %d", kind, len(service.invalidated), want)
		}
	}
}

// A revocation verdict arrives only through the pair the relay extracted;
// without that pair the source behaves exactly as before, so the generic
// 401 (no code in the body) keeps refreshing instead of signing out.
func TestARevokedAccessTokenReauthsInsteadOfInvalidating(t *testing.T) {
	service := signedInService("tok-1")
	source := NewTokenSource(service)
	lease, _, err := source.Acquire(context.Background(), codexapp.CodexProviderID, "gpt-5", nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	lease.Finish(relayapp.AttemptOutcome{Kind: relayapp.AttemptAuthentication, ErrorCode: "token_revoked", ErrorMessage: "access token revoked"})

	if len(service.rejected) != 1 || service.rejected[0] != "tok-1" {
		t.Errorf("rejected %v, want exactly the leased token [tok-1]", service.rejected)
	}
	if len(service.invalidated) != 0 {
		t.Errorf("invalidated %d times, want 0: a revocation supersedes the plain invalidation, not doubles it", len(service.invalidated))
	}
}

// The verdict's unit is the token the lease served, and the code the
// provider named is whatever it spelled: token_invalidated is the same
// verdict worded differently, and the rejected call must carry the
// leased token, not a guess.
func TestTokenInvalidatedIsTheSameRevocationVerdict(t *testing.T) {
	service := signedInService("tok-2")
	source := NewTokenSource(service)
	lease, _, err := source.Acquire(context.Background(), codexapp.CodexProviderID, "gpt-5", nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	lease.Finish(relayapp.AttemptOutcome{Kind: relayapp.AttemptAuthentication, ErrorCode: "token_invalidated"})

	if len(service.rejected) != 1 || service.rejected[0] != "tok-2" {
		t.Errorf("rejected %v, want [tok-2]", service.rejected)
	}
	if len(service.invalidated) != 0 {
		t.Errorf("invalidated %d times, want 0", len(service.invalidated))
	}
}

// The revocation codes ride the authentication verdict only; on every
// other outcome the pair is inert, even when the codes sit right there in
// the struct.
func TestRevocationCodesAreInertOutsideAuthentication(t *testing.T) {
	service := signedInService("tok-1")
	source := NewTokenSource(service)
	lease, _, err := source.Acquire(context.Background(), codexapp.CodexProviderID, "gpt-5", nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	lease.Finish(relayapp.AttemptOutcome{Kind: relayapp.AttemptRequestError, ErrorCode: "token_revoked", ErrorMessage: "revoked"})

	if len(service.rejected) != 0 {
		t.Errorf("rejected %v, want none", service.rejected)
	}
	if len(service.invalidated) != 0 {
		t.Errorf("invalidated %d times, want 0", len(service.invalidated))
	}
}

func TestTheCompositeRoutesCodexToTheSessionOnly(t *testing.T) {
	service := signedInService("tok-1")
	fallback := &fakeFallback{}
	source := NewCompositeSource(NewTokenSource(service), fallback)
	waiting := func() { t.Error("waiting was invoked for the codex token") }

	lease, wait, err := source.Acquire(context.Background(), codexapp.CodexProviderID, "gpt-5", waiting)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if wait != 0 {
		t.Errorf("wait = %v, want 0", wait)
	}
	if credential := lease.Credential(); credential != (relayapp.Credential{Value: "tok-1"}) {
		t.Errorf("credential = %+v, want the session token", credential)
	}
	if quickLease, ok := source.TryAcquire(codexapp.CodexProviderID, "gpt-5"); !ok {
		t.Error("TryAcquire ok = false, want true")
	} else if credential := quickLease.Credential(); credential != (relayapp.Credential{Value: "tok-1"}) {
		t.Errorf("TryAcquire credential = %+v, want the session token", credential)
	}
	if count := source.Count(codexapp.CodexProviderID); count != 2 {
		t.Errorf("Count = %d, want 2", count)
	}
	if len(fallback.acquiredFor)+len(fallback.triedFor)+len(fallback.countedFor) != 0 {
		t.Errorf("the fallback was consulted: %v", fallback)
	}
}

func TestACodexFailureNeverFallsBack(t *testing.T) {
	sessionErr := errors.New("codex session is not signed in")
	service := signedInService("tok-1")
	service.acquireErr = sessionErr
	fallback := &fakeFallback{}
	source := NewCompositeSource(NewTokenSource(service), fallback)

	if _, _, err := source.Acquire(context.Background(), codexapp.CodexProviderID, "gpt-5", nil); err != sessionErr {
		t.Errorf("Acquire err = %v, want the identical session error", err)
	}
	if lease, ok := source.TryAcquire(codexapp.CodexProviderID, "gpt-5"); lease != nil || ok {
		t.Errorf("TryAcquire = (%v, %v), want (nil, false)", lease, ok)
	}
	if len(fallback.acquiredFor)+len(fallback.triedFor) != 0 {
		t.Errorf("the fallback was consulted: %v", fallback)
	}
}

func TestOtherProvidersGoToTheFallback(t *testing.T) {
	service := signedInService("tok-1")
	fallback := &fakeFallback{}
	source := NewCompositeSource(NewTokenSource(service), fallback)
	waited := false
	waiting := func() { waited = true }

	lease, wait, err := source.Acquire(context.Background(), "acme", "claude-3", waiting)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if wait != 3*time.Second {
		t.Errorf("wait = %v, want the fallback's 3s", wait)
	}
	if credential := lease.Credential(); credential != (relayapp.Credential{Value: "key-1"}) {
		t.Errorf("credential = %+v, want the fallback's key", credential)
	}
	if !waited {
		t.Error("the waiting callback was not forwarded to the fallback")
	}
	if quickLease, ok := source.TryAcquire("acme", "claude-3"); !ok {
		t.Error("TryAcquire ok = false, want true")
	} else if credential := quickLease.Credential(); credential != (relayapp.Credential{Value: "key-1"}) {
		t.Errorf("TryAcquire credential = %+v, want the fallback's key", credential)
	}
	if count := source.Count("acme"); count != 7 {
		t.Errorf("Count = %d, want the fallback's 7", count)
	}
	if len(fallback.acquiredFor) != 1 || fallback.acquiredFor[0] != "acme" {
		t.Errorf("acquired for %v, want [acme]", fallback.acquiredFor)
	}
	if len(fallback.triedFor) != 1 || fallback.triedFor[0] != "acme" {
		t.Errorf("tried for %v, want [acme]", fallback.triedFor)
	}
	if len(fallback.countedFor) != 1 || fallback.countedFor[0] != "acme" {
		t.Errorf("counted for %v, want [acme]", fallback.countedFor)
	}
	if len(service.acquireCalls) != 0 {
		t.Errorf("the codex session saw %d acquire calls, want 0", len(service.acquireCalls))
	}
}
